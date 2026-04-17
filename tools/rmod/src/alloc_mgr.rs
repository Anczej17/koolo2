//! In-process RWX memory management for GID-style hook + ROP chain injection.
//!
//! Wraps VirtualAlloc/VirtualFree as RAII-ish primitives. Since rmod is
//! injected INTO D2R (same process as our target code), we don't use the
//! cross-process VirtualAllocEx form — everything is in-process.
//!
//! Types:
//!   - `AllocatedMemory` — owns a VirtualAlloc'd region; `Drop` frees.
//!   - `alloc_near(target_va, size)` — VirtualAlloc within ±2 GB of target so
//!     RIP-relative disp32 jumps/calls fit without needing abs-indirect.
//!
//! No `alloc` crate: rmod is #![no_std]. RAII is manual via `Drop` impl that
//! uses `core::` + Windows FFI.

use core::ptr;

// Re-declare the Windows functions we need — inline so no_std has no `extern`
// resolution issues at link time. These must match lib.rs's existing decls.
#[link(name = "kernel32")]
extern "system" {
    fn VirtualAlloc(
        lpAddress: *const core::ffi::c_void,
        dwSize: usize,
        flAllocationType: u32,
        flProtect: u32,
    ) -> *mut core::ffi::c_void;

    fn VirtualFree(
        lpAddress: *mut core::ffi::c_void,
        dwSize: usize,
        dwFreeType: u32,
    ) -> i32;

    fn VirtualQuery(
        lpAddress: *const core::ffi::c_void,
        lpBuffer: *mut core::ffi::c_void,
        dwLength: usize,
    ) -> usize;
}

const MEM_COMMIT:       u32 = 0x0000_1000;
const MEM_RESERVE:      u32 = 0x0000_2000;
const MEM_RELEASE:      u32 = 0x0000_8000;
const MEM_FREE:         u32 = 0x0001_0000;
const PAGE_EXECUTE_RW:  u32 = 0x0000_0040;
const PAGE_NOACCESS:    u32 = 0x0000_0001;

/// MEMORY_BASIC_INFORMATION — x64 layout. We read enough fields to pick an
/// alloc site inside a MEM_FREE region. Other fields are not inspected.
#[repr(C)]
struct MBI {
    base_address:       *const core::ffi::c_void,
    allocation_base:    *const core::ffi::c_void,
    allocation_protect: u32,
    partition_id:       u16,
    _pad:               u16,
    region_size:        usize,
    state:              u32, // MEM_COMMIT / MEM_FREE / MEM_RESERVE
    protect:            u32,
    type_:              u32,
}

/// RAII wrapper for a VirtualAlloc'd RWX region. Dropping frees with
/// `VirtualFree(..., MEM_RELEASE)`. Leak-safe unless the caller manually
/// `mem::forget`s the handle (e.g., when the page is intentionally leaked
/// because it backs an active trampoline).
pub struct AllocatedMemory {
    base: *mut u8,
    size: usize,
}

impl AllocatedMemory {
    /// Allocate `size` RWX bytes anywhere the OS chooses. Returns None on
    /// allocation failure.
    pub unsafe fn new(size: usize) -> Option<Self> {
        let ptr = VirtualAlloc(
            ptr::null(),
            size,
            MEM_COMMIT | MEM_RESERVE,
            PAGE_EXECUTE_RW,
        ) as *mut u8;
        if ptr.is_null() {
            return None;
        }
        Some(Self { base: ptr, size })
    }

    /// Allocate `size` RWX bytes at a suggested address. Windows may round up
    /// to the next free region — caller must verify `.as_ptr()` if a precise
    /// placement is required.
    pub unsafe fn new_at(hint: *const core::ffi::c_void, size: usize) -> Option<Self> {
        let ptr = VirtualAlloc(
            hint,
            size,
            MEM_COMMIT | MEM_RESERVE,
            PAGE_EXECUTE_RW,
        ) as *mut u8;
        if ptr.is_null() {
            return None;
        }
        Some(Self { base: ptr, size })
    }

    #[inline] pub fn as_ptr(&self) -> *mut u8 { self.base }
    #[inline] pub fn size(&self) -> usize { self.size }

    /// Address as usize — convenient for RIP-rel disp math.
    #[inline] pub fn addr(&self) -> usize { self.base as usize }

    /// Leak the allocation (returns raw pointer, suppresses Drop).
    /// Use when the allocation is the backing for an active trampoline
    /// that must outlive this struct.
    pub fn leak(self) -> *mut u8 {
        let p = self.base;
        core::mem::forget(self);
        p
    }

    /// Write arbitrary bytes into the region at `offset`. Caller ensures
    /// `offset + bytes.len() <= self.size`.
    pub unsafe fn write_bytes(&self, offset: usize, bytes: &[u8]) {
        core::ptr::copy_nonoverlapping(bytes.as_ptr(), self.base.add(offset), bytes.len());
    }
}

impl Drop for AllocatedMemory {
    fn drop(&mut self) {
        unsafe {
            VirtualFree(self.base as *mut core::ffi::c_void, 0, MEM_RELEASE);
        }
    }
}

/// Allocate RWX memory within ±2 GB of `target_va`. Needed so short JMP/CALL
/// rel32 displacements from/to `target_va` can reach this memory without
/// needing `jmp_abs_indirect`.
///
/// Strategy: fixed-stride VirtualQuery walk. On x64 with ASLR, D2R has
/// plenty of free gaps — we start at +/- 64 KB and march outward in 1 MB
/// strides until we find a MEM_FREE region big enough. Bail at ±1 GB (well
/// within 2 GB reach).
///
/// This replaced an exponential-doubling walk that called VirtualQuery up
/// to 38× AND alloc+free on rejects — on slow VMs the round-trip plus the
/// VAD traversal blew past d3d12's Present-callback watchdog. The fixed
/// stride does ≤ 2048 VirtualQuery calls with no speculative allocations.
pub unsafe fn alloc_near(target_va: usize, size: usize) -> Option<AllocatedMemory> {
    const ONE_GB:   isize = 1 * 1024 * 1024 * 1024;
    const STRIDE:   isize = 1 * 1024 * 1024;   // 1 MB — allocation granularity is 64 KB
    const START:    isize = 64 * 1024;

    // Marching outward from target_va. We alternate signs so the closest
    // MEM_FREE slot wins, minimising the resulting rel32 displacement.
    let mut off = START;
    while off < ONE_GB {
        for sign in [-1isize, 1] {
            let probe = (target_va as isize).wrapping_add(sign.wrapping_mul(off));
            if probe < 0x1_0000 { continue; }
            if probe > 0x0000_7FFF_FFFF_FFFF { continue; }

            let mut mbi: MBI = core::mem::zeroed();
            let got = VirtualQuery(
                probe as *const core::ffi::c_void,
                &mut mbi as *mut _ as *mut core::ffi::c_void,
                core::mem::size_of::<MBI>(),
            );
            if got == 0 { continue; }
            if mbi.state != MEM_FREE { continue; }
            if mbi.region_size < size + 0x10000 { continue; }

            // Round up to 64 KB allocation granularity.
            let aligned = ((mbi.base_address as usize) + 0xFFFF) & !0xFFFFusize;
            if aligned == 0 { continue; }
            // Ensure aligned + size fits inside the free region.
            let region_end = (mbi.base_address as usize).wrapping_add(mbi.region_size);
            if aligned + size > region_end { continue; }

            if let Some(mem) = AllocatedMemory::new_at(
                aligned as *const core::ffi::c_void, size,
            ) {
                let delta = (mem.addr() as isize).wrapping_sub(target_va as isize);
                if delta.abs() < 2 * ONE_GB {
                    return Some(mem);
                }
                // Landed out of range somehow — drop and keep marching.
                drop(mem);
            }
        }
        off += STRIDE;
    }
    // Last-resort fallback: let the OS pick anywhere. Caller that cares about
    // rel32 range will need jmp_abs_indirect for distant targets.
    AllocatedMemory::new(size)
}

// Unit tests disabled — rmod is #![no_std] with custom panic handler; std
// test harness conflicts. Future parallel std crate wired via feature gate.
#[cfg(all(test, feature = "std_test_harness"))]
mod tests {
    use super::*;

    #[test]
    fn alloc_and_drop() {
        unsafe {
            let mem = AllocatedMemory::new(4096).expect("alloc");
            assert!(mem.size() == 4096);
            assert!(!mem.as_ptr().is_null());
            // Drop frees
        }
    }

    #[test]
    fn alloc_near_self() {
        unsafe {
            let target = alloc_and_drop as *const () as usize;
            let mem = alloc_near(target, 0x1000).expect("near alloc");
            let delta = (mem.addr() as isize) - (target as isize);
            assert!(delta.abs() < 2 * 1024 * 1024 * 1024);
        }
    }
}
