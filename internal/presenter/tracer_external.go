package presenter

// External (out-of-process) PacketTracer.
//
// Polls D2R buf0 / buf1 via ReadProcessMemory; when bytes change we know a
// packet was just emitted. We then enumerate D2R threads, SuspendThread one
// at a time, GetThreadContext, and check whether the thread's RIP is inside
// send_fn or dual_send_wrap. If yes, that thread is the sender — we read
// 16-deep RBP stack walk via RPM, then resume.
//
// Zero D2R code modification → Arxan happy. Slower than in-process (each
// RPM is a syscall), but reliable.

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	traceProcAllAccess    uintptr = 0x001F0FFF
	traceTHReadCtx        uint32  = 0x0008
	traceTHSuspendResume  uint32  = 0x0002
	traceTH32CSSnapThread uint32  = 0x00000004
	traceCtxControlInt    uint32  = 0x00100003 // CONTEXT_CONTROL | CONTEXT_INTEGER

	traceBuf0RVA   uintptr = 0x019ED886
	traceBuf1RVA   uintptr = 0x01F51330
	traceBufWindow int     = 64
	tracePollMs            = 5
)

type traceThreadEntry32 struct {
	Size           uint32
	Usage          uint32
	ThreadID       uint32
	OwnerProcessID uint32
	BasePri        int32
	DeltaPri       int32
	Flags          uint32
}

// External tracer state.
type ExternalTracer struct {
	pid      uint32
	d2rBase  uintptr
	sendFnVA uintptr
	dualVA   uintptr
	sniffer  *Tracer // SHM ring writer

	hProcess windows.Handle
	stop     atomic.Bool
	once     sync.Once
}

var (
	activeExtTracer *ExternalTracer
	extMu           sync.Mutex
)

func StartExternalTracer(pid uint32, d2rBase, sendFn, dual uintptr, snk *Tracer) (*ExternalTracer, error) {
	extMu.Lock()
	defer extMu.Unlock()
	if activeExtTracer != nil && !activeExtTracer.stop.Load() {
		return activeExtTracer, nil
	}
	hProc, err := windows.OpenProcess(windows.PROCESS_VM_READ|windows.PROCESS_VM_OPERATION|windows.PROCESS_QUERY_INFORMATION, false, pid)
	if err != nil {
		return nil, fmt.Errorf("openprocess pid=%d: %w", pid, err)
	}
	t := &ExternalTracer{
		pid: pid, d2rBase: d2rBase, sendFnVA: sendFn, dualVA: dual,
		sniffer: snk, hProcess: hProc,
	}
	// Bookkeeping in the SHM header so /debug/packettrace/status reflects state
	if snk != nil && snk.localView != nil {
		writeU32(snk.localView, TraceOffEnabled, 1)
		writeU32(snk.localView, TraceOffInstalledFlags, 0x20) // bit5 = external mode
		writeU64(snk.localView, TraceOffSendFnVA, uint64(sendFn))
		writeU64(snk.localView, TraceOffDualSendWrapVA, uint64(dual))
		writeU64(snk.localView, TraceOffD2RBase, uint64(d2rBase))
		writeU64(snk.localView, TraceOffD2RTextEnd, uint64(d2rBase)+0x2800000)
	}
	activeExtTracer = t
	go t.loop()
	return t, nil
}

func StopExternalTracer() {
	extMu.Lock()
	defer extMu.Unlock()
	if activeExtTracer == nil {
		return
	}
	activeExtTracer.stop.Store(true)
	if activeExtTracer.sniffer != nil && activeExtTracer.sniffer.localView != nil {
		writeU32(activeExtTracer.sniffer.localView, TraceOffEnabled, 0)
		writeU32(activeExtTracer.sniffer.localView, TraceOffInstalledFlags, 0)
	}
	if activeExtTracer.hProcess != 0 {
		windows.CloseHandle(activeExtTracer.hProcess)
		activeExtTracer.hProcess = 0
	}
	activeExtTracer = nil
}

func (t *ExternalTracer) loop() {
	// CONTINUOUS thread RIP polling: send_fn / dual_send_wrap return in
	// microseconds, so waiting for buf0/buf1 byte change misses the window.
	// Strategy: enumerate D2R threads in tight loop, suspend each just long
	// enough to read its RIP, and if RIP is in [send_fn..+256] or
	// [dual_send_wrap..+256] grab the full context + payload via RPM.
	//
	// Trade-off: high CPU when many threads, but nothing else catches a
	// 1-microsecond function call.

	prev0 := make([]byte, traceBufWindow)
	prev1 := make([]byte, traceBufWindow)
	t.readMem(t.d2rBase+traceBuf0RVA, prev0)
	t.readMem(t.d2rBase+traceBuf1RVA, prev1)
	cur0 := make([]byte, traceBufWindow)
	cur1 := make([]byte, traceBufWindow)

	for {
		if t.stop.Load() {
			return
		}
		// Continuous: enumerate threads, check each RIP. No sleep — pure spin.
		// On modern CPUs this keeps one core busy but reliably catches send_fn.
		t.scanAllThreads(prev0, prev1)
		// Also check buffer change — if any new packet appeared even though
		// we missed RIP, we still want the payload bytes.
		if t.readMem(t.d2rBase+traceBuf0RVA, cur0) {
			if bytesDiffer(prev0, cur0) {
				copy(prev0, cur0)
				t.pushEntry(0, 0, 0, 0, 0, uint64(len(cur0)), 0, 0, append([]byte(nil), cur0...))
				// Buffer changed but we may have already captured the sender
				// via scanAllThreads. Don't double-push if last entry has same
				// payload — for now accept potential dups.
			}
		}
		if t.readMem(t.d2rBase+traceBuf1RVA, cur1) {
			if bytesDiffer(prev1, cur1) {
				copy(prev1, cur1)
				t.pushEntry(1, 0, 0, 0, 0, uint64(len(cur1)), 0, 0, append([]byte(nil), cur1...))
			}
		}
	}
}

// scanAllThreads iterates D2R threads ONCE and snapshots any whose RIP is in
// send_fn or dual_send_wrap range.
func (t *ExternalTracer) scanAllThreads(buf0, buf1 []byte) {
	snap, _, _ := procCreateToolhelp32Snapshot.Call(uintptr(traceTH32CSSnapThread), 0)
	if snap == 0 || snap == ^uintptr(0) {
		return
	}
	defer windows.CloseHandle(windows.Handle(snap))

	var te traceThreadEntry32
	te.Size = uint32(unsafe.Sizeof(te))
	r, _, _ := procThread32First.Call(snap, uintptr(unsafe.Pointer(&te)))
	if r == 0 {
		return
	}
	for {
		if te.OwnerProcessID == t.pid {
			t.tryRipQuickCheck(te.ThreadID, buf0, buf1)
		}
		te.Size = uint32(unsafe.Sizeof(te))
		r, _, _ := procThread32Next.Call(snap, uintptr(unsafe.Pointer(&te)))
		if r == 0 {
			break
		}
	}
}

// tryRipQuickCheck opens thread, suspends, reads RIP only. If in range,
// captures full snapshot. Otherwise resumes immediately.
func (t *ExternalTracer) tryRipQuickCheck(tid uint32, buf0, buf1 []byte) {
	hThread, _, _ := procOpenThread.Call(uintptr(traceTHReadCtx|traceTHSuspendResume), 0, uintptr(tid))
	if hThread == 0 {
		return
	}
	defer windows.CloseHandle(windows.Handle(hThread))
	susp, _, _ := procSuspendThread.Call(hThread)
	if susp == 0xFFFFFFFF {
		return
	}
	defer procResumeThread.Call(hThread)

	ctx := makeAlignedContext()
	*(*uint32)(unsafe.Pointer(&ctx[0x30])) = traceCtxControlInt
	gc, _, _ := procGetThreadContext.Call(hThread, uintptr(unsafe.Pointer(&ctx[0])))
	if gc == 0 {
		return
	}
	rip := *(*uint64)(unsafe.Pointer(&ctx[0xF8]))
	inSend := t.sendFnVA != 0 && rip >= uint64(t.sendFnVA) && rip < uint64(t.sendFnVA)+512
	inDual := t.dualVA != 0 && rip >= uint64(t.dualVA) && rip < uint64(t.dualVA)+512
	if !inSend && !inDual {
		return
	}

	rcx := *(*uint64)(unsafe.Pointer(&ctx[0x80]))
	rdx := *(*uint64)(unsafe.Pointer(&ctx[0x88]))
	r8 := *(*uint64)(unsafe.Pointer(&ctx[0xB8]))
	r9 := *(*uint64)(unsafe.Pointer(&ctx[0xC0]))
	rbp := *(*uint64)(unsafe.Pointer(&ctx[0xA0]))

	hookID := uint8(0)
	payload := buf0
	if inDual {
		hookID = 1
		payload = buf1
	}
	if direct := t.readPacketArg(rcx, rdx); len(direct) > 0 {
		payload = direct
	}
	t.pushEntry(hookID, tid, rip, rbp, rcx, rdx, r8, r9, payload)
}

func bytesDiffer(a, b []byte) bool {
	if len(a) != len(b) {
		return true
	}
	for i := range a {
		if a[i] != b[i] {
			return true
		}
	}
	return false
}

func (t *ExternalTracer) readMem(addr uintptr, out []byte) bool {
	var n uintptr
	err := windows.ReadProcessMemory(t.hProcess, addr, &out[0], uintptr(len(out)), &n)
	return err == nil && int(n) == len(out)
}

func (t *ExternalTracer) readPacketArg(ptr, size uint64) []byte {
	if ptr < 0x10000 || ptr >= 0x0000800000000000 {
		return nil
	}
	if size == 0 || size > TraceEntryPayloadMax {
		return nil
	}
	out := make([]byte, int(size))
	if !t.readMem(uintptr(ptr), out) {
		return nil
	}
	return out
}

// snapshotForBuf is called immediately after a packet appeared in buf0 or
// buf1. It enumerates D2R threads, suspends each, reads its RIP, and if the
// RIP is inside send_fn or dual_send_wrap, captures the full register state +
// RBP-walked callstack and pushes a TraceEntry.
func (t *ExternalTracer) snapshotForBuf(bufID int, payload []byte) {
	if t.sniffer == nil {
		return
	}
	snap, _, err := procCreateToolhelp32Snapshot.Call(uintptr(traceTH32CSSnapThread), 0)
	if snap == 0 || snap == ^uintptr(0) {
		_ = err
		return
	}
	defer windows.CloseHandle(windows.Handle(snap))

	var te traceThreadEntry32
	te.Size = uint32(unsafe.Sizeof(te))
	r, _, _ := procThread32First.Call(snap, uintptr(unsafe.Pointer(&te)))
	if r == 0 {
		return
	}
	for {
		if te.OwnerProcessID == t.pid {
			t.tryThreadSnapshot(te.ThreadID, bufID, payload)
		}
		te.Size = uint32(unsafe.Sizeof(te))
		r, _, _ := procThread32Next.Call(snap, uintptr(unsafe.Pointer(&te)))
		if r == 0 {
			break
		}
	}
}

func (t *ExternalTracer) tryThreadSnapshot(tid uint32, bufID int, payload []byte) {
	hThread, _, _ := procOpenThread.Call(uintptr(traceTHReadCtx|traceTHSuspendResume), 0, uintptr(tid))
	if hThread == 0 {
		return
	}
	defer windows.CloseHandle(windows.Handle(hThread))
	susp, _, _ := procSuspendThread.Call(hThread)
	if susp == 0xFFFFFFFF {
		return
	}
	defer procResumeThread.Call(hThread)

	// Get context — we need RIP, RBP, RCX, RDX, R8, R9, RSP
	ctx := makeAlignedContext()
	*(*uint32)(unsafe.Pointer(&ctx[0x30])) = traceCtxControlInt
	gc, _, _ := procGetThreadContext.Call(hThread, uintptr(unsafe.Pointer(&ctx[0])))
	if gc == 0 {
		return
	}
	rip := *(*uint64)(unsafe.Pointer(&ctx[0xF8]))
	inSend := t.sendFnVA != 0 && rip >= uint64(t.sendFnVA) && rip < uint64(t.sendFnVA)+256
	inDual := t.dualVA != 0 && rip >= uint64(t.dualVA) && rip < uint64(t.dualVA)+256
	if !inSend && !inDual {
		return
	}

	rcx := *(*uint64)(unsafe.Pointer(&ctx[0x80]))
	rdx := *(*uint64)(unsafe.Pointer(&ctx[0x88]))
	r8 := *(*uint64)(unsafe.Pointer(&ctx[0xB8]))
	r9 := *(*uint64)(unsafe.Pointer(&ctx[0xC0]))
	rbp := *(*uint64)(unsafe.Pointer(&ctx[0xA0]))

	hookID := uint8(0)
	if inDual {
		hookID = 1
	}
	t.pushEntry(hookID, tid, rip, rbp, rcx, rdx, r8, r9, payload)
}

// makeAlignedContext returns a 16-byte-aligned 1232-byte CONTEXT buffer.
func makeAlignedContext() []byte {
	buf := make([]byte, 1232+16)
	addr := uintptr(unsafe.Pointer(&buf[0]))
	pad := (16 - addr%16) % 16
	return buf[pad : pad+1232]
}

// pushEntry writes a single TraceEntry into the SHM ring.
func (t *ExternalTracer) pushEntry(hookID uint8, tid uint32, rip, rbp, rcx, rdx, r8, r9 uint64, payload []byte) {
	if t.sniffer == nil || t.sniffer.localView == nil {
		return
	}
	v := t.sniffer.localView
	head := readU32(v, TraceOffHead)
	tail := readU32(v, TraceOffTail)
	maxEntries := uint32(TraceMaxEntries)
	nextHead := (head + 1) % maxEntries
	if nextHead == tail {
		dropped := readU32(v, TraceOffDropped)
		writeU32(v, TraceOffDropped, dropped+1)
		return
	}

	entryOff := uintptr(TraceOffRing) + uintptr(head)*uintptr(TraceEntrySize)
	base := uintptr(v) + entryOff

	*(*uint64)(unsafe.Pointer(base + uintptr(TraceEntryOffTimestamp))) = uint64(time.Now().UnixMilli())
	*(*byte)(unsafe.Pointer(base + uintptr(TraceEntryOffHookID))) = hookID
	plen := len(payload)
	if plen > TraceEntryPayloadMax {
		plen = TraceEntryPayloadMax
	}
	*(*uint16)(unsafe.Pointer(base + uintptr(TraceEntryOffPayloadLen))) = uint16(plen)
	*(*uint32)(unsafe.Pointer(base + uintptr(TraceEntryOffTID))) = tid
	*(*uint64)(unsafe.Pointer(base + uintptr(TraceEntryOffArgs+0))) = rcx
	*(*uint64)(unsafe.Pointer(base + uintptr(TraceEntryOffArgs+8))) = rdx
	*(*uint64)(unsafe.Pointer(base + uintptr(TraceEntryOffArgs+16))) = r8
	*(*uint64)(unsafe.Pointer(base + uintptr(TraceEntryOffArgs+24))) = r9

	// Callstack via RPM — read [rbp] = saved_rbp, [rbp+8] = return_addr
	d2rBase := uint64(t.d2rBase)
	d2rEnd := d2rBase + 0x2800000
	stackOff := base + uintptr(TraceEntryOffCallstack)
	// First slot = current RIP (filtered)
	first := rip
	if !inRange(first, d2rBase, d2rEnd) {
		first = 0xDEADBEEF00000000 | (first & 0xFFFFFFFF)
	}
	*(*uint64)(unsafe.Pointer(stackOff)) = first
	cur := rbp
	for i := 1; i < 16; i++ {
		if cur < 0x10000 || cur >= 0x7FFFFFFFFFFF || cur&7 != 0 {
			*(*uint64)(unsafe.Pointer(stackOff + uintptr(i*8))) = 0
			break
		}
		var pair [16]byte
		var n uintptr
		err := windows.ReadProcessMemory(t.hProcess, uintptr(cur), &pair[0], 16, &n)
		if err != nil || n != 16 {
			*(*uint64)(unsafe.Pointer(stackOff + uintptr(i*8))) = 0
			break
		}
		savedRbp := *(*uint64)(unsafe.Pointer(&pair[0]))
		retAddr := *(*uint64)(unsafe.Pointer(&pair[8]))
		val := retAddr
		if !inRange(val, d2rBase, d2rEnd) {
			val = 0xDEADBEEF00000000 | (val & 0xFFFFFFFF)
		}
		*(*uint64)(unsafe.Pointer(stackOff + uintptr(i*8))) = val
		if savedRbp <= cur {
			break
		}
		cur = savedRbp
	}

	// Payload — copy from buffer (we already read the buffer in the poll)
	if plen > 0 {
		dst := unsafe.Slice((*byte)(unsafe.Pointer(base+uintptr(TraceEntryOffPayload))), plen)
		copy(dst, payload[:plen])
	}

	writeU32(v, TraceOffHead, nextHead)
	total := readU32(v, TraceOffTotal)
	writeU32(v, TraceOffTotal, total+1)
}

func inRange(v, lo, hi uint64) bool { return v >= lo && v <= hi }

// Win32 lazy procedures reused from elsewhere (declared once here for package).
var (
	traceKernel32                = windows.NewLazySystemDLL("kernel32.dll")
	procCreateToolhelp32Snapshot = traceKernel32.NewProc("CreateToolhelp32Snapshot")
	procThread32First            = traceKernel32.NewProc("Thread32First")
	procThread32Next             = traceKernel32.NewProc("Thread32Next")
	procOpenThread               = traceKernel32.NewProc("OpenThread")
	procSuspendThread            = traceKernel32.NewProc("SuspendThread")
	procResumeThread             = traceKernel32.NewProc("ResumeThread")
	procGetThreadContext         = traceKernel32.NewProc("GetThreadContext")
)
