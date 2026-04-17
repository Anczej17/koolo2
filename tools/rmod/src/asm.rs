//! Minimal x86-64 encoder/decoder for rmod.
//!
//! Handcrafted to avoid pulling in iced-x86 (800 KB+ crate, needs alloc setup).
//! Covers the instruction patterns we need for GID-style Present hook
//! AssembleInsteadOfBytes + ROP chain builder:
//!
//! Encoder (emit bytes to a buffer):
//!   - JMP rel32
//!   - JMP [rip+rel32]
//!   - CALL rel32
//!   - MOV reg64, imm64  (REX.W + B8+rd io)
//!   - PUSH reg64 / POP reg64
//!   - RET / RET imm16
//!
//! Decoder (identify instruction length — "instruction boundary finder"):
//!   - Common prologue prefixes (REX, lock)
//!   - Opcode + ModRM + SIB + displacement + immediate length calc
//!   - Enough to walk past Present's first ~20 bytes safely
//!
//! Reference: Intel SDM Vol 2, encoding tables. No external crate dependency.

use core::ptr;

/// x86-64 general-purpose register encoding (3 bits: RAX=0, RCX=1, ..., R15=15).
#[derive(Copy, Clone, Debug, PartialEq, Eq)]
#[repr(u8)]
pub enum Reg64 {
    Rax = 0, Rcx = 1, Rdx = 2, Rbx = 3,
    Rsp = 4, Rbp = 5, Rsi = 6, Rdi = 7,
    R8  = 8, R9  = 9, R10 = 10, R11 = 11,
    R12 = 12, R13 = 13, R14 = 14, R15 = 15,
}

impl Reg64 {
    #[inline] pub fn idx(self) -> u8 { self as u8 }
    #[inline] pub fn rex_b(self) -> bool { self.idx() >= 8 }
    #[inline] pub fn low3(self) -> u8 { self.idx() & 0x7 }
}

/// Writes machine code into a fixed-size buffer with a bump pointer. The
/// trampoline page is 4 KB — more than enough for the hook prologue + ROP
/// stubs we emit.
pub struct Emitter {
    buf: *mut u8,
    pos: usize,
    cap: usize,
}

impl Emitter {
    /// Caller owns `buf`; must be valid for `cap` bytes.
    #[inline]
    pub unsafe fn new(buf: *mut u8, cap: usize) -> Self {
        Self { buf, pos: 0, cap }
    }

    #[inline]
    pub fn len(&self) -> usize { self.pos }

    #[inline]
    pub fn rip(&self) -> *const u8 { unsafe { self.buf.add(self.pos) } }

    #[inline]
    unsafe fn byte(&mut self, b: u8) {
        if self.pos < self.cap {
            ptr::write(self.buf.add(self.pos), b);
        }
        self.pos += 1;
    }

    #[inline]
    unsafe fn bytes(&mut self, bs: &[u8]) {
        for &b in bs { self.byte(b); }
    }

    #[inline]
    unsafe fn u32_le(&mut self, v: u32) {
        self.byte((v & 0xFF) as u8);
        self.byte(((v >> 8) & 0xFF) as u8);
        self.byte(((v >> 16) & 0xFF) as u8);
        self.byte(((v >> 24) & 0xFF) as u8);
    }

    #[inline]
    unsafe fn u64_le(&mut self, v: u64) {
        self.u32_le((v & 0xFFFF_FFFF) as u32);
        self.u32_le((v >> 32) as u32);
    }

    /// `JMP rel32` (5 bytes): `E9 xx xx xx xx` where disp = target - next_rip.
    /// Caller supplies target as absolute VA; we compute the displacement
    /// based on the current emission position (assuming `buf` base VA is
    /// known — caller must pass `base_ip` = VA of `buf[0]`).
    #[inline]
    pub unsafe fn jmp_rel32(&mut self, base_ip: usize, target: usize) {
        self.byte(0xE9);
        let next_ip = base_ip.wrapping_add(self.pos + 4);
        let disp = target.wrapping_sub(next_ip) as i64 as i32 as u32;
        self.u32_le(disp);
    }

    /// `CALL rel32` (5 bytes): `E8 xx xx xx xx`.
    #[inline]
    pub unsafe fn call_rel32(&mut self, base_ip: usize, target: usize) {
        self.byte(0xE8);
        let next_ip = base_ip.wrapping_add(self.pos + 4);
        let disp = target.wrapping_sub(next_ip) as i64 as i32 as u32;
        self.u32_le(disp);
    }

    /// `JMP [rip + 0]` + absolute 8-byte target (14 bytes total). Used when
    /// target is > ±2 GB from RIP. Format: `FF 25 00 00 00 00 <64-bit target>`.
    #[inline]
    pub unsafe fn jmp_abs_indirect(&mut self, target: usize) {
        self.bytes(&[0xFF, 0x25, 0x00, 0x00, 0x00, 0x00]);
        self.u64_le(target as u64);
    }

    /// `MOV reg64, imm64` (10 bytes): `REX.W B8+rd <64-bit imm>`.
    #[inline]
    pub unsafe fn mov_reg64_imm64(&mut self, reg: Reg64, imm: u64) {
        let rex = 0x48 | if reg.rex_b() { 0x01 } else { 0x00 }; // REX.W + REX.B
        self.byte(rex);
        self.byte(0xB8 | reg.low3()); // MOV r64, imm64
        self.u64_le(imm);
    }

    /// `PUSH reg64` (1-2 bytes): `50+rd` or `REX.B 50+rd`.
    #[inline]
    pub unsafe fn push_reg64(&mut self, reg: Reg64) {
        if reg.rex_b() { self.byte(0x41); }
        self.byte(0x50 | reg.low3());
    }

    /// `POP reg64` (1-2 bytes): `58+rd` or `REX.B 58+rd`.
    #[inline]
    pub unsafe fn pop_reg64(&mut self, reg: Reg64) {
        if reg.rex_b() { self.byte(0x41); }
        self.byte(0x58 | reg.low3());
    }

    /// `RET` (1 byte).
    #[inline]
    pub unsafe fn ret(&mut self) { self.byte(0xC3); }

    /// `NOP` (1 byte) — padding.
    #[inline]
    pub unsafe fn nop(&mut self) { self.byte(0x90); }
}

// ---------------------------------------------------------------------------
// Decoder — instruction-length finder for prologue walking
// ---------------------------------------------------------------------------

/// Result of inspecting a single instruction.
#[derive(Copy, Clone, Debug)]
pub struct InsnInfo {
    /// Total instruction length in bytes.
    pub len: u8,
    /// True if this instruction has a RIP-relative displacement. When
    /// relocating such an instruction from address A to address B, the
    /// displacement must be adjusted by (A - B).
    pub has_rip_rel: bool,
    /// Byte offset of the 32-bit RIP-relative displacement within the
    /// instruction (only meaningful if has_rip_rel = true). Caller patches
    /// `insn[rip_rel_off..rip_rel_off+4]` with the new disp.
    pub rip_rel_off: u8,
}

/// Decode the instruction starting at `ip`, return its length and whether
/// it carries a RIP-relative disp (and where). Handles the subset of x86-64
/// instructions expected in function prologues + Arxan-VM stubs:
///   - REX prefix(es)
///   - 0F two-byte opcodes
///   - ModRM + SIB + disp8/disp32
///   - Immediate operands
///
/// Returns None if the opcode is outside the subset we can confidently
/// decode. Caller should bail the hook in that case.
pub unsafe fn decode_insn(ip: *const u8, max_bytes: usize) -> Option<InsnInfo> {
    let mut p: usize = 0;
    let mut rex: u8 = 0;

    // Step 1: skip legacy / REX prefixes.
    loop {
        if p >= max_bytes { return None; }
        let b = *ip.add(p);
        match b {
            // REX prefix family (0x40-0x4F).
            0x40..=0x4F => { rex = b; p += 1; }
            // Operand/address size override, segment overrides.
            0x26 | 0x2E | 0x36 | 0x3E | 0x64 | 0x65 | 0x66 | 0x67 | 0xF0 | 0xF2 | 0xF3 => { p += 1; }
            _ => break,
        }
    }

    let opcode_start = p;
    if p >= max_bytes { return None; }
    let op1 = *ip.add(p);
    p += 1;

    let _ = rex; // currently unused but available for caller

    // Helper: decode ModR/M + SIB + displacement. Returns total bytes consumed
    // past opcode and whether disp is 32-bit RIP-relative.
    unsafe fn decode_modrm(
        ip: *const u8,
        p_start: usize,
        max_bytes: usize,
    ) -> Option<(usize, bool, usize)> {
        if p_start >= max_bytes { return None; }
        let modrm = *ip.add(p_start);
        let mod_ = (modrm >> 6) & 0x3;
        let rm = modrm & 0x7;
        let mut p = p_start + 1;

        // RIP-relative: Mod=00, R/M=101 (no SIB).
        if mod_ == 0b00 && rm == 0b101 {
            let disp_off = p;
            if p + 4 > max_bytes { return None; }
            p += 4;
            return Some((p - p_start, true, disp_off));
        }

        // SIB follows when R/M=100 and Mod != 11.
        if mod_ != 0b11 && rm == 0b100 {
            if p >= max_bytes { return None; }
            p += 1; // skip SIB
        }

        // Displacement sizing.
        match mod_ {
            0b00 => {
                // No disp, EXCEPT when Mod=00 and the SIB base is 101.
                // We conservatively skip the special-case tracking here —
                // if base=101 with no disp, disp32 follows. Re-check SIB.
                if rm == 0b100 {
                    // Re-peek SIB byte (we advanced past it; read from -1).
                    let sib = *ip.add(p - 1);
                    let base = sib & 0x7;
                    if base == 0b101 {
                        if p + 4 > max_bytes { return None; }
                        p += 4;
                    }
                }
            }
            0b01 => {
                if p >= max_bytes { return None; }
                p += 1;
            }
            0b10 => {
                if p + 4 > max_bytes { return None; }
                p += 4;
            }
            0b11 => { /* register operand — no disp */ }
            _ => unreachable!(),
        }

        Ok_((p - p_start, false, 0))
    }

    // Classify opcode — return immediate size beyond ModR/M (if any) plus
    // whether the opcode uses ModR/M at all.
    let (uses_modrm, imm_size, is_two_byte): (bool, u8, bool) = match op1 {
        // One-byte opcodes WITHOUT ModR/M.
        0x50..=0x5F => (false, 0, false),        // push/pop r64
        0x90 => (false, 0, false),                // nop
        0xC3 => (false, 0, false),                // ret
        0xC2 => (false, 2, false),                // ret imm16
        0xE8 => (false, 4, false),                // call rel32
        0xE9 => (false, 4, false),                // jmp rel32
        0xEB => (false, 1, false),                // jmp rel8
        0x70..=0x7F => (false, 1, false),         // jcc rel8
        0xB8..=0xBF => (false, 8, false),         // mov r64, imm64 (with REX.W)
        0xCC => (false, 0, false),                // int3
        0xF4 => (false, 0, false),                // hlt

        // One-byte opcodes WITH ModR/M and 0 immediate.
        0x01 | 0x03 | 0x09 | 0x0B | 0x21 | 0x23 | 0x29 | 0x2B
        | 0x31 | 0x33 | 0x39 | 0x3B                // common arith r/m, r
        | 0x85 | 0x87 | 0x89 | 0x8B | 0x8D         // test, xchg, mov, lea
        | 0xFF                                      // group 5 (inc/dec/call/jmp/push r/m)
            => (true, 0, false),

        // One-byte opcodes WITH ModR/M + imm8.
        0x83 => (true, 1, false),                  // group 1 (add/or/adc/... r/m64, imm8)
        0xC6 => (true, 1, false),                  // mov r/m8, imm8

        // One-byte opcodes WITH ModR/M + imm32.
        0x81 => (true, 4, false),                  // group 1 (imm32)
        0xC7 => (true, 4, false),                  // mov r/m, imm32

        // Two-byte opcode escape.
        0x0F => (false, 0, true),

        _ => return None, // outside our subset — caller must bail
    };

    if is_two_byte {
        if p >= max_bytes { return None; }
        let op2 = *ip.add(p);
        p += 1;
        // Minimal two-byte opcode subset.
        let (uses_mrm2, imm2): (bool, u8) = match op2 {
            // Jcc rel32 family (0F 80 .. 0F 8F).
            0x80..=0x8F => (false, 4),
            // mov with sign/zero extension, misc r/m.
            0xB6 | 0xB7 | 0xBE | 0xBF   // movzx/movsx
            | 0x1F                       // nop r/m (multi-byte nop)
                => (true, 0),
            _ => return None,
        };
        if uses_mrm2 {
            let (consumed, has_rip, disp_off) = decode_modrm(ip, p, max_bytes)?;
            p += consumed;
            let rip_rel_off = (opcode_start + 2 + disp_off) as u8; // +2 bytes for opcode 0F xx
            p += imm2 as usize;
            if p > max_bytes { return None; }
            return Some(InsnInfo { len: p as u8, has_rip_rel: has_rip, rip_rel_off });
        }
        p += imm2 as usize;
        if p > max_bytes { return None; }
        return Some(InsnInfo { len: p as u8, has_rip_rel: false, rip_rel_off: 0 });
    }

    if uses_modrm {
        let (consumed, has_rip, disp_off) = decode_modrm(ip, p, max_bytes)?;
        let rip_rel_off = (p + disp_off) as u8;
        p += consumed;
        p += imm_size as usize;
        if p > max_bytes { return None; }
        return Some(InsnInfo { len: p as u8, has_rip_rel: has_rip, rip_rel_off });
    }

    p += imm_size as usize;
    if p > max_bytes { return None; }
    Some(InsnInfo { len: p as u8, has_rip_rel: false, rip_rel_off: 0 })
}

/// Walk forward from `ip` counting instructions until total length ≥ `min_len`,
/// stopping on an instruction boundary. Caller uses this to determine how
/// many bytes of prologue to move into the trampoline (GID's "CopyAmount").
pub unsafe fn walk_instruction_boundary(ip: *const u8, min_len: usize, max_bytes: usize) -> Option<usize> {
    let mut cursor = 0usize;
    while cursor < min_len {
        if cursor >= max_bytes { return None; }
        let info = decode_insn(ip.add(cursor), max_bytes - cursor)?;
        cursor += info.len as usize;
    }
    Some(cursor)
}

// Local alias for Option::Some — keeps the decoder easy to read.
#[allow(non_snake_case)]
#[inline]
fn Ok_<T>(v: T) -> Option<T> { Some(v) }

// Unit tests disabled: rmod is #![no_std] with its own panic handler; the
// std-dependent test harness conflicts (E0152 duplicate panic_impl lang item).
// Tests live in a parallel std crate if we want to run them — not wired yet.
// See ROP_PORT_AUDIT.md Phase GID-1.4 for pending action.
#[cfg(all(test, feature = "std_test_harness"))]
mod tests {
    use super::*;

    // Note: rmod is compiled as no_std cdylib. These unit tests run via
    // `cargo test --lib` with std enabled; they exercise just the pure-logic
    // portions (Emitter byte output, decoder instruction lengths).

    fn buf16() -> [u8; 32] { [0u8; 32] }

    #[test]
    fn emit_ret() {
        let mut b = buf16();
        unsafe {
            let mut e = Emitter::new(b.as_mut_ptr(), b.len());
            e.ret();
            assert_eq!(e.len(), 1);
        }
        assert_eq!(b[0], 0xC3);
    }

    #[test]
    fn emit_jmp_rel32_target_above() {
        let mut b = buf16();
        unsafe {
            let base_ip = 0x10_0000usize;
            let target = 0x10_0100usize;
            let mut e = Emitter::new(b.as_mut_ptr(), b.len());
            e.jmp_rel32(base_ip, target);
            assert_eq!(e.len(), 5);
        }
        assert_eq!(b[0], 0xE9);
        // disp = target - (base + 5) = 0x100 - 5 = 0xFB
        let disp = u32::from_le_bytes([b[1], b[2], b[3], b[4]]);
        assert_eq!(disp, 0xFB);
    }

    #[test]
    fn emit_jmp_abs_indirect() {
        let mut b = buf16();
        unsafe {
            let mut e = Emitter::new(b.as_mut_ptr(), b.len());
            e.jmp_abs_indirect(0xDEAD_BEEF_CAFE_BABE);
            assert_eq!(e.len(), 14);
        }
        assert_eq!(&b[..6], &[0xFF, 0x25, 0, 0, 0, 0]);
        assert_eq!(&b[6..14], &[0xBE, 0xBA, 0xFE, 0xCA, 0xEF, 0xBE, 0xAD, 0xDE]);
    }

    #[test]
    fn emit_mov_rax_imm64() {
        let mut b = buf16();
        unsafe {
            let mut e = Emitter::new(b.as_mut_ptr(), b.len());
            e.mov_reg64_imm64(Reg64::Rax, 0x1122_3344_5566_7788);
            assert_eq!(e.len(), 10);
        }
        assert_eq!(b[0], 0x48); // REX.W
        assert_eq!(b[1], 0xB8); // MOV rax, imm64
    }

    #[test]
    fn emit_mov_r11_imm64() {
        let mut b = buf16();
        unsafe {
            let mut e = Emitter::new(b.as_mut_ptr(), b.len());
            e.mov_reg64_imm64(Reg64::R11, 0x42);
            assert_eq!(e.len(), 10);
        }
        assert_eq!(b[0], 0x49); // REX.W + REX.B
        assert_eq!(b[1], 0xBB); // MOV r11, imm64 (B8 + 3)
    }

    #[test]
    fn emit_push_pop_r12() {
        let mut b = buf16();
        unsafe {
            let mut e = Emitter::new(b.as_mut_ptr(), b.len());
            e.push_reg64(Reg64::R12);
            e.pop_reg64(Reg64::R12);
        }
        assert_eq!(&b[..4], &[0x41, 0x54, 0x41, 0x5C]);
    }

    #[test]
    fn decode_push_rbp() {
        // 55 = push rbp
        let ip = [0x55u8];
        let info = unsafe { decode_insn(ip.as_ptr(), ip.len()) }.unwrap();
        assert_eq!(info.len, 1);
        assert!(!info.has_rip_rel);
    }

    #[test]
    fn decode_mov_rbp_rsp() {
        // 48 89 E5 = mov rbp, rsp (REX.W + mov r/m, r; modrm=C5 → mod=11 rm=5 reg=0)
        let ip = [0x48u8, 0x89, 0xE5];
        let info = unsafe { decode_insn(ip.as_ptr(), ip.len()) }.unwrap();
        assert_eq!(info.len, 3);
        assert!(!info.has_rip_rel);
    }

    #[test]
    fn decode_sub_rsp_imm8() {
        // 48 83 EC 28 = sub rsp, 0x28
        let ip = [0x48u8, 0x83, 0xEC, 0x28];
        let info = unsafe { decode_insn(ip.as_ptr(), ip.len()) }.unwrap();
        assert_eq!(info.len, 4);
        assert!(!info.has_rip_rel);
    }

    #[test]
    fn decode_mov_rip_rel() {
        // 48 8B 05 10 20 30 40 = mov rax, [rip+0x40302010]
        // modrm = 05 (mod=00, reg=0=rax, rm=5 → RIP-rel)
        let ip = [0x48u8, 0x8B, 0x05, 0x10, 0x20, 0x30, 0x40];
        let info = unsafe { decode_insn(ip.as_ptr(), ip.len()) }.unwrap();
        assert_eq!(info.len, 7);
        assert!(info.has_rip_rel);
        // rip_rel_off counts from the instruction start; REX(1)+OP(1)+MODRM(1)=3
        assert_eq!(info.rip_rel_off, 3);
    }

    #[test]
    fn decode_call_rel32() {
        // E8 xx xx xx xx
        let ip = [0xE8u8, 0, 0, 0, 0];
        let info = unsafe { decode_insn(ip.as_ptr(), ip.len()) }.unwrap();
        assert_eq!(info.len, 5);
    }

    #[test]
    fn walk_14bytes_of_prologue() {
        // 48 89 5C 24 08  mov [rsp+8], rbx       (5 B)
        // 48 89 74 24 10  mov [rsp+0x10], rsi    (5 B)
        // 57              push rdi               (1 B)
        // 48 83 EC 20     sub rsp, 0x20          (4 B)
        // Total 15 — walk_instruction_boundary(14) lands on 15 (next boundary past 14).
        let bytes: [u8; 15] = [
            0x48, 0x89, 0x5C, 0x24, 0x08,
            0x48, 0x89, 0x74, 0x24, 0x10,
            0x57,
            0x48, 0x83, 0xEC, 0x20,
        ];
        let n = unsafe { walk_instruction_boundary(bytes.as_ptr(), 14, bytes.len()) }.unwrap();
        assert_eq!(n, 15);
    }
}
