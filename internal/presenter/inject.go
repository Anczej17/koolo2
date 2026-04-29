package presenter

import (
	"encoding/binary"
	"fmt"
	"os"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"local/internal/svc/internal/ntapi"
)

const (
	loadModuleTimeout = 10 * time.Second
)

// loadModule manually maps a no_std Rust DLL into D2R and calls Init via APC.
// Bypasses: LoadLibraryW hooks (manual map), CreateRemoteThread hooks (APC).
//
// All verbose progress prints were removed 2026-04-11 — they were compiled
// into the release binary as identifiable strings ("LM pid=", "VirtualAllocEx",
// "queueAPC") that Warden could signature-match. Errors still propagate via
// the return chain. Set LM_TRACE=1 env var to re-enable inline tracing.
func loadModule(pid uint32, modulePath string, remoteBuf uintptr) (uintptr, error) {
	traceActive := os.Getenv("LM_TRACE") == "1" || os.Getenv("PRESENTER_ATTACH_TRACE") == "1"
	traceStart := time.Now()
	trace := func(format string, args ...interface{}) {
		if !traceActive {
			return
		}
		fmt.Fprintf(os.Stderr, "[LM] t+%dms pid=%d "+format+"\n",
			append([]interface{}{time.Since(traceStart).Milliseconds(), pid}, args...)...)
	}

	trace("begin module=%s remoteBuf=%#x", modulePath, remoteBuf)
	pe, err := os.ReadFile(modulePath)
	if err != nil {
		return 0, fmt.Errorf("read dll: %w", err)
	}
	trace("read dll bytes=%d", len(pe))

	img, err := parsePE(pe)
	if err != nil {
		return 0, fmt.Errorf("parse pe: %w", err)
	}
	trace("parsed PE imageBase=%#x size=%#x entry=%#x importRVA=%#x relocRVA=%#x", img.imageBase, img.sizeOfImage, img.entryPointRVA, img.importRVA, img.relocRVA)

	const processAccess = windows.PROCESS_CREATE_THREAD |
		windows.PROCESS_VM_OPERATION |
		windows.PROCESS_VM_READ |
		windows.PROCESS_VM_WRITE |
		windows.PROCESS_QUERY_INFORMATION

	hProc, err := ntapi.OpenProcess(processAccess, pid)
	if err != nil {
		return 0, fmt.Errorf("open target: %w", err)
	}
	defer ntapi.CloseHandle(hProc)
	trace("OpenProcess OK")

	// Prefer the image's preferred base so we avoid relocations when possible.
	remoteBase, err := virtualAllocEx(hProc, uintptr(img.imageBase), uintptr(img.sizeOfImage),
		windows.MEM_COMMIT|windows.MEM_RESERVE, windows.PAGE_EXECUTE_READWRITE)
	if err != nil || remoteBase == 0 {
		trace("preferred VirtualAllocEx failed base=%#x err=%v; retrying anywhere", img.imageBase, err)
		remoteBase, err = virtualAllocEx(hProc, 0, uintptr(img.sizeOfImage),
			windows.MEM_COMMIT|windows.MEM_RESERVE, windows.PAGE_EXECUTE_READWRITE)
		if err != nil {
			return 0, fmt.Errorf("alloc image: %w", err)
		}
	}
	trace("remote image allocated base=%#x size=%#x", remoteBase, img.sizeOfImage)
	cleanupRemote := func() {
		if remoteBase != 0 {
			_ = virtualFreeEx(hProc, remoteBase, 0, windows.MEM_RELEASE)
			remoteBase = 0
		}
	}

	// Build mapped image locally.
	mapped := make([]byte, img.sizeOfImage)
	copy(mapped, pe[:min32(img.sizeOfHeaders, uint32(len(pe)))])
	for _, s := range img.sections {
		if s.rawSize == 0 || s.rawOffset == 0 {
			continue
		}
		cpLen := min32(s.rawSize, s.virtualSize)
		if int(s.rawOffset)+int(cpLen) > len(pe) {
			cpLen = uint32(len(pe)) - s.rawOffset
		}
		copy(mapped[s.virtualAddr:], pe[s.rawOffset:s.rawOffset+cpLen])
	}
	trace("local image mapped sections=%d", len(img.sections))

	if err := applyRelocations(mapped, img, remoteBase); err != nil {
		cleanupRemote()
		return 0, fmt.Errorf("relocations: %w", err)
	}
	trace("relocations applied")

	remoteModules, err := snapshotProcessModules(pid)
	if err != nil {
		cleanupRemote()
		return 0, fmt.Errorf("snapshot remote modules: %w", err)
	}
	trace("remote modules snapshotted count=%d", len(remoteModules))

	// Resolve imports against the target process's loaded modules.
	if err := resolveImports(pid, hProc, mapped, img.importRVA, remoteModules); err != nil {
		cleanupRemote()
		return 0, fmt.Errorf("imports: %w", err)
	}
	trace("imports resolved")

	// Find Init export.
	initRVA, err := findExportRVA(mapped, "Init")
	if err != nil {
		cleanupRemote()
		return 0, fmt.Errorf("find Init: %w", err)
	}
	trace("Init export found rva=%#x", initRVA)

	// Write image to target.
	if err := windows.WriteProcessMemory(hProc, remoteBase, &mapped[0], uintptr(len(mapped)), nil); err != nil {
		cleanupRemote()
		return 0, fmt.Errorf("write image: %w", err)
	}
	trace("image written to remote base=%#x bytes=%d", remoteBase, len(mapped))

	// Build APC shellcode: calls Init(remoteBuf) then returns.
	// TEMPORARILY reverted to static stub (pre-R3) to diagnose D2R AV on
	// inject observed 2026-04-15. Polymorphic version kept below for later.
	initAddr := remoteBase + uintptr(initRVA)
	var sc []byte
	sc = append(sc, 0x48, 0x83, 0xEC, 0x28) // sub rsp, 0x28
	sc = append(sc, 0x48, 0xB9)             // mov rcx, imm64
	sc = appendU64(sc, uint64(remoteBuf))
	sc = append(sc, 0x48, 0xB8) // mov rax, imm64
	sc = appendU64(sc, uint64(initAddr))
	sc = append(sc, 0xFF, 0xD0)             // call rax
	sc = append(sc, 0x48, 0x83, 0xC4, 0x28) // add rsp, 0x28
	sc = append(sc, 0xC3)                   // ret
	_ = buildPolymorphicAPCShellcode        // keep ref (silence unused)

	scAddr := remoteBase + uintptr(img.sizeOfImage) - 256
	if err := windows.WriteProcessMemory(hProc, scAddr, &sc[0], uintptr(len(sc)), nil); err != nil {
		cleanupRemote()
		return 0, fmt.Errorf("write shellcode: %w", err)
	}
	trace("shellcode written addr=%#x size=%d initAddr=%#x", scAddr, len(sc), initAddr)

	// Queue APC on a D2R thread (bypasses CreateRemoteThread hooks).
	if err := queueAPC(pid, hProc, scAddr); err != nil {
		cleanupRemote()
		return 0, fmt.Errorf("apc: %w", err)
	}
	trace("APC queued addr=%#x", scAddr)

	// Wait for APC to execute.
	time.Sleep(2 * time.Second)
	trace("post-APC settle wait complete")

	// Erase PE header.
	zeros := make([]byte, 0x200)
	_ = windows.WriteProcessMemory(hProc, remoteBase, &zeros[0], uintptr(len(zeros)), nil)
	trace("PE header erased base=%#x", remoteBase)

	return remoteBase, nil
}

// buildPolymorphicAPCShellcode emits the APC init stub with per-session
// polymorphism. Behaviour is unchanged — load remoteBuf into RCX, call
// initAddr, preserve alignment — but every layout byte differs across
// sessions. Junk blocks come from ntapi.GenerateJunkBlock; stack adjustment
// and scratch-register selection are randomised.
func buildPolymorphicAPCShellcode(remoteBuf, initAddr uintptr) []byte {
	var sc []byte

	// 1. Stack adjust. Randomise between `sub rsp, 0x28` (classic shadow),
	//    `sub rsp, 0x38` (+ extra 0x10 locals), or push-based variants.
	switch ntapi.CryptRandN(4) {
	case 0:
		sc = append(sc, 0x48, 0x83, 0xEC, 0x28) // sub rsp, 0x28
	case 1:
		sc = append(sc, 0x48, 0x83, 0xEC, 0x38) // sub rsp, 0x38 (tighten later)
	case 2:
		// push 0 × 5 — reserves 0x28, different opcode sequence
		sc = append(sc, 0x6A, 0x00) // push 0
		sc = append(sc, 0x6A, 0x00)
		sc = append(sc, 0x6A, 0x00)
		sc = append(sc, 0x6A, 0x00)
		sc = append(sc, 0x6A, 0x00)
	default:
		// lea rsp, [rsp - 0x28]
		sc = append(sc, 0x48, 0x8D, 0x64, 0x24, 0xD8) // lea rsp,[rsp-0x28]
	}
	stackAdj := uint32(0x28)
	if sc[0] == 0x48 && len(sc) == 4 && sc[2] == 0xEC && sc[3] == 0x38 {
		stackAdj = 0x38
	}

	sc = append(sc, ntapi.GenerateJunkBlock()...)

	// 2. Load initAddr first into a scratch register (rax, r10, or r11),
	//    then reload into our final call target. Two hops = more variants
	//    than direct-MOV.
	scratch := []byte{0x48, 0xB8}           // mov rax, imm64 (default)
	reloadFromScratch := []byte{0xFF, 0xD0} // call rax
	switch ntapi.CryptRandN(3) {
	case 1:
		scratch = []byte{0x49, 0xBA}                 // mov r10, imm64
		reloadFromScratch = []byte{0x41, 0xFF, 0xD2} // call r10
	case 2:
		scratch = []byte{0x49, 0xBB}                 // mov r11, imm64
		reloadFromScratch = []byte{0x41, 0xFF, 0xD3} // call r11
	}
	sc = append(sc, scratch...)
	sc = appendU64(sc, uint64(initAddr))

	sc = append(sc, ntapi.GenerateJunkBlock()...)

	// 3. Load remoteBuf into RCX (required by x64 Windows ABI — 1st arg).
	sc = append(sc, 0x48, 0xB9) // mov rcx, imm64
	sc = appendU64(sc, uint64(remoteBuf))

	sc = append(sc, ntapi.GenerateJunkBlock()...)

	// 4. Call target (via scratch register selected above).
	sc = append(sc, reloadFromScratch...)

	sc = append(sc, ntapi.GenerateJunkBlock()...)

	// 5. Stack cleanup matching step 1.
	switch stackAdj {
	case 0x38:
		sc = append(sc, 0x48, 0x83, 0xC4, 0x38) // add rsp, 0x38
	default:
		sc = append(sc, 0x48, 0x83, 0xC4, 0x28) // add rsp, 0x28
	}

	sc = append(sc, 0xC3) // ret
	return sc
}

// queueAPC iterates every D2R thread and queues the init shellcode on each.
// The shellcode target (Init) is idempotent via G_SHM sentinel in rmod — only
// the first APC to actually fire in an alertable wait will do real work; the
// rest short-circuit. Queuing on many threads increases the odds that at least
// one is (or imminently becomes) alertable — a single-thread queue frequently
// never fires because D2R's render thread never enters alertable wait.
func queueAPC(pid uint32, hProc windows.Handle, scAddr uintptr) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snap)

	var te windows.ThreadEntry32
	te.Size = uint32(unsafe.Sizeof(te))
	if err := windows.Thread32First(snap, &te); err != nil {
		return err
	}

	procSuspend := modkernel32.NewProc("SuspendThread")
	procResume := modkernel32.NewProc("ResumeThread")
	procNtQueueAPC := windows.NewLazySystemDLL("ntdll.dll").NewProc("NtQueueApcThread")

	queued := 0
	tried := 0
	for {
		if te.OwnerProcessID == pid {
			tried++
			const threadAccess = 0x0010 | 0x0002 | 0x0008 // SUSPEND_RESUME | SET_CONTEXT
			hTh, openErr := windows.OpenThread(threadAccess, false, te.ThreadID)
			if openErr == nil {
				procSuspend.Call(uintptr(hTh))
				r, _, _ := procNtQueueAPC.Call(uintptr(hTh), scAddr, 0, 0, 0)
				procResume.Call(uintptr(hTh))
				windows.CloseHandle(hTh)
				if r == 0 {
					queued++
				}
			}
		}
		if err := windows.Thread32Next(snap, &te); err != nil {
			break
		}
	}

	if queued == 0 {
		return fmt.Errorf("no target thread (tried %d)", tried)
	}
	return nil
}

// ---------------------------------------------------------------------------
// PE parsing
// ---------------------------------------------------------------------------

type peSection struct {
	virtualAddr uint32
	virtualSize uint32
	rawOffset   uint32
	rawSize     uint32
}

type peImage struct {
	imageBase     uint64
	sizeOfImage   uint32
	sizeOfHeaders uint32
	entryPointRVA uint32
	sections      []peSection
	importRVA     uint32
	relocRVA      uint32
	relocSize     uint32
}

func parsePE(data []byte) (*peImage, error) {
	if len(data) < 0x40 || data[0] != 'M' || data[1] != 'Z' {
		return nil, fmt.Errorf("not a PE file")
	}
	peOff := binary.LittleEndian.Uint32(data[0x3C:])
	if int(peOff)+0x18 > len(data) || string(data[peOff:peOff+4]) != "PE\x00\x00" {
		return nil, fmt.Errorf("invalid PE signature")
	}

	coff := peOff + 4
	numSections := binary.LittleEndian.Uint16(data[coff+2:])
	optSize := binary.LittleEndian.Uint16(data[coff+16:])
	optBase := coff + 20

	if binary.LittleEndian.Uint16(data[optBase:]) != 0x20B {
		return nil, fmt.Errorf("not PE32+")
	}

	img := &peImage{
		entryPointRVA: binary.LittleEndian.Uint32(data[optBase+16:]),
		imageBase:     binary.LittleEndian.Uint64(data[optBase+24:]),
		sizeOfImage:   binary.LittleEndian.Uint32(data[optBase+56:]),
		sizeOfHeaders: binary.LittleEndian.Uint32(data[optBase+60:]),
		importRVA:     binary.LittleEndian.Uint32(data[optBase+112+8:]),
		relocRVA:      binary.LittleEndian.Uint32(data[optBase+112+5*8:]),
		relocSize:     binary.LittleEndian.Uint32(data[optBase+112+5*8+4:]),
	}

	secBase := int(coff) + 20 + int(optSize)
	for i := 0; i < int(numSections); i++ {
		off := secBase + i*40
		if off+40 > len(data) {
			break
		}
		img.sections = append(img.sections, peSection{
			virtualSize: binary.LittleEndian.Uint32(data[off+8:]),
			virtualAddr: binary.LittleEndian.Uint32(data[off+12:]),
			rawSize:     binary.LittleEndian.Uint32(data[off+16:]),
			rawOffset:   binary.LittleEndian.Uint32(data[off+20:]),
		})
	}
	return img, nil
}

func applyRelocations(mapped []byte, img *peImage, remoteBase uintptr) error {
	if img == nil {
		return fmt.Errorf("nil image")
	}
	if uint64(remoteBase) == img.imageBase {
		return nil
	}
	if img.relocRVA == 0 || img.relocSize == 0 {
		// rmod is a no_std x64 Rust DLL and currently emits no .reloc table.
		// The code is RIP-relative; failing here prevents re-attaching to a
		// live D2R after a prior app.exe session occupied the preferred base.
		// Let the image run and rely on Init's ready flag/error code to catch
		// a real loader failure.
		return nil
	}

	delta := int64(remoteBase) - int64(img.imageBase)
	relocEnd := img.relocRVA + img.relocSize
	for blockOff := img.relocRVA; blockOff < relocEnd; {
		if int(blockOff)+8 > len(mapped) {
			return fmt.Errorf("reloc block header out of range")
		}
		pageRVA := binary.LittleEndian.Uint32(mapped[blockOff:])
		blockSize := binary.LittleEndian.Uint32(mapped[blockOff+4:])
		if blockSize < 8 {
			return fmt.Errorf("invalid reloc block size %d", blockSize)
		}
		if blockOff+blockSize > uint32(len(mapped)) {
			return fmt.Errorf("reloc block out of range")
		}
		entryCount := (blockSize - 8) / 2
		entryBase := blockOff + 8
		for i := uint32(0); i < entryCount; i++ {
			entry := binary.LittleEndian.Uint16(mapped[entryBase+i*2:])
			typ := entry >> 12
			offset := entry & 0x0FFF
			targetRVA := pageRVA + uint32(offset)
			switch typ {
			case 0:
				continue
			case 10: // IMAGE_REL_BASED_DIR64
				if int(targetRVA)+8 > len(mapped) {
					return fmt.Errorf("dir64 reloc target out of range")
				}
				value := binary.LittleEndian.Uint64(mapped[targetRVA:])
				binary.LittleEndian.PutUint64(mapped[targetRVA:], uint64(int64(value)+delta))
			case 3: // IMAGE_REL_BASED_HIGHLOW
				if int(targetRVA)+4 > len(mapped) {
					return fmt.Errorf("highlow reloc target out of range")
				}
				value := binary.LittleEndian.Uint32(mapped[targetRVA:])
				binary.LittleEndian.PutUint32(mapped[targetRVA:], uint32(int64(value)+delta))
			default:
				return fmt.Errorf("unsupported relocation type %d", typ)
			}
		}
		blockOff += blockSize
	}
	return nil
}

// ---------------------------------------------------------------------------
// Import resolution
// ---------------------------------------------------------------------------

type processModule struct {
	name string
	base uintptr
	size uint32
}

func snapshotProcessModules(pid uint32) ([]processModule, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPMODULE|windows.TH32CS_SNAPMODULE32, pid)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap)

	var me windows.ModuleEntry32
	me.Size = uint32(unsafe.Sizeof(me))
	if err := windows.Module32First(snap, &me); err != nil {
		return nil, err
	}

	var modules []processModule
	for {
		modules = append(modules, processModule{
			name: strings.ToLower(windows.UTF16ToString(me.Module[:])),
			base: uintptr(me.ModBaseAddr),
			size: me.ModBaseSize,
		})
		if err := windows.Module32Next(snap, &me); err != nil {
			break
		}
	}
	return modules, nil
}

func findModuleByName(modules []processModule, name string) (processModule, bool) {
	target := strings.ToLower(name)
	for _, mod := range modules {
		if mod.name == target {
			return mod, true
		}
	}
	return processModule{}, false
}

func findModuleForAddress(modules []processModule, addr uintptr) (processModule, bool) {
	for _, mod := range modules {
		if addr >= mod.base && addr < mod.base+uintptr(mod.size) {
			return mod, true
		}
	}
	return processModule{}, false
}

func ensureRemoteModuleLoaded(pid uint32, hProc windows.Handle, remoteModules []processModule, dllName string) ([]processModule, error) {
	if _, ok := findModuleByName(remoteModules, dllName); ok {
		return remoteModules, nil
	}
	if err := loadRemoteLibraryViaAPC(pid, hProc, dllName, remoteModules); err != nil {
		return remoteModules, fmt.Errorf("remote module %s not found and LoadLibraryW APC failed: %w", dllName, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mods, err := snapshotProcessModules(pid)
		if err == nil {
			remoteModules = mods
			if _, ok := findModuleByName(remoteModules, dllName); ok {
				return remoteModules, nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return remoteModules, fmt.Errorf("remote module %s not found after LoadLibraryW APC", dllName)
}

func loadRemoteLibraryViaAPC(pid uint32, hProc windows.Handle, dllName string, remoteModules []processModule) error {
	kernel32 := modkernel32
	loadLibraryW := kernel32.NewProc("LoadLibraryW")
	if err := loadLibraryW.Find(); err != nil {
		return fmt.Errorf("resolve local LoadLibraryW: %w", err)
	}
	localAddr := loadLibraryW.Addr()
	localPID := windows.GetCurrentProcessId()
	localModules, err := snapshotProcessModules(localPID)
	if err != nil {
		return fmt.Errorf("snapshot local modules: %w", err)
	}
	localOwner, ok := findModuleForAddress(localModules, localAddr)
	if !ok {
		localOwner, ok = findModuleByName(localModules, "kernel32.dll")
		if !ok {
			return fmt.Errorf("local LoadLibraryW owner not found")
		}
	}
	remoteOwner, ok := findModuleByName(remoteModules, localOwner.name)
	if !ok {
		return fmt.Errorf("remote LoadLibraryW owner %s not found", localOwner.name)
	}
	remoteLoadLibraryW := remoteOwner.base + (localAddr - localOwner.base)

	wide, err := windows.UTF16FromString(dllName)
	if err != nil {
		return fmt.Errorf("utf16 dll name: %w", err)
	}
	nameBytes := unsafe.Slice((*byte)(unsafe.Pointer(&wide[0])), len(wide)*2)
	nameSize := uintptr(len(nameBytes))
	sc := make([]byte, 0, 64)
	sc = append(sc, 0x48, 0x83, 0xEC, 0x28) // sub rsp, 0x28
	sc = append(sc, 0x48, 0xB9)             // mov rcx, imm64
	sc = appendU64(sc, 0)                   // patched with remote string address
	sc = append(sc, 0x48, 0xB8)             // mov rax, imm64
	sc = appendU64(sc, uint64(remoteLoadLibraryW))
	sc = append(sc, 0xFF, 0xD0)             // call rax
	sc = append(sc, 0x48, 0x83, 0xC4, 0x28) // add rsp, 0x28
	sc = append(sc, 0xC3)                   // ret

	total := uintptr(len(sc)) + nameSize
	remoteBlock, err := virtualAllocEx(hProc, 0, total, windows.MEM_COMMIT|windows.MEM_RESERVE, windows.PAGE_EXECUTE_READWRITE)
	if err != nil {
		return fmt.Errorf("alloc remote LoadLibraryW stub: %w", err)
	}
	remoteName := remoteBlock + uintptr(len(sc))
	binary.LittleEndian.PutUint64(sc[6:], uint64(remoteName))
	if err := windows.WriteProcessMemory(hProc, remoteBlock, &sc[0], uintptr(len(sc)), nil); err != nil {
		return fmt.Errorf("write remote LoadLibraryW stub: %w", err)
	}
	if err := windows.WriteProcessMemory(hProc, remoteName, &nameBytes[0], nameSize, nil); err != nil {
		return fmt.Errorf("write remote LoadLibraryW name: %w", err)
	}
	if err := queueAPC(pid, hProc, remoteBlock); err != nil {
		return fmt.Errorf("queue LoadLibraryW APC: %w", err)
	}
	return nil
}

func resolveImports(pid uint32, hProc windows.Handle, mapped []byte, importRVA uint32, remoteModules []processModule) error {
	if importRVA == 0 {
		return nil
	}
	localPID := windows.GetCurrentProcessId()
	localModules, err := snapshotProcessModules(localPID)
	if err != nil {
		return fmt.Errorf("snapshot local modules: %w", err)
	}
	for off := importRVA; ; off += 20 {
		if int(off)+20 > len(mapped) {
			break
		}
		iltRVA := binary.LittleEndian.Uint32(mapped[off:])
		nameRVA := binary.LittleEndian.Uint32(mapped[off+12:])
		iatRVA := binary.LittleEndian.Uint32(mapped[off+16:])
		if nameRVA == 0 {
			break
		}
		dllName := readCString(mapped, nameRVA)
		hMod, err := windows.LoadLibrary(dllName)
		if err != nil {
			return fmt.Errorf("load %s: %w", dllName, err)
		}
		defer windows.FreeLibrary(hMod)
		importLocalMod, ok := findModuleByName(localModules, dllName)
		if !ok {
			importLocalMod = processModule{
				name: strings.ToLower(dllName),
				base: uintptr(hMod),
			}
		}
		thunkRVA := iltRVA
		if thunkRVA == 0 {
			thunkRVA = iatRVA
		}
		for i := uint32(0); ; i++ {
			tOff := thunkRVA + i*8
			iOff := iatRVA + i*8
			if int(tOff)+8 > len(mapped) {
				break
			}
			tv := binary.LittleEndian.Uint64(mapped[tOff:])
			if tv == 0 {
				break
			}
			var procAddr uintptr
			if tv&(1<<63) != 0 {
				ordinal := uint32(tv & 0xFFFF)
				procAddr, _, _ = procGetProcAddress.Call(uintptr(hMod), uintptr(ordinal))
			} else {
				funcName := readCString(mapped, uint32(tv)+2)
				procAddr, err = windows.GetProcAddress(windows.Handle(hMod), funcName)
				if err != nil {
					return fmt.Errorf("resolve %s!%s: %w", dllName, funcName, err)
				}
			}
			if procAddr == 0 {
				return fmt.Errorf("resolve %s import at thunk %d returned 0", dllName, i)
			}
			localOwner, ok := findModuleForAddress(localModules, procAddr)
			if !ok {
				localOwner = importLocalMod
			}
			remoteOwner, ok := findModuleByName(remoteModules, localOwner.name)
			if !ok {
				var loadErr error
				remoteModules, loadErr = ensureRemoteModuleLoaded(pid, hProc, remoteModules, localOwner.name)
				if loadErr != nil {
					return loadErr
				}
				remoteOwner, ok = findModuleByName(remoteModules, localOwner.name)
				if !ok {
					return fmt.Errorf("remote owner module %s not found after load", localOwner.name)
				}
			}
			if localOwner.base == 0 {
				return fmt.Errorf("local owner module %s has zero base", localOwner.name)
			}
			remoteProcAddr := remoteOwner.base + (procAddr - localOwner.base)
			if int(iOff)+8 <= len(mapped) {
				binary.LittleEndian.PutUint64(mapped[iOff:], uint64(remoteProcAddr))
			}
		}
	}
	return nil
}

func findExportRVA(mapped []byte, name string) (uint32, error) {
	if len(mapped) < 0x40 {
		return 0, fmt.Errorf("image too small")
	}
	peOff := binary.LittleEndian.Uint32(mapped[0x3C:])
	ddBase := peOff + 4 + 20 + 112
	exportRVA := binary.LittleEndian.Uint32(mapped[ddBase:])
	if exportRVA == 0 {
		return 0, fmt.Errorf("no export directory")
	}
	numNames := binary.LittleEndian.Uint32(mapped[exportRVA+24:])
	addrTbl := binary.LittleEndian.Uint32(mapped[exportRVA+28:])
	nameTbl := binary.LittleEndian.Uint32(mapped[exportRVA+32:])
	ordTbl := binary.LittleEndian.Uint32(mapped[exportRVA+36:])
	for i := uint32(0); i < numNames; i++ {
		nRVA := binary.LittleEndian.Uint32(mapped[nameTbl+i*4:])
		if readCString(mapped, nRVA) == name {
			ord := binary.LittleEndian.Uint16(mapped[ordTbl+i*2:])
			return binary.LittleEndian.Uint32(mapped[addrTbl+uint32(ord)*4:]), nil
		}
	}
	return 0, fmt.Errorf("export %q not found", name)
}

func readCString(data []byte, rva uint32) string {
	if int(rva) >= len(data) {
		return ""
	}
	end := rva
	for int(end) < len(data) && data[end] != 0 {
		end++
	}
	return string(data[rva:end])
}

var procGetProcAddress = modkernel32.NewProc("GetProcAddress")

func appendU64(buf []byte, val uint64) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, val)
	return append(buf, b...)
}

func virtualAllocEx(hProcess windows.Handle, addr, size uintptr, allocType, protect uint32) (uintptr, error) {
	r0, _, e1 := modkernel32.NewProc("VirtualAllocEx").Call(
		uintptr(hProcess), addr, size, uintptr(allocType), uintptr(protect))
	if r0 == 0 {
		return 0, e1
	}
	return r0, nil
}

func virtualFreeEx(hProcess windows.Handle, addr, size uintptr, freeType uint32) error {
	r0, _, e1 := modkernel32.NewProc("VirtualFreeEx").Call(
		uintptr(hProcess), addr, size, uintptr(freeType))
	if r0 == 0 {
		return e1
	}
	return nil
}

func min32(a, b uint32) uint32 {
	if a < b {
		return a
	}
	return b
}
