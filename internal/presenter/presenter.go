package presenter

import (
	"encoding/hex"
	"fmt"
	"os"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"local/internal/svc/internal/ntapi"
)

const (
	initReadyTimeout = 15 * time.Second
)

// Presenter manages the in-process Present hook in D2R via shared memory.
type Presenter struct {
	mu          sync.Mutex
	pid         uint32
	hSection    windows.Handle  // named file mapping handle
	localView   unsafe.Pointer  // our mapped view of the shared buffer
	initialized bool
	modulePath  string
}

func New(pid uint32, modulePath string) *Presenter {
	return &Presenter{
		pid:        pid,
		modulePath: modulePath,
	}
}

// Init creates a named file mapping, manual-maps the DLL, and triggers Init via APC.
// After the DLL confirms ready, the process handle is closed — all further
// communication happens through the shared mapped view (zero handle footprint).
//
// fnDispatch    : VA of D2R send_fn (Game NetMan wrapper)
// uiNetManAddr  : VA of D2R UI NetMan global (for vtable[5] dispatch)
// fnRealClick   : VA of D2R real_click_worker (Phase 9 in-process click)
// hwnd          : D2R window handle (Phase 9; required by real_click_worker)
// mirrorBufAddr : VA of D2R global mirror buffer (dual-send for vendor packets)
//
// fnRealClick / hwnd / mirrorBufAddr may be 0 — respective commands will
// return an error, but other paths still work.
func (p *Presenter) Init(fnDispatch, uiNetManAddr, fnRealClick, hwnd, mirrorBufAddr uintptr, extras ...uintptr) error {
	var dualSendWrapAddr uintptr
	if len(extras) > 0 {
		dualSendWrapAddr = extras[0]
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	traceStart := time.Now()
	trace := func(format string, args ...interface{}) {
		if !traceEnabled {
			return
		}
		if traceEnabled {
			fmt.Fprintf(os.Stderr, "[PRES] t+%dms pid=%d "+format+"\n",
				append([]interface{}{time.Since(traceStart).Milliseconds(), p.pid}, args...)...)
		}
	}
	if traceEnabled {
		trace("Init begin fnDispatch=%#x uiNetMan=%#x realClick=%#x hwnd=%#x mirror=%#x module=%s",
			fnDispatch, uiNetManAddr, fnRealClick, hwnd, mirrorBufAddr, p.modulePath)
	}

	if p.initialized {
		if traceEnabled {
			trace("Init already initialized — skip")
		}
		return nil
	}

	// 1a. Try to attach to an existing mapping first. If a previous dev_test
	// session crashed while rmod.dll was still alive in D2R, the named mapping
	// is still held by D2R and CreateFileMapping would either fail or return a
	// handle to a buffer with stale state. Open-first lets us reuse the live
	// runtime without re-injecting the DLL.
	if traceEnabled {
		trace("Init 1a: probing existing named mapping for pid=%d", p.pid)
	}
	if hSection, localView, err := openSharedMemory(p.pid); err == nil {
		magic := readU32(localView, uintptr(offMagic))
		ready := readU32(localView, uintptr(offReadyFlag))
		debugStep := readU32(localView, uintptr(offDebugStep))
		if traceEnabled {
			trace("Init 1a: existing mapping found magic=%#x ready=%d debugStep=%#x", magic, ready, debugStep)
		}
		if magic == SharedMagic && ready == 1 {
			if traceEnabled {
				trace("Init 1a: live rmod detected — reusing")
			}
			writeU64(localView, uintptr(offFnSendPacket), uint64(fnDispatch))
			writeU64(localView, uintptr(offUINetManAddr), uint64(uiNetManAddr))
			writeU64(localView, uintptr(offFnRealClick), uint64(fnRealClick))
			writeU64(localView, uintptr(offHwndD2R), uint64(hwnd))
			writeU64(localView, uintptr(offMirrorBufAddr), uint64(mirrorBufAddr))
			writeU64(localView, uintptr(offDualSendWrap), uint64(dualSendWrapAddr))
			p.hSection = hSection
			p.localView = localView
			p.initialized = true
			if traceEnabled {
				trace("Init DONE via reuse path")
			}
			return nil
		}
		if traceEnabled {
			trace("Init 1a: existing mapping unusable, releasing")
		}
		_ = closeSharedMemory(hSection, localView)
	} else {
		if traceEnabled {
			trace("Init 1a: no existing mapping (%v) — fresh create", err)
		}
	}

	// 1b. Fresh create — first init, or rmod.dll died with the prior session.
	if traceEnabled {
		trace("Init 1b: createSharedMemory")
	}
	hSection, localView, err := createSharedMemory(p.pid)
	if err != nil {
		if traceEnabled {
			trace("Init 1b FAIL: %v", err)
		}
		return fmt.Errorf("create shared memory: %w", err)
	}
	if traceEnabled {
		trace("Init 1b OK: localView=%p hSection=%#x", localView, uintptr(hSection))
	}

	// 2. Write header into the mapped view.
	gameThreadID := findFirstThread(p.pid)
	if traceEnabled {
		trace("Init 2: findFirstThread=%d", gameThreadID)
	}
	writeU64(localView, uintptr(offFnSendPacket), uint64(fnDispatch))
	writeU64(localView, uintptr(offUINetManAddr), uint64(uiNetManAddr))
	writeU64(localView, uintptr(offFnRealClick), uint64(fnRealClick))
	writeU64(localView, uintptr(offHwndD2R), uint64(hwnd))
	writeU64(localView, uintptr(offMirrorBufAddr), uint64(mirrorBufAddr))
	writeU64(localView, uintptr(offDualSendWrap), uint64(dualSendWrapAddr))
	writeU32(localView, uintptr(offGameThreadID), gameThreadID)
	if traceEnabled {
		trace("Init 2: header written to local view (magic=%#x version=%d)",
			readU32(localView, uintptr(offMagic)), readU32(localView, uintptr(offVersion)))
	}

	// 3. Open process briefly for DLL injection only.
	const injectAccess = windows.PROCESS_CREATE_THREAD |
		windows.PROCESS_VM_OPERATION |
		windows.PROCESS_VM_READ |
		windows.PROCESS_VM_WRITE |
		windows.PROCESS_QUERY_INFORMATION

	if traceEnabled {
		trace("Init 3: OpenProcess pid=%d access=%#x", p.pid, injectAccess)
	}
	hProc, err := ntapi.OpenProcess(injectAccess, p.pid)
	if err != nil {
		if traceEnabled {
			trace("Init 3 FAIL: %v", err)
		}
		_ = closeSharedMemory(hSection, localView)
		return fmt.Errorf("open process: %w", err)
	}
	if traceEnabled {
		trace("Init 3 OK: hProc=%#x", uintptr(hProc))
	}

	// DLL still needs a remoteBuf param for the APC call — allocate a small
	// fallback page. The DLL will prefer the named mapping over this pointer.
	// Write valid header so legacy path also works if named mapping fails.
	if traceEnabled {
		trace("Init 4: VirtualAllocEx fallback buf size=%d", SharedBufSize)
	}
	fallbackBuf, err := virtualAllocEx(hProc, 0, SharedBufSize,
		windows.MEM_COMMIT|windows.MEM_RESERVE, windows.PAGE_READWRITE)
	if err != nil {
		if traceEnabled {
			trace("Init 4 FAIL: %v", err)
		}
		ntapi.CloseHandle(hProc)
		_ = closeSharedMemory(hSection, localView)
		return fmt.Errorf("alloc fallback buf: %w", err)
	}
	if traceEnabled {
		trace("Init 4 OK: fallbackBuf=%#x", fallbackBuf)
	}

	// Write header to fallback buffer so DLL can init even without named mapping.
	fallbackHdr := make([]byte, 0x100)
	writeLocal32(fallbackHdr, offMagic, SharedMagic)
	writeLocal32(fallbackHdr, offVersion, SharedVersion)
	writeLocal64(fallbackHdr, offFnSendPacket, uint64(fnDispatch))
	writeLocal64(fallbackHdr, offUINetManAddr, uint64(uiNetManAddr))
	writeLocal64(fallbackHdr, offFnRealClick, uint64(fnRealClick))
	writeLocal64(fallbackHdr, offHwndD2R, uint64(hwnd))
	writeLocal64(fallbackHdr, offMirrorBufAddr, uint64(mirrorBufAddr))
	writeLocal64(fallbackHdr, offDualSendWrap, uint64(dualSendWrapAddr))
	writeLocal32(fallbackHdr, offGameThreadID, gameThreadID)
	// Embed per-session SHM prefix (8 wide chars + null term, total 18 bytes
	// at offset 0x80). DLL Init() reads this BEFORE attempting to open the
	// named SHM, so its OpenFileMapping/CreateFileMapping calls match the
	// Go-side names. Critical for the sniffer/tracer SHMs which the DLL
	// creates internally.
	prefix := SessionShmPrefix()
	prefixWide, perr := windows.UTF16FromString(prefix)
	if perr == nil {
		nameBytes := (*[18]byte)(unsafe.Pointer(&prefixWide[0]))[:]
		// Bound the copy to 18 bytes (8 chars × 2 bytes + null) — UTF16FromString
		// already null-terminates so a fixed slice covers it safely.
		copyLen := len(nameBytes)
		if copyLen > 18 {
			copyLen = 18
		}
		copy(fallbackHdr[offSessionPrefix:offSessionPrefix+copyLen], nameBytes[:copyLen])
	}
	if werr := windows.WriteProcessMemory(hProc, fallbackBuf, &fallbackHdr[0], uintptr(len(fallbackHdr)), nil); werr != nil {
		if traceEnabled {
			trace("Init 4b WPM fallback header: %v", werr)
		}
	} else {
		if traceEnabled {
			trace("Init 4b: fallback header written (%d bytes) → %#x", len(fallbackHdr), fallbackBuf)
		}
	}

	// 4. Manual-map DLL + execute Init via APC.
	if traceEnabled {
		trace("Init 5: loadModule start")
	}
	if lerr := loadModule(p.pid, p.modulePath, fallbackBuf); lerr != nil {
		if traceEnabled {
			trace("Init 5 FAIL: %v", lerr)
		}
		ntapi.CloseHandle(hProc)
		_ = closeSharedMemory(hSection, localView)
		return fmt.Errorf("load module: %w", lerr)
	}
	if traceEnabled {
		trace("Init 5 OK: loadModule returned")
	}

	// 5. Close process handle immediately — no longer needed.
	ntapi.CloseHandle(hProc)
	if traceEnabled {
		trace("Init 5b: hProc closed")
	}

	// 6. Poll the local mapped view for ready_flag. Log debugStep transitions
	// so we see EXACTLY where the DLL is stuck if it doesn't reach ready.
	if traceEnabled {
		trace("Init 6: polling localView for ready (timeout=%s)", initReadyTimeout)
	}
	deadline := time.Now().Add(initReadyTimeout)
	lastStep := uint32(0xFFFFFFFF)
	lastErr := uint32(0)
	for time.Now().Before(deadline) {
		ready := readU32(localView, uintptr(offReadyFlag))
		step := readU32(localView, uintptr(offDebugStep))
		ec := readU32(localView, uintptr(offErrorCode))
		if step != lastStep || ec != lastErr {
			if traceEnabled {
				trace("Init 6: debugStep=%#x errorCode=%#x ready=%d", step, ec, ready)
			}
			lastStep = step
			lastErr = ec
		}
		if ready == 1 {
			p.hSection = hSection
			p.localView = localView
			p.initialized = true
			if traceEnabled {
				trace("Init DONE ready=1 at step=%#x (elapsed=%dms)",
					step, time.Since(traceStart).Milliseconds())
			}
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Timed out.
	debugStep := readU32(localView, uintptr(offDebugStep))
	errCode := readU32(localView, uintptr(offErrorCode))
	if traceEnabled {
		trace("Init TIMEOUT: debugStep=%#x errorCode=%#x (localView not updated by DLL)",
			debugStep, errCode)
	}
	// Dump the first 0x80 bytes of the local view so we can see exactly what's there.
	headerDump := make([]byte, 0x80)
	for i := range headerDump {
		headerDump[i] = readU8(localView, uintptr(i))
	}
	if traceEnabled {
		trace("Init TIMEOUT header dump: %x", headerDump)
	}
	_ = closeSharedMemory(hSection, localView)
	if errCode != 0 {
		return fmt.Errorf("runtime module error 0x%X at step 0x%X", errCode, debugStep)
	}
	if debugStep == 0 {
		return fmt.Errorf("runtime module: DLL did not start (step=0)")
	}
	return fmt.Errorf("runtime module crashed at step 0x%X", debugStep)
}

// LocalView returns the bot-side mapped view of the shared buffer. Used by
// SnapshotReader (P1-GID) to read rmod's per-Present snapshot writes.
// Returns nil if the Presenter has not been successfully initialised.
func (p *Presenter) LocalView() unsafe.Pointer {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.initialized {
		return nil
	}
	return p.localView
}

// SnapshotStaticRole tags a static-region entry so rmod's walker knows whether
// to additionally dereference the head and mirror inner chain targets. Default
// (zero value) is Regular — pure static blob, no walker chain.
type SnapshotStaticRole uint32

const (
	SnapshotStaticRoleRegular     SnapshotStaticRole = 0
	SnapshotStaticRolePingChain   SnapshotStaticRole = 1 // head 8 B ptr slot → deref → 40 B target (+36 = ping u32)
	SnapshotStaticRoleQuestChain  SnapshotStaticRole = 2 // head 8 B ptr slot → deref → 8 B @ questData → deref → 82 B flags buf
	SnapshotStaticRoleTZChain     SnapshotStaticRole = 3 // head 16 B (ptr @0, count @8) → deref → count*4 B zones (capped 8)
	SnapshotStaticRoleRosterChain SnapshotStaticRole = 4 // head 8 B ptr slot → party head → +0x148 linked list walker (capped 16 members, 0x150 B each)
)

// SnapshotStatic is one {VA, Len, Role} entry the bot wants rmod to mirror
// every Present frame into the SHM snapshot. Used for fixed-address D2R statics
// (Hover, UI, WidgetStates, FPS, KeyBindings, etc.). Role != Regular triggers
// post-mirror chain walking for Ping / QuestInfo / TerrorZones heads (B2b).
type SnapshotStatic struct {
	VA   uint64
	Len  uint32
	Role SnapshotStaticRole
}

// WriteSnapshotStatics publishes a list of static regions rmod should mirror
// into SHM every tick. Must be called BEFORE SnapshotInit so rmod sees the
// table populated on first dispatch. Up to SnapStaticMax entries (excess is
// clamped rmod-side).
func (p *Presenter) WriteSnapshotStatics(entries []SnapshotStatic) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.initialized || p.localView == nil {
		return fmt.Errorf("presenter not initialized")
	}
	n := len(entries)
	if n > SnapStaticMax {
		n = SnapStaticMax
	}
	writeU32(p.localView, uintptr(OffSnapStaticCount), uint32(n))
	for i := 0; i < n; i++ {
		off := uintptr(OffSnapStaticTable + i*SnapStaticEntrySz)
		writeU64(p.localView, off, entries[i].VA)
		writeU32(p.localView, off+8, entries[i].Len)
		writeU32(p.localView, off+12, uint32(entries[i].Role))
	}
	return nil
}

// SnapshotInit dispatches CmdSnapshotInit on the rmod side, writing the
// caller-supplied D2R offset VAs (UnitTable, Expansion, WaypointTable) into
// SHM first so rmod picks them up when handling the command. Blocks up to
// ~2 s waiting for STATUS_DONE. See PLAYER_UNIT_FIELDS.md for the offset
// contract.
//
// This is a plumbing call only — it does not wait for the first snapshot
// tick. Callers must use SnapshotReader.WaitForFirstTick for that gate.
func (p *Presenter) SnapshotInit(unitTableVA, expansionVA, waypointTableVA uint64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.initialized || p.localView == nil {
		return fmt.Errorf("presenter not initialized")
	}

	// Publish the three VAs rmod will use to walk the player-unit chain.
	writeU64(p.localView, uintptr(OffSnapUnitTable), unitTableVA)
	writeU64(p.localView, uintptr(OffSnapExpansion), expansionVA)
	writeU64(p.localView, uintptr(OffSnapWaypointTable), waypointTableVA)

	// Trigger dispatch_snapshot_init on the next Present frame.
	writeU32(p.localView, uintptr(offCommandType), CmdSnapshotInit)
	writeU32(p.localView, uintptr(offStatusFlag), StatusBusy)
	writeU32(p.localView, uintptr(offCommandFlag), 1)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status := readU32(p.localView, uintptr(offStatusFlag))
		if status == StatusDone {
			return nil
		}
		if status == StatusError {
			ec := readU32(p.localView, uintptr(offErrorCode))
			return fmt.Errorf("snapshot init error 0x%X", ec)
		}
		time.Sleep(500 * time.Microsecond)
	}
	return fmt.Errorf("snapshot init timeout (rmod didn't ack CmdSnapshotInit within 2 s)")
}

// DispatchPing sends CmdNop and measures round-trip latency. Used to verify
// the Present detour dispatch loop is firing at all. If this succeeds in
// milliseconds but RopScan times out, the problem is in the scan handler,
// not the dispatch pipeline.
func (p *Presenter) DispatchPing() (latency time.Duration, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.initialized || p.localView == nil {
		return 0, fmt.Errorf("presenter not initialized")
	}

	start := time.Now()
	writeU32(p.localView, uintptr(offCommandType), CmdNop)
	writeU32(p.localView, uintptr(offStatusFlag), StatusBusy)
	writeU32(p.localView, uintptr(offCommandFlag), 1)

	deadline := start.Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status := readU32(p.localView, uintptr(offStatusFlag))
		if status == StatusDone {
			return time.Since(start), nil
		}
		time.Sleep(200 * time.Microsecond)
	}
	return time.Since(start), fmt.Errorf("dispatch ping timeout (no ack within 2 s)")
}

// RopScan asks rmod to scan [baseVA..baseVA+length) for ROP gadgets
// (ret-ending useful sequences) and populate its internal pool, plus
// allocate Executor/Stack/Trigger buffers near the target.
//
// Uses chunked scanning: rmod handles at most 4 KB per Present frame so
// Present callback never blocks long enough to trigger d3d12 hang-watchdog.
// We loop issuing the command until ready=1 (scan complete).
//
// Returns final gadget count + ready flag. Safe to run live — does not
// execute any D2R code, just reads memory.
func (p *Presenter) RopScan(baseVA uint64, length uint64) (count uint32, ready uint32, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.initialized || p.localView == nil {
		return 0, 0, fmt.Errorf("presenter not initialized")
	}

	writeU64(p.localView, uintptr(OffRopScanBase), baseVA)
	writeU64(p.localView, uintptr(OffRopScanLen), length)

	// Per-chunk timeout: rmod scans 1 KB ≪ 1 Present frame. Allow 5 s each
	// call to tolerate a stalled frame (GPU retry / Arxan page-fault / VM
	// scheduler hiccup) before giving up.
	const (
		chunkTimeout = 5 * time.Second
		// Total chunk count the scanner CAN need = length / 4 KB. Wall-clock
		// bound per chunk is one Present frame, so completion at 60 fps is
		// ~ length/4KB * 16 ms; at 180 fps ~5 ms. For 1 MB that's ~4 s best
		// case. Give generous overall deadline.
		overallMultiplier = 4
	)
	// rmod chunk is 1 KB. Use that for deadline math so the Go-side overall
	// timeout matches the per-Present-frame work rmod actually does.
	// Deadline assumes ~60 fps best case but our VMs regularly drop to 1 fps,
	// so use a much more generous budget — 1 s per chunk + 10 s slack.
	chunks := int(length/0x400) + 1
	overallDeadline := time.Now().Add(time.Duration(chunks) * time.Second).Add(10 * time.Second)
	_ = overallMultiplier

	for time.Now().Before(overallDeadline) {
		writeU32(p.localView, uintptr(offCommandType), CmdRopScan)
		writeU32(p.localView, uintptr(offStatusFlag), StatusBusy)
		writeU32(p.localView, uintptr(offCommandFlag), 1)

		chunkDeadline := time.Now().Add(chunkTimeout)
		acked := false
		for time.Now().Before(chunkDeadline) {
			status := readU32(p.localView, uintptr(offStatusFlag))
			if status == StatusDone {
				acked = true
				break
			}
			if status == StatusError {
				ec := readU32(p.localView, uintptr(offErrorCode))
				return 0, 0, fmt.Errorf("rop scan error 0x%X", ec)
			}
			time.Sleep(500 * time.Microsecond)
		}
		if !acked {
			dbg := readU32(p.localView, uintptr(OffRopDbg))
			return 0, 0, fmt.Errorf("rop scan chunk timeout (no ack within 2 s, last_dbg=0x%08X)", dbg)
		}

		count = readU32(p.localView, uintptr(OffRopScanCount))
		ready = readU32(p.localView, uintptr(OffRopReady))
		if ready == 1 {
			return count, ready, nil
		}
	}
	dbg := readU32(p.localView, uintptr(OffRopDbg))
	return count, ready, fmt.Errorf("rop scan overall timeout (ready=0 after deadline, count=%d, last_dbg=0x%08X)", count, dbg)
}

// RopPoolBreakdown returns the current gadget-kind distribution in the
// harvested pool. Used by callers to decide whether the pool supports
// specific chains (e.g., build_memcpy needs at least one PopReg rsi/rdi/rcx
// + RepMovsb). Must be called after at least one RopScan ready=1.
type RopPoolBreakdown struct {
	Unknown    uint32
	PopReg     uint32
	MovRegMem  uint32
	MovMemReg  uint32
	RepMovsb   uint32
	RepMovsq   uint32
	XchgReg    uint32
	Ret        uint32
	PopRegMask uint32 // bit n = `pop r<n>; ret` gadget present
}

func (p *Presenter) RopPool() (RopPoolBreakdown, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.initialized || p.localView == nil {
		return RopPoolBreakdown{}, fmt.Errorf("presenter not initialized")
	}
	return RopPoolBreakdown{
		Unknown:    readU32(p.localView, uintptr(OffRopKindCounts+0)),
		PopReg:     readU32(p.localView, uintptr(OffRopKindCounts+4)),
		MovRegMem:  readU32(p.localView, uintptr(OffRopKindCounts+8)),
		MovMemReg:  readU32(p.localView, uintptr(OffRopKindCounts+12)),
		RepMovsb:   readU32(p.localView, uintptr(OffRopKindCounts+16)),
		RepMovsq:   readU32(p.localView, uintptr(OffRopKindCounts+20)),
		XchgReg:    readU32(p.localView, uintptr(OffRopKindCounts+24)),
		Ret:        readU32(p.localView, uintptr(OffRopKindCounts+28)),
		PopRegMask: readU32(p.localView, uintptr(OffRopPopRegMask)),
	}, nil
}

// RopDbg reads the current OFF_ROP_DBG marker. Useful for post-init
// diagnostics (init_from_shm writes 0xC0DE00xx band; Present writes
// 0xBEEF00xx; worker writes 0xCAFE00xx). If no scan ran yet, last value
// is the init marker.
func (p *Presenter) RopDbg() (uint32, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.initialized || p.localView == nil {
		return 0, fmt.Errorf("presenter not initialized")
	}
	return readU32(p.localView, uintptr(OffRopDbg)), nil
}

// RopWorkerHeartbeat reads the ROP scan worker's heartbeat counter from SHM.
// Plan B diagnostic: the worker bumps this every ~30 ms. If it stays at 0
// the thread never spawned; if it increments between calls the worker is
// actively looping.
func (p *Presenter) RopWorkerHeartbeat() (uint32, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.initialized || p.localView == nil {
		return 0, fmt.Errorf("presenter not initialized")
	}
	return readU32(p.localView, uintptr(OffRopWorkerHB)), nil
}

// RopRead asks rmod to execute a ROP-chain memcpy: copy `length` bytes
// from `srcVA` (a D2R virtual address) to `dstVA` (typically a scratch VA
// allocated in our SHM so we can read back). Returns the rmod-reported
// status (0=ok, 1=gadget-pool-missing, 2=exec-failed — trigger encoding
// not yet verified and this path is gated to always return 2 until unit-
// tested). Do NOT call from hot paths until flip-ready.
// RopReadToScratch issues CMD_ROP_READ with dst = SHM scratch buffer and
// returns the copied bytes directly. This is the path Process.ReadBytes
// takes when ROP_READ=1 — no need for the caller to allocate a dst, and
// the scratch offset is an rmod-internal detail.
//
// Size must be <= OffRopReadBufferSize (0x1000). Status !=0 returns err;
// caller should fall back to RPM on error.
func (p *Presenter) RopReadToScratch(srcVA uintptr, size uint32) ([]byte, error) {
	if size == 0 || size > uint32(OffRopReadBufferSize) {
		return nil, fmt.Errorf("rop read size out of range: %d", size)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.initialized || p.localView == nil {
		return nil, fmt.Errorf("presenter not initialized")
	}

	// Write command params. DST is the SHM absolute VA on the rmod side;
	// we encode it as an offset by passing 0 and having rmod resolve via
	// G_SHM+OffRopReadBuffer. Keep the size in OffRopReadLen.
	writeU64(p.localView, uintptr(OffRopReadSrc), uint64(srcVA))
	writeU64(p.localView, uintptr(OffRopReadDst), 0) // sentinel: use internal scratch
	writeU64(p.localView, uintptr(OffRopReadLen), uint64(size))
	writeU32(p.localView, uintptr(offCommandType), CmdRopRead)
	writeU32(p.localView, uintptr(offStatusFlag), StatusBusy)
	writeU32(p.localView, uintptr(offCommandFlag), 1)

	// 50 ms deadline — a healthy Present frame is 16 ms at 60 fps; allowing
	// 3 frames gives the in-process copy plenty of time without stalling
	// the bot's tick. Failure means RPM fallback (fast).
	deadline := time.Now().Add(50 * time.Millisecond)
	for time.Now().Before(deadline) {
		sflag := readU32(p.localView, uintptr(offStatusFlag))
		if sflag == StatusDone {
			break
		}
		if sflag == StatusError {
			return nil, fmt.Errorf("rop read rmod status=ERROR")
		}
		time.Sleep(200 * time.Microsecond)
	}
	if readU32(p.localView, uintptr(offStatusFlag)) != StatusDone {
		return nil, fmt.Errorf("rop read timeout")
	}
	status := readU32(p.localView, uintptr(OffRopReadStatus))
	if status != 0 {
		return nil, fmt.Errorf("rop read status=%d", status)
	}

	// Copy out of the SHM scratch buffer. localView is unsafe.Pointer to the
	// mapped file section base; bytes live at base+OffRopReadBuffer.
	out := make([]byte, size)
	base := uintptr(p.localView) + uintptr(OffRopReadBuffer)
	for i := uint32(0); i < size; i++ {
		out[i] = *(*byte)(unsafe.Pointer(base + uintptr(i)))
	}
	return out, nil
}

func (p *Presenter) RopRead(srcVA, dstVA, length uint64) (status uint32, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.initialized || p.localView == nil {
		return 0, fmt.Errorf("presenter not initialized")
	}

	writeU64(p.localView, uintptr(OffRopReadSrc), srcVA)
	writeU64(p.localView, uintptr(OffRopReadDst), dstVA)
	writeU64(p.localView, uintptr(OffRopReadLen), length)
	writeU32(p.localView, uintptr(offCommandType), CmdRopRead)
	writeU32(p.localView, uintptr(offStatusFlag), StatusBusy)
	writeU32(p.localView, uintptr(offCommandFlag), 1)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		sflag := readU32(p.localView, uintptr(offStatusFlag))
		if sflag == StatusDone {
			return readU32(p.localView, uintptr(OffRopReadStatus)), nil
		}
		if sflag == StatusError {
			ec := readU32(p.localView, uintptr(offErrorCode))
			return 0, fmt.Errorf("rop read error 0x%X", ec)
		}
		time.Sleep(500 * time.Microsecond)
	}
	return 0, fmt.Errorf("rop read timeout (rmod didn't ack CmdRopRead within 2 s)")
}

// BatchReadEntry is one request in a CMD_ROP_READ_BATCH payload. Len must be
// non-zero; sum of Len across a batch must stay <= OffRopReadBufferSize.
type BatchReadEntry struct {
	Src uintptr
	Len uint32
}

// RopReadBatch packs N (src, len) tuples into the batch table and asks rmod
// to NtReadVirtualMemory each, concatenated into the scratch buffer in entry
// order. Returns the per-entry byte slices aligned with the input order.
//
// Caller guarantees len(entries) <= RopBatchMax and sum(Len) <=
// OffRopReadBufferSize (enforced defensively). Single-entry failure status
// surfaces as an error so the caller can drop to RPM or retry; other entries
// still have their bytes populated (zeroed on failure), so partial success is
// NOT preserved — we treat the batch as all-or-nothing at the API level.
func (p *Presenter) RopReadBatch(entries []BatchReadEntry) ([][]byte, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	if len(entries) > RopBatchMax {
		return nil, fmt.Errorf("batch size %d exceeds RopBatchMax (%d)", len(entries), RopBatchMax)
	}
	total := uint32(0)
	for i, e := range entries {
		if e.Len == 0 {
			return nil, fmt.Errorf("batch entry %d: zero length", i)
		}
		total += e.Len
		if total > uint32(OffRopReadBufferSize) {
			return nil, fmt.Errorf("batch total %d exceeds OffRopReadBufferSize (%d)", total, OffRopReadBufferSize)
		}
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.initialized || p.localView == nil {
		return nil, fmt.Errorf("presenter not initialized")
	}

	// Write entries table + count.
	writeU32(p.localView, uintptr(OffRopBatchCount), uint32(len(entries)))
	for i, e := range entries {
		entryOff := uintptr(OffRopBatchEntries + i*RopBatchEntrySize)
		writeU64(p.localView, entryOff, uint64(e.Src))
		writeU32(p.localView, entryOff+8, e.Len)
		writeU32(p.localView, entryOff+12, 0) // reserved
	}

	// Dispatch.
	writeU32(p.localView, uintptr(offCommandType), CmdRopReadBatch)
	writeU32(p.localView, uintptr(offStatusFlag), StatusBusy)
	writeU32(p.localView, uintptr(offCommandFlag), 1)

	// Deadline — 200 ms covers 10+ Present frames of slack. Tight 50 ms
	// deadlines produced high spurious-timeout rate on live bot ticks that
	// fire BatchReadBytes from multiple goroutines within the same tick
	// (GetData + refresh race briefly queues the command ring).
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		sflag := readU32(p.localView, uintptr(offStatusFlag))
		if sflag == StatusDone {
			break
		}
		if sflag == StatusError {
			return nil, fmt.Errorf("rop batch rmod status=ERROR")
		}
		time.Sleep(200 * time.Microsecond)
	}
	if readU32(p.localView, uintptr(offStatusFlag)) != StatusDone {
		sflag := readU32(p.localView, uintptr(offStatusFlag))
		cmd := readU32(p.localView, uintptr(offCommandType))
		cflag := readU32(p.localView, uintptr(offCommandFlag))
		dbg := readU32(p.localView, uintptr(OffRopDbg))
		total := readU32(p.localView, uintptr(OffRopBatchTotalLen))
		return nil, fmt.Errorf("rop batch timeout status=%d cmd=%d cflag=%d dbg=0x%X total=%d",
			sflag, cmd, cflag, dbg, total)
	}

	// Read per-entry status + slice out bytes from scratch.
	statusBase := uintptr(p.localView) + uintptr(OffRopBatchStatus)
	bufBase := uintptr(p.localView) + uintptr(OffRopReadBuffer)
	out := make([][]byte, len(entries))
	offset := uint32(0)
	for i, e := range entries {
		st := *(*byte)(unsafe.Pointer(statusBase + uintptr(i)))
		if st != 0 {
			return nil, fmt.Errorf("rop batch entry %d status=%d", i, st)
		}
		buf := make([]byte, e.Len)
		for j := uint32(0); j < e.Len; j++ {
			buf[j] = *(*byte)(unsafe.Pointer(bufBase + uintptr(offset+j)))
		}
		out[i] = buf
		offset += e.Len
	}
	return out, nil
}

// RopReadSlotBatch dispatches on one of the multi-slot pool entries
// instead of the single CmdRopReadBatch slot. Multiple goroutines can
// use different slots concurrently; rmod drains every pending slot each
// Present frame. `slot` must be in [0, RopBatchSlotCount). Caller must
// have ensured exclusive ownership of the slot (typically via a fixed
// pool and an atomic acquire/release).
func (p *Presenter) RopReadSlotBatch(slot int, entries []BatchReadEntry) ([][]byte, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	if slot < 0 || slot >= RopBatchSlotCount {
		return nil, fmt.Errorf("slot %d out of range", slot)
	}
	if len(entries) > RopBatchMax {
		return nil, fmt.Errorf("batch size %d exceeds RopBatchMax (%d)", len(entries), RopBatchMax)
	}
	total := uint32(0)
	for i, e := range entries {
		if e.Len == 0 {
			return nil, fmt.Errorf("batch entry %d: zero length", i)
		}
		total += e.Len
		if total > uint32(RopBatchSlotOutputSize) {
			return nil, fmt.Errorf("batch total %d exceeds slot output (%d)", total, RopBatchSlotOutputSize)
		}
	}

	// Slot writes don't need presenter.mu — each slot is its own mailbox.
	// We still need to be sure the SHM view is mapped.
	if !p.initialized || p.localView == nil {
		return nil, fmt.Errorf("presenter not initialized")
	}

	slotBase := uintptr(OffRopBatchSlots + slot*RopBatchSlotSize)

	// Safety: only dispatch when the slot is idle. Caller should have
	// owned it via an atomic, but double-check in case of misuse.
	if readU32(p.localView, slotBase+uintptr(RopBatchSlotOffFlag)) != 0 {
		return nil, fmt.Errorf("slot %d busy", slot)
	}

	// Write entries.
	writeU32(p.localView, slotBase+uintptr(RopBatchSlotOffCount), uint32(len(entries)))
	for i, e := range entries {
		entryOff := slotBase + uintptr(RopBatchSlotOffEntries+i*RopBatchEntrySize)
		writeU64(p.localView, entryOff, uint64(e.Src))
		writeU32(p.localView, entryOff+8, e.Len)
		writeU32(p.localView, entryOff+12, 0)
	}

	// Arm — flag=1 signals rmod to process on next Present frame.
	writeU32(p.localView, slotBase+uintptr(RopBatchSlotOffFlag), 1)

	// Poll for flag back to 0 (rmod done).
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if readU32(p.localView, slotBase+uintptr(RopBatchSlotOffFlag)) == 0 {
			break
		}
		time.Sleep(200 * time.Microsecond)
	}
	if readU32(p.localView, slotBase+uintptr(RopBatchSlotOffFlag)) != 0 {
		return nil, fmt.Errorf("slot %d timeout", slot)
	}

	// Read per-entry status + slice out bytes.
	statusBase := uintptr(p.localView) + slotBase + uintptr(RopBatchSlotOffStatus)
	bufBase := uintptr(p.localView) + slotBase + uintptr(RopBatchSlotOffOutput)
	out := make([][]byte, len(entries))
	offset := uint32(0)
	for i, e := range entries {
		st := *(*byte)(unsafe.Pointer(statusBase + uintptr(i)))
		if st != 0 {
			return nil, fmt.Errorf("slot %d entry %d status=%d", slot, i, st)
		}
		buf := make([]byte, e.Len)
		for j := uint32(0); j < e.Len; j++ {
			buf[j] = *(*byte)(unsafe.Pointer(bufBase + uintptr(offset+j)))
		}
		out[i] = buf
		offset += e.Len
	}
	return out, nil
}

// UninstallDetour instructs rmod (in D2R's address space) to restore the
// Present prologue, remove VEHs, and null out G_SHM before this process exits.
// Without this call, app.exe termination leaves rmod's Present detour pointing
// at a trampoline whose G_SHM is about to be unmapped — D2R's next frame
// takes an AV, Arxan's VEH chain cascades, D2R becomes a zombie that only a
// VM reboot clears.
//
// Must be called BEFORE the presenter closes its SHM handle (the command
// channel is the SHM itself). Idempotent-ish: a second call after rmod has
// torn down its side will time out but is otherwise harmless.
//
// Bounded 1 s wait so callers on an exit path don't hang if rmod is already
// in a bad state.
func (p *Presenter) UninstallDetour() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.initialized || p.localView == nil {
		return fmt.Errorf("presenter not initialized")
	}

	writeU32(p.localView, uintptr(offCommandType), CmdUninstallDetour)
	writeU32(p.localView, uintptr(offStatusFlag), StatusBusy)
	writeU32(p.localView, uintptr(offCommandFlag), 1)

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		status := readU32(p.localView, uintptr(offStatusFlag))
		if status == StatusDone {
			return nil
		}
		if status == StatusError {
			ec := readU32(p.localView, uintptr(offErrorCode))
			return fmt.Errorf("uninstall detour error 0x%X", ec)
		}
		time.Sleep(500 * time.Microsecond)
	}
	return fmt.Errorf("uninstall detour timeout")
}

// SendPacket sends a game packet via the Present hook (in-process, frame-synchronized).
func (p *Presenter) SendPacket(packet []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.initialized || p.localView == nil {
		return fmt.Errorf("presenter not initialized")
	}

	if len(packet) == 0 || len(packet) > maxPacketPayload {
		return fmt.Errorf("packet size %d out of range", len(packet))
	}

	opcode := byte(0)
	if len(packet) > 0 {
		opcode = packet[0]
	}
	hexHead := hex.EncodeToString(packet)
	if len(hexHead) > 80 {
		hexHead = hexHead[:80] + "..."
	}
	if traceEnabled {
		fmt.Fprintf(os.Stderr, "[PKT] SendPacket opcode=0x%02X len=%dB hex=%s\n",
			opcode, len(packet), hexHead)
	}

	// Clear previous data beyond payload, then write packet.
	clearBytes(p.localView, uintptr(offPacketData), len(packet)+16)
	writeBytes(p.localView, uintptr(offPacketData), packet)
	writeU32(p.localView, uintptr(offPacketSize), uint32(len(packet)))
	writeU32(p.localView, uintptr(offCommandType), CmdSendPacket)
	writeU32(p.localView, uintptr(offStatusFlag), StatusBusy)
	writeU32(p.localView, uintptr(offCommandFlag), 1)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status := readU32(p.localView, uintptr(offStatusFlag))
		if status == StatusDone {
			if traceEnabled {
				fmt.Fprintf(os.Stderr, "[PKT] SendPacket OK opcode=0x%02X\n", opcode)
			}
			return nil
		}
		if status == StatusError {
			ec := readU32(p.localView, uintptr(offErrorCode))
			if ec&0xFF000000 == 0xEF000000 {
				op := (ec >> 16) & 0xFF
				excCode := ec & 0xFFFF
				if traceEnabled {
					fmt.Fprintf(os.Stderr, "[PKT] SendPacket CRASH opcode=0x%02X exc=0x%04X hex=%s\n",
						op, excCode, hexHead)
				}
				return fmt.Errorf("send_fn CRASHED on opcode 0x%02X, exception 0x%04X (caught by VEH, D2R alive)", op, excCode)
			}
			if traceEnabled {
				fmt.Fprintf(os.Stderr, "[PKT] SendPacket ERROR opcode=0x%02X code=0x%X\n", opcode, ec)
			}
			return fmt.Errorf("send error 0x%X", ec)
		}
		time.Sleep(100 * time.Microsecond)
	}
	if traceEnabled {
		fmt.Fprintf(os.Stderr, "[PKT] SendPacket TIMEOUT opcode=0x%02X\n", opcode)
	}
	return fmt.Errorf("send timeout")
}

// SendUIPacket sends a packet via the D2R *UI* NetMan path (vtable[5]).
//
// This path is required for opcodes that flow through the UI NetMan rather
// than the Game NetMan: 0x27 identify, 0x5C cain identify, 0x32/0x33 buy/sell,
// 0x20 cube transmute, gamble, etc. The Game NetMan send_fn (used by
// SendPacket) crashes D2R for these opcodes because of strict per-NetMan
// state checks inside the queue insert routines.
//
// The Rust DLL reads the UI NetMan global address from offUINetManAddr,
// dereferences its first qword to get the vtable, then calls vtable[5]
// (offset +0x28) with arguments (this, channel=0, &ByteRange{begin, end}).
func (p *Presenter) SendUIPacket(packet []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.initialized || p.localView == nil {
		return fmt.Errorf("presenter not initialized")
	}

	if len(packet) == 0 || len(packet) > maxPacketPayload {
		return fmt.Errorf("ui packet size %d out of range", len(packet))
	}

	opcode := byte(0)
	if len(packet) > 0 {
		opcode = packet[0]
	}
	hexHead := hex.EncodeToString(packet)
	if len(hexHead) > 80 {
		hexHead = hexHead[:80] + "..."
	}
	if traceEnabled {
		fmt.Fprintf(os.Stderr, "[PKT] SendUIPacket opcode=0x%02X len=%dB hex=%s\n",
			opcode, len(packet), hexHead)
	}

	clearBytes(p.localView, uintptr(offPacketData), len(packet)+16)
	writeBytes(p.localView, uintptr(offPacketData), packet)
	writeU32(p.localView, uintptr(offPacketSize), uint32(len(packet)))
	writeU32(p.localView, uintptr(offCommandType), CmdSendUIPacket)
	writeU32(p.localView, uintptr(offStatusFlag), StatusBusy)
	writeU32(p.localView, uintptr(offCommandFlag), 1)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status := readU32(p.localView, uintptr(offStatusFlag))
		if status == StatusDone {
			if traceEnabled {
				fmt.Fprintf(os.Stderr, "[PKT] SendUIPacket OK opcode=0x%02X\n", opcode)
			}
			return nil
		}
		if status == StatusError {
			ec := readU32(p.localView, uintptr(offErrorCode))
			if ec&0xFF000000 == 0xEF000000 {
				op := (ec >> 16) & 0xFF
				excCode := ec & 0xFFFF
				if traceEnabled {
					fmt.Fprintf(os.Stderr, "[PKT] SendUIPacket CRASH opcode=0x%02X exc=0x%04X hex=%s\n",
						op, excCode, hexHead)
				}
				return fmt.Errorf("UI send CRASHED on opcode 0x%02X, exception 0x%04X (caught by VEH, D2R alive)", op, excCode)
			}
			if traceEnabled {
				fmt.Fprintf(os.Stderr, "[PKT] SendUIPacket ERROR opcode=0x%02X code=0x%X\n", opcode, ec)
			}
			return fmt.Errorf("UI send error 0x%X", ec)
		}
		time.Sleep(100 * time.Microsecond)
	}
	if traceEnabled {
		fmt.Fprintf(os.Stderr, "[PKT] SendUIPacket TIMEOUT opcode=0x%02X\n", opcode)
	}
	return fmt.Errorf("UI send timeout")
}

// SendDualPacket sends a packet via the D2R dual-send path: memcpy to
// the global mirror buffer, then call send_fn. This replicates what D2R's
// internal vendor/trade wrapper does (RE'd at RVA ~0x117500).
//
// Required for sell (0x33), buy (0x32), and NPC interaction packets that
// the server expects to see in both the mirror buffer and Game NetMan.
func (p *Presenter) SendDualPacket(packet []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.initialized || p.localView == nil {
		return fmt.Errorf("presenter not initialized")
	}

	if len(packet) == 0 || len(packet) > maxPacketPayload {
		return fmt.Errorf("dual packet size %d out of range", len(packet))
	}

	opcode := packet[0]
	hexHead := hex.EncodeToString(packet)
	if len(hexHead) > 80 {
		hexHead = hexHead[:80] + "..."
	}
	if traceEnabled {
		fmt.Fprintf(os.Stderr, "[PKT] SendDualPacket opcode=0x%02X len=%dB hex=%s\n",
			opcode, len(packet), hexHead)
	}

	clearBytes(p.localView, uintptr(offPacketData), len(packet)+16)
	writeBytes(p.localView, uintptr(offPacketData), packet)
	writeU32(p.localView, uintptr(offPacketSize), uint32(len(packet)))
	writeU32(p.localView, uintptr(offCommandType), CmdSendDual)
	writeU32(p.localView, uintptr(offStatusFlag), StatusBusy)
	writeU32(p.localView, uintptr(offCommandFlag), 1)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status := readU32(p.localView, uintptr(offStatusFlag))
		if status == StatusDone {
			if traceEnabled {
				fmt.Fprintf(os.Stderr, "[PKT] SendDualPacket OK opcode=0x%02X\n", opcode)
			}
			return nil
		}
		if status == StatusError {
			ec := readU32(p.localView, uintptr(offErrorCode))
			if ec&0xFF000000 == 0xEF000000 {
				op := (ec >> 16) & 0xFF
				excCode := ec & 0xFFFF
				if traceEnabled {
					fmt.Fprintf(os.Stderr, "[PKT] SendDualPacket CRASH opcode=0x%02X exc=0x%04X hex=%s\n",
						op, excCode, hexHead)
				}
				return fmt.Errorf("dual send CRASHED on opcode 0x%02X, exception 0x%04X", op, excCode)
			}
			if traceEnabled {
				fmt.Fprintf(os.Stderr, "[PKT] SendDualPacket ERROR opcode=0x%02X code=0x%X\n", opcode, ec)
			}
			return fmt.Errorf("dual send error 0x%X", ec)
		}
		time.Sleep(100 * time.Microsecond)
	}
	if traceEnabled {
		fmt.Fprintf(os.Stderr, "[PKT] SendDualPacket TIMEOUT opcode=0x%02X\n", opcode)
	}
	return fmt.Errorf("dual send timeout")
}

// SendDualPacketGT sends a packet via dual_send_wrap on the GAME THREAD.
// The Present hook (render thread) lazy-installs a GetTickCount64 detour
// on first call. The game thread calls GetTickCount64 every tick — the
// detour checks SHM for pending commands and dispatches dual_send_wrap
// from game thread context (the only context where 0x50 swap works).
func (p *Presenter) SendDualPacketGT(packet []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.initialized || p.localView == nil {
		return fmt.Errorf("presenter not initialized")
	}
	if len(packet) == 0 || len(packet) > maxPacketPayload {
		return fmt.Errorf("packet size %d out of range", len(packet))
	}

	clearBytes(p.localView, uintptr(offPacketData), len(packet)+16)
	writeBytes(p.localView, uintptr(offPacketData), packet)
	writeU32(p.localView, uintptr(offPacketSize), uint32(len(packet)))
	writeU32(p.localView, uintptr(offCommandType), CmdSendDualGT)
	writeU32(p.localView, uintptr(offStatusFlag), 0)
	writeU32(p.localView, uintptr(offCommandFlag), 1)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		status := readU32(p.localView, uintptr(offStatusFlag))
		if status == StatusDone {
			return nil
		}
		if status == StatusError {
			ec := readU32(p.localView, uintptr(offErrorCode))
			return fmt.Errorf("dual-GT error 0x%X", ec)
		}
		time.Sleep(time.Millisecond)
	}
	ec := readU32(p.localView, uintptr(offErrorCode))
	status := readU32(p.localView, uintptr(offStatusFlag))
	return fmt.Errorf("dual-GT timeout (status=0x%X error=0x%X rearms=%d)", status, ec, ec&0xFFFF)
}

// ClickButton encodes the button bit consumed by D2R's real_click_worker.
type ClickButton uint8

const (
	BtnLeft   ClickButton = 1
	BtnMiddle ClickButton = 2
	BtnRight  ClickButton = 4
	BtnX1     ClickButton = 8
	BtnX2     ClickButton = 0x10
)

// Click action codes consumed by real_click_worker.
const (
	clickActionDown   = 10
	clickActionUp     = 11
	clickActionDouble = 12
)

// ClickAt issues a synthetic click at the given client-relative coordinates
// by calling D2R's internal real_click_worker from inside the Present hook.
//
// This bypasses the OS input queue entirely. The user's hardware cursor is
// not touched, no foreground-window requirement, no SendInput / mouse_event
// telemetry. The call lands at the same vtable[1] dispatch a real wndproc
// click would have hit — only difference is who put hwnd into rcx, and that
// comes from our SHM.
//
// A WalkTo on the action layer is just `ClickAt(x, y, BtnLeft)`. D2R's own
// state machine handles pathfinding, walk/run gating, and packet emission.
func (p *Presenter) ClickAt(x, y int32, btn ClickButton) error {
	if err := p.clickRaw(x, y, btn, clickActionDown); err != nil {
		return fmt.Errorf("click down: %w", err)
	}
	if err := p.clickRaw(x, y, btn, clickActionUp); err != nil {
		return fmt.Errorf("click up: %w", err)
	}
	return nil
}

// CallFn calls an arbitrary function inside D2R with up to 4 arguments.
// Returns the function's return value (rax). VEH crash protected.
func (p *Presenter) CallFn(fnAddr uintptr, args ...uintptr) (uint64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.initialized || p.localView == nil {
		return 0, fmt.Errorf("presenter not initialized")
	}

	clearBytes(p.localView, uintptr(offPacketData), 0x40)
	writeU64(p.localView, uintptr(offPacketData+0x00), uint64(fnAddr))
	writeU32(p.localView, uintptr(offPacketData+0x08), uint32(len(args)))
	for i, a := range args {
		if i >= 6 {
			break
		}
		writeU64(p.localView, uintptr(offPacketData+0x10+i*8), uint64(a))
	}

	writeU32(p.localView, uintptr(offCommandType), CmdCallFn)
	writeU32(p.localView, uintptr(offStatusFlag), StatusBusy)
	writeU32(p.localView, uintptr(offCommandFlag), 1)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status := readU32(p.localView, uintptr(offStatusFlag))
		if status == StatusDone {
			ret := readU64(p.localView, uintptr(offPacketData))
			return ret, nil
		}
		if status == StatusError {
			return 0, fmt.Errorf("CallFn error 0x%X", readU32(p.localView, uintptr(offErrorCode)))
		}
		time.Sleep(100 * time.Microsecond)
	}
	return 0, fmt.Errorf("CallFn timeout")
}

// CallFnGameThread is identical to CallFn but dispatches the call on the GAME
// THREAD via APC instead of calling directly from the render thread. This
// avoids deadlocks when calling game-logic functions (swap handler, sell
// handler, walk initiator) that acquire locks also held by the game loop.
// The render thread queues the APC and returns immediately; the game thread
// executes the function and sets STATUS_DONE asynchronously.
func (p *Presenter) CallFnGameThread(fnAddr uintptr, args ...uintptr) (uint64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.initialized || p.localView == nil {
		return 0, fmt.Errorf("presenter not initialized")
	}

	clearBytes(p.localView, uintptr(offPacketData), 0x40)
	writeU64(p.localView, uintptr(offPacketData+0x00), uint64(fnAddr))
	writeU32(p.localView, uintptr(offPacketData+0x08), uint32(len(args)))
	for i, a := range args {
		if i >= 6 {
			break
		}
		writeU64(p.localView, uintptr(offPacketData+0x10+i*8), uint64(a))
	}

	writeU32(p.localView, uintptr(offCommandType), CmdCallFnGT)
	writeU32(p.localView, uintptr(offStatusFlag), StatusBusy)
	writeU32(p.localView, uintptr(offCommandFlag), 1)

	// Longer timeout: APC fires on next game-thread alertable wait. The
	// game tick is ~16ms (60fps) but alertable waits may be less frequent.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		status := readU32(p.localView, uintptr(offStatusFlag))
		if status == StatusDone {
			ret := readU64(p.localView, uintptr(offPacketData))
			return ret, nil
		}
		if status == StatusError {
			return 0, fmt.Errorf("CallFnGT error 0x%X", readU32(p.localView, uintptr(offErrorCode)))
		}
		time.Sleep(1 * time.Millisecond)
	}
	writeU32(p.localView, uintptr(offStatusFlag), StatusBusy)
	writeU32(p.localView, uintptr(offCommandFlag), 0)
	return 0, fmt.Errorf("CallFnGT timeout")
}

// WriteMem writes arbitrary bytes to D2R process memory (from inside the process).
func (p *Presenter) WriteMem(destAddr uintptr, data []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.initialized || p.localView == nil {
		return fmt.Errorf("presenter not initialized")
	}
	if len(data) == 0 || len(data) > 2048 {
		return fmt.Errorf("invalid write size %d", len(data))
	}

	clearBytes(p.localView, uintptr(offPacketData), 12+len(data))
	writeU64(p.localView, uintptr(offPacketData+0x00), uint64(destAddr))
	writeU32(p.localView, uintptr(offPacketData+0x08), uint32(len(data)))
	writeBytes(p.localView, uintptr(offPacketData+0x0C), data)

	writeU32(p.localView, uintptr(offCommandType), CmdWriteMem)
	writeU32(p.localView, uintptr(offStatusFlag), StatusBusy)
	writeU32(p.localView, uintptr(offCommandFlag), 1)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status := readU32(p.localView, uintptr(offStatusFlag))
		if status == StatusDone {
			return nil
		}
		if status == StatusError {
			return fmt.Errorf("WriteMem error 0x%X", readU32(p.localView, uintptr(offErrorCode)))
		}
		time.Sleep(100 * time.Microsecond)
	}
	return fmt.Errorf("WriteMem timeout")
}

// SetGameThreadID overrides the APC target thread for CmdCallFnGT.
func (p *Presenter) SetGameThreadID(tid uint32) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.localView != nil {
		writeU32(p.localView, uintptr(offGameThreadID), tid)
	}
}

// SetForceMoveAddr writes the ForceMove keystate entry address into SHM.
// Call after Init. forceMoveAddr = D2R base + keyBindingsOffset + 0x49C.
func (p *Presenter) SetForceMoveAddr(forceMoveAddr uintptr) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.localView != nil {
		writeU64(p.localView, uintptr(offForceMoveAddr), uint64(forceMoveAddr))
	}
}

// ForceClick sends a left-click with ForceMove key held. D2R will interpret
// this as "move to position" regardless of what's under the cursor (no NPC
// interact, no mini-panel click). Resolution-independent: coords are D2R
// client-area pixels, the DLL reads ForceMove VK from keystate table.
func (p *Presenter) ForceClick(x, y int32) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.initialized || p.localView == nil {
		return fmt.Errorf("presenter not initialized")
	}

	clearBytes(p.localView, uintptr(offPacketData), 16)
	writeU32(p.localView, uintptr(offPacketData+0), uint32(x))
	writeU32(p.localView, uintptr(offPacketData+4), uint32(y))

	writeU32(p.localView, uintptr(offCommandType), CmdForceClick)
	writeU32(p.localView, uintptr(offStatusFlag), StatusBusy)
	writeU32(p.localView, uintptr(offCommandFlag), 1)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status := readU32(p.localView, uintptr(offStatusFlag))
		if status == StatusDone {
			return nil
		}
		if status == StatusError {
			return fmt.Errorf("force click error 0x%X", readU32(p.localView, uintptr(offErrorCode)))
		}
		time.Sleep(100 * time.Microsecond)
	}
	return fmt.Errorf("force click timeout")
}

// ClickHold issues a button-down event without the matching up. Used by
// callers that hold a button (e.g. force-move walk for held shift).
func (p *Presenter) ClickHold(x, y int32, btn ClickButton) error {
	return p.clickRaw(x, y, btn, clickActionDown)
}

// ClickRelease issues a button-up event.
func (p *Presenter) ClickRelease(x, y int32, btn ClickButton) error {
	return p.clickRaw(x, y, btn, clickActionUp)
}

func (p *Presenter) clickRaw(x, y int32, btn ClickButton, action uint8) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.initialized || p.localView == nil {
		return fmt.Errorf("presenter not initialized")
	}

	// Pack click params at the start of the packet data region. The DLL reads:
	//   +0x00 i32 x
	//   +0x04 i32 y
	//   +0x08 u8  btn
	//   +0x09 u8  action
	clearBytes(p.localView, uintptr(offPacketData), 16)
	writeU32(p.localView, uintptr(offPacketData+0), uint32(x))
	writeU32(p.localView, uintptr(offPacketData+4), uint32(y))
	writeU8(p.localView, uintptr(offPacketData+8), uint8(btn))
	writeU8(p.localView, uintptr(offPacketData+9), action)

	writeU32(p.localView, uintptr(offPacketSize), 16)
	writeU32(p.localView, uintptr(offCommandType), CmdClick)
	writeU32(p.localView, uintptr(offStatusFlag), StatusBusy)
	writeU32(p.localView, uintptr(offCommandFlag), 1)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status := readU32(p.localView, uintptr(offStatusFlag))
		if status == StatusDone {
			return nil
		}
		if status == StatusError {
			ec := readU32(p.localView, uintptr(offErrorCode))
			if ec&0xFF000000 == 0xEC000000 {
				return fmt.Errorf("click CRASHED: VEH caught exception 0x%04X (D2R alive)", ec&0xFFFF)
			}
			return fmt.Errorf("click error 0x%X", ec)
		}
		time.Sleep(100 * time.Microsecond)
	}
	return fmt.Errorf("click timeout")
}

// SniffInstall asks the DLL to patch send_fn entry with INT3 (sniff hook ON).
func (p *Presenter) SniffInstall() error {
	return p.sendCommand(CmdSniffInstall)
}

// SniffUninstall asks the DLL to restore the original send_fn byte (sniff hook OFF).
func (p *Presenter) SniffUninstall() error {
	return p.sendCommand(CmdSniffUninstall)
}

// TraceInstall installs the PacketTracer trampoline on send_fn + dual_send_wrap.
// Uses send_fn / dual_send_wrap VAs already loaded into SHM at presenter init.
// Available only in rmod_sniffer.dll (Claude mode).
func (p *Presenter) TraceInstall() error {
	return p.sendCommand(CmdTraceInstall)
}

// TraceUninstall restores the original first 14 bytes of send_fn + dual_send_wrap
// and frees the RWX stub page.
func (p *Presenter) TraceUninstall() error {
	return p.sendCommand(CmdTraceUninstall)
}

// HwbpInstall sets a hardware breakpoint (Dr0 execute) on `targetVA` across
// all D2R threads. If targetVA == 0, rmod uses the dual_send_wrap VA loaded
// at presenter init. No code modification — bypasses Arxan integrity checks.
func (p *Presenter) HwbpInstall(targetVA uint64) error {
	p.mu.Lock()
	if !p.initialized || p.localView == nil {
		p.mu.Unlock()
		return fmt.Errorf("presenter not initialized")
	}
	// Payload: u64 target VA at offPacketData[0..8].
	writeU64(p.localView, uintptr(offPacketData), targetVA)
	writeU32(p.localView, uintptr(offPacketSize), 8)
	p.mu.Unlock()
	return p.sendCommand(CmdHwbpInstall)
}

// HwbpReenum re-arms DR0 on threads created since the last install (idempotent
// for already-armed threads). Should be called periodically (~1 s) to catch
// threads spawned by D2R after initial install.
func (p *Presenter) HwbpReenum() error {
	return p.sendCommand(CmdHwbpReenum)
}

// HwbpUninstall clears Dr0/Dr7 on all D2R threads.
func (p *Presenter) HwbpUninstall() error {
	return p.sendCommand(CmdHwbpUninstall)
}

// HwbpVerify re-enumerates D2R threads and counts how many still have DR0
// equal to send_fn_addr. Diagnoses Windows/Arxan clearing or new threads.
func (p *Presenter) HwbpVerify() error {
	return p.sendCommand(CmdHwbpVerify)
}

// DrProbeEntry is one D2R thread sampled by the DR0 persist probe.
type DrProbeEntry struct {
	TID         uint32 // D2R thread ID
	StepFailed  uint32 // 0=ok, 1=open, 2=suspend, 3=getctx1, 4=setctx, 5=getctx2
	LastErr     uint32 // GetLastError of the failing step
	Dr7Orig     uint32 // low 32b of original DR7 (for context)
	Dr0Orig     uint64 // DR0 BEFORE we wrote
	Dr0After    uint64 // DR0 AFTER our SetThreadContext + GetThreadContext readback
	Persisted   bool   // Dr0After == DrProbeTestDR0 — Arxan did NOT revert
}

// DrProbeResult is the aggregate verdict + per-thread breakdown.
type DrProbeResult struct {
	Status   uint32         // 0=not run, 1=running, 2=done, 0xEExx=err
	Total    uint32         // D2R threads enumerated
	Ok       uint32         // count of threads where DR0 persisted
	Revert   uint32         // count where Arxan reverted DR0
	Err      uint32         // count of probe errors
	Entries  []DrProbeEntry // per-thread results
}

// HwbpEntry is one captured invocation of the HWBP target (dual_send_wrap).
type HwbpEntry struct {
	Ts        uint64
	TID       uint32
	RIP       uint64
	RSP       uint64
	RBP       uint64
	RAX       uint64
	RCX       uint64 // packet ptr
	RDX       uint64 // packet size
	R8        uint64
	R9        uint64
	R10       uint64
	R11       uint64
	Callstack [16]uint64 // RBP-unwound (slot 0 = RIP)
	Payload   []byte     // up to 32 bytes
}

// HwbpStatus is the SHM header snapshot.
type HwbpStatus struct {
	Installed     uint32
	Target        uint64
	Fires         uint32
	SsTotal       uint32
	LastRip       uint64
	InstallOk     uint32
	InstallFail   uint32
	VerifyStill   uint32
	VerifyLost    uint32
	ReenumNew     uint32
	ReenumTotal   uint32
	RingHead      uint32
	RingTail      uint32
	RingTotal     uint32
	RingDropped   uint32
	VehAny        uint32
	VehBp         uint32
	VehAv         uint32
	VehOther      uint32
	VehLastCode   uint32
	WorkerProg    uint32
	WorkerTid     uint32
	WorkerSeen    uint32
	WorkerOk      uint32
	WorkerFail    uint32
	Gtc64Count    uint32
	Gtc64Diag     uint32
}

// HwbpReadStatus returns the current SHM header snapshot. Cheap, can be polled.
func (p *Presenter) HwbpReadStatus() HwbpStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.initialized || p.localView == nil {
		return HwbpStatus{}
	}
	return HwbpStatus{
		Installed:   readU32(p.localView, uintptr(OffHwbpInstalled)),
		Target:      readU64(p.localView, uintptr(OffHwbpTarget)),
		Fires:       readU32(p.localView, uintptr(OffHwbpFires)),
		SsTotal:     readU32(p.localView, uintptr(OffHwbpSsTotal)),
		LastRip:     readU64(p.localView, uintptr(OffHwbpLastRip)),
		InstallOk:   readU32(p.localView, uintptr(OffHwbpInstallOk)),
		InstallFail: readU32(p.localView, uintptr(OffHwbpInstallFail)),
		VerifyStill: readU32(p.localView, uintptr(OffHwbpVerifyStill)),
		VerifyLost:  readU32(p.localView, uintptr(OffHwbpVerifyLost)),
		ReenumNew:   readU32(p.localView, uintptr(OffHwbpReenumNew)),
		ReenumTotal: readU32(p.localView, uintptr(OffHwbpReenumTotal)),
		RingHead:    readU32(p.localView, uintptr(OffHwbpRingHead)),
		RingTail:    readU32(p.localView, uintptr(OffHwbpRingTail)),
		RingTotal:   readU32(p.localView, uintptr(OffHwbpRingTotal)),
		RingDropped: readU32(p.localView, uintptr(OffHwbpRingDropped)),
		VehAny:      readU32(p.localView, uintptr(OffHwbpVehAny)),
		VehBp:       readU32(p.localView, uintptr(OffHwbpVehBp)),
		VehAv:       readU32(p.localView, uintptr(OffHwbpVehAv)),
		VehOther:    readU32(p.localView, uintptr(OffHwbpVehOther)),
		VehLastCode: readU32(p.localView, uintptr(OffHwbpVehLastCode)),
		WorkerProg:  readU32(p.localView, uintptr(OffHwbpWorkerProg)),
		WorkerTid:   readU32(p.localView, uintptr(OffHwbpWorkerTid)),
		WorkerSeen:  readU32(p.localView, uintptr(OffHwbpWorkerSeen)),
		WorkerOk:    readU32(p.localView, uintptr(OffHwbpWorkerOk)),
		WorkerFail:  readU32(p.localView, uintptr(OffHwbpWorkerFail)),
		Gtc64Count:  readU32(p.localView, uintptr(OffGtc64HookCount)),
		Gtc64Diag:   readU32(p.localView, uintptr(OffGtc64InstallDiag)),
	}
}

// HwbpDrain reads all unread entries from the ring and advances tail.
func (p *Presenter) HwbpDrain() []HwbpEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.initialized || p.localView == nil {
		return nil
	}
	head := readU32(p.localView, uintptr(OffHwbpRingHead))
	tail := readU32(p.localView, uintptr(OffHwbpRingTail))
	if head == tail {
		return nil
	}
	max := uint32(HwbpRingEntries)
	out := make([]HwbpEntry, 0, 8)
	cur := tail
	for cur != head {
		entryOff := uintptr(OffHwbpRing) + uintptr(cur)*uintptr(HwbpEntrySize)
		e := HwbpEntry{
			Ts:  readU64(p.localView, entryOff+uintptr(HwbpEntryOffTs)),
			TID: readU32(p.localView, entryOff+uintptr(HwbpEntryOffTID)),
			RIP: readU64(p.localView, entryOff+uintptr(HwbpEntryOffRIP)),
			RSP: readU64(p.localView, entryOff+uintptr(HwbpEntryOffRSP)),
			RBP: readU64(p.localView, entryOff+uintptr(HwbpEntryOffRBP)),
			RAX: readU64(p.localView, entryOff+uintptr(HwbpEntryOffRAX)),
			RCX: readU64(p.localView, entryOff+uintptr(HwbpEntryOffRCX)),
			RDX: readU64(p.localView, entryOff+uintptr(HwbpEntryOffRDX)),
			R8:  readU64(p.localView, entryOff+uintptr(HwbpEntryOffR8)),
			R9:  readU64(p.localView, entryOff+uintptr(HwbpEntryOffR9)),
			R10: readU64(p.localView, entryOff+uintptr(HwbpEntryOffR10)),
			R11: readU64(p.localView, entryOff+uintptr(HwbpEntryOffR11)),
		}
		for i := 0; i < 16; i++ {
			e.Callstack[i] = readU64(p.localView, entryOff+uintptr(HwbpEntryOffCallstack)+uintptr(i*8))
		}
		size := int(e.RDX)
		if size > HwbpEntryPayloadMax {
			size = HwbpEntryPayloadMax
		}
		if size > 0 {
			payload := make([]byte, size)
			payloadBase := uintptr(unsafe.Pointer(p.localView)) + entryOff + uintptr(HwbpEntryOffPayload)
			for i := 0; i < size; i++ {
				payload[i] = *(*byte)(unsafe.Pointer(payloadBase + uintptr(i)))
			}
			e.Payload = payload
		}
		out = append(out, e)
		cur = (cur + 1) % max
	}
	writeU32(p.localView, uintptr(OffHwbpRingTail), head)
	return out
}

// DrProbe runs the DR0 persist diagnostic in rmod.dll. After it returns,
// inspect Result.Ok vs Result.Revert to determine HWBP-tracer viability:
//   - Ok > 0 on game thread = HWBP path open
//   - Revert == Total       = Arxan strips DR0, HWBP dead, need different bypass
func (p *Presenter) DrProbe() (DrProbeResult, error) {
	if err := p.sendCommand(CmdDrProbe); err != nil {
		return DrProbeResult{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.initialized || p.localView == nil {
		return DrProbeResult{}, fmt.Errorf("presenter not initialized")
	}

	res := DrProbeResult{
		Status: readU32(p.localView, uintptr(OffDrProbeValid)),
		Total:  readU32(p.localView, uintptr(OffDrProbeTotal)),
		Ok:     readU32(p.localView, uintptr(OffDrProbeOk)),
		Revert: readU32(p.localView, uintptr(OffDrProbeRevert)),
		Err:    readU32(p.localView, uintptr(OffDrProbeErr)),
	}
	num := readU32(p.localView, uintptr(OffDrProbeNum))
	if num > uint32(DrProbeMaxEntries) {
		num = uint32(DrProbeMaxEntries)
	}
	res.Entries = make([]DrProbeEntry, 0, num)
	for i := uint32(0); i < num; i++ {
		off := uintptr(OffDrProbeEntries) + uintptr(i)*uintptr(DrProbeEntrySize)
		e := DrProbeEntry{
			TID:        readU32(p.localView, off+0x00),
			StepFailed: readU32(p.localView, off+0x04),
			LastErr:    readU32(p.localView, off+0x08),
			Dr7Orig:    readU32(p.localView, off+0x0C),
			Dr0Orig:    readU64(p.localView, off+0x10),
			Dr0After:   readU64(p.localView, off+0x18),
		}
		e.Persisted = e.StepFailed == 0 && e.Dr0After == uint64(DrProbeTestDR0)
		res.Entries = append(res.Entries, e)
	}
	return res, nil
}

// sendCommand sends a no-payload command to the DLL via shared memory.
func (p *Presenter) sendCommand(cmdType uint32) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.initialized || p.localView == nil {
		return fmt.Errorf("presenter not initialized")
	}
	writeU32(p.localView, uintptr(offCommandType), cmdType)
	writeU32(p.localView, uintptr(offStatusFlag), StatusBusy)
	writeU32(p.localView, uintptr(offCommandFlag), 1)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status := readU32(p.localView, uintptr(offStatusFlag))
		if status == StatusDone {
			return nil
		}
		if status == StatusError {
			ec := readU32(p.localView, uintptr(offErrorCode))
			return fmt.Errorf("command 0x%X error 0x%X", cmdType, ec)
		}
		time.Sleep(100 * time.Microsecond)
	}
	return fmt.Errorf("command 0x%X timeout", cmdType)
}

// SniffEntry is a single packet logged by the DLL's INT3 hook on send_fn.
type SniffEntry struct {
	Size uint32 // actual packet size as passed to send_fn
	Data []byte // up to SniffDataMax bytes (truncated if Size > SniffDataMax)
}

// SniffDiag is the diagnostic counter snapshot from the DLL.
type SniffDiag struct {
	Total      uint32
	BpFired    uint32
	BpOurs     uint32
	SsFired    uint32
	Install    uint32
	Uninstall  uint32
	LastBadRip uint32 // narrowed from u64 to avoid stomping entry slot 0

	// Periodic re-arm worker (200ms tick) stats — set by hwbp_periodic_worker
	// in lib.rs. Separate from the install/uninstall diag so they don't collide.
	HwbpTickCount   uint32 // total ticks executed
	HwbpNewArmed    uint32 // cumulative threads armed across all ticks
	HwbpLastNewTid  uint32 // last TID newly armed
	HwbpLastTickNew uint32 // newly-armed count from most recent tick
	HwbpWorkerAlive uint32 // heartbeat (incremented every tick)
}

// ReadSniffDiag returns just the diagnostic counters (no entries).
func (p *Presenter) ReadSniffDiag() SniffDiag {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.initialized || p.localView == nil {
		return SniffDiag{}
	}
	return SniffDiag{
		Total:           readU32(p.localView, uintptr(OffSniffTotal)),
		BpFired:         readU32(p.localView, uintptr(OffSniffBpFired)),
		BpOurs:          readU32(p.localView, uintptr(OffSniffBpOurs)),
		SsFired:         readU32(p.localView, uintptr(OffSniffSsFired)),
		Install:         readU32(p.localView, uintptr(OffSniffInstall)),
		Uninstall:       readU32(p.localView, uintptr(OffSniffUninstall)),
		LastBadRip:      readU32(p.localView, uintptr(OffSniffLastRip)),
		HwbpTickCount:   readU32(p.localView, uintptr(OffHwbpTickCount)),
		HwbpNewArmed:    readU32(p.localView, uintptr(OffHwbpNewArmed)),
		HwbpLastNewTid:  readU32(p.localView, uintptr(OffHwbpLastNewTid)),
		HwbpLastTickNew: readU32(p.localView, uintptr(OffHwbpLastTickNew)),
		HwbpWorkerAlive: readU32(p.localView, uintptr(OffHwbpWorkerAlive)),
	}
}

// CrashDiag is the in-process crash record captured by rmod.dll's VEH when
// D2R hits an unhandled exception. Bot reads this after detecting D2R exit.
//
// Status encodes the install-time diagnostic before any crash occurs:
//
//	0    = handler not installed yet
//	0xC1 = install entered (didn't reach AVE call)
//	0xC2 = AddVectoredExceptionHandler succeeded — handler armed
//	0xC3 = AddVectoredExceptionHandler failed (handler is NOT armed)
//	1    = a real fatal exception was captured; the rest of the fields are valid
type CrashDiag struct {
	Status    uint32 // raw OFF_CRASH_VALID byte — see comment above
	Valid     bool   // true iff Status == 1 (i.e. an actual crash record)
	Count     uint32 // total exceptions caught (incl. non-fatal pass-through)
	Fixups    uint32 // count of auto-fixups applied by VEH (e.g. stash memcpy patch)
	Code      uint32 // EXCEPTION_RECORD.ExceptionCode (e.g. 0xC0000005)
	Flags     uint32
	TID       uint32
	FaultType uint32 // for AV: 0=read 1=write 8=DEP
	RIP       uint64 // instruction that faulted
	FaultVA   uint64 // address being accessed (AV)
	RSP       uint64
	Frames    []uint64 // top 16 stack qwords from RSP

	// Full x64 integer register file at fault time. Order: RAX RCX RDX RBX RSP
	// RBP RSI RDI R8 R9 R10 R11 R12 R13 R14 R15.
	Regs [16]uint64

	// Raw memory snapshots — see protocol.go OffCrash*Bytes.
	RIPBytes    []byte   // CrashRIPBytesLen bytes starting at RIP - CrashRIPBytesPre
	FrameBytes  [][]byte // per-frame raw bytes (CrashFrameBytesLen each), nil if frame skipped

	// Per-code exception counters maintained by the VEH on every fire.
	CountAV  uint32
	CountSO  uint32
	CountSBO uint32
	LastCode uint32
}

// RegNames indexes CrashDiag.Regs to human-readable mnemonics.
var RegNames = [16]string{
	"rax", "rcx", "rdx", "rbx", "rsp", "rbp", "rsi", "rdi",
	"r8", "r9", "r10", "r11", "r12", "r13", "r14", "r15",
}

// ReadCrashDiag pulls the crash record from SHM. Safe to call any time —
// returns Valid=false if no exception was captured yet (or the field block
// hasn't been initialised by rmod.dll).
func (p *Presenter) ReadCrashDiag() CrashDiag {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.initialized || p.localView == nil {
		return CrashDiag{}
	}
	status := readU32(p.localView, uintptr(OffCrashValid))
	d := CrashDiag{
		Status:    status,
		Valid:     status == 1,
		Count:     readU32(p.localView, uintptr(OffCrashCount)),
		Fixups:    readU32(p.localView, uintptr(OffCrashFixups)),
		Code:      readU32(p.localView, uintptr(OffCrashCode)),
		Flags:     readU32(p.localView, uintptr(OffCrashFlags)),
		TID:       readU32(p.localView, uintptr(OffCrashTID)),
		FaultType: readU32(p.localView, uintptr(OffCrashFaultType)),
		RIP:       readU64(p.localView, uintptr(OffCrashRIP)),
		FaultVA:   readU64(p.localView, uintptr(OffCrashFaultVA)),
		RSP:       readU64(p.localView, uintptr(OffCrashRSP)),
		CountAV:        readU32(p.localView, 0xA0),
		CountSO:        readU32(p.localView, 0xA4),
		CountSBO:       readU32(p.localView, 0xA8),
		LastCode:       readU32(p.localView, 0xAC),
	}
	d.Frames = make([]uint64, CrashFrameCount)
	for i := 0; i < CrashFrameCount; i++ {
		d.Frames[i] = readU64(p.localView, uintptr(OffCrashFrames+i*8))
	}
	if d.Valid {
		for i := 0; i < 16; i++ {
			d.Regs[i] = readU64(p.localView, uintptr(OffCrashRegs+i*8))
		}
		d.RIPBytes = make([]byte, CrashRIPBytesLen)
		for i := 0; i < CrashRIPBytesLen; i++ {
			d.RIPBytes[i] = readU8(p.localView, uintptr(OffCrashRIPBytes+i))
		}
		d.FrameBytes = make([][]byte, CrashFrameCount)
		for i := 0; i < CrashFrameCount; i++ {
			buf := make([]byte, CrashFrameBytesLen)
			off := uintptr(OffCrashFrameBytes + i*CrashFrameBytesStride)
			allZero := true
			for j := 0; j < CrashFrameBytesLen; j++ {
				buf[j] = readU8(p.localView, off+uintptr(j))
				if buf[j] != 0 {
					allZero = false
				}
			}
			if !allZero {
				d.FrameBytes[i] = buf
			}
		}
	}
	return d
}

// ReadSniffLog returns the most recent sniff log entries (in order from
// oldest to newest in the visible window). Total is the monotonic count
// of all packets ever logged.
func (p *Presenter) ReadSniffLog() (entries []SniffEntry, total uint32) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.initialized || p.localView == nil {
		return nil, 0
	}
	head := readU32(p.localView, uintptr(OffSniffHead))
	total = readU32(p.localView, uintptr(OffSniffTotal))

	count := SniffNumEntries
	if int(total) < count {
		count = int(total)
	}
	entries = make([]SniffEntry, 0, count)
	// Iterate from oldest to newest
	startSlot := int(head) - count
	for i := 0; i < count; i++ {
		slot := ((startSlot + i) % SniffNumEntries + SniffNumEntries) % SniffNumEntries
		entryOff := uintptr(OffSniffEntries + slot*SniffEntrySize)
		size := readU32(p.localView, entryOff)
		copyLen := int(size)
		if copyLen > SniffDataMax {
			copyLen = SniffDataMax
		}
		data := make([]byte, copyLen)
		for j := 0; j < copyLen; j++ {
			data[j] = readU8(p.localView, entryOff+4+uintptr(j))
		}
		entries = append(entries, SniffEntry{Size: size, Data: data})
	}
	return entries, total
}

// SetCursor writes cursor coordinates to the shared buffer (Phase 8D).
func (p *Presenter) SetCursor(x, y int32) {
	if !p.initialized || p.localView == nil {
		return
	}
	writeU32(p.localView, uintptr(offCursorX), uint32(x))
	writeU32(p.localView, uintptr(offCursorY), uint32(y))
}

// SetKeyState writes key state to the shared buffer (Phase 8D).
func (p *Presenter) SetKeyState(key byte, active bool) {
	if !p.initialized || p.localView == nil {
		return
	}
	val := byte(0)
	if active {
		val = 1
	}
	dst := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(p.localView)+uintptr(offTargetKey))), 2)
	dst[0] = key
	dst[1] = val
}

// IsReady returns true if the DLL has installed the Present hook.
func (p *Presenter) IsReady() bool {
	return p.initialized
}

// CursorBufAddr returns the cursor X/Y address in D2R's address space.
// The DLL writes its mapped view base to offRemoteViewAddr during init.
func (p *Presenter) CursorBufAddr() uintptr {
	if p.localView == nil {
		return 0
	}
	remoteBase := readU64(p.localView, uintptr(offRemoteViewAddr))
	if remoteBase == 0 {
		return 0
	}
	return uintptr(remoteBase) + uintptr(offCursorX)
}

// KeyDataBufAddr returns the key state address in D2R's address space.
// The trampoline reads target_key (1 byte) and key_active (1 byte) from here.
func (p *Presenter) KeyDataBufAddr() uintptr {
	if p.localView == nil {
		return 0
	}
	remoteBase := readU64(p.localView, uintptr(offRemoteViewAddr))
	if remoteBase == 0 {
		return 0
	}
	return uintptr(remoteBase) + uintptr(offTargetKey)
}

// Close releases resources.
func (p *Presenter) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.localView != nil {
		_ = closeSharedMemory(p.hSection, p.localView)
		p.localView = nil
		p.hSection = 0
	}
	p.initialized = false
}

// findFirstThread returns the first (main) thread ID of a process.
func findFirstThread(pid uint32) uint32 {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return 0
	}
	defer windows.CloseHandle(snap)
	var te windows.ThreadEntry32
	te.Size = uint32(unsafe.Sizeof(te))
	if windows.Thread32First(snap, &te) != nil {
		return 0
	}
	for {
		if te.OwnerProcessID == pid {
			return te.ThreadID
		}
		if windows.Thread32Next(snap, &te) != nil {
			break
		}
	}
	return 0
}

// writeLocal32/writeLocal64 write to a Go []byte buffer (for WPM fallback path).
func writeLocal32(buf []byte, off int, val uint32) {
	buf[off] = byte(val)
	buf[off+1] = byte(val >> 8)
	buf[off+2] = byte(val >> 16)
	buf[off+3] = byte(val >> 24)
}

func writeLocal64(buf []byte, off int, val uint64) {
	writeLocal32(buf, off, uint32(val))
	writeLocal32(buf, off+4, uint32(val>>32))
}
