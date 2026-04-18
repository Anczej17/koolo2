//! ROP gadget harvester — scans D2R's .text for `ret`-ending instruction
//! sequences we can chain to perform reads/writes/function calls without
//! emitting our own memory-access instructions.
//!
//! Why: Arxan detects foreign code patterns in D2R. A ROP chain is literally
//! D2R's own code executing — zero foreign instruction footprint. The only
//! "foreign" thing is the stack state we set up.
//!
//! Scan strategy: walk .text forward, find every `ret` (0xC3) or `ret imm16`
//! (0xC2 xx xx). For each, back-decode 1..=MAX_GADGET_LEN bytes looking for
//! a sequence that starts on an instruction boundary (via asm::decode_insn).
//! Score gadgets by "usefulness" — we care most about:
//!   - pop r64; ret            → load reg from stack
//!   - mov r64, [r64]; ret     → deref
//!   - mov [r64], r64; ret     → store
//!   - rep movsb; ret          → memcpy (kingmaker for ROP reads)
//!   - xchg r64, r64; ret      → reg shuffle
//!
//! Fixed-size pool (no_std, no alloc). 256 slots, LRU-ish eviction once full
//! (scan runs once at init so eviction normally not needed).

use crate::asm::decode_insn;

pub const GADGET_POOL_SIZE: usize = 256;
pub const MAX_GADGET_LEN:   usize = 10; // bytes of gadget prologue before ret

/// Compact categorisation of common useful gadgets. One gadget may match
/// multiple categories (e.g., `pop rax; nop; ret` is `PopReg`).
#[derive(Copy, Clone, Debug, PartialEq, Eq)]
pub enum GadgetKind {
    Unknown,
    PopReg,      // `pop r64; ret[imm16]`
    MovRegMem,   // `mov r64, [r64]; ret`
    MovMemReg,   // `mov [r64], r64; ret`
    RepMovsb,    // `rep movsb; ret`         — memcpy!
    RepMovsq,    // `rep movsq; ret`         — memcpy 8B-stride
    XchgReg,     // `xchg r64, r64; ret`
    Ret,         // bare `ret[imm16]` — stack pivot
}

/// A harvested gadget entry.
#[derive(Copy, Clone, Debug)]
pub struct Gadget {
    pub va:   u64,
    pub len:  u8,   // bytes from va to (and including) the ret's opcode
    pub kind: GadgetKind,
    /// Bit-field of GP regs this gadget writes/pops (bit0=rax ... bit15=r15).
    pub regs_touched: u16,
}

impl Gadget {
    pub const fn empty() -> Self {
        Self { va: 0, len: 0, kind: GadgetKind::Unknown, regs_touched: 0 }
    }
}

/// Fixed-size gadget pool.
pub struct ROPGadgets {
    pub pool:  [Gadget; GADGET_POOL_SIZE],
    pub count: usize,
}

impl ROPGadgets {
    pub const fn empty() -> Self {
        Self {
            pool: [Gadget::empty(); GADGET_POOL_SIZE],
            count: 0,
        }
    }

    /// Scan `[base..base+len)` for `ret`-ending gadgets. Populates the pool
    /// in-place, returns number of gadgets added. Safe to call multiple
    /// times — later scans append up to capacity, then stop.
    pub unsafe fn scan(&mut self, base: *const u8, len: usize) -> usize {
        self.scan_with_sink(base, len, core::ptr::null_mut(), 0, 0, 0)
    }

    /// Scan variant that increments kind-count + pop-reg-mask cells in a
    /// caller-provided SHM view as each gadget is added. Lets the Present
    /// handler get per-kind statistics without a post-scan pool walk
    /// (which Arxan was flipping to -1 with its page-hash sentinel).
    ///
    /// `kind_counts_base` points at u32[8] (Unknown,PopReg,MovRegMem,
    /// MovMemReg,RepMovsb,RepMovsq,XchgReg,Ret). `pop_mask_off` points
    /// at a u32 holding the cumulative `pop r<n>; ret` register mask.
    /// `count_off` points at a u32 holding the live count. Pass null for
    /// `shm` to disable the sink (behaves like `scan`).
    pub unsafe fn scan_with_sink(
        &mut self,
        base: *const u8,
        len: usize,
        shm: *mut u8,
        kind_counts_base: usize,
        pop_mask_off: usize,
        count_off: usize,
    ) -> usize {
        let mut added = 0usize;
        let mut off = 0usize;
        while off < len && self.count < GADGET_POOL_SIZE {
            let b = *base.add(off);
            if b == 0xC3 || b == 0xC2 {
                let ret_len: usize = if b == 0xC3 { 1 } else { 3 };
                let max_back = if off >= MAX_GADGET_LEN { MAX_GADGET_LEN } else { off };
                // Iterate back LONGEST → SHORTEST so we prefer richer gadgets
                // (pop rsi; ret) over degenerate Ret-only gadgets. Previously
                // 0..=max_back made back=0 win every time, classifying every
                // ret as a lone Ret kind — build_memcpy needs PopReg etc.
                let mut back = max_back as isize;
                while back >= 0 {
                    let start = off - back as usize;
                    if let Some((kind, regs)) = self.try_classify_return(base.add(start), back as usize + ret_len) {
                        added += 1;
                        if !shm.is_null() {
                            let idx: usize = match kind {
                                GadgetKind::Unknown    => 0,
                                GadgetKind::PopReg     => 1,
                                GadgetKind::MovRegMem  => 2,
                                GadgetKind::MovMemReg  => 3,
                                GadgetKind::RepMovsb   => 4,
                                GadgetKind::RepMovsq   => 5,
                                GadgetKind::XchgReg    => 6,
                                GadgetKind::Ret        => 7,
                            };
                            if idx < 8 {
                                let p = shm.add(kind_counts_base + idx * 4) as *mut u32;
                                core::ptr::write_unaligned(p, core::ptr::read_unaligned(p).wrapping_add(1));
                            }
                            if kind == GadgetKind::PopReg {
                                let pm = shm.add(pop_mask_off) as *mut u32;
                                core::ptr::write_unaligned(pm, core::ptr::read_unaligned(pm) | (regs as u32));
                            }
                            let cp = shm.add(count_off) as *mut u32;
                            core::ptr::write_unaligned(cp, self.count as u32);
                        }
                        break;
                    }
                    back -= 1;
                }
            }
            off += 1;
        }
        added
    }

    /// Like `try_classify` but returns (kind, regs) on success for sinks.
    unsafe fn try_classify_return(&mut self, start: *const u8, len: usize) -> Option<(GadgetKind, u16)> {
        if self.count >= GADGET_POOL_SIZE { return None; }
        let mut cursor = 0usize;
        let mut insns: [(u8, *const u8); 4] = [(0, core::ptr::null()); 4];
        let mut n_insn = 0usize;
        while cursor < len {
            if n_insn >= insns.len() { return None; }
            let info = match decode_insn(start.add(cursor), len - cursor) {
                Some(i) => i,
                None => return None,
            };
            insns[n_insn] = (info.len, start.add(cursor));
            n_insn += 1;
            cursor += info.len as usize;
        }
        if cursor != len { return None; }
        let (_, last_ptr) = insns[n_insn - 1];
        let last_op = *last_ptr;
        if last_op != 0xC3 && last_op != 0xC2 { return None; }
        let (kind, regs) = classify_prefix(&insns[..n_insn - 1]);
        if kind == GadgetKind::Unknown && n_insn > 1 {
            return None;
        }
        self.pool[self.count] = Gadget {
            va:   start as u64,
            len:  len as u8,
            kind,
            regs_touched: regs,
        };
        self.count += 1;
        Some((kind, regs))
    }

    /// Attempt to decode instructions in [start..start+len) and, if the
    /// sequence is a valid gadget (all insns decode + ends on ret), record
    /// it. Returns true if recorded.
    unsafe fn try_classify(&mut self, start: *const u8, len: usize) -> bool {
        if self.count >= GADGET_POOL_SIZE { return false; }

        // Decode each instruction in the candidate sequence.
        let mut cursor = 0usize;
        let mut insns: [(u8, *const u8); 4] = [(0, core::ptr::null()); 4]; // len + ptr
        let mut n_insn = 0usize;
        while cursor < len {
            if n_insn >= insns.len() { return false; } // more than 4 insns — too complex
            let info = match decode_insn(start.add(cursor), len - cursor) {
                Some(i) => i,
                None => return false,
            };
            insns[n_insn] = (info.len, start.add(cursor));
            n_insn += 1;
            cursor += info.len as usize;
        }
        if cursor != len { return false; } // doesn't align

        // Last instruction must be C3 or C2 xx xx.
        let (_, last_ptr) = insns[n_insn - 1];
        let last_op = *last_ptr;
        if last_op != 0xC3 && last_op != 0xC2 { return false; }

        // Classify based on the instructions preceding the ret.
        let (kind, regs) = classify_prefix(&insns[..n_insn - 1]);

        // Reject Unknown gadgets that have too many instructions — we only
        // want tight gadgets (fast to execute, minimal side effects).
        // Exception: Ret alone (n_insn=1) is always recorded.
        if kind == GadgetKind::Unknown && n_insn > 1 {
            return false;
        }

        self.pool[self.count] = Gadget {
            va:   start as u64,
            len:  len as u8,
            kind,
            regs_touched: regs,
        };
        self.count += 1;
        true
    }

    /// Pick a random gadget of `kind` using `tick` as seed. Returns None if
    /// no gadget matches. Walks pool linearly — O(N) but N ≤ 256.
    pub fn get_random_of_kind(&self, kind: GadgetKind, tick: u64) -> Option<&Gadget> {
        let mut matches: [usize; GADGET_POOL_SIZE] = [0; GADGET_POOL_SIZE];
        let mut n = 0usize;
        for i in 0..self.count {
            if self.pool[i].kind == kind {
                matches[n] = i;
                n += 1;
            }
        }
        if n == 0 { return None; }
        Some(&self.pool[matches[(tick as usize) % n]])
    }

    /// Find any gadget writing to the specified register. Used when the
    /// caller needs e.g., `pop rdi; ret` to set up an arg for memcpy.
    pub fn find_pop_reg(&self, reg_bit: u16, tick: u64) -> Option<&Gadget> {
        let mut matches: [usize; GADGET_POOL_SIZE] = [0; GADGET_POOL_SIZE];
        let mut n = 0usize;
        for i in 0..self.count {
            if self.pool[i].kind == GadgetKind::PopReg
                && (self.pool[i].regs_touched & reg_bit) != 0
            {
                matches[n] = i;
                n += 1;
            }
        }
        if n == 0 { return None; }
        Some(&self.pool[matches[(tick as usize) % n]])
    }
}

/// Classify a sequence of 0..=3 instructions preceding a ret.
unsafe fn classify_prefix(insns: &[(u8, *const u8)]) -> (GadgetKind, u16) {
    if insns.is_empty() { return (GadgetKind::Ret, 0); }
    if insns.len() == 1 {
        let (_, p) = insns[0];
        // pop r64: 58+rd or REX.B 58+rd
        let (op, rex_b) = if *p >= 0x41 && *p <= 0x41 {
            // REX.B prefix, next byte is opcode.
            (*p.add(1), true)
        } else {
            (*p, false)
        };
        if op >= 0x58 && op <= 0x5F {
            let reg_low = op - 0x58;
            let reg_idx = if rex_b { reg_low + 8 } else { reg_low };
            return (GadgetKind::PopReg, 1u16 << reg_idx);
        }
        // rep movsb: F3 A4
        if *p == 0xF3 && *p.add(1) == 0xA4 {
            return (GadgetKind::RepMovsb, 0);
        }
        // rep movsq: F3 48 A5 (REX.W + movsq)
        if *p == 0xF3 && *p.add(1) == 0x48 && *p.add(2) == 0xA5 {
            return (GadgetKind::RepMovsq, 0);
        }
        // mov r64, [r64]: REX.W 8B /r with mod=00
        // Fast path: opcode 8B, modrm with mod=00 and rm != 100 and rm != 101.
        // Skip REX prefix if present.
        let (p2, _rex) = if *p & 0xF0 == 0x40 { (p.add(1), *p) } else { (p, 0) };
        if *p2 == 0x8B {
            let modrm = *p2.add(1);
            let mod_ = modrm >> 6;
            let rm = modrm & 7;
            if mod_ == 0 && rm != 0b100 && rm != 0b101 {
                return (GadgetKind::MovRegMem, 0);
            }
        }
        // mov [r64], r64: 89 /r with mod=00
        if *p2 == 0x89 {
            let modrm = *p2.add(1);
            let mod_ = modrm >> 6;
            let rm = modrm & 7;
            if mod_ == 0 && rm != 0b100 && rm != 0b101 {
                return (GadgetKind::MovMemReg, 0);
            }
        }
        // xchg r64, r64: 48 87 /r (or 48 90+rd for xchg rax, r64)
        if *p2 == 0x87 || (*p2 >= 0x90 && *p2 <= 0x97) {
            return (GadgetKind::XchgReg, 0);
        }
    }
    (GadgetKind::Unknown, 0)
}

#[cfg(all(test, feature = "std_test_harness"))]
mod tests {
    use super::*;

    fn scan_vec(bytes: &[u8]) -> ROPGadgets {
        let mut g = ROPGadgets::empty();
        unsafe { g.scan(bytes.as_ptr(), bytes.len()); }
        g
    }

    #[test]
    fn ret_alone() {
        let g = scan_vec(&[0xC3, 0x90, 0xC3]);
        assert!(g.count >= 2);
        assert_eq!(g.pool[0].kind, GadgetKind::Ret);
    }

    #[test]
    fn pop_rax_ret() {
        // 58 C3 = pop rax; ret
        let g = scan_vec(&[0x58, 0xC3]);
        let has = (0..g.count).any(|i| g.pool[i].kind == GadgetKind::PopReg && g.pool[i].regs_touched == 0b1);
        assert!(has, "expected pop rax; ret to be classified as PopReg rax");
    }

    #[test]
    fn pop_r11_ret() {
        // 41 5B C3 = pop r11; ret
        let g = scan_vec(&[0x41, 0x5B, 0xC3]);
        let has = (0..g.count).any(|i| g.pool[i].kind == GadgetKind::PopReg && (g.pool[i].regs_touched & (1 << 11)) != 0);
        assert!(has, "expected pop r11; ret to mark bit 11");
    }
}
