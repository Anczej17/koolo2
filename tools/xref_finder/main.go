// Command xref_finder scans D2R.exe .text for all `CALL rel32` instructions
// whose target resolves to a given RVA. Used to find the real callers of
// send_fn (0x146600) — memory project_hwbp_sniffer.md note #1 says send_fn
// might be "vestigial" and game code uses inlined copies; verifying the
// caller set proves or disproves that theory.
//
// A CALL rel32 on x86-64 is encoded as `E8 dd dd dd dd`, where the 32-bit
// signed displacement is relative to the END of the instruction. So the
// target RVA of a CALL at site S is: S + 5 + disp32.
//
// Usage:
//
//	xref_finder                          # default: target 0x146600 (send_fn)
//	xref_finder -target 0x146600         # explicit target
//	xref_finder -target 0x146600 -json out.json
//	xref_finder -context 16              # print N bytes of context around each hit
package main

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

type Hit struct {
	SiteRVA int64  `json:"site_rva"`
	SiteVA  uint64 `json:"site_va"`
	Disp32  int32  `json:"disp32"`
	Before  string `json:"before_hex,omitempty"`
	After   string `json:"after_hex,omitempty"`
}

func main() {
	var (
		targetRVA int64
		jsonPath  string
		context   int
	)
	flag.Int64Var(&targetRVA, "target", 0x146600, "target RVA — find every CALL whose destination == this")
	flag.StringVar(&jsonPath, "json", "", "write JSON results to this path")
	flag.IntVar(&context, "context", 16, "bytes of code context to print around each call site")
	flag.Parse()

	pid, base, _, err := findD2R()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	h, err := windows.OpenProcess(windows.PROCESS_VM_READ|windows.PROCESS_QUERY_INFORMATION, false, pid)
	if err != nil {
		fmt.Fprintln(os.Stderr, "OpenProcess:", err)
		os.Exit(2)
	}
	defer windows.CloseHandle(h)

	buf, textVA, err := readTextSection(h, base)
	if err != nil {
		fmt.Fprintln(os.Stderr, "readTextSection:", err)
		os.Exit(2)
	}

	fmt.Printf("D2R pid=%d base=0x%X .text size=%d (readable bytes in scan buffer)\n",
		pid, base, len(buf))
	fmt.Printf("Scanning for CALL rel32 → RVA 0x%X (VA 0x%X)...\n\n",
		targetRVA, uint64(base)+uint64(targetRVA))

	var hits []Hit
	for i := 0; i+5 <= len(buf); i++ {
		if buf[i] != 0xE8 { // CALL rel32
			continue
		}
		disp := int32(binary.LittleEndian.Uint32(buf[i+1 : i+5]))
		// RVA of the instruction after the CALL = textVA + i + 5
		afterCall := int64(textVA) + int64(i) + 5
		target := afterCall + int64(disp)
		if target != targetRVA {
			continue
		}
		siteRVA := int64(textVA) + int64(i)
		hit := Hit{
			SiteRVA: siteRVA,
			SiteVA:  uint64(base) + uint64(siteRVA),
			Disp32:  disp,
		}
		if context > 0 {
			lo := i - context
			if lo < 0 {
				lo = 0
			}
			hi := i + 5 + context
			if hi > len(buf) {
				hi = len(buf)
			}
			hit.Before = hex.EncodeToString(buf[lo:i])
			hit.After = hex.EncodeToString(buf[i+5 : hi])
		}
		hits = append(hits, hit)
	}

	fmt.Printf("Found %d call sites\n", len(hits))
	fmt.Println(strings.Repeat("─", 70))
	for _, h := range hits {
		fmt.Printf("  RVA=0x%-8X VA=0x%X  disp32=%+d\n", h.SiteRVA, h.SiteVA, h.Disp32)
		if context > 0 {
			fmt.Printf("    pre:  %s\n", h.Before)
			fmt.Printf("    CALL: e8 %08x\n", uint32(h.Disp32))
			fmt.Printf("    post: %s\n", h.After)
		}
	}

	if jsonPath != "" {
		b, _ := json.MarshalIndent(map[string]any{
			"pid":        pid,
			"base":       fmt.Sprintf("0x%X", base),
			"target_rva": fmt.Sprintf("0x%X", targetRVA),
			"text_va":    fmt.Sprintf("0x%X", textVA),
			"scan_bytes": len(buf),
			"hits":       hits,
		}, "", "  ")
		_ = os.WriteFile(jsonPath, b, 0644)
		fmt.Printf("\nWrote %s\n", jsonPath)
	}
}

// ---------------------------------------------------------------------------
// Process attach + .text read (shares logic with tools/offset_resolver).
// ---------------------------------------------------------------------------

func findD2R() (pid uint32, base uintptr, size uint32, err error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("snapshot processes: %w", err)
	}
	defer windows.CloseHandle(snap)
	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	var target uint32
	for err := windows.Process32First(snap, &pe); err == nil; err = windows.Process32Next(snap, &pe) {
		name := windows.UTF16ToString(pe.ExeFile[:])
		if strings.EqualFold(name, "D2R.exe") {
			target = pe.ProcessID
			break
		}
	}
	if target == 0 {
		return 0, 0, 0, fmt.Errorf("D2R.exe not running")
	}
	msnap, err := windows.CreateToolhelp32Snapshot(
		windows.TH32CS_SNAPMODULE|windows.TH32CS_SNAPMODULE32, target)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("snapshot modules: %w", err)
	}
	defer windows.CloseHandle(msnap)
	var me windows.ModuleEntry32
	me.Size = uint32(unsafe.Sizeof(me))
	for err := windows.Module32First(msnap, &me); err == nil; err = windows.Module32Next(msnap, &me) {
		name := windows.UTF16ToString(me.Module[:])
		if strings.EqualFold(name, "D2R.exe") {
			return target, uintptr(me.ModBaseAddr), me.ModBaseSize, nil
		}
	}
	return 0, 0, 0, fmt.Errorf("D2R.exe module not found")
}

// readTextSection walks PE headers to locate .text and reads every readable
// committed page inside it via VirtualQueryEx. Returns the buffer, the .text
// RVA (so caller can convert offsets → RVAs), and error.
func readTextSection(proc windows.Handle, base uintptr) ([]byte, uint32, error) {
	hdr, err := readRange(proc, base, 0x400)
	if err != nil {
		return nil, 0, fmt.Errorf("read PE header: %w", err)
	}
	if hdr[0] != 'M' || hdr[1] != 'Z' {
		return nil, 0, fmt.Errorf("not a PE image")
	}
	peOff := binary.LittleEndian.Uint32(hdr[0x3C:0x40])
	numSections := binary.LittleEndian.Uint16(hdr[peOff+6 : peOff+8])
	optHeaderSize := binary.LittleEndian.Uint16(hdr[peOff+20 : peOff+22])
	sectionTableOff := peOff + 24 + uint32(optHeaderSize)
	if int(sectionTableOff)+40*int(numSections) > len(hdr) {
		more, err := readRange(proc, base, uint32(sectionTableOff)+40*uint32(numSections)+64)
		if err != nil {
			return nil, 0, fmt.Errorf("read section table: %w", err)
		}
		hdr = more
	}
	var textVA, textSize uint32
	for i := 0; i < int(numSections); i++ {
		off := int(sectionTableOff) + 40*i
		name := strings.TrimRight(string(hdr[off:off+8]), "\x00")
		if name == ".text" {
			textVA = binary.LittleEndian.Uint32(hdr[off+12 : off+16])
			textSize = binary.LittleEndian.Uint32(hdr[off+16 : off+20])
			break
		}
	}
	if textSize == 0 {
		return nil, 0, fmt.Errorf(".text not found")
	}
	buf, err := readRange(proc, base+uintptr(textVA), textSize)
	if err != nil {
		return nil, 0, err
	}
	return buf, textVA, nil
}

// readRange uses VirtualQueryEx to enumerate committed regions inside the
// requested range and reads each one individually. Arxan-encrypted pages
// inside D2R.exe fail ReadProcessMemory — we zero-fill them and keep going.
func readRange(proc windows.Handle, addr uintptr, size uint32) ([]byte, error) {
	out := make([]byte, size)
	end := addr + uintptr(size)
	var mbi windows.MemoryBasicInformation
	cur := addr
	for cur < end {
		err := windows.VirtualQueryEx(proc, cur, &mbi, unsafe.Sizeof(mbi))
		if err != nil {
			cur += 0x1000
			continue
		}
		regBase := uintptr(mbi.BaseAddress)
		regEnd := regBase + uintptr(mbi.RegionSize)
		next := regEnd
		if next <= cur {
			next = cur + 0x1000
		}
		if mbi.State != windows.MEM_COMMIT ||
			mbi.Protect&windows.PAGE_NOACCESS != 0 ||
			mbi.Protect&windows.PAGE_GUARD != 0 {
			cur = next
			continue
		}
		readStart := cur
		if readStart < regBase {
			readStart = regBase
		}
		readEnd := regEnd
		if readEnd > end {
			readEnd = end
		}
		readLen := uint32(readEnd - readStart)
		if readLen == 0 {
			cur = next
			continue
		}
		offInOut := uint32(readStart - addr)
		var got uintptr
		err = windows.ReadProcessMemory(proc, readStart, &out[offInOut], uintptr(readLen), &got)
		_ = err
		cur = next
	}
	return out, nil
}
