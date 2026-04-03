package presenter

import (
	"encoding/binary"
	"fmt"
	"os"
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
func loadModule(pid uint32, modulePath string, remoteBuf uintptr) error {
	pe, err := os.ReadFile(modulePath)
	if err != nil {
		return fmt.Errorf("read dll: %w", err)
	}

	img, err := parsePE(pe)
	if err != nil {
		return fmt.Errorf("parse pe: %w", err)
	}

	const processAccess = windows.PROCESS_CREATE_THREAD |
		windows.PROCESS_VM_OPERATION |
		windows.PROCESS_VM_READ |
		windows.PROCESS_VM_WRITE |
		windows.PROCESS_QUERY_INFORMATION

	hProc, err := ntapi.OpenProcess(processAccess, pid)
	if err != nil {
		return fmt.Errorf("open target: %w", err)
	}
	defer ntapi.CloseHandle(hProc)

	// Allocate RWX in target for the DLL image.
	remoteBase, err := virtualAllocEx(hProc, 0, uintptr(img.sizeOfImage),
		windows.MEM_COMMIT|windows.MEM_RESERVE, windows.PAGE_EXECUTE_READWRITE)
	if err != nil {
		return fmt.Errorf("alloc image: %w", err)
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

	// Resolve imports (kernel32/user32 — same base in all processes).
	if err := resolveImports(mapped, img.importRVA); err != nil {
		return fmt.Errorf("imports: %w", err)
	}

	// Find Init export.
	initRVA, err := findExportRVA(mapped, "Init")
	if err != nil {
		return fmt.Errorf("find Init: %w", err)
	}

	// Write image to target.
	if err := windows.WriteProcessMemory(hProc, remoteBase, &mapped[0], uintptr(len(mapped)), nil); err != nil {
		return fmt.Errorf("write image: %w", err)
	}

	// Build APC shellcode: calls Init(remoteBuf) then returns.
	initAddr := remoteBase + uintptr(initRVA)
	var sc []byte
	sc = append(sc, 0x48, 0x83, 0xEC, 0x28)       // sub rsp, 0x28
	sc = append(sc, 0x48, 0xB9)                     // mov rcx, remoteBuf
	sc = appendU64(sc, uint64(remoteBuf))
	sc = append(sc, 0x48, 0xB8)                     // mov rax, initAddr
	sc = appendU64(sc, uint64(initAddr))
	sc = append(sc, 0xFF, 0xD0)                     // call rax
	sc = append(sc, 0x48, 0x83, 0xC4, 0x28)         // add rsp, 0x28
	sc = append(sc, 0xC3)                           // ret

	scAddr := remoteBase + uintptr(img.sizeOfImage) - 256
	if err := windows.WriteProcessMemory(hProc, scAddr, &sc[0], uintptr(len(sc)), nil); err != nil {
		return fmt.Errorf("write shellcode: %w", err)
	}

	// Queue APC on a D2R thread (bypasses CreateRemoteThread hooks).
	if err := queueAPC(pid, hProc, scAddr); err != nil {
		return fmt.Errorf("apc: %w", err)
	}

	// Wait for APC to execute.
	time.Sleep(2 * time.Second)

	// Erase PE header.
	zeros := make([]byte, 0x200)
	_ = windows.WriteProcessMemory(hProc, remoteBase, &zeros[0], uintptr(len(zeros)), nil)

	return nil
}

// queueAPC finds a D2R thread, suspends it, queues APC, resumes.
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

	for {
		if te.OwnerProcessID == pid {
			const threadAccess = 0x0010 | 0x0002 | 0x0008 // SUSPEND_RESUME | SET_CONTEXT
			hTh, err := windows.OpenThread(threadAccess, false, te.ThreadID)
			if err == nil {
				procSuspend.Call(uintptr(hTh))
				r, _, _ := procNtQueueAPC.Call(uintptr(hTh), scAddr, 0, 0, 0)
				procResume.Call(uintptr(hTh))
				windows.CloseHandle(hTh)
				if r == 0 {
					return nil // APC queued successfully
				}
			}
		}
		if err := windows.Thread32Next(snap, &te); err != nil {
			break
		}
	}

	return fmt.Errorf("no suitable D2R thread found for APC")
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

// ---------------------------------------------------------------------------
// Import resolution
// ---------------------------------------------------------------------------

func resolveImports(mapped []byte, importRVA uint32) error {
	if importRVA == 0 {
		return nil
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
			if int(iOff)+8 <= len(mapped) {
				binary.LittleEndian.PutUint64(mapped[iOff:], uint64(procAddr))
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

func min32(a, b uint32) uint32 {
	if a < b {
		return a
	}
	return b
}
