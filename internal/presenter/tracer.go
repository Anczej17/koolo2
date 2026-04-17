package presenter

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Tracer reads packet trace entries from the in-process PacketTracer ring.
// The Rust side (rmod_sniffer.dll) hooks send_fn + dual_send_wrap with a
// trampoline that captures (timestamp, opcode, args, TID, callstack, payload)
// for every packet the game emits. Only available in Claude mode.
type Tracer struct {
	hSection  windows.Handle
	localView unsafe.Pointer
	pid       uint32
}

// TraceEntry is a single packet capture from the ring buffer.
type TraceEntry struct {
	Timestamp  uint64    // GetTickCount64 ms
	HookID     byte      // 0=send_fn, 1=dual_send_wrap
	PayloadLen uint16    // bytes captured of packet
	TID        uint32    // calling thread id
	Args       [4]uint64 // RCX, RDX, R8, R9 at hook entry
	Callstack  [16]uint64
	Payload    []byte
}

// TraceStatus is a status summary for /debug/packettrace/status.
type TraceStatus struct {
	Magic           uint32
	Enabled         uint32
	Head            uint32
	Tail            uint32
	Total           uint32
	Dropped         uint32
	SendFnVA        uint64
	DualSendWrapVA  uint64
	StubAddr        uint64
	InstalledFlags  uint32
	LastErrorCode   uint32
	D2RBase         uint64
	D2RTextEnd      uint64
	GameThreadID    uint32
	OrigSendFn      []byte // 16 bytes
	OrigDualSendWrap []byte // 16 bytes
}

func tracerSectionName(pid uint32) string {
	return fmt.Sprintf("%s_%d_t", sessionShmPrefix, pid)
}

// CreateTracer creates the tracer SHM (idempotent — opens existing if present).
func CreateTracer(pid uint32) (*Tracer, error) {
	name, err := windows.UTF16PtrFromString(tracerSectionName(pid))
	if err != nil {
		return nil, fmt.Errorf("section name: %w", err)
	}
	sd, err := windows.NewSecurityDescriptor()
	if err != nil {
		return nil, fmt.Errorf("new sd: %w", err)
	}
	if err := sd.SetDACL(nil, true, false); err != nil {
		return nil, fmt.Errorf("set dacl: %w", err)
	}
	sa := windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: sd,
		InheritHandle:      0,
	}
	hSection, err := windows.CreateFileMapping(
		windows.InvalidHandle,
		&sa,
		windows.PAGE_READWRITE,
		0,
		uint32(TraceShmSize),
		name,
	)
	if err != nil {
		return nil, fmt.Errorf("create mapping: %w", err)
	}
	base, err := windows.MapViewOfFile(
		hSection,
		windows.FILE_MAP_READ|windows.FILE_MAP_WRITE,
		0, 0,
		uintptr(TraceShmSize),
	)
	if err != nil {
		windows.CloseHandle(hSection)
		return nil, fmt.Errorf("map view: %w", err)
	}
	ptr := *(*unsafe.Pointer)(unsafe.Pointer(&base))
	writeU32(ptr, TraceOffMagic, TraceMagic)
	return &Tracer{hSection: hSection, localView: ptr, pid: pid}, nil
}

// OpenTracer opens an existing tracer SHM (created by rmod_sniffer.dll or
// the Go side via CreateTracer).
func OpenTracer(pid uint32) (*Tracer, error) {
	name, err := windows.UTF16PtrFromString(tracerSectionName(pid))
	if err != nil {
		return nil, fmt.Errorf("section name: %w", err)
	}
	hSection, err := openFileMapping(
		windows.FILE_MAP_READ|windows.FILE_MAP_WRITE,
		false,
		name,
	)
	if err != nil {
		return nil, fmt.Errorf("open tracer mapping: %w", err)
	}
	base, err := windows.MapViewOfFile(
		hSection,
		windows.FILE_MAP_READ|windows.FILE_MAP_WRITE,
		0, 0,
		uintptr(TraceShmSize),
	)
	if err != nil {
		windows.CloseHandle(hSection)
		return nil, fmt.Errorf("map view: %w", err)
	}
	ptr := *(*unsafe.Pointer)(unsafe.Pointer(&base))
	magic := readU32(ptr, TraceOffMagic)
	if magic != TraceMagic {
		windows.UnmapViewOfFile(base)
		windows.CloseHandle(hSection)
		return nil, fmt.Errorf("tracer magic mismatch: got 0x%08X, want 0x%08X", magic, TraceMagic)
	}
	return &Tracer{hSection: hSection, localView: ptr, pid: pid}, nil
}

// Enable / Disable toggle capture without changing the hook installation.
func (t *Tracer) Enable()  { writeU32(t.localView, TraceOffEnabled, 1) }
func (t *Tracer) Disable() { writeU32(t.localView, TraceOffEnabled, 0) }
func (t *Tracer) IsEnabled() bool {
	return readU32(t.localView, TraceOffEnabled) == 1
}

// Status returns the current SHM header snapshot.
func (t *Tracer) Status() TraceStatus {
	st := TraceStatus{
		Magic:           readU32(t.localView, TraceOffMagic),
		Enabled:         readU32(t.localView, TraceOffEnabled),
		Head:            readU32(t.localView, TraceOffHead),
		Tail:            readU32(t.localView, TraceOffTail),
		Total:           readU32(t.localView, TraceOffTotal),
		Dropped:         readU32(t.localView, TraceOffDropped),
		SendFnVA:        readU64(t.localView, TraceOffSendFnVA),
		DualSendWrapVA:  readU64(t.localView, TraceOffDualSendWrapVA),
		StubAddr:        readU64(t.localView, TraceOffStubAddr),
		InstalledFlags:  readU32(t.localView, TraceOffInstalledFlags),
		LastErrorCode:   readU32(t.localView, TraceOffLastErrorCode),
		D2RBase:         readU64(t.localView, TraceOffD2RBase),
		D2RTextEnd:      readU64(t.localView, TraceOffD2RTextEnd),
		GameThreadID:    readU32(t.localView, TraceOffGameThreadID),
	}
	st.OrigSendFn = readBytes(t.localView, TraceOffOrigBytesSendFn, 16)
	st.OrigDualSendWrap = readBytes(t.localView, TraceOffOrigBytesDual, 16)
	return st
}

// Drain reads all pending entries since last Drain() and advances tail.
func (t *Tracer) Drain() []TraceEntry {
	head := readU32(t.localView, TraceOffHead)
	tail := readU32(t.localView, TraceOffTail)
	if head == tail {
		return nil
	}
	var entries []TraceEntry
	for tail != head {
		e := t.readEntry(tail)
		entries = append(entries, e)
		tail = (tail + 1) % uint32(TraceMaxEntries)
	}
	writeU32(t.localView, TraceOffTail, tail)
	return entries
}

func (t *Tracer) readEntry(index uint32) TraceEntry {
	off := uintptr(TraceOffRing) + uintptr(index)*uintptr(TraceEntrySize)
	base := uintptr(t.localView) + off

	e := TraceEntry{
		Timestamp:  *(*uint64)(unsafe.Pointer(base + uintptr(TraceEntryOffTimestamp))),
		HookID:     *(*byte)(unsafe.Pointer(base + uintptr(TraceEntryOffHookID))),
		PayloadLen: *(*uint16)(unsafe.Pointer(base + uintptr(TraceEntryOffPayloadLen))),
		TID:        *(*uint32)(unsafe.Pointer(base + uintptr(TraceEntryOffTID))),
	}
	for i := 0; i < 4; i++ {
		e.Args[i] = *(*uint64)(unsafe.Pointer(base + uintptr(TraceEntryOffArgs) + uintptr(i*8)))
	}
	for i := 0; i < 16; i++ {
		e.Callstack[i] = *(*uint64)(unsafe.Pointer(base + uintptr(TraceEntryOffCallstack) + uintptr(i*8)))
	}
	pl := int(e.PayloadLen)
	if pl > TraceEntryPayloadMax {
		pl = TraceEntryPayloadMax
	}
	if pl > 0 {
		e.Payload = make([]byte, pl)
		src := unsafe.Slice((*byte)(unsafe.Pointer(base+uintptr(TraceEntryOffPayload))), pl)
		copy(e.Payload, src)
	}
	return e
}

// Close unmaps and releases.
func (t *Tracer) Close() error {
	t.Disable()
	var firstErr error
	if t.localView != nil {
		if err := windows.UnmapViewOfFile(uintptr(t.localView)); err != nil {
			firstErr = err
		}
		t.localView = nil
	}
	if t.hSection != 0 {
		if err := windows.CloseHandle(t.hSection); err != nil && firstErr == nil {
			firstErr = err
		}
		t.hSection = 0
	}
	return firstErr
}

// FormatEntry returns a one-line representation of a trace entry.
func FormatTraceEntry(e TraceEntry, d2rBase uint64) string {
	hookName := "send_fn"
	if e.HookID == 1 {
		hookName = "dual_send_wrap"
	}
	opcode := byte(0)
	if len(e.Payload) > 0 {
		opcode = e.Payload[0]
	}
	return fmt.Sprintf("[t=%d ms] hook=%s tid=%d op=0x%02X len=%d rcx=0x%X rdx=0x%X",
		e.Timestamp, hookName, e.TID, opcode, e.PayloadLen, e.Args[0], e.Args[1])
}

// FormatTraceEntryFull returns multi-line entry with full hex + callstack.
// d2rBase is used to render frames as base+0xRVA when in range, or as
// raw address with "(outside D2R)" tag when masked with 0xDEADBEEF.
func FormatTraceEntryFull(e TraceEntry, d2rBase uint64) string {
	var b strings.Builder
	b.WriteString(FormatTraceEntry(e, d2rBase))
	b.WriteByte('\n')
	if len(e.Payload) > 0 {
		fmt.Fprintf(&b, "  payload: %s\n", hex.EncodeToString(e.Payload))
	}
	fmt.Fprintf(&b, "  args: rcx=0x%X rdx=0x%X r8=0x%X r9=0x%X\n",
		e.Args[0], e.Args[1], e.Args[2], e.Args[3])
	b.WriteString("  callstack:\n")
	for i, fr := range e.Callstack {
		if fr == 0 {
			break
		}
		if (fr >> 32) == 0xDEADBEEF {
			fmt.Fprintf(&b, "    [%2d] 0x%016X (outside D2R)\n", i, fr&0xFFFFFFFF)
		} else if d2rBase != 0 && fr >= d2rBase {
			fmt.Fprintf(&b, "    [%2d] D2R+0x%X\n", i, fr-d2rBase)
		} else {
			fmt.Fprintf(&b, "    [%2d] 0x%016X\n", i, fr)
		}
	}
	return b.String()
}

// readBytes helper (readU64 already in shared.go).
func readBytes(base unsafe.Pointer, offset int, length int) []byte {
	out := make([]byte, length)
	src := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(base)+uintptr(offset))), length)
	copy(out, src)
	return out
}

// HexDumpPayload returns a multi-line hex dump for inline reports.
func HexDumpPayload(p []byte) string {
	if len(p) == 0 {
		return "(empty)"
	}
	return hex.EncodeToString(p)
}

// AnnotatePayload returns short opcode-specific decoding when known.
// Used by /debug/packettrace/dump for quick eyeballing.
func AnnotatePayload(p []byte) string {
	if len(p) == 0 {
		return ""
	}
	switch p[0] {
	case 0x54:
		if len(p) >= 20 {
			gid := binary.LittleEndian.Uint32(p[1:5])
			srcGrid := binary.LittleEndian.Uint32(p[5:9])
			srcCol := p[9]
			srcRow := p[11]
			dstGrid := binary.LittleEndian.Uint32(p[13:17])
			dstCol := p[17]
			dstRow := p[19]
			return fmt.Sprintf("0x54 ItemMove gid=0x%X src(grid=%d,%d,%d)→dst(grid=%d,%d,%d)",
				gid, srcGrid, srcCol, srcRow, dstGrid, dstCol, dstRow)
		}
	case 0x16:
		if len(p) >= 17 {
			gid := binary.LittleEndian.Uint32(p[1:5])
			x := binary.LittleEndian.Uint32(p[5:9])
			y := binary.LittleEndian.Uint32(p[9:13])
			return fmt.Sprintf("0x16 PickUp gid=0x%X (%d,%d)", gid, x, y)
		}
	case 0x41:
		if len(p) >= 13 {
			gid := binary.LittleEndian.Uint32(p[1:5])
			action := binary.LittleEndian.Uint32(p[5:9])
			return fmt.Sprintf("0x41 Interact gid=0x%X action=%d", gid, action)
		}
	case 0x33:
		if len(p) >= 22 {
			price := binary.LittleEndian.Uint32(p[1:5])
			gid := binary.LittleEndian.Uint32(p[5:9])
			npc := binary.LittleEndian.Uint32(p[9:13])
			return fmt.Sprintf("0x33 NPCSell gid=0x%X npc=0x%X price=%d", gid, npc, price)
		}
	case 0x32:
		if len(p) >= 22 {
			price := binary.LittleEndian.Uint32(p[1:5])
			gid := binary.LittleEndian.Uint32(p[5:9])
			npc := binary.LittleEndian.Uint32(p[9:13])
			return fmt.Sprintf("0x32 NPCBuy gid=0x%X npc=0x%X price=%d", gid, npc, price)
		}
	case 0x50:
		if len(p) >= 17 {
			fromL := binary.LittleEndian.Uint32(p[1:5])
			fromR := binary.LittleEndian.Uint32(p[5:9])
			toL := binary.LittleEndian.Uint32(p[9:13])
			toR := binary.LittleEndian.Uint32(p[13:17])
			return fmt.Sprintf("0x50 WeaponSwap fromL=0x%X fromR=0x%X toL=0x%X toR=0x%X",
				fromL, fromR, toL, toR)
		}
	}
	return ""
}
