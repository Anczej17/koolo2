package ntapi

import (
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32          = windows.NewLazySystemDLL("kernel32.dll")
	ntdllLazy         = windows.NewLazySystemDLL("ntdll.dll")
	pIsDebuggerPresent = kernel32.NewProc("IsDebuggerPresent")
	pNtQueryInfoProc   = ntdllLazy.NewProc("NtQueryInformationProcess")
)

// IsDebuggerAttached checks multiple anti-debug indicators:
// 1. PEB.BeingDebugged flag (IsDebuggerPresent)
// 2. NtQueryInformationProcess with ProcessDebugPort
// 3. Timing-based check (rdtsc-style via QueryPerformanceCounter)
//
// Returns true if any debugger is detected.
func IsDebuggerAttached() bool {
	// Check 1: IsDebuggerPresent (checks PEB.BeingDebugged)
	r1, _, _ := pIsDebuggerPresent.Call()
	if r1 != 0 {
		return true
	}

	// Check 2: ProcessDebugPort (class 7)
	// If a debugger is attached, DebugPort is non-zero
	handle := uintptr(^uintptr(0)) // current process pseudo-handle (-1)
	var debugPort uintptr
	var returnLen uint32
	r1, _, _ = pNtQueryInfoProc.Call(
		handle,
		7, // ProcessDebugPort
		uintptr(unsafe.Pointer(&debugPort)),
		unsafe.Sizeof(debugPort),
		uintptr(unsafe.Pointer(&returnLen)),
	)
	if r1 == 0 && debugPort != 0 {
		return true
	}

	// Check 3: ProcessDebugObjectHandle (class 30)
	// If a debug object exists, a debugger is attached
	var debugObject uintptr
	r1, _, _ = pNtQueryInfoProc.Call(
		handle,
		30, // ProcessDebugObjectHandle
		uintptr(unsafe.Pointer(&debugObject)),
		unsafe.Sizeof(debugObject),
		uintptr(unsafe.Pointer(&returnLen)),
	)
	// STATUS_SUCCESS (0) means debug object exists = debugger attached
	if r1 == 0 {
		return true
	}

	return false
}

// TimingCheck performs a timing-based anti-debug check.
// Debuggers cause significant slowdown in code execution.
// Returns true if suspicious timing is detected.
func TimingCheck() bool {
	var freq, start, end int64
	syscall.Syscall(kernel32.NewProc("QueryPerformanceFrequency").Addr(), 1,
		uintptr(unsafe.Pointer(&freq)), 0, 0)
	syscall.Syscall(kernel32.NewProc("QueryPerformanceCounter").Addr(), 1,
		uintptr(unsafe.Pointer(&start)), 0, 0)

	// Perform a trivial operation that should take < 1ms
	sum := 0
	for i := range 1000 {
		sum += i
	}
	_ = sum

	syscall.Syscall(kernel32.NewProc("QueryPerformanceCounter").Addr(), 1,
		uintptr(unsafe.Pointer(&end)), 0, 0)

	if freq == 0 {
		return false
	}

	elapsed := time.Duration(float64(end-start) / float64(freq) * float64(time.Second))
	// A simple loop of 1000 iterations shouldn't take more than 500ms
	// Under a debugger with breakpoints, it often takes 1s+
	// Threshold raised to 500ms to avoid false positives under heavy load
	// (multiple game clients, garble-obfuscated code runs slower)
	return elapsed > 500*time.Millisecond
}

// StartAntiDebugMonitor runs periodic anti-debug checks in a goroutine.
// If a debugger is detected, calls the provided callback.
// The callback should handle graceful shutdown (e.g. os.Exit or cleanup).
// A grace period is applied at startup to avoid false positives during
// heavy initialization (config loading, UI startup, game attach).
func StartAntiDebugMonitor(interval time.Duration, onDetect func()) {
	go func() {
		// Grace period: skip checks during startup when CPU is busy
		// initializing config, UI, game readers, etc.
		time.Sleep(60 * time.Second)

		for {
			time.Sleep(interval)
			// Only trigger on debugger attachment, not timing alone.
			// Timing checks can false-positive under multi-client load.
			if IsDebuggerAttached() {
				onDetect()
				return
			}
			// Timing check requires 3 consecutive failures to trigger
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
		time.Sleep(100 * time.Millisecond)
	}
	return true
}
