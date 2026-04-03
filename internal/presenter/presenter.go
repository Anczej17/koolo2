package presenter

import (
	"fmt"
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
	hProc       windows.Handle // kept open for ReadProcessMemory polling
	remoteBuf   uintptr        // shared buffer allocated in D2R via VirtualAllocEx
	initialized bool
	modulePath  string
}

func New(pid uint32, modulePath string) *Presenter {
	return &Presenter{
		pid:        pid,
		modulePath: modulePath,
	}
}

// Init allocates a shared buffer in D2R, manual-maps the DLL, and triggers Init via APC.
func (p *Presenter) Init(fnDispatch uintptr) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.initialized {
		return nil
	}

	const processAccess = windows.PROCESS_VM_OPERATION | windows.PROCESS_VM_READ | windows.PROCESS_VM_WRITE

	hProc, err := ntapi.OpenProcess(processAccess, p.pid)
	if err != nil {
		return fmt.Errorf("open process: %w", err)
	}
	p.hProc = hProc

	// 1. Allocate shared buffer in D2R (no named mapping — avoids namespace issues).
	remoteBuf, err := virtualAllocEx(hProc, 0, SharedBufSize,
		windows.MEM_COMMIT|windows.MEM_RESERVE, windows.PAGE_READWRITE)
	if err != nil {
		ntapi.CloseHandle(hProc)
		return fmt.Errorf("alloc shared buf: %w", err)
	}
	p.remoteBuf = remoteBuf

	// 2. Find game thread ID (first thread of D2R = main/game thread).
	gameThreadID := findFirstThread(p.pid)

	// 3. Write header + dispatch function + thread ID to remote buffer.
	header := make([]byte, 0x100)
	writeLocal32(header, offMagic, SharedMagic)
	writeLocal32(header, offVersion, SharedVersion)
	writeLocal64(header, offFnSendPacket, uint64(fnDispatch))
	writeLocal32(header, offGameThreadID, gameThreadID)
	if err := windows.WriteProcessMemory(hProc, remoteBuf, &header[0], uintptr(len(header)), nil); err != nil {
		ntapi.CloseHandle(hProc)
		return fmt.Errorf("write header: %w", err)
	}

	// 3. Manual-map DLL + execute Init via APC.
	if err := loadModule(p.pid, p.modulePath, remoteBuf); err != nil {
		ntapi.CloseHandle(hProc)
		return fmt.Errorf("load module: %w", err)
	}

	// 4. Poll remote buffer for ready_flag.
	deadline := time.Now().Add(initReadyTimeout)
	probe := make([]byte, 0x40)
	for time.Now().Before(deadline) {
		_ = windows.ReadProcessMemory(hProc, remoteBuf, &probe[0], uintptr(len(probe)), nil)
		if readLocal32(probe, offReadyFlag) == 1 {
			p.initialized = true
			return nil
		}
		time.Sleep(time.Millisecond)
	}

	// Timed out.
	_ = windows.ReadProcessMemory(hProc, remoteBuf, &probe[0], uintptr(len(probe)), nil)
	debugStep := readLocal32(probe, offDebugStep)
	errCode := readLocal32(probe, offErrorCode)
	ntapi.CloseHandle(hProc)
	p.hProc = 0
	if errCode != 0 {
		return fmt.Errorf("runtime module error 0x%X at step 0x%X", errCode, debugStep)
	}
	if debugStep == 0 {
		return fmt.Errorf("runtime module: DLL did not start (step=0)")
	}
	return fmt.Errorf("runtime module crashed at step 0x%X", debugStep)
}

// SendPacket sends a game packet via the Present hook (in-process, frame-synchronized).
func (p *Presenter) SendPacket(packet []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.initialized || p.hProc == 0 {
		return fmt.Errorf("presenter not initialized")
	}

	if len(packet) == 0 || len(packet) > maxPacketPayload {
		return fmt.Errorf("packet size %d out of range", len(packet))
	}

	// Write packet data first, then command header. Two separate writes
	// to avoid overwriting fn_send_packet and other fields between 0x0C and 0x100.
	if err := windows.WriteProcessMemory(p.hProc, p.remoteBuf+uintptr(offPacketData),
		&packet[0], uintptr(len(packet)), nil); err != nil {
		return fmt.Errorf("write packet data: %w", err)
	}

	// Write command header (12 bytes: cmd_flag + status + cmd_type + pkt_size)
	hdr := make([]byte, 16)
	writeLocal32(hdr, 0, 1)                        // offCommandFlag = trigger
	writeLocal32(hdr, 4, StatusBusy)               // offStatusFlag
	writeLocal32(hdr, 8, CmdSendPacket)            // offCommandType
	writeLocal32(hdr, 12, uint32(len(packet)))     // offPacketSize
	if err := windows.WriteProcessMemory(p.hProc, p.remoteBuf+uintptr(offCommandFlag),
		&hdr[0], 16, nil); err != nil {
		return fmt.Errorf("write command hdr: %w", err)
	}

	// Poll for completion.
	probe := make([]byte, 0x20)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_ = windows.ReadProcessMemory(p.hProc, p.remoteBuf, &probe[0], uintptr(len(probe)), nil)
		status := readLocal32(probe, offStatusFlag)
		if status == StatusDone {
			return nil
		}
		if status == StatusError {
			ec := readLocal32(probe, offErrorCode)
			return fmt.Errorf("send error 0x%X", ec)
		}
		time.Sleep(100 * time.Microsecond)
	}
	return fmt.Errorf("send timeout")
}

// SetCursor writes cursor coordinates to the shared buffer (Phase 8D).
func (p *Presenter) SetCursor(x, y int32) {
	if !p.initialized || p.hProc == 0 {
		return
	}
	buf := make([]byte, 8)
	writeLocal32(buf, 0, uint32(x))
	writeLocal32(buf, 4, uint32(y))
	_ = windows.WriteProcessMemory(p.hProc, p.remoteBuf+uintptr(offCursorX), &buf[0], 8, nil)
}

// SetKeyState writes key state to the shared buffer (Phase 8D).
func (p *Presenter) SetKeyState(key byte, active bool) {
	if !p.initialized || p.hProc == 0 {
		return
	}
	val := byte(0)
	if active {
		val = 1
	}
	buf := []byte{key, val}
	_ = windows.WriteProcessMemory(p.hProc, p.remoteBuf+uintptr(offTargetKey), &buf[0], 2, nil)
}

// IsReady returns true if the DLL has installed the Present hook.
func (p *Presenter) IsReady() bool {
	return p.initialized
}

// CursorBufAddr returns the address of the cursor X/Y region in the remote buffer.
// The GetCursorPos trampoline should read from this address (8 bytes: X:u32, Y:u32).
func (p *Presenter) CursorBufAddr() uintptr {
	if p.remoteBuf == 0 {
		return 0
	}
	return p.remoteBuf + uintptr(offCursorX)
}

// Close releases resources.
func (p *Presenter) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.hProc != 0 {
		ntapi.CloseHandle(p.hProc)
		p.hProc = 0
	}
	p.initialized = false
}

// ---------------------------------------------------------------------------
// Local buffer helpers (read/write to Go []byte, not remote memory)
// ---------------------------------------------------------------------------

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

func readLocal32(buf []byte, off int) uint32 {
	return *(*uint32)(unsafe.Pointer(&buf[off]))
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
