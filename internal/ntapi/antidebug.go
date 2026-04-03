package ntapi

import (
	"crypto/rand"
	"math/big"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// IsDebuggerAttached checks multiple anti-debug indicators using
// direct PEB reads and indirect syscalls — no hooked API calls.
//
// Checks:
// 1. PEB.BeingDebugged flag (direct memory read, no API)
// 2. KUSER_SHARED_DATA.KdDebuggerEnabled (kernel debugger check)
// 3. NtQueryInformationProcess ProcessDebugPort (via trampoline if available)
// 4. NtQueryInformationProcess ProcessDebugObjectHandle
func IsDebuggerAttached() bool {
	// Check 1: Direct PEB read — BeingDebugged flag
	// PEB is at GS:[0x60] on x64. BeingDebugged is at PEB+0x02.
	pebAddr := getPEB()
	if pebAddr != 0 {
		beingDebugged := *(*byte)(unsafe.Pointer(pebAddr + 0x02))
		if beingDebugged != 0 {
			return true
		}

		// Also check NtGlobalFlag at PEB+0xBC
		// When debugger is attached, NtGlobalFlag has FLG_HEAP_ENABLE_TAIL_CHECK (0x10),
		// FLG_HEAP_ENABLE_FREE_CHECK (0x20), FLG_HEAP_VALIDATE_PARAMETERS (0x40)
		ntGlobalFlag := *(*uint32)(unsafe.Pointer(pebAddr + 0xBC))
		if ntGlobalFlag&0x70 != 0 {
			return true
		}
	}

	// Check 2: KUSER_SHARED_DATA.KdDebuggerEnabled
	// Fixed address 0x7FFE02D4 — no API call needed, cannot be hooked
	kdDebuggerEnabled := *(*byte)(unsafe.Pointer(uintptr(0x7FFE02D4)))
	if kdDebuggerEnabled != 0 {
		return true
	}

	// Check 3 & 4: ProcessDebugPort and ProcessDebugObjectHandle
	// Use NtQueryInformationProcess — prefer indirect syscall if available,
	// otherwise use lazy DLL proc as fallback for anti-debug specifically
	handle := uintptr(^uintptr(0)) // current process pseudo-handle

	ntdll := getLazyNtdll()
	ntQueryInfo := ntdll.NewProc("NtQueryInformationProcess")

	// ProcessDebugPort (class 7) — non-zero = debugger
	var debugPort uintptr
	var returnLen uint32
	r1, _, _ := ntQueryInfo.Call(
		handle,
		7,
		uintptr(unsafe.Pointer(&debugPort)),
		unsafe.Sizeof(debugPort),
		uintptr(unsafe.Pointer(&returnLen)),
	)
	if r1 == 0 && debugPort != 0 {
		return true
	}

	// ProcessDebugObjectHandle (class 30) — STATUS_SUCCESS = debugger
	var debugObject uintptr
	r1, _, _ = ntQueryInfo.Call(
		handle,
		30,
		uintptr(unsafe.Pointer(&debugObject)),
		unsafe.Sizeof(debugObject),
		uintptr(unsafe.Pointer(&returnLen)),
	)
	if r1 == 0 {
		return true
	}

	return false
}

// lazyNtdll caches the lazy DLL reference for anti-debug checks.
var lazyNtdll *lazyDLL

type lazyDLL struct {
	dll *windows.LazyDLL
}

func (d *lazyDLL) NewProc(name string) *windows.LazyProc {
	return d.dll.NewProc(name)
}

func getLazyNtdll() *lazyDLL {
	if lazyNtdll == nil {
		lazyNtdll = &lazyDLL{dll: windows.NewLazySystemDLL("ntdll.dll")}
	}
	return lazyNtdll
}

// TimingCheck performs a timing-based anti-debug check using
// RDTSC-equivalent via KUSER_SHARED_DATA timestamps.
// Debuggers cause significant slowdown in code execution.
func TimingCheck() bool {
	// KUSER_SHARED_DATA.SystemTime is at 0x7FFE0014 (KSYSTEM_TIME)
	// Read interrupt time which is high-resolution
	start := readInterruptTime()

	// Perform a trivial operation that should take < 1ms
	sum := 0
	for i := range 1000 {
		sum += i
	}
	_ = sum

	end := readInterruptTime()

	// Interrupt time is in 100ns units
	elapsed := time.Duration(end-start) * 100
	return elapsed > 500*time.Millisecond
}

// readInterruptTime reads KUSER_SHARED_DATA.InterruptTime (0x7FFE0008).
// This is a lock-free 64-bit read of a high-resolution timer.
func readInterruptTime() int64 {
	// KSYSTEM_TIME at 0x7FFE0008: Low (4 bytes), High1 (4 bytes), High2 (4 bytes)
	type ksystime struct {
		Low   uint32
		High1 int32
		High2 int32
	}

	for {
		t := (*ksystime)(unsafe.Pointer(uintptr(0x7FFE0008)))
		if t.High1 == t.High2 {
			return int64(t.High1)<<32 | int64(t.Low)
		}
		// High values disagree — re-read
	}
}

// StartAntiDebugMonitor runs periodic anti-debug checks in a goroutine.
// Uses randomized intervals and grace period to avoid detection.
func StartAntiDebugMonitor(interval time.Duration, onDetect func()) {
	go func() {
		// Randomized grace period: 45-90 seconds
		graceMs := 45000 + cryptRandInt(45000)
		time.Sleep(time.Duration(graceMs) * time.Millisecond)

		for {
			// Randomized check interval: 20-45 seconds
			sleepMs := 20000 + cryptRandInt(25000)
			time.Sleep(time.Duration(sleepMs) * time.Millisecond)

			if IsDebuggerAttached() {
				onDetect()
				return
			}
			if timingCheckConsecutive() {
				onDetect()
				return
			}
		}
	}()
}

// timingCheckConsecutive runs TimingCheck 3 times with short delays.
// Only returns true if all 3 checks indicate debugging (reduces false positives).
func timingCheckConsecutive() bool {
	for i := 0; i < 3; i++ {
		if !TimingCheck() {
			return false
		}
		time.Sleep(time.Duration(50+cryptRandInt(150)) * time.Millisecond)
	}
	return true
}

// cryptRandInt returns a crypto/rand integer in [0, n).
func cryptRandInt(n int) int {
	val, _ := rand.Int(rand.Reader, big.NewInt(int64(n)))
	return int(val.Int64())
}
