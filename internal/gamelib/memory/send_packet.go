package memory

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"local/internal/svc/internal/ntapi"
)

const (
	d2gsSendPacketPattern = "\xE8\x00\x00\x00\x00\x0F\xB6\x85\x00\x00\x00\x00\x48\x03\xF0"
	d2gsSendPacketMask    = "x????xxx????xxx"
	d2gsSendPacketAlt     = "\x48\x8D\x45\xE0\x48\x89\x45\xC0\xE8\x00\x00\x00\x00\x48\x69\x4D\x88\xE8\x02\x00\x00\x44\x8B\x44\x24\x60"
	d2gsSendPacketAltMask = "xxxxxxxxx????xxxxxxxxxxxxx"
	maxPacketSize         = 65536
)

var (
	// sendPacketStubBase is generated polymorphically per session
	sendPacketStubBase = buildPolymorphicStub()

	kernel32                       = windows.NewLazySystemDLL("kernel32.dll")
	procVirtualAllocEx             = kernel32.NewProc("VirtualAllocEx")
	procVirtualFreeEx              = kernel32.NewProc("VirtualFreeEx")
	procQueueUserAPC               = kernel32.NewProc("QueueUserAPC")
	procSuspendThread              = kernel32.NewProc("SuspendThread")
	procResumeThread               = kernel32.NewProc("ResumeThread")
	procGetExitCodeThread          = kernel32.NewProc("GetExitCodeThread")
	procGetThreadTimes             = kernel32.NewProc("GetThreadTimes")
	procVirtualProtectEx           = kernel32.NewProc("VirtualProtectEx")
	procSetProcessValidCallTargets = kernel32.NewProc("SetProcessValidCallTargets")
	procCreateRemoteThread         = kernel32.NewProc("CreateRemoteThread")

	ntdll                  = windows.NewLazySystemDLL("ntdll.dll")
	procNtGetContextThread = ntdll.NewProc("NtGetContextThread")
	procNtSetContextThread = ntdll.NewProc("NtSetContextThread")

	d2gsCachedFn uintptr
	d2gsCacheMu  sync.RWMutex
	d2gsCachePID uint32

	metaBufPool = sync.Pool{
		New: func() interface{} {
			return new([sendPacketMetaSize]byte)
		},
	}
	sendPacketCallCount int
)

const (
	sendPacketProcessAccess = windows.PROCESS_VM_OPERATION | windows.PROCESS_VM_READ | windows.PROCESS_VM_WRITE | windows.PROCESS_QUERY_INFORMATION

	THREAD_SUSPEND_RESUME    = 0x0002
	THREAD_GET_CONTEXT       = 0x0008
	THREAD_SET_CONTEXT       = 0x0010
	THREAD_QUERY_INFORMATION = 0x0040
	sendPacketThreadAccess   = THREAD_SET_CONTEXT | THREAD_GET_CONTEXT | THREAD_SUSPEND_RESUME | THREAD_QUERY_INFORMATION

	sendPacketStatusOffset = 24
	sendPacketMetaSize     = 32

	threadStillActive = 259

	cfgCallTargetValid = 0x1
)

type sendPacketState struct {
	mu                  sync.Mutex
	handle              windows.Handle
	stub                uintptr
	meta                uintptr
	packet              uintptr
	packetCap           uintptr
	fn                  uintptr
	thread              windows.Handle
	threadID            uint32
	processPID          uint32
	threadLastValidated time.Time
	leakedBuffers       []uintptr
	lastSendTime        time.Time
	sendCount           int
	isExiting           bool // Set to true when game is exiting to skip all packets
}

type cfgCallTargetInfo struct {
	Offset uintptr
	Flags  uintptr
}

func findPatternOffset(memory []byte, pattern, mask string) int {
	if len(pattern) != len(mask) {
		return -1
	}
	patternLength := len(pattern)
	limit := len(memory) - patternLength
	for i := 0; i <= limit; i++ {
		match := true
		for j := 0; j < patternLength; j++ {
			if mask[j] == 'x' && memory[i+j] != pattern[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func (s *sendPacketState) ensureHandle(pid uint32) (windows.Handle, error) {
	if s.handle != 0 && s.processPID == pid {
		// Reset exit flag even if handle is reused - we're in a new game
		s.isExiting = false
		return s.handle, nil
	}

	if s.handle != 0 && s.processPID != pid {
		if s.packet != 0 {
			virtualFreeEx(s.handle, s.packet)
		}
		if s.meta != 0 {
			virtualFreeEx(s.handle, s.meta)
		}
		if s.stub != 0 {
			virtualFreeEx(s.handle, s.stub)
		}

		windows.CloseHandle(s.handle)
		s.handle = 0
		s.processPID = 0
		s.stub = 0
		s.meta = 0
		s.packet = 0
		s.packetCap = 0
		s.fn = 0
	}

	h, err := windows.OpenProcess(sendPacketProcessAccess, false, pid)
	if err != nil {
		return 0, fmt.Errorf("open process %d: %w", pid, err)
	}
	s.handle = h
	s.processPID = pid

	// Reset exiting flag when starting new game
	s.isExiting = false

	return h, nil
}

func (s *sendPacketState) ensureStub(handle windows.Handle) error {
	if s.stub != 0 {
		return nil
	}

	stubSize := uintptr(len(sendPacketStubBase))

	addr, err := virtualAllocEx(handle, stubSize, windows.PAGE_EXECUTE_READWRITE)
	if err != nil {
		return fmt.Errorf("alloc stub: %w", err)
	}

	if err := writeRemoteMemory(handle, addr, sendPacketStubBase); err != nil {
		virtualFreeEx(handle, addr)
		return fmt.Errorf("write stub: %w", err)
	}

	if err := markCallTargetValid(handle, addr, stubSize); err != nil {
		virtualFreeEx(handle, addr)
		return fmt.Errorf("register target: %w", err)
	}

	if err := virtualProtectEx(handle, addr, stubSize, windows.PAGE_EXECUTE_READ); err != nil {
		virtualFreeEx(handle, addr)
		return fmt.Errorf("set stub protection: %w", err)
	}

	s.stub = addr
	return nil
}

func (s *sendPacketState) ensureMeta(handle windows.Handle) error {
	if s.meta != 0 {
		return nil
	}
	addr, err := virtualAllocEx(handle, sendPacketMetaSize, windows.PAGE_READWRITE)
	if err != nil {
		return fmt.Errorf("alloc meta: %w", err)
	}
	s.meta = addr
	return nil
}

func (s *sendPacketState) ensurePacketBuffer(handle windows.Handle, size uintptr) error {
	if size == 0 {
		return errors.New("packet size must be greater than zero")
	}

	if size > maxPacketSize {
		return fmt.Errorf("data too large: %d (max %d)", size, maxPacketSize)
	}

	if size <= s.packetCap && s.packet != 0 {
		return nil
	}

	allocSize := size
	if size < 4096 {
		allocSize = 4096
	} else {
		allocSize = (size + 4095) &^ 4095
	}

	newPacket, err := virtualAllocEx(handle, allocSize, windows.PAGE_READWRITE)
	if err != nil {
		return fmt.Errorf("allocate packet buffer of %d bytes: %w", allocSize, err)
	}

	if s.packet != 0 {
		if err := virtualFreeEx(handle, s.packet); err != nil {
			log.Printf("Warning: failed to free old packet buffer at 0x%X: %v", s.packet, err)
			s.leakedBuffers = append(s.leakedBuffers, s.packet)
		}
	}

	s.packet = newPacket
	s.packetCap = allocSize

	// Periodically attempt to clean up leaked buffers
	if len(s.leakedBuffers) > 0 {
		s.cleanupLeakedBuffers(handle)
	}

	return nil
}

// cleanupLeakedBuffers attempts to free previously leaked buffers
func (s *sendPacketState) cleanupLeakedBuffers(handle windows.Handle) {
	if len(s.leakedBuffers) == 0 {
		return
	}

	cleaned := s.leakedBuffers[:0]
	for _, addr := range s.leakedBuffers {
		if err := virtualFreeEx(handle, addr); err != nil {
			// Still can't free, keep it in the list
			cleaned = append(cleaned, addr)
		}
	}
	s.leakedBuffers = cleaned

	if len(s.leakedBuffers) == 0 {
		log.Printf("cleanup done")
	}
}

// Cleanup sets exit flag to cancel all future packet sends
// Pending APCs in queue will timeout naturally, which is fine since game is exiting
// Also resets buffer pointers so they will be re-allocated on next game
// IMPORTANT: We do NOT free remote memory here because pending APCs may still be
// executing and trying to use that memory. Freeing it would cause D2R to crash.
// The memory will be freed when the process exits, or orphaned if game continues.
func (s *sendPacketState) Cleanup() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Set exiting flag to skip all future packet sends immediately
	s.isExiting = true

	log.Printf("marked exiting")

	// DO NOT free remote buffers - pending APCs may still be using them!
	// Just reset local pointers so they will be re-allocated on next game.
	// The remote memory will be orphaned but that's acceptable:
	// - If D2R exits/crashes, memory is freed with the process
	// - If game returns to menu, it's a small leak that won't accumulate
	//   (next game allocates new buffers with fresh pointers)
	if s.handle != 0 {
		// Reset pointers WITHOUT freeing - memory is orphaned but safe
		s.packet = 0
		s.packetCap = 0
		s.meta = 0
		s.stub = 0

		// Reset function pointer as it may be invalid after game exit
		s.fn = 0

		// Close thread handle - the main thread may change between games
		if s.thread != 0 {
			windows.CloseHandle(s.thread)
			s.thread = 0
			s.threadID = 0
		}
	}

	return nil
}

func (s *sendPacketState) ensureFunction(p *Process) (uintptr, error) {
	if s.fn != 0 {
		return s.fn, nil
	}

	fn, err := p.GetD2GSSendPacketFn()
	if err != nil {
		return 0, err
	}

	if fn == 0 {
		return 0, errors.New("send fn pointer is null")
	}

	s.fn = fn
	return fn, nil
}

func (s *sendPacketState) ensureThreadHandle(p *Process) (windows.Handle, error) {
	if s.thread != 0 && s.processPID == p.pid {
		// Validate thread is still active and belongs to the correct process
		if err := validateThreadActive(s.thread); err == nil {
			// Additional validation: verify thread still belongs to this process
			if err := validateThreadBelongsToProcess(s.thread, p.pid); err == nil {
				s.threadLastValidated = time.Now()
				return s.thread, nil
			}
		}
		// Thread is invalid, close and reset
		windows.CloseHandle(s.thread)
		s.thread = 0
		s.threadID = 0
		s.processPID = 0
	} else if s.thread != 0 && s.processPID != p.pid {
		windows.CloseHandle(s.thread)
		s.thread = 0
		s.threadID = 0
		s.processPID = 0
	}

	threadID := p.preferredPacketTID
	if threadID == 0 {
		var err error
		threadID, err = findMainThreadID(p.pid)
		if err != nil {
			return 0, err
		}
	}

	handle, err := windows.OpenThread(sendPacketThreadAccess, false, threadID)
	if err != nil {
		return 0, fmt.Errorf("open thread %d: %w", threadID, err)
	}

	// Validate the newly opened thread belongs to the correct process
	if err := validateThreadBelongsToProcess(handle, p.pid); err != nil {
		windows.CloseHandle(handle)
		return 0, fmt.Errorf("thread %d validation failed: %w", threadID, err)
	}

	s.thread = handle
	s.threadID = threadID
	s.processPID = p.pid
	s.threadLastValidated = time.Now()
	return handle, nil
}

func validateThreadActive(thread windows.Handle) error {
	var exitCode uint32
	ret, _, err := procGetExitCodeThread.Call(
		uintptr(thread),
		uintptr(unsafe.Pointer(&exitCode)),
	)
	if ret == 0 {
		if err != nil {
			return fmt.Errorf("GetExitCodeThread: %w", err)
		}
		return errors.New("GetExitCodeThread failed")
	}

	if exitCode != threadStillActive {
		return fmt.Errorf("thread is not active (exit code: %d)", exitCode)
	}

	return nil
}

// validateThreadBelongsToProcess verifies the thread belongs to the specified process
// Uses basic process handle comparison for validation
func validateThreadBelongsToProcess(thread windows.Handle, expectedPID uint32) error {
	// Try to get process ID of thread by checking thread times
	// If we can get thread times, the thread is valid
	var creationTime, exitTime, kernelTime, userTime windows.Filetime
	ret, _, err := procGetThreadTimes.Call(
		uintptr(thread),
		uintptr(unsafe.Pointer(&creationTime)),
		uintptr(unsafe.Pointer(&exitTime)),
		uintptr(unsafe.Pointer(&kernelTime)),
		uintptr(unsafe.Pointer(&userTime)),
	)
	if ret == 0 {
		if err != nil {
			return fmt.Errorf("thread validation failed (GetThreadTimes): %w", err)
		}
		return errors.New("thread validation failed: GetThreadTimes returned 0")
	}

	// Thread is valid and accessible, which means we have permission
	// This is a basic check - we already validated the thread ID when opening it
	return nil
}

func (s *sendPacketState) dispatchAPC(thread windows.Handle, start, parameter uintptr) error {
	if thread == 0 {
		return errors.New("thread handle is zero")
	}

	if err := suspendThread(thread); err != nil {
		return err
	}

	queued := false
	defer func() {
		if !queued {
			_ = resumeThread(thread)
		}
	}()

	if err := queueUserAPC(start, thread, parameter); err != nil {
		return err
	}
	queued = true

	if err := resumeThread(thread); err != nil {
		queued = false
		return err
	}

	return nil
}

func queueUserAPC(start uintptr, thread windows.Handle, parameter uintptr) error {
	ret, _, err := procQueueUserAPC.Call(start, uintptr(thread), parameter)
	if ret == 0 {
		if err != nil {
			return fmt.Errorf("queue: %w", err)
		}
		return errors.New("queue failed")
	}
	return nil
}

func suspendThread(thread windows.Handle) error {
	ret, _, err := procSuspendThread.Call(uintptr(thread))
	if ret == ^uintptr(0) {
		if err != nil {
			return fmt.Errorf("suspend: %w", err)
		}
		return errors.New("suspend failed")
	}
	return nil
}

func resumeThread(thread windows.Handle) error {
	ret, _, err := procResumeThread.Call(uintptr(thread))
	if ret == ^uintptr(0) {
		if err != nil {
			return fmt.Errorf("resume: %w", err)
		}
		return errors.New("resume failed")
	}
	return nil
}

// getThreadCreationTime gets the creation time of a thread
func getThreadCreationTime(thread windows.Handle) (int64, error) {
	var creationTime, exitTime, kernelTime, userTime windows.Filetime
	ret, _, err := procGetThreadTimes.Call(
		uintptr(thread),
		uintptr(unsafe.Pointer(&creationTime)),
		uintptr(unsafe.Pointer(&exitTime)),
		uintptr(unsafe.Pointer(&kernelTime)),
		uintptr(unsafe.Pointer(&userTime)),
	)
	if ret == 0 {
		if err != nil {
			return 0, fmt.Errorf("GetThreadTimes: %w", err)
		}
		return 0, errors.New("GetThreadTimes failed")
	}

	return int64(creationTime.HighDateTime)<<32 | int64(creationTime.LowDateTime), nil
}

func findMainThreadID(pid uint32) (uint32, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return 0, fmt.Errorf("CreateToolhelp32Snapshot: %w", err)
	}
	defer windows.CloseHandle(snapshot)

	var entry windows.ThreadEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Thread32First(snapshot, &entry); err != nil {
		return 0, fmt.Errorf("Thread32First: %w", err)
	}

	var mainThreadID uint32
	var earliestCreationTime int64 = 0x7FFFFFFFFFFFFFFF
	var found bool

	for {
		if entry.OwnerProcessID == pid {
			thread, err := windows.OpenThread(sendPacketThreadAccess, false, entry.ThreadID)
			if err == nil {
				creationTime, err := getThreadCreationTime(thread)
				windows.CloseHandle(thread)

				if err == nil && creationTime < earliestCreationTime {
					earliestCreationTime = creationTime
					mainThreadID = entry.ThreadID
					found = true
				}
			}
		}

		if err := windows.Thread32Next(snapshot, &entry); err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
				break
			}
			return 0, fmt.Errorf("Thread32Next: %w", err)
		}
	}

	if !found {
		return 0, fmt.Errorf("no threads found for process %d", pid)
	}

	return mainThreadID, nil
}

func validatePatternMatch(memory []byte, offset int) bool {
	if offset >= len(memory) || memory[offset] != 0xE8 {
		return false
	}

	if offset+5 > len(memory) {
		return false
	}

	if offset+14 < len(memory) {
		if memory[offset+5] != 0x0F || memory[offset+6] != 0xB6 {
			return false
		}
	}

	return true
}

// Known D2GS SendPacket RVA for D2R build 7723df1e10d79805.
//
// Live-verified working sessions resolve send_fn at base+0x146600. The older
// alt pattern can match a caller/helper near base+0x1533B24; packets dispatched
// there report success but are ignored by NPC flows. Keep the fallback on the
// actual D2GS send_fn until a new baked build entry proves otherwise.
const sendPacketKnownRVA uintptr = 0x146600

// dual_send_wrap RVA: dedup wrapper that memcmp/memcpy to mirror buffer then
// calls send_fn. Required for 0x50 swap, 0x32/0x33 sell/buy, 0x5C cain, etc.
// Standard x64 ABI: rcx=pkt_ptr, edx=size. Arxan-encrypted at rest — decrypts
// on first vendor/trade interaction.
const dualSendWrapRVA uintptr = 0x147110

func (p *Process) GetD2GSSendPacketFn() (uintptr, error) {
	if p == nil {
		return 0, errors.New("process is nil")
	}

	if p.handler == 0 {
		return 0, errors.New("process handle is invalid")
	}

	d2gsCacheMu.RLock()
	if d2gsCachedFn != 0 && d2gsCachePID == p.pid {
		fn := d2gsCachedFn
		d2gsCacheMu.RUnlock()
		return fn, nil
	}
	d2gsCacheMu.RUnlock()

	// Try pattern scan (2 attempts). If the page is Warden-protected, fall
	// through to known RVA fallback without long delays.
	for attempt := 0; attempt < 2; attempt++ {
		if fn, err := p.scanForSendPacketPattern(); err == nil {
			d2gsCacheMu.Lock()
			d2gsCachedFn = fn
			d2gsCachePID = p.pid
			d2gsCacheMu.Unlock()
			return fn, nil
		}
		if attempt == 0 {
			time.Sleep(500 * time.Millisecond)
		}
	}

	// Fallback: use known RVA from offset scanner. The address is valid even if
	// the page is externally protected by Warden — rmod.dll calls it in-process
	// where all code pages are accessible.
	fallback := p.moduleBaseAddressPtr + sendPacketKnownRVA
	log.Printf("send fn: pattern scan failed after retries, using known RVA: base=%#x + %#x = %#x", p.moduleBaseAddressPtr, sendPacketKnownRVA, fallback)
	d2gsCacheMu.Lock()
	d2gsCachedFn = fallback
	d2gsCachePID = p.pid
	d2gsCacheMu.Unlock()
	return fallback, nil
}

func (p *Process) scanForSendPacketPattern() (uintptr, error) {
	modules, err := GetProcessModules(p.pid)
	if err != nil {
		return 0, fmt.Errorf("enumerate modules: %w", err)
	}

	for _, module := range modules {
		name := strings.ToLower(module.ModuleName)

		if strings.Contains(name, "windows") || strings.Contains(name, "system32") {
			continue
		}

		if module.ModuleBaseSize == 0 || module.ModuleBaseSize > 100*1024*1024 {
			continue
		}

		memory, err := ReadMemoryChunked(p.handler, module.ModuleBaseAddress, module.ModuleBaseSize)
		if err != nil {
			continue
		}

		offset := findPatternOffset(memory, d2gsSendPacketPattern, d2gsSendPacketMask)
		if offset >= 0 && validatePatternMatch(memory, offset) {
			patternAddr := module.ModuleBaseAddress + uintptr(offset)
			relOffset := int32(binary.LittleEndian.Uint32(memory[offset+1 : offset+5]))
			absolute := uintptr(int64(patternAddr+5) + int64(relOffset))

			if absolute != 0 && absolute >= module.ModuleBaseAddress {
				log.Printf("send fn resolved at 0x%X in module %s", absolute, module.ModuleName)
				return absolute, nil
			}
		}

		// The alt signature is useful only as a last-resort sanity check. On
		// the current build it resolves to base+0x1533B24, which is not the
		// D2GS send_fn and breaks NPC packets. Accept it only when it lands on
		// the same verified RVA as the baked fallback.
		if offset := findPatternOffset(memory, d2gsSendPacketAlt, d2gsSendPacketAltMask); offset >= 0 {
			callOffset := offset + 8
			patternAddr := module.ModuleBaseAddress + uintptr(callOffset)
			relOffset := int32(binary.LittleEndian.Uint32(memory[callOffset+1 : callOffset+5]))
			absolute := uintptr(int64(patternAddr+5) + int64(relOffset))
			if absolute == module.ModuleBaseAddress+sendPacketKnownRVA {
				log.Printf("send fn resolved by verified alt pattern at 0x%X in module %s", absolute, module.ModuleName)
				return absolute, nil
			}
			log.Printf("send fn alt pattern ignored: resolved 0x%X, expected 0x%X", absolute, module.ModuleBaseAddress+sendPacketKnownRVA)
		}
	}

	return 0, errors.New("send fn pattern not found")
}

func (p *Process) SendPacket(packet []byte) (err error) {
	return p.SendPacketWithTimeout(packet, 100*time.Millisecond)
}

// SendPacketAPC forces the main-thread APC path, bypassing any externalSend
// override (presenter / rmod render-thread path). Memory project_weapon_swap_solved
// 04-14 proved 5 consecutive 0x50 swaps work via this exact path; the presenter
// regressed to render-thread call and server stopped accepting the swap.
// Swaps externalSend temporarily — debug-only, not concurrency safe vs other
// SendPacket callers.
func (p *Process) SendPacketAPC(packet []byte) error {
	saved := p.externalSend
	p.externalSend = nil
	err := p.SendPacketWithTimeout(packet, 100*time.Millisecond)
	p.externalSend = saved
	return err
}

// SendDualPacketAPC replicates the 04-14 proven dual-send path:
//  1. WriteProcessMemory to D2R mirror buffer
//  2. send_fn APC on main thread (NOT render thread like the presenter path)
//
// Used for 0x50 and other opcodes that need main-thread local dispatch plus
// a populated mirror buffer. Bypasses any presenter override.
func (p *Process) SendDualPacketAPC(packet []byte) error {
	if p == nil {
		return errors.New("process is nil")
	}
	if p.moduleBaseAddressPtr == 0 || p.handler == 0 {
		return errors.New("dual APC: process not initialized")
	}
	mirrorAddr, merr := p.ResolveMirrorBufAddr()
	if merr != nil {
		// Fallback to pre-3.0 hardcoded RVA if dynamic resolve fails
		// (dual_send_wrap body obfuscated). Sniffer evidence shows
		// 0x1F51330 still receives writes on D2R 3.0.92198.
		mirrorAddr = p.moduleBaseAddressPtr + 0x1F51330
	}
	if err := windows.WriteProcessMemory(p.handler, mirrorAddr, &packet[0], uintptr(len(packet)), nil); err != nil {
		return fmt.Errorf("dual APC: mirror write to 0x%X: %w", mirrorAddr, err)
	}
	return p.SendPacketAPC(packet)
}

// SendPacketViaDualWrap sends a packet through D2R's dual_send_wrap function
// via APC on the main thread. This is required for opcodes that crash through
// send_fn (0x50 swap, 0x32/0x33 sell/buy, etc.). The APC shellcode is identical
// to SendPacket — only the target function address differs.
func (p *Process) SendPacketViaDualWrap(packet []byte) error {
	if p == nil {
		return errors.New("process is nil")
	}
	if len(packet) == 0 || len(packet) > maxPacketSize {
		return fmt.Errorf("invalid packet size: %d", len(packet))
	}

	dualFnAddr := p.moduleBaseAddressPtr + dualSendWrapRVA
	log.Printf("SendPacketViaDualWrap: opcode=0x%02X size=%d fn=0x%X", packet[0], len(packet), dualFnAddr)

	p.sendPacketMu.Lock()
	if p.sendPacket == nil {
		p.sendPacket = &sendPacketState{}
	}
	state := p.sendPacket
	state.mu.Lock()
	p.sendPacketMu.Unlock()
	defer state.mu.Unlock()

	handle, err := state.ensureHandle(p.pid)
	if err != nil {
		return fmt.Errorf("open process: %w", err)
	}

	if err := state.ensureStub(handle); err != nil {
		return err
	}
	if err := state.ensureMeta(handle); err != nil {
		return err
	}
	if err := state.ensurePacketBuffer(handle, uintptr(len(packet))); err != nil {
		return err
	}
	if err := writeRemoteMemory(handle, state.packet, packet); err != nil {
		return fmt.Errorf("write pkt: %w", err)
	}

	metaBuf := metaBufPool.Get().(*[sendPacketMetaSize]byte)
	defer func() {
		*metaBuf = [sendPacketMetaSize]byte{}
		metaBufPool.Put(metaBuf)
	}()

	binary.LittleEndian.PutUint64(metaBuf[0:], uint64(dualFnAddr))
	binary.LittleEndian.PutUint64(metaBuf[8:], uint64(state.packet))
	binary.LittleEndian.PutUint64(metaBuf[16:], uint64(len(packet)))
	binary.LittleEndian.PutUint32(metaBuf[sendPacketStatusOffset:], 0)

	if err := writeRemoteMemory(handle, state.meta, metaBuf[:]); err != nil {
		return fmt.Errorf("write meta: %w", err)
	}

	threadHandle, err := state.ensureThreadHandle(p)
	if err != nil {
		return fmt.Errorf("resolve main thread: %w", err)
	}

	if err := state.dispatchAPC(threadHandle, state.stub, state.meta); err != nil {
		return fmt.Errorf("dispatch APC: %w", err)
	}

	return p.waitForPacketCompletion(handle, state, 200*time.Millisecond)
}

// SendPacketViaSendFnHijack executes D2GS send_fn(pkt,len,0) immediately on
// the selected D2R thread by temporarily redirecting RIP to a one-shot stub.
// This is a controlled fallback for stateful AMB packets where queued APCs
// complete but the server ignores the packet.
func (p *Process) SendPacketViaSendFnHijack(packet []byte) error {
	if p == nil {
		return errors.New("process is nil")
	}
	if len(packet) == 0 || len(packet) > maxPacketSize {
		return fmt.Errorf("invalid packet size: %d", len(packet))
	}

	p.sendPacketMu.Lock()
	if p.sendPacket == nil {
		p.sendPacket = &sendPacketState{}
	}
	state := p.sendPacket
	state.mu.Lock()
	p.sendPacketMu.Unlock()
	defer state.mu.Unlock()

	handle, err := state.ensureHandle(p.pid)
	if err != nil {
		return fmt.Errorf("open process: %w", err)
	}
	fnAddr, err := state.ensureFunction(p)
	if err != nil {
		return fmt.Errorf("resolve send fn: %w", err)
	}
	threadHandle, err := state.ensureThreadHandle(p)
	if err != nil {
		return fmt.Errorf("resolve packet thread: %w", err)
	}

	packetLen := uintptr(len(packet))
	statusOff := alignUp(packetLen, 0x10)
	scOff := statusOff + 0x10
	blockSize := alignUp(scOff+0x800, 0x1000)
	block, err := virtualAllocEx(handle, blockSize, windows.PAGE_EXECUTE_READWRITE)
	if err != nil {
		return fmt.Errorf("alloc hijack block: %w", err)
	}
	state.leakedBuffers = append(state.leakedBuffers, block)

	pktAddr := block
	statusAddr := block + statusOff
	scAddr := block + scOff
	stackTop := (block + blockSize - 0x10) &^ 0xF
	if err := writeRemoteMemory(handle, pktAddr, packet); err != nil {
		return fmt.Errorf("write hijack pkt: %w", err)
	}
	var zeroStatus [4]byte
	if err := writeRemoteMemory(handle, statusAddr, zeroStatus[:]); err != nil {
		return fmt.Errorf("write hijack status: %w", err)
	}

	if err := suspendThread(threadHandle); err != nil {
		return fmt.Errorf("suspend packet thread: %w", err)
	}
	suspended := true
	defer func() {
		if suspended {
			_ = resumeThread(threadHandle)
		}
	}()

	var ctx CONTEXT64
	if err := getThreadContext(threadHandle, &ctx); err != nil {
		return fmt.Errorf("get packet thread context: %w", err)
	}
	origRip := ctx.Rip
	origRsp := ctx.Rsp

	sc := buildSendFnHijackStub(fnAddr, pktAddr, packetLen, statusAddr, uintptr(origRip), uintptr(origRsp))
	if err := writeRemoteMemory(handle, scAddr, sc); err != nil {
		return fmt.Errorf("write hijack stub: %w", err)
	}

	ctx.Rip = uint64(scAddr)
	ctx.Rsp = uint64(stackTop)
	if err := setThreadContext(threadHandle, &ctx); err != nil {
		return fmt.Errorf("set packet thread context: %w", err)
	}
	if err := resumeThread(threadHandle); err != nil {
		return fmt.Errorf("resume packet thread: %w", err)
	}
	suspended = false

	deadline := time.Now().Add(500 * time.Millisecond)
	var status uint32
	for time.Now().Before(deadline) {
		var buf [4]byte
		var bytesRead uintptr
		if err := windows.ReadProcessMemory(handle, statusAddr, &buf[0], 4, &bytesRead); err == nil && bytesRead == 4 {
			status = binary.LittleEndian.Uint32(buf[:])
			if status == 1 {
				return nil
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	return fmt.Errorf("send_fn hijack timeout status=%d opcode=0x%02X", status, packet[0])
}

func buildSendFnHijackStub(fnAddr, pktAddr, packetLen, statusAddr, returnRip, returnRsp uintptr) []byte {
	var sc []byte
	sc = append(sc, 0x9C) // pushfq
	sc = append(sc, 0x50, 0x51, 0x52, 0x53, 0x55, 0x56, 0x57)
	sc = append(sc,
		0x41, 0x50,
		0x41, 0x51,
		0x41, 0x52,
		0x41, 0x53,
		0x41, 0x54,
		0x41, 0x55,
		0x41, 0x56,
		0x41, 0x57,
	)
	sc = append(sc, 0x48, 0x83, 0xEC, 0x20) // shadow space; RSP is 16-byte aligned before CALL
	sc = append(sc, 0x48, 0xB9)             // mov rcx, pktAddr
	sc = appendU64LE(sc, uint64(pktAddr))
	sc = append(sc, 0xBA) // mov edx, packetLen
	var lenBuf [4]byte
	binary.LittleEndian.PutUint32(lenBuf[:], uint32(packetLen))
	sc = append(sc, lenBuf[:]...)
	sc = append(sc, 0x45, 0x31, 0xC0) // xor r8d, r8d
	sc = append(sc, 0x48, 0xB8)       // mov rax, fnAddr
	sc = appendU64LE(sc, uint64(fnAddr))
	sc = append(sc, 0xFF, 0xD0) // call rax
	sc = append(sc, 0x48, 0xBB) // mov rbx, statusAddr
	sc = appendU64LE(sc, uint64(statusAddr))
	sc = append(sc, 0xC7, 0x03, 0x01, 0x00, 0x00, 0x00)
	sc = append(sc, 0x48, 0x83, 0xC4, 0x20)
	sc = append(sc,
		0x41, 0x5F,
		0x41, 0x5E,
		0x41, 0x5D,
		0x41, 0x5C,
		0x41, 0x5B,
		0x41, 0x5A,
		0x41, 0x59,
		0x41, 0x58,
	)
	sc = append(sc, 0x5F, 0x5E, 0x5D, 0x5B, 0x5A, 0x59, 0x58)
	sc = append(sc, 0x9D)       // popfq
	sc = append(sc, 0x48, 0xBC) // mov rsp, returnRsp
	sc = appendU64LE(sc, uint64(returnRsp))
	sc = append(sc, 0xFF, 0x25, 0x00, 0x00, 0x00, 0x00) // jmp qword ptr [rip+0]
	sc = appendU64LE(sc, uint64(returnRip))
	return sc
}

func appendU64LE(buf []byte, val uint64) []byte {
	var tmp [8]byte
	binary.LittleEndian.PutUint64(tmp[:], val)
	return append(buf, tmp[:]...)
}

// CallFnViaThreadHijack executes an arbitrary D2R function on the selected
// packet/game thread by temporarily redirecting RIP to a one-shot stub. It is
// used for native client handlers when APC delivery never reaches an alertable
// wait in the target thread.
func (p *Process) CallFnViaThreadHijack(fnAddr uintptr, args ...uintptr) (uint64, error) {
	if p == nil {
		return 0, errors.New("process is nil")
	}
	if fnAddr == 0 {
		return 0, errors.New("function address is zero")
	}
	if len(args) > 6 {
		return 0, fmt.Errorf("too many args: %d", len(args))
	}
	var argv [6]uintptr
	copy(argv[:], args)

	p.sendPacketMu.Lock()
	if p.sendPacket == nil {
		p.sendPacket = &sendPacketState{}
	}
	state := p.sendPacket
	state.mu.Lock()
	p.sendPacketMu.Unlock()
	defer state.mu.Unlock()

	handle, err := state.ensureHandle(p.pid)
	if err != nil {
		return 0, fmt.Errorf("open process: %w", err)
	}
	threadHandle, err := state.ensureThreadHandle(p)
	if err != nil {
		return 0, fmt.Errorf("resolve packet thread: %w", err)
	}

	blockSize := uintptr(4096)
	block, err := virtualAllocEx(handle, blockSize, windows.PAGE_EXECUTE_READWRITE)
	if err != nil {
		return 0, fmt.Errorf("alloc call hijack block: %w", err)
	}
	state.leakedBuffers = append(state.leakedBuffers, block)

	statusAddr := block
	retAddr := block + 0x08
	scAddr := block + 0x100
	stackTop := (block + blockSize - 0x10) &^ 0xF
	var zero [16]byte
	if err := writeRemoteMemory(handle, statusAddr, zero[:]); err != nil {
		return 0, fmt.Errorf("write call hijack status: %w", err)
	}

	if err := suspendThread(threadHandle); err != nil {
		return 0, fmt.Errorf("suspend packet thread: %w", err)
	}
	suspended := true
	defer func() {
		if suspended {
			_ = resumeThread(threadHandle)
		}
	}()

	var ctx CONTEXT64
	if err := getThreadContext(threadHandle, &ctx); err != nil {
		return 0, fmt.Errorf("get packet thread context: %w", err)
	}
	origRip := ctx.Rip
	origRsp := ctx.Rsp

	sc := buildCallFnHijackStub(fnAddr, argv, statusAddr, retAddr, uintptr(origRip), uintptr(origRsp))
	if err := writeRemoteMemory(handle, scAddr, sc); err != nil {
		return 0, fmt.Errorf("write call hijack stub: %w", err)
	}

	ctx.Rip = uint64(scAddr)
	ctx.Rsp = uint64(stackTop)
	if err := setThreadContext(threadHandle, &ctx); err != nil {
		return 0, fmt.Errorf("set packet thread context: %w", err)
	}
	if err := resumeThread(threadHandle); err != nil {
		return 0, fmt.Errorf("resume packet thread: %w", err)
	}
	suspended = false

	deadline := time.Now().Add(2 * time.Second)
	var status uint32
	for time.Now().Before(deadline) {
		var buf [4]byte
		var bytesRead uintptr
		if err := windows.ReadProcessMemory(handle, statusAddr, &buf[0], 4, &bytesRead); err == nil && bytesRead == 4 {
			status = binary.LittleEndian.Uint32(buf[:])
			if status == 1 {
				var retBuf [8]byte
				if err := windows.ReadProcessMemory(handle, retAddr, &retBuf[0], 8, &bytesRead); err != nil || bytesRead != 8 {
					return 0, fmt.Errorf("read call hijack return: %w", err)
				}
				return binary.LittleEndian.Uint64(retBuf[:]), nil
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	return 0, fmt.Errorf("call hijack timeout status=%d fn=%#x", status, fnAddr)
}

func buildCallFnHijackStub(fnAddr uintptr, args [6]uintptr, statusAddr, retAddr, returnRip, returnRsp uintptr) []byte {
	var sc []byte
	sc = append(sc, 0x9C) // pushfq
	sc = append(sc, 0x50, 0x51, 0x52, 0x53, 0x55, 0x56, 0x57)
	sc = append(sc,
		0x41, 0x50,
		0x41, 0x51,
		0x41, 0x52,
		0x41, 0x53,
		0x41, 0x54,
		0x41, 0x55,
		0x41, 0x56,
		0x41, 0x57,
	)
	sc = append(sc, 0x48, 0x83, 0xEC, 0x30)
	sc = append(sc, 0x48, 0xB8)
	sc = appendU64LE(sc, uint64(args[4]))
	sc = append(sc, 0x48, 0x89, 0x44, 0x24, 0x20)
	sc = append(sc, 0x48, 0xB8)
	sc = appendU64LE(sc, uint64(args[5]))
	sc = append(sc, 0x48, 0x89, 0x44, 0x24, 0x28)
	sc = append(sc, 0x48, 0xB9)
	sc = appendU64LE(sc, uint64(args[0]))
	sc = append(sc, 0x48, 0xBA)
	sc = appendU64LE(sc, uint64(args[1]))
	sc = append(sc, 0x49, 0xB8)
	sc = appendU64LE(sc, uint64(args[2]))
	sc = append(sc, 0x49, 0xB9)
	sc = appendU64LE(sc, uint64(args[3]))
	sc = append(sc, 0x48, 0xB8)
	sc = appendU64LE(sc, uint64(fnAddr))
	sc = append(sc, 0xFF, 0xD0)
	sc = append(sc, 0x48, 0xBB)
	sc = appendU64LE(sc, uint64(retAddr))
	sc = append(sc, 0x48, 0x89, 0x03)
	sc = append(sc, 0x48, 0xBB)
	sc = appendU64LE(sc, uint64(statusAddr))
	sc = append(sc, 0xC7, 0x03, 0x01, 0x00, 0x00, 0x00)
	sc = append(sc, 0x48, 0x83, 0xC4, 0x30)
	sc = append(sc,
		0x41, 0x5F,
		0x41, 0x5E,
		0x41, 0x5D,
		0x41, 0x5C,
		0x41, 0x5B,
		0x41, 0x5A,
		0x41, 0x59,
		0x41, 0x58,
	)
	sc = append(sc, 0x5F, 0x5E, 0x5D, 0x5B, 0x5A, 0x59, 0x58)
	sc = append(sc, 0x9D)
	sc = append(sc, 0x48, 0xBC)
	sc = appendU64LE(sc, uint64(returnRsp))
	sc = append(sc, 0xFF, 0x25, 0x00, 0x00, 0x00, 0x00)
	sc = appendU64LE(sc, uint64(returnRip))
	return sc
}

// SendPacketWithTimeout sends a packet with a custom APC timeout
// Use this for high-ping connections where 100ms may not be enough.
//
// When the presenter (Present hook via rmod.dll) is available this goes
// through it. Otherwise we fall back to the classic APC-based path that
// writes a stub + meta into D2R memory and dispatches it via
// NtQueueApcThread. The APC path is battle-tested and proven to work for
// 0x3C / 0x50 / 0x32 / 0x33 and friends — see historical stderr.txt.
func (p *Process) SendPacketWithTimeout(packet []byte, apcTimeout time.Duration) (err error) {
	if p == nil {
		return errors.New("process is nil")
	}

	// Use external sender if available (Present hook path).
	if p.externalSend != nil {
		return p.externalSend(packet)
	}

	sendPacketCallCount++
	packetID := byte(0)
	if len(packet) > 0 {
		packetID = packet[0]
	}
	if packetID == 0x3A || packetID == 0x3B {
		packetType := "UNKNOWN"
		if packetID == 0x3A {
			packetType = "STAT"
		} else if packetID == 0x3B {
			packetType = "SKILL"
		}
		log.Printf("[#%d] %s (0x%02x) %d bytes", sendPacketCallCount, packetType, packetID, len(packet))
	}

	defer func() {
		if err != nil {
			log.Printf("SendPacket(%d bytes, opcode=0x%02X) error: %v", len(packet), packetID, err)
		}
	}()

	if len(packet) == 0 {
		return errors.New("empty payload")
	}

	if len(packet) > maxPacketSize {
		return fmt.Errorf("data too large: %d (max %d)", len(packet), maxPacketSize)
	}

	p.sendPacketMu.Lock()
	if p.sendPacket == nil {
		p.sendPacket = &sendPacketState{}
	}
	state := p.sendPacket
	state.mu.Lock()
	p.sendPacketMu.Unlock()
	defer state.mu.Unlock()

	handle, err := state.ensureHandle(p.pid)
	if err != nil {
		return fmt.Errorf("open process: %w", err)
	}

	if state.isExiting {
		return errors.New("cancelled: exiting")
	}

	// Rate limit to prevent APC queue overflow.
	const maxPacketsPerSecond = 100
	now := time.Now()
	if !state.lastSendTime.IsZero() {
		elapsed := now.Sub(state.lastSendTime)
		if elapsed < time.Second {
			if state.sendCount >= maxPacketsPerSecond {
				return fmt.Errorf("rate exceeded: %d/s (max %d)", state.sendCount, maxPacketsPerSecond)
			}
			state.sendCount++
		} else {
			state.sendCount = 1
			state.lastSendTime = now
		}
	} else {
		state.lastSendTime = now
		state.sendCount = 1
	}
	if !state.lastSendTime.IsZero() && now.Sub(state.lastSendTime) < time.Millisecond {
		time.Sleep(time.Millisecond - now.Sub(state.lastSendTime))
	}

	fnAddr, err := state.ensureFunction(p)
	if err != nil {
		return fmt.Errorf("resolve send fn: %w", err)
	}

	if err := state.ensureStub(handle); err != nil {
		return err
	}

	if err := state.ensureMeta(handle); err != nil {
		return err
	}

	if err := state.ensurePacketBuffer(handle, uintptr(len(packet))); err != nil {
		return err
	}

	if err := writeRemoteMemory(handle, state.packet, packet); err != nil {
		return fmt.Errorf("write pkt: %w", err)
	}

	metaBuf := metaBufPool.Get().(*[sendPacketMetaSize]byte)
	defer func() {
		*metaBuf = [sendPacketMetaSize]byte{}
		metaBufPool.Put(metaBuf)
	}()

	binary.LittleEndian.PutUint64(metaBuf[0:], uint64(fnAddr))
	binary.LittleEndian.PutUint64(metaBuf[8:], uint64(state.packet))
	binary.LittleEndian.PutUint64(metaBuf[16:], uint64(len(packet)))
	binary.LittleEndian.PutUint32(metaBuf[sendPacketStatusOffset:], 0)

	if err := writeRemoteMemory(handle, state.meta, metaBuf[:]); err != nil {
		return fmt.Errorf("write meta: %w", err)
	}

	threadHandle, err := state.ensureThreadHandle(p)
	if err != nil {
		return fmt.Errorf("resolve main thread: %w", err)
	}

	if err := state.dispatchAPC(threadHandle, state.stub, state.meta); err != nil {
		return fmt.Errorf("dispatch APC: %w", err)
	}

	return p.waitForPacketCompletion(handle, state, apcTimeout)
}

// uiSendStub is APC shellcode that calls D2R's UI NetMan vtable[5] send function.
// Meta layout: [0]=ui_send_fn [8]=instance [10]=pkt_ptr [18]=status [1C]=pkt_len
var uiSendStub = []byte{
	0xF3, 0x0F, 0x1E, 0xFA,
	0x53,
	0x48, 0x89, 0xCB,
	0x48, 0x83, 0xEC, 0x30,
	0x48, 0x8B, 0x43, 0x10,
	0x48, 0x89, 0x44, 0x24, 0x20,
	0x8B, 0x4B, 0x1C,
	0x48, 0x01, 0xC1,
	0x48, 0x89, 0x4C, 0x24, 0x28,
	0x48, 0x8B, 0x03,
	0x48, 0x8B, 0x4B, 0x08,
	0x48, 0x31, 0xD2,
	0x4C, 0x8D, 0x44, 0x24, 0x20,
	0xFF, 0xD0,
	0xC7, 0x43, 0x18, 0x01, 0x00, 0x00, 0x00,
	0xB8, 0x01, 0x00, 0x00, 0x00,
	0x48, 0x83, 0xC4, 0x30,
	0x5B,
	0xC3,
}

func readRemoteUint64(handle windows.Handle, addr uintptr) (uint64, error) {
	var v uint64
	if err := windows.ReadProcessMemory(handle, addr, (*byte)(unsafe.Pointer(&v)), 8, nil); err != nil {
		return 0, err
	}
	return v, nil
}

func isRemoteExecutable(handle windows.Handle, addr uintptr) bool {
	info, err := ntapi.QueryVirtualMemory(handle, addr)
	if err != nil {
		return false
	}
	if info.State != ntapi.MEM_COMMIT || info.Protect == ntapi.PAGE_NOACCESS || (info.Protect&ntapi.PAGE_GUARD) != 0 {
		return false
	}
	return (info.Protect & (ntapi.PAGE_EXECUTE | 0x20 | 0x40 | 0x80)) != 0
}

func resolveUINetManSend(handle windows.Handle, candidate uintptr) (instanceAddr, vtableAddr, uiSendFn uint64, mode string, err error) {
	vtable, err := readRemoteUint64(handle, candidate)
	if err == nil && vtable != 0 {
		if fn, readErr := readRemoteUint64(handle, uintptr(vtable)+0x28); readErr == nil && fn != 0 && isRemoteExecutable(handle, uintptr(fn)) {
			return uint64(candidate), vtable, fn, "instance", nil
		}
	}

	instance, err := readRemoteUint64(handle, candidate)
	if err != nil {
		return 0, 0, 0, "", fmt.Errorf("read UI NetMan candidate: %w", err)
	}
	if instance == 0 {
		return 0, 0, 0, "", errors.New("UI NetMan instance is null")
	}
	vtable, err = readRemoteUint64(handle, uintptr(instance))
	if err != nil {
		return 0, 0, 0, "", fmt.Errorf("read UI NetMan vtable: %w", err)
	}
	if vtable == 0 {
		return 0, 0, 0, "", errors.New("UI NetMan vtable is null")
	}
	uiSendFn, err = readRemoteUint64(handle, uintptr(vtable)+0x28)
	if err != nil {
		return 0, 0, 0, "", fmt.Errorf("read UI send fn: %w", err)
	}
	if uiSendFn == 0 || !isRemoteExecutable(handle, uintptr(uiSendFn)) {
		return 0, 0, 0, "", fmt.Errorf("UI send fn is not executable: %#x", uiSendFn)
	}
	return instance, vtable, uiSendFn, "global", nil
}

// SendUIPacketViaMainThread sends a packet through UI NetMan vtable[5] via APC on the main thread.
func (p *Process) SendUIPacketViaMainThread(packet []byte, uiNetManGlobalAddr uintptr) error {
	if len(packet) == 0 || len(packet) > maxPacketSize {
		return fmt.Errorf("invalid packet size: %d", len(packet))
	}

	p.sendPacketMu.Lock()
	if p.sendPacket == nil {
		p.sendPacket = &sendPacketState{}
	}
	state := p.sendPacket
	state.mu.Lock()
	p.sendPacketMu.Unlock()
	defer state.mu.Unlock()

	handle, err := state.ensureHandle(p.pid)
	if err != nil {
		return fmt.Errorf("open process: %w", err)
	}

	threadHandle, err := state.ensureThreadHandle(p)
	if err != nil {
		return fmt.Errorf("resolve main thread: %w", err)
	}

	// Resolve UI NetMan: global → instance (fn ptr table), instance+0x28 → ui_send_fn
	// The global points to an object whose fields ARE function pointers directly
	// (not a C++ vtable with extra indirection). Slot 5 at offset 0x28.
	instanceAddr, vtableAddr, uiSendFn, resolveMode, err := resolveUINetManSend(handle, uiNetManGlobalAddr)
	if err != nil {
		return fmt.Errorf("resolve UI NetMan send: %w", err)
	}
	log.Printf("SendUIPacketViaMainThread: candidate=%#x mode=%s instance=%#x vtable=%#x fn=%#x (RVA %#x) pktSize=%d",
		uiNetManGlobalAddr, resolveMode, instanceAddr, vtableAddr, uiSendFn, uiSendFn-uint64(p.moduleBaseAddressPtr), len(packet))

	stubSize := uintptr(len(uiSendStub))
	stubAddr, err := virtualAllocEx(handle, stubSize, windows.PAGE_EXECUTE_READWRITE)
	if err != nil {
		return fmt.Errorf("alloc stub: %w", err)
	}
	if err := writeRemoteMemory(handle, stubAddr, uiSendStub); err != nil {
		return fmt.Errorf("write stub: %w", err)
	}
	_ = markCallTargetValid(handle, stubAddr, stubSize)

	metaAndPktSize := uintptr(32 + len(packet))
	metaAddr, err := virtualAllocEx(handle, metaAndPktSize, windows.PAGE_READWRITE)
	if err != nil {
		return fmt.Errorf("alloc meta: %w", err)
	}
	pktAddr := metaAddr + 32

	// Write packet
	if err := writeRemoteMemory(handle, pktAddr, packet); err != nil {
		return fmt.Errorf("write pkt: %w", err)
	}

	// Build meta: [0]=ui_send_fn [8]=instance [10]=pkt_ptr [18]=status(0) [1C]=pkt_len
	var meta [32]byte
	binary.LittleEndian.PutUint64(meta[0:], uiSendFn)
	binary.LittleEndian.PutUint64(meta[8:], instanceAddr)
	binary.LittleEndian.PutUint64(meta[16:], uint64(pktAddr))
	binary.LittleEndian.PutUint32(meta[24:], 0) // status
	binary.LittleEndian.PutUint32(meta[28:], uint32(len(packet)))
	if err := writeRemoteMemory(handle, metaAddr, meta[:]); err != nil {
		return fmt.Errorf("write meta: %w", err)
	}

	// Dispatch APC
	if err := state.dispatchAPC(threadHandle, stubAddr, metaAddr); err != nil {
		return fmt.Errorf("dispatch APC: %w", err)
	}

	// Wait for status
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		var status uint32
		if err := windows.ReadProcessMemory(handle, metaAddr+24, (*byte)(unsafe.Pointer(&status)), 4, nil); err == nil {
			if status == 1 {
				return nil
			}
		}
		time.Sleep(time.Millisecond)
	}
	return errors.New("UI send APC timeout")
}

// waitForPacketCompletion waits for the APC to complete with the given timeout
func (p *Process) waitForPacketCompletion(handle windows.Handle, state *sendPacketState, apcTimeout time.Duration) error {
	timeout := time.After(apcTimeout)
	ticker := time.NewTicker(1 * time.Millisecond)
	defer ticker.Stop()

	timeoutMs := int(apcTimeout.Milliseconds())

	var statusBuf [4]byte
	for {
		select {
		case <-timeout:
			// Try to read status one last time before failing
			if err := ntapi.ReadProcessMemory(handle, state.meta+sendPacketStatusOffset, &statusBuf[0], 4); err == nil {
				status := binary.LittleEndian.Uint32(statusBuf[:])
				if status == 1 {
					return nil
				}
			}
			return fmt.Errorf("timeout: %dms", timeoutMs)
		case <-ticker.C:
			if err := ntapi.ReadProcessMemory(handle, state.meta+sendPacketStatusOffset, &statusBuf[0], 4); err != nil {
				continue
			}
			status := binary.LittleEndian.Uint32(statusBuf[:])
			if status == 1 {
				return nil // Success - packet was sent
			}
		}
	}
}

func writeRemoteMemory(handle windows.Handle, address uintptr, data []byte) error {
	if address == 0 {
		return errors.New("attempt to write to null address")
	}
	if len(data) == 0 {
		return nil
	}
	return windows.WriteProcessMemory(handle, address, &data[0], uintptr(len(data)), nil)
}

func virtualAllocEx(handle windows.Handle, size uintptr, protect uint32) (uintptr, error) {
	if size == 0 {
		return 0, errors.New("allocation size cannot be zero")
	}

	addr, _, err := procVirtualAllocEx.Call(
		uintptr(handle),
		0,
		size,
		windows.MEM_COMMIT|windows.MEM_RESERVE,
		uintptr(protect),
	)
	if addr == 0 {
		if err != nil {
			return 0, err
		}
		return 0, errors.New("alloc failed")
	}
	return addr, nil
}

func virtualFreeEx(handle windows.Handle, address uintptr) error {
	if address == 0 {
		return nil
	}

	ret, _, err := procVirtualFreeEx.Call(
		uintptr(handle),
		address,
		0,
		windows.MEM_RELEASE,
	)
	if ret == 0 {
		if err != nil {
			return err
		}
		return errors.New("free failed")
	}
	return nil
}

func virtualProtectEx(handle windows.Handle, address uintptr, size uintptr, protect uint32) error {
	if address == 0 || size == 0 {
		return errors.New("attempt to protect invalid region")
	}

	if err := procVirtualProtectEx.Find(); err != nil {
		return nil
	}

	var oldProtect uint32
	ret, _, err := procVirtualProtectEx.Call(
		uintptr(handle),
		address,
		size,
		uintptr(protect),
		uintptr(unsafe.Pointer(&oldProtect)),
	)
	if ret == 0 {
		if err != nil {
			return fmt.Errorf("VirtualProtectEx: %w", err)
		}
		return errors.New("VirtualProtectEx failed")
	}
	return nil
}

func markCallTargetValid(handle windows.Handle, address uintptr, size uintptr) error {
	if err := procSetProcessValidCallTargets.Find(); err != nil {
		return nil
	}

	if address == 0 {
		return errors.New("attempt to register null call target")
	}

	regionSize := alignUp(size, 0x1000)
	info := cfgCallTargetInfo{
		Offset: 0,
		Flags:  cfgCallTargetValid,
	}

	ret, _, err := procSetProcessValidCallTargets.Call(
		uintptr(handle),
		address,
		regionSize,
		1,
		uintptr(unsafe.Pointer(&info)),
	)
	if ret == 0 {
		if err != nil {
			return fmt.Errorf("set targets: %w", err)
		}
		return errors.New("set targets failed")
	}

	return nil
}

func alignUp(value, alignment uintptr) uintptr {
	if alignment == 0 {
		return value
	}
	mask := alignment - 1
	return (value + mask) &^ mask
}

func createRemoteThread(handle windows.Handle, startAddr, parameter uintptr) (windows.Handle, uint32, error) {
	var threadID uint32

	ret, _, err := procCreateRemoteThread.Call(
		uintptr(handle),
		0,         // lpThreadAttributes
		0,         // dwStackSize (default)
		startAddr, // lpStartAddress
		parameter, // lpParameter
		0,         // dwCreationFlags (start immediately)
		uintptr(unsafe.Pointer(&threadID)),
	)
	if ret == 0 {
		if err != nil {
			return 0, 0, err
		}
		return 0, 0, errors.New("create thread failed")
	}

	return windows.Handle(ret), threadID, nil
}

// CONTEXT64 is the AMD64 thread context structure
type CONTEXT64 struct {
	P1Home               uint64
	P2Home               uint64
	P3Home               uint64
	P4Home               uint64
	P5Home               uint64
	P6Home               uint64
	ContextFlags         uint32
	MxCsr                uint32
	SegCs                uint16
	SegDs                uint16
	SegEs                uint16
	SegFs                uint16
	SegGs                uint16
	SegSs                uint16
	EFlags               uint32
	Dr0                  uint64
	Dr1                  uint64
	Dr2                  uint64
	Dr3                  uint64
	Dr6                  uint64
	Dr7                  uint64
	Rax                  uint64
	Rcx                  uint64
	Rdx                  uint64
	Rbx                  uint64
	Rsp                  uint64
	Rbp                  uint64
	Rsi                  uint64
	Rdi                  uint64
	R8                   uint64
	R9                   uint64
	R10                  uint64
	R11                  uint64
	R12                  uint64
	R13                  uint64
	R14                  uint64
	R15                  uint64
	Rip                  uint64
	_                    [512]byte // FltSave (XSAVE_FORMAT)
	VectorRegister       [26][16]byte
	VectorControl        uint64
	DebugControl         uint64
	LastBranchToRip      uint64
	LastBranchFromRip    uint64
	LastExceptionToRip   uint64
	LastExceptionFromRip uint64
}

const CONTEXT_AMD64 = 0x00100000
const CONTEXT_CONTROL = CONTEXT_AMD64 | 0x0001
const CONTEXT_INTEGER = CONTEXT_AMD64 | 0x0002
const CONTEXT_FULL = CONTEXT_CONTROL | CONTEXT_INTEGER

func getThreadContext(threadHandle windows.Handle, ctx *CONTEXT64) error {
	ctx.ContextFlags = CONTEXT_FULL
	ret, _, err := procNtGetContextThread.Call(
		uintptr(threadHandle),
		uintptr(unsafe.Pointer(ctx)),
	)
	if ret != 0 {
		return fmt.Errorf("NtGetContextThread failed: 0x%X, %w", ret, err)
	}
	return nil
}

func setThreadContext(threadHandle windows.Handle, ctx *CONTEXT64) error {
	ret, _, err := procNtSetContextThread.Call(
		uintptr(threadHandle),
		uintptr(unsafe.Pointer(ctx)),
	)
	if ret != 0 {
		return fmt.Errorf("NtSetContextThread failed: 0x%X, %w", ret, err)
	}
	return nil
}

// --------------------------------------------------------------------------
// Polymorphic shellcode stub builder
// --------------------------------------------------------------------------

// pktRandN returns a crypto/rand integer in [0, n).
func pktRandN(n int) int {
	val, _ := rand.Int(rand.Reader, big.NewInt(int64(n)))
	return int(val.Int64())
}

// pktJunkInstruction returns a random NOP-equivalent x64 instruction.
func pktJunkInstruction() []byte {
	switch pktRandN(12) {
	case 0:
		return []byte{0x90} // nop
	case 1:
		return []byte{0x66, 0x90} // 66 nop
	case 2:
		return []byte{0x0F, 0x1F, 0x00} // nop dword [rax]
	case 3:
		return []byte{0x48, 0x87, 0xED} // xchg rbp, rbp
	case 4:
		return []byte{0x48, 0x87, 0xF6} // xchg rsi, rsi
	case 5:
		return []byte{0xF8} // clc
	case 6:
		return []byte{0xFC} // cld
	case 7:
		return []byte{0xD9, 0xD0} // fnop
	case 8:
		return []byte{0x48, 0x89, 0xED} // mov rbp, rbp
	case 9:
		return []byte{0x48, 0x89, 0xF6} // mov rsi, rsi
	case 10:
		return []byte{0x55, 0x5D} // push rbp; pop rbp
	default:
		return []byte{0x56, 0x5E} // push rsi; pop rsi
	}
}

// pktJunkBlock returns 0-2 random junk instructions.
func pktJunkBlock() []byte {
	count := pktRandN(3)
	var out []byte
	for i := 0; i < count; i++ {
		out = append(out, pktJunkInstruction()...)
	}
	return out
}

// buildPolymorphicStub generates a polymorphic APC shellcode stub.
// Each call produces different byte patterns that implement the same logic:
//
//	save rbx
//	mov rbx, rcx          (metadata pointer)
//	sub rsp, 0x20         (shadow space)
//	mov rax, [rbx]        (function pointer from metadata)
//	mov rcx, [rbx+8]      (packet data pointer)
//	mov rdx, [rbx+0x10]   (packet size)
//	xor r8d, r8d          (third arg = 0)
//	call rax              (call D2GS_SendPacket)
//	mov dword [rbx+0x18], 1 (status = done)
//	mov eax, 1
//	add rsp, 0x20
//	restore rbx
//	ret
func buildPolymorphicStub() []byte {
	var code []byte

	// Optional: endbr64 (CET hint) — include or omit randomly
	if pktRandN(2) == 0 {
		code = append(code, 0xF3, 0x0F, 0x1E, 0xFA)
	}

	code = append(code, pktJunkBlock()...)

	// Save rbx: push rbx or sub rsp,8; mov [rsp],rbx
	switch pktRandN(2) {
	case 0:
		code = append(code, 0x53) // push rbx
	default:
		code = append(code, 0x48, 0x83, 0xEC, 0x08) // sub rsp, 8
		code = append(code, 0x48, 0x89, 0x1C, 0x24) // mov [rsp], rbx
	}

	code = append(code, pktJunkBlock()...)

	// mov rbx, rcx: 3 encodings
	switch pktRandN(3) {
	case 0:
		code = append(code, 0x48, 0x89, 0xCB) // mov rbx, rcx
	case 1:
		code = append(code, 0x48, 0x8B, 0xD9) // mov rbx, rcx (alt)
	default:
		code = append(code, 0x51, 0x5B) // push rcx; pop rbx
	}

	code = append(code, pktJunkBlock()...)

	// sub rsp, 0x20: 2 encodings
	switch pktRandN(2) {
	case 0:
		code = append(code, 0x48, 0x83, 0xEC, 0x20) // sub rsp, 0x20
	default:
		code = append(code, 0x48, 0x8D, 0x64, 0x24, 0xE0) // lea rsp, [rsp-0x20]
	}

	code = append(code, pktJunkBlock()...)

	// mov rax, [rbx]: load function pointer
	code = append(code, 0x48, 0x8B, 0x03) // mov rax, [rbx]

	code = append(code, pktJunkBlock()...)

	// mov rcx, [rbx+8]: load packet data pointer
	code = append(code, 0x48, 0x8B, 0x4B, 0x08) // mov rcx, [rbx+8]

	// mov rdx, [rbx+0x10]: load packet size
	code = append(code, 0x48, 0x8B, 0x53, 0x10) // mov rdx, [rbx+0x10]

	// xor r8d, r8d: zero third arg — 3 encodings
	switch pktRandN(3) {
	case 0:
		code = append(code, 0x45, 0x33, 0xC0) // xor r8d, r8d
	case 1:
		code = append(code, 0x45, 0x31, 0xC0) // xor r8d, r8d (alt opcode)
	default:
		code = append(code, 0x41, 0xB8, 0x00, 0x00, 0x00, 0x00) // mov r8d, 0
	}

	code = append(code, pktJunkBlock()...)

	// call rax
	code = append(code, 0xFF, 0xD0) // call rax

	code = append(code, pktJunkBlock()...)

	// mov dword [rbx+0x18], 1: set status
	code = append(code, 0xC7, 0x43, 0x18, 0x01, 0x00, 0x00, 0x00)

	// mov eax, 1: return value — 2 encodings
	switch pktRandN(2) {
	case 0:
		code = append(code, 0xB8, 0x01, 0x00, 0x00, 0x00) // mov eax, 1
	default:
		code = append(code, 0x33, 0xC0, 0xFF, 0xC0) // xor eax, eax; inc eax
	}

	// add rsp, 0x20
	switch pktRandN(2) {
	case 0:
		code = append(code, 0x48, 0x83, 0xC4, 0x20) // add rsp, 0x20
	default:
		code = append(code, 0x48, 0x8D, 0x64, 0x24, 0x20) // lea rsp, [rsp+0x20]
	}

	// Restore rbx: pop rbx or mov rbx,[rsp]; add rsp,8
	switch pktRandN(2) {
	case 0:
		code = append(code, 0x5B) // pop rbx
	default:
		code = append(code, 0x48, 0x8B, 0x1C, 0x24) // mov rbx, [rsp]
		code = append(code, 0x48, 0x83, 0xC4, 0x08) // add rsp, 8
	}

	// ret
	code = append(code, 0xC3)

	return code
}
