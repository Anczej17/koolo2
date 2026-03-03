// Package ntapi provides low-level NT syscall wrappers that bypass
// usermode API hooks (e.g. Warden hooks on kernel32/ntdll).
//
// Instead of calling kernel32!ReadProcessMemory (which Warden can hook),
// we resolve the syscall number from ntdll at runtime and invoke it
// directly via syscall.SyscallN, skipping any inline hooks.
package ntapi

import (
	"fmt"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	initOnce sync.Once
	initErr  error

	// Syscall numbers resolved at runtime from ntdll
	sysNtReadVirtualMemory  uintptr
	sysNtWriteVirtualMemory uintptr
	sysNtOpenProcess        uintptr
	sysNtClose              uintptr
)

// resolveSSN extracts the syscall service number (SSN) from the first
// bytes of an ntdll export. On x64 Windows the stub is:
//
//	mov r10, rcx      ; 4C 8B D1
//	mov eax, <SSN>    ; B8 xx xx 00 00
//	...
//	syscall
//
// We read the 4 bytes at offset +4 to get the SSN as a uint32.
func resolveSSN(ntdll windows.Handle, name string) (uintptr, error) {
	proc, err := syscall.GetProcAddress(syscall.Handle(ntdll), name)
	if err != nil {
		return 0, fmt.Errorf("GetProcAddress(%s): %w", name, err)
	}

	// Read the stub bytes: expect 4C 8B D1 B8 at offset 0
	stub := (*[8]byte)(unsafe.Pointer(proc))

	// Verify mov r10, rcx prefix (4C 8B D1)
	if stub[0] != 0x4C || stub[1] != 0x8B || stub[2] != 0xD1 {
		return 0, fmt.Errorf("%s: unexpected stub prefix %02X %02X %02X (possibly hooked)", name, stub[0], stub[1], stub[2])
	}
	// Verify mov eax, imm32 opcode (B8)
	if stub[3] != 0xB8 {
		return 0, fmt.Errorf("%s: expected B8 at offset 3, got %02X", name, stub[3])
	}

	ssn := uint32(stub[4]) | uint32(stub[5])<<8 | uint32(stub[6])<<16 | uint32(stub[7])<<24
	return uintptr(ssn), nil
}

func initialize() {
	initOnce.Do(func() {
		ntdll, err := windows.LoadLibrary("ntdll.dll")
		if err != nil {
			initErr = fmt.Errorf("LoadLibrary ntdll.dll: %w", err)
			return
		}

		sysNtReadVirtualMemory, err = resolveSSN(ntdll, "NtReadVirtualMemory")
		if err != nil {
			initErr = err
			return
		}

		sysNtWriteVirtualMemory, err = resolveSSN(ntdll, "NtWriteVirtualMemory")
		if err != nil {
			initErr = err
			return
		}

		sysNtOpenProcess, err = resolveSSN(ntdll, "NtOpenProcess")
		if err != nil {
			initErr = err
			return
		}

		sysNtClose, err = resolveSSN(ntdll, "NtClose")
		if err != nil {
			initErr = err
			return
		}
	})
}

// Init resolves all syscall numbers. Call once at startup.
// Returns an error if ntdll stubs are hooked or unavailable.
func Init() error {
	initialize()
	return initErr
}

// ReadProcessMemory reads memory from a remote process using NtReadVirtualMemory.
func ReadProcessMemory(handle windows.Handle, addr uintptr, buf *byte, size uintptr) error {
	initialize()
	if initErr != nil {
		// Fallback to standard API if syscall resolution failed
		return windows.ReadProcessMemory(handle, addr, buf, size, nil)
	}

	var bytesRead uintptr
	r1, _, _ := syscall.SyscallN(
		sysNtReadVirtualMemory,
		uintptr(handle),
		addr,
		uintptr(unsafe.Pointer(buf)),
		size,
		uintptr(unsafe.Pointer(&bytesRead)),
	)
	if r1 != 0 {
		return fmt.Errorf("NtReadVirtualMemory failed: NTSTATUS 0x%08X", r1)
	}
	return nil
}

// WriteProcessMemory writes memory to a remote process using NtWriteVirtualMemory.
func WriteProcessMemory(handle windows.Handle, addr uintptr, buf *byte, size uintptr) error {
	initialize()
	if initErr != nil {
		return windows.WriteProcessMemory(handle, addr, buf, size, nil)
	}

	var bytesWritten uintptr
	r1, _, _ := syscall.SyscallN(
		sysNtWriteVirtualMemory,
		uintptr(handle),
		addr,
		uintptr(unsafe.Pointer(buf)),
		size,
		uintptr(unsafe.Pointer(&bytesWritten)),
	)
	if r1 != 0 {
		return fmt.Errorf("NtWriteVirtualMemory failed: NTSTATUS 0x%08X", r1)
	}
	return nil
}

// clientID matches the Windows CLIENT_ID structure.
type clientID struct {
	UniqueProcess uintptr
	UniqueThread  uintptr
}

// objectAttributes matches a minimal OBJECT_ATTRIBUTES structure.
type objectAttributes struct {
	Length                   uint32
	RootDirectory            uintptr
	ObjectName               uintptr
	Attributes               uint32
	SecurityDescriptor       uintptr
	SecurityQualityOfService uintptr
}

// OpenProcess opens a process handle using NtOpenProcess.
func OpenProcess(access uint32, pid uint32) (windows.Handle, error) {
	initialize()
	if initErr != nil {
		return windows.OpenProcess(access, false, pid)
	}

	var handle uintptr
	cid := clientID{UniqueProcess: uintptr(pid)}
	oa := objectAttributes{Length: uint32(unsafe.Sizeof(objectAttributes{}))}

	r1, _, _ := syscall.SyscallN(
		sysNtOpenProcess,
		uintptr(unsafe.Pointer(&handle)),
		uintptr(access),
		uintptr(unsafe.Pointer(&oa)),
		uintptr(unsafe.Pointer(&cid)),
	)
	if r1 != 0 {
		return 0, fmt.Errorf("NtOpenProcess failed: NTSTATUS 0x%08X", r1)
	}
	return windows.Handle(handle), nil
}

// CloseHandle closes an NT handle using NtClose.
func CloseHandle(handle windows.Handle) error {
	initialize()
	if initErr != nil {
		return windows.CloseHandle(handle)
	}

	r1, _, _ := syscall.SyscallN(sysNtClose, uintptr(handle))
	if r1 != 0 {
		return fmt.Errorf("NtClose failed: NTSTATUS 0x%08X", r1)
	}
	return nil
}
