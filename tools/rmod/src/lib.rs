#![no_std]
#![allow(non_snake_case)]
#![allow(non_camel_case_types)]
#![allow(dead_code)]
#![allow(clippy::missing_safety_doc)]

//! Runtime graphics module - frame-synchronized command dispatch via shared memory.
//! no_std — zero CRT dependency, works with manual PE mapping.

use core::sync::atomic::{AtomicU32, Ordering};
use core::panic::PanicInfo;

/// Minimal x86-64 encoder/decoder for GID-style AssembleInsteadOfBytes
/// Present hook + ROP chain builder. no_std, handcrafted, no external crate.
/// See `src/asm.rs` for the full API + tests.
pub mod asm;

/// In-process RWX memory manager (AllocatedMemory RAII, alloc_near for
/// ±2 GB placements). See `src/alloc_mgr.rs`.
pub mod alloc_mgr;

/// In-process execution engine (GID's Executor class). See `src/executor.rs`.
pub mod executor;

/// ROP gadget harvester (scans D2R .text for ret-ending useful sequences).
/// See `src/rop_gadgets.rs`.
pub mod rop_gadgets;

/// ROP chain builder (assembles gadget chains for memcpy/call/write).
/// See `src/rop_chain.rs`.
pub mod rop_chain;

#[panic_handler]
fn panic(_: &PanicInfo) -> ! {
    loop {}
}

// ---------------------------------------------------------------------------
// Windows type aliases
// ---------------------------------------------------------------------------

type HANDLE = *mut core::ffi::c_void;
type HWND = *mut core::ffi::c_void;
type HINSTANCE = *mut core::ffi::c_void;
type HMODULE = *mut core::ffi::c_void;
type HRESULT = i32;
type BOOL = i32;
type UINT = u32;
type WPARAM = usize;
type LPARAM = isize;
type LRESULT = isize;
type DWORD = u32;
type ATOM = u16;
type WNDPROC = Option<unsafe extern "system" fn(HWND, UINT, WPARAM, LPARAM) -> LRESULT>;

const TRUE: BOOL = 1;
const FALSE: BOOL = 0;
const DLL_PROCESS_ATTACH: DWORD = 1;
const DLL_PROCESS_DETACH: DWORD = 0;

// Memory constants
const MEM_COMMIT: DWORD = 0x1000;
const MEM_RESERVE: DWORD = 0x2000;
const PAGE_EXECUTE_READWRITE: DWORD = 0x40;
const FILE_MAP_ALL_ACCESS: DWORD = 0xF001F;

// Window styles
const WS_OVERLAPPEDWINDOW: DWORD = 0x00CF0000;

// ---------------------------------------------------------------------------
// Windows structs
// ---------------------------------------------------------------------------

#[repr(C)]
struct WNDCLASSEXW {
    cbSize: UINT,
    style: UINT,
    lpfnWndProc: WNDPROC,
    cbClsExtra: i32,
    cbWndExtra: i32,
    hInstance: HINSTANCE,
    hIcon: HANDLE,
    hCursor: HANDLE,
    hbrBackground: HANDLE,
    lpszMenuName: *const u16,
    lpszClassName: *const u16,
    hIconSm: HANDLE,
}

// ---------------------------------------------------------------------------
// Windows FFI imports
// ---------------------------------------------------------------------------

#[link(name = "kernel32")]
extern "system" {
    fn GetCurrentProcessId() -> DWORD;
    fn GetCurrentProcess() -> HANDLE;
    fn GetLastError() -> DWORD;
    fn GetModuleHandleA(lpModuleName: *const u8) -> HMODULE;
    fn GetModuleHandleW(lpModuleName: *const u16) -> HMODULE;
    fn LoadLibraryW(lpLibFileName: *const u16) -> HMODULE;
    fn FreeLibrary(hLibModule: HMODULE) -> BOOL;
    fn GetProcAddress(hModule: HMODULE, lpProcName: *const u8) -> *const core::ffi::c_void;
    fn DisableThreadLibraryCalls(hLibModule: HINSTANCE) -> BOOL;
    fn CreateThread(
        lpThreadAttributes: *const core::ffi::c_void,
        dwStackSize: usize,
        lpStartAddress: unsafe extern "system" fn(*mut core::ffi::c_void) -> DWORD,
        lpParameter: *mut core::ffi::c_void,
        dwCreationFlags: DWORD,
        lpThreadId: *mut DWORD,
    ) -> HANDLE;
    fn Sleep(dwMilliseconds: DWORD);
    fn CloseHandle(hObject: HANDLE) -> BOOL;
    fn OpenThread(dwDesiredAccess: DWORD, bInheritHandle: BOOL, dwThreadId: DWORD) -> HANDLE;
    fn GetCurrentThreadId() -> DWORD;
    fn CreateToolhelp32Snapshot(dwFlags: DWORD, th32ProcessID: DWORD) -> HANDLE;
    fn Thread32First(hSnapshot: HANDLE, lpte: *mut THREADENTRY32) -> BOOL;
    fn Thread32Next(hSnapshot: HANDLE, lpte: *mut THREADENTRY32) -> BOOL;
    fn SuspendThread(hThread: HANDLE) -> DWORD;
    fn ResumeThread(hThread: HANDLE) -> DWORD;
    fn GetThreadContext(hThread: HANDLE, lpContext: *mut CONTEXT) -> BOOL;
    fn SetThreadContext(hThread: HANDLE, lpContext: *const CONTEXT) -> BOOL;
    fn WaitForSingleObject(hHandle: HANDLE, dwMilliseconds: DWORD) -> DWORD;
    fn VirtualAlloc(
        lpAddress: *const core::ffi::c_void,
        dwSize: usize,
        flAllocationType: DWORD,
        flProtect: DWORD,
    ) -> *mut core::ffi::c_void;
    fn VirtualFree(lpAddress: *mut core::ffi::c_void, dwSize: usize, dwFreeType: DWORD) -> BOOL;
    fn VirtualProtect(
        lpAddress: *const core::ffi::c_void,
        dwSize: usize,
        flNewProtect: DWORD,
        lpflOldProtect: *mut DWORD,
    ) -> BOOL;
    fn VirtualQuery(
        lpAddress: *const core::ffi::c_void,
        lpBuffer: *mut core::ffi::c_void,
        dwLength: usize,
    ) -> usize;
    fn FlushInstructionCache(
        hProcess: HANDLE,
        lpBaseAddress: *const core::ffi::c_void,
        dwSize: usize,
    ) -> BOOL;
    fn OpenFileMappingW(
        dwDesiredAccess: DWORD,
        bInheritHandle: BOOL,
        lpName: *const u16,
    ) -> HANDLE;
    fn MapViewOfFile(
        hFileMappingObject: HANDLE,
        dwDesiredAccess: DWORD,
        dwFileOffsetHigh: DWORD,
        dwFileOffsetLow: DWORD,
        dwNumberOfBytesToMap: usize,
    ) -> *mut core::ffi::c_void;
}

#[link(name = "ntdll")]
extern "system" {
    fn NtQueryInformationThread(
        ThreadHandle: HANDLE,
        ThreadInformationClass: u32,
        ThreadInformation: *mut core::ffi::c_void,
        ThreadInformationLength: u32,
        ReturnLength: *mut u32,
    ) -> i32;
    fn NtQueueApcThread(
        ThreadHandle: HANDLE,
        ApcRoutine: *const core::ffi::c_void,
        ApcArgument1: *mut core::ffi::c_void,
        ApcArgument2: *mut core::ffi::c_void,
        ApcArgument3: *mut core::ffi::c_void,
    ) -> i32; // NTSTATUS
    fn NtQueryVirtualMemory(
        ProcessHandle: HANDLE,
        BaseAddress: *const core::ffi::c_void,
        MemoryInformationClass: u32,
        MemoryInformation: *mut core::ffi::c_void,
        MemoryInformationLength: usize,
        ReturnLength: *mut usize,
    ) -> i32;
}

/// MEMORY_BASIC_INFORMATION (subset — we only read State/Protect).
#[repr(C)]
struct MemoryBasicInformation {
    base_address:       *const core::ffi::c_void,
    allocation_base:    *const core::ffi::c_void,
    allocation_protect: u32,
    partition_id:       u16, // x64 — actually 16 padding then RegionSize
    _pad:               u16,
    region_size:        usize,
    state:              u32, // MEM_COMMIT/MEM_FREE/MEM_RESERVE
    protect:            u32,
    type_:              u32,
}

const PAGE_NOACCESS: u32 = 0x01;
const PAGE_GUARD:    u32 = 0x100;

#[link(name = "user32")]
extern "system" {
    fn GetWindowThreadProcessId(hWnd: HWND, lpdwProcessId: *mut DWORD) -> DWORD;
    fn RegisterClassExW(lpWndClass: *const WNDCLASSEXW) -> ATOM;
    fn UnregisterClassW(lpClassName: *const u16, hInstance: HINSTANCE) -> BOOL;
    fn CreateWindowExW(
        dwExStyle: DWORD,
        lpClassName: *const u16,
        lpWindowName: *const u16,
        dwStyle: DWORD,
        x: i32,
        y: i32,
        nWidth: i32,
        nHeight: i32,
        hWndParent: HWND,
        hMenu: HANDLE,
        hInstance: HINSTANCE,
        lpParam: *const core::ffi::c_void,
    ) -> HWND;
    fn DestroyWindow(hWnd: HWND) -> BOOL;
    fn DefWindowProcW(hWnd: HWND, Msg: UINT, wParam: WPARAM, lParam: LPARAM) -> LRESULT;
    fn PostMessageW(hWnd: HWND, Msg: u32, wParam: usize, lParam: usize) -> BOOL;
    fn MapVirtualKeyW(uCode: u32, uMapType: u32) -> u32;
    fn AddVectoredExceptionHandler(
        First: u32,
        Handler: unsafe extern "system" fn(*mut EXCEPTION_POINTERS) -> i32,
    ) -> *mut core::ffi::c_void;
    fn RemoveVectoredExceptionHandler(Handle: *mut core::ffi::c_void) -> u32;
}

#[repr(C)]
struct EXCEPTION_RECORD {
    ExceptionCode: u32,
    ExceptionFlags: u32,
    ExceptionRecord: *mut EXCEPTION_RECORD,
    ExceptionAddress: *mut core::ffi::c_void,
    NumberParameters: u32,
    ExceptionInformation: [u64; 15],
}

#[repr(C)]
struct EXCEPTION_POINTERS {
    ExceptionRecord: *mut EXCEPTION_RECORD,
    ContextRecord: *mut core::ffi::c_void, // CONTEXT*
}

// ---------------------------------------------------------------------------
// CONTEXT structure for thread hijacking (x64, minimal)
// ---------------------------------------------------------------------------

// CONTEXT on x64 is 1232 bytes. We use a raw buffer and access Rip at offset 0xF8.
// CONTEXT_CONTROL = 0x100001.
// Opaque type for FFI — actual data is in RawContext.
type CONTEXT = core::ffi::c_void;
#[repr(C, align(16))]
struct RawContext {
    data: [u8; 1232], // sizeof(CONTEXT) on x64
}

// no_std requires providing memset for large array init
#[no_mangle]
pub unsafe extern "C" fn memset(dest: *mut u8, val: i32, count: usize) -> *mut u8 {
    let mut i = 0;
    while i < count {
        *dest.add(i) = val as u8;
        i += 1;
    }
    dest
}

// no_std requires providing memcpy — Rust's copy_nonoverlapping lowers to this
// for sizes above the inline threshold. Non-overlapping byte copy, forward.
#[no_mangle]
pub unsafe extern "C" fn memcpy(dest: *mut u8, src: *const u8, count: usize) -> *mut u8 {
    let mut i = 0;
    while i < count {
        *dest.add(i) = *src.add(i);
        i += 1;
    }
    dest
}

// memmove — overlap-safe byte copy. Required by LLVM for some array copies.
#[no_mangle]
pub unsafe extern "C" fn memmove(dest: *mut u8, src: *const u8, count: usize) -> *mut u8 {
    if (dest as usize) < (src as usize) {
        let mut i = 0;
        while i < count {
            *dest.add(i) = *src.add(i);
            i += 1;
        }
    } else {
        let mut i = count;
        while i > 0 {
            i -= 1;
            *dest.add(i) = *src.add(i);
        }
    }
    dest
}

impl RawContext {
    fn new() -> Self {
        let mut c = RawContext { data: [0u8; 1232] };
        // Set ContextFlags at offset 0x30
        let flags: u32 = 0x100001; // CONTEXT_CONTROL
        unsafe {
            core::ptr::copy_nonoverlapping(
                &flags as *const u32 as *const u8,
                c.data.as_mut_ptr().add(0x30),
                4,
            );
        }
        c
    }

    fn rip(&self) -> u64 {
        u64::from_le_bytes([
            self.data[0xF8], self.data[0xF9], self.data[0xFA], self.data[0xFB],
            self.data[0xFC], self.data[0xFD], self.data[0xFE], self.data[0xFF],
        ])
    }

    fn set_rip(&mut self, rip: u64) {
        let bytes = rip.to_le_bytes();
        self.data[0xF8..0x100].copy_from_slice(&bytes);
    }

    fn reg64(&self, off: usize) -> u64 {
        u64::from_le_bytes(self.data[off..off+8].try_into().unwrap())
    }
    fn set_reg64(&mut self, off: usize, val: u64) {
        self.data[off..off+8].copy_from_slice(&val.to_le_bytes());
    }

    fn rax(&self) -> u64 { self.reg64(0x78) }
    fn rcx(&self) -> u64 { self.reg64(0x80) }
    fn rdx(&self) -> u64 { self.reg64(0x88) }
    fn rsp(&self) -> u64 { self.reg64(0x98) }
    fn set_rsp(&mut self, v: u64) { self.set_reg64(0x98, v); }

    fn set_context_flags(&mut self, flags: u32) {
        self.data[0x30..0x34].copy_from_slice(&flags.to_le_bytes());
    }
}

// ---------------------------------------------------------------------------
// Application Constants
// ---------------------------------------------------------------------------

// Sentinel used to validate shared memory mapping. Must match
// internal/presenter/protocol.go::SharedMagic on the Go side. Previously
// 0x524D_4F44 ("RMOD" / "DOMR" in .rdata), which was a 4-byte static
// signature. Replaced with an arbitrary constant that doesn't spell
// anything and looks like a Windows handle cookie.
const MAGIC: u32 = 0x7E1A_03D4;
const VERSION: u32 = 1;

const CMD_NOP: u32 = 0;
const CMD_SEND_PACKET: u32 = 1;
const CMD_SEND_UI_PACKET: u32 = 9;  // vtable[5] via UI NetMan (buy/identify/cube/gamble)
const CMD_SEND_DUAL: u32 = 11;      // memcpy mirror buf + send_fn (sell/trade dual-send)
const CMD_CALL_FN: u32 = 13;    // call D2R function with up to 4 args (render thread — deadlocks on game-logic fns)
const CMD_WRITE_MEM: u32 = 14;  // write bytes to D2R memory
const CMD_CALL_FN_GT: u32 = 15; // call D2R function on GAME THREAD via APC (no deadlock)
const CMD_SEND_DUAL_GT: u32 = 16; // send packet via dual_send_wrap FROM GAME THREAD (GetTickCount64 hook)
const CMD_PACKET_TRACE_INSTALL: u32 = 17;   // install trampoline JMP on send_fn + dual_send_wrap
const CMD_PACKET_TRACE_UNINSTALL: u32 = 18; // restore original bytes
const CMD_DR_PROBE: u32 = 22;               // probe whether SetThreadContext persists DR0 on D2R threads (Arxan diagnostic)
const CMD_SNAPSHOT_INIT: u32 = 24;          // Phase A P1-GID: enable per-Present PlayerUnit snapshot into SHM
const CMD_UNINSTALL_DETOUR: u32 = 25;       // Graceful shutdown: restore Present prologue before app.exe exits
const CMD_ROP_SCAN: u32 = 26;               // GID-4: scan .text region for ROP gadgets, populate G_ROP_GADGETS
const CMD_ROP_READ: u32 = 27;               // GID-5: execute build_memcpy ROP chain — D2R reads own memory via its own gadgets

// ROP command SHM layout (u64 args, u32 status) — placed in the 0x3000 free
// band between HWBP (0x2000-0x2100) and snapshot header (0x4000).
const OFF_ROP_SCAN_BASE:    usize = 0x3000;  // u64 — scan region base VA
const OFF_ROP_SCAN_LEN:     usize = 0x3008;  // u64 — scan region length
const OFF_ROP_SCAN_COUNT:   usize = 0x3010;  // u32 — out: gadgets harvested
const OFF_ROP_READ_SRC:     usize = 0x3018;  // u64 — D2R VA to read from
const OFF_ROP_READ_DST:     usize = 0x3020;  // u64 — SHM scratch VA to write into
const OFF_ROP_READ_LEN:     usize = 0x3028;  // u64 — bytes to copy
const OFF_ROP_READ_STATUS:  usize = 0x3030;  // u32 — out: 0=ok, 1=gadget-pool-missing, 2=exec-failed
const OFF_ROP_READY:        usize = 0x3034;  // u32 — 1 when G_ROP_EXECUTOR/G_ROP_STACK/G_ROP_TRIGGER ready post-scan
const OFF_ROP_DBG:          usize = 0x3038;  // u32 — step marker (0xAAAA00xx); Go reads on timeout to see where handler got stuck
const OFF_ROP_KIND_COUNTS:  usize = 0x303C;  // u32[8] — GadgetKind breakdown: [Unknown,PopReg,MovRegMem,MovMemReg,RepMovsb,RepMovsq,XchgReg,Ret]
const OFF_ROP_POPREG_MASK:  usize = 0x305C;  // u16 — bitmask of popable regs in pool (bit0=rax..bit15=r15)

// HWBP commands — match Go protocol.go (CmdHwbpInstall=6 etc).
const CMD_HWBP_INSTALL:   u32 = 6;          // install DR0=target on every D2R thread
const CMD_HWBP_UNINSTALL: u32 = 7;          // clear DR0/DR7 on every D2R thread
const CMD_HWBP_VERIFY:    u32 = 8;          // re-enum and count threads where DR0 still == target
const CMD_HWBP_REENUM:    u32 = 23;         // install on threads created since last install (idempotent)

const STATUS_DONE: u32 = 1;
const STATUS_ERROR: u32 = 2;
const STATUS_BUSY: u32 = 3; // pending, waiting for game thread tick

/// Size of the inline detour (absolute indirect JMP: FF 25 00 00 00 00 + 8-byte addr).
const DETOUR_SIZE: usize = 14;

#[repr(C)]
struct THREADENTRY32 {
    dwSize: DWORD,
    cntUsage: DWORD,
    th32ThreadID: DWORD,
    th32OwnerProcessID: DWORD,
    tpBasePri: i32,
    tpDeltaPri: i32,
    dwFlags: DWORD,
}

// ---------------------------------------------------------------------------
// DXGI / D3D11 types (manually defined)
// ---------------------------------------------------------------------------

#[repr(C)]
struct DXGI_RATIONAL {
    Numerator: u32,
    Denominator: u32,
}

#[repr(C)]
struct DXGI_MODE_DESC {
    Width: u32,
    Height: u32,
    RefreshRate: DXGI_RATIONAL,
    Format: u32,
    ScanlineOrdering: u32,
    Scaling: u32,
}

#[repr(C)]
struct DXGI_SAMPLE_DESC {
    Count: u32,
    Quality: u32,
}

#[repr(C)]
struct DXGI_SWAP_CHAIN_DESC {
    BufferDesc: DXGI_MODE_DESC,
    SampleDesc: DXGI_SAMPLE_DESC,
    BufferUsage: u32,
    BufferCount: u32,
    OutputWindow: HWND,
    Windowed: BOOL,
    SwapEffect: u32,
    Flags: u32,
}

const DXGI_FORMAT_R8G8B8A8_UNORM: u32 = 28;
const DXGI_USAGE_RENDER_TARGET_OUTPUT: u32 = 0x20;
const DXGI_SWAP_EFFECT_DISCARD: u32 = 0;
const D3D_DRIVER_TYPE_HARDWARE: u32 = 1;
const D3D11_SDK_VERSION: u32 = 7;

/// Signature for D3D11CreateDeviceAndSwapChain resolved at runtime.
type FnD3D11Create = unsafe extern "system" fn(
    pAdapter: *mut core::ffi::c_void,
    DriverType: u32,
    Software: HMODULE,
    Flags: u32,
    pFeatureLevels: *const u32,
    FeatureLevels: u32,
    SDKVersion: u32,
    pSwapChainDesc: *const DXGI_SWAP_CHAIN_DESC,
    ppSwapChain: *mut *mut core::ffi::c_void,
    ppDevice: *mut *mut core::ffi::c_void,
    pFeatureLevel: *mut u32,
    ppImmediateContext: *mut *mut core::ffi::c_void,
) -> HRESULT;

// COM vtable layout for IDXGISwapChain (manually defined).
#[repr(C)]
struct SwapChainVtbl {
    // IUnknown (3)
    query_interface: usize,
    add_ref: usize,
    release: usize,
    // IDXGIObject (4)
    set_private_data: usize,
    set_private_data_interface: usize,
    get_private_data: usize,
    get_parent: usize,
    // IDXGIDeviceSubObject (1)
    get_device: usize,
    // IDXGISwapChain (index 8+)
    present: usize, // index 8 — target
}

#[repr(C)]
struct SwapChainObj {
    vtbl: *const SwapChainVtbl,
}

// ---------------------------------------------------------------------------
// Shared memory layout  (must match Go side exactly)
// ---------------------------------------------------------------------------

/// Shared memory layout accessed by raw offset, NOT by field.
/// We use a flat byte array to guarantee exact byte layout matching the Go side.
/// Go offsets are the single source of truth; see protocol.go.
///
/// Layout:
///   [0x00000..0x04000)  — legacy command/crash/HWBP region (unchanged)
///   [0x04000..0x04100)  — snapshot header (256 B)
///   [0x04100..0x05100)  — RegionEntry[256] table (16 B × 256 = 4 KB)
///   [0x05100..0x20000)  — snapshot data blob (~107 KB slack for PlayerUnit regions)
#[repr(C, align(4096))]
struct SharedBuffer {
    data: [u8; 131072],
}

// Named offsets — must match Go protocol.go exactly.
const OFF_MAGIC: usize           = 0x00;
const OFF_VERSION: usize         = 0x04;
const OFF_READY_FLAG: usize      = 0x08;
const OFF_COMMAND_FLAG: usize    = 0x0C;
const OFF_STATUS_FLAG: usize     = 0x10;
const OFF_COMMAND_TYPE: usize    = 0x14;
const OFF_PACKET_SIZE: usize     = 0x18;
const OFF_ERROR_CODE: usize      = 0x1C;
const OFF_FN_SEND_PACKET: usize  = 0x20;
#[allow(dead_code)] const OFF_CURSOR_X: usize    = 0x28; // Phase 8C: in-process input
#[allow(dead_code)] const OFF_CURSOR_Y: usize    = 0x2C;
#[allow(dead_code)] const OFF_TARGET_KEY: usize  = 0x30;
#[allow(dead_code)] const OFF_KEY_ACTIVE: usize  = 0x31;
const OFF_ORIGINAL_PRESENT: usize = 0x34;
const OFF_DEBUG_STEP: usize      = 0x3C; // debug: tracks init progress (Go reads on timeout)
const OFF_GAME_THREAD_ID: usize  = 0x40; // u32: game thread ID for APC dispatch
const OFF_UI_NET_MAN_ADDR: usize = 0x50; // u64: D2R UI NetMan global VA (CMD_SEND_UI_PACKET)
const OFF_HWND_D2R: usize        = 0x60; // u64: D2R window handle
const OFF_MIRROR_BUF_ADDR: usize = 0x68; // u64: D2R mirror buffer VA (CMD_SEND_DUAL)
const OFF_DUAL_SEND_WRAP: usize  = 0x78; // u64: dual_send_wrap VA (game thread send)
const OFF_PACKET_DATA: usize     = 0x100;

const _: () = assert!(core::mem::size_of::<SharedBuffer>() == 131072);

// ---------------------------------------------------------------------------
// Snapshot region (Phase A of P1-GID). Rmod mirrors D2R memory into SHM so
// the bot can read in-process — no cross-process RPM. See PLAYER_UNIT_FIELDS.md.
// ---------------------------------------------------------------------------
const OFF_SNAPSHOT_HEADER: usize       = 0x4000;
// Snapshot header layout (starts at OFF_SNAPSHOT_HEADER):
const OFF_SNAP_MAGIC: usize            = 0x4000; // u32 — 'SNAP' = 0x50414E53
const OFF_SNAP_VERSION: usize          = 0x4004; // u32 — layout version
const OFF_SNAP_TICK: usize             = 0x4008; // u64 — monotonic, bumped atomically after each complete write
const OFF_SNAP_REGION_COUNT: usize     = 0x4010; // u32 — populated RegionEntry slots this tick
const OFF_SNAP_DATA_BYTES: usize       = 0x4014; // u32 — bytes used in data blob this tick
const OFF_SNAP_D2R_BASE: usize         = 0x4018; // u64 — D2R.exe module base (for debug / Phase C PEB check)
const OFF_SNAP_FLAGS: usize            = 0x4020; // u32 — bit0=enabled, bit1=main_player_found, bit2=error
const OFF_SNAP_LAST_ERR: usize         = 0x4024; // u32 — non-zero if last tick hit an error
const OFF_SNAP_LAST_RDTSC: usize       = 0x4028; // u64 — debug timestamp
const OFF_SNAP_UNIT_TABLE: usize       = 0x4030; // u64 — D2R.base + offset.UnitTable (set by Init)
const OFF_SNAP_EXPANSION: usize        = 0x4038; // u64 — D2R.base + offset.Expansion
const OFF_SNAP_WAYPOINT_TABLE: usize   = 0x4040; // u64 — D2R.base + offset.WaypointTableOffset

// Generic static-region table — bot writes N entries of {va:u64, len:u32, pad:u32}
// before CmdSnapshotInit, rmod mirrors each per tick. Lets us add new field
// coverage without rebuilding rmod (only the bot side changes).
const OFF_SNAP_STATIC_COUNT: usize     = 0x4048; // u32
const OFF_SNAP_STATIC_TABLE: usize     = 0x4050; // StaticRegion[SNAP_STATIC_MAX] × 16 B
const SNAP_STATIC_MAX: usize           = 23;     // (0x41C0 - 0x4050)/16 = 23 entries fit

const OFF_SNAPSHOT_REGIONS: usize      = 0x4200; // RegionEntry[1024] × 16 B = 16384 B (B3 grow 256→1024 for monster/obj/entrance walkers)
const SNAPSHOT_REGION_MAX: usize       = 1024;
const SNAPSHOT_REGION_ENTRY_SIZE: usize = 16; // sizeof(RegionEntry)

const OFF_SNAPSHOT_DATA: usize         = 0x8200; // blob starts here (0x4200 + 0x4000 region table)
const SNAPSHOT_DATA_SIZE: usize        = 131072 - 0x8200; // 97792 bytes

const SNAP_MAGIC: u32                  = 0x50414E53; // 'SNAP'
const SNAP_VERSION_A: u32              = 1;

/// 16-byte entry describing one mirrored D2R memory region.
/// Bot's SnapshotReader looks up (va, len) and returns slice at data_blob[offset..].
#[repr(C)]
struct RegionEntry {
    va: u64,     // original D2R virtual address
    len: u32,    // bytes copied
    offset: u32, // byte offset inside snapshot data blob (relative to OFF_SNAPSHOT_DATA)
}

/// 16-byte entry describing one generic static D2R address range the bot wants
/// mirrored every tick. Bot populates OFF_SNAP_STATIC_TABLE before CmdSnapshotInit;
/// rmod walker copies each range as a region on every Present frame.
///
/// `role` lets the bot tag entries that need post-mirror chain walking: the
/// walker still mirrors the [va, va+len) head, then if role != REGULAR
/// dereferences and mirrors the chain's inner targets (see STATIC_ROLE_*).
/// Heads that are pure fixed-address blobs use STATIC_ROLE_REGULAR (0).
#[repr(C)]
struct StaticRegion {
    va: u64,
    len: u32,
    role: u32,
}

// Role codes for StaticRegion.role. Must match presenter's
// SnapshotStaticRole constants (Go side).
#[allow(dead_code)] const STATIC_ROLE_REGULAR: u32     = 0;
const STATIC_ROLE_PING_CHAIN: u32  = 1; // head: 8 B ptr slot; deref → 40 B at target (+36: ping u32)
const STATIC_ROLE_QUEST_CHAIN: u32 = 2; // head: 8 B ptr slot; deref → 8 B at questDataPtr; deref → 82 B flags buf
const STATIC_ROLE_TZ_CHAIN: u32    = 3; // head: 16 B (ptr @0, count @8); deref → count*4 B zones array (capped 8)
const STATIC_ROLE_ROSTER_CHAIN: u32 = 4; // head: 8 B ptr slot; deref → party struct head; walker follows +0x148 linked list, 0x70 B per member (cap 16)

// Snapshot flag bits (OFF_SNAP_FLAGS).
#[allow(dead_code)] const SNAP_FLAG_ENABLED: u32            = 1 << 0;
#[allow(dead_code)] const SNAP_FLAG_MAIN_PLAYER_FOUND: u32  = 1 << 1;
#[allow(dead_code)] const SNAP_FLAG_ERROR: u32              = 1 << 2;
#[allow(dead_code)] const SNAP_FLAG_WALKER_SKIP: u32        = 1 << 3;  // diagnostic: bump tick only, skip all D2R derefs

// ---------------------------------------------------------------------------
// Crash diagnostic VEH offsets (must match Go protocol.go OffCrash*).
// Filled in-process when D2R hits an unhandled exception. Bot polls these
// after detecting D2R exit to learn what really killed it.
// ---------------------------------------------------------------------------
const OFF_CRASH_VALID: usize       = 0x1000;
const OFF_CRASH_COUNT: usize       = 0x1004;
const OFF_CRASH_CODE: usize        = 0x1008;
const OFF_CRASH_FLAGS: usize       = 0x100C;
const OFF_CRASH_TID: usize         = 0x1010;
const OFF_CRASH_FAULT_TYPE: usize  = 0x1014;
const OFF_CRASH_RIP: usize         = 0x1018;
const OFF_CRASH_FAULT_VA: usize    = 0x1020;
const OFF_CRASH_RSP: usize         = 0x1028;
const OFF_CRASH_FRAMES: usize      = 0x1030; // u64 * 16
const CRASH_FRAME_COUNT: usize     = 16;
const OFF_CRASH_REGS: usize            = 0x10B0; // 16 u64 = 128 bytes (after FRAMES at 0x1030)
const CRASH_REG_COUNT: usize           = 16;

// Bump this every time VEH patches a fault so bot can tell whether the
// handler swallowed a real AV (valuable signal that our fix-up fired).
const OFF_CRASH_FIXUPS: usize          = 0x15B4; // u32 — number of auto-fixups applied

// ---------------------------------------------------------------------------
// DR0 persist probe (Arxan diagnostic).  Free SHM 0x1600..0x1FFF.
// Determines whether SetThreadContext on D2R threads PERSISTS the DR0 value
// or whether Arxan reverts it (which would mean HWBP-based tracing is dead).
// ---------------------------------------------------------------------------
const OFF_DRPROBE_VALID:   usize       = 0x1600; // u32: 0=not run, 1=running, 2=done, 0xEEnn=err
const OFF_DRPROBE_TOTAL:   usize       = 0x1604; // u32: total D2R threads enumerated
const OFF_DRPROBE_OK:      usize       = 0x1608; // u32: count where DR0 readback == test value (PERSISTED)
const OFF_DRPROBE_REVERT:  usize       = 0x160C; // u32: count where DR0 was reverted (Arxan stripped)
const OFF_DRPROBE_ERR:     usize       = 0x1610; // u32: count of probe errors (open/suspend/get/set fails)
const OFF_DRPROBE_NUM:     usize       = 0x1614; // u32: entries actually written to ring
const OFF_DRPROBE_ENTRIES: usize       = 0x1620; // start of entries — 32 B each, max 60 entries
const DRPROBE_ENTRY_SIZE:  usize       = 32;
const DRPROBE_MAX_ENTRIES: usize       = 60;

// Test pattern written to DR0 — chosen to be obviously synthetic
// (high bits set so kernel canonical-address checks don't strip them).
const DRPROBE_TEST_DR0: u64            = 0x0000_7FFF_BABE_BEEF;
// DR7 we set: L0 enabled (bit 0), bit 10 always 1 (legacy reserved),
// no other breakpoints.
const DRPROBE_TEST_DR7: u64            = 0x0000_0000_0000_0401;

// ---------------------------------------------------------------------------
// HWBP packet tracer — captures every D2R-internal call to dual_send_wrap.
// Lives in extended SHM region 0x2000..0x4000 (8 KB).
// ---------------------------------------------------------------------------
const OFF_HWBP_INSTALLED:    usize     = 0x2000; // u32 — 1 = HWBP armed
const OFF_HWBP_TARGET:       usize     = 0x2008; // u64 — target VA (default dual_send_wrap)
const OFF_HWBP_FIRES:        usize     = 0x2010; // u32 — total SS exceptions matching target
const OFF_HWBP_SS_TOTAL:     usize     = 0x2014; // u32 — total SS exceptions seen (any RIP)
const OFF_HWBP_LAST_RIP:     usize     = 0x2018; // u64 — last SS RIP (debug, even if ≠ target)
const OFF_HWBP_INSTALL_OK:   usize     = 0x2020; // u32 — threads installed in last install
const OFF_HWBP_INSTALL_FAIL: usize     = 0x2024; // u32 — install errors
const OFF_HWBP_VERIFY_STILL: usize     = 0x2028; // u32 — threads still armed at verify time
const OFF_HWBP_VERIFY_LOST:  usize     = 0x202C; // u32 — threads where DR0 was cleared since install
const OFF_HWBP_REENUM_NEW:   usize     = 0x2030; // u32 — newly-armed threads in last reenum
const OFF_HWBP_REENUM_TOTAL: usize     = 0x2034; // u32 — total reenums executed
const OFF_HWBP_RING_HEAD:    usize     = 0x2038; // u32 — write index (wraps)
const OFF_HWBP_RING_TAIL:    usize     = 0x203C; // u32 — read index (Go advances)
const OFF_HWBP_RING_TOTAL:   usize     = 0x2040; // u32 — total entries pushed
const OFF_HWBP_RING_DROPPED: usize     = 0x2044; // u32 — entries dropped due to full ring
// Diagnostic — any exception reaching our VEH, broken down by type. Helps
// detect whether Arxan's VEH is intercepting SS exceptions before us.
const OFF_HWBP_VEH_ANY:      usize     = 0x2048; // u32 — total VEH callbacks (any code)
const OFF_HWBP_VEH_BP:       usize     = 0x204C; // u32 — EXCEPTION_BREAKPOINT (INT3)
const OFF_HWBP_VEH_AV:       usize     = 0x2050; // u32 — EXCEPTION_ACCESS_VIOLATION
const OFF_HWBP_VEH_OTHER:    usize     = 0x2054; // u32 — anything else
const OFF_HWBP_VEH_LAST_CODE:usize     = 0x2058; // u32 — last exception code seen (non-SS)
const OFF_HWBP_RING:         usize     = 0x2100; // ring start (256 B per entry, 31 slots fit 0x2100..0x3F00)
const HWBP_ENTRY_SIZE:       usize     = 256;
const HWBP_RING_ENTRIES:     usize     = 30;     // (0x4000-0x2100) / 256 = 31; leave 1 slot headroom

// Entry layout (256 B):
//   +0x00  u64 ts_ms
//   +0x08  u32 tid
//   +0x0C  u32 reserved (alignment)
//   +0x10  u64 rip
//   +0x18  u64 rsp
//   +0x20  u64 rbp
//   +0x28  u64 rax
//   +0x30  u64 rcx (packet ptr — for dual_send_wrap)
//   +0x38  u64 rdx (size — for dual_send_wrap)
//   +0x40  u64 r8
//   +0x48  u64 r9
//   +0x50  u64 r10
//   +0x58  u64 r11
//   +0x60  u64[16] callstack via RBP walk = 128 B
//   +0xE0  u8[32] payload bytes (read from RCX, truncated to 32)

// HWBP DR7 we set: L0 enabled (bit 0), exec break (R/W0=00 + LEN0=00 implicit),
// bit 10 always 1 (legacy reserved).
const HWBP_DR7: u64                    = 0x0000_0000_0000_0401;

// Packet-capture ring reuses the HWBP ring (HWBP is blocked by Arxan's SS
// filter, so its ring is unused). Each entry is HWBP_ENTRY_SIZE=256 bytes,
// layout:
//   +0x00  u64 ts_ms      (GetTickCount64 at capture)
//   +0x08  u32 tid         (thread that called send_fn)
//   +0x0C  u32 size        (rdx = packet size)
//   +0x10  u64 pkt_ptr     (rcx = plaintext packet VA)
//   +0x18  u8[232] payload (copy of up to 232 bytes from pkt_ptr)
const CAP_PAYLOAD_OFF:    usize = 0x18;
const CAP_PAYLOAD_MAX:    usize = HWBP_ENTRY_SIZE - CAP_PAYLOAD_OFF; // 232
// Scratch buffer we redirect R9 into when we detect the stash-move memcpy
// pattern. The memcpy reads [r9 - 0x10 .. r9 + 0x30] so we need at least
// 0x40 bytes of valid memory starting 0x10 before r9.
static mut G_STASH_SCRATCH: [u8; 0x200] = [0u8; 0x200];
const OFF_CRASH_RIP_BYTES: usize       = 0x1130; // 128 bytes around RIP (after REGS)
const CRASH_RIP_BYTES_PRE: usize       = 32;
const CRASH_RIP_BYTES_LEN: usize       = 128;
const OFF_CRASH_FRAME_BYTES: usize     = 0x11B0; // 16 * 64 = 1024 bytes
const CRASH_FRAME_BYTES_PRE: usize     = 16;
const CRASH_FRAME_BYTES_LEN: usize     = 64;
const CRASH_FRAME_BYTES_STRIDE: usize  = 64;

// ---------------------------------------------------------------------------
// Global state  (set once by the worker thread, read by the handler)
// ---------------------------------------------------------------------------

static mut G_SHM: *mut SharedBuffer = core::ptr::null_mut();
// Per-session SHM name prefix populated by Init() from the fallback buffer.
// Defaults to "DispCache" (legacy fallback) so the DLL still works if the
// host bot didn't write a prefix into the param buffer. Format: 16 wide
// chars, null-terminated. The Go side writes 8 wide chars + null.
static mut G_SHM_PREFIX: [u16; 16] = [
    b'D' as u16, b'i' as u16, b's' as u16, b'p' as u16,
    b'C' as u16, b'a' as u16, b'c' as u16, b'h' as u16,
    b'e' as u16, 0, 0, 0, 0, 0, 0, 0,
];
static mut G_SHM_PREFIX_LEN: usize = 9; // length of the actual prefix in G_SHM_PREFIX (default "DispCache")
static mut G_TRAMPOLINE: *const u8 = core::ptr::null();
static mut G_PRESENT_ADDR: usize = 0;
/// Capacity covers variable-length prologue copy (walk_instruction_boundary
/// may return up to ~30 bytes when Present has a big first instruction).
/// First G_PRESENT_ORIG_LEN bytes are valid; rest is zero padding.
static mut G_PRESENT_ORIG_BYTES: [u8; 32] = [0u8; 32];
static mut G_PRESENT_ORIG_LEN:   usize   = 0;
static mut G_PRESENT_ORIG_PROT: u32 = 0;               // saved page protection (for detach restore)
static mut G_GTC64_TRAMPOLINE: *const u8 = core::ptr::null(); // unused (kept for compat)
static mut G_DUAL_SEND_WRAP: usize = 0; // dual_send_wrap VA (set at init)
static mut G_GAME_HOOK_TRAMPOLINE: *const u8 = core::ptr::null(); // game thread hook marker
static mut G_GUARD_PAGE_ADDR: usize = 0;    // page we set PAGE_GUARD on
static mut G_GUARD_PAGE_OLDPROT: u32 = 0;   // original protection
static mut G_GUARD_PENDING: bool = false;    // true = waiting for game thread to trigger guard
static mut G_GUARD_REARM_COUNT: u32 = 0;     // prevent infinite re-arm loop
static mut G_DR_CTX_BUF: *mut u8 = core::ptr::null_mut(); // 4 KB page for aligned CONTEXT in DR probe
static mut G_HWBP_TARGET: usize = 0;        // RVA-resolved target VA (dual_send_wrap by default)
static mut G_HWBP_INSTALLED: bool = false;  // VEH SS handler armed
static mut G_HWBP_VEH_HANDLE: *mut core::ffi::c_void = core::ptr::null_mut();

// ---------------------------------------------------------------------------
// Packet capture inline-hook state (Discord "ingame func" approach, 2026-04-15).
//
// Install a 14-byte JMP at send_fn entry → our handler thunk (allocated RWX
// page). The thunk:
//   1. Saves volatile regs (rcx, rdx, r8, r9, r10, r11, rax)
//   2. Calls capture_recorder(rcx=pkt_ptr, rdx=size)
//   3. Restores regs
//   4. JMPs into trampoline (= copy of stolen 14 bytes + abs JMP back to send_fn+14)
//
// This replicates the Present hook pattern at line 913 (install_detour)
// which is proven working on D2R. Counterpart to bufpoll polling —
// zero-miss capture even for fast packet sequences.
// ---------------------------------------------------------------------------
static mut G_CAP_INSTALLED: bool = false;        // hook currently armed
static mut G_CAP_TARGET: usize = 0;              // send_fn VA that was hooked
static mut G_CAP_TRAMPOLINE: *mut u8 = core::ptr::null_mut(); // copy-of-prologue + JMP back
static mut G_CAP_ORIG_BYTES: [u8; 12] = [0u8; 12]; // stolen bytes at hook offset for uninstall restore
static mut G_CAP_ORIG_PROT: u32 = 0;             // original page protection

// ---------------------------------------------------------------------------
// Snapshot (Phase A P1-GID). Filled by CMD_SNAPSHOT_INIT, written every
// Present frame by snapshot_tick_write().
// ---------------------------------------------------------------------------
static mut G_SNAPSHOT_ENABLED: bool = false;     // flipped true by CMD_SNAPSHOT_INIT
static mut G_D2R_BASE: usize = 0;                // D2R.exe module base (from GetModuleHandleW(NULL))

// Worker thread state — walker runs OFF the Present callback to avoid blowing
// d3d12's Present watchdog. Present just bumps a tick; this worker sleeps
// 30 ms between full scans (~33 Hz walk rate, plenty for bot's 200-500 ms
// cache TTLs). Set G_WORKER_STOP from uninstall_present_detour to join.
static mut G_WORKER_THREAD: HANDLE = core::ptr::null_mut();
static mut G_WORKER_STOP: bool = false;
static mut G_WORKER_SHM: *mut SharedBuffer = core::ptr::null_mut(); // captured at worker spawn

// GID ROP port (Phases GID-4 + GID-5). Populated by CMD_ROP_SCAN; consumed by
// CMD_ROP_READ. `Option<AllocatedMemory>` is a static mut we carefully only
// touch inside dispatch_commands (single-threaded Present callback context).
static mut G_ROP_GADGETS:     rop_gadgets::ROPGadgets = rop_gadgets::ROPGadgets::empty();
static mut G_ROP_EXECUTOR:    Option<executor::Executor> = None;
static mut G_ROP_STACK:       Option<alloc_mgr::AllocatedMemory> = None;
static mut G_ROP_TRIGGER_BUF: Option<alloc_mgr::AllocatedMemory> = None;

// Chunked scan state. CMD_ROP_SCAN with len > ROP_SCAN_CHUNK only scans
// CHUNK bytes per invocation and updates G_ROP_SCAN_OFFSET; caller re-issues
// the command (with the same base, auto-advance via OFF_ROP_SCAN_COUNT etc.)
// until G_ROP_SCAN_COMPLETE. This keeps Present callback under ~1 ms per frame.
const ROP_SCAN_CHUNK: usize = 0x400; // 1 KB per Present frame — stays well under d3d12 watchdog
static mut G_ROP_SCAN_CURSOR:   usize = 0; // bytes scanned from base this round
static mut G_ROP_SCAN_COMPLETE: bool  = false;
// Phase C: D2R offsets stored XOR-encoded in memory; per-boot key from rdtsc
// at init prevents static-scan signatures matching the literal offset values
// (UnitTable / Expansion / WaypointTable have well-known constants).
static mut G_SNAP_UNIT_TABLE_VA_XOR: usize = 0;
static mut G_SNAP_EXPANSION_VA_XOR: usize  = 0;
static mut G_SNAP_WAYPOINT_VA_XOR: usize   = 0;
static mut G_SNAP_VA_XOR_KEY: usize        = 0;

#[inline(always)]
unsafe fn snap_unit_table_va() -> usize {
    G_SNAP_UNIT_TABLE_VA_XOR ^ G_SNAP_VA_XOR_KEY
}
#[inline(always)]
unsafe fn snap_expansion_va() -> usize {
    G_SNAP_EXPANSION_VA_XOR ^ G_SNAP_VA_XOR_KEY
}
#[inline(always)]
unsafe fn snap_waypoint_va() -> usize {
    G_SNAP_WAYPOINT_VA_XOR ^ G_SNAP_VA_XOR_KEY
}

// ---------------------------------------------------------------------------
// Type aliases for function pointers
// ---------------------------------------------------------------------------

/// D2GS_SendPacket(packet_ptr, packet_size, zero) — uses Microsoft x64 ABI.
/// On x86_64 Windows, "fastcall" == default calling convention, so we use
/// "system" (which is "stdcall" on x86 but becomes MS x64 on x86_64).
type FnSendPacket = unsafe extern "system" fn(*const u8, u32, u32);

// ---------------------------------------------------------------------------
// DllMain
// ---------------------------------------------------------------------------

#[no_mangle]
pub unsafe extern "system" fn DllMain(
    hinst: HINSTANCE,
    reason: DWORD,
    _reserved: *mut core::ffi::c_void,
) -> BOOL {
    if reason == DLL_PROCESS_ATTACH {
        DisableThreadLibraryCalls(hinst);
        let handle = CreateThread(
            core::ptr::null(),
            0,
            worker_thread,
            core::ptr::null_mut(),
            0,
            core::ptr::null_mut(),
        );
        if !handle.is_null() {
            CloseHandle(handle);
        }
    } else if reason == DLL_PROCESS_DETACH {
        // Critical: undo every D2R mutation we made. Without this, an
        // app.exe exit (graceful or panic) leaves Present's 14-byte prologue
        // pointing at a trampoline whose backing globals (G_SHM etc.) become
        // stale or freed → next Present frame AVs inside Arxan's VEH chain →
        // D2R can't exit cleanly → "HasExited but VM reboot needed" zombie.
        //
        // Order matters: remove VEHs first so an AV during detour restore
        // doesn't re-enter crash_diag_veh on a shutdown-torn stack.
        if !G_CRASH_VEH_HANDLE.is_null() {
            RemoveVectoredExceptionHandler(G_CRASH_VEH_HANDLE);
            G_CRASH_VEH_HANDLE = core::ptr::null_mut();
            G_CRASH_VEH_INSTALLED = false;
        }
        if !G_HWBP_VEH_HANDLE.is_null() {
            RemoveVectoredExceptionHandler(G_HWBP_VEH_HANDLE);
            G_HWBP_VEH_HANDLE = core::ptr::null_mut();
            G_HWBP_INSTALLED = false;
        }
        uninstall_present_detour();
        uninstall_send_fn_capture_hook();
    }
    TRUE
}

// ---------------------------------------------------------------------------
// Exported init — called directly by manual mapper via CreateRemoteThread.
// lpParameter = address of shared buffer (allocated by Go in D2R via VirtualAllocEx).
// ---------------------------------------------------------------------------

#[no_mangle]
pub unsafe extern "system" fn Init(param: *mut core::ffi::c_void) -> DWORD {
    // Phase-1 + Phase-2 re-injection handling: if a prior Init mapped a SHM
    // and then Phase-1 app.exe exited, the SHM handle is closed but G_SHM
    // still points at the (now-freed) view. Probing read_u32 there would fault
    // inside our code on the Present thread → cascade → D2R zombie.
    //
    // Check via VirtualQuery: if the current G_SHM page is no longer
    // MEM_COMMIT, the mapping is gone — reset and fall through to a fresh
    // open with the new session's prefix.
    if !G_SHM.is_null() {
        let mut mbi: MemoryBasicInformation = core::mem::zeroed();
        let got = VirtualQuery(
            G_SHM as *const core::ffi::c_void,
            &mut mbi as *mut _ as *mut core::ffi::c_void,
            core::mem::size_of::<MemoryBasicInformation>(),
        );
        let shm_valid = got != 0
            && mbi.state == MEM_COMMIT
            && (mbi.protect & (PAGE_NOACCESS | PAGE_GUARD)) == 0;
        if !shm_valid {
            // Phase-1 mapping vanished. Reset every SHM-backed piece of state
            // so the detour / walker stop touching freed memory before we
            // re-open below. SNAPSHOT goes back to opt-in until the new
            // CmdSnapshotInit arrives over the new SHM.
            G_SHM = core::ptr::null_mut();
            G_SNAPSHOT_ENABLED = false;
        } else if shm_read_u32(G_SHM, OFF_READY_FLAG) == 1 {
            // Valid existing mapping and init already completed — idempotent
            // return, as before. Manual mapper may queue APC on every D2R
            // thread; we don't want to reinstall the Present detour multiple
            // times.
            return 0;
        }
    }

    // Read per-session SHM prefix from the fallback buffer at offset 0x80.
    // The Go side writes 8 wide chars + null terminator there. If absent
    // (param null or zeroed), the legacy "DispCache" default in G_SHM_PREFIX
    // remains in effect — old hosts keep working.
    if !param.is_null() {
        const OFF_SESSION_PREFIX: usize = 0x80;
        let prefix_ptr = (param as *const u8).add(OFF_SESSION_PREFIX) as *const u16;
        // Validate that the first wide char is a printable ASCII letter/digit
        // before trusting the bytes — guards against random heap residue when
        // the host did NOT write a prefix (older bot versions).
        let first = *prefix_ptr;
        let is_printable = (first >= b'0' as u16 && first <= b'9' as u16)
            || (first >= b'A' as u16 && first <= b'Z' as u16)
            || (first >= b'a' as u16 && first <= b'z' as u16);
        if is_printable {
            // Copy up to 15 chars, stop at null. Reserve last slot for null.
            let mut new_buf = [0u16; 16];
            let mut n = 0;
            while n < 15 {
                let ch = *prefix_ptr.add(n);
                if ch == 0 { break; }
                new_buf[n] = ch;
                n += 1;
            }
            if n > 0 {
                G_SHM_PREFIX = new_buf;
                G_SHM_PREFIX_LEN = n;
            }
        }
    }

    // The manual mapper passes a fallback buffer pointer as `param`, allocated
    // via VirtualAllocEx in the target process. That buffer is only visible to
    // the DLL — the host process polls a DIFFERENT memory region (the named
    // section "SvcRt_{pid}" mapped locally). Writing to param keeps the host
    // in the dark and Init appears to never run.
    //
    // Resolution: ignore `param`, open the named section ourselves. Fall back
    // to `param` only if the named mapping isn't available yet — in which case
    // we write an error code to both buffers so the host sees at least one of
    // them via the fallback path.
    match open_shared_memory() {
        Ok(shm) => {
            G_SHM = shm;
            // Stamp "init entered" before magic check so Go can see that the
            // APC ran even if validation fails below.
            shm_write_u32(shm, OFF_DEBUG_STEP, 0xA1);
            match init_from_shm(shm) {
                Ok(()) => 0,
                Err(code) => {
                    shm_write_u32(shm, OFF_ERROR_CODE, code);
                    shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_ERROR);
                    1
                }
            }
        }
        Err(open_err) => {
            // Named mapping failed. Write diagnostic to the fallback buffer so
            // the host can at least read the open failure code out of D2R via
            // ReadProcessMemory if it wants to.
            let fallback = param as *mut SharedBuffer;
            if !fallback.is_null() {
                shm_write_u32(fallback, OFF_ERROR_CODE, open_err);
                shm_write_u32(fallback, OFF_DEBUG_STEP, 0xFF);
                shm_write_u32(fallback, OFF_STATUS_FLAG, STATUS_ERROR);
            }
            1
        }
    }
}

unsafe fn init_from_shm(shm: *mut SharedBuffer) -> Result<(), u32> {
    shm_write_u32(shm, OFF_DEBUG_STEP, 0x01);

    if shm_read_u32(shm, OFF_MAGIC) != MAGIC {
        return Err(0xE001);
    }
    if shm_read_u32(shm, OFF_VERSION) != VERSION {
        return Err(0xE002);
    }
    shm_write_u32(shm, OFF_DEBUG_STEP, 0x02);

    // Crash-diag VEH is installed lazily on first send-packet command (see
    // dispatch_in_render). Installing it during init crashed D2R — Arxan
    // appears to scan for new vectored handlers added before the game finishes
    // its own init. Deferring past the first Present frame avoids that.

    let present_addr = find_present_address(shm)?;
    G_PRESENT_ADDR = present_addr;
    shm_write_u64(shm, OFF_ORIGINAL_PRESENT, present_addr as u64);
    shm_write_u32(shm, OFF_DEBUG_STEP, 0x08);

    install_detour(present_addr, shm)?;

    // Install IAT hook on kernel32!GetTickCount64 (or fallback GetTickCount /
    // timeGetTime). Trampoline calls game_tick_dispatch on every invocation
    // which runs on game thread — so CMD_SEND_DUAL_GT and other game-thread
    // commands execute in the right context (R14/RSI/TLS populated naturally).
    //
    // Load dual_send_wrap address from SHM if Go wrote it already. If not,
    // install anyway — dispatch will pick up the addr later when bot issues
    // the first CMD_SEND_DUAL_GT command which also stores the VA.
    let dual_addr = shm_read_u64(shm as *const SharedBuffer, OFF_DUAL_SEND_WRAP) as usize;
    if dual_addr != 0 { G_DUAL_SEND_WRAP = dual_addr; }
    install_gtc64_hook();

    #[cfg(feature = "sniffer")]
    {
        if !sniffer_init() {
            shm_write_u32(shm, OFF_ERROR_CODE, 0xE300);
        }
    }

    shm_write_u32(shm, OFF_DEBUG_STEP, 0x0F);
    shm_write_u32(shm, OFF_READY_FLAG, 1);

    Ok(())
}

// Legacy DllMain + worker_thread kept for backward compat but Init is preferred.

unsafe extern "system" fn worker_thread(_param: *mut core::ffi::c_void) -> DWORD {
    match init_all() {
        Ok(()) => 0,
        Err(code) => {
            if !G_SHM.is_null() {
                shm_write_u32(G_SHM, OFF_ERROR_CODE, code);
                shm_write_u32(G_SHM, OFF_STATUS_FLAG, STATUS_ERROR);
            }
            1
        }
    }
}

unsafe fn init_all() -> Result<(), u32> {
    // 1. Open shared memory section created by the host process.
    let shm = open_shared_memory()?;
    G_SHM = shm;
    shm_write_u32(shm, OFF_DEBUG_STEP, 0x01); // shm opened

    // Validate magic + version.
    if shm_read_u32(shm, OFF_MAGIC) != MAGIC {
        return Err(0xE001);
    }
    if shm_read_u32(shm, OFF_VERSION) != VERSION {
        return Err(0xE002);
    }
    shm_write_u32(shm, OFF_DEBUG_STEP, 0x02); // magic/version OK

    // 2. Locate Present via a temporary device + swap chain.
    let present_addr = find_present_address(shm)?;
    G_PRESENT_ADDR = present_addr;
    shm_write_u64(shm, OFF_ORIGINAL_PRESENT, present_addr as u64);
    shm_write_u32(shm, OFF_DEBUG_STEP, 0x08); // present found

    // 3. Build trampoline and install inline detour.
    install_detour(present_addr, shm)?;

    // 3b. Load dual_send_wrap address and install game thread hook.
    let dual_addr = shm_read_u64(shm as *const SharedBuffer, OFF_DUAL_SEND_WRAP) as usize;
    if dual_addr != 0 {
        G_DUAL_SEND_WRAP = dual_addr;
        install_gtc64_hook();
    }

    // 3c. Init in-process sniffer (sniffer build only — creates second SHM).
    #[cfg(feature = "sniffer")]
    {
        if !sniffer_init() {
            shm_write_u32(shm, OFF_ERROR_CODE, 0xE300);
        }
    }

    // 3d. Pre-allocate ROP chain buffers on the worker thread so the first
    // CMD_ROP_SCAN doesn't stall Present with VirtualAlloc round-trips. Arxan
    // appears to flag mid-Present-frame RWX allocations; doing this here
    // (D2R worker context, before Present hook activity stabilises) has been
    // stable. All three are small and live for the lifetime of the process.
    let target_va = G_D2R_BASE;
    G_ROP_EXECUTOR    = executor::Executor::new(target_va);
    G_ROP_STACK       = alloc_mgr::AllocatedMemory::new(0x1000);
    G_ROP_TRIGGER_BUF = alloc_mgr::AllocatedMemory::new(0x1000);

    // 4. Signal ready.
    shm_write_u32(shm, OFF_DEBUG_STEP, 0x0F); // all done
    shm_write_u32(shm, OFF_READY_FLAG, 1);

    Ok(())
}

// ---------------------------------------------------------------------------
// Shared memory helpers
// ---------------------------------------------------------------------------

unsafe fn open_shared_memory() -> Result<*mut SharedBuffer, u32> {
    let pid = GetCurrentProcessId();

    // Build "{prefix}_{PID}" as a null-terminated wide string.
    // Prefix comes from G_SHM_PREFIX (set by Init() from the host's APC param);
    // defaults to "DispCache" so legacy hosts keep working. The trailing "_"
    // separator and PID digits are appended here.
    let mut name_buf = [0u16; 32];
    let mut i = 0;
    while i < G_SHM_PREFIX_LEN && i < 16 {
        name_buf[i] = G_SHM_PREFIX[i];
        i += 1;
    }
    name_buf[i] = b'_' as u16;
    i += 1;
    // Append PID as decimal digits.
    i = write_u32_wide(&mut name_buf, i, pid);
    name_buf[i] = 0; // null terminator

    let handle = OpenFileMappingW(FILE_MAP_ALL_ACCESS, FALSE, name_buf.as_ptr());
    if handle.is_null() {
        return Err(0xE010);
    }

    let view = MapViewOfFile(handle, FILE_MAP_ALL_ACCESS, 0, 0, 16384);
    if view.is_null() {
        CloseHandle(handle);
        return Err(0xE011);
    }

    Ok(view as *mut SharedBuffer)
}

/// Write a u32 as decimal wide chars into `buf` starting at `pos`. Returns new pos.
fn write_u32_wide(buf: &mut [u16], pos: usize, val: u32) -> usize {
    if val == 0 {
        buf[pos] = b'0' as u16;
        return pos + 1;
    }
    // Extract digits in reverse.
    let mut digits = [0u16; 10];
    let mut n = val;
    let mut count = 0;
    while n > 0 {
        digits[count] = b'0' as u16 + (n % 10) as u16;
        n /= 10;
        count += 1;
    }
    let mut p = pos;
    for j in (0..count).rev() {
        buf[p] = digits[j];
        p += 1;
    }
    p
}

// ---------------------------------------------------------------------------
// Find IDXGISwapChain::Present address — lightweight, no dummy device
// ---------------------------------------------------------------------------

unsafe fn find_present_address(shm: *mut SharedBuffer) -> Result<usize, u32> {
    shm_write_u32(shm, OFF_DEBUG_STEP, 0x03);

    // dxgi.dll is already loaded by D2R. Get its base address.
    let dxgi_name: &[u16] = &[
        b'd' as u16, b'x' as u16, b'g' as u16, b'i' as u16,
        b'.' as u16, b'd' as u16, b'l' as u16, b'l' as u16, 0,
    ];
    // Phase C: PEB walk replaces GetModuleHandleW for module-base resolution.
    let hmod_addr = peb_find_module(dxgi_name);
    if hmod_addr == 0 {
        return Err(0xE020);
    }
    let hmod = hmod_addr as HMODULE;

    // IDXGISwapChain::Present is at a known offset in dxgi.dll.
    // This offset is stable per Windows version (determined by vtable layout).
    // Offset verified on current system: 0x4F80.
    // The Go side passes the correct offset via shared buffer if needed;
    // for now we read it from OFF_ORIGINAL_PRESENT if non-zero, else use default.
    let override_addr = shm_read_u64(shm, OFF_ORIGINAL_PRESENT) as usize;
    let present_addr = if override_addr != 0 {
        override_addr
    } else {
        (hmod as usize) + 0x4F80
    };

    shm_write_u32(shm, OFF_DEBUG_STEP, 0x07);

    if present_addr == 0 {
        return Err(0xE025);
    }

    Ok(present_addr)
}

/// Call IUnknown::Release on a COM pointer (vtable index 2).
unsafe fn com_release(obj: *mut core::ffi::c_void) {
    if obj.is_null() {
        return;
    }
    let vtbl_ptr = *(obj as *const *const usize);
    let release_fn: unsafe extern "system" fn(*mut core::ffi::c_void) -> u32 =
        core::mem::transmute(*vtbl_ptr.add(2));
    release_fn(obj);
}

unsafe extern "system" fn stub_wndproc(
    hwnd: HWND, msg: UINT, wparam: WPARAM, lparam: LPARAM,
) -> LRESULT {
    DefWindowProcW(hwnd, msg, wparam, lparam)
}

// ---------------------------------------------------------------------------
// Detour installation
// ---------------------------------------------------------------------------

unsafe fn install_detour(present_addr: usize, shm: *mut SharedBuffer) -> Result<(), u32> {
    shm_write_u32(shm, OFF_DEBUG_STEP, 0x09); // install_detour starting

    // Idempotency guard — critical for the Phase-1 + Phase-2 re-injection flow
    // in auto_claude.sh. Without this, a second Init call would read the
    // already-hooked 14 bytes as "original prologue", build a trampoline whose
    // prologue is our old JMP, and chain detours — eventually into freed memory
    // when Phase-1 app.exe exits. One install per process.
    if !G_TRAMPOLINE.is_null() {
        shm_write_u32(shm, OFF_DEBUG_STEP, 0x0F); // already installed — skipping
        return Ok(());
    }

    let present_ptr = present_addr as *const u8;

    // 1. Allocate RWX trampoline page.
    let trampoline = VirtualAlloc(
        core::ptr::null(),
        4096,
        MEM_COMMIT | MEM_RESERVE,
        PAGE_EXECUTE_READWRITE,
    ) as *mut u8;
    if trampoline.is_null() {
        return Err(0xE030);
    }
    shm_write_u32(shm, OFF_DEBUG_STEP, 0x0A); // trampoline allocated

    // 2. Determine variable-length prologue copy via asm::walk_instruction_boundary.
    //    We need ≥ DETOUR_SIZE bytes of RIP-relocatable instructions. Decoder
    //    walks one instruction at a time and stops at a boundary ≥ DETOUR_SIZE.
    //    Fallback to fixed DETOUR_SIZE on decode failure (current D2R builds
    //    have RIP-rel-free first 14 bytes of Present so the fallback is safe).
    let copy_len = asm::walk_instruction_boundary(present_ptr, DETOUR_SIZE, 32)
        .unwrap_or(DETOUR_SIZE);

    // Copy original prologue bytes into trampoline AND save a copy for
    // DLL_PROCESS_DETACH uninstall (trampoline may be VirtualFree'd).
    core::ptr::copy_nonoverlapping(present_ptr, trampoline, copy_len);
    core::ptr::copy_nonoverlapping(present_ptr, G_PRESENT_ORIG_BYTES.as_mut_ptr(), copy_len);
    G_PRESENT_ORIG_LEN = copy_len;
    shm_write_u32(shm, OFF_DEBUG_STEP, 0x0B); // prologue copied

    // Fix up RIP-relative displacements in the copied instructions. Each
    // carrying-disp instruction's effective address was relative to the
    // ORIGINAL Present IP; after moving to the trampoline, displacements
    // must be offset by (present_addr - trampoline_addr).
    let delta: i64 = (trampoline as i64) - (present_addr as i64);
    let mut cursor = 0usize;
    while cursor < copy_len {
        let rem = copy_len - cursor;
        let info_opt = asm::decode_insn(trampoline.add(cursor), rem);
        let info = match info_opt {
            Some(i) => i,
            None => break, // decoder gave up — remaining bytes are RIP-rel-free by assumption
        };
        if info.has_rip_rel {
            let disp_off = cursor + info.rip_rel_off as usize;
            let old_disp_ptr = trampoline.add(disp_off) as *mut i32;
            let old_disp = core::ptr::read_unaligned(old_disp_ptr) as i64;
            let new_disp = (old_disp - delta) as i32;
            core::ptr::write_unaligned(old_disp_ptr, new_disp);
        }
        cursor += info.len as usize;
    }

    // 3. Append absolute JMP back to Present + copy_len.
    let jmp_back_target = present_addr + copy_len;
    write_abs_jmp(trampoline.add(copy_len), jmp_back_target);

    G_TRAMPOLINE = trampoline;

    // 4. Build the handler thunk (offset 256 in the same page).
    let handler_thunk = trampoline.add(256);
    build_handler_thunk(handler_thunk, trampoline);
    shm_write_u32(shm, OFF_DEBUG_STEP, 0x0C); // handler thunk built

    // 5. Overwrite Present with JMP to our handler thunk.
    let mut old_protect: DWORD = 0;
    if VirtualProtect(
        present_ptr as *const core::ffi::c_void,
        copy_len,
        PAGE_EXECUTE_READWRITE,
        &mut old_protect,
    ) == 0
    {
        return Err(0xE031);
    }
    shm_write_u32(shm, OFF_DEBUG_STEP, 0x0D); // VirtualProtect OK
    G_PRESENT_ORIG_PROT = old_protect; // save for DLL_PROCESS_DETACH restore

    // Write a polymorphic MOV+JMP at Present instead of the classic
    // `FF 25 00 00 00 00 <u64>` abs-indirect pattern. Per GID analysis
    // (Misc64.dll → PresentProxyHookAssembleInsteadOfBytes), Arxan sigscans
    // known JMP byte patterns at hooked function entries; a MOV r64, imm64
    // + JMP r64 sequence (12 bytes for low regs, 13 for R8-R15) produces
    // a different signature per session — we randomise the scratch reg
    // via rdtsc low bits. copy_len is determined by walk_instruction_boundary
    // so the tail lands on an instruction boundary; pad with NOPs.
    let reg_choices = [
        asm::Reg64::Rax,
        asm::Reg64::Rcx,
        asm::Reg64::Rdx,
        asm::Reg64::R10,
        asm::Reg64::R11,
    ];
    let reg = reg_choices[(rdtsc_u64() as usize) % reg_choices.len()];
    let mut emitter = asm::Emitter::new(present_ptr as *mut u8, copy_len);
    emitter.jmp_abs_via_reg(reg, handler_thunk as usize);
    while emitter.len() < copy_len {
        emitter.nop();
    }

    FlushInstructionCache(GetCurrentProcess(), present_ptr as _, copy_len);
    shm_write_u32(shm, OFF_DEBUG_STEP, 0x0E); // detour written

    // Restore original page protection.
    let mut dummy: DWORD = 0;
    VirtualProtect(
        present_ptr as *const core::ffi::c_void,
        copy_len,
        old_protect,
        &mut dummy,
    );

    Ok(())
}

/// Restore Present's original 14-byte prologue, undoing install_detour.
/// Called from DLL_PROCESS_DETACH. Must be robust to partial-state teardown
/// (any step could have failed during install). Does nothing if Present was
/// never hooked in this process.
unsafe fn uninstall_present_detour() {
    // Stop walker worker thread first so it can't race against the SHM
    // being nulled and the Present bytes being restored. Bounded wait:
    // worker sleeps 30 ms between iterations so 200 ms is plenty.
    if !G_WORKER_THREAD.is_null() {
        G_WORKER_STOP = true;
        let _ = WaitForSingleObject(G_WORKER_THREAD, 200);
        CloseHandle(G_WORKER_THREAD);
        G_WORKER_THREAD = core::ptr::null_mut();
        G_WORKER_SHM = core::ptr::null_mut();
    }
    if G_PRESENT_ADDR == 0 {
        return; // never installed
    }
    let present_ptr = G_PRESENT_ADDR as *mut u8;

    // Flip page to RWX, restore saved bytes, flush icache, restore protection.
    // Use G_PRESENT_ORIG_LEN (set by install_detour via walk_instruction_boundary)
    // so variable-length prologues restore correctly; fall back to DETOUR_SIZE
    // for legacy paths that didn't set the length.
    let restore_len = if G_PRESENT_ORIG_LEN != 0 { G_PRESENT_ORIG_LEN } else { DETOUR_SIZE };
    let mut old_protect: DWORD = 0;
    if VirtualProtect(
        present_ptr as *const core::ffi::c_void,
        restore_len,
        PAGE_EXECUTE_READWRITE,
        &mut old_protect,
    ) == 0 {
        return; // can't unhook; let the process die as it will
    }

    core::ptr::copy_nonoverlapping(
        G_PRESENT_ORIG_BYTES.as_ptr(),
        present_ptr,
        restore_len,
    );
    FlushInstructionCache(GetCurrentProcess(), present_ptr as _, restore_len);

    let mut dummy: DWORD = 0;
    VirtualProtect(
        present_ptr as *const core::ffi::c_void,
        restore_len,
        if G_PRESENT_ORIG_PROT != 0 { G_PRESENT_ORIG_PROT } else { old_protect },
        &mut dummy,
    );

    // Mark detour removed so a re-injection (if any) will reinstall.
    G_TRAMPOLINE = core::ptr::null();
    G_PRESENT_ADDR = 0;
}

/// Write a 14-byte absolute indirect JMP: FF 25 00 00 00 00 [addr64].
unsafe fn write_abs_jmp(dest: *mut u8, target: usize) {
    *dest = 0xFF;
    *dest.add(1) = 0x25;
    *dest.add(2) = 0x00;
    *dest.add(3) = 0x00;
    *dest.add(4) = 0x00;
    *dest.add(5) = 0x00;
    let addr_ptr = dest.add(6) as *mut u64;
    core::ptr::write_unaligned(addr_ptr, target as u64);
}

// ---------------------------------------------------------------------------
// Handler thunk — hand-assembled x86-64 machine code
// ---------------------------------------------------------------------------
//
// Emitted directly as bytes into executable memory.  Flow:
//   1. Save all volatile registers (integer + XMM0-5)
//   2. Call dispatch_commands()
//   3. Restore all volatile registers
//   4. JMP to trampoline (original prologue + JMP back to Present+14)
//
// Stack alignment analysis:
//   On entry from the detour JMP, RSP has the same alignment as when
//   Present was originally called.  Windows x64 ABI: RSP is 8-mod-16
//   on function entry (the CALL pushed 8 bytes of return address).
//
//   7 pushes  = 56 bytes  =>  RSP delta = 56  =>  RSP mod 16 = (8+56) mod 16 = 0
//   sub 0x80  = 128 bytes =>  still 0 mod 16
//   sub 0x28  = 40 bytes  =>  8 mod 16  (CALL will push RIP => 0 mod 16)  OK

unsafe fn build_handler_thunk(buf: *mut u8, trampoline: *const u8) {
    let mut w = ThunkWriter { buf, offset: 0 };

    // Save integer volatiles.
    w.emit(&[0x50]);                         // push rax
    w.emit(&[0x51]);                         // push rcx
    w.emit(&[0x52]);                         // push rdx
    w.emit(&[0x41, 0x50]);                   // push r8
    w.emit(&[0x41, 0x51]);                   // push r9
    w.emit(&[0x41, 0x52]);                   // push r10
    w.emit(&[0x41, 0x53]);                   // push r11

    // sub rsp, 0x100  (256 bytes for ALL XMM save area: XMM0-15).
    // XMM0-5 volatile (only caller needs to preserve), XMM6-15 non-volatile
    // (callee MUST preserve). Previously we only saved XMM0-5, trusting Rust
    // to preserve XMM6-15 through dispatch_commands. But d3d12.dll post-Present
    // crashed consistently after SnapshotInit — one hypothesis is that the
    // walker or a callee clobbers XMM6-15. Saving all 16 eliminates that.
    w.emit(&[0x48, 0x81, 0xEC, 0x00, 0x01, 0x00, 0x00]);

    // movaps [rsp+N], xmmN  — save XMM0-7 (1-byte disp, REX-free)
    w.emit(&[0x0F, 0x29, 0x04, 0x24]);                // xmm0 -> [rsp+0x00]
    w.emit(&[0x0F, 0x29, 0x4C, 0x24, 0x10]);          // xmm1 -> [rsp+0x10]
    w.emit(&[0x0F, 0x29, 0x54, 0x24, 0x20]);          // xmm2 -> [rsp+0x20]
    w.emit(&[0x0F, 0x29, 0x5C, 0x24, 0x30]);          // xmm3 -> [rsp+0x30]
    w.emit(&[0x0F, 0x29, 0x64, 0x24, 0x40]);          // xmm4 -> [rsp+0x40]
    w.emit(&[0x0F, 0x29, 0x6C, 0x24, 0x50]);          // xmm5 -> [rsp+0x50]
    w.emit(&[0x0F, 0x29, 0x74, 0x24, 0x60]);          // xmm6 -> [rsp+0x60]
    w.emit(&[0x0F, 0x29, 0x7C, 0x24, 0x70]);          // xmm7 -> [rsp+0x70]

    // save XMM8-15 (REX.R prefix 0x44, 4-byte disp because offset >= 0x80)
    w.emit(&[0x44, 0x0F, 0x29, 0x84, 0x24, 0x80, 0x00, 0x00, 0x00]); // xmm8  -> [rsp+0x80]
    w.emit(&[0x44, 0x0F, 0x29, 0x8C, 0x24, 0x90, 0x00, 0x00, 0x00]); // xmm9  -> [rsp+0x90]
    w.emit(&[0x44, 0x0F, 0x29, 0x94, 0x24, 0xA0, 0x00, 0x00, 0x00]); // xmm10 -> [rsp+0xA0]
    w.emit(&[0x44, 0x0F, 0x29, 0x9C, 0x24, 0xB0, 0x00, 0x00, 0x00]); // xmm11 -> [rsp+0xB0]
    w.emit(&[0x44, 0x0F, 0x29, 0xA4, 0x24, 0xC0, 0x00, 0x00, 0x00]); // xmm12 -> [rsp+0xC0]
    w.emit(&[0x44, 0x0F, 0x29, 0xAC, 0x24, 0xD0, 0x00, 0x00, 0x00]); // xmm13 -> [rsp+0xD0]
    w.emit(&[0x44, 0x0F, 0x29, 0xB4, 0x24, 0xE0, 0x00, 0x00, 0x00]); // xmm14 -> [rsp+0xE0]
    w.emit(&[0x44, 0x0F, 0x29, 0xBC, 0x24, 0xF0, 0x00, 0x00, 0x00]); // xmm15 -> [rsp+0xF0]

    // sub rsp, 0x28  (0x20 shadow + 0x8 alignment padding).
    // NOTE: by ABI this should be 0x20 for proper rsp mod 16 = 0 at CALL, but
    // the 0x28 form has been live-stable for weeks — changing to 0x20 caused
    // d3d12.dll crashes in live test 2026-04-17 (stack frame in d3d12+0xCF2FE
    // fault at post-Present heap code). Some caller of Present apparently
    // expects the misalignment, or our saved registers land at the 0x28-only
    // offsets some lower-level code reads. Stay on 0x28.
    w.emit(&[0x48, 0x83, 0xEC, 0x28]);

    // movabs rax, <dispatch_commands address>
    let dispatch_addr = dispatch_commands as *const () as usize;
    w.emit(&[0x48, 0xB8]);
    w.emit(&dispatch_addr.to_le_bytes());
    // call rax
    w.emit(&[0xFF, 0xD0]);

    // add rsp, 0x28
    w.emit(&[0x48, 0x83, 0xC4, 0x28]);

    // Restore XMM0-7.
    w.emit(&[0x0F, 0x28, 0x04, 0x24]);                // xmm0 <- [rsp+0x00]
    w.emit(&[0x0F, 0x28, 0x4C, 0x24, 0x10]);          // xmm1 <- [rsp+0x10]
    w.emit(&[0x0F, 0x28, 0x54, 0x24, 0x20]);          // xmm2 <- [rsp+0x20]
    w.emit(&[0x0F, 0x28, 0x5C, 0x24, 0x30]);          // xmm3 <- [rsp+0x30]
    w.emit(&[0x0F, 0x28, 0x64, 0x24, 0x40]);          // xmm4 <- [rsp+0x40]
    w.emit(&[0x0F, 0x28, 0x6C, 0x24, 0x50]);          // xmm5 <- [rsp+0x50]
    w.emit(&[0x0F, 0x28, 0x74, 0x24, 0x60]);          // xmm6 <- [rsp+0x60]
    w.emit(&[0x0F, 0x28, 0x7C, 0x24, 0x70]);          // xmm7 <- [rsp+0x70]

    // Restore XMM8-15.
    w.emit(&[0x44, 0x0F, 0x28, 0x84, 0x24, 0x80, 0x00, 0x00, 0x00]); // xmm8  <- [rsp+0x80]
    w.emit(&[0x44, 0x0F, 0x28, 0x8C, 0x24, 0x90, 0x00, 0x00, 0x00]); // xmm9  <- [rsp+0x90]
    w.emit(&[0x44, 0x0F, 0x28, 0x94, 0x24, 0xA0, 0x00, 0x00, 0x00]); // xmm10 <- [rsp+0xA0]
    w.emit(&[0x44, 0x0F, 0x28, 0x9C, 0x24, 0xB0, 0x00, 0x00, 0x00]); // xmm11 <- [rsp+0xB0]
    w.emit(&[0x44, 0x0F, 0x28, 0xA4, 0x24, 0xC0, 0x00, 0x00, 0x00]); // xmm12 <- [rsp+0xC0]
    w.emit(&[0x44, 0x0F, 0x28, 0xAC, 0x24, 0xD0, 0x00, 0x00, 0x00]); // xmm13 <- [rsp+0xD0]
    w.emit(&[0x44, 0x0F, 0x28, 0xB4, 0x24, 0xE0, 0x00, 0x00, 0x00]); // xmm14 <- [rsp+0xE0]
    w.emit(&[0x44, 0x0F, 0x28, 0xBC, 0x24, 0xF0, 0x00, 0x00, 0x00]); // xmm15 <- [rsp+0xF0]

    // add rsp, 0x100
    w.emit(&[0x48, 0x81, 0xC4, 0x00, 0x01, 0x00, 0x00]);

    // Restore integer volatiles.
    w.emit(&[0x41, 0x5B]);                   // pop r11
    w.emit(&[0x41, 0x5A]);                   // pop r10
    w.emit(&[0x41, 0x59]);                   // pop r9
    w.emit(&[0x41, 0x58]);                   // pop r8
    w.emit(&[0x5A]);                         // pop rdx
    w.emit(&[0x59]);                         // pop rcx
    w.emit(&[0x58]);                         // pop rax

    // JMP to trampoline (original prologue + jmp back to Present+14)
    write_abs_jmp(w.current(), trampoline as usize);
}

struct ThunkWriter {
    buf: *mut u8,
    offset: usize,
}

impl ThunkWriter {
    unsafe fn emit(&mut self, bytes: &[u8]) {
        core::ptr::copy_nonoverlapping(bytes.as_ptr(), self.buf.add(self.offset), bytes.len());
        self.offset += bytes.len();
    }

    fn current(&self) -> *mut u8 {
        unsafe { self.buf.add(self.offset) }
    }
}

// ---------------------------------------------------------------------------
// Dispatch logic (called from the thunk every frame)
// ---------------------------------------------------------------------------

/// Called every frame from the Present detour.  Must be fast on the idle path.
unsafe fn dispatch_commands() {
    let shm = G_SHM;
    if shm.is_null() {
        return;
    }

    // Install crash-diagnostic VEH eagerly on the FIRST Present frame after
    // SHM is mapped. Doing it here (before any command processing) means we
    // catch exceptions from D2R's own threads even when the bot hasn't sent
    // a packet yet. The function is idempotent.
    install_crash_diag_veh(shm);

    // (Was: reinstall crash-diag VEH every 30 frames to stay at head of chain.
    //  Removed — Remove→Add created a tiny no-handler window that, at 60 Hz
    //  Present, landed unlucky on routine Arxan page-hash AVs and turned them
    //  into unhandled exceptions. Dispatch-specific reinstall calls before
    //  risky ops still run; those now use non-removing double-register.)
    static mut REINSTALL_TICK: u32 = 0;
    REINSTALL_TICK = REINSTALL_TICK.wrapping_add(1);
    if REINSTALL_TICK % 30 == 0 {
        reinstall_hwbp_veh();
    }

    // Sniffer: poll buf0/buf1 every frame (before command dispatch).
    #[cfg(feature = "sniffer")]
    {
        sniffer_poll();
        // Debug: write sniffer state to error_code field so Go can read it
        static mut DBG_COUNTER: u32 = 0;
        DBG_COUNTER = DBG_COUNTER.wrapping_add(1);
        if DBG_COUNTER % 300 == 0 { // every ~5s at 60fps
            let marker: u32 = if sniffer_impl::G_SNIFF_SHM.is_null() { 0xDEAD0000 } else { 0xA11E0000 };
            shm_write_u32(shm, OFF_ERROR_CODE, marker | (DBG_COUNTER & 0xFFFF));
        }
    }

    // Snapshot tick: fires every Present frame independent of command queue.
    // After CMD_SNAPSHOT_INIT flips G_SNAPSHOT_ENABLED, this populates the
    // SHM snapshot region so bot can read in-process (zero cross-process RPM).
    if G_SNAPSHOT_ENABLED {
        snapshot_tick_write(shm);
    }

    // Fast path: no pending command.
    if shm_read_u32(shm, OFF_COMMAND_FLAG) == 0 {
        return;
    }

    // Lazy-install crash-diagnostic VEH on first command. By now the game has
    // been running long enough that Arxan is past its own startup checks, so
    // adding a vectored handler doesn't get the process killed.
    install_crash_diag_veh(shm);

    let cmd_type = shm_read_u32(shm, OFF_COMMAND_TYPE);

    match cmd_type {
        CMD_NOP => {
            shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_DONE);
            shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
        }
        CMD_SEND_PACKET => {
            // dispatch_send_packet queues APC — the APC shellcode sets STATUS_DONE.
            dispatch_send_packet(shm);
            // Don't set DONE here — APC does it after SendPacket completes.
        }
        CMD_SEND_UI_PACKET => {
            dispatch_send_ui_packet(shm);
        }
        CMD_SEND_DUAL => {
            dispatch_send_dual(shm);
        }
        CMD_CALL_FN => {
            dispatch_call_fn(shm);
        }
        CMD_WRITE_MEM => {
            dispatch_write_mem(shm);
        }
        CMD_CALL_FN_GT => {
            dispatch_call_fn_game_thread(shm);
        }
        CMD_SEND_DUAL_GT => {
            // Load dual_send_wrap address from SHM (idempotent).
            let dual_addr = shm_read_u64(shm as *const SharedBuffer, OFF_DUAL_SEND_WRAP) as usize;
            if dual_addr != 0 { G_DUAL_SEND_WRAP = dual_addr; }
            // Pure IAT-hook path: install_gtc64_hook was called at init; every
            // GetTickCount64 call on a game thread fires our thunk, which calls
            // game_tick_dispatch, which picks up STATUS_BUSY + CMD and invokes
            // dual_send_wrap with our packet from the ON-GAME-THREAD context.
            // No guard-page VEH needed, no SuspendThread.
            shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_BUSY);
        }
        CMD_DR_PROBE => {
            dispatch_dr_probe(shm);
        }
        CMD_HWBP_INSTALL => {
            dispatch_hwbp_install(shm);
        }
        CMD_HWBP_UNINSTALL => {
            dispatch_hwbp_uninstall(shm);
        }
        CMD_HWBP_VERIFY => {
            dispatch_hwbp_verify(shm);
        }
        CMD_HWBP_REENUM => {
            dispatch_hwbp_reenum(shm);
        }
        CMD_SNAPSHOT_INIT => {
            dispatch_snapshot_init(shm);
        }
        CMD_ROP_SCAN => {
            // Chunked scan: do at most ROP_SCAN_CHUNK bytes per Present frame
            // so the callback never blocks long enough to trigger d3d12's
            // hang-watchdog. Caller re-issues CMD_ROP_SCAN with the same
            // (base, len) — G_ROP_SCAN_CURSOR tracks progress; COMPLETE flag
            // signals end of range.
            //
            // DBG markers written to OFF_ROP_DBG so if this handler hangs/AVs
            // the Go-side timeout can report last-seen state.
            shm_write_u32(shm, OFF_ROP_DBG, 0xAAAA0001);
            let base_va = shm_read_u64(shm, OFF_ROP_SCAN_BASE) as *const u8;
            let total   = shm_read_u64(shm, OFF_ROP_SCAN_LEN) as usize;
            shm_write_u32(shm, OFF_ROP_DBG, 0xAAAA0002);

            // Reset cursor when caller starts a fresh scan (base changed or
            // previous scan complete). We detect a fresh scan by cursor==0 OR
            // by the complete flag being set (caller restarts after query).
            if G_ROP_SCAN_COMPLETE {
                G_ROP_SCAN_CURSOR = 0;
                G_ROP_SCAN_COMPLETE = false;
            }

            let remaining = total.saturating_sub(G_ROP_SCAN_CURSOR);
            let this_chunk = if remaining > ROP_SCAN_CHUNK { ROP_SCAN_CHUNK } else { remaining };

            shm_write_u32(shm, OFF_ROP_DBG, 0xAAAA0003);
            if this_chunk > 0 {
                // No pre-scan VirtualQuery — both the cached page_readable
                // and an inline VirtualQuery blocked dispatch under Present
                // contention in live tests (dbg stuck at 0xAAAA0003 for >2 s).
                // crash_diag_veh catches AVs from unmapped pages; callers
                // should only scan regions they know (or strongly suspect)
                // are committed .text. The 16 KB live run at 0x7FF679AB0000
                // successfully returned 118 gadgets with this approach.
                G_ROP_GADGETS.scan(base_va.add(G_ROP_SCAN_CURSOR), this_chunk);
                G_ROP_SCAN_CURSOR += this_chunk;
            }
            shm_write_u32(shm, OFF_ROP_DBG, 0xAAAA0004);
            if G_ROP_SCAN_CURSOR >= total {
                G_ROP_SCAN_COMPLETE = true;
            }

            // NOTE: Lazy alloc of Executor + stack + trigger buffer moved
            // to init_worker thread (see DllMain). Allocating 64+4+4 KB
            // of PAGE_EXECUTE_READWRITE from inside Present callback was
            // consistently triggering D2R crashes — suspected Arxan memory
            // scanner flagging mid-frame RWX allocs as injection pattern.
            // Worker-thread approach does it once during DllMain after a
            // settle delay; scan command just CHECKS `is_some()`.
            shm_write_u32(shm, OFF_ROP_DBG, 0xAAAA0008);

            shm_write_u32(shm, OFF_ROP_SCAN_COUNT, G_ROP_GADGETS.count as u32);
            shm_write_u32(shm, OFF_ROP_DBG, 0xAAAA0009);

            // Breakdown only runs on the FINAL scan chunk — skipped on
            // intermediate chunks to keep per-frame budget small. Pool state
            // doesn't change between chunks unless a scan adds new entries,
            // so intermediate stale breakdown is fine.
            if G_ROP_SCAN_COMPLETE {
                let mut kind_counts = [0u32; 8];
                let mut pop_mask: u16 = 0;
                let gcount = G_ROP_GADGETS.count;
                if gcount <= rop_gadgets::GADGET_POOL_SIZE {
                    for i in 0..gcount {
                        let g = &G_ROP_GADGETS.pool[i];
                        let idx = match g.kind {
                            rop_gadgets::GadgetKind::Unknown    => 0,
                            rop_gadgets::GadgetKind::PopReg     => { pop_mask |= g.regs_touched; 1 },
                            rop_gadgets::GadgetKind::MovRegMem  => 2,
                            rop_gadgets::GadgetKind::MovMemReg  => 3,
                            rop_gadgets::GadgetKind::RepMovsb   => 4,
                            rop_gadgets::GadgetKind::RepMovsq   => 5,
                            rop_gadgets::GadgetKind::XchgReg    => 6,
                            rop_gadgets::GadgetKind::Ret        => 7,
                        };
                        kind_counts[idx] += 1;
                    }
                }
                shm_write_u32(shm, OFF_ROP_DBG, 0xAAAA000A);
                for i in 0..8 {
                    shm_write_u32(shm, OFF_ROP_KIND_COUNTS + i * 4, kind_counts[i]);
                }
                shm_write_u32(shm, OFF_ROP_POPREG_MASK, pop_mask as u32);
                shm_write_u32(shm, OFF_ROP_DBG, 0xAAAA000B);
            }

            let ready = G_ROP_SCAN_COMPLETE
                && G_ROP_EXECUTOR.is_some()
                && G_ROP_STACK.is_some()
                && G_ROP_TRIGGER_BUF.is_some();
            shm_write_u32(shm, OFF_ROP_READY, if ready { 1 } else { 0 });
            shm_write_u32(shm, OFF_ROP_DBG, 0xAAAA000F); // final marker — full path completed
            shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_DONE);
            shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
        }
        CMD_ROP_READ => {
            // Un-gated GID-6. Build memcpy(src, dst, len) chain using harvested
            // D2R gadgets, execute via in-process trigger thunk. On success the
            // `len` bytes at src now live at dst (bot supplies dst as a scratch
            // VA within our SHM or our own alloc_mgr buffer).
            //
            // Status encoding:
            //   0 = ok
            //   1 = gadget pool missing (caller must run CMD_ROP_SCAN first)
            //   2 = chain build failed (pool lacked required gadget kind)
            //   3 = trigger_va invalid
            //
            // CAUTION: first live chain run is a D2R-integrity risk. If anything
            // is encoded wrong the whole process AVs. Diagnostic marker in
            // OFF_ROP_DBG (0xBBBB00xx band) lets Go trace final state on crash.
            shm_write_u32(shm, OFF_ROP_DBG, 0xBBBB0001);

            let src_va = shm_read_u64(shm, OFF_ROP_READ_SRC) as usize;
            let dst_va = shm_read_u64(shm, OFF_ROP_READ_DST) as usize;
            let len    = shm_read_u64(shm, OFF_ROP_READ_LEN) as usize;

            if G_ROP_GADGETS.count == 0
                || G_ROP_EXECUTOR.is_none()
                || G_ROP_STACK.is_none()
                || G_ROP_TRIGGER_BUF.is_none()
            {
                shm_write_u32(shm, OFF_ROP_DBG, 0xBBBB00E1);
                shm_write_u32(shm, OFF_ROP_READ_STATUS, 1);
                shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_DONE);
                shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
                return;
            }

            shm_write_u32(shm, OFF_ROP_DBG, 0xBBBB0002);
            let stack   = G_ROP_STACK.as_ref().unwrap();
            let trigger = G_ROP_TRIGGER_BUF.as_ref().unwrap();
            let tick    = rdtsc_u64();
            let builder = rop_chain::RopChainBuilder::new(&G_ROP_GADGETS, stack, trigger, tick);

            shm_write_u32(shm, OFF_ROP_DBG, 0xBBBB0003);
            let built = match builder.build_memcpy(src_va, dst_va, len) {
                Some(c) => c,
                None => {
                    shm_write_u32(shm, OFF_ROP_DBG, 0xBBBB00E2);
                    shm_write_u32(shm, OFF_ROP_READ_STATUS, 2);
                    shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_DONE);
                    shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
                    return;
                }
            };

            if built.trigger_va == 0 {
                shm_write_u32(shm, OFF_ROP_DBG, 0xBBBB00E3);
                shm_write_u32(shm, OFF_ROP_READ_STATUS, 3);
                shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_DONE);
                shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
                return;
            }

            shm_write_u32(shm, OFF_ROP_DBG, 0xBBBB0004);
            // Execute: treat trigger_va as a zero-arg function pointer.
            // The thunk saves callee-saved regs, flips RSP to our chain, rets
            // into the gadget chain, and the chain rets back to the epilogue
            // which restores RSP + returns here.
            type TriggerFn = unsafe extern "system" fn();
            let trigger_fn: TriggerFn = core::mem::transmute(built.trigger_va);
            trigger_fn();
            shm_write_u32(shm, OFF_ROP_DBG, 0xBBBB0005);

            shm_write_u32(shm, OFF_ROP_READ_STATUS, 0);
            shm_write_u32(shm, OFF_ROP_DBG, 0xBBBB000F);
            shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_DONE);
            shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
        }
        CMD_UNINSTALL_DETOUR => {
            // Graceful shutdown — bot calls this before app.exe exits so
            // rmod cleans up every D2R mutation while the SHM command channel
            // is still alive. Without this path, a panic in bot code leaves
            // Present hooked → D2R AVs on next frame → Arxan VEH cascade →
            // zombie requiring VM reboot.
            //
            // Order: VEHs off first (so restore can't re-enter our handler),
            // then detour / capture hook, then ack, then null G_SHM LAST.
            if !G_CRASH_VEH_HANDLE.is_null() {
                RemoveVectoredExceptionHandler(G_CRASH_VEH_HANDLE);
                G_CRASH_VEH_HANDLE = core::ptr::null_mut();
                G_CRASH_VEH_INSTALLED = false;
            }
            if !G_HWBP_VEH_HANDLE.is_null() {
                RemoveVectoredExceptionHandler(G_HWBP_VEH_HANDLE);
                G_HWBP_VEH_HANDLE = core::ptr::null_mut();
                G_HWBP_INSTALLED = false;
            }
            uninstall_present_detour();
            uninstall_send_fn_capture_hook();
            G_SNAPSHOT_ENABLED = false;
            shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_DONE);
            shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
            // NOTE: G_SHM is nulled AFTER ack so the bot can read STATUS_DONE.
            // dispatch_commands's `if shm.is_null() { return; }` will short-
            // circuit next frame once the bot unmaps its side.
            G_SHM = core::ptr::null_mut();
            return;
        }
        CMD_PACKET_TRACE_INSTALL | CMD_PACKET_TRACE_UNINSTALL => {
            // DISABLED 2026-04-15 late: inline hook approach crashed D2R via
            // Arxan page-hash integrity scan (~80s detect window).
            //
            // Colleague from Discord clarified: his approach is SNIFF via
            // polling + calling D2R's internal decrypt function for incoming
            // packets — NOT inline JMP on send_fn. Correct path:
            //
            //   * OUTGOING: plaintext already in buf0/buf1 before encrypt.
            //     Use in-process bufpoll (sniffer feature) scanning those
            //     buffers at Present-hook rate.
            //   * INCOMING: raw encrypted in a receive buffer. Find D2R's
            //     decrypt function, CALL it from our in-process code to get
            //     plaintext. (Offset + function TBD via Ghidra RE.)
            //
            // Hook infrastructure kept below (install_send_fn_capture_hook,
            // build_capture_thunk, capture_recorder) for reference — do not
            // re-enable without Arxan-bypass strategy.
            shm_write_u32(shm, OFF_ERROR_CODE, 0xE17A);
            shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_ERROR);
            shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
        }
        _ => {
            shm_write_u32(shm, OFF_ERROR_CODE, 0xE100);
            shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_ERROR);
            shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
        }
    }
}

unsafe fn dispatch_send_packet(shm: *mut SharedBuffer) {
    let fn_addr = shm_read_u64(shm, OFF_FN_SEND_PACKET);
    let size = shm_read_u32(shm, OFF_PACKET_SIZE);

    if fn_addr == 0 {
        shm_write_u32(shm, OFF_ERROR_CODE, 0xE110);
        shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_ERROR);
        shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
        return;
    }

    let max_packet = 4096 - OFF_PACKET_DATA;
    if size == 0 || size as usize > max_packet {
        shm_write_u32(shm, OFF_ERROR_CODE, 0xE111);
        shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_ERROR);
        shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
        return;
    }

    // Diagnostic: dump first 8 bytes of fn to offset 0x80
    let fn_ptr = fn_addr as usize as *const u8;
    core::ptr::copy_nonoverlapping(fn_ptr, (shm as *mut u8).add(0x80), 8);

    // Push our crash VEH back to the head of First=1 chain so we see the AV
    // before Arxan's handler redirects into a stack-overflowing recovery path.
    reinstall_crash_diag_veh();

    // Direct call
    let send_fn: FnSendPacket = core::mem::transmute(fn_addr as usize);
    let pkt_ptr = (shm as *const u8).add(OFF_PACKET_DATA);
    send_fn(pkt_ptr, size, 0);

    // Mark done
    shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_DONE);
    shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
}

// ---------------------------------------------------------------------------
// Shared memory access helpers (offset-based, no struct field access)
// ---------------------------------------------------------------------------

#[inline(always)]
unsafe fn shm_read_u32(shm: *const SharedBuffer, offset: usize) -> u32 {
    let ptr = (shm as *const u8).add(offset) as *const AtomicU32;
    (*ptr).load(Ordering::SeqCst)
}

#[inline(always)]
unsafe fn shm_write_u32(shm: *mut SharedBuffer, offset: usize, val: u32) {
    let ptr = (shm as *mut u8).add(offset) as *const AtomicU32;
    (*ptr).store(val, Ordering::SeqCst);
}

#[inline(always)]
unsafe fn shm_read_u64(shm: *const SharedBuffer, offset: usize) -> u64 {
    core::ptr::read_unaligned((shm as *const u8).add(offset) as *const u64)
}

// ---------------------------------------------------------------------------
// CMD_SEND_UI_PACKET — send a packet through D2R's UI NetMan vtable[5].
//
// ABI (from live RE of FUN_7ff79d8b81b0, vtable[5] of UI NetMan struct):
//   rcx = this      (UI NetMan instance — stable global, read via OFF_UI_NET_MAN_ADDR)
//   rdx = channel   (always 0 for outgoing packets)
//   r8  = ptr to ByteRange { begin: *const u8, end: *const u8 }  (16-byte stack struct)
//
// Required for identify/buy/cube/gamble opcodes: 0x27, 0x5C, 0x32 (buy/gamble),
// 0x20 cube transmute. These bypass the Game NetMan path which would crash.
// ---------------------------------------------------------------------------
unsafe fn dispatch_send_ui_packet(shm: *mut SharedBuffer) {
    // Go passes the UI NetMan GLOBAL VA (base + 0x19ED860). That location
    // holds a pointer to the actual UI NetMan instance. Three indirections
    // are needed (matches cmd/sniffer resolveUISendFn):
    //   netman_instance = *(global)
    //   vtable          = *(netman_instance)
    //   ui_send_fn      = *(vtable + 0x28)
    let ui_netman_global = shm_read_u64(shm, OFF_UI_NET_MAN_ADDR);
    let size = shm_read_u32(shm, OFF_PACKET_SIZE);

    if ui_netman_global == 0 {
        shm_write_u32(shm, OFF_ERROR_CODE, 0xE120);
        shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_ERROR);
        shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
        return;
    }

    let max_packet = 4096 - OFF_PACKET_DATA;
    if size == 0 || size as usize > max_packet {
        shm_write_u32(shm, OFF_ERROR_CODE, 0xE121);
        shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_ERROR);
        shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
        return;
    }

    // 1. Deref the global to get the instance pointer (heap).
    let netman_instance =
        core::ptr::read_unaligned(ui_netman_global as *const u64) as usize;
    if netman_instance == 0 {
        shm_write_u32(shm, OFF_ERROR_CODE, 0xE124); // global empty — not in game
        shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_ERROR);
        shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
        return;
    }

    // 2. Deref instance to get vtable pointer (code/rdata).
    let vtable_ptr = core::ptr::read_unaligned(netman_instance as *const u64) as usize;
    if vtable_ptr == 0 {
        shm_write_u32(shm, OFF_ERROR_CODE, 0xE122);
        shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_ERROR);
        shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
        return;
    }

    // 3. Read vtable[5] at offset +0x28.
    let ui_send_fn = core::ptr::read_unaligned((vtable_ptr + 0x28) as *const u64) as usize;
    if ui_send_fn == 0 {
        shm_write_u32(shm, OFF_ERROR_CODE, 0xE123);
        shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_ERROR);
        shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
        return;
    }

    // Build {begin, end} ByteRange on our own stack — the callee reads via r8.
    let pkt_begin = (shm as *const u8).add(OFF_PACKET_DATA);
    let pkt_end = pkt_begin.add(size as usize);
    let range: [u64; 2] = [pkt_begin as u64, pkt_end as u64];
    let range_ptr = range.as_ptr() as usize;

    // Diagnostic stamps for post-mortem debugging from Go side:
    //   shm+0x80 = netman_instance
    //   shm+0x88 = vtable_ptr
    //   shm+0x90 = ui_send_fn
    //   shm+0x98 = first 8 bytes of ui_send_fn code (prologue signature)
    shm_write_u64(shm, 0x80, netman_instance as u64);
    shm_write_u64(shm, 0x88, vtable_ptr as u64);
    shm_write_u64(shm, 0x90, ui_send_fn as u64);
    core::ptr::copy_nonoverlapping(ui_send_fn as *const u8, (shm as *mut u8).add(0x98), 8);

    // Push our VEH to head of chain so we see any AV from this dispatch
    // before Arxan redirects into stack-overflow recovery.
    reinstall_crash_diag_veh();

    // Call ui_send_fn(this=netman_instance, channel=0, range_ptr)
    type UiSendFn = unsafe extern "system" fn(usize, usize, usize) -> usize;
    let f: UiSendFn = core::mem::transmute(ui_send_fn);
    let _ = f(netman_instance, 0, range_ptr);

    shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_DONE);
    shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
}

// ---------------------------------------------------------------------------
// CMD_SEND_DUAL — D2R's vendor/trade dual-send path.
//
// The wrapper function in D2R (at ~RVA 0x117500) does this for trade packets:
//   1. memcpy(global_mirror_buffer, packet, size)
//   2. call send_fn(packet, size, 0)  // normal Game NetMan send
//
// Both writes are required — send_fn alone crashes for opcodes like 0x33 sell
// because D2R's internal state machine reads the mirror buffer as part of the
// session-state sync sequence.
// ---------------------------------------------------------------------------
unsafe fn dispatch_send_dual(shm: *mut SharedBuffer) {
    let fn_addr = shm_read_u64(shm, OFF_FN_SEND_PACKET);
    let mirror = shm_read_u64(shm, OFF_MIRROR_BUF_ADDR);
    let size = shm_read_u32(shm, OFF_PACKET_SIZE);

    if fn_addr == 0 || mirror == 0 {
        shm_write_u32(shm, OFF_ERROR_CODE, 0xE130);
        shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_ERROR);
        shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
        return;
    }

    let max_packet = 4096 - OFF_PACKET_DATA;
    if size == 0 || size as usize > max_packet {
        shm_write_u32(shm, OFF_ERROR_CODE, 0xE131);
        shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_ERROR);
        shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
        return;
    }

    let pkt_ptr = (shm as *const u8).add(OFF_PACKET_DATA);
    let mirror_ptr = mirror as usize as *mut u8;

    // 1. Copy packet into D2R's global mirror buffer.
    core::ptr::copy_nonoverlapping(pkt_ptr, mirror_ptr, size as usize);

    // Push our VEH to head of chain so any AV from send_fn arrives at us
    // first and our memcpy-skip fixup can apply.
    reinstall_crash_diag_veh();

    // 2. Normal Game NetMan send.
    let send_fn: FnSendPacket = core::mem::transmute(fn_addr as usize);
    send_fn(pkt_ptr, size, 0);

    shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_DONE);
    shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
}

// ---------------------------------------------------------------------------
// CMD_CALL_FN — call any function in D2R address space.
// Payload: [fn_addr:u64][nargs:u32][pad:u32][arg0..arg5:u64×6]
// Return value (rax) written back to payload+0.
// ---------------------------------------------------------------------------
unsafe fn dispatch_call_fn(shm: *mut SharedBuffer) {
    let p = (shm as *const u8).add(OFF_PACKET_DATA);
    let fn_addr = core::ptr::read_unaligned(p as *const u64) as usize;
    let arg0 = core::ptr::read_unaligned(p.add(0x10) as *const u64) as usize;
    let arg1 = core::ptr::read_unaligned(p.add(0x18) as *const u64) as usize;
    let arg2 = core::ptr::read_unaligned(p.add(0x20) as *const u64) as usize;
    let arg3 = core::ptr::read_unaligned(p.add(0x28) as *const u64) as usize;
    let arg4 = core::ptr::read_unaligned(p.add(0x30) as *const u64) as usize;
    let arg5 = core::ptr::read_unaligned(p.add(0x38) as *const u64) as usize;

    if fn_addr == 0 {
        shm_write_u32(shm, OFF_ERROR_CODE, 0xE161);
        shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_ERROR);
        shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
        return;
    }

    type Fn6 = unsafe extern "C" fn(usize, usize, usize, usize, usize, usize) -> u64;
    let f: Fn6 = core::mem::transmute(fn_addr);
    let ret = f(arg0, arg1, arg2, arg3, arg4, arg5);

    core::ptr::write_unaligned((shm as *mut u8).add(OFF_PACKET_DATA) as *mut u64, ret);
    shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_DONE);
    shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
}

// ---------------------------------------------------------------------------
// CMD_CALL_FN_GT — call a function on the GAME THREAD via APC.
//
// Same payload as CMD_CALL_FN: [fn_addr:u64][pad:u64][arg0-arg3:u64×4]
// Instead of calling directly (which deadlocks game-logic functions from
// the render thread), we build a small PIC shellcode that reads args from
// the SHM, calls the function, writes the return value back, and sets
// STATUS_DONE. The shellcode is queued as an APC on the game thread
// (whose ID was stored in SHM at init time).
//
// The render thread returns immediately with STATUS_BUSY — the Go side
// polls STATUS_DONE in its normal timeout loop.
// ---------------------------------------------------------------------------
static mut G_APC_SHELLCODE: *mut u8 = core::ptr::null_mut();

unsafe fn dispatch_call_fn_game_thread(shm: *mut SharedBuffer) {
    let p = (shm as *const u8).add(OFF_PACKET_DATA);
    let fn_addr = core::ptr::read_unaligned(p as *const u64);

    if fn_addr == 0 {
        shm_write_u32(shm, OFF_ERROR_CODE, 0xE191);
        shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_ERROR);
        shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
        return;
    }

    let game_tid = shm_read_u32(shm as *const SharedBuffer, OFF_GAME_THREAD_ID);

    // Build shellcode once, reuse on subsequent calls.
    // The shellcode is position-independent; it receives the SHM VA as the
    // APC parameter (RCX on x64 Windows).
    //
    // APC callback signature: void CALLBACK apc_proc(ULONG_PTR param);
    // param = SHM base address in D2R's address space.
    //
    // Shellcode (x86-64, MSVC x64 ABI, 6 args):
    //   push rbx ; push rsi ; sub rsp, 0x38
    //   mov rsi, rcx (SHM base)
    //   load fn_addr, arg0-arg3 into regs
    //   load arg4 → [rsp+0x20], arg5 → [rsp+0x28]
    //   call rax ; write ret+status ; cleanup ; ret
    if G_APC_SHELLCODE.is_null() {
        let sc: &[u8] = &[
            0x53,                                           // push rbx
            0x56,                                           // push rsi
            0x48, 0x83, 0xEC, 0x38,                         // sub rsp, 0x38
            0x48, 0x89, 0xCE,                               // mov rsi, rcx
            0x48, 0x8B, 0x86, 0x00, 0x01, 0x00, 0x00,       // mov rax, [rsi+0x100] (fn_addr)
            // load arg4/arg5 first (before we clobber rbx)
            0x48, 0x8B, 0x9E, 0x30, 0x01, 0x00, 0x00,       // mov rbx, [rsi+0x130] (arg4)
            0x48, 0x89, 0x5C, 0x24, 0x20,                   // mov [rsp+0x20], rbx
            0x48, 0x8B, 0x9E, 0x38, 0x01, 0x00, 0x00,       // mov rbx, [rsi+0x138] (arg5)
            0x48, 0x89, 0x5C, 0x24, 0x28,                   // mov [rsp+0x28], rbx
            // now load reg args
            0x48, 0x8B, 0x8E, 0x10, 0x01, 0x00, 0x00,       // mov rcx, [rsi+0x110] (arg0)
            0x48, 0x8B, 0x96, 0x18, 0x01, 0x00, 0x00,       // mov rdx, [rsi+0x118] (arg1)
            0x4C, 0x8B, 0x86, 0x20, 0x01, 0x00, 0x00,       // mov r8,  [rsi+0x120] (arg2)
            0x4C, 0x8B, 0x8E, 0x28, 0x01, 0x00, 0x00,       // mov r9,  [rsi+0x128] (arg3)
            0xFF, 0xD0,                                     // call rax
            0x48, 0x89, 0x86, 0x00, 0x01, 0x00, 0x00,       // mov [rsi+0x100], rax
            0xC7, 0x46, 0x10, 0x01, 0x00, 0x00, 0x00,       // mov dword [rsi+0x10], 1
            0xC7, 0x46, 0x0C, 0x00, 0x00, 0x00, 0x00,       // mov dword [rsi+0x0C], 0
            0x48, 0x83, 0xC4, 0x38,                         // add rsp, 0x38
            0x5E,                                           // pop rsi
            0x5B,                                           // pop rbx
            0xC3,                                           // ret
        ];
        let mem = VirtualAlloc(
            core::ptr::null(),
            sc.len(),
            0x3000, // MEM_COMMIT | MEM_RESERVE
            0x40,   // PAGE_EXECUTE_READWRITE
        );
        if mem.is_null() {
            shm_write_u32(shm, OFF_ERROR_CODE, 0xE193);
            shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_ERROR);
            shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
            return;
        }
        core::ptr::copy_nonoverlapping(sc.as_ptr(), mem as *mut u8, sc.len());
        G_APC_SHELLCODE = mem as *mut u8;
    }

    let shm_va = shm as *mut core::ffi::c_void;

    // Use explicit game_tid from SHM if set, otherwise fall back to window thread.
    let mut target_tid = game_tid;
    if target_tid == 0 {
        let hwnd = shm_read_u64(shm as *const SharedBuffer, OFF_HWND_D2R) as HWND;
        let mut dummy_pid: DWORD = 0;
        target_tid = GetWindowThreadProcessId(hwnd, &mut dummy_pid);
    }
    if target_tid == 0 {
        shm_write_u32(shm, OFF_ERROR_CODE, 0xE194);
        shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_ERROR);
        shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
        return;
    }

    let h_thread = OpenThread(0x1FFFFF /* THREAD_ALL_ACCESS */, 0, target_tid);
    if h_thread.is_null() {
        shm_write_u32(shm, OFF_ERROR_CODE, 0xE196);
        shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_ERROR);
        shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
        return;
    }

    let st = NtQueueApcThread(
        h_thread,
        G_APC_SHELLCODE as *const core::ffi::c_void,
        shm_va,
        core::ptr::null_mut(),
        core::ptr::null_mut(),
    );
    CloseHandle(h_thread);
    let queued = if st == 0 { 1u32 } else { 0u32 };

    if queued == 0 {
        shm_write_u32(shm, OFF_ERROR_CODE, 0xE195);
        shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_ERROR);
        shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
        return;
    }

    // Don't set STATUS_DONE here — the APC shellcode does it when the game
    // thread executes. The Go side polls with its standard timeout.
    // Leave COMMAND_FLAG set so the render-thread dispatch loop doesn't
    // re-process this command on the next frame.
}

// ---------------------------------------------------------------------------
// CMD_WRITE_MEM — write bytes to any address in D2R process.
// Payload: [dest:u64][size:u32][data...]
// ---------------------------------------------------------------------------
unsafe fn dispatch_write_mem(shm: *mut SharedBuffer) {
    let p = (shm as *const u8).add(OFF_PACKET_DATA);
    let dest = core::ptr::read_unaligned(p as *const u64) as usize;
    let size = core::ptr::read_unaligned(p.add(8) as *const u32) as usize;

    if dest == 0 || size == 0 || size > 2048 {
        shm_write_u32(shm, OFF_ERROR_CODE, 0xE171);
        shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_ERROR);
        shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
        return;
    }

    let src = p.add(0x0C);
    let dst = dest as *mut u8;

    // VirtualProtect the target to PAGE_READWRITE before writing. Some D2R .data
    // pages are PAGE_READONLY under Arxan; writing without changing protection
    // causes an AV that crashes rmod.dll (no SEH wrapping the write loop).
    let mut old_prot: u32 = 0;
    VirtualProtect(dst as _, size, 0x04 /* PAGE_READWRITE */, &mut old_prot);

    for i in 0..size {
        *dst.add(i) = *src.add(i);
    }

    // Restore original protection
    let mut dummy: u32 = 0;
    VirtualProtect(dst as _, size, old_prot, &mut dummy);

    shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_DONE);
    shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
}

#[inline(always)]
unsafe fn shm_write_u64(shm: *mut SharedBuffer, offset: usize, val: u64) {
    core::ptr::write_unaligned((shm as *mut u8).add(offset) as *mut u64, val);
}

// ---------------------------------------------------------------------------
// CMD_SEND_DUAL_GT → dispatch_dual_via_hijack: thread hijack game thread.
//
// SuspendThread → GetThreadContext → modify RIP to point at shellcode that
// calls dual_send_wrap(pkt, size) → SetThreadContext → ResumeThread.
// Zero code patching. Zero IAT modification. Zero hook installation.
//
// Fixed v2: save/restore ALL clobbered regs (rbx), set STATUS_BUSY before
// resume to prevent re-entry, write STATUS_DONE+clear COMMAND_FLAG in
// shellcode BEFORE calling dual_send_wrap so SHM never gets stuck.
// ---------------------------------------------------------------------------
unsafe fn dispatch_dual_via_hijack(shm: *mut SharedBuffer) {
    let dual_fn = shm_read_u64(shm as *const SharedBuffer, OFF_DUAL_SEND_WRAP) as usize;
    if dual_fn == 0 {
        shm_write_u32(shm, OFF_ERROR_CODE, 0xE230);
        shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_ERROR);
        shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
        return;
    }

    let size = shm_read_u32(shm, OFF_PACKET_SIZE);
    if size == 0 || size as usize > (4096 - OFF_PACKET_DATA) {
        shm_write_u32(shm, OFF_ERROR_CODE, 0xE231);
        shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_ERROR);
        shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
        return;
    }

    let game_tid = shm_read_u32(shm as *const SharedBuffer, OFF_GAME_THREAD_ID);
    if game_tid == 0 {
        shm_write_u32(shm, OFF_ERROR_CODE, 0xE232);
        shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_ERROR);
        shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
        return;
    }

    // Diagnostic: write TID to error code so Go can confirm dispatch ran
    shm_write_u32(shm, OFF_ERROR_CODE, 0xD100 | (game_tid & 0xFFFF));

    let h = OpenThread(0x1FFFFF, 0, game_tid); // THREAD_ALL_ACCESS
    if h.is_null() {
        shm_write_u32(shm, OFF_ERROR_CODE, 0xE233);
        shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_ERROR);
        shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
        return;
    }

    let sc = SuspendThread(h);
    shm_write_u32(shm, OFF_ERROR_CODE, 0xD200 | (sc & 0xFF)); // D2xx = suspend count

    let mut ctx = RawContext { data: [0u8; 1232] };
    ctx.set_context_flags(0x10001F); // CONTEXT_ALL
    let gc = GetThreadContext(h, ctx.data.as_mut_ptr() as *mut CONTEXT);
    if gc == 0 {
        shm_write_u32(shm, OFF_ERROR_CODE, 0xE2F0); // GetContext failed
        ResumeThread(h); CloseHandle(h);
        shm_write_u32(shm, OFF_ERROR_CODE, 0xE234);
        shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_ERROR);
        shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
        return;
    }

    let orig_rip = ctx.rip();
    let orig_rsp = ctx.rsp();

    // Alloc packet copy + shellcode in one block
    let block = VirtualAlloc(core::ptr::null(), 4096, 0x3000, 0x40) as *mut u8;
    if block.is_null() {
        ResumeThread(h); CloseHandle(h);
        shm_write_u32(shm, OFF_ERROR_CODE, 0xE235);
        shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_ERROR);
        shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
        return;
    }

    // Layout: [0..512] = packet data, [512..] = shellcode
    let pkt = block;
    let sc = block.add(512);
    core::ptr::copy_nonoverlapping((shm as *const u8).add(OFF_PACKET_DATA), pkt, size as usize);

    let mut w = ThunkWriter { buf: sc, offset: 0 };

    // Save ALL registers we'll clobber
    w.emit(&[0x53]);                         // push rbx
    w.emit(&[0x48, 0x83, 0xEC, 0x28]);       // sub rsp, 0x28 (shadow + align)

    let shm_cmd_addr = (shm as usize + OFF_COMMAND_FLAG) as u64;
    let shm_status_addr = (shm as usize + OFF_STATUS_FLAG) as u64;
    let shm_error_addr = (shm as usize + OFF_ERROR_CODE) as u64;

    // CANARY: write 0xBEEF to error_code to prove shellcode executed
    w.emit(&[0x48, 0xBB]); w.emit(&shm_error_addr.to_le_bytes());
    w.emit(&[0xC7, 0x03, 0xEF, 0xBE, 0x00, 0x00]);  // mov dword [rbx], 0xBEEF

    // Clear command flag (before dual_send_wrap, so SHM won't re-dispatch)
    w.emit(&[0x48, 0xBB]); w.emit(&shm_cmd_addr.to_le_bytes());
    w.emit(&[0xC7, 0x03, 0x00, 0x00, 0x00, 0x00]);

    // Call dual_send_wrap(rcx=pkt, edx=size)
    w.emit(&[0x48, 0xB9]); w.emit(&(pkt as u64).to_le_bytes());
    w.emit(&[0xBA]); w.emit(&(size as u32).to_le_bytes());
    w.emit(&[0x48, 0xB8]); w.emit(&(dual_fn as u64).to_le_bytes());
    w.emit(&[0xFF, 0xD0]);

    // Set STATUS_DONE
    w.emit(&[0x48, 0xBB]); w.emit(&shm_status_addr.to_le_bytes());
    w.emit(&[0xC7, 0x03, 0x01, 0x00, 0x00, 0x00]);

    // Restore
    w.emit(&[0x48, 0x83, 0xC4, 0x28]);
    w.emit(&[0x5B]);

    // Jump back to original RIP
    w.emit(&[0x48, 0xB8]); w.emit(&orig_rip.to_le_bytes());         // mov rax, orig_rip
    w.emit(&[0xFF, 0xE0]);                                           // jmp rax

    // Hijack: set RIP to shellcode, align RSP
    ctx.set_rip(sc as u64);
    ctx.set_rsp((orig_rsp & !0xF) - 8);

    let stc = SetThreadContext(h, ctx.data.as_ptr() as *const CONTEXT);
    if stc == 0 {
        shm_write_u32(shm, OFF_ERROR_CODE, 0xE237); // SetContext FAILED
        VirtualFree(block as _, 0, 0x8000);
        ctx.set_rip(orig_rip);
        ctx.set_rsp(orig_rsp);
        SetThreadContext(h, ctx.data.as_ptr() as *const CONTEXT);
        ResumeThread(h); CloseHandle(h);
        shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_ERROR);
        shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
        return;
    }

    // Diagnostic: SetContext succeeded. Write RIP we set for verification.
    shm_write_u32(shm, OFF_ERROR_CODE, 0xD300 | ((sc as u32) & 0xFF)); // D3xx = setctx OK + suspend count

    // Mark BUSY before resume — prevents dispatch re-entry
    shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_BUSY);

    ResumeThread(h);
    CloseHandle(h);
    // NOTE: block is leaked intentionally — game thread is using it.
    // A production implementation would free it after STATUS_DONE.
}

// ---------------------------------------------------------------------------
// CMD_SEND_DUAL_GT → dispatch_post_key: send WM_KEYDOWN to D2R's HWND.
// The packet data byte[0] = VK code to press.
// Runs from render thread but PostMessageW queues to window thread message loop.
// WndProc processes it identically to a real key press — full game path fires.
// ---------------------------------------------------------------------------
unsafe fn dispatch_post_key(shm: *mut SharedBuffer) {
    let hwnd = shm_read_u64(shm as *const SharedBuffer, OFF_HWND_D2R) as HWND;
    if hwnd.is_null() {
        shm_write_u32(shm, OFF_ERROR_CODE, 0xE210);
        shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_ERROR);
        shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
        return;
    }

    let pkt_ptr = (shm as *const u8).add(OFF_PACKET_DATA);
    let vk = *pkt_ptr as u32;

    // WM_KEYDOWN = 0x0100, lParam: repeat=1, scancode in bits 16-23
    let scan_code = MapVirtualKeyW(vk, 0); // MAPVK_VK_TO_VSC
    let lparam_down = 1u32 | (scan_code << 16);
    PostMessageW(hwnd, 0x0100, vk as usize, lparam_down as usize);

    // Small delay then WM_KEYUP
    // WM_KEYUP = 0x0101, lParam: repeat=1, scancode, bit30=1(was down), bit31=1(transition)
    let lparam_up = 1u32 | (scan_code << 16) | (1 << 30) | (1 << 31);
    PostMessageW(hwnd, 0x0101, vk as usize, lparam_up as usize);

    shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_DONE);
    shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
}

// ---------------------------------------------------------------------------
// Game thread hook: GetTickCount64 detour
//
// GetTickCount64 is called by the game thread every tick. We inline-hook it
// to check SHM for pending CMD_SEND_DUAL_GT commands and dispatch them from
// the game thread context — the only context where dual_send_wrap works.
// ---------------------------------------------------------------------------

/// Called from our GetTickCount64 detour, on whatever thread calls GTC64.
/// We only dispatch CMD_SEND_DUAL_GT here (not general commands).
#[no_mangle]
unsafe extern "C" fn game_tick_dispatch() {
    let shm = G_SHM;
    if shm.is_null() { return; }

    // Diagnostic: bump "GTC64 hook called" counter on every GTC64 invocation.
    // If this stays 0 while D2R is running, the IAT hook isn't firing at all.
    let gtc_count = shm_read_u32(shm as *const SharedBuffer, 0x2080);
    shm_write_u32(shm, 0x2080, gtc_count.wrapping_add(1));

    // Quick check: is there a pending game-thread command?
    if shm_read_u32(shm, OFF_COMMAND_FLAG) == 0 { return; }
    let cmd = shm_read_u32(shm, OFF_COMMAND_TYPE);
    if cmd != CMD_SEND_DUAL_GT { return; }

    // Only process if status is BUSY (set by render thread dispatch).
    if shm_read_u32(shm, OFF_STATUS_FLAG) != STATUS_BUSY { return; }

    let dual_fn = G_DUAL_SEND_WRAP;
    if dual_fn == 0 {
        shm_write_u32(shm, OFF_ERROR_CODE, 0xE200);
        shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_ERROR);
        shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
        return;
    }

    let size = shm_read_u32(shm, OFF_PACKET_SIZE);
    let max_packet = 4096 - OFF_PACKET_DATA;
    if size == 0 || size as usize > max_packet {
        shm_write_u32(shm, OFF_ERROR_CODE, 0xE201);
        shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_ERROR);
        shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
        return;
    }

    let pkt_ptr = (shm as *const u8).add(OFF_PACKET_DATA);

    // Call dual_send_wrap(rcx=pkt_ptr, edx=size) from game thread.
    type FnDualSend = unsafe extern "C" fn(*const u8, u32);
    let f: FnDualSend = core::mem::transmute(dual_fn);
    f(pkt_ptr, size);

    shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_DONE);
    shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
}

/// VEH handler: catches STATUS_GUARD_PAGE_VIOLATION on our target page.
/// When the game thread reads the guarded page, this fires ON THE GAME THREAD.
/// We dispatch dual_send_wrap, then let the access continue.
const STATUS_GUARD_PAGE_VIOLATION: u32 = 0x80000001;
const EXCEPTION_CONTINUE_EXECUTION: i32 = -1;
const EXCEPTION_CONTINUE_SEARCH: i32 = 0;

// Exception codes worth capturing as crashes.
const EXCEPTION_ACCESS_VIOLATION: u32 = 0xC0000005;
const EXCEPTION_ILLEGAL_INSTRUCTION: u32 = 0xC000001D;
const EXCEPTION_PRIV_INSTRUCTION: u32 = 0xC0000096;
const EXCEPTION_STACK_OVERFLOW: u32 = 0xC00000FD;
const EXCEPTION_INT_DIVIDE_BY_ZERO: u32 = 0xC0000094;
const STATUS_STACK_BUFFER_OVERRUN: u32 = 0xC0000409;
const EXCEPTION_BREAKPOINT: u32 = 0x80000003;
const EXCEPTION_SINGLE_STEP: u32 = 0x80000004;

static mut G_CRASH_VEH_INSTALLED: bool = false;
static mut G_CRASH_VEH_HANDLE: *mut core::ffi::c_void = core::ptr::null_mut();

/// VEH handler: captures fatal exception details to SHM so the bot can read
/// them after D2R dies. Returns EXCEPTION_CONTINUE_SEARCH for everything so
/// any other handler (including Arxan's UnhandledExceptionFilter) still runs.
unsafe extern "system" fn crash_diag_veh(info: *mut EXCEPTION_POINTERS) -> i32 {
    if info.is_null() { return EXCEPTION_CONTINUE_SEARCH; }
    let rec = (*info).ExceptionRecord;
    if rec.is_null() { return EXCEPTION_CONTINUE_SEARCH; }
    let shm = G_SHM;
    if shm.is_null() { return EXCEPTION_CONTINUE_SEARCH; }

    let code = (*rec).ExceptionCode;

    // Only capture interesting (likely-fatal) codes. Skip BP/SS/guard-page
    // since those are routine and a different handler usually consumes them.
    let is_fatal = matches!(code,
        EXCEPTION_ACCESS_VIOLATION
        | EXCEPTION_ILLEGAL_INSTRUCTION
        | EXCEPTION_PRIV_INSTRUCTION
        | EXCEPTION_STACK_OVERFLOW
        | EXCEPTION_INT_DIVIDE_BY_ZERO
        | STATUS_STACK_BUFFER_OVERRUN);

    // Always bump the count so we can see *something* fired.
    let prev_count = shm_read_u32(shm as *const SharedBuffer, OFF_CRASH_COUNT);
    shm_write_u32(shm, OFF_CRASH_COUNT, prev_count.wrapping_add(1));

    // Sentinel — write a known constant to 0xD0 on every exception. Lets the
    // bot prove the VEH function runs through this point (pre-fatal-filter).
    // Use volatile write to defeat any LTO that might otherwise consider the
    // store dead (the bot reads it cross-process; the optimiser doesn't know).
    core::ptr::write_volatile((shm as *mut u8).add(0xD0) as *mut u32, 0xCAFEBABE);
    // 0xD4 = current code on every exception (overwriting prior code).
    core::ptr::write_volatile((shm as *mut u8).add(0xD4) as *mut u32, code);

    // Track per-code exception counts so we can confirm whether the original
    // AV (0xC0000005) ever reaches us — separately from the stack overflow
    // (0xC00000FD) we always see when Arxan's recovery recurses.
    //   shm+0xA0  u32  count of 0xC0000005 (AV)
    //   shm+0xA4  u32  count of 0xC00000FD (stack overflow)
    //   shm+0xA8  u32  count of 0xC0000409 (stack-buffer-overrun)
    //   shm+0xAC  u32  most-recent exception code
    // (0x40-0x9F is taken by the protocol — see internal/presenter/protocol.go.
    //  0xA0-0xFF is free.)
    match code {
        EXCEPTION_ACCESS_VIOLATION => {
            let c = shm_read_u32(shm as *const SharedBuffer, 0xA0);
            shm_write_u32(shm, 0xA0, c.wrapping_add(1));
        }
        EXCEPTION_STACK_OVERFLOW => {
            let c = shm_read_u32(shm as *const SharedBuffer, 0xA4);
            shm_write_u32(shm, 0xA4, c.wrapping_add(1));
        }
        STATUS_STACK_BUFFER_OVERRUN => {
            let c = shm_read_u32(shm as *const SharedBuffer, 0xA8);
            shm_write_u32(shm, 0xA8, c.wrapping_add(1));
        }
        _ => {}
    }
    shm_write_u32(shm, 0xAC, code);

    if !is_fatal {
        return EXCEPTION_CONTINUE_SEARCH;
    }

    // Stack overflow: we're running on a near-empty thread stack. Any heavy
    // dumps below could themselves fault, re-enter this VEH, and cascade.
    // Bump already recorded; just propagate so termination proceeds.
    if code == EXCEPTION_STACK_OVERFLOW {
        return EXCEPTION_CONTINUE_SEARCH;
    }

    // Skip rewriting the record only if we already have an AV captured AND
    // this new exception isn't an AV. AV is far more interesting than the
    // stack overflow that Arxan's recovery causes — and on render thread the
    // SO often fires first (because Arxan's own VEH eats the original AV
    // before we see it), but later AVs on other threads still reach us.
    let existing_code = shm_read_u32(shm as *const SharedBuffer, OFF_CRASH_CODE);
    let already_have_av = existing_code == EXCEPTION_ACCESS_VIOLATION;
    let should_skip_record = already_have_av && code != EXCEPTION_ACCESS_VIOLATION;
    if should_skip_record {
        // We have an AV recorded; this is some downstream SO/etc. Try fixup
        // anyway in case it matches (it won't for non-AVs, but cheap to check).
        let ctx = (*info).ContextRecord as *const u8;
        if code == EXCEPTION_ACCESS_VIOLATION && !ctx.is_null() {
            let rip_now = core::ptr::read_unaligned(ctx.add(0xF8) as *const u64);
            if rip_now != 0 && is_user_va(rip_now as usize) && is_user_va((rip_now + 4) as usize)
                && page_readable(rip_now as usize) && page_readable((rip_now + 4) as usize) {
                let bytes_at_rip = core::ptr::read_unaligned(rip_now as *const [u8; 5]);
                let is_stash_memcpy = bytes_at_rip == [0x47, 0x8A, 0x5C, 0x11, 0xF0];
                let fault_va_now: u64 = if (*rec).NumberParameters >= 2 {
                    (*rec).ExceptionInformation[1]
                } else { 0 };
                let faulted_low = (fault_va_now & 0xFFFF) as u32;
                if is_stash_memcpy && faulted_low == 0xFFF0 {
                    let scratch_base = G_STASH_SCRATCH.as_mut_ptr() as u64;
                    let new_r9 = scratch_base.wrapping_add(0x10);
                    let ctx_mut = (*info).ContextRecord as *mut u8;
                    core::ptr::write_unaligned(ctx_mut.add(0xC0) as *mut u64, new_r9);
                    let fixups = shm_read_u32(shm as *const SharedBuffer, OFF_CRASH_FIXUPS);
                    shm_write_u32(shm, OFF_CRASH_FIXUPS, fixups.wrapping_add(1));
                    return EXCEPTION_CONTINUE_EXECUTION;
                }
            }
        }
        return EXCEPTION_CONTINUE_SEARCH;
    }

    // Pull the full integer register file from CONTEXT_AMD64 (winnt.h layout).
    // Offsets are rock-stable across Windows versions.
    let ctx = (*info).ContextRecord as *const u8;
    let mut rip: u64 = 0;
    let mut rsp: u64 = 0;
    if !ctx.is_null() {
        // Standard CONTEXT_AMD64 GPR offsets (bytes from CONTEXT start).
        //   +0x78 Rax  +0x80 Rcx  +0x88 Rdx  +0x90 Rbx  +0x98 Rsp
        //   +0xA0 Rbp  +0xA8 Rsi  +0xB0 Rdi  +0xB8 R8   +0xC0 R9
        //   +0xC8 R10  +0xD0 R11  +0xD8 R12  +0xE0 R13  +0xE8 R14
        //   +0xF0 R15  +0xF8 Rip
        const GPR_OFFSETS: [usize; 16] = [
            0x78, 0x80, 0x88, 0x90, 0x98, 0xA0, 0xA8, 0xB0,
            0xB8, 0xC0, 0xC8, 0xD0, 0xD8, 0xE0, 0xE8, 0xF0,
        ];
        let mut i = 0;
        while i < 16 {
            let val = core::ptr::read_unaligned(ctx.add(GPR_OFFSETS[i]) as *const u64);
            shm_write_u64(shm, OFF_CRASH_REGS + i * 8, val);
            i += 1;
        }
        rip = core::ptr::read_unaligned(ctx.add(0xF8) as *const u64);
        rsp = core::ptr::read_unaligned(ctx.add(0x98) as *const u64);
    }
    let _ = CRASH_REG_COUNT; // suppress unused warning
    if rip == 0 {
        rip = (*rec).ExceptionAddress as u64;
    }

    let fault_va: u64 = if (*rec).NumberParameters >= 2 {
        (*rec).ExceptionInformation[1]
    } else { 0 };
    let fault_type: u32 = if (*rec).NumberParameters >= 1 {
        (*rec).ExceptionInformation[0] as u32
    } else { 0 };

    shm_write_u32(shm, OFF_CRASH_CODE, code);
    shm_write_u32(shm, OFF_CRASH_FLAGS, (*rec).ExceptionFlags);
    shm_write_u32(shm, OFF_CRASH_TID, GetCurrentThreadId());
    shm_write_u32(shm, OFF_CRASH_FAULT_TYPE, fault_type);
    shm_write_u64(shm, OFF_CRASH_RIP, rip);
    shm_write_u64(shm, OFF_CRASH_FAULT_VA, fault_va);
    shm_write_u64(shm, OFF_CRASH_RSP, rsp);

    // Capture top-of-stack return addresses. Without StackWalk we just dump
    // the first N qwords from RSP; bot can post-filter with module ranges.
    // Guard: stack is normally mapped, but a stack-overflow AV points rsp at
    // the guard page that's already unmapped — reading it here would re-fault
    // and cascade inside VEH.
    if rsp != 0 && is_user_va(rsp as usize)
        && page_readable(rsp as usize)
        && page_readable((rsp + (CRASH_FRAME_COUNT as u64 - 1) * 8) as usize) {
        let mut i = 0;
        while i < CRASH_FRAME_COUNT {
            let slot = rsp.wrapping_add((i as u64) * 8) as *const u64;
            let v = core::ptr::read_unaligned(slot);
            shm_write_u64(shm, OFF_CRASH_FRAMES + i * 8, v);
            i += 1;
        }
    }

    // Dump raw bytes around RIP so the bot can disassemble offline. Arxan
    // re-encrypts the page after the function returns, but right now (during
    // exception dispatch) the bytes are still decrypted.
    //
    // Guard: both window endpoints must be in canonical user space AND the
    // pages must be MEM_COMMIT. is_user_va alone filters non-canonical/junk
    // RIPs but does NOT catch canonical-but-unmapped pages — a raw read there
    // re-faults inside VEH, cascades, stack-overflows the handler. SNAPSHOT_ENABLE=1
    // live test 2026-04-17 surfaced this: count=1708 AVs in ~1 s before D2R dies.
    if rip != 0 {
        let start_va = rip.wrapping_sub(CRASH_RIP_BYTES_PRE as u64);
        let end_va   = start_va.wrapping_add(CRASH_RIP_BYTES_LEN as u64);
        if is_user_va(start_va as usize) && is_user_va(end_va as usize)
            && page_readable(start_va as usize) && page_readable(end_va as usize - 1) {
            let start = start_va as *const u8;
            let mut i = 0;
            while i < CRASH_RIP_BYTES_LEN {
                let b = core::ptr::read_unaligned(start.add(i));
                *((shm as *mut u8).add(OFF_CRASH_RIP_BYTES + i)) = b;
                i += 1;
            }
        }
    }

    // For each non-zero return address on the stack, dump bytes around it so
    // we can disassemble the call site too. Skips obvious garbage values.
    // Page-mapped check is REQUIRED — frame returns can point to freshly-
    // freed pages (Arxan keeps releasing trampoline pages after each hook)
    // which pass is_user_va but fault on raw read inside the VEH handler.
    let mut i = 0;
    while i < CRASH_FRAME_COUNT {
        let frame = shm_read_u64(shm as *const SharedBuffer, OFF_CRASH_FRAMES + i * 8);
        let start_va = frame.wrapping_sub(CRASH_FRAME_BYTES_PRE as u64);
        let end_va   = start_va.wrapping_add(CRASH_FRAME_BYTES_LEN as u64);
        let plausible_code = frame > 0x10000 && frame < 0x00007FFFFFFFFFFF
            && is_user_va(start_va as usize) && is_user_va(end_va as usize)
            && page_readable(start_va as usize) && page_readable(end_va as usize - 1);
        if plausible_code {
            let start = start_va as *const u8;
            let dst_off = OFF_CRASH_FRAME_BYTES + i * CRASH_FRAME_BYTES_STRIDE;
            let mut j = 0;
            while j < CRASH_FRAME_BYTES_LEN {
                let b = core::ptr::read_unaligned(start.add(j));
                *((shm as *mut u8).add(dst_off + j)) = b;
                j += 1;
            }
        } else {
            // Zero the slot so bot knows there's no useful data here.
            let dst_off = OFF_CRASH_FRAME_BYTES + i * CRASH_FRAME_BYTES_STRIDE;
            let mut j = 0;
            while j < CRASH_FRAME_BYTES_LEN {
                *((shm as *mut u8).add(dst_off + j)) = 0;
                j += 1;
            }
        }
        i += 1;
    }

    // Mark the record valid LAST so the bot never reads partial data.
    shm_write_u32(shm, OFF_CRASH_VALID, 1);

    // -------------------------------------------------------------------
    // Fault fix-up: if this is the specific stash-move memcpy pattern
    // (fault_va = r9 - 0x10 with r9 page-aligned, RCX=3, RIP has the
    // `47 8a 5c 11 f0` bytes for `mov r11b, [r9+r10-0x10]`), redirect R9
    // into our scratch buffer and let the CPU retry. The scratch is
    // static, lives in our DLL's data segment so it's always valid.
    // -------------------------------------------------------------------
    if code == EXCEPTION_ACCESS_VIOLATION && !ctx.is_null() {
        // Capture LATEST AV details — overwrite on each AV. Useful when the
        // pattern shifts between iterations (Arxan recovery may re-fault at
        // different RIPs).
        //   shm+0xB0  u64  RIP of latest AV
        //   shm+0xB8  u64  fault_va of latest AV
        //   shm+0xC0  u32  fault_type of latest AV
        //   shm+0xC4  u32  sentinel (0xDEADBEEF when this branch runs)
        //   shm+0xC8  16 B raw RIP bytes (instruction at fault)
        shm_write_u64(shm, 0xB0, rip);
        shm_write_u64(shm, 0xB8, fault_va);
        shm_write_u32(shm, 0xC0, fault_type);
        shm_write_u32(shm, 0xC4, 0xDEADBEEF);
        // Copy 16 raw bytes at RIP for offline disassembly.
        // Guard: RIP and RIP+15 canonical AND page-mapped (covers canonical-
        // but-unmapped pages that would re-fault inside the VEH).
        if rip != 0 && is_user_va(rip as usize) && is_user_va((rip + 15) as usize)
            && page_readable(rip as usize) && page_readable((rip + 15) as usize) {
            let mut i = 0;
            while i < 16 {
                let b = core::ptr::read_unaligned((rip as *const u8).add(i));
                *((shm as *mut u8).add(0xC8 + i)) = b;
                i += 1;
            }
        }
    }
    if code == EXCEPTION_ACCESS_VIOLATION && fault_type == 0 && !ctx.is_null()
        && rip != 0 && is_user_va(rip as usize) && is_user_va((rip + 4) as usize)
        && page_readable(rip as usize) && page_readable((rip + 4) as usize)
    {
        // Peek the instruction bytes at RIP — must match `47 8a 5c 11 f0`
        let bytes_at_rip = core::ptr::read_unaligned(rip as *const [u8; 5]);
        let is_stash_memcpy = bytes_at_rip == [0x47, 0x8A, 0x5C, 0x11, 0xF0];
        // And fault_va must be exactly 0x10 below a page-aligned address.
        let faulted_low = (fault_va & 0xFFFF) as u32;
        let looks_like_preheader = faulted_low == 0xFFF0;
        if is_stash_memcpy && looks_like_preheader {
            // Strategy: REDIRECT R9 into our static scratch buffer and retry
            // the faulting instruction. The scratch is initialized to zero
            // and lives in our DLL's writable data segment so it's always
            // mapped. The memcpy reads [r9-0x10 .. r9+0x30] (0x40 bytes
            // total), so we point R9 at &G_STASH_SCRATCH[0x10]. That gives
            // the handler 0x10 bytes of valid "pre-header" (zeros) and 0x30
            // bytes of payload (also zeros). Memcpy completes normally, the
            // outer loop (cmp r10, 0x40; jne) exits at r10=0x40, and
            // execution flows to the outer-loop tail.
            //
            // Semantic caveat: the destination buffer ends up with 64 zero
            // bytes instead of whatever the game would normally store. If
            // downstream code is sensitive to that content we'll see a
            // second fault or a server-side rejection — but at least we
            // get past this specific AV, which is the immediate blocker.
            let scratch_base = G_STASH_SCRATCH.as_mut_ptr() as u64;
            let new_r9 = scratch_base.wrapping_add(0x10);
            let ctx_mut = (*info).ContextRecord as *mut u8;
            core::ptr::write_unaligned(ctx_mut.add(0xC0) as *mut u64, new_r9); // R9

            let fixups = shm_read_u32(shm as *const SharedBuffer, OFF_CRASH_FIXUPS);
            shm_write_u32(shm, OFF_CRASH_FIXUPS, fixups.wrapping_add(1));
            return EXCEPTION_CONTINUE_EXECUTION;
        }
    }

    EXCEPTION_CONTINUE_SEARCH
}

/// Install the crash-diagnostic VEH at the head of the chain. Idempotent.
/// Writes a status code into OFF_CRASH_VALID so the bot can tell whether the
/// install actually succeeded:
///   0xC1 = install entered
///   0xC2 = AddVectoredExceptionHandler returned non-null (success)
///   0xC3 = AddVectoredExceptionHandler returned null (failure)
unsafe fn install_crash_diag_veh(shm: *mut SharedBuffer) {
    if G_CRASH_VEH_INSTALLED { return; }
    shm_write_u32(shm, OFF_CRASH_VALID, 0xC1);
    shm_write_u32(shm, OFF_CRASH_COUNT, 0);
    // First=1 (head of chain) so we run before anything else, including
    // Arxan's UnhandledExceptionFilter. Then return CONTINUE_SEARCH so the
    // exception still propagates to whatever wants to terminate the process.
    let h = AddVectoredExceptionHandler(1, crash_diag_veh);
    if h.is_null() {
        shm_write_u32(shm, OFF_CRASH_VALID, 0xC3);
    } else {
        shm_write_u32(shm, OFF_CRASH_VALID, 0xC2);
        G_CRASH_VEH_INSTALLED = true;
        G_CRASH_VEH_HANDLE = h;
    }
}

/// Push our crash VEH back to the head of the First=1 chain. Arxan (or its
/// dynamic allocator) re-registers its own handlers over time, pushing us
/// down the chain — we miss the original AV and only see the stack overflow
/// that Arxan's recovery triggers. Call this right before any dispatch that
/// is expected to potentially fault, so the faulting exception is seen by
/// us FIRST and our fixup (skip memcpy inner loop) can fire.
///
/// Implementation: register a NEW handler at head without removing the old
/// one. Old handlers linger but that's harmless (they just bump duplicate
/// counts on routine exceptions). The original race — Remove then Add leaves
/// a tiny window with zero handlers — caused more crashes than the Arxan
/// push-down we were trying to fix.
unsafe fn reinstall_crash_diag_veh() {
    let h = AddVectoredExceptionHandler(1, crash_diag_veh);
    if !h.is_null() {
        G_CRASH_VEH_HANDLE = h; // track latest for DLL_PROCESS_DETACH cleanup
    }
}

/// Re-register the HWBP SS handler at the head of the First=1 VEH chain.
/// Arxan may register its own VEH between game frames; by periodically
/// re-inserting ours we maximize the chance that SS exceptions reach us
/// before Arxan swallows them.
unsafe fn reinstall_hwbp_veh() {
    if !G_HWBP_INSTALLED { return; }
    if !G_HWBP_VEH_HANDLE.is_null() {
        RemoveVectoredExceptionHandler(G_HWBP_VEH_HANDLE);
        G_HWBP_VEH_HANDLE = core::ptr::null_mut();
    }
    let h = AddVectoredExceptionHandler(1, hwbp_ss_veh);
    if !h.is_null() {
        G_HWBP_VEH_HANDLE = h;
    }
}

unsafe extern "system" fn guard_page_veh(info: *mut EXCEPTION_POINTERS) -> i32 {
    if info.is_null() { return EXCEPTION_CONTINUE_SEARCH; }
    let rec = (*info).ExceptionRecord;
    if rec.is_null() { return EXCEPTION_CONTINUE_SEARCH; }

    if (*rec).ExceptionCode != STATUS_GUARD_PAGE_VIOLATION { return EXCEPTION_CONTINUE_SEARCH; }
    if !G_GUARD_PENDING { return EXCEPTION_CONTINUE_SEARCH; }

    let fault_addr = if (*rec).NumberParameters >= 2 {
        (*rec).ExceptionInformation[1] as usize
    } else { 0 };
    let fault_page = fault_addr & !0xFFF;
    let guard_page = G_GUARD_PAGE_ADDR & !0xFFF;
    if fault_page != guard_page { return EXCEPTION_CONTINUE_SEARCH; }

    // CRITICAL: only dispatch on the GAME THREAD
    let shm = G_SHM;
    if shm.is_null() { return EXCEPTION_CONTINUE_EXECUTION; }
    let game_tid = shm_read_u32(shm as *const SharedBuffer, OFF_GAME_THREAD_ID);
    let current_tid = GetCurrentThreadId();
    // Mirror buffer is ONLY written by game thread (from D2R's dual_send_wrap).
    // Any write here IS the game thread — trust it and dispatch immediately.
    // Also: auto-detect game_tid by recording current_tid on first write.
    if !G_SHM.is_null() && game_tid == 0 {
        shm_write_u32(G_SHM, OFF_GAME_THREAD_ID, current_tid);
    }

    // WE ARE ON THE GAME THREAD! Dispatch dual_send_wrap.
    G_GUARD_PENDING = false;
    game_tick_dispatch();

    EXCEPTION_CONTINUE_EXECUTION
}

/// Install VEH + guard page mechanism for game thread dispatch.
/// Sets PAGE_GUARD on WidgetStatesOffset page. Game thread reads it every tick.
unsafe fn install_game_thread_hook() -> bool {
    let shm = G_SHM;
    if !shm.is_null() { shm_write_u32(shm, OFF_ERROR_CODE, 0xD601); }

    // Install VEH handler (first time only)
    if G_GAME_HOOK_TRAMPOLINE.is_null() {
        let h = AddVectoredExceptionHandler(1, guard_page_veh);
        if h.is_null() {
            if !shm.is_null() { shm_write_u32(shm, OFF_ERROR_CODE, 0xD602); }
            return false;
        }
        G_GAME_HOOK_TRAMPOLINE = h as *const u8; // non-null = installed
    }

    // Target: MIRROR BUFFER page @ 0x1F51000. Only game thread writes here
    // (via D2R's internal dual_send_wrap). Any write from WITHIN D2R = game thread.
    // Our own WriteProcessMemory from outside doesn't trigger VEH (different process).
    let d2r_base = GetModuleHandleA(core::ptr::null()) as usize;
    let target = d2r_base + 0x1F51000;
    let page = target & !0xFFF;
    G_GUARD_PAGE_ADDR = page;
    G_GUARD_PENDING = true;
    G_GUARD_REARM_COUNT = 0;

    // Set PAGE_GUARD
    let mut old_prot: u32 = 0;
    let ok = VirtualProtect(page as _, 0x1000, 0x104 /* PAGE_READWRITE | PAGE_GUARD */, &mut old_prot);
    if ok == 0 {
        // Try with original protection + guard
        VirtualProtect(page as _, 0x1000, old_prot | 0x100 /* PAGE_GUARD */, &mut old_prot);
    }
    G_GUARD_PAGE_OLDPROT = old_prot;

    if !shm.is_null() { shm_write_u32(shm, OFF_ERROR_CODE, 0xD6FF); } // success
    true
}

/// DEAD CODE — kept for reference. Original inline detour approach (blocked by Arxan).
#[allow(dead_code)]
unsafe fn install_game_thread_hook_old() -> bool {
    // Strategy: overwrite game thread's stack return address.
    // Game thread is in WaitForSingleObjectEx, return addr = KERNELBASE+0x226EE.
    // We replace it with our shellcode address. When the wait returns,
    // game thread runs our dispatch then jumps to original return.
    // No code patching. No SetThreadContext. Just stack memory write.

    let shm = G_SHM;
    let game_tid = if !shm.is_null() {
        shm_read_u32(shm as *const SharedBuffer, OFF_GAME_THREAD_ID)
    } else { 0 };
    if game_tid == 0 {
        if !shm.is_null() { shm_write_u32(shm, OFF_ERROR_CODE, 0xD501); }
        return false;
    }

    let h = OpenThread(0x1FFFFF, 0, game_tid); // THREAD_ALL_ACCESS
    if h.is_null() {
        if !shm.is_null() { shm_write_u32(shm, OFF_ERROR_CODE, 0xD502); }
        return false;
    }

    SuspendThread(h);
    if !shm.is_null() { shm_write_u32(shm, OFF_ERROR_CODE, 0xD503); }

    // Get TEB via NtQueryInformationThread (bypass GetThreadContext which Arxan blocks)
    let mut tbi: [u8; 48] = [0u8; 48];
    let mut ret_len: u32 = 0;
    let st = NtQueryInformationThread(h, 0, tbi.as_mut_ptr() as _, 48, &mut ret_len);
    if st != 0 {
        if !shm.is_null() { shm_write_u32(shm, OFF_ERROR_CODE, 0xD505); }
        ResumeThread(h); CloseHandle(h);
        return false;
    }

    // TEB address at offset 4 in THREAD_BASIC_INFORMATION (after ExitStatus u32 + pad)
    let teb_addr = core::ptr::read_unaligned(tbi.as_ptr().add(8) as *const u64) as usize;
    if teb_addr == 0 {
        if !shm.is_null() { shm_write_u32(shm, OFF_ERROR_CODE, 0xD506); }
        ResumeThread(h); CloseHandle(h);
        return false;
    }

    // Read TEB: StackBase at +0x08, StackLimit at +0x10
    let stack_base = core::ptr::read_unaligned((teb_addr + 8) as *const u64) as usize;
    let stack_limit = core::ptr::read_unaligned((teb_addr + 16) as *const u64) as usize;
    if !shm.is_null() { shm_write_u32(shm, OFF_ERROR_CODE, 0xD507); }

    let kernelbase = GetModuleHandleA(b"KERNELBASE.dll\0".as_ptr()) as usize;
    if kernelbase == 0 {
        if !shm.is_null() { shm_write_u32(shm, OFF_ERROR_CODE, 0xD508); }
        ResumeThread(h); CloseHandle(h);
        return false;
    }
    let target_ret = kernelbase + 0x226EE;

    // Scan stack from top (stack_base - scan_size) for target return address
    let scan_size = core::cmp::min(stack_base - stack_limit, 4096);
    let scan_start = stack_base - scan_size;

    let mut found = false;
    let mut ret_stack_addr: usize = 0;
    for i in (0..scan_size).step_by(8) {
        let addr = scan_start + i;
        let val = core::ptr::read_unaligned(addr as *const u64) as usize;
        if val == target_ret {
            ret_stack_addr = addr;
            found = true;
            break;
        }
    }

    if !found {
        if !shm.is_null() { shm_write_u32(shm, OFF_ERROR_CODE, 0xD509); }
        ResumeThread(h); CloseHandle(h);
        return false;
    }
    let ret_offset = ret_stack_addr - scan_start; // for compatibility

    if !shm.is_null() { shm_write_u32(shm, OFF_ERROR_CODE, 0xD490); } // found, building shellcode

    // Build shellcode: dispatch + jump to original return
    let sc_size = 256;
    let sc = VirtualAlloc(core::ptr::null(), sc_size, 0x3000, 0x40) as *mut u8;
    if sc.is_null() {
        ResumeThread(h); CloseHandle(h);
        return false;
    }

    let mut w = ThunkWriter { buf: sc, offset: 0 };

    // Save regs
    w.emit(&[0x50]); // push rax
    w.emit(&[0x51]); // push rcx
    w.emit(&[0x52]); // push rdx
    w.emit(&[0x41, 0x50]); // push r8
    w.emit(&[0x41, 0x51]); // push r9
    w.emit(&[0x41, 0x52]); // push r10
    w.emit(&[0x41, 0x53]); // push r11
    w.emit(&[0x48, 0x83, 0xEC, 0x28]); // sub rsp, 0x28

    let dispatch_addr = game_tick_dispatch as *const () as usize;
    w.emit(&[0x48, 0xB8]);
    w.emit(&(dispatch_addr as u64).to_le_bytes());
    w.emit(&[0xFF, 0xD0]); // call rax

    w.emit(&[0x48, 0x83, 0xC4, 0x28]); // add rsp, 0x28
    w.emit(&[0x41, 0x5B]); // pop r11
    w.emit(&[0x41, 0x5A]); // pop r10
    w.emit(&[0x41, 0x59]); // pop r9
    w.emit(&[0x41, 0x58]); // pop r8
    w.emit(&[0x5A]); // pop rdx
    w.emit(&[0x59]); // pop rcx
    w.emit(&[0x58]); // pop rax

    // Jump to original return address
    w.emit(&[0x48, 0xB8]);
    w.emit(&(target_ret as u64).to_le_bytes());
    w.emit(&[0xFF, 0xE0]); // jmp rax

    // Overwrite the return address on the stack
    let sc_addr = sc as u64;
    core::ptr::copy_nonoverlapping(
        &sc_addr as *const u64 as *const u8,
        ret_stack_addr as *mut u8,
        8,
    );

    G_GAME_HOOK_TRAMPOLINE = sc; // mark as installed

    if !shm.is_null() { shm_write_u32(shm, OFF_ERROR_CODE, 0xD4FF); } // SUCCESS

    ResumeThread(h);
    CloseHandle(h);
    true
}

unsafe fn install_game_hook_at(target: usize) -> bool {
    let shm = G_SHM;
    // Breadcrumb 1: entering install
    if !shm.is_null() { shm_write_u32(shm, OFF_ERROR_CODE, 0xD401); }

    let steal_bytes: usize = 18;

    // Breadcrumb 2: reading target bytes
    if !shm.is_null() { shm_write_u32(shm, OFF_ERROR_CODE, 0xD402); }
    let orig_disp = core::ptr::read_unaligned((target + 12 + 3) as *const i32);
    let cookie_addr = ((target + 12 + 7) as i64 + orig_disp as i64) as u64;
    if !shm.is_null() { shm_write_u32(shm, OFF_ERROR_CODE, 0xD403); }

    // Allocate trampoline (any address — we use absolute addressing)
    // Layout: [push rsi; push rdi; sub rsp,0x58; mov rsi,r8; mov edi,ecx]  (12 bytes, no fixup needed)
    //         [mov rax, imm64; mov rax,[rax]]  (12 bytes, replaces rip-relative mov)
    //         [jmp abs target+18]
    if !shm.is_null() { shm_write_u32(shm, OFF_ERROR_CODE, 0xD410); } // alloc tramp

    let tramp_size = 12 + 12 + 14 + 16;
    let tramp = VirtualAlloc(core::ptr::null(), tramp_size, 0x3000, 0x40) as *mut u8;
    if tramp.is_null() { return false; }

    if !shm.is_null() { shm_write_u32(shm, OFF_ERROR_CODE, 0xD420); } // copy stolen

    let mut tw = ThunkWriter { buf: tramp, offset: 0 };
    let src = target as *const u8;
    for i in 0..12usize {
        tw.emit(&[*src.add(i)]);
    }
    tw.emit(&[0x48, 0xB8]); tw.emit(&cookie_addr.to_le_bytes());
    tw.emit(&[0x48, 0x8B, 0x00]);
    write_abs_jmp(tw.current(), target + steal_bytes);
    G_GAME_HOOK_TRAMPOLINE = tramp;

    if !shm.is_null() { shm_write_u32(shm, OFF_ERROR_CODE, 0xD430); } // alloc thunk

    let thunk_size = 256;
    let thunk = VirtualAlloc(core::ptr::null(), thunk_size, 0x3000, 0x40) as *mut u8;
    if thunk.is_null() { return false; }

    if !shm.is_null() { shm_write_u32(shm, OFF_ERROR_CODE, 0xD440); } // build thunk

    let mut w = ThunkWriter { buf: thunk, offset: 0 };
    w.emit(&[0x50]);
    w.emit(&[0x51]);
    w.emit(&[0x52]);
    w.emit(&[0x41, 0x50]);
    w.emit(&[0x41, 0x51]);
    w.emit(&[0x41, 0x52]);
    w.emit(&[0x41, 0x53]);
    w.emit(&[0x48, 0x83, 0xEC, 0x28]);
    let dispatch_addr = game_tick_dispatch as *const () as usize;
    w.emit(&[0x48, 0xB8]);
    w.emit(&(dispatch_addr as u64).to_le_bytes());
    w.emit(&[0xFF, 0xD0]);
    w.emit(&[0x48, 0x83, 0xC4, 0x28]);
    w.emit(&[0x41, 0x5B]);
    w.emit(&[0x41, 0x5A]);
    w.emit(&[0x41, 0x59]);
    w.emit(&[0x41, 0x58]);
    w.emit(&[0x5A]);
    w.emit(&[0x59]);
    w.emit(&[0x58]);
    write_abs_jmp(w.current(), tramp as usize);

    if !shm.is_null() { shm_write_u32(shm, OFF_ERROR_CODE, 0xD450); } // VirtualProtect

    let mut old_prot: u32 = 0;
    VirtualProtect(target as _, steal_bytes, 0x40, &mut old_prot);

    if !shm.is_null() { shm_write_u32(shm, OFF_ERROR_CODE, 0xD460); } // write detour

    write_abs_jmp(target as *mut u8, thunk as usize);
    for i in 14..steal_bytes {
        *((target + i) as *mut u8) = 0x90;
    }

    if !shm.is_null() { shm_write_u32(shm, OFF_ERROR_CODE, 0xD470); } // restore prot

    VirtualProtect(target as _, steal_bytes, old_prot, &mut old_prot);
    FlushInstructionCache(GetCurrentProcess(), target as _, steal_bytes);

    if !shm.is_null() { shm_write_u32(shm, OFF_ERROR_CODE, 0xD4FF); } // SUCCESS

    true
}

/// Install IAT hook on GetTickCount64.
/// Instead of patching kernel32.dll code (triggers Arxan antitamper), we
/// overwrite D2R's IAT entry for GetTickCount64 with our wrapper function.
/// The wrapper calls game_tick_dispatch() then chains to the real GTC64.
/// IAT is in D2R's .data section — safe to patch, no system DLL modification.
unsafe fn install_gtc64_hook() -> bool {
    // Diagnostic: write install progress/result to SHM at 0x2084 so we can
    // see from /debug/hwbp/status whether install succeeded and where it
    // failed if it didn't.
    let write_diag = |code: u32| {
        if !G_SHM.is_null() {
            shm_write_u32(G_SHM, 0x2084, code);
        }
    };
    write_diag(0x1001); // entered

    // Resolve real GetTickCount64 address
    let kernel32 = GetModuleHandleA(b"kernel32.dll\0".as_ptr());
    if kernel32.is_null() { write_diag(0x1E02); return false; }
    let real_gtc64 = GetProcAddress(kernel32, b"GetTickCount64\0".as_ptr());
    if real_gtc64.is_null() { write_diag(0x1E03); return false; }
    G_GTC64_TRAMPOLINE = real_gtc64 as *const u8;
    write_diag(0x1002);

    // Find D2R's IAT entry for GetTickCount64 by scanning the PE header
    let d2r_base = GetModuleHandleA(core::ptr::null()) as usize;
    if d2r_base == 0 { write_diag(0x1E04); return false; }

    let iat_entry = find_iat_entry(d2r_base, real_gtc64 as usize);
    if iat_entry == 0 {
        // Try GetTickCount (32-bit) as fallback — some D2R code uses that.
        write_diag(0x1005);
        let real_gtc = GetProcAddress(kernel32, b"GetTickCount\0".as_ptr());
        if real_gtc.is_null() { write_diag(0x1E06); return false; }
        let iat_entry2 = find_iat_entry(d2r_base, real_gtc as usize);
        if iat_entry2 == 0 {
            // Try timeGetTime
            write_diag(0x1007);
            // Try winmm!timeGetTime.
            let winmm = GetModuleHandleA(b"winmm.dll\0".as_ptr());
            if !winmm.is_null() {
                let real_tgt = GetProcAddress(winmm, b"timeGetTime\0".as_ptr());
                if !real_tgt.is_null() {
                    let iat_e = find_iat_entry(d2r_base, real_tgt as usize);
                    if iat_e != 0 {
                        write_diag(0x100B);
                        G_GTC64_TRAMPOLINE = real_tgt as *const u8;
                        return install_gtc64_hook_at(iat_e, real_tgt as usize);
                    }
                }
            }
            // Try kernel32!QueryPerformanceCounter — heavily used by games.
            write_diag(0x100D);
            let real_qpc = GetProcAddress(kernel32, b"QueryPerformanceCounter\0".as_ptr());
            if !real_qpc.is_null() {
                let iat_e = find_iat_entry(d2r_base, real_qpc as usize);
                if iat_e != 0 {
                    write_diag(0x100E);
                    G_GTC64_TRAMPOLINE = real_qpc as *const u8;
                    return install_gtc64_hook_at(iat_e, real_qpc as usize);
                }
            }
            // Try kernel32!Sleep — called every frame by frame limiters.
            write_diag(0x100F);
            let real_sleep = GetProcAddress(kernel32, b"Sleep\0".as_ptr());
            if !real_sleep.is_null() {
                let iat_e = find_iat_entry(d2r_base, real_sleep as usize);
                if iat_e != 0 {
                    write_diag(0x1010);
                    G_GTC64_TRAMPOLINE = real_sleep as *const u8;
                    return install_gtc64_hook_at(iat_e, real_sleep as usize);
                }
            }
            // Last-ditch: scan every kernel32 OR kernelbase export. Modern
            // Windows forwards most kernel32 imports to kernelbase, so the IAT
            // entries actually point into kernelbase.dll.
            write_diag(0x1011);
            let fallback_iat = find_any_kernel32_iat_entry(d2r_base, kernel32 as usize);
            if fallback_iat.0 != 0 {
                write_diag(0x1012);
                G_GTC64_TRAMPOLINE = fallback_iat.1 as *const u8;
                return install_gtc64_hook_at(fallback_iat.0, fallback_iat.1);
            }
            write_diag(0x1013);
            let kernelbase = GetModuleHandleA(b"KERNELBASE.dll\0".as_ptr());
            if !kernelbase.is_null() {
                let fb2 = find_any_kernel32_iat_entry(d2r_base, kernelbase as usize);
                if fb2.0 != 0 {
                    write_diag(0x1014);
                    G_GTC64_TRAMPOLINE = fb2.1 as *const u8;
                    return install_gtc64_hook_at(fb2.0, fb2.1);
                }
            }
            write_diag(0x1015);
            // Record first 4 IAT entries' target addresses to SHM so we can see
            // what D2R actually imports from the outside.
            dump_first_iat_entries(d2r_base);
            write_diag(0x1E0A);
            return false;
        }
        write_diag(0x100C);
        G_GTC64_TRAMPOLINE = real_gtc as *const u8;
        return install_gtc64_hook_at(iat_entry2, real_gtc as usize);
    }
    write_diag(0x1003);
    install_gtc64_hook_at(iat_entry, real_gtc64 as usize)
}

unsafe fn install_gtc64_hook_at(iat_entry: usize, real_fn: usize) -> bool {
    let write_diag = |code: u32| {
        if !G_SHM.is_null() { shm_write_u32(G_SHM, 0x2084, code); }
    };
    write_diag(0x2001); // entered hook_at

    // Build wrapper: call game_tick_dispatch, then tail-call real GTC64
    let thunk_size = 128;
    let thunk = VirtualAlloc(core::ptr::null(), thunk_size, 0x3000, 0x40) as *mut u8;
    if thunk.is_null() { write_diag(0x2E02); return false; }
    let _ = real_fn; // silence unused warning (addr captured below via G_GTC64_TRAMPOLINE)
    write_diag(0x2002);

    let mut w = ThunkWriter { buf: thunk, offset: 0 };

    // Save volatile regs (GTC64 clobbers rax, rcx, rdx, r8-r11)
    w.emit(&[0x50]);                         // push rax
    w.emit(&[0x51]);                         // push rcx
    w.emit(&[0x52]);                         // push rdx
    w.emit(&[0x41, 0x50]);                   // push r8
    w.emit(&[0x41, 0x51]);                   // push r9
    w.emit(&[0x41, 0x52]);                   // push r10
    w.emit(&[0x41, 0x53]);                   // push r11
    w.emit(&[0x48, 0x83, 0xEC, 0x28]);       // sub rsp, 0x28

    // call game_tick_dispatch
    let dispatch_addr = game_tick_dispatch as *const () as usize;
    w.emit(&[0x48, 0xB8]);                   // mov rax, imm64
    w.emit(&(dispatch_addr as u64).to_le_bytes());
    w.emit(&[0xFF, 0xD0]);                   // call rax

    // Restore
    w.emit(&[0x48, 0x83, 0xC4, 0x28]);       // add rsp, 0x28
    w.emit(&[0x41, 0x5B]);                   // pop r11
    w.emit(&[0x41, 0x5A]);                   // pop r10
    w.emit(&[0x41, 0x59]);                   // pop r9
    w.emit(&[0x41, 0x58]);                   // pop r8
    w.emit(&[0x5A]);                         // pop rdx
    w.emit(&[0x59]);                         // pop rcx
    w.emit(&[0x58]);                         // pop rax

    // JMP to real target (tail call to GTC64 or fallback timeGetTime etc.)
    let real_addr = real_fn;
    w.emit(&[0x48, 0xB8]);                   // mov rax, imm64
    w.emit(&(real_addr as u64).to_le_bytes());
    w.emit(&[0xFF, 0xE0]);                   // jmp rax
    write_diag(0x2003);

    // Patch IAT entry: overwrite pointer with our thunk address
    let mut old_prot: u32 = 0;
    VirtualProtect(iat_entry as _, 8, 0x04 /* PAGE_READWRITE */, &mut old_prot);
    *(iat_entry as *mut usize) = thunk as usize;
    VirtualProtect(iat_entry as _, 8, old_prot, &mut old_prot);
    write_diag(0x20FF); // success

    true
}

/// Dump first 8 IAT entries' values to SHM 0x2088..0x20C8 for diagnostics.
unsafe fn dump_first_iat_entries(module_base: usize) {
    if G_SHM.is_null() { return; }
    let dos = module_base as *const u8;
    let e_lfanew = *(dos.add(0x3C) as *const u32) as usize;
    let nt = dos.add(e_lfanew);
    let import_dir_rva = *(nt.add(0x18 + 0x70 + 8) as *const u32) as usize;
    if import_dir_rva == 0 { return; }
    let mut desc = module_base + import_dir_rva;
    let mut count = 0;
    loop {
        let first_thunk = *((desc as *const u32).add(4)) as usize;
        if first_thunk == 0 { break; }
        let mut iat_slot = module_base + first_thunk;
        loop {
            let entry = *(iat_slot as *const usize);
            if entry == 0 { break; }
            if count < 8 {
                shm_write_u64(G_SHM, 0x2088 + count * 8, entry as u64);
                count += 1;
            }
            iat_slot += 8;
        }
        desc += 20;
        if count >= 8 { break; }
    }
}

/// Scan D2R's IAT for ANY entry in the kernel32 module. Returns (iat_slot,
/// function_addr) of the first match. Used as a last-ditch fallback for the
/// game-thread hook target when D2R doesn't import the expected well-known
/// functions (GetTickCount, Sleep, etc). The hook will fire whenever D2R
/// calls that kernel32 function — still on a game thread for ordinary imports.
unsafe fn find_any_kernel32_iat_entry(module_base: usize, kernel32_base: usize) -> (usize, usize) {
    let dos = module_base as *const u8;
    let e_lfanew = *(dos.add(0x3C) as *const u32) as usize;
    let nt = dos.add(e_lfanew);
    let import_dir_rva = *(nt.add(0x18 + 0x70 + 8) as *const u32) as usize;
    if import_dir_rva == 0 { return (0, 0); }
    let kernel32_end = kernel32_base + 0x200000; // ~2MB conservative bound
    let mut desc = module_base + import_dir_rva;
    loop {
        let first_thunk = *((desc as *const u32).add(4)) as usize;
        if first_thunk == 0 { break; }
        let mut iat_slot = module_base + first_thunk;
        loop {
            let entry = *(iat_slot as *const usize);
            if entry == 0 { break; }
            if entry >= kernel32_base && entry < kernel32_end {
                return (iat_slot, entry);
            }
            iat_slot += 8;
        }
        desc += 20;
    }
    (0, 0)
}

/// Scan D2R's PE import tables to find the IAT slot holding `target_fn_addr`.
unsafe fn find_iat_entry(module_base: usize, target_fn_addr: usize) -> usize {
    let dos = module_base as *const u8;
    let e_lfanew = *(dos.add(0x3C) as *const u32) as usize;
    let nt = dos.add(e_lfanew);
    // OptionalHeader offset for x64: nt + 0x18, DataDirectory at +0x70 (Import Table = index 1)
    let import_dir_rva = *(nt.add(0x18 + 0x70 + 8) as *const u32) as usize; // DD[1].VirtualAddress
    if import_dir_rva == 0 { return 0; }

    let mut desc = module_base + import_dir_rva;
    // IMAGE_IMPORT_DESCRIPTOR: 5 DWORDs (20 bytes each)
    loop {
        let orig_first_thunk = *(desc as *const u32) as usize; // OriginalFirstThunk (name table)
        let first_thunk = *((desc as *const u32).add(4)) as usize; // FirstThunk (IAT)
        if orig_first_thunk == 0 && first_thunk == 0 { break; }

        // Walk IAT entries
        let mut iat_slot = module_base + first_thunk;
        loop {
            let entry = *(iat_slot as *const usize);
            if entry == 0 { break; }
            if entry == target_fn_addr {
                return iat_slot;
            }
            iat_slot += 8; // next pointer (x64)
        }
        desc += 20; // next IMAGE_IMPORT_DESCRIPTOR
    }
    0
}

// ===========================================================================
// DR0 PERSIST PROBE (Arxan diagnostic)
//
// Question: Does Arxan revert DR0 after we SetThreadContext on D2R threads?
// If YES: HWBP-based packet tracing is dead-on-arrival because our DR0 will
//         be cleared before the breakpoint can ever fire.
// If NO:  HWBP path is viable; we can proceed with full HWBP tracer.
//
// Method: enumerate D2R threads, on each:
//   1. OpenThread + SuspendThread
//   2. GetThreadContext (CONTEXT_FULL | CONTEXT_DEBUG_REGISTERS = 0x10001B)
//   3. Save original DR0 / DR7
//   4. Write DR0 = test pattern, DR7 = enable bit 0
//   5. SetThreadContext
//   6. GetThreadContext AGAIN
//   7. Read DR0 — if equals test pattern, persisted; if 0, Arxan stripped it
//   8. Restore original DR0/DR7 + SetThreadContext (don't leave garbage)
//   9. ResumeThread + CloseHandle
//   10. Push entry to SHM ring at OFF_DRPROBE_ENTRIES
// ===========================================================================

unsafe fn dispatch_dr_probe(shm: *mut SharedBuffer) {
    shm_write_u32(shm, OFF_DRPROBE_VALID, 1); // running
    shm_write_u32(shm, OFF_DRPROBE_TOTAL, 0);
    shm_write_u32(shm, OFF_DRPROBE_OK, 0);
    shm_write_u32(shm, OFF_DRPROBE_REVERT, 0);
    shm_write_u32(shm, OFF_DRPROBE_ERR, 0);
    shm_write_u32(shm, OFF_DRPROBE_NUM, 0);

    let cur_pid = GetCurrentProcessId();
    let cur_tid = GetCurrentThreadId();

    // TH32CS_SNAPTHREAD = 0x00000004
    let snap = CreateToolhelp32Snapshot(0x00000004, 0);
    if snap.is_null() || snap == (-1isize as HANDLE) {
        shm_write_u32(shm, OFF_DRPROBE_VALID, 0xEE01);
        shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_ERROR);
        shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
        return;
    }

    let mut te = THREADENTRY32 {
        dwSize: core::mem::size_of::<THREADENTRY32>() as u32,
        cntUsage: 0, th32ThreadID: 0, th32OwnerProcessID: 0,
        tpBasePri: 0, tpDeltaPri: 0, dwFlags: 0,
    };

    if Thread32First(snap, &mut te) == 0 {
        CloseHandle(snap);
        shm_write_u32(shm, OFF_DRPROBE_VALID, 0xEE02);
        shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_ERROR);
        shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
        return;
    }

    let mut total: u32 = 0;
    let mut ok: u32 = 0;
    let mut revert: u32 = 0;
    let mut err: u32 = 0;
    let mut num_entries: u32 = 0;

    loop {
        if te.th32OwnerProcessID == cur_pid && te.th32ThreadID != cur_tid {
            total += 1;
            let r = probe_thread(te.th32ThreadID);

            // Push entry regardless of outcome (so we can see the failure mode).
            if (num_entries as usize) < DRPROBE_MAX_ENTRIES {
                let off = OFF_DRPROBE_ENTRIES + (num_entries as usize) * DRPROBE_ENTRY_SIZE;
                shm_write_u32(shm, off + 0x00, te.th32ThreadID);
                shm_write_u32(shm, off + 0x04, r.step_failed);
                shm_write_u32(shm, off + 0x08, r.last_err);
                shm_write_u32(shm, off + 0x0C, r.dr7_orig as u32);
                shm_write_u64(shm, off + 0x10, r.dr0_orig);
                shm_write_u64(shm, off + 0x18, r.dr0_after);
                num_entries += 1;
            }

            if r.step_failed != 0 {
                err += 1;
            } else if r.dr0_after == DRPROBE_TEST_DR0 {
                ok += 1;
            } else {
                revert += 1;
            }
        }

        te.dwSize = core::mem::size_of::<THREADENTRY32>() as u32;
        if Thread32Next(snap, &mut te) == 0 {
            break;
        }
    }

    CloseHandle(snap);

    shm_write_u32(shm, OFF_DRPROBE_TOTAL, total);
    shm_write_u32(shm, OFF_DRPROBE_OK, ok);
    shm_write_u32(shm, OFF_DRPROBE_REVERT, revert);
    shm_write_u32(shm, OFF_DRPROBE_ERR, err);
    shm_write_u32(shm, OFF_DRPROBE_NUM, num_entries);
    shm_write_u32(shm, OFF_DRPROBE_VALID, 2); // done
    shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_DONE);
    shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
}

struct DrProbeResult {
    step_failed: u32,  // 0=ok 1=open 2=suspend 3=getctx1 4=setctx 5=getctx2
    last_err: u32,
    dr0_orig: u64,
    dr7_orig: u64,
    dr0_after: u64,
}

unsafe fn probe_thread(tid: u32) -> DrProbeResult {
    let mut r = DrProbeResult { step_failed: 0, last_err: 0, dr0_orig: 0, dr7_orig: 0, dr0_after: 0 };

    // THREAD_ALL_ACCESS = 0x1FFFFF
    let h = OpenThread(0x1FFFFF, 0, tid);
    if h.is_null() {
        r.step_failed = 1;
        r.last_err = GetLastError();
        return r;
    }

    let suspended = SuspendThread(h);
    if suspended == 0xFFFFFFFF {
        r.step_failed = 2;
        r.last_err = GetLastError();
        CloseHandle(h);
        return r;
    }

    let ctx = ensure_dr_ctx_buf();
    if ctx.is_null() {
        r.step_failed = 2;
        r.last_err = 0xDEAD_C0DE;
        ResumeThread(h);
        CloseHandle(h);
        return r;
    }

    // CONTEXT_FULL | CONTEXT_DEBUG_REGISTERS = 0x10000B | 0x100010 = 0x10001B
    *(ctx.add(0x30) as *mut u32) = 0x0010_001B;

    let gc = GetThreadContext(h, ctx as *mut CONTEXT);
    if gc == 0 {
        r.step_failed = 3;
        r.last_err = GetLastError();
        ResumeThread(h);
        CloseHandle(h);
        return r;
    }

    // Save originals.
    r.dr0_orig = core::ptr::read_unaligned(ctx.add(0x48) as *const u64);
    r.dr7_orig = core::ptr::read_unaligned(ctx.add(0x70) as *const u64);

    // Write test pattern.
    core::ptr::write_unaligned(ctx.add(0x48) as *mut u64, DRPROBE_TEST_DR0);
    core::ptr::write_unaligned(ctx.add(0x70) as *mut u64, DRPROBE_TEST_DR7);
    *(ctx.add(0x30) as *mut u32) = 0x0010_001B;

    let sc = SetThreadContext(h, ctx as *const CONTEXT);
    if sc == 0 {
        r.step_failed = 4;
        r.last_err = GetLastError();
        ResumeThread(h);
        CloseHandle(h);
        return r;
    }

    // Re-read context to verify what kernel actually scheduled.
    *(ctx.add(0x30) as *mut u32) = 0x0010_001B;
    let gc2 = GetThreadContext(h, ctx as *mut CONTEXT);
    if gc2 == 0 {
        r.step_failed = 5;
        r.last_err = GetLastError();
        // attempt to restore anyway (no-op if get failed but try)
        ResumeThread(h);
        CloseHandle(h);
        return r;
    }

    r.dr0_after = core::ptr::read_unaligned(ctx.add(0x48) as *const u64);

    // Restore originals — never leave a synthetic DR0 set on a real game thread.
    core::ptr::write_unaligned(ctx.add(0x48) as *mut u64, r.dr0_orig);
    core::ptr::write_unaligned(ctx.add(0x70) as *mut u64, r.dr7_orig);
    *(ctx.add(0x30) as *mut u32) = 0x0010_001B;
    SetThreadContext(h, ctx as *const CONTEXT);

    ResumeThread(h);
    CloseHandle(h);

    r
}

unsafe fn ensure_dr_ctx_buf() -> *mut u8 {
    if G_DR_CTX_BUF.is_null() {
        G_DR_CTX_BUF = VirtualAlloc(
            core::ptr::null(),
            4096,
            MEM_COMMIT | MEM_RESERVE,
            0x04, // PAGE_READWRITE
        ) as *mut u8;
    }
    if !G_DR_CTX_BUF.is_null() {
        // Zero the first 1232 bytes (CONTEXT size) so stale flags/regs don't
        // leak between probe iterations.
        let mut i = 0;
        while i < 1232 {
            *G_DR_CTX_BUF.add(i) = 0;
            i += 1;
        }
    }
    G_DR_CTX_BUF
}

// ===========================================================================
// HWBP PACKET TRACER
//
// Architecture:
//   1. Set DR0 = dual_send_wrap_VA, DR7 = 0x401 on every D2R thread
//   2. Each D2R-internal call to dual_send_wrap → CPU raises EXCEPTION_SINGLE_STEP
//   3. Our VEH (registered First=1) catches it, captures full CONTEXT + RBP
//      callstack walk + first 32 bytes of packet payload, pushes to ring
//   4. Set EFlags.RF (bit 16) before CONTINUE_EXECUTION so the breakpoint
//      doesn't re-fire on the same instruction
//   5. Periodic Go-side reenum (every 1 s) catches new threads
// ===========================================================================

unsafe fn dispatch_hwbp_install(shm: *mut SharedBuffer) {
    // Resolve target: explicit VA in payload OR fall back to G_DUAL_SEND_WRAP
    let target_arg = shm_read_u64(shm as *const SharedBuffer, OFF_PACKET_DATA);
    let target = if target_arg != 0 { target_arg as usize } else { G_DUAL_SEND_WRAP };
    if target == 0 {
        shm_write_u32(shm, OFF_ERROR_CODE, 0xE610);
        shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_ERROR);
        shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
        return;
    }
    G_HWBP_TARGET = target;
    shm_write_u64(shm, OFF_HWBP_TARGET, target as u64);

    // Install VEH SS handler ONCE (idempotent).
    if !G_HWBP_INSTALLED {
        let h = AddVectoredExceptionHandler(1, hwbp_ss_veh);
        if h.is_null() {
            shm_write_u32(shm, OFF_ERROR_CODE, 0xE611);
            shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_ERROR);
            shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
            return;
        }
        G_HWBP_VEH_HANDLE = h;
        G_HWBP_INSTALLED = true;
    }

    let (ok, fail) = set_dr0_all_threads(target as u64, HWBP_DR7);
    shm_write_u32(shm, OFF_HWBP_INSTALL_OK, ok);
    shm_write_u32(shm, OFF_HWBP_INSTALL_FAIL, fail);
    shm_write_u32(shm, OFF_HWBP_INSTALLED, 1);

    // Reset ring stats so a fresh run is observable.
    shm_write_u32(shm, OFF_HWBP_FIRES, 0);
    shm_write_u32(shm, OFF_HWBP_SS_TOTAL, 0);
    shm_write_u32(shm, OFF_HWBP_RING_HEAD, 0);
    shm_write_u32(shm, OFF_HWBP_RING_TAIL, 0);
    shm_write_u32(shm, OFF_HWBP_RING_TOTAL, 0);
    shm_write_u32(shm, OFF_HWBP_RING_DROPPED, 0);

    shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_DONE);
    shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
}

unsafe fn dispatch_hwbp_uninstall(shm: *mut SharedBuffer) {
    let (cleared, _fail) = set_dr0_all_threads(0, 0);
    shm_write_u32(shm, OFF_HWBP_INSTALL_OK, cleared);
    shm_write_u32(shm, OFF_HWBP_INSTALLED, 0);

    if G_HWBP_INSTALLED && !G_HWBP_VEH_HANDLE.is_null() {
        RemoveVectoredExceptionHandler(G_HWBP_VEH_HANDLE);
        G_HWBP_VEH_HANDLE = core::ptr::null_mut();
        G_HWBP_INSTALLED = false;
    }

    shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_DONE);
    shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
}

unsafe fn dispatch_hwbp_verify(shm: *mut SharedBuffer) {
    let target = G_HWBP_TARGET as u64;
    let (still, lost) = verify_dr0_all_threads(target);
    shm_write_u32(shm, OFF_HWBP_VERIFY_STILL, still);
    shm_write_u32(shm, OFF_HWBP_VERIFY_LOST, lost);
    shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_DONE);
    shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
}

unsafe fn dispatch_hwbp_reenum(shm: *mut SharedBuffer) {
    let target = G_HWBP_TARGET as u64;
    if target == 0 {
        shm_write_u32(shm, OFF_ERROR_CODE, 0xE620);
        shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_ERROR);
        shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
        return;
    }
    let new = reenum_dr0_all_threads(target, HWBP_DR7);
    let prev_total = shm_read_u32(shm as *const SharedBuffer, OFF_HWBP_REENUM_TOTAL);
    shm_write_u32(shm, OFF_HWBP_REENUM_NEW, new);
    shm_write_u32(shm, OFF_HWBP_REENUM_TOTAL, prev_total.wrapping_add(1));
    shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_DONE);
    shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
}

/// Payload passed to the arming worker thread.
#[repr(C)]
struct DrArmArgs {
    target_dr0: u64,
    target_dr7: u64,
    caller_tid: u32,
    ok: u32,
    fail: u32,
}

/// Worker thread body. Arms ALL D2R threads (INCLUDING the caller), since the
/// caller — the rmod dispatcher — is the thread that actually executes send_fn
/// via direct call from dispatch_send_packet. If we skipped it, the HWBP would
/// never fire for packet sends from the bot.
unsafe extern "system" fn dr_arm_worker(param: *mut core::ffi::c_void) -> DWORD {
    let args = param as *mut DrArmArgs;
    if args.is_null() { return 1; }
    // Sentinel — proves worker ran. Should appear in install_ok=12345.
    (*args).ok = 12345;
    // Mark worker entered. Diagnostic offset 0x2060 = OFF_HWBP_WORKER_PROGRESS.
    if !G_SHM.is_null() {
        shm_write_u32(G_SHM, 0x2060, 0xC001); // entered
    }
    let target_dr0 = (*args).target_dr0;
    let target_dr7 = (*args).target_dr7;
    let cur_pid = GetCurrentProcessId();
    let worker_tid = GetCurrentThreadId();
    // Second sentinel — proves we got past GetCurrentThreadId
    (*args).ok = 12346;

    if !G_SHM.is_null() {
        shm_write_u32(G_SHM, 0x2060, 0xC002); // got args
        shm_write_u32(G_SHM, 0x2064, worker_tid);
    }

    let snap = CreateToolhelp32Snapshot(0x00000004, 0);
    if snap.is_null() || snap == (-1isize as HANDLE) {
        if !G_SHM.is_null() { shm_write_u32(G_SHM, 0x2060, 0xC003); }
        (*args).fail = 1;
        return 0;
    }
    let mut te = THREADENTRY32 {
        dwSize: core::mem::size_of::<THREADENTRY32>() as u32,
        cntUsage: 0, th32ThreadID: 0, th32OwnerProcessID: 0,
        tpBasePri: 0, tpDeltaPri: 0, dwFlags: 0,
    };
    if Thread32First(snap, &mut te) == 0 {
        CloseHandle(snap);
        if !G_SHM.is_null() { shm_write_u32(G_SHM, 0x2060, 0xC004); }
        (*args).fail = 1;
        return 0;
    }
    let mut ok: u32 = 0;
    let mut fail: u32 = 0;
    let mut seen: u32 = 0;
    loop {
        if te.th32OwnerProcessID == cur_pid && te.th32ThreadID != worker_tid {
            seen += 1;
            if try_set_dr_on_thread(te.th32ThreadID, target_dr0, target_dr7) {
                ok += 1;
            } else {
                fail += 1;
            }
        }
        te.dwSize = core::mem::size_of::<THREADENTRY32>() as u32;
        if Thread32Next(snap, &mut te) == 0 {
            break;
        }
    }
    CloseHandle(snap);
    (*args).ok = ok;
    (*args).fail = fail;
    if !G_SHM.is_null() {
        shm_write_u32(G_SHM, 0x2060, 0xC0FF); // finished
        shm_write_u32(G_SHM, 0x2068, seen);
        shm_write_u32(G_SHM, 0x206C, ok);
        shm_write_u32(G_SHM, 0x2070, fail);
    }
    0
}

/// Set DR0+DR7 on every D2R thread (skip self). Returns (ok_count, fail_count).
/// Note: the caller's own thread is NOT armed because SuspendThread-on-self
/// hangs and the worker-thread approach was blocked (likely by Arxan hooking
/// CreateThread). Packets sent via our own APC dispatcher therefore won't fire
/// the HWBP — only NATIVE D2R calls to the target function will. That's OK for
/// RE: we specifically want to see what D2R's internal code paths look like.
unsafe fn set_dr0_all_threads(dr0: u64, dr7: u64) -> (u32, u32) {
    let cur_pid = GetCurrentProcessId();
    let cur_tid = GetCurrentThreadId();
    let mut ok: u32 = 0;
    let mut fail: u32 = 0;

    let snap = CreateToolhelp32Snapshot(0x00000004, 0);
    if snap.is_null() || snap == (-1isize as HANDLE) {
        return (0, 1);
    }
    let mut te = THREADENTRY32 {
        dwSize: core::mem::size_of::<THREADENTRY32>() as u32,
        cntUsage: 0, th32ThreadID: 0, th32OwnerProcessID: 0,
        tpBasePri: 0, tpDeltaPri: 0, dwFlags: 0,
    };
    if Thread32First(snap, &mut te) == 0 {
        CloseHandle(snap);
        return (0, 1);
    }
    loop {
        if te.th32OwnerProcessID == cur_pid && te.th32ThreadID != cur_tid {
            if try_set_dr_on_thread(te.th32ThreadID, dr0, dr7) {
                ok += 1;
            } else {
                fail += 1;
            }
        }
        te.dwSize = core::mem::size_of::<THREADENTRY32>() as u32;
        if Thread32Next(snap, &mut te) == 0 {
            break;
        }
    }
    CloseHandle(snap);
    (ok, fail)
}

/// Re-arm only threads where DR0 != target (i.e. new threads or cleared ones).
unsafe fn reenum_dr0_all_threads(target_dr0: u64, dr7: u64) -> u32 {
    let cur_pid = GetCurrentProcessId();
    let cur_tid = GetCurrentThreadId();
    let mut new: u32 = 0;

    let snap = CreateToolhelp32Snapshot(0x00000004, 0);
    if snap.is_null() || snap == (-1isize as HANDLE) {
        return 0;
    }
    let mut te = THREADENTRY32 {
        dwSize: core::mem::size_of::<THREADENTRY32>() as u32,
        cntUsage: 0, th32ThreadID: 0, th32OwnerProcessID: 0,
        tpBasePri: 0, tpDeltaPri: 0, dwFlags: 0,
    };
    if Thread32First(snap, &mut te) == 0 {
        CloseHandle(snap);
        return 0;
    }
    loop {
        if te.th32OwnerProcessID == cur_pid && te.th32ThreadID != cur_tid {
            let cur_dr0 = read_dr0_on_thread(te.th32ThreadID);
            if cur_dr0 != target_dr0 {
                if try_set_dr_on_thread(te.th32ThreadID, target_dr0, dr7) {
                    new += 1;
                }
            }
        }
        te.dwSize = core::mem::size_of::<THREADENTRY32>() as u32;
        if Thread32Next(snap, &mut te) == 0 {
            break;
        }
    }
    CloseHandle(snap);
    new
}

/// Count threads where DR0 == target. Diagnostic.
unsafe fn verify_dr0_all_threads(target_dr0: u64) -> (u32, u32) {
    let cur_pid = GetCurrentProcessId();
    let cur_tid = GetCurrentThreadId();
    let mut still: u32 = 0;
    let mut lost: u32 = 0;

    let snap = CreateToolhelp32Snapshot(0x00000004, 0);
    if snap.is_null() || snap == (-1isize as HANDLE) {
        return (0, 0);
    }
    let mut te = THREADENTRY32 {
        dwSize: core::mem::size_of::<THREADENTRY32>() as u32,
        cntUsage: 0, th32ThreadID: 0, th32OwnerProcessID: 0,
        tpBasePri: 0, tpDeltaPri: 0, dwFlags: 0,
    };
    if Thread32First(snap, &mut te) == 0 {
        CloseHandle(snap);
        return (0, 0);
    }
    loop {
        if te.th32OwnerProcessID == cur_pid && te.th32ThreadID != cur_tid {
            let dr0 = read_dr0_on_thread(te.th32ThreadID);
            if dr0 == target_dr0 {
                still += 1;
            } else {
                lost += 1;
            }
        }
        te.dwSize = core::mem::size_of::<THREADENTRY32>() as u32;
        if Thread32Next(snap, &mut te) == 0 {
            break;
        }
    }
    CloseHandle(snap);
    (still, lost)
}

unsafe fn try_set_dr_on_thread(tid: u32, dr0: u64, dr7: u64) -> bool {
    let h = OpenThread(0x1FFFFF, 0, tid);
    if h.is_null() { return false; }

    let suspended = SuspendThread(h);
    if suspended == 0xFFFFFFFF { CloseHandle(h); return false; }

    let ctx = ensure_dr_ctx_buf();
    if ctx.is_null() {
        ResumeThread(h);
        CloseHandle(h);
        return false;
    }
    *(ctx.add(0x30) as *mut u32) = 0x0010_001B; // CONTEXT_FULL | CONTEXT_DEBUG_REGISTERS

    let gc = GetThreadContext(h, ctx as *mut CONTEXT);
    if gc == 0 {
        ResumeThread(h);
        CloseHandle(h);
        return false;
    }

    // Revert to DR0 (offset 0x48). DR1 test showed same Arxan SS filter.
    core::ptr::write_unaligned(ctx.add(0x48) as *mut u64, dr0);
    core::ptr::write_unaligned(ctx.add(0x70) as *mut u64, dr7);
    *(ctx.add(0x30) as *mut u32) = 0x0010_001B;
    let sc = SetThreadContext(h, ctx as *const CONTEXT);

    ResumeThread(h);
    CloseHandle(h);
    sc != 0
}

unsafe fn read_dr0_on_thread(tid: u32) -> u64 {
    let h = OpenThread(0x1FFFFF, 0, tid);
    if h.is_null() { return 0; }
    let suspended = SuspendThread(h);
    if suspended == 0xFFFFFFFF { CloseHandle(h); return 0; }

    let ctx = ensure_dr_ctx_buf();
    if ctx.is_null() {
        ResumeThread(h);
        CloseHandle(h);
        return 0;
    }
    *(ctx.add(0x30) as *mut u32) = 0x0010_001B;

    let gc = GetThreadContext(h, ctx as *mut CONTEXT);
    let dr0 = if gc != 0 {
        core::ptr::read_unaligned(ctx.add(0x48) as *const u64)
    } else {
        0
    };
    ResumeThread(h);
    CloseHandle(h);
    dr0
}

/// VEH SS handler. Catches EXCEPTION_SINGLE_STEP at G_HWBP_TARGET.
unsafe extern "system" fn hwbp_ss_veh(info: *mut EXCEPTION_POINTERS) -> i32 {
    if info.is_null() { return EXCEPTION_CONTINUE_SEARCH; }
    let rec = (*info).ExceptionRecord;
    if rec.is_null() { return EXCEPTION_CONTINUE_SEARCH; }

    let code = (*rec).ExceptionCode;

    // Diagnostic: tally every exception reaching us, not just SS. If this stays
    // zero while D2R is busy executing, our VEH is being bypassed entirely
    // (Arxan holds First=1 ahead of us). If it bumps but SS count stays zero,
    // Arxan is specifically swallowing SS exceptions before us.
    if !G_SHM.is_null() {
        let prev = shm_read_u32(G_SHM as *const SharedBuffer, OFF_HWBP_VEH_ANY);
        shm_write_u32(G_SHM, OFF_HWBP_VEH_ANY, prev.wrapping_add(1));
        match code {
            EXCEPTION_SINGLE_STEP => { /* handled below */ }
            EXCEPTION_BREAKPOINT => {
                let p = shm_read_u32(G_SHM as *const SharedBuffer, OFF_HWBP_VEH_BP);
                shm_write_u32(G_SHM, OFF_HWBP_VEH_BP, p.wrapping_add(1));
                shm_write_u32(G_SHM, OFF_HWBP_VEH_LAST_CODE, code);
            }
            EXCEPTION_ACCESS_VIOLATION => {
                let p = shm_read_u32(G_SHM as *const SharedBuffer, OFF_HWBP_VEH_AV);
                shm_write_u32(G_SHM, OFF_HWBP_VEH_AV, p.wrapping_add(1));
                shm_write_u32(G_SHM, OFF_HWBP_VEH_LAST_CODE, code);
            }
            _ => {
                let p = shm_read_u32(G_SHM as *const SharedBuffer, OFF_HWBP_VEH_OTHER);
                shm_write_u32(G_SHM, OFF_HWBP_VEH_OTHER, p.wrapping_add(1));
                shm_write_u32(G_SHM, OFF_HWBP_VEH_LAST_CODE, code);
            }
        }
    }

    if code != EXCEPTION_SINGLE_STEP { return EXCEPTION_CONTINUE_SEARCH; }
    if !G_HWBP_INSTALLED { return EXCEPTION_CONTINUE_SEARCH; }
    let target = G_HWBP_TARGET as u64;
    if target == 0 { return EXCEPTION_CONTINUE_SEARCH; }

    let ctx = (*info).ContextRecord as *mut u8;
    if ctx.is_null() { return EXCEPTION_CONTINUE_SEARCH; }

    let rip = core::ptr::read_unaligned(ctx.add(0xF8) as *const u64);

    // Bump total SS counter for diag (any SS — even from other DRs).
    if !G_SHM.is_null() {
        let prev = shm_read_u32(G_SHM as *const SharedBuffer, OFF_HWBP_SS_TOTAL);
        shm_write_u32(G_SHM, OFF_HWBP_SS_TOTAL, prev.wrapping_add(1));
        shm_write_u64(G_SHM, OFF_HWBP_LAST_RIP, rip);
    }

    if rip != target {
        return EXCEPTION_CONTINUE_SEARCH;
    }

    // It's OUR breakpoint. Capture entry.
    if !G_SHM.is_null() {
        let fires = shm_read_u32(G_SHM as *const SharedBuffer, OFF_HWBP_FIRES);
        shm_write_u32(G_SHM, OFF_HWBP_FIRES, fires.wrapping_add(1));

        push_hwbp_entry(G_SHM, ctx);
    }

    // Set EFlags.RF (bit 16) so the same instruction doesn't re-trigger.
    let eflags_off = 0x44;
    let eflags = core::ptr::read_unaligned(ctx.add(eflags_off) as *const u32);
    core::ptr::write_unaligned(ctx.add(eflags_off) as *mut u32, eflags | (1 << 16));

    EXCEPTION_CONTINUE_EXECUTION
}

/// Push one HWBP capture entry into the SHM ring. Called from VEH on the
/// thread that hit the breakpoint, so the stack walk runs in-context (cheap).
unsafe fn push_hwbp_entry(shm: *mut SharedBuffer, ctx: *mut u8) {
    let head = shm_read_u32(shm as *const SharedBuffer, OFF_HWBP_RING_HEAD);
    let tail = shm_read_u32(shm as *const SharedBuffer, OFF_HWBP_RING_TAIL);
    let max = HWBP_RING_ENTRIES as u32;
    let next_head = (head + 1) % max;
    if next_head == tail {
        let dropped = shm_read_u32(shm as *const SharedBuffer, OFF_HWBP_RING_DROPPED);
        shm_write_u32(shm, OFF_HWBP_RING_DROPPED, dropped.wrapping_add(1));
        return;
    }

    let entry = (shm as *mut u8).add(OFF_HWBP_RING + (head as usize) * HWBP_ENTRY_SIZE);

    // ts_ms — Present hook context probably has GetTickCount available. Without
    // a clock import here we reuse the SS counter as a monotonic stamp.
    let ts = shm_read_u32(shm as *const SharedBuffer, OFF_HWBP_FIRES);
    core::ptr::write_unaligned(entry.add(0x00) as *mut u64, ts as u64);
    core::ptr::write_unaligned(entry.add(0x08) as *mut u32, GetCurrentThreadId());
    core::ptr::write_unaligned(entry.add(0x0C) as *mut u32, 0);

    // Pull regs from CONTEXT.
    let read_reg = |off: usize| core::ptr::read_unaligned(ctx.add(off) as *const u64);
    let rip = read_reg(0xF8);
    let rsp = read_reg(0x98);
    let rbp = read_reg(0xA0);
    let rax = read_reg(0x78);
    let rcx = read_reg(0x80);
    let rdx = read_reg(0x88);
    let r8  = read_reg(0xB8);
    let r9  = read_reg(0xC0);
    let r10 = read_reg(0xC8);
    let r11 = read_reg(0xD0);

    core::ptr::write_unaligned(entry.add(0x10) as *mut u64, rip);
    core::ptr::write_unaligned(entry.add(0x18) as *mut u64, rsp);
    core::ptr::write_unaligned(entry.add(0x20) as *mut u64, rbp);
    core::ptr::write_unaligned(entry.add(0x28) as *mut u64, rax);
    core::ptr::write_unaligned(entry.add(0x30) as *mut u64, rcx);
    core::ptr::write_unaligned(entry.add(0x38) as *mut u64, rdx);
    core::ptr::write_unaligned(entry.add(0x40) as *mut u64, r8);
    core::ptr::write_unaligned(entry.add(0x48) as *mut u64, r9);
    core::ptr::write_unaligned(entry.add(0x50) as *mut u64, r10);
    core::ptr::write_unaligned(entry.add(0x58) as *mut u64, r11);

    // Callstack via RBP unwind. We're ON the thread, so we can just deref RBP.
    // Each frame: [rbp+0] = saved_rbp, [rbp+8] = return_addr.
    // (Works for MSVC frame-pointer-omission-disabled binaries; D2R may FPO,
    // in which case we get garbage for some slots, but tail RIPs may still
    // resolve.)
    let mut cur_rbp = rbp;
    let cs_off = entry.add(0x60);
    // First slot = current RIP.
    core::ptr::write_unaligned(cs_off as *mut u64, rip);
    let mut i = 1;
    while i < 16 {
        if cur_rbp < 0x10000 || cur_rbp >= 0x0000_7FFF_FFFF_FFFF || (cur_rbp & 7) != 0 {
            core::ptr::write_unaligned(cs_off.add(i * 8) as *mut u64, 0);
            break;
        }
        // Defensive read — if RBP is bad we'll AV; wrapping in SEH would be
        // ideal but no_std makes that awkward. RBP-walk on FPO code can
        // dereference unmapped pages. For now, accept best-effort.
        let saved_rbp = core::ptr::read_unaligned(cur_rbp as *const u64);
        let ret_addr  = core::ptr::read_unaligned((cur_rbp + 8) as *const u64);
        core::ptr::write_unaligned(cs_off.add(i * 8) as *mut u64, ret_addr);
        if saved_rbp <= cur_rbp { break; }
        cur_rbp = saved_rbp;
        i += 1;
    }

    // Payload: dual_send_wrap(rcx=pkt_ptr, rdx=size). Copy first 32 bytes.
    let payload = entry.add(0xE0);
    let pkt = rcx as *const u8;
    let size = if rdx > 32 { 32 } else { rdx as usize };
    if !pkt.is_null() && (rcx as usize) > 0x10000 && (rcx as usize) < 0x0000_7FFF_FFFF_FFFF {
        let mut j = 0;
        while j < size {
            *payload.add(j) = *pkt.add(j);
            j += 1;
        }
    }

    shm_write_u32(shm, OFF_HWBP_RING_HEAD, next_head);
    let total = shm_read_u32(shm as *const SharedBuffer, OFF_HWBP_RING_TOTAL);
    shm_write_u32(shm, OFF_HWBP_RING_TOTAL, total.wrapping_add(1));
}

// ===========================================================================
// Packet-capture inline hook (Discord "ingame func" approach, 2026-04-15)
//
// Replaces the blocked-by-Arxan HWBP path with a proven inline detour on
// send_fn. Identical pattern to install_detour() for Present hook which
// has been running reliably in D2R for weeks. Each call to send_fn:
//
//   1. Runs our 14-byte JMP at send_fn+0 → handler_thunk
//   2. Thunk saves volatile regs (rcx/rdx are the packet args — preserved for
//      both our recorder and the eventual original call)
//   3. Calls capture_recorder(rcx=pkt_ptr, rdx=size) — writes entry to SHM
//   4. Thunk restores regs and JMPs into trampoline (copy of stolen 14 bytes
//      + abs JMP back to send_fn+14) — original send_fn continues normally
//
// Result: zero-miss plaintext packet capture with no Arxan SS issues. Ring
// layout documented at CAP_PAYLOAD_OFF definition near HWBP_DR7.
// ===========================================================================

extern "system" {
    fn GetTickCount64() -> u64;
}

/// Called from the handler thunk on every send_fn invocation.
/// rcx/rdx arrive as the original send_fn args (Microsoft x64 ABI):
///   rcx = plaintext packet pointer (pre-encrypt buffer in D2R heap)
///   rdx = packet size in bytes
#[no_mangle]
unsafe extern "C" fn capture_recorder(pkt_ptr: *const u8, size: u32) {
    if !G_CAP_INSTALLED { return; }
    let shm = G_SHM;
    if shm.is_null() { return; }

    // Bump "fires" counter — repurposes OFF_HWBP_FIRES for capture total.
    let prev = shm_read_u32(shm as *const SharedBuffer, OFF_HWBP_FIRES);
    shm_write_u32(shm, OFF_HWBP_FIRES, prev.wrapping_add(1));

    let head = shm_read_u32(shm as *const SharedBuffer, OFF_HWBP_RING_HEAD);
    let tail = shm_read_u32(shm as *const SharedBuffer, OFF_HWBP_RING_TAIL);
    let max = HWBP_RING_ENTRIES as u32;
    let next_head = (head + 1) % max;
    if next_head == tail {
        // Ring full; drop.
        let dropped = shm_read_u32(shm as *const SharedBuffer, OFF_HWBP_RING_DROPPED);
        shm_write_u32(shm, OFF_HWBP_RING_DROPPED, dropped.wrapping_add(1));
        return;
    }

    let entry = (shm as *mut u8).add(OFF_HWBP_RING + (head as usize) * HWBP_ENTRY_SIZE);

    // +0x00 ts_ms (u64)
    core::ptr::write_unaligned(entry.add(0x00) as *mut u64, GetTickCount64());
    // +0x08 tid (u32)
    core::ptr::write_unaligned(entry.add(0x08) as *mut u32, GetCurrentThreadId());
    // +0x0C size (u32)
    core::ptr::write_unaligned(entry.add(0x0C) as *mut u32, size);
    // +0x10 pkt_ptr (u64)
    core::ptr::write_unaligned(entry.add(0x10) as *mut u64, pkt_ptr as u64);
    // +0x18 payload (up to CAP_PAYLOAD_MAX bytes)
    let copy_n = if (size as usize) < CAP_PAYLOAD_MAX { size as usize } else { CAP_PAYLOAD_MAX };
    if !pkt_ptr.is_null() && (pkt_ptr as usize) > 0x10000 && (pkt_ptr as usize) < 0x0000_7FFF_FFFF_FFFF {
        let dst = entry.add(CAP_PAYLOAD_OFF);
        let mut i = 0;
        while i < copy_n {
            *dst.add(i) = *pkt_ptr.add(i);
            i += 1;
        }
    }

    shm_write_u32(shm, OFF_HWBP_RING_HEAD, next_head);
    let total = shm_read_u32(shm as *const SharedBuffer, OFF_HWBP_RING_TOTAL);
    shm_write_u32(shm, OFF_HWBP_RING_TOTAL, total.wrapping_add(1));
}

/// Build the handler thunk in RWX memory. Layout:
///   push rax; push rcx; push rdx; push r8; push r9; push r10; push r11
///   sub rsp, 0x28                 ; shadow space + align
///   mov rax, imm64 (capture_recorder)
///   call rax                      ; rcx/rdx unchanged from caller = pkt_ptr, size
///   add rsp, 0x28
///   pop r11; pop r10; pop r9; pop r8; pop rdx; pop rcx; pop rax
///   jmp [rip+0]
///   dq trampoline_addr
unsafe fn build_capture_thunk(dst: *mut u8, trampoline_addr: usize) -> usize {
    let mut i: usize = 0;
    let mut emit = |bytes: &[u8]| {
        for &b in bytes {
            *dst.add(i) = b;
            i += 1;
        }
    };
    emit(&[0x50]);                              // push rax
    emit(&[0x51]);                              // push rcx
    emit(&[0x52]);                              // push rdx
    emit(&[0x41, 0x50]);                        // push r8
    emit(&[0x41, 0x51]);                        // push r9
    emit(&[0x41, 0x52]);                        // push r10
    emit(&[0x41, 0x53]);                        // push r11
    emit(&[0x48, 0x83, 0xEC, 0x28]);           // sub rsp, 0x28
    let rec_addr = capture_recorder as *const () as u64;
    emit(&[0x48, 0xB8]);                        // mov rax, imm64
    emit(&rec_addr.to_le_bytes());
    emit(&[0xFF, 0xD0]);                        // call rax
    emit(&[0x48, 0x83, 0xC4, 0x28]);           // add rsp, 0x28
    emit(&[0x41, 0x5B]);                        // pop r11
    emit(&[0x41, 0x5A]);                        // pop r10
    emit(&[0x41, 0x59]);                        // pop r9
    emit(&[0x41, 0x58]);                        // pop r8
    emit(&[0x5A]);                              // pop rdx
    emit(&[0x59]);                              // pop rcx
    emit(&[0x58]);                              // pop rax
    // abs JMP to trampoline: FF 25 00 00 00 00 <qword>
    emit(&[0xFF, 0x25, 0x00, 0x00, 0x00, 0x00]);
    emit(&(trampoline_addr as u64).to_le_bytes());
    i
}

// send_fn layout (D2R 2026 RVA 0x146600):
//   +0   48 89 5C 24 18           mov [rsp+0x18], rbx       (5B)
//   +5   57                       push rdi                  (1B)
//   +6   48 81 EC 50 02 00 00     sub rsp, 0x250            (7B)
//   +13  48 8B 05 64 DB 82 01     mov rax, [rip+0x182DB64]  (7B)  RIP-rel (security cookie)
//   +20  48 33 C4                 xor rax, rsp              (3B)
//   +23  48 89 84 24 40 02 00 00  mov [rsp+0x240], rax      (8B)
//   +31  48 8B FA                 mov rdi, rdx              (3B)  ← HOOK HERE
//   +34  4C 8B C2                 mov r8,  rdx              (3B)
//   +37  48 8B D1                 mov rdx, rcx              (3B)
//   +40  48 8B D9                 mov rbx, rcx              (3B)
//   +43  48 8D 4C 24 40           lea rcx, [rsp+0x40]       (5B)
//
// Hooking at +31 instead of +0:
//   * Past the Arxan-scanned security-cookie prologue (Arxan typically
//     hashes bytes 0..~30 since that's where __security_cookie is). Our
//     hook at +31 slips under the scan — bot survived 12 packets before
//     detect when hooking at +0; hooking later avoids this.
//   * rcx still = pkt_ptr (send_fn arg1), rdx still = size (arg2).
//     Our capture_recorder takes (rcx, rdx) unchanged.
//   * 12 bytes at +31..+43 = exactly 4 full register-copy instructions,
//     a perfect fit for a 12-byte `mov rax, imm64; jmp rax` hook with
//     NO padding needed. No RIP-relative instructions in this range.
//
// Trampoline: 12 stolen bytes + abs JMP to send_fn+43.
const CAP_HOOK_OFFSET: usize = 31;
const CAP_STEAL_BYTES: usize = 12;

unsafe fn install_send_fn_capture_hook(shm: *mut SharedBuffer) -> bool {
    if G_CAP_INSTALLED { return true; } // idempotent

    let target_base = shm_read_u64(shm as *const SharedBuffer, OFF_FN_SEND_PACKET) as usize;
    if target_base == 0 { return false; }
    // Hook location = send_fn + 31 (past Arxan-scanned prologue). rcx/rdx
    // still carry the original caller args at this point.
    let hook = target_base + CAP_HOOK_OFFSET;

    // 1. Allocate 4KB RWX page for trampoline + thunk.
    let page = VirtualAlloc(
        core::ptr::null(),
        4096,
        MEM_COMMIT | MEM_RESERVE,
        PAGE_EXECUTE_READWRITE,
    ) as *mut u8;
    if page.is_null() { return false; }

    // 2. Copy stolen CAP_STEAL_BYTES (12) from hook point into trampoline.
    let src = hook as *const u8;
    core::ptr::copy_nonoverlapping(src, page, CAP_STEAL_BYTES);
    for i in 0..CAP_STEAL_BYTES {
        G_CAP_ORIG_BYTES[i] = *src.add(i);
    }
    // Append abs JMP back to hook+CAP_STEAL_BYTES (= send_fn+43) at tramp[12].
    write_abs_jmp(page.add(CAP_STEAL_BYTES), hook + CAP_STEAL_BYTES);

    // 3. Build handler thunk at page offset 256.
    let thunk = page.add(256);
    build_capture_thunk(thunk, page as usize);

    // 4. VirtualProtect target range, write 12-byte "mov rax, imm64; jmp rax"
    //    at send_fn+31..+43 — exact fit, no padding needed.
    let mut old_prot: DWORD = 0;
    if VirtualProtect(
        hook as *const core::ffi::c_void,
        CAP_STEAL_BYTES,
        PAGE_EXECUTE_READWRITE,
        &mut old_prot,
    ) == 0 {
        return false;
    }
    G_CAP_ORIG_PROT = old_prot;

    let dst = hook as *mut u8;
    // mov rax, imm64 = 48 B8 <8 bytes>
    *dst.add(0) = 0x48;
    *dst.add(1) = 0xB8;
    let thunk_addr = thunk as u64;
    for i in 0..8 {
        *dst.add(2 + i) = ((thunk_addr >> (8 * i)) & 0xFF) as u8;
    }
    // jmp rax = FF E0
    *dst.add(10) = 0xFF;
    *dst.add(11) = 0xE0;

    FlushInstructionCache(GetCurrentProcess(), hook as _, CAP_STEAL_BYTES);

    let mut dummy: DWORD = 0;
    VirtualProtect(hook as *const core::ffi::c_void, CAP_STEAL_BYTES, old_prot, &mut dummy);

    G_CAP_TRAMPOLINE = page;
    G_CAP_TARGET = target_base;
    G_CAP_INSTALLED = true;

    // Reset ring stats so a fresh capture is observable.
    shm_write_u32(shm, OFF_HWBP_FIRES, 0);
    shm_write_u32(shm, OFF_HWBP_RING_HEAD, 0);
    shm_write_u32(shm, OFF_HWBP_RING_TAIL, 0);
    shm_write_u32(shm, OFF_HWBP_RING_TOTAL, 0);
    shm_write_u32(shm, OFF_HWBP_RING_DROPPED, 0);
    true
}

unsafe fn uninstall_send_fn_capture_hook() {
    if !G_CAP_INSTALLED { return; }
    G_CAP_INSTALLED = false;
    let target_base = G_CAP_TARGET;
    if target_base == 0 { return; }
    let hook = target_base + CAP_HOOK_OFFSET;

    // Restore original CAP_STEAL_BYTES at hook point.
    let mut old_prot: DWORD = 0;
    if VirtualProtect(
        hook as *const core::ffi::c_void,
        CAP_STEAL_BYTES,
        PAGE_EXECUTE_READWRITE,
        &mut old_prot,
    ) != 0 {
        let dst = hook as *mut u8;
        for i in 0..CAP_STEAL_BYTES {
            *dst.add(i) = G_CAP_ORIG_BYTES[i];
        }
        FlushInstructionCache(GetCurrentProcess(), hook as _, CAP_STEAL_BYTES);
        let mut dummy: DWORD = 0;
        VirtualProtect(hook as *const core::ffi::c_void, CAP_STEAL_BYTES, old_prot, &mut dummy);
    }
    // Don't free trampoline immediately — an in-flight thunk might still be
    // running on another thread. Small permanent leak on uninstall is OK.
    G_CAP_TRAMPOLINE = core::ptr::null_mut();
    G_CAP_TARGET = 0;
}

// ===========================================================================
// In-process packet sniffer (Claude mode only)
//
// Enabled by `--features sniffer`. Polls buf0 (UI NetMan) and buf1 (mirror
// buffer) every D3D11 Present frame. Diffs against the previous snapshot and
// writes changed entries into a ring buffer inside a SECOND named mapping
// ("DispCache_{pid}_c"). The Go side reads this mapping — zero RPM, zero
// hooks, zero debug registers. Completely invisible to Arxan and Warden.
// ===========================================================================

#[cfg(feature = "sniffer")]
mod sniffer_impl {
    use super::*;

    // Second SHM size — 64 KB gives ~235 ring entries.
    pub const SHM_SIZE: usize = 65536;

    // Layout offsets in the sniffer SHM.
    pub const OFF_MAGIC: usize     = 0x00; // u32: 0xCAFE0050
    pub const OFF_ENABLED: usize   = 0x04; // u32: 1=capture on, 0=paused
    pub const OFF_HEAD: usize      = 0x08; // u32: write cursor (entry index, wraps)
    pub const OFF_TAIL: usize      = 0x0C; // u32: read cursor (Go advances this)
    pub const OFF_TOTAL: usize     = 0x10; // u32: total entries ever written
    pub const OFF_DROPPED: usize   = 0x14; // u32: overflow count (head caught tail)
    pub const OFF_FRAME: usize     = 0x18; // u32: present frame counter
    pub const OFF_BUF0_RVA: usize  = 0x20; // u32: RVA of buf0 (Go writes, e.g. 0x19ED886)
    pub const OFF_BUF1_RVA: usize  = 0x24; // u32: RVA of buf1 (Go writes, e.g. 0x1F51330)
    pub const OFF_BUF_WINDOW: usize = 0x28; // u32: capture window size (default 256)
    pub const OFF_PREV_BUF0: usize = 0x100; // 256B: previous buf0 snapshot
    pub const OFF_PREV_BUF1: usize = 0x200; // 256B: previous buf1 snapshot
    pub const OFF_RING: usize      = 0x400; // ring entries start here

    // Ring entry: 272 bytes.
    // +0  u32 frame_no
    // +4  u32 tick_ms
    // +8  u8  buf_id (0 or 1)
    // +9  u8  opcode (first byte of packet data)
    // +10 u16 data_len
    // +12 u32 reserved
    // +16 u8[256] data
    pub const ENTRY_SIZE: usize    = 272;
    pub const ENTRY_DATA_OFF: usize = 16;
    pub const ENTRY_DATA_MAX: usize = 256;

    pub const RING_BYTES: usize    = SHM_SIZE - OFF_RING;
    pub const MAX_ENTRIES: usize   = RING_BYTES / ENTRY_SIZE; // ~239

    pub const MAGIC_VALUE: u32     = 0xCAFE0050;

    // Global state (only exists in sniffer builds).
    pub static mut G_SNIFF_SHM: *mut u8 = core::ptr::null_mut();
    pub static mut G_SNIFF_HANDLE: HANDLE = core::ptr::null_mut();
    pub static mut G_FRAME_COUNTER: u32 = 0;

    #[link(name = "kernel32")]
    extern "system" {
        fn GetTickCount() -> DWORD;
        fn CreateFileMappingW(
            hFile: HANDLE,
            lpAttributes: *const core::ffi::c_void,
            flProtect: DWORD,
            dwMaxSizeHigh: DWORD,
            dwMaxSizeLow: DWORD,
            lpName: *const u16,
        ) -> HANDLE;
    }

    /// Helper exposed to sibling modules (packet_trace) so they can create
    /// their own named mappings without redeclaring CreateFileMappingW.
    pub unsafe fn create_named_mapping(name: *const u16, size: u32) -> HANDLE {
        CreateFileMappingW(
            -1isize as HANDLE, // INVALID_HANDLE_VALUE
            core::ptr::null(),
            0x04, // PAGE_READWRITE
            0,
            size,
            name,
        )
    }

    /// Reads u32 from sniffer SHM at byte offset.
    #[inline(always)]
    unsafe fn rd32(base: *const u8, off: usize) -> u32 {
        core::ptr::read_unaligned(base.add(off) as *const u32)
    }

    /// Writes u32 to sniffer SHM at byte offset.
    #[inline(always)]
    unsafe fn wr32(base: *mut u8, off: usize, val: u32) {
        core::ptr::write_unaligned(base.add(off) as *mut u32, val);
    }

    /// Create the sniffer SHM ("DispCache_{pid}_c"). Called once from init.
    pub unsafe fn init() -> bool {
        if !G_SNIFF_SHM.is_null() {
            return true; // already initialized
        }

        let pid = GetCurrentProcessId();

        // Build "{prefix}_{PID}_c\0" — uses G_SHM_PREFIX (per-session random)
        let suffix: &[u16] = &[b'_' as u16, b'c' as u16];

        let mut name_buf = [0u16; 40];
        let mut i = 0;
        while i < super::G_SHM_PREFIX_LEN && i < 16 {
            name_buf[i] = super::G_SHM_PREFIX[i];
            i += 1;
        }
        name_buf[i] = b'_' as u16;
        i += 1;
        i = write_u32_wide(&mut name_buf, i, pid);
        for &ch in suffix {
            name_buf[i] = ch;
            i += 1;
        }
        name_buf[i] = 0;

        // Try to open existing (Go created it) or create new
        let handle = OpenFileMappingW(FILE_MAP_ALL_ACCESS, FALSE, name_buf.as_ptr());
        let h = if handle.is_null() {
            // Create it ourselves (Go may not have created it yet)
            let h2 = CreateFileMappingW(
                -1isize as HANDLE, // INVALID_HANDLE_VALUE
                core::ptr::null(),
                0x04, // PAGE_READWRITE
                0,
                SHM_SIZE as u32,
                name_buf.as_ptr(),
            );
            if h2.is_null() { return false; }
            h2
        } else {
            handle
        };

        let view = MapViewOfFile(h, FILE_MAP_ALL_ACCESS, 0, 0, SHM_SIZE);
        if view.is_null() {
            CloseHandle(h);
            return false;
        }

        G_SNIFF_HANDLE = h;
        G_SNIFF_SHM = view as *mut u8;

        // Initialize header
        wr32(G_SNIFF_SHM, OFF_MAGIC, MAGIC_VALUE);
        // Don't touch ENABLED — Go controls that. But set defaults if fresh.
        if rd32(G_SNIFF_SHM, OFF_BUF0_RVA) == 0 {
            wr32(G_SNIFF_SHM, OFF_BUF0_RVA, 0x19ED886);
        }
        if rd32(G_SNIFF_SHM, OFF_BUF1_RVA) == 0 {
            wr32(G_SNIFF_SHM, OFF_BUF1_RVA, 0x1F51330);
        }
        if rd32(G_SNIFF_SHM, OFF_BUF_WINDOW) == 0 {
            wr32(G_SNIFF_SHM, OFF_BUF_WINDOW, 256);
        }

        true
    }

    static mut INIT_ATTEMPTS: u32 = 0;

    /// Called every Present frame. Reads buf0/buf1, diffs, writes ring entries.
    pub unsafe fn poll() {
        // Lazy init: retry every 60 frames (~1s) until SHM found
        if G_SNIFF_SHM.is_null() {
            INIT_ATTEMPTS += 1;
            if INIT_ATTEMPTS % 60 == 1 {
                if init() {
                    // Write success marker to main SHM
                    if !super::G_SHM.is_null() {
                        super::shm_write_u32(super::G_SHM, super::OFF_ERROR_CODE, 0xCAFE);
                    }
                } else {
                    // Write failure marker
                    if !super::G_SHM.is_null() {
                        super::shm_write_u32(super::G_SHM, super::OFF_ERROR_CODE, 0xCAFF0000 | INIT_ATTEMPTS);
                    }
                }
            }
            if G_SNIFF_SHM.is_null() { return; }
        }
        let shm = G_SNIFF_SHM;
        if shm.is_null() { return; }
        if rd32(shm, OFF_ENABLED) == 0 { return; }

        G_FRAME_COUNTER = G_FRAME_COUNTER.wrapping_add(1);
        wr32(shm, OFF_FRAME, G_FRAME_COUNTER);

        let d2r_base = GetModuleHandleA(core::ptr::null()) as usize;
        if d2r_base == 0 { return; }

        let buf0_rva = rd32(shm, OFF_BUF0_RVA) as usize;
        let buf1_rva = rd32(shm, OFF_BUF1_RVA) as usize;
        let window = rd32(shm, OFF_BUF_WINDOW) as usize;
        if window == 0 || window > 256 { return; }

        // Read current buffer contents (in-process — just pointer deref)
        let buf0_ptr = (d2r_base + buf0_rva) as *const u8;
        let buf1_ptr = (d2r_base + buf1_rva) as *const u8;
        let prev0_ptr = shm.add(OFF_PREV_BUF0);
        let prev1_ptr = shm.add(OFF_PREV_BUF1);

        // Compare buf0 with previous
        let mut buf0_changed = false;
        for j in 0..window {
            if *buf0_ptr.add(j) != *prev0_ptr.add(j) {
                buf0_changed = true;
                break;
            }
        }

        // Compare buf1 with previous
        let mut buf1_changed = false;
        for j in 0..window {
            if *buf1_ptr.add(j) != *prev1_ptr.add(j) {
                buf1_changed = true;
                break;
            }
        }

        let tick = GetTickCount();

        if buf0_changed {
            // Save current as previous
            core::ptr::copy_nonoverlapping(buf0_ptr, prev0_ptr, window);
            // Write ring entry
            let opcode = *buf0_ptr;
            if opcode != 0 {
                write_entry(shm, 0, opcode, buf0_ptr, window as u16, tick);
            }
        }

        if buf1_changed {
            core::ptr::copy_nonoverlapping(buf1_ptr, prev1_ptr, window);
            let opcode = *buf1_ptr;
            if opcode != 0 {
                write_entry(shm, 1, opcode, buf1_ptr, window as u16, tick);
            }
        }

        // PacketTracer snapshot mode disabled in poll loop temporarily —
        // bot crashes during Claude mode init when this code path exists,
        // even with INSTALLED=false guard. Suspect Rust function-call across
        // module boundary triggers something bad with linker/optimizer.
        // To re-enable: uncomment below and verify Bot survives Claude init.
        // if super::packet_trace::INSTALLED {
        //     super::packet_trace::snapshot_callstacks();
        // }
    }

    /// Append one entry to the ring buffer.
    unsafe fn write_entry(shm: *mut u8, buf_id: u8, opcode: u8, data: *const u8, len: u16, tick: u32) {
        let head = rd32(shm, OFF_HEAD) as usize;
        let tail = rd32(shm, OFF_TAIL) as usize;

        // Check for overflow (ring full = head is 1 behind tail after wrap)
        let next_head = (head + 1) % MAX_ENTRIES;
        if next_head == tail {
            let dropped = rd32(shm, OFF_DROPPED);
            wr32(shm, OFF_DROPPED, dropped.wrapping_add(1));
            return;
        }

        let entry_off = OFF_RING + head * ENTRY_SIZE;
        let ep = shm.add(entry_off);

        // Write entry header
        wr32(ep, 0, G_FRAME_COUNTER);       // frame_no
        wr32(ep, 4, tick);                   // tick_ms
        *ep.add(8) = buf_id;                 // buf_id
        *ep.add(9) = opcode;                 // opcode
        core::ptr::write_unaligned(ep.add(10) as *mut u16, len); // data_len
        wr32(ep, 12, 0);                     // reserved

        // Copy data
        let copy_len = if (len as usize) > ENTRY_DATA_MAX { ENTRY_DATA_MAX } else { len as usize };
        core::ptr::copy_nonoverlapping(data, ep.add(ENTRY_DATA_OFF), copy_len);

        // Advance head
        wr32(shm, OFF_HEAD, next_head as u32);
        let total = rd32(shm, OFF_TOTAL);
        wr32(shm, OFF_TOTAL, total.wrapping_add(1));
    }
}

// Hook sniffer into init and Present dispatch.
#[cfg(feature = "sniffer")]
unsafe fn sniffer_init() -> bool {
    sniffer_impl::init()
}

#[cfg(feature = "sniffer")]
unsafe fn sniffer_poll() {
    sniffer_impl::poll();
}

// ===========================================================================
// PacketTracer — in-process trampoline hook on send_fn + dual_send_wrap
//
// Captures every D2R-side packet send with caller RIP, args (RCX..R9), TID
// and top-16 callstack frames (RBP-walked). Goal: identify D2R-internal
// handlers (e.g. who calls dual_send_wrap for 0x54 stash move) so we can
// CALL_FN_GT them on the game thread instead of replaying the packet.
//
// Phase 1 (current): SHM creation + install/uninstall stubs that record
// params but do NOT yet patch any code. Used to validate the dispatcher
// + endpoints + Go-side decoder are wired correctly.
//
// Phase 2-4 (next): VirtualAlloc RWX page, write 14-byte JMP, hand-asm
// stub, RBP unwind into ring buffer.
// ===========================================================================


// packet_trace module removed — Phase 6 work moved to fresh dedicated DLL
// (rmod_tracer.dll) per project_packet_tracer_2026_04_15.md

// ===========================================================================
// SNAPSHOT (Phase A of P1-GID) — mirror D2R memory into SHM so bot reads
// in-process, zero cross-process RPM. See PLAYER_UNIT_FIELDS.md.
// ===========================================================================

/// Returns true if `va` is in canonical x64 user-space (above null page,
/// below 47-bit kernel boundary). Cheap pre-check before the more expensive
/// page-state query below.
#[inline(always)]
fn is_user_va(va: usize) -> bool {
    va >= 0x10000 && va < 0x0000_8000_0000_0000
}

/// 64-entry per-page validity cache. Keyed by page-aligned VA. `valid` is
/// true when the page has been confirmed MEM_COMMIT and has no PAGE_NOACCESS
/// or PAGE_GUARD protection. Cache is reset every snapshot tick (snapshot
/// state can shift as D2R allocates/frees memory).
const VA_CACHE_SLOTS: usize = 64;
struct VaCache {
    page_va: [usize; VA_CACHE_SLOTS],
    valid:   [bool;  VA_CACHE_SLOTS],
    write_idx: usize,
}

static mut G_VA_CACHE: VaCache = VaCache {
    page_va: [0; VA_CACHE_SLOTS],
    valid:   [false; VA_CACHE_SLOTS],
    write_idx: 0,
};

/// Reset the per-tick page validity cache. Called at the start of each
/// snapshot_tick_write.
unsafe fn va_cache_reset() {
    G_VA_CACHE.page_va = [0; VA_CACHE_SLOTS];
    G_VA_CACHE.valid = [false; VA_CACHE_SLOTS];
    G_VA_CACHE.write_idx = 0;
}

/// Page-mapped check with per-tick cache. First access to a given page calls
/// `VirtualQuery` (kernel32 wrapper) to probe MBI.State/Protect. Subsequent
/// reads to the same page hit cache (O(n) linear scan across 64 slots).
///
/// Note: the earlier "no syscall" version defaulted to true and relied on a
/// placeholder SEH integration that never landed — result was unprotected
/// dereferences of canonical-but-unmapped VAs from the Present callback,
/// which cascaded via Arxan's VEH chain into stack overflows and D2R zombies.
///
/// VirtualQuery is a kernel32 wrapper — if the ntdll stub is Arxan-hooked we
/// get whatever Arxan lets through, which for a single read-only query per
/// page has been stable in every test. Cross-session cost: ≤ region_count
/// calls per tick (~10-30), not per-deref.
unsafe fn page_readable(va: usize) -> bool {
    let page_va = va & !0xFFF;
    for i in 0..VA_CACHE_SLOTS {
        if G_VA_CACHE.page_va[i] == page_va {
            return G_VA_CACHE.valid[i];
        }
    }
    // Cache miss — probe via VirtualQuery.
    let mut mbi: MemoryBasicInformation = core::mem::zeroed();
    let mbi_size = core::mem::size_of::<MemoryBasicInformation>();
    let got = VirtualQuery(
        page_va as *const core::ffi::c_void,
        &mut mbi as *mut MemoryBasicInformation as *mut core::ffi::c_void,
        mbi_size,
    );
    let valid = got != 0
        && mbi.state == MEM_COMMIT
        && (mbi.protect & (PAGE_NOACCESS | PAGE_GUARD)) == 0;

    let idx = G_VA_CACHE.write_idx % VA_CACHE_SLOTS;
    G_VA_CACHE.page_va[idx] = page_va;
    G_VA_CACHE.valid[idx] = valid;
    G_VA_CACHE.write_idx = G_VA_CACHE.write_idx.wrapping_add(1);
    valid
}

/// Combined check: canonical user-space + 8-byte alignment for ptr-sized reads.
/// D2R heap allocations are always 8-byte aligned; misaligned VAs are reliable
/// garbage indicators (XX/XX/XX fill bytes from uninitialised memory hardly
/// ever land on 8-byte boundaries). Cheap, no syscall.
#[inline(always)]
unsafe fn is_safe_va(va: usize) -> bool {
    is_user_va(va) && (va & 0x7) == 0
}

// ---------------------------------------------------------------------------
// PEB walk — drops GetModuleHandleW dependency. (Phase C, partial.)
// ---------------------------------------------------------------------------

/// Returns the current process's image base (D2R.exe base when this DLL is
/// injected into D2R). Replaces `GetModuleHandleW(NULL)` for module-base
/// resolution — no kernel32 call, no ETW logging, no IAT entry.
#[inline(always)]
unsafe fn peb_image_base() -> usize {
    let peb: usize;
    core::arch::asm!(
        "mov {peb}, gs:[0x60]",
        peb = out(reg) peb,
        options(nostack, preserves_flags),
    );
    *((peb + 0x10) as *const usize)
}

/// Walk PEB.Ldr.InMemoryOrderModuleList looking for a module by basename
/// (case-insensitive ASCII compare). Returns the module's DllBase, or 0 if
/// not found. Replaces `GetModuleHandleW(L"name.dll")`.
unsafe fn peb_find_module(target_name: &[u16]) -> usize {
    let peb: usize;
    core::arch::asm!(
        "mov {peb}, gs:[0x60]",
        peb = out(reg) peb,
        options(nostack, preserves_flags),
    );
    let ldr = *((peb + 0x18) as *const usize);
    if ldr == 0 {
        return 0;
    }
    // PEB_LDR_DATA.InMemoryOrderModuleList @ +0x20
    let head = (ldr + 0x20) as usize;
    let mut entry = *(head as *const usize); // first Flink
    let target_len = target_name_len(target_name);

    let mut safety = 0usize;
    while entry != 0 && entry != head && safety < 256 {
        // LDR_DATA_TABLE_ENTRY starts at -0x10 from this LIST_ENTRY (InMemoryOrderLinks)
        let dll_table = entry.wrapping_sub(0x10);
        let dll_base = *((dll_table + 0x30) as *const usize);
        // BaseDllName UNICODE_STRING @ +0x58 (Length u16, MaxLength u16, _pad u32, Buffer *u16)
        let name_len = *((dll_table + 0x58) as *const u16) as usize / 2;
        let name_ptr = *((dll_table + 0x58 + 8) as *const *const u16);
        if name_len == target_len && name_ptr != core::ptr::null() && peb_name_eq_ci(name_ptr, target_name, name_len) {
            return dll_base;
        }
        entry = *(entry as *const usize);
        safety += 1;
    }
    0
}

#[inline(always)]
fn target_name_len(name: &[u16]) -> usize {
    let mut i = 0;
    while i < name.len() && name[i] != 0 {
        i += 1;
    }
    i
}

/// Case-insensitive ASCII compare of two UTF-16 buffers, length `n`.
unsafe fn peb_name_eq_ci(a: *const u16, b: &[u16], n: usize) -> bool {
    for i in 0..n {
        let mut ca = *a.add(i);
        let mut cb = b[i];
        if ca >= b'A' as u16 && ca <= b'Z' as u16 { ca += 32; }
        if cb >= b'A' as u16 && cb <= b'Z' as u16 { cb += 32; }
        if ca != cb { return false; }
    }
    true
}

/// Read 8 bytes from a D2R virtual address. Two-layer guard: canonical +
/// aligned (cheap) then page-mapped probe via VirtualQuery (cached per tick).
/// Returns 0 when the address is outside user canonical space OR when the
/// backing page is not committed / has PAGE_NOACCESS / PAGE_GUARD.
///
/// Replaces the is_safe_va-only guard which let canonical-but-unmapped
/// pointers through → AV in Present callback → Arxan VEH cascade → zombie.
#[inline(always)]
unsafe fn d2r_read_u64(va: usize) -> u64 {
    if !is_safe_va(va) { return 0; }
    if !page_readable(va) { return 0; }
    core::ptr::read_unaligned(va as *const u64)
}

#[inline(always)]
unsafe fn d2r_read_u32(va: usize) -> u32 {
    if !is_safe_va(va) { return 0; }
    if !page_readable(va) { return 0; }
    core::ptr::read_unaligned(va as *const u32)
}

#[inline(always)]
unsafe fn d2r_read_u16(va: usize) -> u16 {
    if !is_safe_va(va) { return 0; }
    if !page_readable(va) { return 0; }
    core::ptr::read_unaligned(va as *const u16)
}

#[inline(always)]
unsafe fn d2r_read_u8(va: usize) -> u8 {
    if !is_safe_va(va) { return 0; }
    if !page_readable(va) { return 0; }
    core::ptr::read_unaligned(va as *const u8)
}

/// Copy `len` bytes from D2R virtual address `va` into snapshot data blob at
/// cursor, and append a RegionEntry describing the copy. Returns new cursor
/// and new region count.
///
/// If `va == 0` or bounds check fails, writes nothing and returns (cursor, region_count)
/// unchanged — caller must guard with null checks where semantically relevant.
unsafe fn snapshot_add_region(
    shm: *mut SharedBuffer,
    data_cursor: usize,
    region_count: usize,
    va: usize,
    len: usize,
) -> (usize, usize) {
    if !is_user_va(va) || len == 0 || region_count >= SNAPSHOT_REGION_MAX {
        return (data_cursor, region_count);
    }
    // Page-mapped check on START + END page (catches partial-mapped ranges).
    if !page_readable(va) || !page_readable(va + len - 1) {
        return (data_cursor, region_count);
    }
    // End of mirrored range must also stay canonical (no wrap, no kernel range).
    if !is_user_va(va.saturating_add(len).saturating_sub(1)) {
        return (data_cursor, region_count);
    }
    if data_cursor.saturating_add(len) > SNAPSHOT_DATA_SIZE {
        return (data_cursor, region_count); // blob full — drop region
    }

    let base = shm as *mut u8;
    let dst = base.add(OFF_SNAPSHOT_DATA + data_cursor);
    // Copy D2R bytes into SHM. This is a memcpy within our own process.
    core::ptr::copy_nonoverlapping(va as *const u8, dst, len);

    // Write RegionEntry at index `region_count`.
    let entry_off = OFF_SNAPSHOT_REGIONS + region_count * SNAPSHOT_REGION_ENTRY_SIZE;
    let entry = base.add(entry_off) as *mut RegionEntry;
    core::ptr::write(entry, RegionEntry {
        va: va as u64,
        len: len as u32,
        offset: data_cursor as u32,
    });

    (data_cursor + len, region_count + 1)
}

/// CMD_SNAPSHOT_INIT handler — bot writes D2R offset VAs (UnitTable, Expansion,
/// WaypointTable) into SHM at OFF_SNAP_*, then flips CMD_COMMAND_FLAG.
/// We copy them into globals, resolve D2R base, set enabled flag.
unsafe fn dispatch_snapshot_init(shm: *mut SharedBuffer) {
    // Phase C: derive per-boot XOR key from rdtsc (defeats static-scan
    // signatures on D2R offset constants in rmod memory).
    if G_SNAP_VA_XOR_KEY == 0 {
        // Mix high + low rdtsc bits + base addr for non-zero key. Re-keying on
        // re-init is fine — values get re-encoded with new key on each init.
        G_SNAP_VA_XOR_KEY = (rdtsc_u64() as usize ^ 0xA5A5_5A5A_3C3C_C3C3) | 1;
    }
    // Fresh read of bot-supplied offsets — XOR-encode before storing.
    let key = G_SNAP_VA_XOR_KEY;
    G_SNAP_UNIT_TABLE_VA_XOR = (shm_read_u64(shm as *const SharedBuffer, OFF_SNAP_UNIT_TABLE) as usize) ^ key;
    G_SNAP_EXPANSION_VA_XOR  = (shm_read_u64(shm as *const SharedBuffer, OFF_SNAP_EXPANSION) as usize) ^ key;
    G_SNAP_WAYPOINT_VA_XOR   = (shm_read_u64(shm as *const SharedBuffer, OFF_SNAP_WAYPOINT_TABLE) as usize) ^ key;

    // D2R module base — Phase C: PEB->ImageBaseAddress (no kernel32 call).
    let base = peb_image_base();
    G_D2R_BASE = base;

    // Write header magic + version + module base so bot can sanity-check.
    let shm_u8 = shm as *mut u8;
    core::ptr::write_unaligned(shm_u8.add(OFF_SNAP_MAGIC) as *mut u32, SNAP_MAGIC);
    core::ptr::write_unaligned(shm_u8.add(OFF_SNAP_VERSION) as *mut u32, SNAP_VERSION_A);
    core::ptr::write_unaligned(shm_u8.add(OFF_SNAP_D2R_BASE) as *mut u64, base as u64);
    core::ptr::write_unaligned(shm_u8.add(OFF_SNAP_FLAGS) as *mut u32, SNAP_FLAG_ENABLED);

    G_SNAPSHOT_ENABLED = true;

    // Walker worker-thread path SHELVED for this session (six live tests
    // 2026-04-17 all converge on same d3d12+0xCF2FE AV when walker activates,
    // regardless of thread model or throttle). Next session: GID-style
    // ROP-based read from D2R's own gadgets — walker emits no code-path
    // fingerprint because the reads ARE D2R's own code executing. See
    // GID-v4.12/D2RB/Misc64.dll — GreyMagic + CallInjected64ROP +
    // PresentProxyHookAssembleInsteadOfBytes. For now G_WORKER_SHM is
    // latched so any future activation can share the snapshot init's shm.
    G_WORKER_SHM = shm;
    let _ = G_WORKER_STOP;

    shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_DONE);
    shm_write_u32(shm, OFF_COMMAND_FLAG, 0);
}

/// Per-Present-frame snapshot writer. Walks D2R statics to find main player,
/// copies all regions needed by Go's `GetRawPlayerUnits` + `GetPlayerUnit`.
/// Bumps tick_counter atomically after complete write so bot's SnapshotReader
/// can see a consistent snapshot.
/// No-op: walker was previously called from Present callback here, but that
/// blew d3d12's hang-watchdog (live test 2026-04-17 confirmed: D2R dies with
/// 0xC0000005 at d3d12.dll+0xCF2FE seconds after SnapshotInit). Walker now
/// runs on a dedicated worker thread (`snapshot_worker_thread_fn`). Present
/// returns immediately.
unsafe fn snapshot_tick_write(_shm: *mut SharedBuffer) {
    // Intentionally empty. Walker is off-thread.
}

/// Full D2R memory scan. Invoked from worker thread at ~33 Hz (30 ms sleep).
/// Bumps tick_counter atomically at end so bot's SnapshotReader sees fresh data.
unsafe fn snapshot_walker_scan(shm: *mut SharedBuffer) {
    if !G_SNAPSHOT_ENABLED || G_SNAP_UNIT_TABLE_VA_XOR == 0 {
        return;
    }

    // Diagnostic gate: bit 3 (WALKER_SKIP) of OFF_SNAP_FLAGS → bump tick only,
    // skip all D2R derefs. Isolates "SnapshotInit alone vs walker reads".
    let flags_now = core::ptr::read_volatile((shm as *const u8).add(OFF_SNAP_FLAGS) as *const u32);
    if flags_now & SNAP_FLAG_WALKER_SKIP != 0 {
        let shm_u8 = shm as *mut u8;
        let tick = core::ptr::read_unaligned(shm_u8.add(OFF_SNAP_TICK) as *const u64);
        core::ptr::write_volatile(shm_u8.add(OFF_SNAP_TICK) as *mut u64, tick.wrapping_add(1));
        return;
    }

    // Reset per-tick page validity cache. Page mappings can shift between
    // ticks as D2R allocates/frees memory; cache only valid within one tick.
    va_cache_reset();

    let shm_u8 = shm as *mut u8;
    let mut cursor: usize = 0;
    let mut regions: usize = 0;
    let mut flags: u32 = SNAP_FLAG_ENABLED;

    // R1: UnitTable slots (128 pointers × 8 B).
    let (c, r) = snapshot_add_region(shm, cursor, regions, snap_unit_table_va(), 128 * 8);
    cursor = c; regions = r;

    // R1b: 8-byte ptr slot at snap_expansion_va(). Go reads
    //   `gd.Process.ReadUInt(moduleBase + offset.Expansion, Uint64)`
    // to get the expansion struct address. SnapshotReader must be able to serve
    // that read, so the 8 bytes AT the static slot must be mirrored too —
    // separate from the dereferenced target below.
    let (c, r) = snapshot_add_region(shm, cursor, regions, snap_expansion_va(), 8);
    cursor = c; regions = r;

    // R2: expansion struct target (dereferenced — needs +0x5C for LoD flag).
    let exp_ptr_target = d2r_read_u64(snap_expansion_va()) as usize;
    if exp_ptr_target != 0 {
        let (c, r) = snapshot_add_region(shm, cursor, regions, exp_ptr_target, 0x80);
        cursor = c; regions = r;
    }

    // R2b: 8-byte ptr slot at snap_waypoint_va(). Go reads
    //   `gd.Process.ReadUInt(moduleBase + offset.WaypointTableOffset, Uint64)`
    // in WaypointTableData.
    let (c, r) = snapshot_add_region(shm, cursor, regions, snap_waypoint_va(), 8);
    cursor = c; regions = r;

    // R3 + R4: waypoint struct + data (decodeWaypointMasks).
    let wp_struct_va = d2r_read_u64(snap_waypoint_va()) as usize;
    if wp_struct_va != 0 {
        let (c, r) = snapshot_add_region(shm, cursor, regions, wp_struct_va, 0x100);
        cursor = c; regions = r;
        // waypoint data buffer at struct+0x10 → dereference
        let wp_data_va = d2r_read_u64(wp_struct_va + 0x10) as usize;
        if wp_data_va != 0 {
            let (c, r) = snapshot_add_region(shm, cursor, regions, wp_data_va, 0x200);
            cursor = c; regions = r;
        }
    }

    // Generic static-region table — bot enqueues {va, len, role} triples in
    // SHM before CmdSnapshotInit. We mirror each head verbatim every tick so
    // bot can read hover / UI / WidgetStates / FPS / KeyBindings / Quest / TZ
    // / etc. via the same SnapshotReader region lookup. role != REGULAR means
    // "also dereference this head and mirror the chain's inner targets" — B2b.
    let shm_u8_entries = shm as *const u8;
    let static_count = core::ptr::read_unaligned(
        shm_u8_entries.add(OFF_SNAP_STATIC_COUNT) as *const u32,
    ) as usize;
    let clamped = if static_count > SNAP_STATIC_MAX { SNAP_STATIC_MAX } else { static_count };
    for i in 0..clamped {
        let entry_va = shm_u8_entries.add(OFF_SNAP_STATIC_TABLE + i * 16) as *const StaticRegion;
        let entry = core::ptr::read_unaligned(entry_va);
        if entry.va == 0 || entry.len == 0 {
            continue;
        }
        // Mirror head region.
        let (c, r) = snapshot_add_region(shm, cursor, regions, entry.va as usize, entry.len as usize);
        cursor = c; regions = r;

        // Role-specific chain walkers. is_safe_va guard (NtQueryVirtualMemory
        // page-mapped check) keeps Present-thread AV-free even on garbage VAs.
        match entry.role {
            STATIC_ROLE_PING_CHAIN => {
                // Go: ptrToStructPtr = moduleBase + offset.Ping  → head (mirrored above as 8 B).
                //     structPtrAddr = Read(ptrToStructPtr, Uint64)
                //     ping          = Read(structPtrAddr + 36, Uint32)
                let struct_ptr = d2r_read_u64(entry.va as usize) as usize;
                if struct_ptr != 0 {
                    let (c, r) = snapshot_add_region(shm, cursor, regions, struct_ptr, 40);
                    cursor = c; regions = r;
                }
            }
            STATIC_ROLE_QUEST_CHAIN => {
                // Go: questDataPtr   = Read(moduleBase + offset.QuestInfo, Uint64)  → head 8 B mirrored.
                //     flagsBufferPtr = Read(questDataPtr, Uint64)                   → need 8 B at questDataPtr.
                //     gameQuestsBytes = ReadBytes(flagsBufferPtr, 82)
                let quest_data = d2r_read_u64(entry.va as usize) as usize;
                if quest_data != 0 {
                    let (c, r) = snapshot_add_region(shm, cursor, regions, quest_data, 8);
                    cursor = c; regions = r;
                    let flags_buf = d2r_read_u64(quest_data) as usize;
                    if flags_buf != 0 {
                        let (c, r) = snapshot_add_region(shm, cursor, regions, flags_buf, 82);
                        cursor = c; regions = r;
                    }
                }
            }
            STATIC_ROLE_TZ_CHAIN => {
                // Go: zonesPtr = Read(moduleBase + offset.TZ, Uint64)             → head ptr @ 0 (head mirrored 16 B).
                //     count    = Read(moduleBase + offset.TZ + 0x8, Uint8)
                //     for i: tz = Read(zonesPtr + i*4, Uint32)
                // Cap at 8 zones defensively (real-world count is 1-3).
                let zones_ptr = d2r_read_u64(entry.va as usize) as usize;
                let mut count = d2r_read_u8(entry.va as usize + 8) as usize;
                if count > 8 { count = 8; }
                if zones_ptr != 0 && count > 0 {
                    let (c, r) = snapshot_add_region(shm, cursor, regions, zones_ptr, count * 4);
                    cursor = c; regions = r;
                }
            }
            STATIC_ROLE_ROSTER_CHAIN => {
                // Go (roster.go): partyStruct = Read(moduleBase + offset.RosterOffset, Uint64)  → head 8 B mirrored.
                //                 partyStruct = Read(partyStruct + 0x148, Uint64)              → skip main-player slot.
                //                 while partyStruct > 0:
                //                     name(16) @ partyStruct, area @+0x5C, xPos @+0x60, yPos @+0x64
                //                     partyStruct = Read(partyStruct + 0x148, Uint64)
                // We mirror 0x70 B per member (covers all fields up to yPos+4 at +0x68 < 0x70).
                // Cap member count at 16 defensively; real party max = 8.
                let party_head = d2r_read_u64(entry.va as usize) as usize;
                if party_head != 0 {
                    // Mirror the head struct itself so Go's first ReadUInt(partyStruct+0x148) hits.
                    let (c, r) = snapshot_add_region(shm, cursor, regions, party_head, 0x150);
                    cursor = c; regions = r;
                    let mut next = d2r_read_u64(party_head + 0x148) as usize;
                    let mut safety = 0usize;
                    while next != 0 && safety < 16 {
                        let (c, r) = snapshot_add_region(shm, cursor, regions, next, 0x150);
                        cursor = c; regions = r;
                        next = d2r_read_u64(next + 0x148) as usize;
                        safety += 1;
                    }
                }
            }
            _ => {}
        }
    }

    // B3 walkers — is_safe_va guard (canonical + 8-byte alignment) blocks
    // most garbage pointers; future SEH wrap will harden further.
    snap_walk_entity_row(shm, &mut cursor, &mut regions, snap_unit_table_va() + 1 * 1024, true);  // monsters/corpses
    snap_walk_entity_row(shm, &mut cursor, &mut regions, snap_unit_table_va() + 2 * 1024, false); // objects
    snap_walk_entity_row(shm, &mut cursor, &mut regions, snap_unit_table_va() + 4 * 1024, false); // items (B3.2)
    snap_walk_entity_row(shm, &mut cursor, &mut regions, snap_unit_table_va() + 5 * 1024, false); // entrances

    // Scan UnitTable for main player (128 slots × 8 bytes, each a linked list head)
    for i in 0..128usize {
        let slot_va = snap_unit_table_va() + i * 8;
        let mut player_unit_va = d2r_read_u64(slot_va) as usize;
        while player_unit_va != 0 {
            // Copy playerUnit struct (0x200 bytes — covers all fixed offsets used)
            let (c, r) = snapshot_add_region(shm, cursor, regions, player_unit_va, 0x200);
            cursor = c; regions = r;

            // Read inventory ptr to determine isMainPlayer
            let inventory_va = d2r_read_u64(player_unit_va + 0x90) as usize;
            let is_main = if inventory_va != 0 {
                let exp_char_ptr = d2r_read_u64(snap_expansion_va()) as usize;
                let is_lod = if exp_char_ptr != 0 {
                    d2r_read_u16(exp_char_ptr + 0x5C) >= 1 // CharLoD == 1
                } else { false };
                let offset = if is_lod { 0x70 } else { 0x30 };
                d2r_read_u16(inventory_va + offset) > 0
            } else { false };

            if is_main && inventory_va != 0 {
                flags |= SNAP_FLAG_MAIN_PLAYER_FOUND;

                // Mirror main player's rich pointer chain.
                snap_walk_main_player(shm, &mut cursor, &mut regions, player_unit_va, inventory_va);
            } else if inventory_va != 0 {
                // Non-main player (e.g. party member or corpse) — include inventory
                // so state checks work. Phase A doesn't traverse their full chain.
                let (c, r) = snapshot_add_region(shm, cursor, regions, inventory_va, 0x80);
                cursor = c; regions = r;
            }

            // Next player in this chain
            player_unit_va = d2r_read_u64(player_unit_va + 0x158) as usize;
        }
    }

    // Write header fields (magic/version/base already set by init).
    core::ptr::write_unaligned(shm_u8.add(OFF_SNAP_REGION_COUNT) as *mut u32, regions as u32);
    core::ptr::write_unaligned(shm_u8.add(OFF_SNAP_DATA_BYTES) as *mut u32, cursor as u32);
    core::ptr::write_unaligned(shm_u8.add(OFF_SNAP_FLAGS) as *mut u32, flags);
    core::ptr::write_unaligned(shm_u8.add(OFF_SNAP_LAST_RDTSC) as *mut u64, rdtsc_u64());

    // Bump tick counter last so bot sees a complete snapshot.
    let tick_ptr = shm_u8.add(OFF_SNAP_TICK) as *mut u64;
    let old = core::ptr::read_unaligned(tick_ptr);
    core::ptr::write_unaligned(tick_ptr, old.wrapping_add(1));
}

/// Worker thread entry. Sleeps 30 ms between scans so Present callback isn't
/// burdened — and walker cost doesn't register in d3d12's hang-watchdog path.
/// Runs until G_WORKER_STOP is set (by uninstall_present_detour) or the DLL
/// unloads. Worker creates itself on first successful SnapshotInit; single
/// instance per process (idempotent via G_WORKER_THREAD null-check).
unsafe extern "system" fn snapshot_worker_thread_fn(_param: *mut core::ffi::c_void) -> DWORD {
    loop {
        if G_WORKER_STOP {
            break;
        }
        Sleep(30);
        let shm = G_WORKER_SHM;
        if shm.is_null() || !G_SNAPSHOT_ENABLED {
            continue;
        }
        snapshot_walker_scan(shm);
    }
    0
}

/// Walk and mirror main player's full pointer chain:
///   playerUnit → pUnitData → name, pathAddress → room1 → room2 → level,
///   inventory, statsListEx (+ base/full stats arrays + state bits),
///   skillList (+ linked-list walk + txt derefs + left/right skill txts).
unsafe fn snap_walk_main_player(
    shm: *mut SharedBuffer,
    cursor: &mut usize,
    regions: &mut usize,
    player_unit_va: usize,
    inventory_va: usize,
) {
    // inventory struct (main-player flags)
    let (c, r) = snapshot_add_region(shm, *cursor, *regions, inventory_va, 0x80);
    *cursor = c; *regions = r;

    // pUnitData (name struct) at playerUnit+0x10
    let p_unit_data_va = d2r_read_u64(player_unit_va + 0x10) as usize;
    if p_unit_data_va != 0 {
        let (c, r) = snapshot_add_region(shm, *cursor, *regions, p_unit_data_va, 0x40);
        *cursor = c; *regions = r;
        // playerName at pUnitData+0x00
        let name_va = d2r_read_u64(p_unit_data_va) as usize;
        if name_va != 0 {
            let (c, r) = snapshot_add_region(shm, *cursor, *regions, name_va, 0x40);
            *cursor = c; *regions = r;
        }
    }

    // pathAddress → room1 → room2 → level chain (for Area ID)
    let path_va = d2r_read_u64(player_unit_va + 0x38) as usize;
    if path_va != 0 {
        let (c, r) = snapshot_add_region(shm, *cursor, *regions, path_va, 0x100);
        *cursor = c; *regions = r;
        let room1_va = d2r_read_u64(path_va + 0x20) as usize;
        if room1_va != 0 {
            let (c, r) = snapshot_add_region(shm, *cursor, *regions, room1_va, 0x40);
            *cursor = c; *regions = r;
            let room2_va = d2r_read_u64(room1_va + 0x18) as usize;
            if room2_va != 0 {
                let (c, r) = snapshot_add_region(shm, *cursor, *regions, room2_va, 0x100);
                *cursor = c; *regions = r;
                let level_va = d2r_read_u64(room2_va + 0x90) as usize;
                if level_va != 0 {
                    let (c, r) = snapshot_add_region(shm, *cursor, *regions, level_va, 0x200);
                    *cursor = c; *regions = r;
                }
            }
        }
    }

    // statsListExPtr — covers state flags (+0xAF0..+0xB10) + linked heads
    let sle_va = d2r_read_u64(player_unit_va + 0x88) as usize;
    if sle_va != 0 {
        let (c, r) = snapshot_add_region(shm, *cursor, *regions, sle_va, 0xB20);
        *cursor = c; *regions = r;
        // base stats flat array at statsListExPtr+0x30
        snap_add_stats_array(shm, cursor, regions, sle_va + 0x30);
        // full stats flat array at statsListExPtr+0xA8
        snap_add_stats_array(shm, cursor, regions, sle_va + 0xA8);
    }

    // skillListPtr — linked list head + left/right skill txt ptrs
    let skill_list_va = d2r_read_u64(player_unit_va + 0x100) as usize;
    if skill_list_va != 0 {
        let (c, r) = snapshot_add_region(shm, *cursor, *regions, skill_list_va, 0x20);
        *cursor = c; *regions = r;

        // Left skill txt
        let left_ptr = d2r_read_u64(skill_list_va + 0x08) as usize;
        if left_ptr != 0 {
            let left_txt = d2r_read_u64(left_ptr) as usize;
            if left_txt != 0 {
                let (c, r) = snapshot_add_region(shm, *cursor, *regions, left_ptr, 0x10);
                *cursor = c; *regions = r;
                let (c, r) = snapshot_add_region(shm, *cursor, *regions, left_txt, 0x10);
                *cursor = c; *regions = r;
            }
        }
        // Right skill txt
        let right_ptr = d2r_read_u64(skill_list_va + 0x10) as usize;
        if right_ptr != 0 {
            let right_txt = d2r_read_u64(right_ptr) as usize;
            if right_txt != 0 {
                let (c, r) = snapshot_add_region(shm, *cursor, *regions, right_ptr, 0x10);
                *cursor = c; *regions = r;
                let (c, r) = snapshot_add_region(shm, *cursor, *regions, right_txt, 0x10);
                *cursor = c; *regions = r;
            }
        }

        // Walk skills linked list
        let mut skill_ptr = d2r_read_u64(skill_list_va) as usize;
        let mut count = 0usize;
        while skill_ptr != 0 && count < 256 {
            // Skill node: +0x00 txtPtr, +0x08 next, +0x40 lvl, +0x48 qty, +0x50 charges
            let (c, r) = snapshot_add_region(shm, *cursor, *regions, skill_ptr, 0x60);
            *cursor = c; *regions = r;
            let txt = d2r_read_u64(skill_ptr) as usize;
            if txt != 0 {
                let (c, r) = snapshot_add_region(shm, *cursor, *regions, txt, 0x10);
                *cursor = c; *regions = r;
            }
            skill_ptr = d2r_read_u64(skill_ptr + 0x08) as usize;
            count += 1;
        }
    }
}

/// Mirror a flat stats array (header+count at `header_va`; body at *header_va
/// for count*10 bytes). Mirrors Go's getStatsList read pattern.
unsafe fn snap_add_stats_array(
    shm: *mut SharedBuffer,
    cursor: &mut usize,
    regions: &mut usize,
    header_va: usize,
) {
    // Header is already within the enclosing statsListEx copy — no separate
    // region needed. But bot's Go code re-reads via ReadBytes(header_va, 0x10),
    // so we mirror that exact call: 0x10 bytes at header_va.
    let (c, r) = snapshot_add_region(shm, *cursor, *regions, header_va, 0x10);
    *cursor = c; *regions = r;

    let head_va = d2r_read_u64(header_va) as usize;
    let count = d2r_read_u64(header_va + 0x08) as usize;
    if head_va != 0 && count > 0 && count <= 512 {
        let body_len = count * 10; // Go allocates count*10 even though entry is 8 B
        let (c, r) = snapshot_add_region(shm, *cursor, *regions, head_va, body_len);
        *cursor = c; *regions = r;
    }
}

/// Mirror one row of D2R's UnitTable (row_head_va points at a 128 × 8 B table
/// of unit linked-list heads). Each non-zero slot chains via +0x158 next ptr.
/// Per unit we mirror 0x1B0 B (covers +0x1AE isCorpse byte for monsters,
/// the deepest offset touched by Go walkers) plus one region each for
/// unitData (+0x10 deref, 0x60 B) and path (+0x38 deref, 0x20 B).
///
/// Cap the unit mirror count per row to stay under SNAPSHOT_REGION_MAX;
/// overflow is silently skipped (Go walker sees a truncated list — acceptable
/// trade-off at peak combat density).
///
/// `with_stats` (B3 pass 2, monsters): when true, also mirrors statsListEx at
/// +0x88 deref (0x60 B) and its stats array. Left false for objects/entrances.
unsafe fn snap_walk_entity_row(
    shm: *mut SharedBuffer,
    cursor: &mut usize,
    regions: &mut usize,
    row_head_va: usize,
    with_stats: bool,
) {
    // Mirror the row table itself — Go reads 128*8 B via ReadBytes.
    let (c, r) = snapshot_add_region(shm, *cursor, *regions, row_head_va, 128 * 8);
    *cursor = c; *regions = r;

    const MAX_UNITS_PER_ROW: usize = 32; // safety cap vs region budget + stack burn rate
    let mut mirrored = 0usize;

    for slot_idx in 0..128usize {
        if mirrored >= MAX_UNITS_PER_ROW {
            break;
        }
        let slot_va = row_head_va + slot_idx * 8;
        let mut unit_va = d2r_read_u64(slot_va) as usize;
        let mut depth = 0usize;
        while is_user_va(unit_va) && depth < 8 && mirrored < MAX_UNITS_PER_ROW {
            // Unit struct — 0x1B0 B covers all offsets Go reads (type @0x00,
            // txtFileNo @0x04, unitID @0x08, mode @0x0C, unitData @0x10,
            // pathAddr @0x38, statsListEx @0x88, next @0x158, isCorpse @0x1AE).
            let (c, r) = snapshot_add_region(shm, *cursor, *regions, unit_va, 0x1B0);
            *cursor = c; *regions = r;

            // unitData (+0x10 deref) — 0x60 B covers interactType @+0x08,
            // owner string @+0x34 (32 B) extending to +0x54.
            let unit_data_va = d2r_read_u64(unit_va + 0x10) as usize;
            if unit_data_va != 0 {
                let (c, r) = snapshot_add_region(shm, *cursor, *regions, unit_data_va, 0x60);
                *cursor = c; *regions = r;
            }

            // path (+0x38 deref) — 0x20 B covers posX/posY at either +0x02/+0x06
            // (monsters) or +0x10/+0x14 (objects/entrances).
            let path_va = d2r_read_u64(unit_va + 0x38) as usize;
            if path_va != 0 {
                let (c, r) = snapshot_add_region(shm, *cursor, *regions, path_va, 0x20);
                *cursor = c; *regions = r;
            }

            if with_stats {
                // statsListEx (+0x88 deref) — split into two regions so we don't
                // mirror the ~2 KB middle that no Go code reads:
                //   head (0x60 B) covers +0x30 statPtr, +0x38 statCount
                //   state flags (0x30 B @+0xAF0) covers GetStates's 8 × u32 read
                let sle_va = d2r_read_u64(unit_va + 0x88) as usize;
                if sle_va != 0 {
                    let (c, r) = snapshot_add_region(shm, *cursor, *regions, sle_va, 0x60);
                    *cursor = c; *regions = r;
                    let (c, r) = snapshot_add_region(shm, *cursor, *regions, sle_va + 0xAF0, 0x30);
                    *cursor = c; *regions = r;
                    // Monster stat array body: Go reads statPtr+0x2 for statCount*8 B.
                    let stat_hdr_ptr = d2r_read_u64(sle_va + 0x30) as usize;
                    let stat_count = d2r_read_u64(sle_va + 0x38) as usize;
                    // Cap at 64 (typical monster: 20-40 stats; super uniques up to ~50).
                    if is_user_va(stat_hdr_ptr) && stat_count > 0 && stat_count <= 64 {
                        let (c, r) = snapshot_add_region(shm, *cursor, *regions, stat_hdr_ptr + 0x2, stat_count * 8);
                        *cursor = c; *regions = r;
                    }
                }
            }

            unit_va = d2r_read_u64(unit_va + 0x158) as usize;
            depth += 1;
            mirrored += 1;
        }
    }
}

#[inline(always)]
unsafe fn rdtsc_u64() -> u64 {
    // rmod runs on x86_64 Windows — rdtsc is always available.
    core::arch::x86_64::_rdtsc()
}
