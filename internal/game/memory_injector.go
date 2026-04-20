package game

// ⚠️ AUDIT_E finding 2026-04-11:
// Live-process scan of 121 modules in D2R confirmed D2R.exe does NOT import
// or call GetKeyState, GetAsyncKeyState, GetKeyboardState, GetCursorPos,
// SetCursorPos, PeekMessage*, GetMessage*, TranslateMessage, DispatchMessage*.
// D2R buffers its own keystate at 0x7ff7(605d)eda25b0 and cursor at
// 0x7ff7(605d)ec3bb8/0xec3bbc via internal helpers. The USER32 trampolines
// in this file are DEAD FROM D2R'S PERSPECTIVE — they only intercept reads
// by other loaded modules (NVIDIA overlays, Discord overlay, Steam overlay,
// Blizzard UI). For driving D2R itself, use:
//   - FUN_7ff7(605d)d52e400 set_cursor_screen_xy(x, y) via CmdCallFn
//   - real_click_worker @ RVA 0xBA95D0 via CmdCallFn
//   - keystate table @ keyBindings + action_offset direct write via CmdWriteMem
// Full analysis: logs/AUDIT_E_forcemove.md + logs/MOVEMENT_INPROCESS_SPEC.md.

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
	"time"
	"unsafe"

	"local/internal/svc/internal/gamelib/memory"
	"local/internal/svc/internal/presenter"
	"golang.org/x/sys/windows"
)

const fullAccess = windows.PROCESS_VM_OPERATION | windows.PROCESS_VM_WRITE | windows.PROCESS_VM_READ

// writeCodePage writes to a code page (.text) in the remote process.
// Uses kernel32 WriteProcessMemory which handles page protection automatically.
// This is a one-time init write (not hot path), so kernel32 call is acceptable.
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
	sendMessageWAddr      uintptr // user32!SendMessageW — called in-process via APC for HID clicks/keys without OS msg queue
	logger                *slog.Logger
	cursorOverrideActive  bool
	lastCursorX           int
	lastCursorY           int

	// Data buffers in remote process (RW pages, allocated via VirtualAllocEx)
	cursorDataBuf uintptr // 8 bytes: [X:uint32][Y:uint32]

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

// GetPresenter returns the installed Presenter (or nil).
func (i *MemoryInjector) GetPresenter() *presenter.Presenter {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.presenter
}

// SetPresenter installs a Presenter and re-points the GetCursorPos and GetKeyState
// trampolines to read from the presenter's shared mapped view.
func (i *MemoryInjector) SetPresenter(p *presenter.Presenter) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.presenter = p

	if p == nil || !p.IsReady() || !i.isLoaded {
		return
	}

	i.reapplyPresenterTrampolines()
}

// reapplyPresenterTrampolines re-installs cursor and key trampolines
// to read from the presenter's shared mapped view. Called from SetPresenter
// (under lock) and from Load() (single-threaded init, no concurrent access).
func (i *MemoryInjector) reapplyPresenterTrampolines() {
	p := i.presenter

	// Re-install cursor trampoline to read from presenter's shared buffer.
	cursorAddr := p.CursorBufAddr()
	if cursorAddr != 0 {
		code := buildCursorPosTrampoline(cursorAddr)
		writeCodePage(i.handle, i.getCursorPosAddr, &code[0], uintptr(len(code)))
		i.cursorDataBuf = cursorAddr
	}

	// Install permanent GetKeyState trampoline reading from shared buffer.
	keyAddr := p.KeyDataBufAddr()
	if keyAddr != 0 {
		body := buildKeyStateMappedTrampoline(keyAddr, i.getKeyStateOrigBytes[:])
		if len(body) > 0 {
			returnAddr := i.getKeyStateAddr + uintptr(len(i.getKeyStateOrigBytes))
			body = appendAbsJmp(body, returnAddr)

			trampolineAddr, allocErr := i.allocRemoteRWX(uintptr(len(body)))
			if allocErr == nil {
				wpmErr := windows.WriteProcessMemory(i.handle, trampolineAddr, &body[0], uintptr(len(body)), nil)
				if wpmErr == nil {
					// Seal as RX
					var oldProtect uint32
					kernel32 := windows.NewLazySystemDLL("kernel32.dll")
					vpEx := kernel32.NewProc("VirtualProtectEx")
					vpEx.Call(uintptr(i.handle), trampolineAddr, uintptr(len(body)), 0x20, uintptr(unsafe.Pointer(&oldProtect)))
					// Overwrite GetKeyState with jmp to trampoline
					jmp := buildAbsJmp(trampolineAddr)
					writeCodePage(i.handle, i.getKeyStateAddr, &jmp[0], uintptr(len(jmp)))
				}
			}
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

// allocRemoteRWX allocates a RW page, writes code to it, then marks it RX.
// Never leaves a persistent RWX region in the target (avoids VAD scan detection).
func (i *MemoryInjector) allocRemoteRWX(size uintptr) (uintptr, error) {
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
		return 0, fmt.Errorf("alloc remote page: %w", err)
	}
	return addr, nil
}

// buildAbsJmp builds a 14-byte absolute jmp: ff 25 00 00 00 00 [8-byte addr]
func buildAbsJmp(target uintptr) []byte {
	jmp := make([]byte, 14)
	jmp[0] = 0xFF
	jmp[1] = 0x25
	// jmp[2..5] = 0 (rip-relative offset to next 8 bytes)
	binary.LittleEndian.PutUint64(jmp[6:], uint64(target))
	return jmp
}

// appendAbsJmp appends a 14-byte absolute jmp to the code buffer.
func appendAbsJmp(code []byte, target uintptr) []byte {
	return append(code, buildAbsJmp(target)...)
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
			i.sendMessageWAddr, _ = syscall.GetProcAddress(module.ModuleHandle, "SendMessageW")

			err = windows.ReadProcessMemory(i.handle, i.getCursorPosAddr, &i.getCursorPosOrigBytes[0], uintptr(len(i.getCursorPosOrigBytes)), nil)
			if err != nil {
				return fmt.Errorf("error reading memory: %w", err)
			}

			err = i.stopTrackingMouseLeaveEvents()
			if err != nil {
				return err
			}

			err = windows.ReadProcessMemory(i.handle, i.setCursorPosAddr, &i.setCursorPosOrigBytes[0], uintptr(len(i.setCursorPosOrigBytes)), nil)
			if err != nil {
				return fmt.Errorf("error reading setcursor memory: %w", err)
			}

			err = i.overrideSetCursorPosPolymorphic()
			if err != nil {
				return err
			}

			err = windows.ReadProcessMemory(i.handle, i.getKeyStateAddr, &i.getKeyStateOrigBytes[0], uintptr(len(i.getKeyStateOrigBytes)), nil)
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

	if err := i.installCursorPosTrampoline(); err != nil {
		return fmt.Errorf("install cursor trampoline: %w", err)
	}

	i.isLoaded = true

	// If presenter was set before Load(), re-install trampolines to use shared buffer.
	if i.presenter != nil && i.presenter.IsReady() {
		i.reapplyPresenterTrampolines()
	}

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
// When presenter is available, writes to the shared mapped view (no WPM).
func (i *MemoryInjector) CursorPos(x, y int) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	if !i.isLoaded {
		return nil
	}

	i.lastCursorX = x
	i.lastCursorY = y
	i.cursorOverrideActive = true

	// Prefer presenter's mapped view — zero cross-process writes.
	if i.presenter != nil && i.presenter.IsReady() {
		i.presenter.SetCursor(int32(x), int32(y))
		return nil
	}

	// Fallback: write via WPM to remote data page.
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(x))
	binary.LittleEndian.PutUint32(buf[4:8], uint32(y))

	return windows.WriteProcessMemory(i.handle, i.cursorDataBuf, &buf[0], 8, nil)
}

// --------------------------------------------------------------------------
// Write-once GetKeyState trampoline + data buffer
// --------------------------------------------------------------------------

// buildKeyStateMappedTrampoline generates a permanent GetKeyState trampoline
// that reads key/active from the presenter's shared mapped view.
//
// Layout (allocated in remote RWX page):
//   [check logic ~40 bytes]
//   [saved original 18 bytes]
//   [jmp back to GetKeyState+18, 14 bytes]
//
// At GetKeyState itself: 14-byte absolute jmp to this page.
// Runtime: OverrideGetKeyState/RestoreGetKeyState only write to mapped view.
func buildKeyStateMappedTrampoline(keyDataAddr uintptr, origBytes []byte) []byte {
	addrBytes := [8]byte{}
	binary.LittleEndian.PutUint64(addrBytes[:], uint64(keyDataAddr))

	var code []byte

	// push rax
	code = append(code, 0x50)
	// mov rax, keyDataAddr (10 bytes)
	code = append(code, 0x48, 0xB8)
	code = append(code, addrBytes[:]...)
	// cmp byte [rax+1], 0  — check active flag
	code = append(code, 0x80, 0x78, 0x01, 0x00)
	// je .original (forward jump — patched below)
	code = append(code, 0x74, 0x00) // placeholder offset
	jeOffset := len(code) - 1       // index of the offset byte

	// Active path: compare requested key (cl) with target key [rax]
	// cmp cl, [rax]
	code = append(code, 0x3A, 0x08)
	// pop rax
	code = append(code, 0x58)
	// jne .original_after_pop (non-target key → call real GetKeyState)
	code = append(code, 0x75, 0x05) // placeholder, patched below
	jneOffset := len(code) - 1
	// mov eax, 0x8000; ret (target key matched)
	code = append(code, 0xB8, 0x00, 0x80, 0x00, 0x00)
	code = append(code, 0xC3)

	// .original: pop rax, fall through to saved original bytes
	// (reached when active=0 via je)
	origStart := len(code)
	code[jeOffset] = byte(origStart - jeOffset - 1) // patch je offset
	// pop rax (needed for active=0 path where rax is still pushed)
	code = append(code, 0x58)
	// .original_after_pop: saved original bytes
	// (jne from active path lands here — rax already popped)
	origAfterPop := len(code)
	code[jneOffset] = byte(origAfterPop - jneOffset - 1) // patch jne offset
	// Saved original bytes (18 bytes of GetKeyState prologue)
	code = append(code, origBytes...)

	return code
}

// SendMessageInProcess calls user32!SendMessageW FROM D2R's own thread (via
// APC), so WndProc fires synchronously from D2R's legitimate call stack.
// No cross-process SendMessage / OS message queue involvement — the OS sees
// only an in-process user32 call, indistinguishable from D2R's own internal
// message posts. Use this for all HID-style input that must survive in a
// full-packet bot posture.
//
// Blocks up to ~5s via CallFnGameThread. Returns WndProc's LRESULT.
func (i *MemoryInjector) SendMessageInProcess(hwnd uintptr, msg uint32, wParam, lParam uintptr) (uint64, error) {
	if i == nil || !i.isLoaded {
		return 0, nil
	}
	if i.sendMessageWAddr == 0 {
		return 0, fmt.Errorf("SendMessageW not resolved")
	}
	if i.presenter == nil || !i.presenter.IsReady() {
		return 0, fmt.Errorf("presenter not ready for in-process SendMessage")
	}
	return i.presenter.CallFnGameThread(i.sendMessageWAddr, hwnd, uintptr(msg), wParam, lParam)
}

// PressKeyInProcess posts WM_KEYDOWN + WM_KEYUP to D2R's window via
// SendMessageW executed on D2R's OWN game thread (APC). From D2R's
// perspective this is indistinguishable from a real key press: its
// WndProc processes the message, looks up the keybinding in the dispatch
// table, runs handler1 (widget toggle) and handler2 (state propagator),
// and triggers the FULL action chain — for weapon swap: widget flip +
// item-pointer swap + stat recalc + 0x50 packet via dual_send_wrap.
//
// Detection surface: ZERO OS-HID. No SendInput, no keybd_event, no
// SetWindowsHookEx. Message originates in D2R process, APC fires on
// D2R's own game thread, WndProc runs on its own window thread —
// Warden cannot distinguish from legitimate internal message posting.
//
// Used for state-dependent packets that CANNOT be emitted externally
// (0x50 swap, 0x5C Cain identify, etc.) — see memory
// project_weapon_swap_solved 2026-04-12 HID-SOLUTION block.
func (i *MemoryInjector) PressKeyInProcess(hwnd uintptr, vk byte) error {
	if i == nil || !i.isLoaded {
		return fmt.Errorf("memory injector not loaded")
	}
	if i.sendMessageWAddr == 0 {
		return fmt.Errorf("SendMessageW not resolved")
	}
	if i.presenter == nil || !i.presenter.IsReady() {
		return fmt.Errorf("presenter not ready for in-process key press")
	}
	const (
		wmKeyDown uint32 = 0x0100
		wmKeyUp   uint32 = 0x0101
	)
	// lParam for WM_KEYDOWN: bit 0..15 = repeat count (1), rest 0.
	lparamDown := uintptr(1)
	if _, err := i.presenter.CallFnGameThread(i.sendMessageWAddr, hwnd, uintptr(wmKeyDown), uintptr(vk), lparamDown); err != nil {
		return fmt.Errorf("WM_KEYDOWN vk=0x%02X: %w", vk, err)
	}
	// Brief natural delay between down + up so D2R's WndProc processes
	// the key-down action before seeing the release.
	time.Sleep(30 * time.Millisecond)
	// lParam for WM_KEYUP: bit 30 (was-down) + bit 31 (transition) + repeat.
	lparamUp := uintptr(1) | (1 << 30) | (1 << 31)
	if _, err := i.presenter.CallFnGameThread(i.sendMessageWAddr, hwnd, uintptr(wmKeyUp), uintptr(vk), lparamUp); err != nil {
		return fmt.Errorf("WM_KEYUP vk=0x%02X: %w", vk, err)
	}
	return nil
}

// OverrideGetKeyState temporarily patches GetKeyState to return 0x8000 for the given key.
// With presenter: writes to shared mapped view (no cross-process .text patching).
// Without presenter: falls back to per-keypress .text patching.
func (i *MemoryInjector) OverrideGetKeyState(key byte) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	if !i.isLoaded {
		return nil
	}

	// If presenter has a permanent trampoline, just set the key in shared buffer.
	if i.presenter != nil && i.presenter.IsReady() && i.presenter.KeyDataBufAddr() != 0 {
		i.presenter.SetKeyState(key, true)
		return nil
	}

	// Fallback: per-keypress polymorphic shellcode written to .text.
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

	// Always restore original .text bytes during full cleanup.
	return i.restoreGetKeyStateBytes()
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

// RestoreGetKeyState clears the key override. With presenter, just clears the flag.
// Without presenter or during unload, restores original .text bytes.
func (i *MemoryInjector) RestoreGetKeyState() error {
	if i.presenter != nil && i.presenter.IsReady() && i.presenter.KeyDataBufAddr() != 0 {
		i.presenter.SetKeyState(0, false)
		return nil
	}
	return i.restoreGetKeyStateBytes()
}

// restoreGetKeyStateBytes unconditionally writes original bytes back to GetKeyState.
// Used during Unload/RestoreMemory to fully clean up.
func (i *MemoryInjector) restoreGetKeyStateBytes() error {
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
	err := windows.ReadProcessMemory(i.handle, i.trackMouseEventAddr, &i.trackMouseEventBytes[0], uintptr(len(i.trackMouseEventBytes)), nil)
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
