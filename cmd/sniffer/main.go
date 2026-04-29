// Standalone D2R packet sniffer.
//
// Attaches to a running D2R.exe as a Win32 debugger, sets a hardware
// breakpoint (DR0, exec) on the game's send_packet function, and prints
// the bytes of every outgoing packet to stdout.
//
// Architecture:
//   - DebugActiveProcess(pid) makes us a debugger for D2R
//   - DebugSetProcessKillOnExit(FALSE) so when WE exit, D2R does NOT die
//   - On CREATE_PROCESS_DEBUG_EVENT we get D2R's BaseOfImage and the main
//     thread handle. send_fn = base + 0x146600.
//   - On CREATE_THREAD_DEBUG_EVENT (synthetic for existing threads, real
//     for newly-spawned ones), we install DR0 = send_fn on that thread.
//     This handles thread propagation FOR FREE — no polling worker needed.
//   - On EXCEPTION_DEBUG_EVENT (EXCEPTION_SINGLE_STEP at send_fn), we read
//     RCX (packet ptr) and RDX (size) from the thread context, then
//     ReadProcessMemory the bytes, hex-print them, set RF flag in EFLAGS
//     so the breakpoint doesn't immediately re-trigger, and continue.
//   - All other exceptions are passed through (DBG_EXCEPTION_NOT_HANDLED).
//
// Build:  go build -o build/sniffer.exe ./cmd/sniffer
// Usage:  sniffer.exe --pid 1234 [--out packets.log]
package main

import (
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ------------- Win32 FFI -------------

var (
	kernel32                      = windows.NewLazySystemDLL("kernel32.dll")
	procDebugActiveProcess        = kernel32.NewProc("DebugActiveProcess")
	procDebugActiveProcessStop    = kernel32.NewProc("DebugActiveProcessStop")
	procDebugSetProcessKillOnExit = kernel32.NewProc("DebugSetProcessKillOnExit")
	procWaitForDebugEventEx       = kernel32.NewProc("WaitForDebugEventEx")
	procContinueDebugEvent        = kernel32.NewProc("ContinueDebugEvent")
	procGetThreadContext          = kernel32.NewProc("GetThreadContext")
	procSetThreadContext          = kernel32.NewProc("SetThreadContext")
	procReadProcessMemory         = kernel32.NewProc("ReadProcessMemory")
)

const (
	dbgContinue            = 0x00010002
	dbgExceptionNotHandled = 0x80010001

	exceptionDebugEvent     = 1
	createThreadDebugEvent  = 2
	createProcessDebugEvent = 3
	exitThreadDebugEvent    = 4
	exitProcessDebugEvent   = 5
	loadDllDebugEvent       = 6
	unloadDllDebugEvent     = 7
	outputDebugStringEvent  = 8
	ripEvent                = 9

	excBreakpoint = 0x80000003
	excSingleStep = 0x80000004

	contextAmd64          = 0x00100000
	contextControl        = contextAmd64 | 0x1
	contextInteger        = contextAmd64 | 0x2
	contextFloat          = contextAmd64 | 0x8
	contextDebugRegisters = contextAmd64 | 0x10
	contextFullPlusDebug  = contextControl | contextInteger | contextFloat | contextDebugRegisters
	contextDebugRegsOnly  = contextDebugRegisters

	// AMD64 CONTEXT field offsets (Windows ABI). Verified against winnt.h.
	ctxBufSize   = 1232
	ctxFlagsOff  = 0x30
	ctxDr0Off    = 0x48
	ctxDr1Off    = 0x50
	ctxDr2Off    = 0x58
	ctxDr3Off    = 0x60
	ctxDr6Off    = 0x68
	ctxDr7Off    = 0x70
	ctxEflagsOff = 0x44
	ctxRcxOff    = 0x80
	ctxRdxOff    = 0x88
	ctxR8Off     = 0x98
	ctxRipOff    = 0xF8

	// EFLAGS Resume Flag — set this in the trapped thread's context so the
	// HWBP doesn't immediately fire again on the same instruction when we
	// continue execution.
	eflagsRF = 0x00010000

	// Hardware breakpoint slots — we use both DR0 (Game NetMan send_fn) and
	// DR1 (UI NetMan vtable[5] entry). Both are exec breakpoints, length=1.
	//
	// DR7 layout for x64:
	//   bit 0  = L0 (slot 0 local enable)
	//   bit 2  = L1 (slot 1 local enable)
	//   bit 8  = LE (local exact match)
	//   bits 16-17 = DR0 R/W (00 = exec)
	//   bits 18-19 = DR0 LEN (00 = 1 byte)
	//   bits 20-21 = DR1 R/W (00 = exec)
	//   bits 22-23 = DR1 LEN (00 = 1 byte)
	//
	// L0 + L1 + LE = 0x1 | 0x4 | 0x100 = 0x105
	dr7EnableBoth = 0x00000105
	dr7Enable     = 0x00000101 // legacy: DR0 only
	dr7Clear      = 0

	// D2R send_packet offset from D2R.exe base. Verified live: 0x146600.
	// This is the Game NetMan wrapper (carries movement, casts, interacts).
	sendFnOffset = 0x146600

	// D2R UI NetMan global offset from D2R.exe base. The address at this
	// location holds a pointer to the UI NetMan instance whose vtable[5]
	// (offset +0x28) is the actual queue-inserter function. This carries
	// sell/buy/identify/repair/cube/gamble.
	//
	// Resolution chain:
	//   ui_netman_instance = *(base + 0x19ED860)
	//   vtable             = *(ui_netman_instance + 0)
	//   ui_send_fn         = *(vtable + 0x28)
	uiNetManGlobalOffset = 0x19ED860
	uiVtableSlot         = 0x28

	// Cap packet read size — D2R packets are tiny in practice (<32 bytes).
	// Reduced from 256 → 64 to avoid crossing page boundaries when RDX
	// contains a junk size (e.g. high 32 bits not zeroed by caller).
	maxPacketBytes = 64
)

var npcOpcodeFilter = map[byte]bool{
	0x03: true, // RunToLocation, useful to verify close-range NPC setup
	0x2F: true, // NPCInit
	0x30: true, // NPCCancel
	0x32: true, // NPCBuy
	0x33: true, // NPCSell
	0x34: true, // NPCIdentify/cube-context path
	0x38: true, // NPCAction / dialog option
	0x40: true, // UnitInteract
	0x41: true, // InteractEx / UseWaypoint/TP
	0x4D: true, // PreInteract
}

func parseOpcodeFilter(raw string, includeNPC bool) (map[byte]bool, error) {
	var filter map[byte]bool
	if includeNPC {
		filter = make(map[byte]bool, len(npcOpcodeFilter))
		for op := range npcOpcodeFilter {
			filter[op] = true
		}
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return filter, nil
	}
	if filter == nil {
		filter = make(map[byte]bool)
	}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		part = strings.TrimPrefix(strings.ToLower(part), "0x")
		v, err := strconv.ParseUint(part, 16, 8)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", part, err)
		}
		filter[byte(v)] = true
	}
	return filter, nil
}

func annotatePacket(p []byte) string {
	if len(p) == 0 {
		return ""
	}
	switch p[0] {
	case 0x03:
		if len(p) >= 9 {
			x := binary.LittleEndian.Uint16(p[1:3])
			y := binary.LittleEndian.Uint16(p[3:5])
			return fmt.Sprintf("RunToLocation x=%d y=%d", x, y)
		}
	case 0x2F:
		if len(p) >= 5 {
			return fmt.Sprintf("NPCInit npc=0x%X", binary.LittleEndian.Uint32(p[1:5]))
		}
	case 0x30:
		if len(p) >= 5 {
			return fmt.Sprintf("NPCCancel npc=0x%X", binary.LittleEndian.Uint32(p[1:5]))
		}
	case 0x32:
		if len(p) >= 22 {
			price := binary.LittleEndian.Uint32(p[1:5])
			item := binary.LittleEndian.Uint32(p[5:9])
			npc := binary.LittleEndian.Uint32(p[9:13])
			return fmt.Sprintf("NPCBuy item=0x%X npc=0x%X price=%d", item, npc, price)
		}
	case 0x33:
		if len(p) >= 22 {
			price := binary.LittleEndian.Uint32(p[1:5])
			item := binary.LittleEndian.Uint32(p[5:9])
			npc := binary.LittleEndian.Uint32(p[9:13])
			return fmt.Sprintf("NPCSell item=0x%X npc=0x%X price=%d", item, npc, price)
		}
	case 0x34:
		if len(p) >= 21 {
			item := binary.LittleEndian.Uint32(p[1:5])
			npc := binary.LittleEndian.Uint32(p[5:9])
			return fmt.Sprintf("NPCIdentify item=0x%X npc=0x%X", item, npc)
		}
	case 0x38:
		if len(p) >= 9 {
			action := binary.LittleEndian.Uint32(p[1:5])
			npc := binary.LittleEndian.Uint32(p[5:9])
			return fmt.Sprintf("NPCAction9 action=%d npc=0x%X", action, npc)
		}
		if len(p) >= 6 {
			action := binary.LittleEndian.Uint32(p[1:5])
			return fmt.Sprintf("NPCAction6 action=%d term=0x%02X", action, p[5])
		}
	case 0x40:
		if len(p) >= 13 {
			unitType := binary.LittleEndian.Uint32(p[1:5])
			gid := binary.LittleEndian.Uint32(p[5:9])
			return fmt.Sprintf("UnitInteract type=%d gid=0x%X", unitType, gid)
		}
		if len(p) >= 5 {
			return fmt.Sprintf("UnitInteract gid=0x%X", binary.LittleEndian.Uint32(p[1:5]))
		}
	case 0x41:
		if len(p) >= 13 {
			gid := binary.LittleEndian.Uint32(p[1:5])
			action := binary.LittleEndian.Uint32(p[5:9])
			return fmt.Sprintf("InteractEx/UseObject gid=0x%X action=%d", gid, action)
		}
	case 0x4D:
		if len(p) >= 5 {
			return fmt.Sprintf("PreInteract unit=0x%X", binary.LittleEndian.Uint32(p[1:5]))
		}
	}
	return ""
}

// DEBUG_EVENT layout on x64. The union starts at offset 16 and the largest
// member is EXCEPTION_DEBUG_INFO (~156 bytes). We use a fixed buffer and
// pull fields out by offset, same approach as the rmod CONTEXT handling.
const debugEventBufSize = 200

type debugEvent [debugEventBufSize]byte

func (e *debugEvent) code() uint32      { return *(*uint32)(unsafe.Pointer(&e[0])) }
func (e *debugEvent) processID() uint32 { return *(*uint32)(unsafe.Pointer(&e[4])) }
func (e *debugEvent) threadID() uint32  { return *(*uint32)(unsafe.Pointer(&e[8])) }
func (e *debugEvent) unionPtr() unsafe.Pointer {
	return unsafe.Pointer(&e[16])
}

// EXCEPTION_DEBUG_INFO at union+0:
//
//	ExceptionRecord (EXCEPTION_RECORD64):
//	  +0  ExceptionCode      u32
//	  +4  ExceptionFlags     u32
//	  +8  ExceptionRecord    u64
//	  +16 ExceptionAddress   u64
func (e *debugEvent) excCode() uint32 {
	return *(*uint32)(unsafe.Pointer(&e[16]))
}
func (e *debugEvent) excAddress() uint64 {
	return *(*uint64)(unsafe.Pointer(&e[16+16]))
}

// CREATE_THREAD_DEBUG_INFO at union+0:
//
//	+0 hThread        uintptr
//	+8 ThreadLocalBase uintptr
//	+16 StartAddress  uintptr
func (e *debugEvent) createThreadHandle() uintptr {
	return *(*uintptr)(unsafe.Pointer(&e[16]))
}

// CREATE_PROCESS_DEBUG_INFO at union+0:
//
//	+0  hFile             uintptr
//	+8  hProcess          uintptr
//	+16 hThread           uintptr
//	+24 BaseOfImage       uintptr
func (e *debugEvent) createProcessHandle() uintptr {
	return *(*uintptr)(unsafe.Pointer(&e[16+8]))
}
func (e *debugEvent) createProcessThreadHandle() uintptr {
	return *(*uintptr)(unsafe.Pointer(&e[16+16]))
}
func (e *debugEvent) createProcessBaseOfImage() uintptr {
	return *(*uintptr)(unsafe.Pointer(&e[16+24]))
}

// ------------- thread context helpers -------------

// 16-byte aligned buffer for AMD64 CONTEXT. Allocate via make([]byte, ...)
// then ensure alignment by overprovisioning. Easier on Go: use a struct with
// align hint via embedding.
type alignedCtx struct {
	_   [0]uint64 // align to 8 — we'll force 16 by overprovisioning
	buf [ctxBufSize + 16]byte
}

func (a *alignedCtx) ptr() unsafe.Pointer {
	addr := uintptr(unsafe.Pointer(&a.buf[0]))
	off := (16 - (addr & 15)) & 15
	return unsafe.Pointer(&a.buf[off])
}

func getContext(hThread uintptr, flags uint32) (unsafe.Pointer, *alignedCtx, error) {
	a := &alignedCtx{}
	p := a.ptr()
	*(*uint32)(unsafe.Add(p, ctxFlagsOff)) = flags
	r1, _, err := procGetThreadContext.Call(hThread, uintptr(p))
	if r1 == 0 {
		return nil, nil, fmt.Errorf("GetThreadContext: %v", err)
	}
	return p, a, nil
}

func setContext(hThread uintptr, p unsafe.Pointer, flags uint32) error {
	*(*uint32)(unsafe.Add(p, ctxFlagsOff)) = flags
	r1, _, err := procSetThreadContext.Call(hThread, uintptr(p))
	if r1 == 0 {
		return fmt.Errorf("SetThreadContext: %v", err)
	}
	return nil
}

func installHwbpOnHandle(hThread uintptr, sendFn uint64) error {
	p, _, err := getContext(hThread, contextFullPlusDebug)
	if err != nil {
		return err
	}
	*(*uint64)(unsafe.Add(p, ctxDr0Off)) = sendFn
	*(*uint64)(unsafe.Add(p, ctxDr7Off)) = dr7Enable
	// Write back ONLY debug regs — leave RIP/RSP/integer regs untouched.
	return setContext(hThread, p, contextDebugRegsOnly)
}

// installBothHwbpOnHandle sets DR0 = sendFn (Game NetMan) AND DR1 = uiSendFn
// (UI NetMan vtable[5]) on the given thread, both as exec breakpoints. If
// uiSendFn == 0, only DR0 is installed (graceful degrade).
func installBothHwbpOnHandle(hThread uintptr, sendFn, uiSendFn uint64) error {
	p, _, err := getContext(hThread, contextFullPlusDebug)
	if err != nil {
		return err
	}
	*(*uint64)(unsafe.Add(p, ctxDr0Off)) = sendFn
	if uiSendFn != 0 {
		*(*uint64)(unsafe.Add(p, ctxDr1Off)) = uiSendFn
		*(*uint64)(unsafe.Add(p, ctxDr7Off)) = dr7EnableBoth
	} else {
		*(*uint64)(unsafe.Add(p, ctxDr7Off)) = dr7Enable
	}
	return setContext(hThread, p, contextDebugRegsOnly)
}

// resolveUISendFn walks the UI NetMan indirection chain to find the actual
// queue-inserter function (vtable[5]). Returns 0 on any failure OR if any
// intermediate result fails sanity checks. ALL THREE indirections must
// produce values in the expected memory regions:
//
//	ui_netman_instance = *(base + uiRVA)            ; must be HEAP (not 0, not in code)
//	vtable             = *(ui_netman_instance + 0)  ; MUST be in CODE/RDATA range
//	ui_send_fn         = *(vtable + 0x28)           ; MUST be in CODE range
//
// If the vtable pointer is not in code range, the chain is wrong (probably
// the global RVA shifted between D2R patches) — return 0 and let the caller
// fall back to Game-only sniffing.
//
// "Code range" for D2R is approximately [base, base + 0x4000000) — D2R is
// roughly 30MB of code. Generous bound to tolerate growth across patches.
func resolveUISendFn(hProc windows.Handle, base uint64, uiRVA uint64) (uint64, string) {
	const maxCodeSpan = uint64(0x4000000) // 64 MB sanity bound

	inCodeRange := func(addr uint64) bool {
		return addr >= base && addr < base+maxCodeSpan
	}

	rd64 := func(addr uint64) (uint64, bool) {
		var v uint64
		var read uintptr
		r1, _, _ := procReadProcessMemory.Call(
			uintptr(hProc),
			uintptr(addr),
			uintptr(unsafe.Pointer(&v)),
			8,
			uintptr(unsafe.Pointer(&read)),
		)
		if r1 == 0 || read != 8 {
			return 0, false
		}
		return v, true
	}

	netmanInstance, ok := rd64(base + uiRVA)
	if !ok {
		return 0, fmt.Sprintf("RPM failed at base+0x%x", uiRVA)
	}
	if netmanInstance == 0 {
		return 0, fmt.Sprintf("UI NetMan global @ base+0x%x is null (D2R not in game yet?)", uiRVA)
	}
	if inCodeRange(netmanInstance) {
		return 0, fmt.Sprintf("UI NetMan instance 0x%x is in CODE range — wrong RVA (expected heap pointer)", netmanInstance)
	}

	vtable, ok := rd64(netmanInstance)
	if !ok || vtable == 0 {
		return 0, fmt.Sprintf("vtable read failed at instance 0x%x", netmanInstance)
	}
	if !inCodeRange(vtable) {
		return 0, fmt.Sprintf("vtable 0x%x is NOT in code range [0x%x..0x%x] — chain is wrong",
			vtable, base, base+maxCodeSpan)
	}

	uiSendFn, ok := rd64(vtable + uiVtableSlot)
	if !ok {
		return 0, fmt.Sprintf("vtable[5] read failed at 0x%x", vtable+uiVtableSlot)
	}
	if !inCodeRange(uiSendFn) {
		return 0, fmt.Sprintf("vtable[5] 0x%x is NOT in code range — chain is wrong", uiSendFn)
	}
	return uiSendFn, ""
}

// ------------- PEB anti-debug patch -------------
//
// D2R may check PEB.BeingDebugged (offset 0x02) and PEB.NtGlobalFlag
// (offset 0xBC) to detect debuggers. We zero both via WriteProcessMemory.
//
// PEB address is found via NtQueryInformationProcess(ProcessBasicInformation).

var (
	ntdll                         = windows.NewLazySystemDLL("ntdll.dll")
	procNtQueryInformationProcess = ntdll.NewProc("NtQueryInformationProcess")
	procWriteProcessMemory        = kernel32.NewProc("WriteProcessMemory")
)

type processBasicInformation struct {
	ExitStatus                   uint32
	_                            uint32
	PebBaseAddress               uintptr
	AffinityMask                 uintptr
	BasePriority                 int32
	_                            uint32
	UniqueProcessId              uintptr
	InheritedFromUniqueProcessId uintptr
}

func patchPEBAntiDebug(hProc windows.Handle, _ uint32) error {
	var pbi processBasicInformation
	var retLen uint32
	r1, _, _ := procNtQueryInformationProcess.Call(
		uintptr(hProc),
		0, // ProcessBasicInformation
		uintptr(unsafe.Pointer(&pbi)),
		unsafe.Sizeof(pbi),
		uintptr(unsafe.Pointer(&retLen)),
	)
	if r1 != 0 || pbi.PebBaseAddress == 0 {
		return fmt.Errorf("NtQueryInformationProcess: status=0x%x", r1)
	}
	pebAddr := pbi.PebBaseAddress

	// Clear BeingDebugged (1 byte at PEB+0x02)
	zeroByte := []byte{0}
	var written uintptr
	r1, _, err := procWriteProcessMemory.Call(
		uintptr(hProc),
		pebAddr+0x02,
		uintptr(unsafe.Pointer(&zeroByte[0])),
		1,
		uintptr(unsafe.Pointer(&written)),
	)
	if r1 == 0 {
		return fmt.Errorf("WPM BeingDebugged: %v", err)
	}

	// Clear NtGlobalFlag (4 bytes at PEB+0xBC)
	zero4 := []byte{0, 0, 0, 0}
	r1, _, err = procWriteProcessMemory.Call(
		uintptr(hProc),
		pebAddr+0xBC,
		uintptr(unsafe.Pointer(&zero4[0])),
		4,
		uintptr(unsafe.Pointer(&written)),
	)
	if r1 == 0 {
		return fmt.Errorf("WPM NtGlobalFlag: %v", err)
	}
	return nil
}

// ------------- main -------------

var (
	flagPid      = flag.Int("pid", 0, "PID of D2R.exe to attach to")
	flagOut      = flag.String("out", "", "Optional file to mirror packet log into")
	flagMax      = flag.Int("max", 0, "Stop after capturing N packets (0 = unlimited)")
	flagOps      = flag.String("op", "", "Comma-separated opcode filter, e.g. 0x2f,0x38,0x30")
	flagNPC      = flag.Bool("npc", false, "Only print NPC-related outgoing packets")
	flagDryRun   = flag.Bool("dry-run", false, "Attach as debugger only — no DR0 install, no context modification")
	flagNoBP     = flag.Bool("no-bp", false, "Install DR0 but on BP fire just log RCX/RDX, don't read packet, don't modify ctx")
	flagProbe    = flag.Bool("probe", false, "Install DR0 only on initial thread (not on subsequent CREATE_THREAD events)")
	flagPatchPEB = flag.Bool("patch-peb", false, "Before attach, clear PEB.BeingDebugged & NtGlobalFlag in D2R via WPM")
	flagDuration = flag.Int("duration", 60, "Auto-detach after N seconds (safety net)")
	flagUI       = flag.Bool("ui", false, "Also install DR1 HWBP on UI NetMan vtable[5] (sell/buy/identify/etc.). OFF by default — needs verified RVA per D2R version.")
	flagUIRVA    = flag.Int64("ui-rva", uiNetManGlobalOffset, "Override UI NetMan global RVA (default 0x19ED860, may shift between D2R patches)")
)

func main() {
	flag.Parse()
	if *flagPid == 0 {
		fmt.Fprintln(os.Stderr, "usage: sniffer --pid <D2R pid> [--out file] [--max N] [--npc|--op 0x2f,0x38]")
		os.Exit(2)
	}
	opFilter, err := parseOpcodeFilter(*flagOps, *flagNPC)
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse --op: %v\n", err)
		os.Exit(2)
	}

	var outFile *os.File
	if *flagOut != "" {
		f, err := os.Create(*flagOut)
		if err != nil {
			fmt.Fprintf(os.Stderr, "open %s: %v\n", *flagOut, err)
			os.Exit(1)
		}
		outFile = f
		defer f.Close()
	}

	emit := func(line string) {
		fmt.Println(line)
		if outFile != nil {
			fmt.Fprintln(outFile, line)
			outFile.Sync()
		}
	}

	// Open D2R for ReadProcessMemory + (optional) WriteProcessMemory for PEB patch.
	access := uint32(windows.PROCESS_VM_READ | windows.PROCESS_QUERY_INFORMATION)
	if *flagPatchPEB {
		access |= windows.PROCESS_VM_WRITE | windows.PROCESS_VM_OPERATION
	}
	hProc, err := windows.OpenProcess(access, false, uint32(*flagPid))
	if err != nil {
		fmt.Fprintf(os.Stderr, "OpenProcess(%d): %v\n", *flagPid, err)
		os.Exit(1)
	}
	defer windows.CloseHandle(hProc)

	// NOTE: PEB patch is applied AFTER DebugActiveProcess (below), because
	// the kernel sets BeingDebugged DURING the attach syscall. Patching before
	// attach is a no-op — it gets immediately re-set.

	// Attach as debugger.
	r1, _, attachErr := procDebugActiveProcess.Call(uintptr(*flagPid))
	if r1 == 0 {
		fmt.Fprintf(os.Stderr, "DebugActiveProcess(%d): %v\n", *flagPid, attachErr)
		os.Exit(1)
	}
	emit(fmt.Sprintf("# attached to pid=%d", *flagPid))

	// CRITICAL: don't kill D2R when we exit / detach.
	r1, _, _ = procDebugSetProcessKillOnExit.Call(0)
	if r1 == 0 {
		emit("# WARN: DebugSetProcessKillOnExit failed — D2R will die when we exit!")
	}

	// Apply PEB anti-debug patch IMMEDIATELY after attach (kernel just set
	// BeingDebugged=1, we clear it back). Then keep clearing it in a loop —
	// D2R may poll periodically and we need to win every check.
	if *flagPatchPEB {
		if perr := patchPEBAntiDebug(hProc, uint32(*flagPid)); perr != nil {
			emit(fmt.Sprintf("# WARN: initial PEB patch failed: %v", perr))
		} else {
			emit("# initial PEB patch applied (BeingDebugged=0, NtGlobalFlag=0)")
		}
		// Background re-patcher: every 50ms reset PEB flags. Cheap WPM call.
		go func() {
			for {
				time.Sleep(50 * time.Millisecond)
				_ = patchPEBAntiDebug(hProc, uint32(*flagPid))
			}
		}()
	}

	// Detach gracefully on signal OR after --duration seconds.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	stopRequested := atomic.Bool{}
	go func() {
		select {
		case <-sigCh:
			emit("# stop requested (signal), detaching...")
		case <-time.After(time.Duration(*flagDuration) * time.Second):
			emit(fmt.Sprintf("# duration %ds reached, detaching...", *flagDuration))
		}
		stopRequested.Store(true)
	}()

	defer func() {
		procDebugActiveProcessStop.Call(uintptr(*flagPid))
		emit("# detached")
	}()

	var sendFnAddr uint64
	var uiSendFnAddr uint64
	var packetCount uint32

	// Buffer for ReadProcessMemory.
	pktBuf := make([]byte, maxPacketBytes)
	rangeBuf := make([]byte, 16) // for UI NetMan ByteRange{begin,end}

	startTime := time.Now()

	for {
		if stopRequested.Load() {
			return
		}

		var ev debugEvent
		// 200ms timeout so we can poll the stop flag.
		r1, _, _ := procWaitForDebugEventEx.Call(
			uintptr(unsafe.Pointer(&ev[0])),
			200,
		)
		if r1 == 0 {
			// timeout or error — loop and check stop flag
			continue
		}

		code := ev.code()
		tid := ev.threadID()
		continueStatus := uintptr(dbgContinue)

		switch code {
		case createProcessDebugEvent:
			base := uint64(ev.createProcessBaseOfImage())
			sendFnAddr = base + sendFnOffset

			// UI NetMan resolution is OPT-IN via --ui flag. Without --ui we
			// only install DR0 (Game NetMan), exact same behavior as the
			// pre-extension sniffer. With --ui we attempt resolve and only
			// install DR1 if all sanity checks pass.
			if *flagUI {
				addr, why := resolveUISendFn(hProc, base, uint64(*flagUIRVA))
				if addr == 0 {
					emit(fmt.Sprintf("# UI NetMan resolve FAILED: %s — DR1 will NOT be installed", why))
				} else {
					uiSendFnAddr = addr
					emit(fmt.Sprintf("# UI NetMan resolved: ui_send_fn=0x%x", uiSendFnAddr))
				}
			}

			emit(fmt.Sprintf("# create_process base=0x%x send_fn=0x%x ui_send_fn=0x%x",
				base, sendFnAddr, uiSendFnAddr))

			h := ev.createProcessThreadHandle()
			if h != 0 && sendFnAddr != 0 && !*flagDryRun {
				if err := installBothHwbpOnHandle(h, sendFnAddr, uiSendFnAddr); err != nil {
					emit(fmt.Sprintf("# initial thread HWBP set failed tid=%d: %v", tid, err))
				} else {
					if uiSendFnAddr != 0 {
						emit(fmt.Sprintf("# initial thread HWBP set tid=%d (DR0=send_fn, DR1=ui_send_fn=0x%x)",
							tid, uiSendFnAddr))
					} else {
						emit(fmt.Sprintf("# initial thread HWBP set tid=%d (DR0=send_fn only)", tid))
					}
				}
			}

		case createThreadDebugEvent:
			// In --probe mode we only install on the very first thread (the one
			// from CREATE_PROCESS), so skip subsequent threads here.
			// In --dry-run we never install.
			if *flagDryRun || *flagProbe {
				break
			}

			// Lazy UI NetMan resolution: if --ui requested but we couldn't
			// resolve at create_process (e.g. the global wasn't populated yet),
			// retry on every create_thread until it succeeds. Cheap, idempotent,
			// and protected by the same sanity checks.
			if *flagUI && uiSendFnAddr == 0 && sendFnAddr != 0 {
				base := sendFnAddr - sendFnOffset
				if addr, _ := resolveUISendFn(hProc, base, uint64(*flagUIRVA)); addr != 0 {
					uiSendFnAddr = addr
					emit(fmt.Sprintf("# ui_send_fn lazy-resolved to 0x%x", uiSendFnAddr))
				}
			}

			h := ev.createThreadHandle()
			if h != 0 && sendFnAddr != 0 {
				if err := installBothHwbpOnHandle(h, sendFnAddr, uiSendFnAddr); err != nil {
					emit(fmt.Sprintf("# thread HWBP set failed tid=%d: %v", tid, err))
				}
			}

		case exitThreadDebugEvent:
			// nothing to do — handle is closed automatically by debug subsystem

		case exitProcessDebugEvent:
			emit("# D2R exited")
			return

		case loadDllDebugEvent, unloadDllDebugEvent, outputDebugStringEvent, ripEvent:
			// no-op — pass through

		case exceptionDebugEvent:
			ec := ev.excCode()
			ea := ev.excAddress()

			isGameHit := ec == excSingleStep && sendFnAddr != 0 && ea == sendFnAddr
			isUIHit := ec == excSingleStep && uiSendFnAddr != 0 && ea == uiSendFnAddr

			if isGameHit || isUIHit {
				// HWBP fired. Read register state from the trapped thread.
				hThread, openErr := windows.OpenThread(
					0x0008|0x0010|0x0002|0x0040, // GET|SET|SUSPEND|QUERY
					false,
					tid,
				)
				if openErr != nil {
					emit(fmt.Sprintf("# OpenThread tid=%d: %v", tid, openErr))
					break
				}
				p, _, gerr := getContext(uintptr(hThread), contextFullPlusDebug)
				if gerr != nil {
					emit(fmt.Sprintf("# get ctx tid=%d: %v", tid, gerr))
					windows.CloseHandle(hThread)
					break
				}
				rcx := *(*uint64)(unsafe.Add(p, ctxRcxOff))
				rdx := *(*uint64)(unsafe.Add(p, ctxRdxOff))
				r8 := *(*uint64)(unsafe.Add(p, ctxR8Off))

				var captured []byte
				var pathTag string
				var actualSize uint64
				var pktAddr uint64

				if isGameHit {
					// Game NetMan ABI: send_fn(packet_ptr, size, channel)
					//   rcx = packet_ptr
					//   rdx = size (may be junk in upper bits)
					pathTag = "GAME"
					pktAddr = rcx
					actualSize = rdx & 0xFFFF
					if !*flagNoBP && rcx != 0 {
						readLen := maxPacketBytes
						if actualSize > 0 && int(actualSize) < readLen {
							readLen = int(actualSize)
						}
						var read uintptr
						rmRet, _, _ := procReadProcessMemory.Call(
							uintptr(hProc),
							uintptr(rcx),
							uintptr(unsafe.Pointer(&pktBuf[0])),
							uintptr(readLen),
							uintptr(unsafe.Pointer(&read)),
						)
						if rmRet != 0 && read > 0 {
							captured = make([]byte, read)
							copy(captured, pktBuf[:read])
						}
					}
				} else {
					// UI NetMan ABI: ui_send_fn(this, channel, &ByteRange{begin,end})
					//   rcx = this (NetMan instance, ignore)
					//   edx = channel (usually 0)
					//   r8  = ptr to ByteRange{void*begin; void*end} (16 bytes)
					pathTag = "UI"
					if !*flagNoBP && r8 != 0 {
						var read uintptr
						rmRet, _, _ := procReadProcessMemory.Call(
							uintptr(hProc),
							uintptr(r8),
							uintptr(unsafe.Pointer(&rangeBuf[0])),
							16,
							uintptr(unsafe.Pointer(&read)),
						)
						if rmRet != 0 && read == 16 {
							begin := *(*uint64)(unsafe.Pointer(&rangeBuf[0]))
							end := *(*uint64)(unsafe.Pointer(&rangeBuf[8]))
							pktAddr = begin
							if end > begin {
								size := end - begin
								if size > maxPacketBytes {
									size = maxPacketBytes
								}
								actualSize = size
								var read2 uintptr
								rmRet2, _, _ := procReadProcessMemory.Call(
									uintptr(hProc),
									uintptr(begin),
									uintptr(unsafe.Pointer(&pktBuf[0])),
									uintptr(size),
									uintptr(unsafe.Pointer(&read2)),
								)
								if rmRet2 != 0 && read2 > 0 {
									captured = make([]byte, read2)
									copy(captured, pktBuf[:read2])
								}
							}
						}
					}
				}

				// Set Resume Flag in EFLAGS so HWBP doesn't immediately retrigger.
				eflags := *(*uint32)(unsafe.Add(p, ctxEflagsOff))
				*(*uint32)(unsafe.Add(p, ctxEflagsOff)) = eflags | eflagsRF
				if serr := setContext(uintptr(hThread), p, contextControl); serr != nil {
					emit(fmt.Sprintf("# set ctx (RF) tid=%d: %v", tid, serr))
				}
				windows.CloseHandle(hThread)

				op := byte(0)
				if len(captured) > 0 {
					op = captured[0]
				}
				if opFilter != nil && !opFilter[op] {
					continueStatus = dbgContinue
					continue
				}
				n := atomic.AddUint32(&packetCount, 1)
				ts := time.Since(startTime).Truncate(time.Millisecond)
				note := annotatePacket(captured)
				if note != "" {
					emit(fmt.Sprintf("[%d] t+%s [%s] tid=%d ptr=0x%x size=%d op=0x%02X note=%q hex=%s",
						n, ts, pathTag, tid, pktAddr, actualSize, op, note, hex.EncodeToString(captured)))
				} else {
					emit(fmt.Sprintf("[%d] t+%s [%s] tid=%d ptr=0x%x size=%d op=0x%02X hex=%s",
						n, ts, pathTag, tid, pktAddr, actualSize, op, hex.EncodeToString(captured)))
				}

				if *flagMax > 0 && int(n) >= *flagMax {
					emit(fmt.Sprintf("# reached max=%d, detaching", *flagMax))
					return
				}
				continueStatus = dbgContinue
			} else if ec == excBreakpoint {
				// Initial debug breakpoint at debugger attach — handle and continue.
				continueStatus = dbgContinue
			} else {
				// Anything else: let the game handle it.
				continueStatus = dbgExceptionNotHandled
			}
		}

		procContinueDebugEvent.Call(
			uintptr(ev.processID()),
			uintptr(tid),
			continueStatus,
		)
	}
}
