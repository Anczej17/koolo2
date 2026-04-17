// memdiff — PlayerUnit field scanner for movement destination discovery.
//
// Reads the PlayerUnit struct from D2R memory every N ms, diffs against
// previous snapshot, and reports which dwords changed. Run while the
// character is STANDING STILL, then click to walk — the new dwords that
// light up are movement destination candidates.
//
// Build:  go build -o build/memdiff.exe ./cmd/memdiff
// Usage:  memdiff.exe --pid <D2R pid>
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	flagPid      = flag.Int("pid", 0, "PID of D2R.exe")
	flagInterval = flag.Int("interval", 50, "Poll interval in ms")
	flagDuration = flag.Int("duration", 30, "Run for N seconds")
	flagWindow   = flag.Int("window", 8192, "Bytes to read from PlayerUnit")
	flagOut      = flag.String("out", "", "Optional output file")
)

const (
	TH32CS_SNAPMODULE   = 0x00000008
	TH32CS_SNAPMODULE32 = 0x00000010
)

type moduleEntry32W struct {
	Size         uint32
	ModuleID     uint32
	ProcessID    uint32
	GlblcntUsage uint32
	ProccntUsage uint32
	ModBaseAddr  uintptr
	ModBaseSize  uint32
	HModule      windows.Handle
	SzModule     [256]uint16
	SzExePath    [260]uint16
}

var (
	kernel32                     = windows.NewLazySystemDLL("kernel32.dll")
	procCreateToolhelp32Snapshot = kernel32.NewProc("CreateToolhelp32Snapshot")
	procModule32FirstW           = kernel32.NewProc("Module32FirstW")
	procModule32NextW            = kernel32.NewProc("Module32NextW")
)

// Known offsets (Diobyte 2026-04-10)
const (
	playerUnitIndexOffset = 0x1EAA3D4 // u32 index into unitHashTable
	unitHashTableOffset   = 0x1DFDAA0 // base of unit hash table
	playerPosOffset       = 0x1EC3FCC // playerPos.X (u32), +4=Y
)

func findD2RBase(pid uint32) (uintptr, error) {
	snap, _, err := procCreateToolhelp32Snapshot.Call(uintptr(TH32CS_SNAPMODULE|TH32CS_SNAPMODULE32), uintptr(pid))
	if snap == 0 || snap == ^uintptr(0) {
		return 0, fmt.Errorf("CreateToolhelp32Snapshot: %v", err)
	}
	defer windows.CloseHandle(windows.Handle(snap))

	var me moduleEntry32W
	me.Size = uint32(unsafe.Sizeof(me))
	r, _, err := procModule32FirstW.Call(snap, uintptr(unsafe.Pointer(&me)))
	if r == 0 {
		return 0, fmt.Errorf("Module32First: %v", err)
	}
	for {
		name := windows.UTF16ToString(me.SzModule[:])
		if name == "D2R.exe" {
			return me.ModBaseAddr, nil
		}
		r, _, _ := procModule32NextW.Call(snap, uintptr(unsafe.Pointer(&me)))
		if r == 0 {
			break
		}
	}
	return 0, fmt.Errorf("D2R.exe not found in pid %d", pid)
}

func readU32(h windows.Handle, addr uintptr) (uint32, error) {
	var buf [4]byte
	var read uintptr
	err := windows.ReadProcessMemory(h, addr, &buf[0], 4, &read)
	if err != nil || read < 4 {
		return 0, fmt.Errorf("read u32 at 0x%X: %v", addr, err)
	}
	return binary.LittleEndian.Uint32(buf[:]), nil
}

func readU64(h windows.Handle, addr uintptr) (uint64, error) {
	var buf [8]byte
	var read uintptr
	err := windows.ReadProcessMemory(h, addr, &buf[0], 8, &read)
	if err != nil || read < 8 {
		return 0, fmt.Errorf("read u64 at 0x%X: %v", addr, err)
	}
	return binary.LittleEndian.Uint64(buf[:]), nil
}

func readBytes(h windows.Handle, addr uintptr, size int) ([]byte, error) {
	buf := make([]byte, size)
	var read uintptr
	err := windows.ReadProcessMemory(h, addr, &buf[0], uintptr(size), &read)
	if err != nil || read == 0 {
		return nil, fmt.Errorf("read %d bytes at 0x%X: %v", size, addr, err)
	}
	return buf[:read], nil
}

func main() {
	flag.Parse()
	if *flagPid == 0 {
		fmt.Fprintln(os.Stderr, "usage: memdiff --pid <D2R pid>")
		os.Exit(2)
	}

	var outFile *os.File
	if *flagOut != "" {
		f, err := os.Create(*flagOut)
		if err != nil {
			fmt.Fprintf(os.Stderr, "open %s: %v\n", *flagOut, err)
			os.Exit(1)
		}
		outFile = f
		defer f.Close()
	}
	emit := func(s string) {
		fmt.Println(s)
		if outFile != nil {
			fmt.Fprintln(outFile, s)
			outFile.Sync()
		}
	}

	hProc, err := windows.OpenProcess(
		windows.PROCESS_VM_READ|windows.PROCESS_QUERY_INFORMATION,
		false, uint32(*flagPid),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "OpenProcess: %v\n", err)
		os.Exit(1)
	}
	defer windows.CloseHandle(hProc)

	base, err := findD2RBase(uint32(*flagPid))
	if err != nil {
		fmt.Fprintf(os.Stderr, "findD2RBase: %v\n", err)
		os.Exit(1)
	}
	emit(fmt.Sprintf("# D2R base=0x%X pid=%d", base, *flagPid))

	// Read player position
	posX, _ := readU32(hProc, base+playerPosOffset)
	posY, _ := readU32(hProc, base+playerPosOffset+4)
	emit(fmt.Sprintf("# playerPos: (%d, %d)", posX, posY))

	// Scan known data regions for movement-related changes.
	// We scan wide regions around playerPos, mouseXY, and other globals.
	type scanTarget struct {
		name   string
		addr   uintptr
		size   int
	}
	targets := []scanTarget{
		{"playerPos_wide", base + playerPosOffset - 512, 2048},   // 2KB around playerPos
		{"mouseXY_wide", base + 0x1EC3BB8 - 256, 1024},          // 1KB around mouseXY
		{"gameManager", base + 0x1EDF6F8 - 256, 1024},           // 1KB around gameManager
		{"unitHashTable", base + unitHashTableOffset - 256, 1024}, // 1KB around hash table
	}

	type snapshot struct {
		data []byte
	}
	prev := make([]snapshot, len(targets))
	hasPrev := false

	start := time.Now()
	ticker := time.NewTicker(time.Duration(*flagInterval) * time.Millisecond)
	defer ticker.Stop()
	deadline := start.Add(time.Duration(*flagDuration) * time.Second)

	changeCount := 0
	for now := range ticker.C {
		if now.After(deadline) {
			emit(fmt.Sprintf("# done, %d change events", changeCount))
			return
		}
		ts := now.Sub(start).Truncate(time.Millisecond)

		for i, t := range targets {
			cur, err := readBytes(hProc, t.addr, t.size)
			if err != nil {
				continue
			}

			if !hasPrev {
				prev[i] = snapshot{data: make([]byte, len(cur))}
				copy(prev[i].data, cur)
				continue
			}

			// Diff as dwords
			n := len(cur) / 4
			if len(prev[i].data)/4 < n {
				n = len(prev[i].data) / 4
			}
			for d := 0; d < n; d++ {
				off := d * 4
				curVal := binary.LittleEndian.Uint32(cur[off:])
				prevVal := binary.LittleEndian.Uint32(prev[i].data[off:])
				if curVal != prevVal {
					changeCount++
					emit(fmt.Sprintf("[%s] t+%s off=0x%04X prev=0x%08X cur=0x%08X (prev_dec=%d cur_dec=%d)",
						t.name, ts, off, prevVal, curVal, prevVal, curVal))
				}
			}
			copy(prev[i].data, cur)
		}
		if !hasPrev {
			hasPrev = true
			emit("# baseline captured, waiting for changes...")
		}
	}
}
