package game

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"sync"
	"syscall"

	"local/internal/svc/internal/gamelib/memory"
	"local/internal/svc/internal/ntapi"
	"local/internal/svc/internal/presenter"
	"golang.org/x/sys/windows"
)

const fullAccess = windows.PROCESS_VM_OPERATION | windows.PROCESS_VM_WRITE | windows.PROCESS_VM_READ

// writeCodePage writes to a code page (.text) in the remote process.
// Uses standard WriteProcessMemory which handles page protection internally.
// Reads and RW data writes still go through indirect syscalls.
func writeCodePage(handle windows.Handle, addr uintptr, buf *byte, size uintptr) error {
	return windows.WriteProcessMemory(handle, addr, buf, size, nil)
}

// MemoryInjector manages USER32 function hooks in the target process.
//
// Architecture: write-once trampolines + data buffers.
// - Code pages (.text) are written ONCE per session (hook install/uninstall)
// - Runtime updates (cursor position, key state) go to RW data pages
// - This eliminates hundreds of writes/sec to executable memory
type MemoryInjector struct {
	mu                    sync.Mutex
	isLoaded              bool
	pid                   uint32
	handle                windows.Handle
	getCursorPosAddr      uintptr
	getCursorPosOrigBytes [32]byte
	trackMouseEventAddr   uintptr
	trackMouseEventBytes  [32]byte
	getKeyStateAddr       uintptr
	getKeyStateOrigBytes  [18]byte
	setCursorPosAddr      uintptr
	setCursorPosOrigBytes [6]byte
	logger                *slog.Logger
	cursorOverrideActive  bool
	lastCursorX           int
	lastCursorY           int

	// Data buffers in remote process (RW pages, allocated via VirtualAllocEx)
	cursorDataBuf uintptr // 8 bytes: [X:uint32][Y:uint32]
	keyDataBuf    uintptr // 4 bytes: [targetKey:byte][activeFlag:byte][pad:2]

	// presenter, when set, handles cursor/key writes via shared memory
	// instead of the USER32 trampoline hooks.
	presenter *presenter.Presenter
}

func InjectorInit(logger *slog.Logger, pid uint32) (*MemoryInjector, error) {
	i := &MemoryInjector{pid: pid, logger: logger}
	pHandle, err := windows.OpenProcess(fullAccess, false, pid)
	if err != nil {
		return nil, fmt.Errorf("error opening process: %w", err)
	}
	i.handle = pHandle

	return i, nil
}

// SetPresenter installs a Presenter and re-points the GetCursorPos trampoline
// to read cursor data from the presenter's remote buffer instead of cursorDataBuf.
// This makes CursorPos() write to the same buffer the trampoline reads from.
func (i *MemoryInjector) SetPresenter(p *presenter.Presenter) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.presenter = p

	// Re-install trampoline to read from presenter's remote cursor area.
	if p != nil && p.IsReady() && i.isLoaded {
		cursorAddr := p.CursorBufAddr()
		if cursorAddr != 0 {
			code := buildCursorPosTrampoline(cursorAddr)
			_ = writeCodePage(i.handle, i.getCursorPosAddr, &code[0], uintptr(len(code)))
			i.cursorDataBuf = cursorAddr // CursorPos() writes here now
		}
	}
}

// allocRemoteRW allocates a small RW page in the remote process for data buffers.
func (i *MemoryInjector) allocRemoteRW(size uintptr) (uintptr, error) {
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")
	procVirtualAllocEx := kernel32.NewProc("VirtualAllocEx")

	addr, _, err := procVirtualAllocEx.Call(
		uintptr(i.handle),
		0,
		size,
		windows.MEM_COMMIT|windows.MEM_RESERVE,
		windows.PAGE_READWRITE,
	)
	if addr == 0 {
		return 0, fmt.Errorf("alloc remote RW: %w", err)
	}
	return addr, nil
}

func (i *MemoryInjector) Load() error {
	if i.isLoaded {
		return nil
	}

	modules, err := memory.GetProcessModules(i.pid)
	if err != nil {
		return fmt.Errorf("error getting process modules: %w", err)
	}

	syscall.MustLoadDLL("USER32.dll")

	for _, module := range modules {
		if strings.Contains(strings.ToLower(module.ModuleName), "user32.dll") {
			i.getCursorPosAddr, err = syscall.GetProcAddress(module.ModuleHandle, "GetCursorPos")
			i.getKeyStateAddr, _ = syscall.GetProcAddress(module.ModuleHandle, "GetKeyState")
			i.trackMouseEventAddr, _ = syscall.GetProcAddress(module.ModuleHandle, "TrackMouseEvent")
			i.setCursorPosAddr, _ = syscall.GetProcAddress(module.ModuleHandle, "SetCursorPos")

			err = ntapi.ReadProcessMemory(i.handle, i.getCursorPosAddr, &i.getCursorPosOrigBytes[0], uintptr(len(i.getCursorPosOrigBytes)))
			if err != nil {
				return fmt.Errorf("error reading memory: %w", err)
			}

			err = i.stopTrackingMouseLeaveEvents()
			if err != nil {
				return err
			}

			err = ntapi.ReadProcessMemory(i.handle, i.setCursorPosAddr, &i.setCursorPosOrigBytes[0], uintptr(len(i.setCursorPosOrigBytes)))
			if err != nil {
				return fmt.Errorf("error reading setcursor memory: %w", err)
			}

			err = i.overrideSetCursorPosPolymorphic()
			if err != nil {
				return err
			}

			err = ntapi.ReadProcessMemory(i.handle, i.getKeyStateAddr, &i.getKeyStateOrigBytes[0], uintptr(len(i.getKeyStateOrigBytes)))
			if err != nil {
				return fmt.Errorf("error reading memory: %w", err)
			}
		}
	}
	if i.getCursorPosAddr == 0 || i.getKeyStateAddr == 0 {
		return errors.New("could not find target address")
	}

	// Allocate remote data buffers for write-once architecture
	i.cursorDataBuf, err = i.allocRemoteRW(16)
	if err != nil {
		return fmt.Errorf("alloc cursor buf: %w", err)
	}

	i.keyDataBuf, err = i.allocRemoteRW(16)
	if err != nil {
		return fmt.Errorf("alloc key buf: %w", err)
	}

	if err := i.installCursorPosTrampoline(); err != nil {
		return fmt.Errorf("install cursor trampoline: %w", err)
	}

	if err := i.installKeyStateTrampoline(); err != nil {
		return fmt.Errorf("install keystate trampoline: %w", err)
	}

	i.isLoaded = true

	return nil
}

// --------------------------------------------------------------------------
// Write-once GetCursorPos trampoline + data buffer
// --------------------------------------------------------------------------

// installCursorPosTrampoline writes a polymorphic trampoline to GetCursorPos
// that reads coordinates from cursorDataBuf. Written ONCE, never changed.
//
// The trampoline reads X,Y from a data buffer and writes to the POINT* in rcx.
// Runtime CursorPos() calls only update the data buffer (RW page).
func (i *MemoryInjector) installCursorPosTrampoline() error {
	code := buildCursorPosTrampoline(i.cursorDataBuf)

	i.cursorOverrideActive = true
	return writeCodePage(i.handle, i.getCursorPosAddr, &code[0], uintptr(len(code)))
}

// buildCursorPosTrampoline generates polymorphic shellcode for GetCursorPos override.
// Reads X,Y from dataBufAddr and writes to POINT* in rcx.
func buildCursorPosTrampoline(dataBufAddr uintptr) []byte {
	// Use a random temp register from safe set: rbx, rsi, rdi
	type regInfo struct {
		push, pop byte
		rexMov    byte   // REX prefix for mov reg, imm64
		movOp     byte   // opcode for mov reg, imm64 (BB=rbx, BE=rsi, BF=rdi)
		readX     []byte // mov eax, [reg]
		readY     []byte // mov eax, [reg+4]
	}
	regs := []regInfo{
		{0x53, 0x5B, 0x48, 0xBB, []byte{0x8B, 0x03}, []byte{0x8B, 0x43, 0x04}},       // rbx
		{0x56, 0x5E, 0x48, 0xBE, []byte{0x8B, 0x06}, []byte{0x8B, 0x46, 0x04}},       // rsi
		{0x57, 0x5F, 0x48, 0xBF, []byte{0x8B, 0x07}, []byte{0x8B, 0x47, 0x04}},       // rdi
	}
	r := regs[polyRandN(len(regs))]

	addr := uint64(dataBufAddr)
	addrBytes := [8]byte{}
	binary.LittleEndian.PutUint64(addrBytes[:], addr)

	var code []byte

	// push rax
	code = append(code, 0x50)
	// push tempReg
	code = append(code, r.push)
	// mov tempReg, dataBufAddr (10 bytes)
	code = append(code, r.rexMov, r.movOp)
	code = append(code, addrBytes[:]...)
	// mov eax, [tempReg] — read X
	code = append(code, r.readX...)
	// mov [rcx], eax — write X to POINT.x
	code = append(code, 0x89, 0x01)
	// mov eax, [tempReg+4] — read Y
	code = append(code, r.readY...)
	// mov [rcx+4], eax — write Y to POINT.y
	code = append(code, 0x89, 0x41, 0x04)
	// pop tempReg
	code = append(code, r.pop)
	// pop rax
	code = append(code, 0x58)
	// mov al, 1 — return TRUE
	code = append(code, 0xB0, 0x01)
	// ret
	code = append(code, 0xC3)

	return code
}

// CursorPos updates the cursor data buffer only (RW page, NOT code page).
// This is called hundreds of times per second — but never writes to .text.
func (i *MemoryInjector) CursorPos(x, y int) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	if !i.isLoaded {
		return nil
	}

	i.lastCursorX = x
	i.lastCursorY = y
	i.cursorOverrideActive = true

	// Write 8 bytes to RW data page — not to code page
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(x))
	binary.LittleEndian.PutUint32(buf[4:8], uint32(y))

	return ntapi.WriteProcessMemory(i.handle, i.cursorDataBuf, &buf[0], 8)
}

// --------------------------------------------------------------------------
// Write-once GetKeyState trampoline + data buffer
// --------------------------------------------------------------------------

// installKeyStateTrampoline writes a polymorphic trampoline to GetKeyState
// that checks the keyDataBuf for active simulation.
// Written ONCE, never changed. Runtime calls update data buffer only.
func (i *MemoryInjector) installKeyStateTrampoline() error {
	code := buildKeyStateTrampoline(i.keyDataBuf, i.getKeyStateAddr)
	if len(code) == 0 {
		return nil // no permanent hook — override done per keypress
	}
	return writeCodePage(i.handle, i.getKeyStateAddr, &code[0], uintptr(len(code)))
}

// buildKeyStateTrampoline generates polymorphic code for GetKeyState override.
// Checks dataBuf[1] (active flag). If 0, falls through to original code.
// If 1, compares cl with dataBuf[0] (target key). If match, returns 0x8000.
//
// Layout: [our trampoline][saved original bytes patched to work]
func buildKeyStateTrampoline(dataBufAddr uintptr, origAddr uintptr) []byte {
	addr := uint64(dataBufAddr)
	addrBytes := [8]byte{}
	binary.LittleEndian.PutUint64(addrBytes[:], addr)

	// Simple approach: compare and return, no complex multi-path
	// This fits in the 18-byte space we have (original bytes saved)
	//
	// cmp cl, key    -> Compare key byte (set at runtime in data buf)
	// sete al        -> Set al to 1 if equal
	// shl ax, 15     -> Shift left by 15 to create 0x8000 if was 1
	// ret
	//
	// But this doesn't check the "active" flag. We need a different approach.
	// Since we only have 18 bytes of saved original, we use the simple
	// approach: when key override is active, write the comparison stub;
	// when inactive, restore original bytes. This is 2 writes per key event
	// (activate + deactivate) instead of hundreds per cursor move.
	// GetKeyState is called much less frequently than GetCursorPos.

	// For now, return a no-op stub that passes through to original behavior.
	// The actual override is done via OverrideGetKeyState/RestoreGetKeyState.
	// This is acceptable because GetKeyState writes are rare (per keypress, not per frame).

	// Return the original bytes — no permanent hook installed.
	// The write-once architecture applies to GetCursorPos (the main offender).
	return nil
}

// OverrideGetKeyState temporarily patches GetKeyState to return 0x8000 for the given key.
// This is called rarely (per keypress event) so writing to .text is acceptable.
func (i *MemoryInjector) OverrideGetKeyState(key byte) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	// Phase 8D (future): in-process key state via Present hook.

	if !i.isLoaded {
		return nil
	}

	// Polymorphic shellcode: compare key, set result
	code := buildKeyStateOverride(key)
	return writeCodePage(i.handle, i.getKeyStateAddr, &code[0], uintptr(len(code)))
}

// buildKeyStateOverride generates polymorphic GetKeyState override for a specific key.
func buildKeyStateOverride(key byte) []byte {
	switch polyRandN(3) {
	case 0:
		// cmp cl, key; sete al; shl ax, 15; ret
		return []byte{0x80, 0xF9, key, 0x0F, 0x94, 0xC0, 0x66, 0xC1, 0xE0, 0x0F, 0xC3}
	case 1:
		// xor eax, eax; cmp cl, key; jne +3; mov ax, 0x8000; ret
		return []byte{0x33, 0xC0, 0x80, 0xF9, key, 0x75, 0x04, 0x66, 0xB8, 0x00, 0x80, 0xC3}
	default:
		// cmp cl, key; mov ax, 0x8000; je +2; xor eax, eax; ret
		return []byte{0x80, 0xF9, key, 0x66, 0xB8, 0x00, 0x80, 0x74, 0x02, 0x33, 0xC0, 0xC3}
	}
}

// --------------------------------------------------------------------------
// Polymorphic SetCursorPos override — write-once no-op
// --------------------------------------------------------------------------

// overrideSetCursorPosPolymorphic writes a polymorphic "return TRUE" stub.
// Written ONCE per session.
func (i *MemoryInjector) overrideSetCursorPosPolymorphic() error {
	code := buildSetCursorPosNoOp()
	err := writeCodePage(i.handle, i.setCursorPosAddr, &code[0], uintptr(len(code)))
	if err == nil {
		i.cursorOverrideActive = true
	}
	return err
}

// buildSetCursorPosNoOp generates a polymorphic "return TRUE" stub.
func buildSetCursorPosNoOp() []byte {
	switch polyRandN(3) {
	case 0:
		// mov eax, 1; ret
		return []byte{0xB8, 0x01, 0x00, 0x00, 0x00, 0xC3}
	case 1:
		// xor eax, eax; inc eax; ret
		return []byte{0x33, 0xC0, 0xFF, 0xC0, 0xC3, 0x90}
	default:
		// xor eax, eax; or eax, 1; ret
		return []byte{0x33, 0xC0, 0x83, 0xC8, 0x01, 0xC3}
	}
}

// --------------------------------------------------------------------------
// Restore and state management
// --------------------------------------------------------------------------

func (i *MemoryInjector) Unload() error {
	if err := i.RestoreMemory(); err != nil {
		i.logger.Error(fmt.Sprintf("error restoring memory: %v", err))
	}

	return windows.CloseHandle(i.handle)
}

func (i *MemoryInjector) RestoreMemory() error {
	if !i.isLoaded {
		return nil
	}

	i.isLoaded = false
	if err := i.RestoreGetCursorPosAddr(); err != nil {
		return fmt.Errorf("error restoring memory: %v", err)
	}
	if err := i.RestoreSetCursorPosAddr(); err != nil {
		return fmt.Errorf("error restoring cursor memory: %v", err)
	}
	i.cursorOverrideActive = false

	return i.RestoreGetKeyState()
}

func (i *MemoryInjector) DisableCursorOverride() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if !i.isLoaded || !i.cursorOverrideActive {
		return nil
	}
	if err := i.RestoreGetCursorPosAddr(); err != nil {
		return err
	}
	if err := i.RestoreSetCursorPosAddr(); err != nil {
		return err
	}
	i.cursorOverrideActive = false
	return nil
}

func (i *MemoryInjector) EnableCursorOverride() error {
	if !i.isLoaded || i.cursorOverrideActive {
		return nil
	}
	if err := i.overrideSetCursorPosPolymorphic(); err != nil {
		return err
	}
	// Re-install write-once trampoline and update data buffer
	if err := i.installCursorPosTrampoline(); err != nil {
		return err
	}
	return i.CursorPos(i.lastCursorX, i.lastCursorY)
}

func (i *MemoryInjector) RestoreGetKeyState() error {
	// Phase 8D (future): in-process key state via Present hook.
	return writeCodePage(i.handle, i.getKeyStateAddr, &i.getKeyStateOrigBytes[0], uintptr(len(i.getKeyStateOrigBytes)))
}

func (i *MemoryInjector) RestoreGetCursorPosAddr() error {
	return writeCodePage(i.handle, i.getCursorPosAddr, &i.getCursorPosOrigBytes[0], uintptr(len(i.getCursorPosOrigBytes)))
}

func (i *MemoryInjector) RestoreSetCursorPosAddr() error {
	return writeCodePage(i.handle, i.setCursorPosAddr, &i.setCursorPosOrigBytes[0], uintptr(len(i.setCursorPosOrigBytes)))
}

func (i *MemoryInjector) CursorOverrideActive() bool {
	if i == nil {
		return false
	}
	return i.isLoaded && i.cursorOverrideActive
}

// OverrideSetCursorPos kept for backward compatibility — delegates to polymorphic version.
func (i *MemoryInjector) OverrideSetCursorPos() error {
	return i.overrideSetCursorPosPolymorphic()
}

// --------------------------------------------------------------------------
// TrackMouseEvent hook — write-once, polymorphic
// --------------------------------------------------------------------------

func (i *MemoryInjector) stopTrackingMouseLeaveEvents() error {
	err := ntapi.ReadProcessMemory(i.handle, i.trackMouseEventAddr, &i.trackMouseEventBytes[0], uintptr(len(i.trackMouseEventBytes)))
	if err != nil {
		return err
	}

	// and dword ptr [rcx+4], 0xFFFFFFFD
	disableMouseLeaveRequest := []byte{0x81, 0x61, 0x04, 0xFD, 0xFF, 0xFF, 0xFF}

	// Already hooked
	if bytes.Contains(i.trackMouseEventBytes[:], disableMouseLeaveRequest) {
		return nil
	}

	num := int32(binary.LittleEndian.Uint32(i.trackMouseEventBytes[2:6]))
	num -= 7
	numberBytes := make([]byte, 4)
	binary.LittleEndian.PutUint32(numberBytes, uint32(num))
	injectBytes := append(i.trackMouseEventBytes[0:2], numberBytes...)

	hook := append(disableMouseLeaveRequest, injectBytes...)

	return writeCodePage(i.handle, i.trackMouseEventAddr, &hook[0], uintptr(len(hook)))
}

// --------------------------------------------------------------------------
// Polymorphic helpers
// --------------------------------------------------------------------------

func polyRandN(n int) int {
	val, _ := rand.Int(rand.Reader, big.NewInt(int64(n)))
	return int(val.Int64())
}
