package main

import (
	"encoding/hex"
	"fmt"
	"os"
	"strconv"

	"golang.org/x/sys/windows"

	"local/internal/svc/internal/ntapi"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: ntapi_probe <pid> <hex-addr> [size]")
		os.Exit(2)
	}
	pid64, _ := strconv.ParseUint(os.Args[1], 10, 32)
	addr64, _ := strconv.ParseUint(os.Args[2], 0, 64)
	size := uint64(16)
	if len(os.Args) > 3 {
		size, _ = strconv.ParseUint(os.Args[3], 10, 32)
	}

	fmt.Printf("[probe] Init ntapi...\n")
	if err := ntapi.Init(); err != nil {
		fmt.Printf("[probe] Init FAILED: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("[probe] Init OK\n")

	h, err := ntapi.OpenProcess(windows.PROCESS_VM_READ|windows.PROCESS_QUERY_INFORMATION, uint32(pid64))
	if err != nil {
		fmt.Printf("[probe] ntapi.OpenProcess FAILED: %v\n", err)
		os.Exit(1)
	}
	defer ntapi.CloseHandle(h)
	fmt.Printf("[probe] ntapi.OpenProcess h=0x%X pid=%d\n", h, pid64)

	buf := make([]byte, size)
	err = ntapi.ReadProcessMemory(h, uintptr(addr64), &buf[0], uintptr(size))
	if err != nil {
		fmt.Printf("[probe] ntapi.ReadProcessMemory ERR: %v\n", err)
	}
	fmt.Printf("[probe] ntapi hex=%s\n", hex.EncodeToString(buf))

	buf2 := make([]byte, size)
	var got uintptr
	err = windows.ReadProcessMemory(h, uintptr(addr64), &buf2[0], uintptr(size), &got)
	if err != nil {
		fmt.Printf("[probe] windows.ReadProcessMemory ERR: %v\n", err)
	}
	fmt.Printf("[probe] win  hex=%s got=%d\n", hex.EncodeToString(buf2), got)
}
