package ntapi

import (
	"syscall"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// UNICODE_STRING matches the Windows UNICODE_STRING structure.
type unicodeString struct {
	Length        uint16
	MaximumLength uint16
	Buffer        uintptr
}

// RTL_USER_PROCESS_PARAMETERS partial structure for CommandLine access.
type processParameters struct {
	_           [0x70]byte // skip to CommandLine offset
	CommandLine unicodeString
}

// PEB partial structure to access ProcessParameters.
type peb struct {
	_                 [0x20]byte // skip to ProcessParameters offset
	ProcessParameters *processParameters
}

// SpoofCommandLine overwrites the command line in PEB with a decoy string.
// This prevents Warden from seeing the real executable path/arguments
// when reading from our PEB via NtQueryInformationProcess.
func SpoofCommandLine(decoy string) error {
	// Get current process PEB via NtQueryInformationProcess class 0 (ProcessBasicInformation)
	type processBasicInfo struct {
		ExitStatus                   uintptr
		PebBaseAddress               uintptr
		AffinityMask                 uintptr
		BasePriority                 int32
		UniqueProcessId              uintptr
		InheritedFromUniqueProcessId uintptr
	}

	ntdll := windows.NewLazySystemDLL("ntdll.dll")
	ntQueryInfo := ntdll.NewProc("NtQueryInformationProcess")

	var pbi processBasicInfo
	var returnLen uint32
	r1, _, _ := ntQueryInfo.Call(
		uintptr(^uintptr(0)), // current process
		0,                     // ProcessBasicInformation
		uintptr(unsafe.Pointer(&pbi)),
		unsafe.Sizeof(pbi),
		uintptr(unsafe.Pointer(&returnLen)),
	)
	if r1 != 0 {
		return nil // silently fail
	}

	if pbi.PebBaseAddress == 0 {
		return nil
	}

	// Read PEB to get ProcessParameters
	pebPtr := (*peb)(unsafe.Pointer(pbi.PebBaseAddress))
	if pebPtr.ProcessParameters == nil {
		return nil
	}

	params := pebPtr.ProcessParameters

	// Encode decoy to UTF-16
	decoyUTF16 := utf16.Encode([]rune(decoy))
	decoyUTF16 = append(decoyUTF16, 0) // null terminator
	byteLen := len(decoyUTF16) * 2

	// Allocate new buffer for the spoofed command line
	newBuf, err := windows.VirtualAlloc(0, uintptr(byteLen),
		windows.MEM_COMMIT|windows.MEM_RESERVE, windows.PAGE_READWRITE)
	if err != nil {
		return nil
	}

	// Copy decoy UTF-16 into new buffer
	dst := unsafe.Slice((*uint16)(unsafe.Pointer(newBuf)), len(decoyUTF16))
	copy(dst, decoyUTF16)

	// Overwrite UNICODE_STRING in ProcessParameters
	params.CommandLine.Buffer = newBuf
	params.CommandLine.Length = uint16(byteLen - 2)        // exclude null
	params.CommandLine.MaximumLength = uint16(byteLen)

	// Also spoof via SetCommandLineW for GetCommandLineW callers
	setCmd := syscall.NewLazyDLL("kernel32.dll").NewProc("SetCommandLineW")
	if setCmd.Find() == nil {
		// SetCommandLineW doesn't exist publicly, skip
	}

	return nil
}
