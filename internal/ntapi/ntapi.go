// Package ntapi provides low-level NT syscall wrappers that bypass
// usermode API hooks placed by anti-cheat systems.
//
// Technique: We resolve the syscall service number (SSN) from ntdll
// stubs, then build a minimal syscall trampoline in executable memory
// and call it directly. This bypasses any inline hooks on ntdll exports.
//
// If a target stub is hooked (inline patch detected), we use Halo's Gate:
// scan neighboring syscall stubs up/down to find an unhooked one and
// calculate the target SSN from the offset.
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

	// Function pointers to our syscall trampolines
	fnNtReadVirtualMemory  uintptr
	fnNtWriteVirtualMemory uintptr
	fnNtOpenProcess        uintptr
	fnNtClose              uintptr
)

const (
	// x64 syscall stub size in ntdll (each Nt* function is 32 bytes apart)
	stubSize = 32
	// How many neighbors to scan in each direction for Halo's Gate
	maxNeighborScan = 25
)

// isCleanStub checks if the bytes at addr look like an unhooked ntdll stub:
//
//	4C 8B D1    mov r10, rcx
//	B8 xx xx 00 00  mov eax, SSN
func isCleanStub(addr uintptr) bool {
	stub := (*[8]byte)(unsafe.Pointer(addr))
	return stub[0] == 0x4C && stub[1] == 0x8B && stub[2] == 0xD1 && stub[3] == 0xB8
}

// extractSSN reads the SSN from a clean stub at addr.
func extractSSN(addr uintptr) uint32 {
	stub := (*[8]byte)(unsafe.Pointer(addr))
	return uint32(stub[4]) | uint32(stub[5])<<8 | uint32(stub[6])<<16 | uint32(stub[7])<<24
}

// resolveSSN extracts the syscall service number from an ntdll export.
// If the target stub is hooked (inline patch), uses Halo's Gate:
// walks neighboring stubs to find a clean one and calculates the target SSN.
func resolveSSN(ntdll windows.Handle, name string) (uint32, error) {
	proc, err := syscall.GetProcAddress(syscall.Handle(ntdll), name)
	if err != nil {
		return 0, fmt.Errorf("resolve %s: %w", name, err)
	}

	// Direct extraction: stub is clean
	if isCleanStub(proc) {
		return extractSSN(proc), nil
	}

	// Halo's Gate: target is hooked, scan neighbors
	// Each Nt* stub in ntdll is exactly stubSize bytes apart
	for offset := 1; offset <= maxNeighborScan; offset++ {
		// Scan upward (lower addresses)
		up := proc - uintptr(offset*stubSize)
		if isCleanStub(up) {
			neighborSSN := extractSSN(up)
			return neighborSSN + uint32(offset), nil
		}

		// Scan downward (higher addresses)
		down := proc + uintptr(offset*stubSize)
		if isCleanStub(down) {
			neighborSSN := extractSSN(down)
			if neighborSSN >= uint32(offset) {
				return neighborSSN - uint32(offset), nil
			}
		}
	}

	return 0, fmt.Errorf("%s: hooked and no clean neighbor found within %d stubs", name, maxNeighborScan)
}

// buildTrampoline allocates executable memory and writes a minimal
// x64 syscall stub that invokes the given SSN directly.
//
// The generated code:
//
//	mov r10, rcx      ; 4C 8B D1
//	mov eax, <SSN>    ; B8 xx xx xx xx
//	syscall            ; 0F 05
//	ret                ; C3
func buildTrampoline(ssn uint32) (uintptr, error) {
	code := []byte{
		0x4C, 0x8B, 0xD1, // mov r10, rcx
		0xB8, 0x00, 0x00, 0x00, 0x00, // mov eax, SSN (patched below)
		0x0F, 0x05, // syscall
		0xC3, // ret
	}
	code[4] = byte(ssn)
	code[5] = byte(ssn >> 8)
	code[6] = byte(ssn >> 16)
	code[7] = byte(ssn >> 24)

	// Allocate RWX memory for the trampoline
	addr, err := windows.VirtualAlloc(0, uintptr(len(code)),
		windows.MEM_COMMIT|windows.MEM_RESERVE,
		windows.PAGE_EXECUTE_READWRITE)
	if err != nil {
		return 0, fmt.Errorf("VirtualAlloc trampoline: %w", err)
	}

	// Copy code into executable page
	dst := (*[11]byte)(unsafe.Pointer(addr))
	copy(dst[:], code)

	// Harden: change to RX (remove write)
	var oldProtect uint32
	err = windows.VirtualProtect(addr, uintptr(len(code)), windows.PAGE_EXECUTE_READ, &oldProtect)
	if err != nil {
		// Non-fatal: trampoline still works with RWX
	}

	return addr, nil
}

func initialize() {
	initOnce.Do(func() {
		ntdll, err := windows.LoadLibrary("ntdll.dll")
		if err != nil {
			initErr = fmt.Errorf("load ntdll: %w", err)
			return
		}

		type ssnEntry struct {
			name string
			dst  *uintptr
		}

		entries := []ssnEntry{
			{"NtReadVirtualMemory", &fnNtReadVirtualMemory},
			{"NtWriteVirtualMemory", &fnNtWriteVirtualMemory},
			{"NtOpenProcess", &fnNtOpenProcess},
			{"NtClose", &fnNtClose},
		}

		for _, e := range entries {
			ssn, err := resolveSSN(ntdll, e.name)
			if err != nil {
				initErr = err
				return
			}
			trampoline, err := buildTrampoline(ssn)
			if err != nil {
				initErr = err
				return
			}
			*e.dst = trampoline
		}
	})
}

// Init resolves all syscall numbers and builds trampolines.
// Call once at startup. Returns error if resolution fails.
func Init() error {
	initialize()
	return initErr
}

// ReadProcessMemory reads memory from a remote process via NtReadVirtualMemory.
func ReadProcessMemory(handle windows.Handle, addr uintptr, buf *byte, size uintptr) error {
	initialize()
	if initErr != nil {
		return windows.ReadProcessMemory(handle, addr, buf, size, nil)
	}

	var bytesRead uintptr
	r1, _, _ := syscall.SyscallN(
		fnNtReadVirtualMemory,
		uintptr(handle),
		addr,
		uintptr(unsafe.Pointer(buf)),
		size,
		uintptr(unsafe.Pointer(&bytesRead)),
	)
	if r1 != 0 {
		return fmt.Errorf("NtReadVirtualMemory: NTSTATUS 0x%08X", r1)
	}
	return nil
}

// WriteProcessMemory writes memory to a remote process via NtWriteVirtualMemory.
func WriteProcessMemory(handle windows.Handle, addr uintptr, buf *byte, size uintptr) error {
	initialize()
	if initErr != nil {
		return windows.WriteProcessMemory(handle, addr, buf, size, nil)
	}

	var bytesWritten uintptr
	r1, _, _ := syscall.SyscallN(
		fnNtWriteVirtualMemory,
		uintptr(handle),
		addr,
		uintptr(unsafe.Pointer(buf)),
		size,
		uintptr(unsafe.Pointer(&bytesWritten)),
	)
	if r1 != 0 {
		return fmt.Errorf("NtWriteVirtualMemory: NTSTATUS 0x%08X", r1)
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

// OpenProcess opens a process handle via NtOpenProcess.
func OpenProcess(access uint32, pid uint32) (windows.Handle, error) {
	initialize()
	if initErr != nil {
		return windows.OpenProcess(access, false, pid)
	}

	var handle uintptr
	cid := clientID{UniqueProcess: uintptr(pid)}
	oa := objectAttributes{Length: uint32(unsafe.Sizeof(objectAttributes{}))}

	r1, _, _ := syscall.SyscallN(
		fnNtOpenProcess,
		uintptr(unsafe.Pointer(&handle)),
		uintptr(access),
		uintptr(unsafe.Pointer(&oa)),
		uintptr(unsafe.Pointer(&cid)),
	)
	if r1 != 0 {
		return 0, fmt.Errorf("NtOpenProcess: NTSTATUS 0x%08X", r1)
	}
	return windows.Handle(handle), nil
}

// CloseHandle closes an NT handle via NtClose.
func CloseHandle(handle windows.Handle) error {
	initialize()
	if initErr != nil {
		return windows.CloseHandle(handle)
	}

	r1, _, _ := syscall.SyscallN(fnNtClose, uintptr(handle))
	if r1 != 0 {
		return fmt.Errorf("NtClose: NTSTATUS 0x%08X", r1)
	}
	return nil
}
