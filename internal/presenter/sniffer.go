package presenter

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Sniffer reads packet captures from the in-process sniffer ring buffer.
// Only functional when rmod_sniffer.dll is loaded (Claude mode).
type Sniffer struct {
	hSection  windows.Handle
	localView unsafe.Pointer
	pid       uint32
}

// CaptureEntry is a single captured packet from the ring buffer.
type CaptureEntry struct {
	FrameNo  uint32
	TickMs   uint32
	BufID    byte   // 0=buf0 (UI NetMan), 1=buf1 (mirror)
	Opcode   byte
	DataLen  uint16
	Data     []byte // up to 256 bytes
}

// sniffSectionName returns the named mapping for the sniffer SHM.
// Uses the per-session random prefix shared with the main SHM (see shared.go).
func sniffSectionName(pid uint32) string {
	return fmt.Sprintf("%s_%d_c", sessionShmPrefix, pid)
}

// CreateSniffer creates the sniffer SHM from the Go side (fallback when
// rmod_sniffer.dll didn't create it). Initialises magic, default RVAs.
func CreateSniffer(pid uint32) (*Sniffer, error) {
	name, err := windows.UTF16PtrFromString(sniffSectionName(pid))
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
		uint32(CapShmSize),
		name,
	)
	if err != nil {
		return nil, fmt.Errorf("create mapping: %w", err)
	}

	base, err := windows.MapViewOfFile(
		hSection,
		windows.FILE_MAP_READ|windows.FILE_MAP_WRITE,
		0, 0,
		uintptr(CapShmSize),
	)
	if err != nil {
		windows.CloseHandle(hSection)
		return nil, fmt.Errorf("map view: %w", err)
	}

	ptr := *(*unsafe.Pointer)(unsafe.Pointer(&base))

	// Init header
	writeU32(ptr, CapOffMagic, CapMagic)
	writeU32(ptr, CapOffBuf0RVA, 0x19ED886)
	writeU32(ptr, CapOffBuf1RVA, 0x1F51330)
	writeU32(ptr, CapOffBufWindow, 256)

	return &Sniffer{
		hSection:  hSection,
		localView: ptr,
		pid:       pid,
	}, nil
}

// OpenSniffer connects to the sniffer SHM created by rmod_sniffer.dll.
func OpenSniffer(pid uint32) (*Sniffer, error) {
	name, err := windows.UTF16PtrFromString(sniffSectionName(pid))
	if err != nil {
		return nil, fmt.Errorf("section name: %w", err)
	}

	hSection, err := openFileMapping(
		windows.FILE_MAP_READ|windows.FILE_MAP_WRITE,
		false,
		name,
	)
	if err != nil {
		return nil, fmt.Errorf("open sniffer mapping: %w", err)
	}

	base, err := windows.MapViewOfFile(
		hSection,
		windows.FILE_MAP_READ|windows.FILE_MAP_WRITE,
		0, 0,
		uintptr(CapShmSize),
	)
	if err != nil {
		windows.CloseHandle(hSection)
		return nil, fmt.Errorf("map sniffer view: %w", err)
	}

	ptr := *(*unsafe.Pointer)(unsafe.Pointer(&base))

	// Validate magic.
	magic := readU32(ptr, CapOffMagic)
	if magic != CapMagic {
		windows.UnmapViewOfFile(base)
		windows.CloseHandle(hSection)
		return nil, fmt.Errorf("sniffer magic mismatch: got 0x%08X, want 0x%08X", magic, CapMagic)
	}

	return &Sniffer{
		hSection:  hSection,
		localView: ptr,
		pid:       pid,
	}, nil
}

// Enable starts packet capture.
func (s *Sniffer) Enable() {
	writeU32(s.localView, CapOffEnabled, 1)
}

// Disable pauses packet capture.
func (s *Sniffer) Disable() {
	writeU32(s.localView, CapOffEnabled, 0)
}

// IsEnabled returns true if capture is active.
func (s *Sniffer) IsEnabled() bool {
	return readU32(s.localView, CapOffEnabled) == 1
}

// Stats returns diagnostic counters.
func (s *Sniffer) Stats() (total, dropped, frame uint32) {
	total = readU32(s.localView, CapOffTotal)
	dropped = readU32(s.localView, CapOffDropped)
	frame = readU32(s.localView, CapOffFrame)
	return
}

// Drain reads all available entries from the ring buffer and advances the tail.
func (s *Sniffer) Drain() []CaptureEntry {
	head := readU32(s.localView, CapOffHead)
	tail := readU32(s.localView, CapOffTail)

	if head == tail {
		return nil // empty
	}

	var entries []CaptureEntry
	for tail != head {
		e := s.readEntry(uint32(tail))
		entries = append(entries, e)
		tail = (tail + 1) % uint32(CapMaxEntries)
	}

	// Advance tail
	writeU32(s.localView, CapOffTail, tail)
	return entries
}

func (s *Sniffer) readEntry(index uint32) CaptureEntry {
	off := uintptr(CapOffRing) + uintptr(index)*uintptr(CapEntrySize)
	base := uintptr(s.localView) + off

	e := CaptureEntry{
		FrameNo: *(*uint32)(unsafe.Pointer(base)),
		TickMs:  *(*uint32)(unsafe.Pointer(base + 4)),
		BufID:   *(*byte)(unsafe.Pointer(base + 8)),
		Opcode:  *(*byte)(unsafe.Pointer(base + 9)),
		DataLen: *(*uint16)(unsafe.Pointer(base + 10)),
	}

	dataLen := int(e.DataLen)
	if dataLen > CapEntryDataMax {
		dataLen = CapEntryDataMax
	}
	if dataLen > 0 {
		e.Data = make([]byte, dataLen)
		src := unsafe.Slice((*byte)(unsafe.Pointer(base+uintptr(CapEntryDataOff))), dataLen)
		copy(e.Data, src)
	}

	return e
}

// SetBufferRVAs configures which D2R buffers to monitor.
func (s *Sniffer) SetBufferRVAs(buf0RVA, buf1RVA uint32) {
	writeU32(s.localView, CapOffBuf0RVA, buf0RVA)
	writeU32(s.localView, CapOffBuf1RVA, buf1RVA)
}

// Close unmaps and releases resources.
func (s *Sniffer) Close() error {
	s.Disable()
	var firstErr error
	if s.localView != nil {
		if err := windows.UnmapViewOfFile(uintptr(s.localView)); err != nil {
			firstErr = err
		}
		s.localView = nil
	}
	if s.hSection != 0 {
		if err := windows.CloseHandle(s.hSection); err != nil && firstErr == nil {
			firstErr = err
		}
		s.hSection = 0
	}
	return firstErr
}

// FormatEntry returns a human-readable one-line representation.
func FormatEntry(e CaptureEntry) string {
	bufName := "buf0"
	if e.BufID == 1 {
		bufName = "buf1"
	}

	// Show first 32 bytes of hex data (or less)
	showLen := int(e.DataLen)
	if showLen > 32 {
		showLen = 32
	}
	hexStr := ""
	if len(e.Data) > 0 {
		hexStr = hex.EncodeToString(e.Data[:showLen])
	}

	return fmt.Sprintf("[%s] tick=%d frame=%d op=0x%02X len=%d hex=%s",
		bufName, e.TickMs, e.FrameNo, e.Opcode, e.DataLen, hexStr)
}

// FormatEntryFull returns full hex dump with field annotations for known opcodes.
func FormatEntryFull(e CaptureEntry) string {
	var b strings.Builder
	b.WriteString(FormatEntry(e))
	b.WriteByte('\n')

	if len(e.Data) == 0 {
		return b.String()
	}

	// Full hex dump
	b.WriteString("  full: ")
	b.WriteString(hex.EncodeToString(e.Data))
	b.WriteByte('\n')

	// Field annotations for known opcodes
	switch e.Opcode {
	case 0x50: // weapon swap
		if len(e.Data) >= 25 {
			fromL := binary.LittleEndian.Uint32(e.Data[1:5])
			fromR := binary.LittleEndian.Uint32(e.Data[5:9])
			toL := binary.LittleEndian.Uint32(e.Data[9:13])
			toR := binary.LittleEndian.Uint32(e.Data[13:17])
			fmt.Fprintf(&b, "  0x50 SWAP: fromL=%d fromR=%d toL=%d toR=%d\n", fromL, fromR, toL, toR)
			if len(e.Data) >= 30 {
				fmt.Fprintf(&b, "  trailing: %02x %02x %02x %02x %02x\n",
					e.Data[25], e.Data[26], e.Data[27], e.Data[28], e.Data[29])
			}
		}
	case 0x3C: // skill select
		if len(e.Data) >= 5 {
			skillID := binary.LittleEndian.Uint16(e.Data[1:3])
			btn := e.Data[4]
			btnName := "right"
			if btn == 0x80 {
				btnName = "left"
			}
			fmt.Fprintf(&b, "  0x3C SKILL: id=%d btn=%s\n", skillID, btnName)
		}
	case 0x33: // sell
		if len(e.Data) >= 9 {
			price := binary.LittleEndian.Uint32(e.Data[1:5])
			gid := binary.LittleEndian.Uint32(e.Data[5:9])
			fmt.Fprintf(&b, "  0x33 SELL: price=%d itemGID=%d\n", price, gid)
		}
	case 0x32: // buy/gamble
		if len(e.Data) >= 13 {
			pGID := binary.LittleEndian.Uint32(e.Data[1:5])
			iGID := binary.LittleEndian.Uint32(e.Data[5:9])
			action := binary.LittleEndian.Uint32(e.Data[9:13])
			fmt.Fprintf(&b, "  0x32 INTERACT: pGID=%d iGID=%d action=0x%X\n", pGID, iGID, action)
		}
	}

	return b.String()
}
