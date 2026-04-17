// Package ntapi provides low-level NT syscall wrappers that bypass
// usermode API hooks placed by anti-cheat systems.
//
// Technique: indirect syscalls via polymorphic trampolines. Each build
// generates unique code patterns through:
// - Multiple encoding variants for each real instruction
// - Dynamic junk instruction generation (30+ patterns)
// - Hash-based export resolution (no plaintext function names)
// - Per-session randomized register selection
//
// The trampoline jumps into a legitimate ntdll syscall;ret gadget,
// defeating both inline hooks and syscall-origin checks.
// If a target stub is hooked, Halo's Gate is used to find the SSN
// from neighboring clean stubs.
//
//garble:controlflow flatten_passes=3 junk_jumps=10 block_splits=5
package ntapi

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"math/big"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	initOnce sync.Once
	initErr  error

	// Function pointers to our indirect syscall trampolines
	fnNtReadVirtualMemory    uintptr
	fnNtWriteVirtualMemory   uintptr
	fnNtAllocVirtualMemory   uintptr
	fnNtProtectVirtualMemory uintptr
	fnNtQueryVirtualMemory   uintptr

	// Address of a syscall;ret gadget inside ntdll.dll
	syscallRetGadget uintptr
)

const (
	// x64 syscall stub size in ntdll (each Nt* function is 32 bytes apart)
	stubSize = 32
	// How many neighbors to scan in each direction for Halo's Gate
	maxNeighborScan = 25
)

// --------------------------------------------------------------------------
// DJB2 hash-based export resolution — eliminates plaintext Nt* strings
// --------------------------------------------------------------------------

// Precomputed DJB2 hashes of target function names.
// These are computed at build time and never stored as strings.
const (
	hashNtReadVirtualMemory     uint32 = 0xc24062e3 // djb2("NtReadVirtualMemory")
	hashNtWriteVirtualMemory    uint32 = 0x95f3a792 // djb2("NtWriteVirtualMemory")
	hashNtAllocateVirtualMemory uint32 = 0x6793c34c // djb2("NtAllocateVirtualMemory")
	hashNtProtectVirtualMemory  uint32 = 0x082962c8 // djb2("NtProtectVirtualMemory")
	hashNtQueryVirtualMemory    uint32 = 0xe39d8e5d // djb2("NtQueryVirtualMemory")
)

// djb2 computes the DJB2 hash of a byte slice.
func djb2(data []byte) uint32 {
	var hash uint32 = 5381
	for _, b := range data {
		hash = ((hash << 5) + hash) + uint32(b)
	}
	return hash
}

// resolveExportByHash walks the PE export table of a loaded module
// and returns the address of the export whose name matches targetHash.
// This avoids calling GetProcAddress (which is logged by ETW).
func resolveExportByHash(moduleBase uintptr, targetHash uint32) (uintptr, error) {
	// Parse DOS header
	dosHeader := (*[64]byte)(unsafe.Pointer(moduleBase))
	if dosHeader[0] != 'M' || dosHeader[1] != 'Z' {
		return 0, fmt.Errorf("invalid DOS header")
	}
	peOffset := *(*uint32)(unsafe.Pointer(moduleBase + 0x3C))

	// Parse PE signature + optional header
	peBase := moduleBase + uintptr(peOffset)
	peHdr := (*[6]byte)(unsafe.Pointer(peBase))
	if peHdr[0] != 'P' || peHdr[1] != 'E' {
		return 0, fmt.Errorf("invalid PE header")
	}

	// Optional header starts at PE + 24
	optBase := peBase + 24
	// Export directory RVA is at optional header + 112 (for PE32+)
	exportDirRVA := *(*uint32)(unsafe.Pointer(optBase + 112))
	if exportDirRVA == 0 {
		return 0, fmt.Errorf("no export directory")
	}

	// Parse export directory
	exportDir := moduleBase + uintptr(exportDirRVA)
	numberOfNames := *(*uint32)(unsafe.Pointer(exportDir + 24))
	addressTableRVA := *(*uint32)(unsafe.Pointer(exportDir + 28))
	nameTableRVA := *(*uint32)(unsafe.Pointer(exportDir + 32))
	ordinalTableRVA := *(*uint32)(unsafe.Pointer(exportDir + 36))

	nameTable := moduleBase + uintptr(nameTableRVA)
	ordinalTable := moduleBase + uintptr(ordinalTableRVA)
	addressTable := moduleBase + uintptr(addressTableRVA)

	for i := uint32(0); i < numberOfNames; i++ {
		nameRVA := *(*uint32)(unsafe.Pointer(nameTable + uintptr(i*4)))
		nameAddr := moduleBase + uintptr(nameRVA)

		// Read the name (null-terminated ASCII)
		var nameBytes []byte
		for j := uintptr(0); j < 128; j++ {
			b := *(*byte)(unsafe.Pointer(nameAddr + j))
			if b == 0 {
				break
			}
			nameBytes = append(nameBytes, b)
		}

		if djb2(nameBytes) == targetHash {
			ordinal := *(*uint16)(unsafe.Pointer(ordinalTable + uintptr(i*2)))
			funcRVA := *(*uint32)(unsafe.Pointer(addressTable + uintptr(ordinal*4)))
			return moduleBase + uintptr(funcRVA), nil
		}
	}

	return 0, fmt.Errorf("export with hash 0x%08x not found", targetHash)
}

// --------------------------------------------------------------------------
// Stub detection and SSN extraction
// --------------------------------------------------------------------------

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
// sequence inside a clean ntdll stub.
func findSyscallRetGadget(stubAddr uintptr) uintptr {
	mem := (*[32]byte)(unsafe.Pointer(stubAddr))
	for i := 0; i < 30; i++ {
		if mem[i] == 0x0F && mem[i+1] == 0x05 && mem[i+2] == 0xC3 {
			return stubAddr + uintptr(i)
		}
	}
	return 0
}

// resolveSSNByHash resolves SSN using hash-based export resolution + Halo's Gate.
func resolveSSNByHash(ntdllBase uintptr, nameHash uint32) (uint32, uintptr, error) {
	proc, err := resolveExportByHash(ntdllBase, nameHash)
	if err != nil {
		return 0, 0, err
	}

	// Direct extraction: stub is clean
	if isCleanStub(proc) {
		return extractSSN(proc), proc, nil
	}

	// Halo's Gate: target is hooked, scan neighbors
	for offset := 1; offset <= maxNeighborScan; offset++ {
		up := proc - uintptr(offset*stubSize)
		if isCleanStub(up) {
			neighborSSN := extractSSN(up)
			return neighborSSN + uint32(offset), up, nil
		}

		down := proc + uintptr(offset*stubSize)
		if isCleanStub(down) {
			neighborSSN := extractSSN(down)
			if neighborSSN >= uint32(offset) {
				return neighborSSN - uint32(offset), down, nil
			}
		}
	}

	return 0, 0, fmt.Errorf("hash 0x%08x: resolution failed", nameHash)
}

// --------------------------------------------------------------------------
// Polymorphic junk instruction generation — 30+ unique patterns
// --------------------------------------------------------------------------

// cryptRandN returns a crypto/rand integer in [0, n).
func cryptRandN(n int) int {
	val, _ := rand.Int(rand.Reader, big.NewInt(int64(n)))
	return int(val.Int64())
}

// cryptRandBytes fills a byte slice with crypto/rand data.
func cryptRandBytes(b []byte) {
	rand.Read(b)
}

// generateJunkInstruction returns a single NOP-equivalent x64 instruction.
// 28 distinct patterns, selected randomly via crypto/rand.
// Only uses registers that don't interfere with syscall ABI (avoids r10, rax, rcx, rdx, r8, r9).
// NOTE: LAHF/SAHF removed — LAHF writes FLAGS into AH, corrupting EAX (SSN) in trampoline.
func generateJunkInstruction() []byte {
	// Safe registers for junk: rbx, rbp, rsi, rdi, r12, r13, r14, r15
	// REX.W prefix (0x48) for 64-bit, REX.WB (0x49) for r8-r15
	safeRegs64 := []byte{0xDB, 0xED, 0xF6, 0xFF} // rbx, rbp, rsi, rdi (ModRM reg,reg)
	safeRegsExt := []byte{0xE4, 0xED, 0xF6, 0xFF} // r12, r13, r14, r15

	pick := cryptRandN(30)
	switch {
	case pick < 4:
		// NOP variants (1-5 bytes)
		nops := [][]byte{
			{0x90},                                     // nop
			{0x66, 0x90},                               // 66 nop
			{0x0F, 0x1F, 0x00},                         // nop dword [rax]
			{0x0F, 0x1F, 0x40, 0x00},                   // nop dword [rax+0]
			{0x0F, 0x1F, 0x44, 0x00, 0x00},             // nop dword [rax+rax*1+0]
			{0x66, 0x0F, 0x1F, 0x44, 0x00, 0x00},       // nop word [rax+rax*1+0]
		}
		return nops[cryptRandN(len(nops))]

	case pick < 8:
		// XCHG reg, reg (same) — no-op: 48 87 xx
		r := safeRegs64[cryptRandN(len(safeRegs64))]
		return []byte{0x48, 0x87, r}

	case pick < 12:
		// MOV reg, reg (same) — no-op: 48 89 xx (xx = 0xC0 | reg<<3 | reg)
		regIdx := cryptRandN(4) // rbx=3, rbp=5, rsi=6, rdi=7
		regNums := []byte{3, 5, 6, 7}
		rn := regNums[regIdx]
		modrm := byte(0xC0) | (rn << 3) | rn
		return []byte{0x48, 0x89, modrm}

	case pick < 15:
		// LEA reg, [reg] — no-op: 48 8D xx
		regNums := []byte{3, 5, 6, 7} // rbx, rbp, rsi, rdi
		rn := regNums[cryptRandN(len(regNums))]
		modrm := (rn << 3) | rn
		return []byte{0x48, 0x8D, modrm}

	case pick < 18:
		// PUSH/POP same register — no-op
		pushRegs := []byte{0x53, 0x55, 0x56, 0x57} // push rbx/rbp/rsi/rdi
		popRegs := []byte{0x5B, 0x5D, 0x5E, 0x5F}  // pop rbx/rbp/rsi/rdi
		idx := cryptRandN(len(pushRegs))
		return []byte{pushRegs[idx], popRegs[idx]}

	case pick < 20:
		// PUSH/POP r12-r15 — no-op: 41 5x / 41 5x
		idx := cryptRandN(4) // r12=4, r13=5, r14=6, r15=7
		pushOp := byte(0x54) + byte(idx)
		popOp := byte(0x5C) + byte(idx)
		return []byte{0x41, pushOp, 0x41, popOp}

	case pick < 22:
		// Single-byte no-ops: CLC, STC, CMC, CLD
		singles := []byte{0xF8, 0xF9, 0xF5, 0xFC}
		return []byte{singles[cryptRandN(len(singles))]}

	case pick < 24:
		// FNOP — x87 no-op
		return []byte{0xD9, 0xD0}

	case pick < 26:
		// TEST reg, reg + JZ $+0 (conditional jump over nothing)
		regNums := []byte{3, 5, 6, 7}
		rn := regNums[cryptRandN(len(regNums))]
		modrm := byte(0xC0) | (rn << 3) | rn
		return []byte{0x48, 0x85, modrm, 0x74, 0x00} // test reg,reg; jz +0

	case pick < 28:
		// XCHG r12-r15, r12-r15 (same) — no-op: 4D 87 xx
		r := safeRegsExt[cryptRandN(len(safeRegsExt))]
		return []byte{0x4D, 0x87, r}

	default:
		// ADD reg, 0 — no-op: 48 83 /0 reg 00
		regNums := []byte{0xC3, 0xC5, 0xC6, 0xC7} // add rbx/rbp/rsi/rdi, 0
		return []byte{0x48, 0x83, regNums[cryptRandN(len(regNums))], 0x00}
	}
}

// GenerateJunkBlock is the exported variant — used by sibling packages
// (presenter/inject.go) to share the same polymorphism primitive.
func GenerateJunkBlock() []byte { return generateJunkBlock() }

// CryptRandN exposes the same cryptographically-random integer source used
// internally by the ntapi polymorphism routines.
func CryptRandN(n int) int { return cryptRandN(n) }

// generateJunkBlock returns 1-4 random junk instructions concatenated.
func generateJunkBlock() []byte {
	count := cryptRandN(4) + 1
	var out []byte
	for i := 0; i < count; i++ {
		out = append(out, generateJunkInstruction()...)
	}
	return out
}

// --------------------------------------------------------------------------
// Polymorphic trampoline builder — multiple encoding variants per instruction
// --------------------------------------------------------------------------

// emitMovR10Rcx generates "mov r10, rcx" with one of several encodings.
func emitMovR10Rcx() []byte {
	switch cryptRandN(3) {
	case 0:
		// Direct: mov r10, rcx
		return []byte{0x4C, 0x8B, 0xD1}
	case 1:
		// Alt encoding: mov r10, rcx (REX.WR)
		return []byte{0x49, 0x89, 0xCA}
	default:
		// push rcx; pop r10
		return []byte{0x51, 0x41, 0x5A}
	}
}

// emitMovEaxSSN generates "mov eax, <SSN>" with one of several encodings.
func emitMovEaxSSN(ssn uint32) []byte {
	ssnBytes := [4]byte{}
	binary.LittleEndian.PutUint32(ssnBytes[:], ssn)

	switch cryptRandN(4) {
	case 0:
		// mov eax, imm32: B8 xx xx xx xx
		return append([]byte{0xB8}, ssnBytes[:]...)
	case 1:
		// xor eax,eax; or eax,imm32: 33 C0 0D xx xx xx xx
		out := []byte{0x33, 0xC0, 0x0D}
		return append(out, ssnBytes[:]...)
	case 2:
		// xor eax,eax; add eax,imm32: 33 C0 05 xx xx xx xx
		out := []byte{0x33, 0xC0, 0x05}
		return append(out, ssnBytes[:]...)
	default:
		// push imm8; pop rax (only if SSN < 128)
		if ssn < 128 {
			return []byte{0x6A, byte(ssn), 0x58}
		}
		// Fallback to standard mov eax
		return append([]byte{0xB8}, ssnBytes[:]...)
	}
}

// emitMovR11Gadget generates "mov r11, <addr>" with one of several encodings.
func emitMovR11Gadget(addr uintptr) []byte {
	addrBytes := [8]byte{}
	binary.LittleEndian.PutUint64(addrBytes[:], uint64(addr))

	switch cryptRandN(2) {
	case 0:
		// mov r11, imm64: 49 BB xx xx xx xx xx xx xx xx
		return append([]byte{0x49, 0xBB}, addrBytes[:]...)
	default:
		// movabs r11, imm64: same encoding, different assembler mnemonic
		// Use lea-relative if addr fits, else standard
		return append([]byte{0x49, 0xBB}, addrBytes[:]...)
	}
}

// emitJmpR11 emits "jmp r11" (41 FF E3). Only the direct form: the alternative
// "push r11; ret" variant ran afoul of the CPU Return Stack Buffer predictor
// and was a suspect in the sporadic first-boot trampoline crash, so we pin to
// the deterministic encoding.
func emitJmpR11() []byte {
	return []byte{0x41, 0xFF, 0xE3}
}

// buildPolymorphicTrampoline builds a fully polymorphic indirect syscall trampoline.
// Each invocation produces unique byte patterns through:
// - Random junk instructions between every real instruction (1-4 per gap)
// - Multiple encoding variants for each of the 4 real instructions
// NOTE: no junk between mov r11, gadget and the final dispatch — a prior sporadic
// crash had r11 observed as a small garbage value on entry to the gadget, and
// any junk instruction in that gap is a candidate for the corruption even when
// its encoding "shouldn't" touch r11.
func buildPolymorphicTrampoline(ssn uint32, gadget uintptr) ([]byte, error) {
	var code []byte

	// [junk] mov r10, rcx [junk] mov eax, SSN [junk] mov r11, gadget jmp r11
	code = append(code, generateJunkBlock()...)
	code = append(code, emitMovR10Rcx()...)
	code = append(code, generateJunkBlock()...)
	code = append(code, emitMovEaxSSN(ssn)...)
	code = append(code, generateJunkBlock()...)
	code = append(code, emitMovR11Gadget(gadget)...)
	code = append(code, emitJmpR11()...)

	return code, nil
}

// --------------------------------------------------------------------------
// Bootstrap: raw syscall for initial VirtualAlloc (before trampolines exist)
// --------------------------------------------------------------------------

// rawSyscall performs a direct syscall instruction for NtAllocateVirtualMemory.
// Used only during bootstrap when no trampoline exists yet.
// After bootstrap, all allocations go through the trampoline.
func bootstrapAllocateRWX(size uintptr) (uintptr, error) {
	// We need NtAllocateVirtualMemory SSN to do a raw syscall.
	// But we can also just use VirtualAlloc for the initial bootstrap allocation
	// since at this point we haven't done anything suspicious yet.
	// This is a single allocation at startup — acceptable risk.
	addr, err := windows.VirtualAlloc(0, size,
		windows.MEM_COMMIT|windows.MEM_RESERVE,
		windows.PAGE_READWRITE)
	if err != nil {
		return 0, fmt.Errorf("bootstrap alloc: %w", err)
	}
	return addr, nil
}

// allocateTrampoline allocates memory, writes polymorphic code, and sets RX permissions.
func allocateTrampoline(ssn uint32, gadget uintptr) (uintptr, error) {
	code, err := buildPolymorphicTrampoline(ssn, gadget)
	if err != nil {
		return 0, err
	}

	// Allocate RW memory (no execute yet — avoids RWX anomaly)
	allocSize := uintptr(len(code))
	addr, err := bootstrapAllocateRWX(allocSize)
	if err != nil {
		return 0, err
	}

	// Copy code into writable page
	dst := unsafe.Slice((*byte)(unsafe.Pointer(addr)), len(code))
	copy(dst, code)

	// Flip to RX (executable, non-writable) — page is never RWX
	var oldProtect uint32
	err = windows.VirtualProtect(addr, allocSize, windows.PAGE_EXECUTE_READ, &oldProtect)
	if err != nil {
		return 0, fmt.Errorf("protect RW→RX: %w", err)
	}

	return addr, nil
}

// --------------------------------------------------------------------------
// Module base resolution via PEB walk (no LoadLibrary/GetModuleHandle)
// --------------------------------------------------------------------------

type listEntry struct {
	Flink uintptr
	Blink uintptr
}

// getNtdllBase walks the PEB's InMemoryOrderModuleList to find ntdll.dll base.
// This avoids calling LoadLibrary or GetModuleHandle (which are monitored).
func getNtdllBase() (uintptr, error) {
	// Read PEB from TEB (GS:[0x60] on x64)
	// TEB.ProcessEnvironmentBlock is at offset 0x60
	type teb struct {
		_   [0x60]byte
		Peb uintptr
	}

	// Get TEB via NtCurrentTeb() equivalent — read GS segment
	// On Windows x64, TEB is at GS:[0x30], PEB is at GS:[0x60]
	// We use the windows package helper
	pebAddr := getPEB()
	if pebAddr == 0 {
		return 0, fmt.Errorf("failed to read PEB")
	}

	// PEB.Ldr is at offset 0x18
	ldr := *(*uintptr)(unsafe.Pointer(pebAddr + 0x18))
	if ldr == 0 {
		return 0, fmt.Errorf("PEB.Ldr is null")
	}

	// PEB_LDR_DATA.InMemoryOrderModuleList is at offset 0x20
	listHead := ldr + 0x20
	flink := *(*uintptr)(unsafe.Pointer(listHead))

	// Walk the list. Entry 0 = main exe, entry 1 = ntdll.dll
	count := 0
	for current := flink; current != listHead && count < 256; {
		count++
		// `current` points to LDR_DATA_TABLE_ENTRY.InMemoryOrderLinks (entry+0x10).
		// LDR_DATA_TABLE_ENTRY layout on x64:
		//   +0x00 InLoadOrderLinks (LIST_ENTRY, 16B)
		//   +0x10 InMemoryOrderLinks     ← current points here
		//   +0x20 InInitializationOrderLinks
		//   +0x30 DllBase                  → offset 0x20 from current
		//   +0x38 EntryPoint
		//   +0x40 SizeOfImage
		//   +0x48 FullDllName (UNICODE_STRING, 16B: Length u16, MaxLength u16, pad u32, Buffer u64)
		//   +0x58 BaseDllName (UNICODE_STRING, 16B)   → offset 0x48 from current
		// The buffer pointer inside a UNICODE_STRING is at +8 (after Length/MaxLength/pad).
		// Previously this code used +0x50 for BaseDllName, which was off by 8 bytes
		// and read into FullDllName's Buffer pointer high half — the length looked
		// like garbage and ntdll was never found. The LoadLibrary fallback masked it.
		dllBase := *(*uintptr)(unsafe.Pointer(current + 0x20))

		const baseDllNameOff = 0x48
		nameLen := *(*uint16)(unsafe.Pointer(current + baseDllNameOff))
		nameBuf := *(*uintptr)(unsafe.Pointer(current + baseDllNameOff + 8))

		if nameLen > 0 && nameBuf != 0 {
			// Read UTF-16 name
			chars := nameLen / 2
			if chars > 64 {
				chars = 64
			}
			nameSlice := unsafe.Slice((*uint16)(unsafe.Pointer(nameBuf)), chars)

			// Check if this is ntdll.dll (case-insensitive)
			if matchesNtdll(nameSlice) {
				return dllBase, nil
			}
		}

		current = *(*uintptr)(unsafe.Pointer(current))
	}

	return 0, fmt.Errorf("ntdll not found in PEB module list")
}

// matchesNtdll does a case-insensitive comparison against "ntdll.dll".
func matchesNtdll(name []uint16) bool {
	target := []uint16{'n', 't', 'd', 'l', 'l', '.', 'd', 'l', 'l'}
	if len(name) != len(target) {
		return false
	}
	for i, c := range name {
		// toLower for ASCII
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		if c != target[i] {
			return false
		}
	}
	return true
}

// getPEB reads PEB base address directly from GS:[0x60] via assembly
// (see peb_windows_amd64.s). No syscall, no API string in the binary.
func getPEB() uintptr {
	return getPEBDirect()
}

// --------------------------------------------------------------------------
// Initialization
// --------------------------------------------------------------------------

func initialize() {
	initOnce.Do(func() {
		// Step 1: Get ntdll base address via PEB walk. LoadLibrary("ntdll.dll")
		// is a last-resort fallback — if PEB walk returns a bogus base we still
		// want the bot to work, and garble -literals obfuscates the string in
		// the release binary so it's not a visible signature. The fallback is
		// ETW-monitored but the chain is: PEB walk first, only fall through on
		// hard failure, so production builds should never take this path.
		ntdllBase, err := getNtdllBase()
		if err != nil || ntdllBase == 0 {
			ntdllHandle, loadErr := windows.LoadLibrary("ntdll.dll")
			if loadErr != nil {
				initErr = fmt.Errorf("ntdll PEB walk: %v; LoadLibrary fallback: %w", err, loadErr)
				return
			}
			ntdllBase = uintptr(ntdllHandle)
		}

		// Step 2: Find syscall;ret gadget
		// Resolve NtReadVirtualMemory by hash to find a clean stub for gadget
		readProc, err := resolveExportByHash(ntdllBase, hashNtReadVirtualMemory)
		if err != nil {
			initErr = fmt.Errorf("resolve read export: %w", err)
			return
		}

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
			initErr = fmt.Errorf("could not find syscall;ret gadget")
			return
		}

		// Step 3: Resolve SSNs by hash and build polymorphic trampolines
		type ssnEntry struct {
			hash uint32
			dst  *uintptr
		}

		entries := []ssnEntry{
			{hashNtReadVirtualMemory, &fnNtReadVirtualMemory},
			{hashNtWriteVirtualMemory, &fnNtWriteVirtualMemory},
			{hashNtAllocateVirtualMemory, &fnNtAllocVirtualMemory},
			{hashNtProtectVirtualMemory, &fnNtProtectVirtualMemory},
			{hashNtQueryVirtualMemory, &fnNtQueryVirtualMemory},
		}

		for _, e := range entries {
			ssn, _, err := resolveSSNByHash(ntdllBase, e.hash)
			if err != nil {
				initErr = err
				return
			}
			trampoline, err := allocateTrampoline(ssn, syscallRetGadget)
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

// --------------------------------------------------------------------------
// Public API — NO fallback to hooked APIs
// --------------------------------------------------------------------------

// ReadProcessMemory reads memory from a remote process via indirect syscall.
// Returns error if trampoline is not initialized — no silent fallback.
func ReadProcessMemory(handle windows.Handle, addr uintptr, buf *byte, size uintptr) error {
	initialize()
	if initErr != nil {
		return fmt.Errorf("syscall not available: %w", initErr)
	}

	var bytesRead uintptr
	r1, _, _ := rawSyscallN(
		fnNtReadVirtualMemory,
		uintptr(handle),
		addr,
		uintptr(unsafe.Pointer(buf)),
		size,
		uintptr(unsafe.Pointer(&bytesRead)),
	)
	if r1 != 0 {
		return fmt.Errorf("read failed: status 0x%X", r1)
	}
	return nil
}

// WriteProcessMemory writes memory to a remote process via indirect syscall.
// Returns error if trampoline is not initialized — no silent fallback.
func WriteProcessMemory(handle windows.Handle, addr uintptr, buf *byte, size uintptr) error {
	initialize()
	if initErr != nil {
		return fmt.Errorf("syscall not available: %w", initErr)
	}

	var bytesWritten uintptr
	r1, _, _ := rawSyscallN(
		fnNtWriteVirtualMemory,
		uintptr(handle),
		addr,
		uintptr(unsafe.Pointer(buf)),
		size,
		uintptr(unsafe.Pointer(&bytesWritten)),
	)
	if r1 != 0 {
		return fmt.Errorf("write failed: status 0x%X", r1)
	}
	return nil
}

// MemoryBasicInformation mirrors the Win32 MEMORY_BASIC_INFORMATION
// returned by NtQueryVirtualMemory(MemoryBasicInformation = 0).
type MemoryBasicInformation struct {
	BaseAddress       uintptr
	AllocationBase    uintptr
	AllocationProtect uint32
	_pad1             uint32 // alignment for x64
	RegionSize        uintptr
	State             uint32
	Protect           uint32
	Type              uint32
	_pad2             uint32 // alignment for x64
}

// Memory page state/protect constants (subset matching Win32 headers).
const (
	MEM_COMMIT      uint32 = 0x1000
	MEM_FREE        uint32 = 0x10000
	MEM_RESERVE     uint32 = 0x2000
	PAGE_NOACCESS   uint32 = 0x01
	PAGE_READONLY   uint32 = 0x02
	PAGE_READWRITE  uint32 = 0x04
	PAGE_EXECUTE    uint32 = 0x10
	PAGE_GUARD      uint32 = 0x100
)

// QueryVirtualMemory queries the page state of a remote address via indirect
// syscall. Returns the MEMORY_BASIC_INFORMATION for the region containing
// `addr`. Returns (zero, error) if the syscall fails — callers MUST treat
// failure as "page not safely readable" and fall back to a tiny direct read.
//
// Used by Layer 3 chunk cache to avoid blind 4-8KB reads that would straddle
// VAD boundaries / unmapped pages — those crash D2R or trip Arxan via SEH.
func QueryVirtualMemory(handle windows.Handle, addr uintptr) (MemoryBasicInformation, error) {
	initialize()
	if initErr != nil {
		return MemoryBasicInformation{}, fmt.Errorf("syscall not available: %w", initErr)
	}
	var info MemoryBasicInformation
	var returnLen uintptr
	r1, _, _ := rawSyscallN(
		fnNtQueryVirtualMemory,
		uintptr(handle),
		addr,
		0, // MemoryInformationClass = 0 = MemoryBasicInformation
		uintptr(unsafe.Pointer(&info)),
		unsafe.Sizeof(info),
		uintptr(unsafe.Pointer(&returnLen)),
	)
	if r1 != 0 {
		return MemoryBasicInformation{}, fmt.Errorf("query failed: status 0x%X", r1)
	}
	return info, nil
}

// ProtectVirtualMemory changes memory protection in a remote process via indirect syscall.
// NtProtectVirtualMemory(ProcessHandle, *BaseAddress, *RegionSize, NewProtect, *OldProtect)
func ProtectVirtualMemory(handle windows.Handle, addr uintptr, size uintptr, newProtect uint32) (oldProtect uint32, err error) {
	initialize()
	if initErr != nil {
		return 0, fmt.Errorf("syscall not available: %w", initErr)
	}

	baseAddr := addr
	regionSize := size
	r1, _, _ := rawSyscallN(
		fnNtProtectVirtualMemory,
		uintptr(handle),
		uintptr(unsafe.Pointer(&baseAddr)),
		uintptr(unsafe.Pointer(&regionSize)),
		uintptr(newProtect),
		uintptr(unsafe.Pointer(&oldProtect)),
	)
	if r1 != 0 {
		return 0, fmt.Errorf("protect failed: status 0x%X", r1)
	}
	return oldProtect, nil
}

// WriteCodePage writes to a code page (.text) in a remote process via indirect syscalls.
// Changes protection to RWX, writes, restores — all through indirect syscalls.
func WriteCodePage(handle windows.Handle, addr uintptr, buf *byte, size uintptr) error {
	oldProtect, err := ProtectVirtualMemory(handle, addr, size, 0x40) // PAGE_EXECUTE_READWRITE
	if err != nil {
		return fmt.Errorf("protect RWX: %w", err)
	}

	writeErr := WriteProcessMemory(handle, addr, buf, size)

	// Always restore
	ProtectVirtualMemory(handle, addr, size, oldProtect)

	return writeErr
}

// rawSyscallN calls a trampoline with variable arguments.
// This is a thin wrapper around syscall.SyscallN for our trampolines.
func rawSyscallN(trap uintptr, args ...uintptr) (r1, r2 uintptr, err error) {
	switch len(args) {
	case 0:
		return syscallN(trap)
	case 1:
		return syscallN(trap, args[0])
	case 2:
		return syscallN(trap, args[0], args[1])
	case 3:
		return syscallN(trap, args[0], args[1], args[2])
	case 4:
		return syscallN(trap, args[0], args[1], args[2], args[3])
	case 5:
		return syscallN(trap, args[0], args[1], args[2], args[3], args[4])
	case 6:
		return syscallN(trap, args[0], args[1], args[2], args[3], args[4], args[5])
	default:
		return syscallN(trap, args...)
	}
}

// syscallN is the actual syscall dispatcher.
var syscallN = syscallNImpl

func syscallNImpl(trap uintptr, args ...uintptr) (r1, r2 uintptr, err error) {
	r1, r2, callErr := syscall.SyscallN(trap, args...)
	return r1, r2, callErr
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
