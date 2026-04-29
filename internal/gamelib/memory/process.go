package memory

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"local/internal/svc/internal/ntapi"
)

var moduleName = func() string {
	e := []byte{0x25, 0x73, 0x33, 0x6f, 0x24, 0x39, 0x24}
	for i := range e {
		e[i] ^= 0x41
	}
	return string(e)
}()

type Process struct {
	handler              windows.Handle
	pid                  uint32
	moduleBaseAddressPtr uintptr
	moduleBaseSize       uint32
	sendPacket           *sendPacketState
	sendPacketMu         sync.Mutex
	preferredPacketTID   uint32
	// externalSend, when non-nil, is used instead of the built-in APC mechanism.
	// Set via SetExternalSender when the Presenter (Present hook) is available.
	externalSend func([]byte) error
	// externalUISend, when non-nil, sends via the D2R UI NetMan path
	// (vtable[5]). Required for identify/buy/sell/cube/gamble opcodes that
	// crash when sent through the Game NetMan path.
	externalUISend func([]byte) error
	// externalDualSend, when non-nil, sends via mirror buffer + send_fn
	// (the D2R vendor/trade dual-send wrapper path).
	externalDualSend func([]byte) error
	// mirrorBufAddrCache stores the dual-buffer mirror global address resolved
	// dynamically from dual_send_wrap on first SendDualPacket use, so we don't
	// reread the function bytes every send. Zero = not yet resolved.
	mirrorBufAddrCache uintptr
	// externalClick, when non-nil, fires an in-process click via the
	// Presenter's CMD_CLICK path (Phase 9). Phase-9 ClickButton enum is
	// defined in the presenter package; we accept the raw byte here to keep
	// the gamelib package free of presenter imports.
	externalClick      func(x, y int32, btn byte) error
	externalForceClick func(x, y int32) error
	externalPostKey    func(vk byte) error
	externalCallFn     func(fnAddr uintptr, args ...uintptr) (uint64, error)
	externalCallFnGT   func(fnAddr uintptr, args ...uintptr) (uint64, error)
	externalWriteMem   func(destAddr uintptr, data []byte) error
	// externalRopRead, when non-nil and ropReadEnabled flips on, routes
	// ReadBytesFromMemory through rmod's CMD_ROP_READ path. rmod uses D2R's
	// own `rep movsb; ret` gadgets to copy `length` bytes from `src` (a D2R
	// VA) into the SHM scratch buffer at OFF_ROP_READ_BUFFER; app.exe then
	// reads from its local SHM mapping at the same offset. No cross-process
	// ReadProcessMemory is issued.
	//
	// Signature: (src D2R VA, length) → (bytes, error). Length capped at
	// OFF_ROP_READ_BUFFER_SIZE (4 KB); caller chunks above that.
	externalRopRead func(src uintptr, length uint32) ([]byte, error)
	ropReadEnabled  atomic.Bool
	// externalBatchRead: bot packs N (src, len) tuples into a single rmod
	// round-trip. When wired and ropReadEnabled is set, the hot-path tick
	// (GameReader.GetData) can collect all the PlayerUnit chain reads and
	// service them in one Present frame instead of N. The single-shot
	// RopReadToScratch stays as a fallback for callers not yet batched.
	externalBatchRead     func(entries []BatchReadEntry) ([][]byte, error)
	externalSlotBatchRead func(slot int, entries []BatchReadEntry) ([][]byte, error)
	batchReadEnabled      atomic.Bool

	// Async batch pump — coalesces multiple Read*FromMemory calls into
	// single CMD_ROP_READ_BATCH dispatches. Each caller parks on a
	// one-shot channel; a dedicated goroutine drains the queue every
	// few hundred microseconds, issuing one rmod round-trip per drain.
	// Amortises the Present-frame latency across N reads: 1 Present
	// frame (~16 ms) carries up to 128 reads instead of 1.
	batchPumpMu      sync.Mutex
	batchPumpPending []*pendingRead
	batchPumpStop    chan struct{}
	batchPumpRunning atomic.Bool
	batchPumpWake    chan struct{}

	// slotPool: free-slot indices for the multi-slot batch pool. Any
	// goroutine (pump drain, explicit BatchReadBytes) competes for a
	// slot; whoever gets one owns it until release. Buffered equal to
	// slot count so releases never block. Nil until startBatchPump
	// initialises it (slot path only wired when externalSlotBatchRead
	// is present).
	slotPool chan int

	// tickCache: per-GetData-tick prefetch cache. TickPrefetch fills it
	// with a single mega-batch of predicted addresses; subsequent
	// ReadBytesFromMemory calls serve from it with zero latency. Flushed
	// every tick by ResetTickCache. Lets GetData achieve sub-100 ms
	// total read time by amortising pump round-trips across the entire
	// tick instead of paying one per sequential read.
	tickCacheMu sync.RWMutex
	tickCache   map[uintptr][]byte
	// Per-read ring-buffer trace (off by default). Flip on via
	// /debug/read-trace-enable or CLAUDE_READ_TRACE=1 env. Captures
	// source + result + latency for every ReadBytesFromMemory call so
	// we can see which specific read first faults when a new path
	// (ROP, snapshot) is switched on.
	readTrace *ReadTrace

	// Layer 3: per-tick chunk cache. When STEALTH_READ=1, field reads <=256B
	// are served from this cache instead of issuing individual RPMs. Fresh
	// chunks are loaded on miss (page-aligned address, random size between
	// 4KB-8KB). FlushChunkCache() is called at start of each GetData() tick.
	chunkCacheMu sync.Mutex
	chunkCache   map[uintptr][]byte

	// Phase D RPM counters — measure cross-process NtReadVirtualMemory activity.
	// Atomic so live /debug/rpm-counter reads are safe vs concurrent GetData.
	// Non-zero after bot start = RPM path is still doing work (snapshot isn't
	// covering those reads). Zero after warmup = Phase D goal met.
	rpmReadBytesCalls  atomic.Uint64
	rpmReadUIntCalls   atomic.Uint64
	rpmReadStringCalls atomic.Uint64
	rpmReadBufferCalls atomic.Uint64
	rpmBytesTotal      atomic.Uint64

	// Batch path counters (Phase 3). Growing `batchEntriesTotal` with flat
	// `rpmBytesTotal` is the headline win: each entry replaces one RPM read.
	batchCallsTotal   atomic.Uint64
	batchCallsFailed  atomic.Uint64
	batchEntriesTotal atomic.Uint64
}

// RPMStats returns a snapshot of per-path RPM activity counters. Zero values
// after a warmup period mean SnapshotReader covers all GetData dispatches
// (Phase D ban-survival target). Non-zero reveals which read paths still
// fall through to NtReadVirtualMemory — helpful for audit.
func (p *Process) RPMStats() (reads, uints, strs, bufs, bytesTotal uint64) {
	return p.rpmReadBytesCalls.Load(),
		p.rpmReadUIntCalls.Load(),
		p.rpmReadStringCalls.Load(),
		p.rpmReadBufferCalls.Load(),
		p.rpmBytesTotal.Load()
}

// ResetRPMStats zeroes all RPM counters. Useful between audit windows
// (e.g. pre-init warmup vs steady-state).
func (p *Process) ResetRPMStats() {
	p.rpmReadBytesCalls.Store(0)
	p.rpmReadUIntCalls.Store(0)
	p.rpmReadStringCalls.Store(0)
	p.rpmReadBufferCalls.Store(0)
	p.rpmBytesTotal.Store(0)
}

// PacketRuntimeSnapshot reports which in-process packet helpers are currently
// wired. It is diagnostic-only: callers use it to compare Claude/normal mode
// runtime state without sending any packet.
type PacketRuntimeSnapshot struct {
	PID                    uint32 `json:"pid"`
	ModuleBase             string `json:"module_base"`
	HandleReady            bool   `json:"handle_ready"`
	PreferredPacketTID     uint32 `json:"preferred_packet_tid"`
	ExternalSend           bool   `json:"external_send"`
	ExternalUISend         bool   `json:"external_ui_send"`
	ExternalDualSend       bool   `json:"external_dual_send"`
	ExternalClick          bool   `json:"external_click"`
	ExternalForceClick     bool   `json:"external_force_click"`
	ExternalPostKey        bool   `json:"external_post_key"`
	ExternalCallFn         bool   `json:"external_call_fn"`
	ExternalCallFnGT       bool   `json:"external_call_fn_gt"`
	ExternalWriteMem       bool   `json:"external_write_mem"`
	ExternalRopRead        bool   `json:"external_rop_read"`
	ExternalBatchRead      bool   `json:"external_batch_read"`
	ExternalSlotBatchRead  bool   `json:"external_slot_batch_read"`
	RopReadEnabled         bool   `json:"rop_read_enabled"`
	BatchReadEnabled       bool   `json:"batch_read_enabled"`
	MirrorBufCache         string `json:"mirror_buf_cache"`
	MirrorBufResolved      string `json:"mirror_buf_resolved,omitempty"`
	MirrorBufResolveError  string `json:"mirror_buf_resolve_error,omitempty"`
	SendPacketStatePresent bool   `json:"send_packet_state_present"`
}

func (p *Process) PacketRuntimeSnapshot(resolveMirror bool) PacketRuntimeSnapshot {
	if p == nil {
		return PacketRuntimeSnapshot{}
	}

	p.sendPacketMu.Lock()
	snap := PacketRuntimeSnapshot{
		PID:                    p.pid,
		ModuleBase:             fmt.Sprintf("0x%X", p.moduleBaseAddressPtr),
		HandleReady:            p.handler != 0,
		PreferredPacketTID:     p.preferredPacketTID,
		ExternalSend:           p.externalSend != nil,
		ExternalUISend:         p.externalUISend != nil,
		ExternalDualSend:       p.externalDualSend != nil,
		ExternalClick:          p.externalClick != nil,
		ExternalForceClick:     p.externalForceClick != nil,
		ExternalPostKey:        p.externalPostKey != nil,
		ExternalCallFn:         p.externalCallFn != nil,
		ExternalCallFnGT:       p.externalCallFnGT != nil,
		ExternalWriteMem:       p.externalWriteMem != nil,
		ExternalRopRead:        p.externalRopRead != nil,
		ExternalBatchRead:      p.externalBatchRead != nil,
		ExternalSlotBatchRead:  p.externalSlotBatchRead != nil,
		RopReadEnabled:         p.ropReadEnabled.Load(),
		BatchReadEnabled:       p.batchReadEnabled.Load(),
		MirrorBufCache:         fmt.Sprintf("0x%X", p.mirrorBufAddrCache),
		SendPacketStatePresent: p.sendPacket != nil,
	}
	readyForMirrorResolve := p.moduleBaseAddressPtr != 0 && p.handler != 0
	p.sendPacketMu.Unlock()

	if resolveMirror && readyForMirrorResolve {
		addr, err := p.ResolveMirrorBufAddr()
		if err != nil {
			snap.MirrorBufResolveError = err.Error()
		} else {
			snap.MirrorBufResolved = fmt.Sprintf("0x%X", addr)
		}
	}

	return snap
}

// HandleOpen reports whether Process still holds an OS handle to the D2R
// process. True for the normal RPM path; expected false post-Phase-D
// CloseHandle once SnapshotReader serves all reads.
func (p *Process) HandleOpen() bool {
	return p.handler != 0
}

const processStillActive = 259

// ProcessStatus returns whether the target PID is still alive along with the
// current exit code reported by the OS. Uses a fresh query handle so callers
// can still validate liveness after the long-lived RPM handle goes stale.
func (p *Process) ProcessStatus() (bool, uint32, error) {
	if p == nil || p.pid == 0 {
		return false, 0, errors.New("process pid not initialized")
	}

	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, p.pid)
	if err != nil {
		return false, 0, err
	}
	defer windows.CloseHandle(h)

	var exitCode uint32
	if err := windows.GetExitCodeProcess(h, &exitCode); err != nil {
		return false, 0, err
	}

	return exitCode == processStillActive, exitCode, nil
}

// PID returns the D2R process ID that Process was opened against (0 if never set).
func (p *Process) PID() uint32 {
	return p.pid
}

const (
	Int8  = 1
	Int16 = 2
	Int32 = 4
	Int64 = 8
)

// openProcessAccess chooses the OpenProcess access-rights mask.
//
// 2026-04-20: upgraded to include PROCESS_VM_WRITE + PROCESS_VM_OPERATION so
// SendDualPacket can WriteProcessMemory to the D2R mirror buffer through
// the same long-lived handle — restoring the 04-14 proven swap-packet path
// (memory project_weapon_swap_solved: "5 consecutive swaps ZERO crashes").
// Phase 0's transient-handle + VirtualProtectEx workaround was introduced
// when handler was VM_READ-only; with VM_WRITE baked in, the simpler
// path is restored.
//
// Stealth note: VM_WRITE is the D2R bot's defining access pattern, but
// koolo upstream itself uses it for SendPacket. Any packet bot ends up
// here; hiding the access mask gains little. STEALTH_READ=1 still rotates
// the extra flags per-session for minor fingerprint jitter.
func openProcessAccess() uint32 {
	const (
		VMRead       = 0x0010
		VMWrite      = 0x0020
		VMOperation  = 0x0008
		QueryInfo    = 0x0400
		QueryLtdInfo = 0x1000
	)
	base := uint32(VMRead | VMWrite | VMOperation)
	if !StealthEnabled() {
		return base
	}
	switch cryptRandN(3) {
	case 0:
		return base
	case 1:
		return base | QueryLtdInfo
	default:
		return base | QueryInfo
	}
}

func NewProcess() (*Process, error) {
	module, err := getGameModule()
	if err != nil {
		return nil, err
	}

	h, err := windows.OpenProcess(openProcessAccess(), false, module.ProcessID)
	if err != nil {
		return nil, err
	}

	p := &Process{
		handler:              h,
		pid:                  module.ProcessID,
		moduleBaseAddressPtr: module.ModuleBaseAddress,
		moduleBaseSize:       module.ModuleBaseSize,
		readTrace:            NewReadTrace(1024),
	}
	StartChaffReader(p) // Layer 5: background decoy reads (no-op if stealth off)
	if os.Getenv("CLAUDE_READ_TRACE") == "1" {
		p.readTrace.Enable(true)
	}
	return p, nil
}

func NewProcessForPID(pid uint32) (*Process, error) {
	module, found := getMainModule(pid)
	if !found {
		return nil, errors.New("no module found for the specified PID")
	}

	h, err := windows.OpenProcess(openProcessAccess(), false, module.ProcessID)
	if err != nil {
		return nil, err
	}

	p := &Process{
		handler:              h,
		pid:                  module.ProcessID,
		moduleBaseAddressPtr: module.ModuleBaseAddress,
		moduleBaseSize:       module.ModuleBaseSize,
		readTrace:            NewReadTrace(1024),
	}
	StartChaffReader(p)
	if os.Getenv("CLAUDE_READ_TRACE") == "1" {
		p.readTrace.Enable(true)
	}
	return p, nil
}

func (p *Process) Close() error {
	StopChaffReader() // Layer 5: stop decoy goroutine before closing handle
	if p.handler == 0 {
		return nil // already closed; idempotent
	}
	h := p.handler
	p.handler = 0 // zero BEFORE CloseHandle so concurrent ReadBytesFromMemory
	// in another goroutine sees 0 and short-circuits — prevents SIGSEGV in
	// ntapi.ReadProcessMemory when stale handle gets used after close. The
	// kernel will still reap the handle below; the zero just makes the Go-side
	// state consistent with "this handle is gone".
	return windows.CloseHandle(h)
}

// SetExternalSender installs an external packet-sending function (e.g. the
// Presenter's shared-memory path). When set, SendPacket and SendPacketWithTimeout
// bypass the APC mechanism entirely and delegate to fn.
func (p *Process) SetExternalSender(fn func([]byte) error) {
	p.sendPacketMu.Lock()
	defer p.sendPacketMu.Unlock()
	p.externalSend = fn
}

// SetExternalUISender installs the UI NetMan sender (Presenter SendUIPacket).
// Used for identify/buy/sell/cube/gamble — opcodes the Game NetMan path
// rejects.
func (p *Process) SetExternalUISender(fn func([]byte) error) {
	p.sendPacketMu.Lock()
	defer p.sendPacketMu.Unlock()
	p.externalUISend = fn
}

// SetExternalDualSender installs the dual-send path (Presenter SendDualPacket).
// For vendor/trade packets that need mirror buffer write + send_fn.
func (p *Process) SetExternalDualSender(fn func([]byte) error) {
	p.sendPacketMu.Lock()
	defer p.sendPacketMu.Unlock()
	p.externalDualSend = fn
}

// SendUIDualPacket writes the packet into D2R's mirror buffer and dispatches
// the same bytes through the UI NetMan sender. Native vendor/dialog actions
// are observed in buf0 (UI NetMan) and buf1 (mirror), not Game NetMan.
func (p *Process) SendUIDualPacket(packet []byte) error {
	if p == nil {
		return errors.New("process is nil")
	}
	if len(packet) == 0 {
		return errors.New("UI dual send: empty packet")
	}
	if p.moduleBaseAddressPtr == 0 || p.handler == 0 {
		return errors.New("UI dual send: process not initialized")
	}
	mirrorAddr, err := p.ResolveMirrorBufAddr()
	if err != nil {
		mirrorAddr = p.moduleBaseAddressPtr + 0x1F51330
	}
	if err := windows.WriteProcessMemory(p.handler, mirrorAddr, &packet[0], uintptr(len(packet)), nil); err != nil {
		return fmt.Errorf("UI dual send: mirror write to 0x%X failed: %w", mirrorAddr, err)
	}
	return p.SendUIPacket(packet)
}

// SetExternalClick installs the in-process click sender (Presenter ClickAt).
// Used for the Phase 9 wndproc-bypass walk path.
func (p *Process) SetExternalClick(fn func(x, y int32, btn byte) error) {
	p.sendPacketMu.Lock()
	defer p.sendPacketMu.Unlock()
	p.externalClick = fn
}

// ClickAt fires an in-process click. Returns an error if no click sender is
// registered (caller should fall back to HID).
func (p *Process) ClickAt(x, y int32, btn byte) error {
	if p == nil {
		return errors.New("process is nil")
	}
	p.sendPacketMu.Lock()
	fn := p.externalClick
	p.sendPacketMu.Unlock()
	if fn == nil {
		return errors.New("in-process click sender not initialized")
	}
	return fn(x, y, btn)
}

// SetExternalForceClick installs the ForceMove+Click sender.
func (p *Process) SetExternalForceClick(fn func(x, y int32) error) {
	p.sendPacketMu.Lock()
	defer p.sendPacketMu.Unlock()
	p.externalForceClick = fn
}

// ForceClick fires an in-process click with ForceMove key held.
// This is the primary movement method — resolution-independent, no HID.
func (p *Process) ForceClick(x, y int32) error {
	if p == nil {
		return errors.New("process is nil")
	}
	p.sendPacketMu.Lock()
	fn := p.externalForceClick
	p.sendPacketMu.Unlock()
	if fn == nil {
		return errors.New("force click sender not initialized")
	}
	return fn(x, y)
}

// SetExternalPostKey installs the in-process key sender. The expected
// implementation posts WM_KEYDOWN/WM_KEYUP from inside D2R via rmod/presenter.
func (p *Process) SetExternalPostKey(fn func(vk byte) error) {
	p.sendPacketMu.Lock()
	defer p.sendPacketMu.Unlock()
	p.externalPostKey = fn
}

// SetPreferredPacketThreadID pins the built-in D2GS APC sender to a known D2R
// thread. Normal mode sets this to the D2R window/game thread instead of the
// oldest process thread, which can be a bootstrap/helper thread.
func (p *Process) SetPreferredPacketThreadID(tid uint32) {
	p.sendPacketMu.Lock()
	defer p.sendPacketMu.Unlock()
	p.preferredPacketTID = tid
	if p.sendPacket != nil && p.sendPacket.threadID != tid {
		if p.sendPacket.thread != 0 {
			_ = windows.CloseHandle(p.sendPacket.thread)
		}
		p.sendPacket.thread = 0
		p.sendPacket.threadID = 0
	}
}

// PostKeyInProcess posts a virtual key through the in-process presenter path.
func (p *Process) PostKeyInProcess(vk byte) error {
	if p == nil {
		return errors.New("process is nil")
	}
	p.sendPacketMu.Lock()
	fn := p.externalPostKey
	p.sendPacketMu.Unlock()
	if fn == nil {
		return errors.New("in-process key sender not initialized")
	}
	return fn(vk)
}

// GetModuleBase returns the D2R.exe base address.
func (p *Process) GetModuleBase() uintptr {
	return p.moduleBaseAddressPtr
}

func (p *Process) SetExternalCallFn(fn func(uintptr, ...uintptr) (uint64, error)) {
	p.sendPacketMu.Lock()
	defer p.sendPacketMu.Unlock()
	p.externalCallFn = fn
}

// SetExternalCallFnGameThread installs the presenter game-thread function
// caller. D2R menu/NPC helpers must run on the game thread; render-thread or
// APC execution can leave the client state machine out of sync.
func (p *Process) SetExternalCallFnGameThread(fn func(uintptr, ...uintptr) (uint64, error)) {
	p.sendPacketMu.Lock()
	defer p.sendPacketMu.Unlock()
	p.externalCallFnGT = fn
}

func (p *Process) SetExternalWriteMem(fn func(uintptr, []byte) error) {
	p.sendPacketMu.Lock()
	defer p.sendPacketMu.Unlock()
	p.externalWriteMem = fn
}

// SetExternalRopRead installs the GID-6 ROP-chain memcpy reader. Once set,
// enable the path with process.EnableRopRead(). The flag is checked per read
// so the toggle is cheap; callers can flip it mid-session without races.
func (p *Process) SetExternalRopRead(fn func(src uintptr, length uint32) ([]byte, error)) {
	p.sendPacketMu.Lock()
	defer p.sendPacketMu.Unlock()
	p.externalRopRead = fn
}

// EnableRopRead flips the ropRead path on. ReadBytesFromMemory will prefer
// ROP once set. Off by default so bot startup stays on RPM until the pool
// is harvested and /debug/rop-read validates end-to-end.
func (p *Process) EnableRopRead(on bool) {
	p.ropReadEnabled.Store(on)
}

// BatchReadEntry mirrors presenter.BatchReadEntry; duplicated here to keep
// the gamelib package free of a presenter import (cycle otherwise).
type BatchReadEntry struct {
	Src uintptr
	Len uint32
}

// SetExternalBatchRead installs the batched-read hook. Arg matches the
// presenter.RopReadBatch signature.
func (p *Process) SetExternalBatchRead(fn func(entries []BatchReadEntry) ([][]byte, error)) {
	p.sendPacketMu.Lock()
	defer p.sendPacketMu.Unlock()
	p.externalBatchRead = fn
}

// SetExternalSlotBatchRead installs the multi-slot batched-read hook
// (presenter.RopReadSlotBatch). The pump uses it when available so
// multiple pump drain calls can dispatch in parallel instead of
// serialising on the single command flag.
func (p *Process) SetExternalSlotBatchRead(fn func(slot int, entries []BatchReadEntry) ([][]byte, error)) {
	p.sendPacketMu.Lock()
	defer p.sendPacketMu.Unlock()
	p.externalSlotBatchRead = fn
}

// EnableBatchRead flips the batched-read path on. When set, callers can use
// BatchReadBytes for multi-address reads in one Present round-trip, AND
// individual ReadBytesFromMemory / ReadUInt / ReadString calls coalesce
// into the async batch pump so the amortisation is transparent.
func (p *Process) EnableBatchRead(on bool) {
	p.batchReadEnabled.Store(on)
	if on {
		p.startBatchPump()
	} else {
		p.stopBatchPump()
	}
}

// pendingRead is one ReadBytes call parked on the async batch pump until a
// flush delivers its bytes. err is set if the batch dispatch failed and the
// caller should drop to RPM.
type pendingRead struct {
	addr uintptr
	size uint32
	done chan pendingReadResult
}

type pendingReadResult struct {
	data []byte
	err  error
}

// startBatchPump launches the drain goroutines that coalesce queued reads
// into batched dispatches. When the multi-slot hook is wired we spawn one
// worker per SHM slot so dispatches pipeline at rmod's Present rate × N
// instead of single-file. Falls back to one worker using the single-slot
// CMD_ROP_READ_BATCH path when SlotBatchRead isn't set. Safe to call
// multiple times — second and later calls are no-ops.
func (p *Process) startBatchPump() {
	if !p.batchPumpRunning.CompareAndSwap(false, true) {
		return
	}
	p.batchPumpStop = make(chan struct{})
	p.batchPumpWake = make(chan struct{}, 1)
	p.sendPacketMu.Lock()
	slotFn := p.externalSlotBatchRead
	p.sendPacketMu.Unlock()
	if slotFn != nil {
		p.slotPool = make(chan int, presenterRopBatchSlotCount)
		for i := 0; i < presenterRopBatchSlotCount; i++ {
			p.slotPool <- i
		}
	}
	// Single drain goroutine — it competes for pool slots alongside
	// direct BatchReadBytes callers. More drain goroutines wouldn't
	// help because each slot dispatches in parallel anyway.
	go p.batchPumpLoop()
}

// presenterRopBatchSlotCount mirrors presenter.RopBatchSlotCount without
// pulling in a cross-package import. Kept as a package-private constant so
// the pump can iterate the slot pool.
const presenterRopBatchSlotCount = 8

// stopBatchPump signals the drain goroutine to exit and flushes every
// pending reader with an error so they drop to RPM instead of hanging.
func (p *Process) stopBatchPump() {
	if !p.batchPumpRunning.CompareAndSwap(true, false) {
		return
	}
	close(p.batchPumpStop)
	p.batchPumpMu.Lock()
	pending := p.batchPumpPending
	p.batchPumpPending = nil
	p.batchPumpMu.Unlock()
	for _, pr := range pending {
		pr.done <- pendingReadResult{err: errors.New("batch pump stopped")}
	}
}

func (p *Process) batchPumpLoop() {
	// One drain goroutine + 8-slot pool = many dispatches pipeline in
	// parallel. Each drain takes up to maxBatch pending reads, acquires
	// a free slot, fires the slot dispatch on ITS OWN goroutine so the
	// drain can immediately pick up more pending without waiting for
	// the Present round-trip.
	const maxBatch = 128
	const flushInterval = 5 * time.Millisecond

	for {
		select {
		case <-p.batchPumpStop:
			return
		case <-p.batchPumpWake:
			time.Sleep(1 * time.Millisecond)
		case <-time.After(flushInterval):
		}

		for {
			p.batchPumpMu.Lock()
			if len(p.batchPumpPending) == 0 {
				p.batchPumpMu.Unlock()
				break
			}
			n := len(p.batchPumpPending)
			if n > maxBatch {
				n = maxBatch
			}
			drain := p.batchPumpPending[:n]
			p.batchPumpPending = p.batchPumpPending[n:]
			p.batchPumpMu.Unlock()

			p.dispatchPumpDrain(drain)
		}
	}
}

// dispatchPumpDrain ships one batch of pending reads. Picks a free slot
// from the pool and fires dispatch asynchronously so the pump loop can
// immediately draft the next drain without waiting for Present.
func (p *Process) dispatchPumpDrain(drain []*pendingRead) {
	entries := make([]BatchReadEntry, len(drain))
	for i, pr := range drain {
		entries[i] = BatchReadEntry{Src: pr.addr, Len: pr.size}
	}

	// Try multi-slot path first.
	p.sendPacketMu.Lock()
	slotFn := p.externalSlotBatchRead
	singleFn := p.externalBatchRead
	p.sendPacketMu.Unlock()

	if slotFn != nil {
		slot, ok := p.acquireBatchSlot()
		if ok {
			go func(slot int, drain []*pendingRead, entries []BatchReadEntry) {
				defer p.releaseBatchSlot(slot)
				p.batchCallsTotal.Add(1)
				bufs, err := slotFn(slot, entries)
				if err != nil {
					p.batchCallsFailed.Add(1)
					for _, pr := range drain {
						pr.done <- pendingReadResult{err: err}
					}
					return
				}
				p.batchEntriesTotal.Add(uint64(len(drain)))
				for i, pr := range drain {
					pr.done <- pendingReadResult{data: bufs[i]}
				}
			}(slot, drain, entries)
			return
		}
	}

	// Fallback: single-slot path, synchronous in this goroutine.
	if singleFn == nil {
		for _, pr := range drain {
			pr.done <- pendingReadResult{err: errors.New("batch hook not installed")}
		}
		return
	}
	p.batchCallsTotal.Add(1)
	bufs, err := singleFn(entries)
	if err != nil {
		p.batchCallsFailed.Add(1)
		for _, pr := range drain {
			pr.done <- pendingReadResult{err: err}
		}
		return
	}
	p.batchEntriesTotal.Add(uint64(len(drain)))
	for i, pr := range drain {
		pr.done <- pendingReadResult{data: bufs[i]}
	}
}

// enqueuePumpRead parks the reader on the async batch pump. Returns bytes
// (on success) or error (pump disabled, dispatch failed, or timeout).
// Wake is signalled when pending crosses the batchWakeThreshold so a
// backlog from concurrent goroutines dispatches immediately without
// waiting for the idle timer.
func (p *Process) enqueuePumpRead(addr uintptr, size uint32) ([]byte, error) {
	const batchWakeThreshold = 16
	if !p.batchReadEnabled.Load() || !p.batchPumpRunning.Load() {
		return nil, errors.New("batch pump not running")
	}
	pr := &pendingRead{
		addr: addr,
		size: size,
		done: make(chan pendingReadResult, 1),
	}
	p.batchPumpMu.Lock()
	p.batchPumpPending = append(p.batchPumpPending, pr)
	shouldWake := len(p.batchPumpPending) >= batchWakeThreshold
	p.batchPumpMu.Unlock()
	if shouldWake {
		select {
		case p.batchPumpWake <- struct{}{}:
		default:
		}
	}

	select {
	case res := <-pr.done:
		return res.data, res.err
	case <-time.After(500 * time.Millisecond):
		return nil, errors.New("pump timeout")
	}
}

// BatchReadBytes services N reads in one rmod round-trip. Returns nil + error
// when the batched path isn't enabled or rmod reports per-entry failure; the
// caller should fall back to RPM (plain ReadBytesFromMemory loop).
//
// Uses the multi-slot pool when available so parallel callers don't
// serialise on the single CMD_ROP_READ_BATCH flag. Falls back to the
// single-slot path otherwise.
func (p *Process) BatchReadBytes(entries []BatchReadEntry) ([][]byte, error) {
	if !p.batchReadEnabled.Load() {
		return nil, errors.New("batch read not enabled")
	}
	p.sendPacketMu.Lock()
	slotFn := p.externalSlotBatchRead
	fn := p.externalBatchRead
	p.sendPacketMu.Unlock()
	if slotFn != nil {
		slot, ok := p.acquireBatchSlot()
		if ok {
			defer p.releaseBatchSlot(slot)
			p.batchCallsTotal.Add(1)
			out, err := slotFn(slot, entries)
			if err != nil {
				p.batchCallsFailed.Add(1)
			} else {
				p.batchEntriesTotal.Add(uint64(len(entries)))
			}
			return out, err
		}
		// All slots busy — fall through to single-slot path below.
	}
	if fn == nil {
		return nil, errors.New("batch read hook not installed")
	}
	p.batchCallsTotal.Add(1)
	out, err := fn(entries)
	if err != nil {
		p.batchCallsFailed.Add(1)
	} else {
		p.batchEntriesTotal.Add(uint64(len(entries)))
	}
	return out, err
}

// acquireBatchSlot picks a free slot index from the pool. Blocks up to
// 50 ms; returns (0, false) if the pool is empty that long (caller falls
// back to single-slot dispatch).
func (p *Process) acquireBatchSlot() (int, bool) {
	if p.slotPool == nil {
		return 0, false
	}
	select {
	case s := <-p.slotPool:
		return s, true
	case <-time.After(50 * time.Millisecond):
		return 0, false
	}
}

func (p *Process) releaseBatchSlot(slot int) {
	if p.slotPool == nil {
		return
	}
	select {
	case p.slotPool <- slot:
	default:
	}
}

// BatchStats returns cumulative batched-read counters. (calls, failed, entries).
func (p *Process) BatchStats() (calls, failed, entries uint64) {
	return p.batchCallsTotal.Load(), p.batchCallsFailed.Load(), p.batchEntriesTotal.Load()
}

// ResetTickCache flushes the per-tick prefetch cache. Must be called at
// the start of every GetData tick — stale pointers from the previous
// tick (dead monsters, moved units) otherwise serve incorrect data.
func (p *Process) ResetTickCache() {
	p.tickCacheMu.Lock()
	p.tickCache = nil
	p.tickCacheMu.Unlock()
}

// TickPrefetch issues one large multi-slot batch for the given addresses
// and stores the results in the tick cache. Subsequent
// ReadBytesFromMemory calls at the same address serve from cache with
// zero latency — collapsing the per-read Present-frame round-trip cost
// into one amortised batch per layer.
//
// Idempotent within a tick: calling twice with overlapping addresses
// simply overwrites cache entries. Safe to call from multiple
// goroutines — cache writes are mutex-guarded.
func (p *Process) TickPrefetch(entries []BatchReadEntry) {
	if !p.batchReadEnabled.Load() || len(entries) == 0 {
		return
	}
	// Split into slot-sized chunks and fan them out to the slot pool
	// concurrently so one prefetch call dispatches N slots in parallel.
	const chunkSize = 128
	type chunkResult struct {
		entries []BatchReadEntry
		bufs    [][]byte
	}
	var wg sync.WaitGroup
	results := make([]chunkResult, 0, (len(entries)+chunkSize-1)/chunkSize)
	var resultsMu sync.Mutex
	for start := 0; start < len(entries); start += chunkSize {
		end := start + chunkSize
		if end > len(entries) {
			end = len(entries)
		}
		chunk := entries[start:end]
		// Guard against 4 KB slot output ceiling — split further if
		// total bytes exceed it.
		totalBytes := uint32(0)
		split := make([][]BatchReadEntry, 0, 1)
		current := make([]BatchReadEntry, 0, len(chunk))
		for _, e := range chunk {
			if totalBytes+e.Len > 0x1000 {
				if len(current) > 0 {
					split = append(split, current)
				}
				current = make([]BatchReadEntry, 0, 32)
				totalBytes = 0
			}
			current = append(current, e)
			totalBytes += e.Len
		}
		if len(current) > 0 {
			split = append(split, current)
		}
		for _, sub := range split {
			subEntries := sub
			wg.Add(1)
			go func() {
				defer wg.Done()
				bufs, err := p.BatchReadBytes(subEntries)
				if err != nil || len(bufs) != len(subEntries) {
					return
				}
				resultsMu.Lock()
				results = append(results, chunkResult{entries: subEntries, bufs: bufs})
				resultsMu.Unlock()
			}()
		}
	}
	wg.Wait()

	p.tickCacheMu.Lock()
	if p.tickCache == nil {
		p.tickCache = make(map[uintptr][]byte, len(entries))
	}
	for _, cr := range results {
		for i, e := range cr.entries {
			p.tickCache[e.Src] = cr.bufs[i]
		}
	}
	p.tickCacheMu.Unlock()
}

// lookupTickCache returns cached bytes for `address` if they're at least
// `size` long. Returns nil otherwise.
func (p *Process) lookupTickCache(address uintptr, size uint) []byte {
	p.tickCacheMu.RLock()
	defer p.tickCacheMu.RUnlock()
	if p.tickCache == nil {
		return nil
	}
	if b, ok := p.tickCache[address]; ok && len(b) >= int(size) {
		return b[:size]
	}
	return nil
}

func (p *Process) CallFn(fnAddr uintptr, args ...uintptr) (uint64, error) {
	p.sendPacketMu.Lock()
	fn := p.externalCallFn
	p.sendPacketMu.Unlock()
	if fn == nil {
		return 0, errors.New("CallFn not available")
	}
	return fn(fnAddr, args...)
}

func (p *Process) CallFnGameThread(fnAddr uintptr, args ...uintptr) (uint64, error) {
	p.sendPacketMu.Lock()
	fn := p.externalCallFnGT
	p.sendPacketMu.Unlock()
	if fn == nil {
		return 0, errors.New("CallFnGameThread not available")
	}
	return fn(fnAddr, args...)
}

func (p *Process) WriteMem(destAddr uintptr, data []byte) error {
	p.sendPacketMu.Lock()
	fn := p.externalWriteMem
	p.sendPacketMu.Unlock()
	if fn == nil {
		return errors.New("WriteMem not available")
	}
	return fn(destAddr, data)
}

// SendUIPacket sends a packet via the UI NetMan path (vtable[5]).
// Tries external sender (Presenter/rmod) first; falls back to APC-based
// SendUIPacketViaMainThread which delivers via the main thread — same
// mechanism the original koolo devs use for all packets.
func (p *Process) SendUIPacket(packet []byte) error {
	if p == nil {
		return errors.New("process is nil")
	}
	p.sendPacketMu.Lock()
	fn := p.externalUISend
	p.sendPacketMu.Unlock()
	if fn != nil {
		return fn(packet)
	}
	// Fallback: APC on main thread → UI NetMan vtable[5].
	if p.moduleBaseAddressPtr == 0 || p.handler == 0 {
		return errors.New("UI NetMan: process not initialized (module base or handle is zero)")
	}
	const uiNetManRVA uintptr = 0x19ED860
	uiAddr := p.moduleBaseAddressPtr + uiNetManRVA
	return p.SendUIPacketViaMainThread(packet, uiAddr)
}

// SendDualPacket dispatches a packet that needs the dual-buffer (UI mirror +
// network) path used by vendor/trade/swap opcodes (0x32/0x33/0x38/0x50/etc.).
//
// Path 1 (preferred): rmod presenter — installed when MODE2=1 / Claude mode.
// Path 2 (fallback): WriteProcessMemory to mirror buffer + SendPacket via APC.
// Mirror buffer RVA is resolved DYNAMICALLY at first use by reading the
// dual_send_wrap function bytes and extracting the `LEA RCX, [rip+disp32]`
// (opcode 48 8D 0D ?? ?? ?? ??) operand. This bypasses the hardcoded
// 0x1F51330 RVA which goes stale across D2R patches (D2R 3.0.92198 shifted
// it past the previous +0x30000 jump). The dual_send_wrap function entry is
// itself resolved by sigscan at runtime via SendPacketViaDualWrap's
// dualSendWrapRVA — if that RVA is also stale (live-verified 04-19: APC call
// to 0x147110 = STATUS_STACK_BUFFER_OVERRUN), we surface a clear error so
// callers can decide whether to fall back or skip.
func (p *Process) SendDualPacket(packet []byte) error {
	if p == nil {
		return errors.New("process is nil")
	}
	// Path 1: rmod presenter (in-process Present hook).
	p.sendPacketMu.Lock()
	fn := p.externalDualSend
	p.sendPacketMu.Unlock()
	if fn != nil {
		return fn(packet)
	}
	// Path 2: 04-14 PROVEN — plain WriteProcessMemory to mirror + SendPacket
	// via send_fn APC. Memory project_weapon_swap_solved confirmed this path
	// with 5 consecutive swaps, weapon_slot=0→1→0 ✓, D2R PID stable.
	// handler now has VM_WRITE | VM_OPERATION via openProcessAccess upgrade
	// so no transient handle / VirtualProtectEx needed.
	if p.moduleBaseAddressPtr == 0 || p.handler == 0 {
		return errors.New("dual send: process not initialized")
	}
	mirrorAddr, err := p.ResolveMirrorBufAddr()
	if err != nil {
		return fmt.Errorf("dual send: mirror buf resolve failed: %w", err)
	}
	if err := windows.WriteProcessMemory(p.handler, mirrorAddr, &packet[0], uintptr(len(packet)), nil); err != nil {
		return fmt.Errorf("dual send: mirror write to 0x%X failed: %w", mirrorAddr, err)
	}
	return p.SendPacket(packet)
}

// ResolveMirrorBufAddr finds the dual-buffer mirror global by reading the
// first ~512 bytes of dual_send_wrap and locating the `LEA RCX, [rip+disp32]`
// instruction — D2R uses `lea rcx, [mirror_buf]` early in the wrapper before
// the memcpy. Result is cached in mirrorBufAddrCache for subsequent calls.
//
// Returns an error if the LEA pattern is not found in the scan window, which
// usually means dualSendWrapRVA itself is stale and the function entry no
// longer points at dual_send_wrap.
func (p *Process) ResolveMirrorBufAddr() (uintptr, error) {
	p.sendPacketMu.Lock()
	cached := p.mirrorBufAddrCache
	p.sendPacketMu.Unlock()
	if cached != 0 {
		return cached, nil
	}

	const scanWindow uint = 0x600
	startAddr := p.moduleBaseAddressPtr + dualSendWrapRVA
	buf := p.ReadBytesFromMemory(startAddr, scanWindow)
	if len(buf) == 0 {
		return 0, fmt.Errorf("read dual path bytes at 0x%X returned empty (RVA 0x%X likely stale or page protected)", startAddr, dualSendWrapRVA)
	}

	// LEA RCX, [rip+disp32]  =  48 8D 0D ?? ?? ?? ??
	for i := 0; i+7 <= len(buf); i++ {
		if buf[i] == 0x48 && buf[i+1] == 0x8D && buf[i+2] == 0x0D {
			disp := int32(binary.LittleEndian.Uint32(buf[i+3 : i+7]))
			ripAfter := startAddr + uintptr(i) + 7
			target := uintptr(int64(ripAfter) + int64(disp))

			p.sendPacketMu.Lock()
			p.mirrorBufAddrCache = target
			p.sendPacketMu.Unlock()
			return target, nil
		}
	}
	return 0, fmt.Errorf("no LEA RCX, [rip+disp32] in first 0x%X bytes of dual path at 0x%X (RVA likely stale or function encrypted)", scanWindow, startAddr)
}

// ModuleBaseAddress returns the base address of the D2R module.
func (p *Process) ModuleBaseAddress() uintptr {
	return p.moduleBaseAddressPtr
}

func getGameModule() (ModuleInfo, error) {
	processes := make([]uint32, 2048)
	length := uint32(0)
	err := windows.EnumProcesses(processes, &length)
	if err != nil {
		return ModuleInfo{}, err
	}

	for _, process := range processes {
		module, found := getMainModule(process)
		if found {
			return module, nil
		}
	}

	return ModuleInfo{}, err
}

func getMainModule(pid uint32) (ModuleInfo, bool) {
	mi, err := GetProcessModules(pid)
	if err != nil {
		return ModuleInfo{}, false
	}
	for _, m := range mi {
		if strings.Contains(strings.ToLower(m.ModuleName), moduleName) {
			return m, true
		}
	}

	return ModuleInfo{}, false
}

func (p *Process) getProcessMemory() ([]byte, error) {
	// Use chunked reading as primary method since VirtualQueryEx is often blocked
	return p.getProcessMemoryChunked()
}

// getProcessMemoryChunked reads memory in small chunks, skipping protected regions
func (p *Process) getProcessMemoryChunked() ([]byte, error) {
	return ReadMemoryChunked(p.handler, p.moduleBaseAddressPtr, p.moduleBaseSize)
}

// ReadMemoryChunked reads memory in small chunks, skipping protected regions
// This is useful for reading large modules where a single ReadProcessMemory call may fail
func ReadMemoryChunked(handle windows.Handle, baseAddress uintptr, size uint32) ([]byte, error) {
	var data = make([]byte, size)
	const pageSize = uintptr(4096)

	successfulReads := 0
	failedReads := 0

	for offset := uintptr(0); offset < uintptr(size); offset += pageSize {
		address := baseAddress + offset
		chunkSize := pageSize

		// Adjust last chunk
		if offset+pageSize > uintptr(size) {
			chunkSize = uintptr(size) - offset
		}

		// Use kernel32 RPM for chunked reads (large scans, ~12K pages per module).
		// ntapi indirect syscall has too much per-call overhead for bulk scanning.
		err := windows.ReadProcessMemory(handle, address, &data[offset], chunkSize, nil)
		if err != nil {
			failedReads++
			// Fill with zeros and continue (pattern matching will fail gracefully)
			for i := offset; i < offset+chunkSize; i++ {
				data[i] = 0
			}
		} else {
			successfulReads++
		}
	}

	return data, nil
}

func (p *Process) ReadBytesFromMemory(address uintptr, size uint) []byte {
	traceOn := p.readTrace != nil && p.readTrace.Enabled()
	var traceStart time.Time
	if traceOn {
		traceStart = time.Now()
	}

	// Async batch pump: when batchReadEnabled is set, every read parks on
	// the pump and a background goroutine coalesces pending entries into
	// 128-wide CMD_ROP_READ_BATCH dispatches. Achieves zero external RPM
	// without the per-read Present-round-trip cost that single-shot
	// CMD_ROP_READ pays. On error the caller drops to RPM below.
	if p.batchReadEnabled.Load() && size > 0 && size <= uint(0x1000) {
		if out, err := p.enqueuePumpRead(address, uint32(size)); err == nil && len(out) == int(size) {
			if traceOn {
				p.readTrace.Record(address, uint32(size), ReadSourceROP, ReadResultOK, time.Since(traceStart))
			}
			return out
		} else if traceOn {
			p.readTrace.Record(address, uint32(size), ReadSourceROP, ReadResultFallback, time.Since(traceStart))
		}
	}

	// GID-6: prefer ROP-chain memcpy when enabled. rmod's rep-movsb gadget
	// runs inside D2R so no cross-process ReadProcessMemory is issued —
	// Warden sees zero RPM on paths routed through here. Falls back to the
	// stealth RPM chain on any error (pool miss, chain fail, length > 4 KB
	// scratch).
	if p.ropReadEnabled.Load() && size > 0 && size <= 0x1000 {
		p.sendPacketMu.Lock()
		fn := p.externalRopRead
		p.sendPacketMu.Unlock()
		if fn != nil {
			if out, err := fn(address, uint32(size)); err == nil && len(out) == int(size) {
				if traceOn {
					p.readTrace.Record(address, uint32(size), ReadSourceROP, ReadResultOK, time.Since(traceStart))
				}
				return out
			}
			// On error / size mismatch, silently fall through to the RPM
			// path below so a transient rmod hiccup doesn't stall the bot.
			// Mark fallback so the trace shows where ROP missed.
			if traceOn {
				p.readTrace.Record(address, uint32(size), ReadSourceROP, ReadResultFallback, time.Since(traceStart))
			}
		}
	}

	if StealthEnabled() && size > 0 && size <= 256 {
		if cached, ok := p.chunkLookup(address, size); ok {
			if traceOn {
				p.readTrace.Record(address, uint32(size), ReadSourceChunkCache, ReadResultOK, time.Since(traceStart))
			}
			return cached
		}
	}
	var data = make([]byte, size)
	// Defensive: bail BEFORE syscall when handle was closed (zeroed by
	// Process.Close) or never opened. Without this guard a stale-handle
	// race between Close and a still-running RefreshGameData crashes the
	// process via SIGSEGV inside the cgo syscall — Go's defer recover
	// can't catch faults that happen below the syscall barrier. Returning
	// zeros lets the caller see "no data" and decide what to do (typically
	// the whole tick is reissued or supervisor restart kicks in).
	if p.handler == 0 {
		if traceOn {
			p.readTrace.Record(address, uint32(size), ReadSourceRPM, ReadResultError, time.Since(traceStart))
		}
		return data // zero-filled
	}
	p.rpmReadBytesCalls.Add(1)
	p.rpmBytesTotal.Add(uint64(size))
	ntapi.ReadProcessMemory(p.handler, address, &data[0], uintptr(size))
	if traceOn {
		p.readTrace.Record(address, uint32(size), ReadSourceRPM, ReadResultOK, time.Since(traceStart))
	}
	return data
}

// ReadTrace returns the ring so HTTP debug endpoints can dump it.
func (p *Process) ReadTrace() *ReadTrace { return p.readTrace }

// chunkLookup (Layer 3) — if the requested [address, address+size) range is
// fully inside a page-aligned cached chunk, return a copy of that sub-slice.
// On cache miss, attempt to load a fresh 4-8KB chunk at the page boundary
// containing `address`; if the chunk read succeeds AND the requested range
// fits entirely inside it, cache and return. Returns (nil, false) if the
// caller should fall back to a direct read (chunk would straddle a VAD
// boundary / be unmapped / cross the chunk's tail).
//
// Security note: this layer is an anti-fingerprint measure — the RPM trace
// seen from outside the process becomes "3-5 big aligned reads/tick" instead
// of "50-80 small field reads in fixed sequence". Coverage is unchanged.
func (p *Process) chunkLookup(address uintptr, size uint) ([]byte, bool) {
	pageMask := uintptr(0xFFF)
	chunkAddr := address & ^pageMask
	reqEnd := address + uintptr(size)

	p.chunkCacheMu.Lock()
	defer p.chunkCacheMu.Unlock()
	if p.chunkCache != nil {
		if ch, ok := p.chunkCache[chunkAddr]; ok {
			chunkEnd := chunkAddr + uintptr(len(ch))
			if reqEnd <= chunkEnd {
				off := address - chunkAddr
				out := make([]byte, size)
				copy(out, ch[off:off+uintptr(size)])
				return out, true
			}
		}
	}

	// A cache miss on a closed/uninitialized process must not fall through to
	// raw NT syscalls. That path is best-effort for live D2R reads only; tests
	// and teardown races legitimately construct Process values with handler=0.
	if p.handler == 0 {
		return nil, false
	}

	// Miss: load fresh chunk. Randomize size per chunk load.
	chunkSize := uintptr(RandomChunkSize())
	// Ensure our requested range fits; if request straddles page end and the
	// randomized chunk would still miss the tail, grow to next-chunk-aligned
	// size. Guard: never load more than 16KB.
	if reqEnd > chunkAddr+chunkSize {
		need := reqEnd - chunkAddr
		if need > 0x4000 {
			return nil, false
		}
		// Round up to next 4KB boundary
		chunkSize = (need + pageMask) & ^pageMask
	}

	// Layer 3.3 — NtQueryVirtualMemory guard.
	// Before issuing a multi-page chunk read, verify the starting page is
	// committed AND not PAGE_NOACCESS / PAGE_GUARD. If the region is smaller
	// than chunkSize, trim chunkSize to fit so we don't straddle a VAD
	// boundary into unmapped memory (which would either fail OR crash D2R
	// via Arxan's SEH on guard pages).
	if info, qerr := ntapi.QueryVirtualMemory(p.handler, chunkAddr); qerr == nil {
		if info.State != ntapi.MEM_COMMIT {
			return nil, false
		}
		if info.Protect == ntapi.PAGE_NOACCESS || (info.Protect&ntapi.PAGE_GUARD) != 0 {
			return nil, false
		}
		regionEnd := info.BaseAddress + info.RegionSize
		if chunkAddr+chunkSize > regionEnd {
			// Trim to region — but only if the trimmed chunk still covers
			// the requested range. Otherwise fall back.
			trimmed := regionEnd - chunkAddr
			if trimmed < uintptr(size) {
				return nil, false
			}
			chunkSize = trimmed
		}
	}
	// If QueryVirtualMemory FAILED, proceed with the original chunkSize —
	// ReadProcessMemory below will reject if the region is bad. This keeps
	// the path resilient if NtQueryVirtualMemory itself is hooked / failing.

	buf := make([]byte, chunkSize)
	if err := ntapi.ReadProcessMemory(p.handler, chunkAddr, &buf[0], chunkSize); err != nil {
		// VAD boundary / unmapped — let caller fall back to tiny direct read.
		return nil, false
	}
	if p.chunkCache == nil {
		p.chunkCache = make(map[uintptr][]byte, 16)
	}
	p.chunkCache[chunkAddr] = buf
	off := address - chunkAddr
	out := make([]byte, size)
	copy(out, buf[off:off+uintptr(size)])
	return out, true
}

// FlushChunkCache drops all cached chunks. Called at start of each GetData()
// tick so stale values don't survive across game ticks. Cheap — just a map
// reset.
func (p *Process) FlushChunkCache() {
	p.chunkCacheMu.Lock()
	defer p.chunkCacheMu.Unlock()
	if len(p.chunkCache) > 0 {
		p.chunkCache = nil
	}
}

// ReadBytesViaKernel32 reads memory via kernel32!ReadProcessMemory (not the
// indirect ntapi syscall). For some pages D2R/Arxan blocks the indirect
// syscall but allows the kernel32 path. Returns nil on error.
func (p *Process) ReadBytesViaKernel32(address uintptr, size uint) []byte {
	if size == 0 {
		return nil
	}
	data := make([]byte, size)
	if err := windows.ReadProcessMemory(p.handler, address, &data[0], uintptr(size), nil); err != nil {
		return nil
	}
	return data
}

// WriteBytesToMemory writes raw bytes into the target process at the given
// absolute address. Opens a transient handle with PROCESS_VM_WRITE +
// PROCESS_VM_OPERATION because the long-lived handler is read-only. Returns
// an error if the write fails. Intended for debug/RE use only.
func (p *Process) WriteBytesToMemory(address uintptr, data []byte) error {
	if address == 0 {
		return errors.New("attempt to write to null address")
	}
	if len(data) == 0 {
		return nil
	}
	const writeAccess = 0x0020 | 0x0008 // PROCESS_VM_WRITE | PROCESS_VM_OPERATION
	h, err := windows.OpenProcess(writeAccess, false, p.pid)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	return windows.WriteProcessMemory(h, address, &data[0], uintptr(len(data)), nil)
}

type IntType uint

const (
	Uint8  = 1
	Uint16 = 2
	Uint32 = 4
	Uint64 = 8
)

func (p *Process) ReadUInt(address uintptr, size IntType) uint {
	p.rpmReadUIntCalls.Add(1)
	bytes := p.ReadBytesFromMemory(address, uint(size))

	return bytesToUint(bytes, size)
}

func ReadUIntFromBuffer(bytes []byte, offset uint, size IntType) uint {
	return bytesToUint(bytes[offset:offset+uint(size)], size)
}

func bytesToUint(bytes []byte, size IntType) uint {
	switch size {
	case Uint8:
		return uint(bytes[0])
	case Uint16:
		return uint(binary.LittleEndian.Uint16(bytes))
	case Uint32:
		return uint(binary.LittleEndian.Uint32(bytes))
	case Uint64:
		return uint(binary.LittleEndian.Uint64(bytes))
	}

	return 0
}
func ReadIntFromBuffer(bytes []byte, offset uint, size IntType) int {
	return bytesToInt(bytes[offset:offset+uint(size)], size)
}
func bytesToInt(bytes []byte, size IntType) int {
	switch size {
	case Int8:
		return int(int8(bytes[0]))
	case Int16:
		return int(int16(binary.LittleEndian.Uint16(bytes)))
	case Int32:
		return int(int32(binary.LittleEndian.Uint32(bytes)))
	case Int64:
		return int(int64(binary.LittleEndian.Uint64(bytes)))
	}
	return 0
}

func (p *Process) ReadStringFromMemory(address uintptr, size uint) string {
	p.rpmReadStringCalls.Add(1)
	if size == 0 {
		for i := 1; true; i++ {
			data := p.ReadBytesFromMemory(address, uint(i))
			if data[i-1] == 0 {
				return string(bytes.Trim(data, "\x00"))
			}
		}
	}

	return string(bytes.Trim(p.ReadBytesFromMemory(address, size), "\x00"))
}

func (p *Process) findPattern(memory []byte, pattern, mask string) int {
	patternLength := len(pattern)
	for i := 0; i < int(p.moduleBaseSize)-patternLength; i++ {
		found := true
		for j := 0; j < patternLength; j++ {
			if string(mask[j]) != "?" && string(pattern[j]) != string(memory[i+j]) {
				found = false
				break
			}
		}

		if found {
			return i
		}
	}

	return 0
}

func (p *Process) FindPattern(memory []byte, pattern, mask string) uintptr {
	if offset := p.findPattern(memory, pattern, mask); offset != 0 {
		return p.moduleBaseAddressPtr + uintptr(offset)
	}

	return 0
}

func (p *Process) FindPatternByOperand(memory []byte, pattern, mask string) uintptr {
	if offset := p.findPattern(memory, pattern, mask); offset != 0 {
		// Adjust the address based on the operand value
		operandAddress := p.moduleBaseAddressPtr + uintptr(offset)
		operandValue := binary.LittleEndian.Uint32(memory[offset+3 : offset+7])
		finalAddress := operandAddress + uintptr(operandValue) + 7 // 7 is the length of the instruction
		return finalAddress
	}

	return 0
}

func (p *Process) GetPID() uint32 {
	return p.pid
}

type ModuleInfo struct {
	ProcessID         uint32
	ModuleBaseAddress uintptr
	ModuleBaseSize    uint32
	ModuleHandle      syscall.Handle
	ModuleName        string
}

func GetProcessModules(processID uint32) ([]ModuleInfo, error) {
	hProcess, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_READ, false, processID)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(hProcess)

	var modules [1024]windows.Handle
	var needed uint32
	if err := windows.EnumProcessModules(hProcess, &modules[0], uint32(unsafe.Sizeof(modules[0]))*1024, &needed); err != nil {
		return nil, err
	}
	count := needed / uint32(unsafe.Sizeof(modules[0]))

	var moduleInfos []ModuleInfo
	for i := uint32(0); i < count; i++ {
		var mi windows.ModuleInfo
		if err := windows.GetModuleInformation(hProcess, modules[i], &mi, uint32(unsafe.Sizeof(mi))); err != nil {
			return nil, err
		}

		var moduleName [windows.MAX_PATH]uint16
		if err := windows.GetModuleFileNameEx(hProcess, modules[i], &moduleName[0], windows.MAX_PATH); err != nil {
			return nil, err
		}

		moduleInfos = append(moduleInfos, ModuleInfo{
			ProcessID:         processID,
			ModuleBaseAddress: mi.BaseOfDll,
			ModuleBaseSize:    mi.SizeOfImage,
			ModuleHandle:      syscall.Handle(modules[i]),
			ModuleName:        syscall.UTF16ToString(moduleName[:]),
		})
	}

	return moduleInfos, nil
}

// ReadPointer reads a pointer from the specified memory address.
func (p *Process) ReadPointer(address uintptr, size int) (uintptr, error) {
	buffer := p.ReadBytesFromMemory(address, uint(size))
	if len(buffer) == 0 {
		return 0, errors.New("failed to read memory")
	}

	return uintptr(*(*uint64)(unsafe.Pointer(&buffer[0]))), nil
}

func (p *Process) ReadIntoBuffer(address uintptr, buffer []byte) error {
	p.rpmReadBufferCalls.Add(1)
	p.rpmBytesTotal.Add(uint64(len(buffer)))
	return ntapi.ReadProcessMemory(p.handler, address, &buffer[0], uintptr(len(buffer)))
}

// ReadWidgetContainer reads the WidgetContainer structure.
func (p *Process) ReadWidgetContainer(address uintptr, full bool) (map[string]interface{}, error) {
	widgetPtr, err := p.ReadPointer(address+0x8, 8)
	if err != nil {
		return nil, err
	}

	widgetNameLength := p.ReadUInt(address+0x10, 4)

	widgetName := p.ReadStringFromMemory(widgetPtr, uint(widgetNameLength))
	if widgetName == "" {
		return nil, errors.New("failed to read widget name")
	}

	widget_visible := p.ReadUInt(address+0x51, 1) == 1
	widget_active := p.ReadUInt(address+0x50, 1) == 1

	result := map[string]interface{}{
		"WidgetNameString": widgetName,
		"WidgetNameLength": widgetNameLength,
		"WidgetVisible":    widget_visible,
		"WidgetActive":     widget_active,
	}

	if full {
		childWidgetsListPtr, err := p.ReadPointer(widgetPtr+0x38, 8)
		if err != nil {
			return nil, err
		}

		childWidgetSize := p.ReadUInt(widgetPtr+0x40, 4)

		widgetListPtr, err := p.ReadPointer(widgetPtr+0x68, 8)
		if err != nil {
			return nil, err
		}

		widgetListSize := p.ReadUInt(widgetPtr+0x78, 4)

		widgetList2Ptr, err := p.ReadPointer(widgetPtr+0x80, 8)
		if err != nil {
			return nil, err
		}

		widgetList2Size := p.ReadUInt(widgetPtr+0x90, 4)

		result["ChildWidgetsListPointer"] = childWidgetsListPtr
		result["ChildWidgetSize"] = childWidgetSize
		result["WidgetListPointer"] = widgetListPtr
		result["WidgetListSize"] = widgetListSize
		result["WidgetList2Pointer"] = widgetList2Ptr
		result["WidgetList2Size"] = widgetList2Size
	}

	return result, nil
}

// ReadWidgetList iterates through a list of widgets given a pointer to the list and its size.
func (p *Process) ReadWidgetList(listPointer uintptr, listSize int) (map[string]map[string]interface{}, error) {
	widgetMap := make(map[string]map[string]interface{})
	widgetSize := int(unsafe.Sizeof(uintptr(0)))

	for i := 0; i < listSize; i++ {
		widgetAddr, err := p.ReadPointer(listPointer+uintptr(i*widgetSize), 8)
		if err != nil {
			return nil, err
		}

		widgetContainer, err := p.ReadWidgetContainer(widgetAddr, false)
		if err != nil {
			return nil, err
		}

		widgetName, ok := widgetContainer["WidgetNameString"].(string)
		if !ok {
			return nil, errors.New("failed to read widget name")
		}

		widgetMap[widgetName] = widgetContainer
	}

	return widgetMap, nil
}
