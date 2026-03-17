package ntapi

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// PatchETW disables Event Tracing for Windows in the current process
// by patching EtwEventWrite in ntdll.dll to immediately return 0 (STATUS_SUCCESS).
//
// This prevents ETW-based telemetry that anti-cheat systems can consume
// to monitor syscall patterns, .NET activity, and other runtime events.
//
// The patch is: xor eax, eax / ret (3 bytes: 31 C0 C3)
func PatchETW() error {
	ntdll, err := windows.LoadLibrary("ntdll.dll")
	if err != nil {
		return fmt.Errorf("load ntdll: %w", err)
	}

	etwAddr, err := syscall.GetProcAddress(syscall.Handle(ntdll), "EtwEventWrite")
	if err != nil {
		return fmt.Errorf("resolve EtwEventWrite: %w", err)
	}

	// Step 1: Change page protection to RW (writable, not executable)
	var oldProtect uint32
	err = windows.VirtualProtect(etwAddr, 3, windows.PAGE_READWRITE, &oldProtect)
	if err != nil {
		return fmt.Errorf("VirtualProtect RW: %w", err)
	}

	// Write the patch: xor eax, eax / ret
	patch := (*[3]byte)(unsafe.Pointer(etwAddr))
	patch[0] = 0x31 // xor eax, eax
	patch[1] = 0xC0
	patch[2] = 0xC3 // ret

	// Step 2: Flip to RX (executable, non-writable) — page is never RWX
	err = windows.VirtualProtect(etwAddr, 3, windows.PAGE_EXECUTE_READ, &oldProtect)
	if err != nil {
		// Non-fatal
	}

	return nil
}
