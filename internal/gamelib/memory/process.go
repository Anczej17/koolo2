package memory

import (
	"bytes"
	"encoding/binary"
	"errors"
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

var moduleName = func() string { e := []byte{0x25,0x73,0x33,0x6f,0x24,0x39,0x24}; for i := range e { e[i] ^= 0x41 }; return string(e) }()

type Process struct {
	handler              windows.Handle
	pid                  uint32
	moduleBaseAddressPtr uintptr
	moduleBaseSize       uint32
	sendPacket           *sendPacketState
	sendPacketMu         sync.Mutex
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
	// externalClick, when non-nil, fires an in-process click via the
	// Presenter's CMD_CLICK path (Phase 9). Phase-9 ClickButton enum is
	// defined in the presenter package; we accept the raw byte here to keep
	// the gamelib package free of presenter imports.
	externalClick      func(x, y int32, btn byte) error
	externalForceClick func(x, y int32) error
	externalCallFn    func(fnAddr uintptr, args ...uintptr) (uint64, error)
	externalWriteMem  func(destAddr uintptr, data []byte) error
	// externalRopRead, when non-nil and ropReadEnabled flips on, routes
	// ReadBytesFromMemory through rmod's CMD_ROP_READ path. rmod uses D2R's
	// own `rep movsb; ret` gadgets to copy `length` bytes from `src` (a D2R
	// VA) into the SHM scratch buffer at OFF_ROP_READ_BUFFER; app.exe then
	// reads from its local SHM mapping at the same offset. No cross-process
	// ReadProcessMemory is issued.
	//
	// Signature: (src D2R VA, length) → (bytes, error). Length capped at
	// OFF_ROP_READ_BUFFER_SIZE (4 KB); caller chunks above that.
	externalRopRead   func(src uintptr, length uint32) ([]byte, error)
	ropReadEnabled    atomic.Bool
	// externalBatchRead: bot packs N (src, len) tuples into a single rmod
	// round-trip. When wired and ropReadEnabled is set, the hot-path tick
	// (GameReader.GetData) can collect all the PlayerUnit chain reads and
	// service them in one Present frame instead of N. The single-shot
	// RopReadToScratch stays as a fallback for callers not yet batched.
	externalBatchRead func(entries []BatchReadEntry) ([][]byte, error)
	batchReadEnabled  atomic.Bool

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
	batchCallsTotal    atomic.Uint64
	batchCallsFailed   atomic.Uint64
	batchEntriesTotal  atomic.Uint64
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

// HandleOpen reports whether Process still holds an OS handle to the D2R
// process. True for the normal RPM path; expected false post-Phase-D
// CloseHandle once SnapshotReader serves all reads.
func (p *Process) HandleOpen() bool {
	return p.handler != 0
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
// Upstream koolo (and thus everyone Warden has already fingerprinted)
// uses exactly `0x0010` = PROCESS_VM_READ. When STEALTH_READ=1 we rotate
// between three variants per bot start, so the access bitmask itself is
// not a stable identifier across users/processes.
func openProcessAccess() uint32 {
	if !StealthEnabled() {
		return 0x0010 // PROCESS_VM_READ — upstream-parity
	}
	const (
		VMRead                = 0x0010
		QueryInformation      = 0x0400
		QueryLimitedInfo      = 0x1000
	)
	switch cryptRandN(3) {
	case 0:
		return VMRead
	case 1:
		return VMRead | QueryLimitedInfo
	default:
		return VMRead | QueryInformation
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
	return windows.CloseHandle(p.handler)
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

// GetModuleBase returns the D2R.exe base address.
func (p *Process) GetModuleBase() uintptr {
	return p.moduleBaseAddressPtr
}

func (p *Process) SetExternalCallFn(fn func(uintptr, ...uintptr) (uint64, error)) {
	p.sendPacketMu.Lock()
	defer p.sendPacketMu.Unlock()
	p.externalCallFn = fn
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

// startBatchPump launches the drain goroutine that coalesces queued reads
// into CMD_ROP_READ_BATCH dispatches. Safe to call multiple times — second
// and later calls are no-ops.
func (p *Process) startBatchPump() {
	if !p.batchPumpRunning.CompareAndSwap(false, true) {
		return
	}
	p.batchPumpStop = make(chan struct{})
	p.batchPumpWake = make(chan struct{}, 1)
	go p.batchPumpLoop()
}

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
	// Coalescing window. Short enough that bot ticks feel responsive,
	// long enough that concurrent readers pile up into one dispatch.
	// Wake fires from enqueuePumpRead when pending >= batchWakeThreshold
	// (typical hot-path GetData burst fills 128 entries in a few ms).
	const maxBatch = 128
	const batchWakeThreshold = 64
	const flushInterval = 3 * time.Millisecond

	for {
		select {
		case <-p.batchPumpStop:
			return
		case <-p.batchPumpWake:
			// Give late arrivals one more coalescing window — typical
			// GetData burst enqueues 200+ over ~5 ms.
			time.Sleep(500 * time.Microsecond)
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

			entries := make([]BatchReadEntry, n)
			for i, pr := range drain {
				entries[i] = BatchReadEntry{Src: pr.addr, Len: pr.size}
			}
			p.sendPacketMu.Lock()
			fn := p.externalBatchRead
			p.sendPacketMu.Unlock()
			if fn == nil {
				for _, pr := range drain {
					pr.done <- pendingReadResult{err: errors.New("batch hook not installed")}
				}
				continue
			}

			p.batchCallsTotal.Add(1)
			bufs, err := fn(entries)
			if err != nil {
				p.batchCallsFailed.Add(1)
				for _, pr := range drain {
					pr.done <- pendingReadResult{err: err}
				}
				continue
			}
			p.batchEntriesTotal.Add(uint64(n))
			for i, pr := range drain {
				pr.done <- pendingReadResult{data: bufs[i]}
			}
		}
	}

	_ = batchWakeThreshold
}

// enqueuePumpRead parks the reader on the async batch pump. Returns bytes
// (on success) or error (pump disabled, dispatch failed, or timeout).
// Wake is signalled only when pending crosses the batchWakeThreshold so
// small bursts have time to pile into a full-width dispatch.
func (p *Process) enqueuePumpRead(addr uintptr, size uint32) ([]byte, error) {
	const batchWakeThreshold = 64
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
func (p *Process) BatchReadBytes(entries []BatchReadEntry) ([][]byte, error) {
	if !p.batchReadEnabled.Load() {
		return nil, errors.New("batch read not enabled")
	}
	p.sendPacketMu.Lock()
	fn := p.externalBatchRead
	p.sendPacketMu.Unlock()
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

// BatchStats returns cumulative batched-read counters. (calls, failed, entries).
func (p *Process) BatchStats() (calls, failed, entries uint64) {
	return p.batchCallsTotal.Load(), p.batchCallsFailed.Load(), p.batchEntriesTotal.Load()
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

// SendDualPacket replicates D2R's dual_send_wrap behavior:
// 1. WriteProcessMemory to mirror buffer (dedup/observation copy)
// 2. SendPacket via send_fn APC on main thread (network dispatch)
//
// This is safe from main thread — no Arxan crash, no deadlock.
// Proven: 5 consecutive weapon swaps, zero crashes.
func (p *Process) SendDualPacket(packet []byte) error {
	if p == nil {
		return errors.New("process is nil")
	}
	// Try external dual sender first (Presenter/rmod).
	p.sendPacketMu.Lock()
	fn := p.externalDualSend
	p.sendPacketMu.Unlock()
	if fn != nil {
		return fn(packet)
	}
	// Fallback: manual mirror write + send_fn APC.
	if p.moduleBaseAddressPtr == 0 || p.handler == 0 {
		return errors.New("dual send: process not initialized")
	}
	const mirrorBufRVA uintptr = 0x1F51330
	mirrorAddr := p.moduleBaseAddressPtr + mirrorBufRVA
	if err := windows.WriteProcessMemory(p.handler, mirrorAddr, &packet[0], uintptr(len(packet)), nil); err != nil {
		return errors.New("dual send: mirror write failed")
	}
	return p.SendPacket(packet)
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
