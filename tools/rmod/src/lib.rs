#![no_std]
#![allow(non_snake_case)]
#![allow(non_camel_case_types)]
#![allow(clippy::missing_safety_doc)]

//! Runtime graphics module - frame-synchronized command dispatch via shared memory.
//! no_std — zero CRT dependency, works with manual PE mapping.

use core::sync::atomic::{AtomicU32, Ordering};
use core::panic::PanicInfo;

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
    fn CloseHandle(hObject: HANDLE) -> BOOL;
    fn OpenThread(dwDesiredAccess: DWORD, bInheritHandle: BOOL, dwThreadId: DWORD) -> HANDLE;
    fn SuspendThread(hThread: HANDLE) -> DWORD;
    fn ResumeThread(hThread: HANDLE) -> DWORD;
    fn GetThreadContext(hThread: HANDLE, lpContext: *mut CONTEXT) -> BOOL;
    fn SetThreadContext(hThread: HANDLE, lpContext: *const CONTEXT) -> BOOL;
    fn VirtualAlloc(
        lpAddress: *const core::ffi::c_void,
        dwSize: usize,
        flAllocationType: DWORD,
        flProtect: DWORD,
    ) -> *mut core::ffi::c_void;
    fn VirtualProtect(
        lpAddress: *const core::ffi::c_void,
        dwSize: usize,
        flNewProtect: DWORD,
        lpflOldProtect: *mut DWORD,
    ) -> BOOL;
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
    fn NtQueueApcThread(
        ThreadHandle: HANDLE,
        ApcRoutine: *const core::ffi::c_void,
        ApcArgument1: *mut core::ffi::c_void,
        ApcArgument2: *mut core::ffi::c_void,
        ApcArgument3: *mut core::ffi::c_void,
    ) -> i32; // NTSTATUS
}

#[link(name = "user32")]
extern "system" {
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
}

// ---------------------------------------------------------------------------
// Application Constants
// ---------------------------------------------------------------------------

const MAGIC: u32 = 0x524D_4F44; // "RMOD"
const VERSION: u32 = 1;

const CMD_NOP: u32 = 0;
const CMD_SEND_PACKET: u32 = 1;

const STATUS_DONE: u32 = 1;
const STATUS_ERROR: u32 = 2;

/// Size of the inline detour (absolute indirect JMP: FF 25 00 00 00 00 + 8-byte addr).
const DETOUR_SIZE: usize = 14;

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
#[repr(C, align(4096))]
struct SharedBuffer {
    data: [u8; 4096],
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
const OFF_PACKET_DATA: usize     = 0x100;

const _: () = assert!(core::mem::size_of::<SharedBuffer>() == 4096);

// ---------------------------------------------------------------------------
// Global state  (set once by the worker thread, read by the handler)
// ---------------------------------------------------------------------------

static mut G_SHM: *mut SharedBuffer = core::ptr::null_mut();
static mut G_TRAMPOLINE: *const u8 = core::ptr::null();
static mut G_PRESENT_ADDR: usize = 0;

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
    }
    TRUE
}

// ---------------------------------------------------------------------------
// Exported init — called directly by manual mapper via CreateRemoteThread.
// lpParameter = address of shared buffer (allocated by Go in D2R via VirtualAllocEx).
// ---------------------------------------------------------------------------

#[no_mangle]
pub unsafe extern "system" fn Init(param: *mut core::ffi::c_void) -> DWORD {
    let shm = param as *mut SharedBuffer;
    if shm.is_null() {
        return 0xE000;
    }
    G_SHM = shm;

    match init_from_shm(shm) {
        Ok(()) => 0,
        Err(code) => {
            shm_write_u32(shm, OFF_ERROR_CODE, code);
            shm_write_u32(shm, OFF_STATUS_FLAG, STATUS_ERROR);
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

    let present_addr = find_present_address(shm)?;
    G_PRESENT_ADDR = present_addr;
    shm_write_u64(shm, OFF_ORIGINAL_PRESENT, present_addr as u64);
    shm_write_u32(shm, OFF_DEBUG_STEP, 0x08);

    install_detour(present_addr, shm)?;

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

    // Build "SvcRt_{PID}" as a null-terminated wide string.
    // No namespace prefix — defaults to session-local, avoids privilege issues.
    let prefix: &[u16] = &[
        b'S' as u16, b'v' as u16, b'c' as u16, b'R' as u16, b't' as u16,
        b'_' as u16,
    ];
    let mut name_buf = [0u16; 32];
    let mut i = 0;
    for &ch in prefix {
        name_buf[i] = ch;
        i += 1;
    }
    // Append PID as decimal digits.
    i = write_u32_wide(&mut name_buf, i, pid);
    name_buf[i] = 0; // null terminator

    let handle = OpenFileMappingW(FILE_MAP_ALL_ACCESS, FALSE, name_buf.as_ptr());
    if handle.is_null() {
        return Err(0xE010);
    }

    let view = MapViewOfFile(handle, FILE_MAP_ALL_ACCESS, 0, 0, 4096);
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
    let hmod = GetModuleHandleW(dxgi_name.as_ptr());
    if hmod.is_null() {
        return Err(0xE020);
    }

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

    // 2. Copy original prologue bytes into trampoline.
    core::ptr::copy_nonoverlapping(present_ptr, trampoline, DETOUR_SIZE);
    shm_write_u32(shm, OFF_DEBUG_STEP, 0x0B); // prologue copied

    // 3. Append absolute JMP back to Present + DETOUR_SIZE.
    let jmp_back_target = present_addr + DETOUR_SIZE;
    write_abs_jmp(trampoline.add(DETOUR_SIZE), jmp_back_target);

    G_TRAMPOLINE = trampoline;

    // 4. Build the handler thunk (offset 256 in the same page).
    let handler_thunk = trampoline.add(256);
    build_handler_thunk(handler_thunk, trampoline);
    shm_write_u32(shm, OFF_DEBUG_STEP, 0x0C); // handler thunk built

    // 5. Overwrite Present with JMP to our handler thunk.
    let mut old_protect: DWORD = 0;
    if VirtualProtect(
        present_ptr as *const core::ffi::c_void,
        DETOUR_SIZE,
        PAGE_EXECUTE_READWRITE,
        &mut old_protect,
    ) == 0
    {
        return Err(0xE031);
    }
    shm_write_u32(shm, OFF_DEBUG_STEP, 0x0D); // VirtualProtect OK

    write_abs_jmp(present_ptr as *mut u8, handler_thunk as usize);

    FlushInstructionCache(GetCurrentProcess(), present_ptr as _, DETOUR_SIZE);
    shm_write_u32(shm, OFF_DEBUG_STEP, 0x0E); // detour written

    // Restore original page protection.
    let mut dummy: DWORD = 0;
    VirtualProtect(
        present_ptr as *const core::ffi::c_void,
        DETOUR_SIZE,
        old_protect,
        &mut dummy,
    );

    Ok(())
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

    // sub rsp, 0x80  (128 bytes for XMM save area)
    w.emit(&[0x48, 0x81, 0xEC, 0x80, 0x00, 0x00, 0x00]);

    // movaps [rsp+N], xmmN  — save XMM0-5
    w.emit(&[0x0F, 0x29, 0x04, 0x24]);                // xmm0 -> [rsp+0x00]
    w.emit(&[0x0F, 0x29, 0x4C, 0x24, 0x10]);          // xmm1 -> [rsp+0x10]
    w.emit(&[0x0F, 0x29, 0x54, 0x24, 0x20]);          // xmm2 -> [rsp+0x20]
    w.emit(&[0x0F, 0x29, 0x5C, 0x24, 0x30]);          // xmm3 -> [rsp+0x30]
    w.emit(&[0x0F, 0x29, 0x64, 0x24, 0x40]);          // xmm4 -> [rsp+0x40]
    w.emit(&[0x0F, 0x29, 0x6C, 0x24, 0x50]);          // xmm5 -> [rsp+0x50]

    // sub rsp, 0x28  (shadow space + alignment for CALL)
    w.emit(&[0x48, 0x83, 0xEC, 0x28]);

    // movabs rax, <dispatch_commands address>
    let dispatch_addr = dispatch_commands as *const () as usize;
    w.emit(&[0x48, 0xB8]);
    w.emit(&dispatch_addr.to_le_bytes());
    // call rax
    w.emit(&[0xFF, 0xD0]);

    // add rsp, 0x28
    w.emit(&[0x48, 0x83, 0xC4, 0x28]);

    // Restore XMM0-5.
    w.emit(&[0x0F, 0x28, 0x04, 0x24]);                // xmm0 <- [rsp+0x00]
    w.emit(&[0x0F, 0x28, 0x4C, 0x24, 0x10]);          // xmm1 <- [rsp+0x10]
    w.emit(&[0x0F, 0x28, 0x54, 0x24, 0x20]);          // xmm2 <- [rsp+0x20]
    w.emit(&[0x0F, 0x28, 0x5C, 0x24, 0x30]);          // xmm3 <- [rsp+0x30]
    w.emit(&[0x0F, 0x28, 0x64, 0x24, 0x40]);          // xmm4 <- [rsp+0x40]
    w.emit(&[0x0F, 0x28, 0x6C, 0x24, 0x50]);          // xmm5 <- [rsp+0x50]

    // add rsp, 0x80
    w.emit(&[0x48, 0x81, 0xC4, 0x80, 0x00, 0x00, 0x00]);

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

    // Fast path: no pending command.
    if shm_read_u32(shm, OFF_COMMAND_FLAG) == 0 {
        return;
    }

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

#[inline(always)]
unsafe fn shm_write_u64(shm: *mut SharedBuffer, offset: usize, val: u64) {
    core::ptr::write_unaligned((shm as *mut u8).add(offset) as *mut u64, val);
}
