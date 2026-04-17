// bufpoll v2 — passive D2R packet sniffer via ReadProcessMemory + diff.
//
// Reads two known outgoing packet buffers in D2R every N ms.
// When content changes, XORs old vs new to find EXACTLY which bytes changed.
// The changed region = the new packet. Its length = the REAL packet size.
//
// Zero injection, zero debugger, zero code patches. Pure passive read.
//
// Build:  go build -o build/bufpoll.exe ./cmd/bufpoll
// Usage:  bufpoll.exe --pid <D2R pid> [--interval 10] [--duration 120]
//
// Perform ONE game action at a time (sell one item, cast one skill, etc.)
// to get clean single-packet captures with definitive sizes.
package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	flagPid      = flag.Int("pid", 0, "PID of D2R.exe")
	flagInterval = flag.Int("interval", 10, "Poll interval in milliseconds")
	flagDuration = flag.Int("duration", 120, "Run for N seconds")
	flagWindow   = flag.Int("window", 512, "Bytes to read at each offset")
	flagOut      = flag.String("out", "", "Optional file to mirror output into")
)

var bufferOffsets = []uintptr{
	0x19ED886, // buf0 — UI NetMan outgoing
	0x1F51330, // buf1 — Game NetMan outgoing
}

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

// diffRange returns the first and last index where a and b differ.
// Returns (-1, -1) if identical.
func diffRange(a, b []byte) (int, int) {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	first := -1
	last := -1
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			if first == -1 {
				first = i
			}
			last = i
		}
	}
	return first, last
}

func main() {
	flag.Parse()
	if *flagPid == 0 {
		fmt.Fprintln(os.Stderr, "usage: bufpoll --pid <D2R pid> [--interval 10] [--duration 120]")
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
		false,
		uint32(*flagPid),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "OpenProcess(%d): %v\n", *flagPid, err)
		os.Exit(1)
	}
	defer windows.CloseHandle(hProc)

	base, err := findD2RBase(uint32(*flagPid))
	if err != nil {
		fmt.Fprintf(os.Stderr, "findD2RBase: %v\n", err)
		os.Exit(1)
	}
	emit(fmt.Sprintf("# pid=%d D2R.exe base=0x%x", *flagPid, base))

	type slot struct {
		addr    uintptr
		label   string
		cur     []byte
		prev    []byte
		hasPrev bool
	}
	win := *flagWindow
	slots := make([]slot, len(bufferOffsets))
	for i, off := range bufferOffsets {
		slots[i] = slot{
			addr:  base + off,
			label: fmt.Sprintf("buf%d@+0x%x", i, off),
			cur:   make([]byte, win),
			prev:  make([]byte, win),
		}
		emit(fmt.Sprintf("# %s addr=0x%x window=%d", slots[i].label, slots[i].addr, win))
	}

	start := time.Now()
	ticker := time.NewTicker(time.Duration(*flagInterval) * time.Millisecond)
	defer ticker.Stop()
	deadline := start.Add(time.Duration(*flagDuration) * time.Second)

	pktCount := 0
	for now := range ticker.C {
		if now.After(deadline) {
			emit(fmt.Sprintf("# duration reached, %d packets captured", pktCount))
			return
		}
		ts := now.Sub(start).Truncate(time.Millisecond)

		for i := range slots {
			s := &slots[i]
			var read uintptr
			rerr := windows.ReadProcessMemory(hProc, s.addr, &s.cur[0], uintptr(win), &read)
			if rerr != nil || read == 0 {
				continue
			}

			if !s.hasPrev {
				copy(s.prev, s.cur[:read])
				s.hasPrev = true
				continue
			}

			first, last := diffRange(s.prev[:read], s.cur[:read])
			if first == -1 {
				continue // no change
			}

			opcode := s.cur[0] // TRUE opcode is always at buffer offset 0

			// Always dump a generous window so we see the full packet incl footer.
			// Use min(256, read) to capture enough context.
			dumpEnd := last + 1
			minDump := 64
			if dumpEnd < minDump {
				dumpEnd = minDump
			}
			if dumpEnd > int(read) {
				dumpEnd = int(read)
			}
			pktBytes := s.cur[:dumpEnd]
			hexStr := hex.EncodeToString(pktBytes)

			pktCount++
			emit(fmt.Sprintf("[%d] t+%s %s op=0x%02X first_diff=%d last_diff=%d hex=%s",
				pktCount, ts, s.label, opcode, first, last, hexStr))

			copy(s.prev, s.cur[:read])
		}
	}
}
