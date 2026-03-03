package game

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"strings"
	"syscall"

	"github.com/hectorgimenez/d2go/pkg/memory"
	"golang.org/x/sys/windows"
)

const fullAccess = windows.PROCESS_VM_OPERATION | windows.PROCESS_VM_WRITE | windows.PROCESS_VM_READ

type MemoryInjector struct {
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
}

// nopEquivalents are x64 instructions that do nothing useful but change the
// byte pattern of the injected code. Each slice is one "NOP-equivalent"
// instruction that Warden cannot distinguish from real code.
var nopEquivalents = [][]byte{
	{0x90},                   // nop
	{0x66, 0x90},             // 66 nop (2-byte)
	{0x0F, 0x1F, 0x00},      // nop dword ptr [rax] (3-byte)
	{0x87, 0xDB},             // xchg ebx, ebx
	{0x87, 0xC9},             // xchg ecx, ecx
	{0x48, 0x87, 0xC0},       // xchg rax, rax
	{0x8D, 0x40, 0x00},       // lea eax, [rax+0]
	{0x0F, 0x1F, 0x40, 0x00}, // nop dword ptr [rax+0] (4-byte)
}

// randNopSled generates a random-length NOP sled (0-3 instructions) using
// NOP-equivalent encodings, so each injection has a unique byte pattern.
func randNopSled() []byte {
	n := rand.Intn(4) // 0..3 NOPs
	var sled []byte
	for range n {
		sled = append(sled, nopEquivalents[rand.Intn(len(nopEquivalents))]...)
	}
	return sled
}

// buildCursorPosHook assembles the GetCursorPos hook at runtime.
// Each instruction is appended individually so no contiguous shellcode
// pattern exists in the binary's data section.
func buildCursorPosHook(x, y int) []byte {
	buf := randNopSled()
	buf = append(buf, 0x50)             // push rax
	buf = append(buf, 0x48, 0x89, 0xC8) // mov rax, rcx

	// mov dword ptr [rax], X
	buf = append(buf, 0xC7, 0x00)
	xb := make([]byte, 4)
	binary.LittleEndian.PutUint32(xb, uint32(x))
	buf = append(buf, xb...)

	// mov dword ptr [rax+4], Y
	buf = append(buf, 0xC7, 0x40, 0x04)
	yb := make([]byte, 4)
	binary.LittleEndian.PutUint32(yb, uint32(y))
	buf = append(buf, yb...)

	buf = append(buf, 0x58)       // pop rax
	buf = append(buf, 0xB0, 0x01) // mov al, 1
	buf = append(buf, 0xC3)       // ret
	return buf
}

// buildKeyStateHook assembles the GetKeyState hook at runtime.
func buildKeyStateHook(key byte) []byte {
	buf := randNopSled()
	buf = append(buf, 0x80, 0xF9, key)        // cmp cl, key
	buf = append(buf, 0x0F, 0x94, 0xC0)       // sete al
	buf = append(buf, 0x66, 0xC1, 0xE0, 0x0F) // shl ax, 15
	buf = append(buf, 0xC3)                    // ret
	return buf
}

// buildSetCursorPosStub assembles the SetCursorPos no-op stub at runtime.
func buildSetCursorPosStub() []byte {
	buf := randNopSled()
	buf = append(buf, 0xB8, 0x01, 0x00, 0x00, 0x00) // mov eax, 1
	buf = append(buf, 0xC3)                           // ret
	return buf
}

// buildTrackMouseDisable assembles the TrackMouseEvent disable hook.
func buildTrackMouseDisable() []byte {
	buf := make([]byte, 0, 7)
	buf = append(buf, 0x81, 0x61, 0x04)       // and dword ptr [rcx+4],
	buf = append(buf, 0xFD, 0xFF, 0xFF, 0xFF) // 0xFFFFFFFD
	return buf
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
		// GetCursorPos
		if strings.Contains(strings.ToLower(module.ModuleName), "user32.dll") {
			i.getCursorPosAddr, err = syscall.GetProcAddress(module.ModuleHandle, "GetCursorPos")
			i.getKeyStateAddr, _ = syscall.GetProcAddress(module.ModuleHandle, "GetKeyState")
			i.trackMouseEventAddr, _ = syscall.GetProcAddress(module.ModuleHandle, "TrackMouseEvent")
			i.setCursorPosAddr, _ = syscall.GetProcAddress(module.ModuleHandle, "SetCursorPos")

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

			err = i.OverrideSetCursorPos()
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
		return errors.New("could not find GetCursorPos address")
	}

	i.isLoaded = true
	return nil
}

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
	if err := i.OverrideSetCursorPos(); err != nil {
		return err
	}
	// Reapply GetCursorPos hook using the last known coordinates
	return i.CursorPos(i.lastCursorX, i.lastCursorY)
}

func (i *MemoryInjector) CursorPos(x, y int) error {
	if !i.isLoaded {
		return nil
	}

	i.lastCursorX = x
	i.lastCursorY = y
	i.cursorOverrideActive = true

	hook := buildCursorPosHook(x, y)
	return windows.WriteProcessMemory(i.handle, i.getCursorPosAddr, &hook[0], uintptr(len(hook)), nil)
}

func (i *MemoryInjector) OverrideGetKeyState(key byte) error {
	if !i.isLoaded {
		return nil
	}

	hook := buildKeyStateHook(key)
	return windows.WriteProcessMemory(i.handle, i.getKeyStateAddr, &hook[0], uintptr(len(hook)), nil)
}

func (i *MemoryInjector) OverrideSetCursorPos() error {
	stub := buildSetCursorPosStub()
	err := windows.WriteProcessMemory(i.handle, i.setCursorPosAddr, &stub[0], uintptr(len(stub)), nil)
	if err == nil {
		i.cursorOverrideActive = true
	}
	return err
}

func (i *MemoryInjector) RestoreGetKeyState() error {
	return windows.WriteProcessMemory(i.handle, i.getKeyStateAddr, &i.getKeyStateOrigBytes[0], uintptr(len(i.getKeyStateOrigBytes)), nil)
}

func (i *MemoryInjector) RestoreGetCursorPosAddr() error {
	return windows.WriteProcessMemory(i.handle, i.getCursorPosAddr, &i.getCursorPosOrigBytes[0], uintptr(len(i.getCursorPosOrigBytes)), nil)
}

func (i *MemoryInjector) RestoreSetCursorPosAddr() error {
	return windows.WriteProcessMemory(i.handle, i.setCursorPosAddr, &i.setCursorPosOrigBytes[0], uintptr(len(i.setCursorPosOrigBytes)), nil)
}

func (i *MemoryInjector) CursorOverrideActive() bool {
	if i == nil {
		return false
	}
	return i.isLoaded && i.cursorOverrideActive
}

// stopTrackingMouseLeaveEvents disables mouse leave tracking so the game
// keeps processing mouse events even when the cursor is outside the window.
func (i *MemoryInjector) stopTrackingMouseLeaveEvents() error {
	err := windows.ReadProcessMemory(i.handle, i.trackMouseEventAddr, &i.trackMouseEventBytes[0], uintptr(len(i.trackMouseEventBytes)), nil)
	if err != nil {
		return err
	}

	disableMouseLeaveRequest := buildTrackMouseDisable()

	// Already hooked
	if bytes.Contains(i.trackMouseEventBytes[:], disableMouseLeaveRequest) {
		return nil
	}

	// Move back the pointer 7 bytes since we inject 7 bytes in front
	num := int32(binary.LittleEndian.Uint32(i.trackMouseEventBytes[2:6]))
	num -= 7
	numberBytes := make([]byte, 4)
	binary.LittleEndian.PutUint32(numberBytes, uint32(num))
	injectBytes := append(i.trackMouseEventBytes[0:2], numberBytes...)

	hook := append(disableMouseLeaveRequest, injectBytes...)

	return windows.WriteProcessMemory(i.handle, i.trackMouseEventAddr, &hook[0], uintptr(len(hook)), nil)
}
