package ntapi

import (
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

// RTL_USER_PROCESS_PARAMETERS partial structure.
// Offsets: ImagePathName at 0x60, CommandLine at 0x70.
type processParameters struct {
	_             [0x60]byte // skip to ImagePathName offset
	ImagePathName unicodeString
	CommandLine   unicodeString
}

// PEB partial structure to access ProcessParameters.
type peb struct {
	_                 [0x20]byte // skip to ProcessParameters offset
	ProcessParameters *processParameters
}

// spoofUnicodeString allocates a new UTF-16 buffer with the given text
// and overwrites the target UNICODE_STRING to point to it.
func spoofUnicodeString(target *unicodeString, text string) error {
	encoded := utf16.Encode([]rune(text))
	encoded = append(encoded, 0)
	byteLen := len(encoded) * 2

	buf, err := windows.VirtualAlloc(0, uintptr(byteLen),
		windows.MEM_COMMIT|windows.MEM_RESERVE, windows.PAGE_READWRITE)
	if err != nil {
		return err
	}

	dst := unsafe.Slice((*uint16)(unsafe.Pointer(buf)), len(encoded))
	copy(dst, encoded)

	target.Buffer = buf
	target.Length = uint16(byteLen - 2)
	target.MaximumLength = uint16(byteLen)
	return nil
}

// SpoofCommandLine overwrites the command line and image path in PEB
// with decoy strings. This prevents Warden from seeing the real
// executable path/arguments via NtQueryInformationProcess.
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

	// Spoof CommandLine
	_ = spoofUnicodeString(&params.CommandLine, decoy)

	// Spoof ImagePathName to match the decoy executable
	_ = spoofUnicodeString(&params.ImagePathName, "C:\\Windows\\System32\\svchost.exe")

	return nil
}
