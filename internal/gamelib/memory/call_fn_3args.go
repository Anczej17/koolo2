package memory

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// call_fn_3args.go — APC-based invocation of D2R internal functions that take
// 3 args in standard x64 fastcall (rcx, rdx, r8). Specifically built to call
// send_9b_wrapper (RVA 0x149400) with (opcode:u8, arg1:u32, arg2:u32).
//
// Decompiled signature (Ghidra 2026-04-20):
//
//	void* FUN_140149400(undefined1 param_1, undefined4 param_2, undefined4 param_3)
//
// D2R's internal wrapper builds the 9-byte packet from these args, adds the
// current transaction_id / session state that the server validates, then
// dispatches via dual_send_wrap. Writing bytes directly to the mirror buffer
// (SendDualPacket path) skips this state machine → server silently drops.
//
// Meta layout (40 bytes):
//   0x00: fn ptr (u64) — target function address
//   0x08: arg1 (u64)   — rcx
//   0x10: arg2 (u64)   — rdx
//   0x18: arg3 (u64)   — r8
//   0x20: status (u32) — set to 1 by stub on completion

const (
	callFn3StatusOffset = 0x20
	callFn3MetaSize     = 0x28 // 40 bytes
)

// Hardcoded stub — identical prologue to buildPolymorphicStub but loads r8
// from meta[0x18] instead of zeroing it. Status write shifted to [rbx+0x20].
// Not polymorphic — single-use path, low fingerprint anyway.
var callFn3ArgsStub = []byte{
	0xF3, 0x0F, 0x1E, 0xFA, // endbr64
	0x53,             // push rbx
	0x48, 0x89, 0xCB, // mov rbx, rcx              ; rcx = meta ptr
	0x48, 0x83, 0xEC, 0x20, // sub rsp, 0x20
	0x48, 0x8B, 0x03, // mov rax, [rbx]            ; fn ptr
	0x48, 0x8B, 0x4B, 0x08, // mov rcx, [rbx+0x08]       ; arg1
	0x48, 0x8B, 0x53, 0x10, // mov rdx, [rbx+0x10]       ; arg2
	0x4C, 0x8B, 0x43, 0x18, // mov r8,  [rbx+0x18]       ; arg3
	0xFF, 0xD0, // call rax
	0xC7, 0x43, 0x20, 0x01, 0x00, 0x00, 0x00, // mov dword [rbx+0x20], 1
	0xB8, 0x01, 0x00, 0x00, 0x00, // mov eax, 1
	0x48, 0x83, 0xC4, 0x20, // add rsp, 0x20
	0x5B, // pop rbx
	0xC3, // ret
}

type callFn3State struct {
	mu         sync.Mutex
	handle     windows.Handle
	stub       uintptr
	meta       uintptr
	thread     windows.Handle
	threadID   uint32
	processPID uint32
}

var (
	callFn3StateMap   = map[uint32]*callFn3State{}
	callFn3StateMapMu sync.Mutex
)

func getCallFn3State(pid uint32) *callFn3State {
	callFn3StateMapMu.Lock()
	defer callFn3StateMapMu.Unlock()
	s, ok := callFn3StateMap[pid]
	if !ok {
		s = &callFn3State{}
		callFn3StateMap[pid] = s
	}
	return s
}

// CallFn3Args invokes fn(arg1, arg2, arg3) in the D2R main thread via APC.
// Blocks up to timeout waiting for stub completion.
func (p *Process) CallFn3Args(fnAddr, arg1, arg2, arg3 uintptr) error {
	if p == nil {
		return errors.New("process is nil")
	}
	if p.pid == 0 {
		return errors.New("process pid is 0")
	}

	s := getCallFn3State(p.pid)
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureHandle(p.pid); err != nil {
		return fmt.Errorf("open process: %w", err)
	}
	if err := s.ensureStub(); err != nil {
		return err
	}
	if err := s.ensureMeta(); err != nil {
		return err
	}

	// Build meta
	var metaBuf [callFn3MetaSize]byte
	binary.LittleEndian.PutUint64(metaBuf[0x00:], uint64(fnAddr))
	binary.LittleEndian.PutUint64(metaBuf[0x08:], uint64(arg1))
	binary.LittleEndian.PutUint64(metaBuf[0x10:], uint64(arg2))
	binary.LittleEndian.PutUint64(metaBuf[0x18:], uint64(arg3))
	binary.LittleEndian.PutUint32(metaBuf[callFn3StatusOffset:], 0)
	if err := writeRemoteMemory(s.handle, s.meta, metaBuf[:]); err != nil {
		return fmt.Errorf("write meta: %w", err)
	}

	// Find main thread
	threadHandle, err := s.ensureThreadHandle(p)
	if err != nil {
		return fmt.Errorf("resolve main thread: %w", err)
	}

	// Dispatch APC: stub(rcx=meta)
	r, _, _ := procQueueUserAPC.Call(
		s.stub,
		uintptr(threadHandle),
		s.meta,
	)
	if r == 0 {
		return errors.New("QueueUserAPC failed")
	}

	// Wait for completion (status at meta+0x20 == 1)
	deadline := time.Now().Add(500 * time.Millisecond)
	var status uint32
	for time.Now().Before(deadline) {
		var buf [4]byte
		var bytesRead uintptr
		err := windows.ReadProcessMemory(s.handle, s.meta+callFn3StatusOffset, &buf[0], 4, &bytesRead)
		if err == nil && bytesRead == 4 {
			status = binary.LittleEndian.Uint32(buf[:])
			if status == 1 {
				return nil
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	return fmt.Errorf("CallFn3Args timeout (status=%d fn=%#x args=[%#x %#x %#x])", status, fnAddr, arg1, arg2, arg3)
}

func (s *callFn3State) ensureHandle(pid uint32) error {
	if s.handle != 0 && s.processPID == pid {
		return nil
	}
	if s.handle != 0 {
		windows.CloseHandle(s.handle)
		s.handle = 0
		s.stub = 0
		s.meta = 0
	}
	h, err := windows.OpenProcess(sendPacketProcessAccess, false, pid)
	if err != nil {
		return err
	}
	s.handle = h
	s.processPID = pid
	return nil
}

func (s *callFn3State) ensureStub() error {
	if s.stub != 0 {
		return nil
	}
	stubSize := uintptr(len(callFn3ArgsStub))
	addr, err := virtualAllocEx(s.handle, stubSize, windows.PAGE_EXECUTE_READWRITE)
	if err != nil {
		return fmt.Errorf("alloc stub: %w", err)
	}
	if err := writeRemoteMemory(s.handle, addr, callFn3ArgsStub); err != nil {
		virtualFreeEx(s.handle, addr)
		return err
	}
	if err := markCallTargetValid(s.handle, addr, stubSize); err != nil {
		virtualFreeEx(s.handle, addr)
		return err
	}
	if err := virtualProtectEx(s.handle, addr, stubSize, windows.PAGE_EXECUTE_READ); err != nil {
		virtualFreeEx(s.handle, addr)
		return err
	}
	s.stub = addr
	log.Printf("CallFn3Args: stub installed at 0x%X (size=%d)", addr, stubSize)
	return nil
}

func (s *callFn3State) ensureMeta() error {
	if s.meta != 0 {
		return nil
	}
	addr, err := virtualAllocEx(s.handle, callFn3MetaSize, windows.PAGE_READWRITE)
	if err != nil {
		return fmt.Errorf("alloc meta: %w", err)
	}
	s.meta = addr
	return nil
}

func (s *callFn3State) ensureThreadHandle(p *Process) (windows.Handle, error) {
	if s.thread != 0 {
		return s.thread, nil
	}
	tid, err := findMainThreadID(p.pid)
	if err != nil {
		return 0, err
	}
	h, err := windows.OpenThread(sendPacketThreadAccess|windows.THREAD_SET_CONTEXT, false, tid)
	if err != nil {
		return 0, err
	}
	s.thread = h
	s.threadID = tid
	return h, nil
}

const (
	callFn6RetOffset    = 0x40
	callFn6StatusOffset = 0x48
	callFn6MetaSize     = 0x50
)

// callFn6ArgsStub invokes a standard MS x64 ABI function with up to six
// integer/pointer arguments. Args 0..3 go in rcx/rdx/r8/r9; args 4..5 are
// written into the caller shadow stack at rsp+0x20/rsp+0x28.
var callFn6ArgsStub = []byte{
	0xF3, 0x0F, 0x1E, 0xFA, // endbr64
	0x53,             // push rbx
	0x48, 0x89, 0xCB, // mov rbx, rcx              ; rcx = meta ptr
	0x48, 0x83, 0xEC, 0x38, // sub rsp, 0x38          ; shadow + stack args + align
	0x48, 0x8B, 0x03, // mov rax, [rbx]            ; fn ptr
	0x48, 0x8B, 0x4B, 0x28, // mov rcx, [rbx+0x28]     ; arg4
	0x48, 0x89, 0x4C, 0x24, 0x20, // mov [rsp+0x20], rcx
	0x48, 0x8B, 0x4B, 0x30, // mov rcx, [rbx+0x30]     ; arg5
	0x48, 0x89, 0x4C, 0x24, 0x28, // mov [rsp+0x28], rcx
	0x48, 0x8B, 0x4B, 0x08, // mov rcx, [rbx+0x08]     ; arg0
	0x48, 0x8B, 0x53, 0x10, // mov rdx, [rbx+0x10]     ; arg1
	0x4C, 0x8B, 0x43, 0x18, // mov r8,  [rbx+0x18]     ; arg2
	0x4C, 0x8B, 0x4B, 0x20, // mov r9,  [rbx+0x20]     ; arg3
	0xFF, 0xD0, // call rax
	0x48, 0x89, 0x43, 0x40, // mov [rbx+0x40], rax     ; ret
	0xC7, 0x43, 0x48, 0x01, 0x00, 0x00, 0x00, // mov dword [rbx+0x48], 1
	0xB8, 0x01, 0x00, 0x00, 0x00, // mov eax, 1
	0x48, 0x83, 0xC4, 0x38, // add rsp, 0x38
	0x5B, // pop rbx
	0xC3, // ret
}

type callFn6State struct {
	mu         sync.Mutex
	handle     windows.Handle
	stub       uintptr
	meta       uintptr
	thread     windows.Handle
	threadID   uint32
	processPID uint32
}

var (
	callFn6StateMap   = map[uint32]*callFn6State{}
	callFn6StateMapMu sync.Mutex
)

func getCallFn6State(pid uint32) *callFn6State {
	callFn6StateMapMu.Lock()
	defer callFn6StateMapMu.Unlock()
	s, ok := callFn6StateMap[pid]
	if !ok {
		s = &callFn6State{}
		callFn6StateMap[pid] = s
	}
	return s
}

// CallFn6Args invokes fn(arg0..arg5) in D2R's main thread via APC and returns
// rax. This is for native handlers whose ABI needs stack args; it deliberately
// does not use rmod's CmdCallFn because Claude/normal packet mode can run with
// the Present hook disabled.
func (p *Process) CallFn6Args(fnAddr uintptr, args ...uintptr) (uint64, error) {
	if p == nil {
		return 0, errors.New("process is nil")
	}
	if p.pid == 0 {
		return 0, errors.New("process pid is 0")
	}
	if fnAddr == 0 {
		return 0, errors.New("function address is zero")
	}
	if len(args) > 6 {
		return 0, fmt.Errorf("too many args: %d", len(args))
	}
	var argv [6]uintptr
	copy(argv[:], args)

	s := getCallFn6State(p.pid)
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureHandle(p.pid); err != nil {
		return 0, fmt.Errorf("open process: %w", err)
	}
	if err := s.ensureStub(); err != nil {
		return 0, err
	}
	if err := s.ensureMeta(); err != nil {
		return 0, err
	}

	var metaBuf [callFn6MetaSize]byte
	binary.LittleEndian.PutUint64(metaBuf[0x00:], uint64(fnAddr))
	for i, a := range argv {
		binary.LittleEndian.PutUint64(metaBuf[0x08+i*8:], uint64(a))
	}
	binary.LittleEndian.PutUint64(metaBuf[callFn6RetOffset:], 0)
	binary.LittleEndian.PutUint32(metaBuf[callFn6StatusOffset:], 0)
	if err := writeRemoteMemory(s.handle, s.meta, metaBuf[:]); err != nil {
		return 0, fmt.Errorf("write meta: %w", err)
	}

	threadHandle, err := s.ensureThreadHandle(p)
	if err != nil {
		return 0, fmt.Errorf("resolve main thread: %w", err)
	}
	r, _, _ := procQueueUserAPC.Call(s.stub, uintptr(threadHandle), s.meta)
	if r == 0 {
		return 0, errors.New("QueueUserAPC failed")
	}

	deadline := time.Now().Add(1500 * time.Millisecond)
	var status uint32
	for time.Now().Before(deadline) {
		var statusBuf [4]byte
		var bytesRead uintptr
		err := windows.ReadProcessMemory(s.handle, s.meta+callFn6StatusOffset, &statusBuf[0], 4, &bytesRead)
		if err == nil && bytesRead == 4 {
			status = binary.LittleEndian.Uint32(statusBuf[:])
			if status == 1 {
				var retBuf [8]byte
				err = windows.ReadProcessMemory(s.handle, s.meta+callFn6RetOffset, &retBuf[0], 8, &bytesRead)
				if err != nil || bytesRead != 8 {
					return 0, fmt.Errorf("read return: %w", err)
				}
				return binary.LittleEndian.Uint64(retBuf[:]), nil
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	return 0, fmt.Errorf("CallFn6Args timeout (status=%d fn=%#x args=[%#x %#x %#x %#x %#x %#x])",
		status, fnAddr, argv[0], argv[1], argv[2], argv[3], argv[4], argv[5])
}

func (s *callFn6State) ensureHandle(pid uint32) error {
	if s.handle != 0 && s.processPID == pid {
		return nil
	}
	if s.handle != 0 {
		windows.CloseHandle(s.handle)
		s.handle = 0
		s.stub = 0
		s.meta = 0
		s.thread = 0
	}
	h, err := windows.OpenProcess(sendPacketProcessAccess, false, pid)
	if err != nil {
		return err
	}
	s.handle = h
	s.processPID = pid
	return nil
}

func (s *callFn6State) ensureStub() error {
	if s.stub != 0 {
		return nil
	}
	stubSize := uintptr(len(callFn6ArgsStub))
	addr, err := virtualAllocEx(s.handle, stubSize, windows.PAGE_EXECUTE_READWRITE)
	if err != nil {
		return fmt.Errorf("alloc callfn6 stub: %w", err)
	}
	if err := writeRemoteMemory(s.handle, addr, callFn6ArgsStub); err != nil {
		virtualFreeEx(s.handle, addr)
		return err
	}
	if err := markCallTargetValid(s.handle, addr, stubSize); err != nil {
		virtualFreeEx(s.handle, addr)
		return err
	}
	if err := virtualProtectEx(s.handle, addr, stubSize, windows.PAGE_EXECUTE_READ); err != nil {
		virtualFreeEx(s.handle, addr)
		return err
	}
	s.stub = addr
	log.Printf("CallFn6Args: stub installed at 0x%X (size=%d)", addr, stubSize)
	return nil
}

func (s *callFn6State) ensureMeta() error {
	if s.meta != 0 {
		return nil
	}
	addr, err := virtualAllocEx(s.handle, callFn6MetaSize, windows.PAGE_READWRITE)
	if err != nil {
		return fmt.Errorf("alloc callfn6 meta: %w", err)
	}
	s.meta = addr
	return nil
}

func (s *callFn6State) ensureThreadHandle(p *Process) (windows.Handle, error) {
	if s.thread != 0 {
		return s.thread, nil
	}
	tid, err := findMainThreadID(p.pid)
	if err != nil {
		return 0, err
	}
	h, err := windows.OpenThread(sendPacketThreadAccess|windows.THREAD_SET_CONTEXT, false, tid)
	if err != nil {
		return 0, err
	}
	s.thread = h
	s.threadID = tid
	return h, nil
}

// Send9BWrapper calls D2R's internal send_9b_wrapper (FUN_140149400) with
// 3 args: opcode (u8), arg1 (u32), arg2 (u32). D2R's wrapper builds the
// 9-byte packet with proper transaction_id + session state, then dispatches
// via dual_send_wrap. Server accepts because state machine is valid.
//
// Used for 0x38 NPCDialogOption where (arg1, arg2) = (option, npcGID).
const send9BWrapperRVA uintptr = 0x149400

func (p *Process) Send9BWrapper(opcode uint8, arg1, arg2 uint32) error {
	if p.moduleBaseAddressPtr == 0 {
		return errors.New("Send9BWrapper: module base not resolved")
	}
	fn := p.moduleBaseAddressPtr + send9BWrapperRVA
	log.Printf("Send9BWrapper: fn=0x%X opcode=0x%02X arg1=0x%X arg2=0x%X", fn, opcode, arg1, arg2)
	p.sendPacketMu.Lock()
	callGT := p.externalCallFnGT
	p.sendPacketMu.Unlock()
	if callGT != nil {
		_, err := callGT(fn, uintptr(opcode), uintptr(arg1), uintptr(arg2))
		if err != nil {
			return fmt.Errorf("Send9BWrapper game-thread call failed: %w", err)
		}
		return nil
	}
	return p.CallFn3Args(fn, uintptr(opcode), uintptr(arg1), uintptr(arg2))
}

const (
	npcDialogOption1HandlerRVA uintptr = 0x1745E0
	npcDialogOption2HandlerRVA uintptr = 0x1746D0
	npcDialogCancelHandlerRVA  uintptr = 0x17F350
)

// CallNPCDialogOption invokes D2R's native dialog-option handler after NPCInit
// has opened the NPC dialog. These handlers are the native callers of
// send_9b_wrapper(0x38, action, currentNPCGID); they also update the local UI
// state that a raw 0x38 send does not touch.
func (p *Process) CallNPCDialogOption(actionID uint32) error {
	if p == nil {
		return errors.New("process is nil")
	}
	if p.moduleBaseAddressPtr == 0 {
		return errors.New("CallNPCDialogOption: module base not resolved")
	}

	var rva uintptr
	switch actionID {
	case 1:
		rva = npcDialogOption1HandlerRVA
	case 2:
		rva = npcDialogOption2HandlerRVA
	default:
		return fmt.Errorf("CallNPCDialogOption: unsupported AMB action %d", actionID)
	}

	fn := p.moduleBaseAddressPtr + rva
	log.Printf("CallNPCDialogOption: fn=0x%X action=%d", fn, actionID)
	return p.CallFn3Args(fn, 0, 0, 0)
}

// CallNPCCancel invokes the native NPC close path. The function takes the NPC
// unit id in ECX, clears local dialog/shop state, sends AMB 0x30 through the
// native 5-byte wrapper, then refreshes the UI panels.
func (p *Process) CallNPCCancel(npcID uint32) error {
	if p == nil {
		return errors.New("process is nil")
	}
	if p.moduleBaseAddressPtr == 0 {
		return errors.New("CallNPCCancel: module base not resolved")
	}
	fn := p.moduleBaseAddressPtr + npcDialogCancelHandlerRVA
	log.Printf("CallNPCCancel: fn=0x%X npc=0x%X", fn, npcID)
	return p.CallFn3Args(fn, uintptr(npcID), 0, 0)
}

// Shadow declaration to keep compilation alignment
var _ = unsafe.Sizeof(uintptr(0))
