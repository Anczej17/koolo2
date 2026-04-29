//! In-process Executor — GID's Executor class ported to Rust rmod.
//!
//! Owns two RWX memory regions (code + data) allocated via alloc_near() so
//! RIP-relative jumps fit without abs-indirect. Provides primitives for:
//!
//!   - `inject_code(bytes) -> code_va`   — append assembled bytes to code region
//!   - `reset()`                          — rewind offsets (reuse buffers)
//!   - `data_base()` / `data_write`       — scratch data buffer for ROP args
//!   - `call_raw(fn_ptr, arg1, arg2, …)`  — direct in-process call (SystemV x64 ABI)
//!
//! Later (GID-4/GID-5) we add ROP-specific builders on top:
//!   - build_rop_chain(gadgets, args)
//!   - call_injected_rop(fn_addr, args)   — schedule D2R's own gadgets to execute
//!
//! Unlike GID's cross-process Executor, ours is IN-PROCESS (rmod is injected
//! INTO D2R). So `call_raw` is a plain function-pointer call; no APC/ROP plumbing
//! needed for trivial "call D2R function with args" ops. ROP is still valuable
//! for writes we want to hide from Arxan telemetry (writes from an "external"
//! context vs writes that look like D2R's own code).

use crate::alloc_mgr::{alloc_near, AllocatedMemory};
use crate::asm::{Emitter, Reg64};

/// Default sizes. 64 KB is plenty for hundreds of ROP chains + scratch.
const CODE_BUF_SIZE: usize = 64 * 1024;
const DATA_BUF_SIZE: usize = 64 * 1024;

/// Execution engine. One per (optional) target fn — for D2R, we typically
/// need just one Executor because everything is within rmod's ±2 GB range
/// of D2R itself.
pub struct Executor {
    code: AllocatedMemory,
    data: AllocatedMemory,
    code_cursor: usize,
    data_cursor: usize,
}

impl Executor {
    /// Allocate code + data buffers. Was previously alloc_near(target_va) to
    /// keep RIP-rel jumps into D2R within disp32 range, but on slow VMs the
    /// VirtualQuery march blew past d3d12's Present-callback watchdog. The
    /// trampoline path uses jmp_abs_via_reg anyway, so abs-placement is fine.
    /// `target_va` is still accepted as a hint for future callers that want
    /// to re-enable near-alloc.
    pub unsafe fn new(_target_va: usize) -> Option<Self> {
        let code = AllocatedMemory::new(CODE_BUF_SIZE)?;
        let data = AllocatedMemory::new(DATA_BUF_SIZE)?;
        Some(Self {
            code,
            data,
            code_cursor: 0,
            data_cursor: 0,
        })
    }

    /// Fallback allocator when alloc_near fails (e.g., no free gap within
    /// ±2 GB of target). Falls back to VirtualAlloc anywhere; callers
    /// that emit RIP-relative code will need to add jmp_abs_indirect
    /// trampolines for out-of-range targets.
    pub unsafe fn new_anywhere() -> Option<Self> {
        let code = AllocatedMemory::new(CODE_BUF_SIZE)?;
        let data = AllocatedMemory::new(DATA_BUF_SIZE)?;
        Some(Self {
            code,
            data,
            code_cursor: 0,
            data_cursor: 0,
        })
    }

    #[inline]
    pub fn code_base(&self) -> usize {
        self.code.addr()
    }
    #[inline]
    pub fn data_base(&self) -> usize {
        self.data.addr()
    }
    #[inline]
    pub fn code_rip(&self) -> usize {
        self.code.addr() + self.code_cursor
    }
    #[inline]
    pub fn data_rip(&self) -> usize {
        self.data.addr() + self.data_cursor
    }

    /// Write bytes to the code buffer. Returns the VA of the first byte
    /// (useful as the call target). Panics if over-run — caller must not
    /// emit > CODE_BUF_SIZE bytes; reset() rewinds.
    pub unsafe fn inject_code(&mut self, bytes: &[u8]) -> Option<usize> {
        if self.code_cursor + bytes.len() > CODE_BUF_SIZE {
            return None;
        }
        let start = self.code_rip();
        self.code.write_bytes(self.code_cursor, bytes);
        self.code_cursor += bytes.len();
        Some(start)
    }

    /// Write bytes to the data buffer. Returns the VA of the first byte.
    pub unsafe fn inject_data(&mut self, bytes: &[u8]) -> Option<usize> {
        if self.data_cursor + bytes.len() > DATA_BUF_SIZE {
            return None;
        }
        let start = self.data_rip();
        self.data.write_bytes(self.data_cursor, bytes);
        self.data_cursor += bytes.len();
        Some(start)
    }

    /// Write a u64 to the data buffer (8-byte aligned). Returns VA.
    pub unsafe fn inject_data_u64(&mut self, v: u64) -> Option<usize> {
        // Ensure 8-byte alignment before write.
        let pad = (8 - (self.data_cursor & 7)) & 7;
        if self.data_cursor + pad + 8 > DATA_BUF_SIZE {
            return None;
        }
        self.data_cursor += pad;
        let va = self.data_rip();
        let bytes = v.to_le_bytes();
        self.data.write_bytes(self.data_cursor, &bytes);
        self.data_cursor += 8;
        Some(va)
    }

    /// Rewind both buffers to empty. Existing code at old offsets is
    /// invalidated — don't call after an `inject_code` return is still
    /// being used as a call target.
    pub unsafe fn reset(&mut self) {
        self.code_cursor = 0;
        self.data_cursor = 0;
    }

    /// Get a polymorphic-ish register for an ASM template. Uses rdtsc low
    /// bits modulo a small pool. Avoids rsp/rbp (special encoding).
    #[inline]
    pub fn random_caller_saved(&self, tick: u64) -> Reg64 {
        let pool = [Reg64::Rax, Reg64::Rcx, Reg64::Rdx, Reg64::R10, Reg64::R11];
        pool[(tick as usize) % pool.len()]
    }

    /// Write a trivial stub at the current code_rip:
    ///     mov rax, target_fn
    ///     jmp rax
    /// Returns the VA of the stub. Useful as a polymorphic trampoline.
    pub unsafe fn emit_abs_jmp_trampoline(&mut self, target_fn: usize, tick: u64) -> Option<usize> {
        let reg = self.random_caller_saved(tick);
        let start = self.code_rip();
        // Up to 13 bytes for mov + jmp reg.
        let mut buf = [0u8; 16];
        let mut e = Emitter::new(buf.as_mut_ptr(), buf.len());
        e.jmp_abs_via_reg(reg, target_fn);
        let emitted_len = e.len();
        self.inject_code(&buf[..emitted_len])?;
        Some(start)
    }
}

// Tests gated behind feature="std_test_harness" per rmod no_std constraint.
#[cfg(all(test, feature = "std_test_harness"))]
mod tests {
    use super::*;

    #[test]
    fn executor_alloc() {
        unsafe {
            let here = executor_alloc as *const () as usize;
            let e = Executor::new(here).expect("alloc near self");
            let delta = (e.code_base() as isize) - (here as isize);
            assert!(delta.abs() < 2 * 1024 * 1024 * 1024);
        }
    }

    #[test]
    fn inject_bytes() {
        unsafe {
            let here = inject_bytes as *const () as usize;
            let mut e = Executor::new(here).expect("alloc");
            let va = e.inject_code(&[0xC3]).expect("inject ret");
            assert_eq!(va, e.code_base());
            // Subsequent inject advances cursor.
            let va2 = e.inject_code(&[0x90, 0x90]).expect("inject nop nop");
            assert_eq!(va2, va + 1);
        }
    }
}
