// Package ntapi provides low-level NT syscall wrappers that bypass
// usermode API hooks placed by anti-cheat systems.
//
// Technique: indirect syscalls. We resolve the SSN from ntdll stubs,
// then build a trampoline that sets up registers but jumps into a
// legitimate ntdll syscall;ret gadget instead of executing syscall
// from our own memory. This defeats both inline hooks and
// syscall-origin checks (which flag syscall from non-ntdll pages).
//
// If a target stub is hooked (inline patch detected), we use Halo's Gate:
// scan neighboring syscall stubs up/down to find an unhooked one and
// calculate the target SSN from the offset.
package ntapi

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	initOnce sync.Once
	initErr  error

	// Function pointers to our indirect syscall trampolines
	fnNtReadVirtualMemory  uintptr
	fnNtWriteVirtualMemory uintptr

	// Address of a syscall;ret gadget inside ntdll.dll
	syscallRetGadget uintptr
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

// findSyscallRetGadget locates a "syscall; ret" (0F 05 C3) instruction
// sequence inside a clean ntdll stub. This address is used by trampolines
// to execute the syscall instruction from ntdll's own memory pages,
// defeating syscall-origin detection.
func findSyscallRetGadget(stubAddr uintptr) uintptr {
	// Scan forward from the stub start (up to 32 bytes) for 0F 05 C3
	mem := (*[32]byte)(unsafe.Pointer(stubAddr))
	for i := 0; i < 30; i++ {
		if mem[i] == 0x0F && mem[i+1] == 0x05 && mem[i+2] == 0xC3 {
			return stubAddr + uintptr(i)
		}
	}
	return 0
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

// nopSleds contains NOP-equivalent x64 instructions that don't affect
// registers used by the syscall (r10, rax, r11). Each trampoline gets
// random junk inserted between real instructions, defeating memory
// pattern scanning.
var nopSleds = [][]byte{
	{0x90},                         // nop
	{0x66, 0x90},                   // 66 nop
	{0x0F, 0x1F, 0x00},            // nop dword ptr [rax]
	{0x0F, 0x1F, 0x40, 0x00},      // nop dword ptr [rax+0]
	{0x48, 0x87, 0xDB},            // xchg rbx, rbx
	{0x48, 0x87, 0xED},            // xchg rbp, rbp
	{0x87, 0xDB},                   // xchg ebx, ebx
	{0x0F, 0x1F, 0x44, 0x00, 0x00}, // nop dword ptr [rax+rax*1+0]
}

// randNops returns 1-3 random NOP-equivalent instructions.
func randNops() []byte {
	n, _ := rand.Int(rand.Reader, big.NewInt(3))
	count := int(n.Int64()) + 1
	var out []byte
	for i := 0; i < count; i++ {
		idx, _ := rand.Int(rand.Reader, big.NewInt(int64(len(nopSleds))))
		out = append(out, nopSleds[idx.Int64()]...)
	}
	return out
}

// buildTrampoline allocates executable memory and writes an indirect
// syscall stub with randomized NOP padding. The trampoline jumps into
// the real syscall;ret gadget inside ntdll.dll, and random NOP-equivalent
// instructions are inserted between real instructions to defeat
// signature-based memory scanning.
//
// The generated code (with random NOPs between each real instruction):
//
//	[random NOPs]
//	mov r10, rcx              ; 4C 8B D1
//	[random NOPs]
//	mov eax, <SSN>            ; B8 xx xx xx xx
//	[random NOPs]
//	mov r11, <gadget_addr>    ; 49 BB xx xx xx xx xx xx xx xx
//	jmp r11                   ; 41 FF E3
func buildTrampoline(ssn uint32, gadget uintptr) (uintptr, error) {
	// Build code with random NOP padding
	var code []byte
	code = append(code, randNops()...)
	code = append(code, 0x4C, 0x8B, 0xD1) // mov r10, rcx
	code = append(code, randNops()...)
	code = append(code, 0xB8, byte(ssn), byte(ssn>>8), byte(ssn>>16), byte(ssn>>24)) // mov eax, SSN
	code = append(code, randNops()...)

	// mov r11, <gadget_addr>
	ga := uint64(gadget)
	gadgetBytes := []byte{0x49, 0xBB}
	for i := 0; i < 8; i++ {
		gadgetBytes = append(gadgetBytes, byte(ga>>(i*8)))
	}
	code = append(code, gadgetBytes...)
	code = append(code, 0x41, 0xFF, 0xE3) // jmp r11

	// Allocate RW memory (no execute yet — avoids RWX anomaly)
	allocSize := uintptr(len(code))
	addr, err := windows.VirtualAlloc(0, allocSize,
		windows.MEM_COMMIT|windows.MEM_RESERVE,
		windows.PAGE_READWRITE)
	if err != nil {
		return 0, fmt.Errorf("VirtualAlloc trampoline: %w", err)
	}

	// Copy code into writable page
	dst := unsafe.Slice((*byte)(unsafe.Pointer(addr)), len(code))
	copy(dst, code)

	// Flip to RX (executable, non-writable) — page is never RWX
	var oldProtect uint32
	err = windows.VirtualProtect(addr, allocSize, windows.PAGE_EXECUTE_READ, &oldProtect)
	if err != nil {
		return 0, fmt.Errorf("VirtualProtect RW→RX: %w", err)
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

		// Find a syscall;ret gadget from a known clean stub.
		// We use NtReadVirtualMemory's stub to locate the gadget.
		readProc, err := syscall.GetProcAddress(syscall.Handle(ntdll), "NtReadVirtualMemory")
		if err != nil {
			initErr = fmt.Errorf("resolve NtReadVirtualMemory for gadget: %w", err)
			return
		}

		// Try the target stub first; if hooked, scan neighbors for a clean one
		gadgetSource := readProc
		if !isCleanStub(readProc) {
			for i := 1; i <= maxNeighborScan; i++ {
				if isCleanStub(readProc + uintptr(i*stubSize)) {
					gadgetSource = readProc + uintptr(i*stubSize)
					break
				}
				if isCleanStub(readProc - uintptr(i*stubSize)) {
					gadgetSource = readProc - uintptr(i*stubSize)
					break
				}
			}
		}

		syscallRetGadget = findSyscallRetGadget(gadgetSource)
		if syscallRetGadget == 0 {
			initErr = fmt.Errorf("could not find syscall;ret gadget in ntdll")
			return
		}

		// Resolve Read/Write syscalls only. OpenProcess stays standard
		// because NtOpenProcess via direct syscall crashes D2R.
		type ssnEntry struct {
			name string
			dst  *uintptr
		}

		entries := []ssnEntry{
			{"NtReadVirtualMemory", &fnNtReadVirtualMemory},
			{"NtWriteVirtualMemory", &fnNtWriteVirtualMemory},
		}

		for _, e := range entries {
			ssn, err := resolveSSN(ntdll, e.name)
			if err != nil {
				initErr = err
				return
			}
			trampoline, err := buildTrampoline(ssn, syscallRetGadget)
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

// syscallJitter adds a tiny random delay (0–50µs) before syscalls
// to break deterministic timing patterns that anti-cheat can fingerprint.
func syscallJitter() {
	n, _ := rand.Int(rand.Reader, big.NewInt(50))
	if n.Int64() > 0 {
		time.Sleep(time.Duration(n.Int64()) * time.Microsecond)
	}
}

// ReadProcessMemory reads memory from a remote process via NtReadVirtualMemory.
// Falls back to standard API if syscall trampoline fails.
func ReadProcessMemory(handle windows.Handle, addr uintptr, buf *byte, size uintptr) error {
	initialize()
	if initErr != nil {
		return windows.ReadProcessMemory(handle, addr, buf, size, nil)
	}

	syscallJitter()

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
		return windows.ReadProcessMemory(handle, addr, buf, size, nil)
	}
	return nil
}

// WriteProcessMemory writes memory to a remote process via NtWriteVirtualMemory.
// Falls back to standard API if syscall trampoline fails.
func WriteProcessMemory(handle windows.Handle, addr uintptr, buf *byte, size uintptr) error {
	initialize()
	if initErr != nil {
		return windows.WriteProcessMemory(handle, addr, buf, size, nil)
	}

	syscallJitter()

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
		return windows.WriteProcessMemory(handle, addr, buf, size, nil)
	}
	return nil
}

// OpenProcess uses standard Windows API.
// NtOpenProcess via direct syscall crashes D2R, so we keep this standard.
func OpenProcess(access uint32, pid uint32) (windows.Handle, error) {
	return windows.OpenProcess(access, false, pid)
}

// CloseHandle uses standard Windows API.
func CloseHandle(handle windows.Handle) error {
	return windows.CloseHandle(handle)
}
