// Command offset_resolver scans a running D2R.exe process for known AOB
// signatures and outputs every resolved RVA needed by d2go / Koolo. It
// eliminates the manual reverse-engineering step that every D2R patch
// otherwise requires.
//
// Usage:
//
//	offset_resolver                 # pretty print summary to stdout
//	offset_resolver -json out.json  # also write machine-readable JSON
//	offset_resolver -go out.go      # also write a Go file with the struct literal
//	offset_resolver -only UI,FPS    # scan only the listed patterns
//	offset_resolver -verbose        # log each scan step
//
// Pattern database lives in patterns.go (ported from
// PatternRecognition_DMA/scanner.py). To add new patterns, append them there.
package main

import (
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ScanResult is the full diagnostic record for one offset. Modeled after
// scanner.py:ScanResult to ease future diff work.
type ScanResult struct {
	Name         string   `json:"name"`
	Status       string   `json:"status"` // "ok" | "miss" | "fail" | "skip"
	RVA          int64    `json:"rva,omitempty"`
	Method       string   `json:"method,omitempty"` // "aob-primary", "aob-alt-1", "delta", "alias"
	Detail       string   `json:"detail,omitempty"`
	MatchCount   int      `json:"match_count,omitempty"`
	MatchOffsets []int64  `json:"match_offsets,omitempty"`
	Log          []string `json:"log,omitempty"`
}

func (r *ScanResult) Logf(format string, args ...any) {
	r.Log = append(r.Log, fmt.Sprintf(format, args...))
}

type resolver struct {
	proc      windows.Handle
	pid       uint32
	base      uintptr
	size      uint32
	textBytes []byte // copy of D2R .text section
	textVA    uint32 // RVA of .text inside the module (base + textVA = textBytes[0])
	results   map[string]*ScanResult
	verbose   bool
}

func main() {
	var (
		onlyFlag    string
		jsonPath    string
		goPath      string
		filePath    string
		dumpTextPath string
		verboseFlag bool
	)
	flag.StringVar(&onlyFlag, "only", "", "comma-separated pattern names to scan (default: all)")
	flag.StringVar(&jsonPath, "json", "", "write JSON results to this path")
	flag.StringVar(&goPath, "go", "", "write a Go snippet with the resolved Offset struct to this path")
	flag.StringVar(&filePath, "file", "", "scan D2R.exe from disk instead of attaching to running process (offline mode for CI/buildbox)")
	flag.StringVar(&dumpTextPath, "dump-text", "", "after attaching, write raw .text section bytes to this file (skip pattern scan if no other output requested) — for offline pattern analysis")
	flag.BoolVar(&verboseFlag, "verbose", false, "log each scan step to stderr")
	flag.Parse()

	var only map[string]bool
	if onlyFlag != "" {
		only = map[string]bool{}
		for _, s := range strings.Split(onlyFlag, ",") {
			only[strings.TrimSpace(s)] = true
		}
	}

	var r *resolver
	var err error
	if filePath != "" {
		r, err = newResolverFromFile(filePath, verboseFlag)
	} else {
		r, err = newResolver(verboseFlag)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: %v\n", err)
		os.Exit(2)
	}
	if r.proc != 0 {
		defer windows.CloseHandle(r.proc)
	}

	if filePath != "" {
		fmt.Printf("D2R offline: file=%s base=0x%X size=0x%X textBytesRead=%d\n",
			filePath, r.base, r.size, len(r.textBytes))
	} else {
		fmt.Printf("D2R attached: pid=%d base=0x%X size=0x%X textBytesRead=%d\n",
			r.pid, r.base, r.size, len(r.textBytes))
	}

	// Dump raw .text bytes early so a failed scan still leaves the dump.
	// textVA is the RVA of the dumped bytes inside D2R.exe (offset 0 in the
	// dump file = D2R.base + textVA). Useful for offline pattern analysis.
	if dumpTextPath != "" {
		header := fmt.Sprintf("# .text dump for D2R build base=0x%X size=0x%X textVA=0x%X textLen=%d\n",
			r.base, r.size, r.textVA, len(r.textBytes))
		if err := os.WriteFile(dumpTextPath+".meta", []byte(header), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "WARN: write dump meta failed: %v\n", err)
		}
		if err := os.WriteFile(dumpTextPath, r.textBytes, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "FATAL: write dump failed: %v\n", err)
			os.Exit(2)
		}
		fmt.Printf("Wrote %s (%d bytes) + %s.meta\n", dumpTextPath, len(r.textBytes), dumpTextPath)
		// If only -dump-text was requested, skip the scan to avoid noise.
		if jsonPath == "" && goPath == "" && onlyFlag == "" {
			return
		}
	}

	names := make([]string, 0, len(Patterns))
	for name := range Patterns {
		if only != nil && !only[name] {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)

	t0 := time.Now()
	for _, name := range names {
		r.scan(name)
	}
	// Aliases & deltas after all primary scans complete.
	for alias, source := range Aliases {
		if only != nil && !only[alias] {
			continue
		}
		r.resolveAlias(alias, source)
	}
	for name, d := range Deltas {
		if only != nil && !only[name] {
			continue
		}
		r.resolveDelta(name, d.Source, d.Delta)
	}

	elapsed := time.Since(t0)
	r.printSummary(elapsed)

	if jsonPath != "" {
		if err := r.writeJSON(jsonPath); err != nil {
			fmt.Fprintf(os.Stderr, "write json: %v\n", err)
		} else {
			fmt.Printf("\nWrote %s\n", jsonPath)
		}
	}
	if goPath != "" {
		if err := r.writeGoSnippet(goPath); err != nil {
			fmt.Fprintf(os.Stderr, "write go snippet: %v\n", err)
		} else {
			fmt.Printf("Wrote %s\n", goPath)
		}
	}

	// Exit non-zero if any d2go-required offset missed.
	for _, name := range names {
		if meta, ok := OffsetMeta[name]; ok && meta.Category == "d2go" {
			if res := r.results[name]; res == nil || res.Status != "ok" {
				os.Exit(1)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Process attach + module read
// ---------------------------------------------------------------------------

func newResolver(verbose bool) (*resolver, error) {
	pid, base, size, err := findD2R()
	if err != nil {
		return nil, err
	}
	const access = windows.PROCESS_QUERY_INFORMATION | windows.PROCESS_VM_READ
	h, err := windows.OpenProcess(access, false, pid)
	if err != nil {
		return nil, fmt.Errorf("OpenProcess pid=%d: %w", pid, err)
	}
	r := &resolver{
		proc:    h,
		pid:     pid,
		base:    base,
		size:    size,
		results: map[string]*ScanResult{},
		verbose: verbose,
	}
	if err := r.readTextSection(); err != nil {
		windows.CloseHandle(h)
		return nil, fmt.Errorf("read .text: %w", err)
	}
	return r, nil
}

// newResolverFromFile builds a resolver that reads D2R.exe's .text section
// directly from disk (no running process needed).
//
// IMPORTANT LIMITATION: Arxan packs/encrypts D2R's runtime .text — the bytes
// on disk are the Arxan unpacker + encrypted code blob, NOT the game's
// real instructions. AOB patterns in patterns.go target the unpacked
// runtime bytes; they will NOT match against the on-disk PE.
//
// This file-mode entry point is therefore only useful when scanning a
// previously-captured RUNTIME memory dump of D2R (e.g. a Process Hacker
// minidump, dumped after the game finished initialising). For a truly raw
// D2R.exe from disk all patterns will MISS — use the attached-process mode
// instead (the default, no -file flag).
func newResolverFromFile(path string, verbose bool) (*resolver, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("open D2R.exe at %s: %w", path, err)
	}
	if len(data) < 0x400 || data[0] != 'M' || data[1] != 'Z' {
		return nil, fmt.Errorf("not a valid PE image at %s", path)
	}
	peOff := binary.LittleEndian.Uint32(data[0x3C:0x40])
	if int(peOff)+24 > len(data) {
		return nil, fmt.Errorf("PE offset out of file")
	}
	numSections := binary.LittleEndian.Uint16(data[peOff+6 : peOff+8])
	optHeaderSize := binary.LittleEndian.Uint16(data[peOff+20 : peOff+22])
	// OptionalHeader for PE32+ has ImageBase at +24 (u64) and SizeOfImage at +56.
	optHeaderOff := peOff + 24
	if int(optHeaderOff)+int(optHeaderSize) > len(data) {
		return nil, fmt.Errorf("optional header out of file")
	}
	imageBase := binary.LittleEndian.Uint64(data[optHeaderOff+24 : optHeaderOff+32])
	sizeOfImage := binary.LittleEndian.Uint32(data[optHeaderOff+56 : optHeaderOff+60])

	sectionTableOff := peOff + 24 + uint32(optHeaderSize)
	if int(sectionTableOff)+40*int(numSections) > len(data) {
		return nil, fmt.Errorf("section table out of file")
	}

	var textVA, textRawPtr, textRawSize uint32
	for i := 0; i < int(numSections); i++ {
		off := int(sectionTableOff) + 40*i
		name := string(data[off : off+8])
		name = strings.TrimRight(name, "\x00")
		if name == ".text" {
			textVA = binary.LittleEndian.Uint32(data[off+12 : off+16])
			textRawSize = binary.LittleEndian.Uint32(data[off+16 : off+20])
			textRawPtr = binary.LittleEndian.Uint32(data[off+20 : off+24])
			break
		}
	}
	if textRawSize == 0 {
		return nil, fmt.Errorf(".text section not found in %s", path)
	}
	if int(textRawPtr)+int(textRawSize) > len(data) {
		return nil, fmt.Errorf(".text raw bytes out of file: ptr=0x%X size=0x%X file=%d", textRawPtr, textRawSize, len(data))
	}

	textBytes := make([]byte, textRawSize)
	copy(textBytes, data[textRawPtr:textRawPtr+textRawSize])

	return &resolver{
		proc:      0, // file mode — no handle
		pid:       0, // unused
		base:      uintptr(imageBase),
		size:      sizeOfImage,
		textBytes: textBytes,
		textVA:    textVA,
		results:   map[string]*ScanResult{},
		verbose:   verbose,
	}, nil
}

// findD2R enumerates processes until it finds one named "D2R.exe" and returns
// its main module's base + size.
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
	return 0, 0, 0, fmt.Errorf("D2R.exe module not found in pid %d", target)
}

// readTextSection reads the full .text section of D2R.exe into memory.
// For simplicity we read the full module image and scan the entire thing —
// the AOB patterns target code-only bytes and won't match inside .rdata/.data
// by accident.
func (r *resolver) readTextSection() error {
	// Read PE headers first to locate .text — at 0x3C we get PE offset.
	hdr, err := r.readAt(r.base, 0x400)
	if err != nil {
		return fmt.Errorf("read PE headers: %w", err)
	}
	if hdr[0] != 'M' || hdr[1] != 'Z' {
		return fmt.Errorf("not a valid PE image at base 0x%X", r.base)
	}
	peOff := binary.LittleEndian.Uint32(hdr[0x3C:0x40])
	if int(peOff)+24 > len(hdr) {
		return fmt.Errorf("PE offset out of header read")
	}
	// At PE+0, signature "PE\0\0"; PE+4 is FileHeader.
	numSections := binary.LittleEndian.Uint16(hdr[peOff+6 : peOff+8])
	optHeaderSize := binary.LittleEndian.Uint16(hdr[peOff+20 : peOff+22])
	sectionTableOff := peOff + 24 + uint32(optHeaderSize)

	// Re-read enough to cover the section table.
	if int(sectionTableOff)+40*int(numSections) > len(hdr) {
		more, err := r.readAt(r.base, uint32(sectionTableOff)+40*uint32(numSections)+64)
		if err != nil {
			return fmt.Errorf("read section table: %w", err)
		}
		hdr = more
	}

	var textVA, textSize uint32
	for i := 0; i < int(numSections); i++ {
		off := int(sectionTableOff) + 40*i
		name := string(hdr[off : off+8])
		name = strings.TrimRight(name, "\x00")
		// virtualSize := binary.LittleEndian.Uint32(hdr[off+8:off+12])
		virtualAddr := binary.LittleEndian.Uint32(hdr[off+12 : off+16])
		sizeOfRawData := binary.LittleEndian.Uint32(hdr[off+16 : off+20])
		if name == ".text" {
			textVA = virtualAddr
			textSize = sizeOfRawData
			break
		}
	}
	if textSize == 0 {
		return fmt.Errorf(".text section not found in D2R.exe")
	}

	textAddr := r.base + uintptr(textVA)
	buf, err := r.readAt(textAddr, textSize)
	if err != nil {
		return fmt.Errorf("read .text bytes: %w", err)
	}
	// Store as RVA-indexed buffer: offset inside the buffer equals (RVA - textVA).
	// For simplicity we store the actual bytes starting at textVA. Scanners must
	// translate match offsets back to RVAs by adding textVA.
	r.textBytes = buf
	// Stash textVA on resolver so scan() can convert offsets → RVAs.
	r.textVA = textVA
	return nil
}

// readAt copies `size` bytes starting at `addr`. Uses VirtualQueryEx to walk
// the committed memory regions inside [addr, addr+size) and reads each region
// individually, because Arxan leaves large swaths of D2R's .text section with
// PAGE_NOACCESS or otherwise unreadable flags between the few decrypted code
// pages. A single monolithic ReadProcessMemory would bail at the first bad
// page; chunk-at-a-time RPM still wastes calls on pages that aren't committed.
// VirtualQueryEx → read-each-committed-region is the fastest clean scheme.
func (r *resolver) readAt(addr uintptr, size uint32) ([]byte, error) {
	out := make([]byte, size)
	end := addr + uintptr(size)
	var mbi windows.MemoryBasicInformation
	var totalRead, regions uint32
	cur := addr
	for cur < end {
		err := windows.VirtualQueryEx(r.proc, cur, &mbi, unsafe.Sizeof(mbi))
		if err != nil {
			// Query failed — skip a page and keep going.
			cur += 0x1000
			continue
		}
		regBase := uintptr(mbi.BaseAddress)
		regEnd := regBase + uintptr(mbi.RegionSize)
		next := regEnd
		if next <= cur {
			next = cur + 0x1000
		}
		if mbi.State != windows.MEM_COMMIT {
			cur = next
			continue
		}
		// Skip guard / no-access regions — ReadProcessMemory will fail anyway
		// and the verbose output gets noisy. PAGE_NOACCESS=0x01, PAGE_GUARD=0x100.
		if mbi.Protect&windows.PAGE_NOACCESS != 0 || mbi.Protect&windows.PAGE_GUARD != 0 {
			cur = next
			continue
		}
		// Clamp region to [addr, end).
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
		err = windows.ReadProcessMemory(r.proc, readStart, &out[offInOut], uintptr(readLen), &got)
		if err == nil && got > 0 {
			totalRead += uint32(got)
			regions++
		}
		cur = next
	}
	if totalRead == 0 {
		return nil, fmt.Errorf("no readable region inside 0x%X..0x%X", addr, end)
	}
	if r.verbose {
		fmt.Fprintf(os.Stderr, "  readAt 0x%X size=%d: %d regions, %d bytes read\n",
			addr, size, regions, totalRead)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Per-offset scan driver
// ---------------------------------------------------------------------------

// textVA is added to a `textBytes` offset to recover the RVA of a hit.
func (r *resolver) vlog(format string, args ...any) {
	if r.verbose {
		fmt.Fprintf(os.Stderr, "  "+format+"\n", args...)
	}
}

func (r *resolver) scan(name string) {
	defs := Patterns[name]
	res := &ScanResult{Name: name}
	r.results[name] = res

	if len(defs) == 0 {
		res.Status = "skip"
		res.Detail = "no patterns registered"
		return
	}

	for i, def := range defs {
		label := "aob-primary"
		if i > 0 {
			label = fmt.Sprintf("aob-alt-%d", i)
		}
		res.Logf("trying %s: %s", label, def.Description)
		r.vlog("%s [%s] %s", name, label, def.Description)

		p, err := ParsePattern(def.Pattern)
		if err != nil {
			res.Logf("pattern parse failed: %v", err)
			continue
		}
		hits := Scan(r.textBytes, p)
		if len(hits) == 0 {
			res.Logf("%s: 0 matches", label)
			continue
		}
		if len(hits) > 1 {
			res.Logf("%s: %d matches (ambiguous, skipping)", label, len(hits))
			res.MatchCount = len(hits)
			continue
		}
		// Exactly one match — resolve to RVA.
		matchOff := hits[0]
		matchRVA := int64(matchOff) + int64(r.textVA)

		var targetRVA int64
		if def.OperandOffset < 0 {
			// Match IS the RVA (for direct-match patterns like HpUpdateFn).
			targetRVA = matchRVA
		} else {
			// RIP-relative: read disp32 + compute target RVA.
			targetRVA, err = ResolveRip(r.textBytes, matchOff, def.OperandOffset)
			if err != nil {
				res.Logf("%s: rip resolve failed: %v", label, err)
				continue
			}
			// ResolveRip returns an offset relative to textBytes start (which corresponds
			// to textVA). Shift by textVA to get the final module RVA.
			targetRVA += int64(r.textVA)
		}

		res.Status = "ok"
		res.RVA = targetRVA
		res.Method = label
		res.Detail = def.Description
		res.MatchCount = 1
		res.MatchOffsets = []int64{matchRVA}
		res.Logf("MATCH @ RVA 0x%X → target RVA 0x%X", matchRVA, targetRVA)
		return
	}

	res.Status = "miss"
	res.Detail = "no pattern matched exactly once"
}

func (r *resolver) resolveAlias(alias, source string) {
	src := r.results[source]
	res := &ScanResult{Name: alias, Method: "alias"}
	r.results[alias] = res
	if src == nil || src.Status != "ok" {
		res.Status = "skip"
		res.Detail = fmt.Sprintf("source %s not resolved", source)
		return
	}
	res.Status = "ok"
	res.RVA = src.RVA
	res.Detail = fmt.Sprintf("= %s (0x%X)", source, src.RVA)
	res.Logf("aliased to %s", source)
}

func (r *resolver) resolveDelta(name, source string, delta int64) {
	src := r.results[source]
	res := &ScanResult{Name: name, Method: "delta"}
	r.results[name] = res
	if src == nil || src.Status != "ok" {
		res.Status = "skip"
		res.Detail = fmt.Sprintf("source %s not resolved", source)
		return
	}
	res.Status = "ok"
	res.RVA = src.RVA + delta
	res.Detail = fmt.Sprintf("%s + 0x%X", source, delta)
	res.Logf("%s (0x%X) + 0x%X = 0x%X", source, src.RVA, delta, res.RVA)
}

// ---------------------------------------------------------------------------
// Output
// ---------------------------------------------------------------------------

func (r *resolver) printSummary(elapsed time.Duration) {
	names := make([]string, 0, len(r.results))
	for n := range r.results {
		names = append(names, n)
	}
	sort.Strings(names)

	ok, miss, skip := 0, 0, 0
	fmt.Println("\n── scan results ────────────────────────────────────────────")
	for _, n := range names {
		res := r.results[n]
		var mark string
		switch res.Status {
		case "ok":
			ok++
			mark = "[OK]"
		case "miss":
			miss++
			mark = "[MISS]"
		case "skip":
			skip++
			mark = "[SKIP]"
		default:
			mark = "[??]"
		}
		if res.Status == "ok" {
			fmt.Printf("  %-6s %-18s  RVA=0x%-8X  (%s)\n", mark, n, res.RVA, res.Method)
		} else {
			fmt.Printf("  %-6s %-18s  %s\n", mark, n, res.Detail)
		}
	}
	fmt.Println("─────────────────────────────────────────────────────────────")
	fmt.Printf("  %d ok, %d miss, %d skip (elapsed %s)\n", ok, miss, skip, elapsed)
}

func (r *resolver) writeJSON(path string) error {
	out := struct {
		PID     uint32                 `json:"pid"`
		Base    string                 `json:"base"`
		Size    uint32                 `json:"size"`
		Results map[string]*ScanResult `json:"results"`
	}{r.pid, fmt.Sprintf("0x%X", r.base), r.size, r.results}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0644)
}

// writeGoSnippet writes a Go file that can be pasted into
// internal/gamelib/memory/offset.go to replace the hardcoded RVAs.
func (r *resolver) writeGoSnippet(path string) error {
	var sb strings.Builder
	sb.WriteString("// AUTO-GENERATED by tools/offset_resolver. Do not edit by hand.\n")
	sb.WriteString(fmt.Sprintf("// D2R pid=%d base=0x%X size=0x%X\n", r.pid, r.base, r.size))
	sb.WriteString(fmt.Sprintf("// Scan timestamp: %s\n\n", time.Now().Format(time.RFC3339)))
	sb.WriteString("package memory\n\n")
	sb.WriteString("func calculateOffsetsScanned() Offset {\n")
	sb.WriteString("\treturn Offset{\n")

	names := make([]string, 0, len(OffsetMeta))
	for n := range OffsetMeta {
		if OffsetMeta[n].Category == "d2go" && OffsetMeta[n].D2GoField != "" {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		res := r.results[n]
		if res == nil || res.Status != "ok" {
			sb.WriteString(fmt.Sprintf("\t\t// %s: NOT RESOLVED — keep previous value\n", OffsetMeta[n].D2GoField))
			continue
		}
		sb.WriteString(fmt.Sprintf("\t\t%s: uintptr(0x%X),\n", OffsetMeta[n].D2GoField, res.RVA))
	}
	sb.WriteString("\t}\n")
	sb.WriteString("}\n")
	return os.WriteFile(path, []byte(sb.String()), 0644)
}
