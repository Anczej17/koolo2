package ntapi

import (
	"crypto/rand"
	"math/big"
	"time"
	"unsafe"
)

// IsDebuggerAttached checks multiple anti-debug indicators using only
// direct memory reads — no API calls, no plaintext API/module names.
//
// Checks:
// 1. PEB.BeingDebugged flag (PEB+0x02)
// 2. PEB.NtGlobalFlag (PEB+0xBC) heap debug bits
// 3. KUSER_SHARED_DATA.KdDebuggerEnabled (0x7FFE02D4)
//
// ProcessDebugPort/ProcessDebugObjectHandle via NtQueryInformationProcess
// were removed — they required a lazy DLL proc call that left
// "NtQueryInformationProcess" + "ntdll.dll" plaintext in the binary.
// PEB + KUSER_SHARED_DATA checks catch the same cases without strings.
func IsDebuggerAttached() bool {
	pebAddr := getPEB()
	if pebAddr != 0 {
		beingDebugged := *(*byte)(unsafe.Pointer(pebAddr + 0x02))
		if beingDebugged != 0 {
			return true
		}

		// NtGlobalFlag at PEB+0xBC:
		// FLG_HEAP_ENABLE_TAIL_CHECK (0x10) | FLG_HEAP_ENABLE_FREE_CHECK (0x20) |
		// FLG_HEAP_VALIDATE_PARAMETERS (0x40) = 0x70 when a debugger attached.
		ntGlobalFlag := *(*uint32)(unsafe.Pointer(pebAddr + 0xBC))
		if ntGlobalFlag&0x70 != 0 {
			return true
		}
	}

	// KUSER_SHARED_DATA.KdDebuggerEnabled @ 0x7FFE02D4 — cannot be hooked.
	kdDebuggerEnabled := *(*byte)(unsafe.Pointer(uintptr(0x7FFE02D4)))
	return kdDebuggerEnabled != 0
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
