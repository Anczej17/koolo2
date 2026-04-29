//! ROP chain builder — uses ROPGadgets + Executor to construct stack-based
//! chains that execute D2R's own code for memcpy / call-with-args / write.
//!
//! This is Phase GID-5 foundation. The primary chain we build is `memcpy`
//! (for Process.RopRead). Subsequent chains (CallInjected with N args,
//! write-to-addr) are trivial variations on the same pattern.
//!
//! Chain stack layout (x64 SystemV ABI — return-address chain):
//!
//!   rsp_start →  [pop_rsi_gadget_va]   → POP RSI
//!                [src_va]              ← RSI
//!                [pop_rdi_gadget_va]   → POP RDI
//!                [dst_va]              ← RDI
//!                [pop_rcx_gadget_va]   → POP RCX
//!                [len_value]           ← RCX
//!                [rep_movsb_gadget_va] → executes `rep movsb; ret`
//!                [epilogue_va]         → control returns here
//!
//! Triggering: a small in-process thunk (emit_rop_trigger) saves the current
//! RSP, sets RSP to chain_base, and RETs. Gadgets chain through via their
//! trailing `ret`. Final ret lands on `epilogue_va` which restores RSP +
//! returns to the caller.
//!
//! Unlike true cross-process ROP (where you need CreateRemoteThread / APC
//! to start the chain), our ROP runs IN-PROCESS because rmod is already
//! injected into D2R. We just swap stacks and ret.

use crate::alloc_mgr::AllocatedMemory;
use crate::asm::{Emitter, Reg64};
use crate::rop_gadgets::{Gadget, GadgetKind, ROPGadgets};

/// Register-index bits (matches Reg64::idx()) used by ROPGadgets.regs_touched.
pub const BIT_RAX: u16 = 1 << 0;
pub const BIT_RCX: u16 = 1 << 1;
pub const BIT_RDX: u16 = 1 << 2;
pub const BIT_RBX: u16 = 1 << 3;
pub const BIT_RSI: u16 = 1 << 6;
pub const BIT_RDI: u16 = 1 << 7;

/// A fully built chain ready for execution. `stack_base` points into a
/// data buffer (usually Executor.data), arranged with gadget addrs + values
/// per the header comment. `trigger_va` is a code pointer: call it like a
/// normal function (no args), it executes the chain and returns.
pub struct BuiltChain {
    pub stack_base: *mut u64,
    pub stack_words: usize,
    pub trigger_va: usize,
}

/// Chain builder that owns a scratch stack + trigger thunk, each in
/// pre-allocated memory. Build phase: assemble chain. Execute phase:
/// call `trigger_va` as a zero-arg function pointer.
pub struct RopChainBuilder<'a> {
    gadgets: &'a ROPGadgets,
    stack_mem: &'a AllocatedMemory,   // scratch stack: 4 KB+ RWX
    trigger_mem: &'a AllocatedMemory, // code buffer for the trigger thunk
    tick: u64,                        // rdtsc — drives polymorphic picks
}

impl<'a> RopChainBuilder<'a> {
    pub fn new(
        gadgets: &'a ROPGadgets,
        stack_mem: &'a AllocatedMemory,
        trigger_mem: &'a AllocatedMemory,
        tick: u64,
    ) -> Self {
        Self {
            gadgets,
            stack_mem,
            trigger_mem,
            tick,
        }
    }

    /// Build a `memcpy(dst, src, len)` chain using D2R's own `rep movsb` gadget.
    /// Returns None if the gadget pool lacks required pieces.
    pub unsafe fn build_memcpy(&self, src: usize, dst: usize, len: usize) -> Option<BuiltChain> {
        // Required gadgets: pop rsi; ret, pop rdi; ret, pop rcx; ret, rep movsb; ret.
        let g_pop_rsi = self.gadgets.find_pop_reg(BIT_RSI, self.tick)?;
        let g_pop_rdi = self.gadgets.find_pop_reg(BIT_RDI, self.tick ^ 0xA5A5)?;
        let g_pop_rcx = self.gadgets.find_pop_reg(BIT_RCX, self.tick ^ 0xC3C3)?;
        let g_movsb = self
            .gadgets
            .get_random_of_kind(GadgetKind::RepMovsb, self.tick)?;

        // Build the stack in the scratch buffer. Layout grows DOWN (higher
        // addresses = bottom of stack). We write from base upwards so the
        // first qword we write is popped first.
        let stack_base = self.stack_mem.as_ptr() as *mut u64;
        let mut i = 0usize;
        *stack_base.add(i) = g_pop_rsi.va;
        i += 1;
        *stack_base.add(i) = src as u64;
        i += 1;
        *stack_base.add(i) = g_pop_rdi.va;
        i += 1;
        *stack_base.add(i) = dst as u64;
        i += 1;
        *stack_base.add(i) = g_pop_rcx.va;
        i += 1;
        *stack_base.add(i) = len as u64;
        i += 1;
        *stack_base.add(i) = g_movsb.va;
        i += 1;
        // The epilogue is placed at the END of the trigger thunk — gadget
        // chain eventually `ret`s to it and we return normally.
        let trigger_va = self.trigger_mem.addr();
        // Compute epilogue VA lazily — caller plugs after emit.
        // We leave a placeholder here; emit_trigger writes it back.
        *stack_base.add(i) = 0;
        i += 1; // patched after emit_trigger

        // Emit the trigger thunk into trigger_mem. It:
        //   1. pushes all callee-saved GPR + flags
        //   2. saves RSP to a local slot
        //   3. sets RSP = stack_base
        //   4. RETs (pops first gadget)
        //   ... chain executes ...
        //   6. epilogue: restores RSP + pops callee-saved + ret to caller
        let epilogue_va = self.emit_trigger(stack_base)?;
        *stack_base.add(i - 1) = epilogue_va as u64;

        Some(BuiltChain {
            stack_base,
            stack_words: i,
            trigger_va,
        })
    }

    /// Emit the trigger + epilogue thunk. Returns the VA of the epilogue
    /// label (which becomes the final `ret` target in the chain).
    ///
    /// Layout of emitted code:
    ///   trigger:
    ///     pushfq
    ///     push rbx; push rbp; push r12; push r13; push r14; push r15
    ///     push rsi; push rdi
    ///     mov  [rip+save_rsp_slot], rsp
    ///     mov  rsp, stack_base
    ///     ret                            ; pop first gadget
    ///   save_rsp_slot: dq 0
    ///   epilogue:
    ///     mov  rsp, [rip+save_rsp_slot]
    ///     pop  rdi; pop rsi
    ///     pop  r15; pop r14; pop r13; pop r12; pop rbp; pop rbx
    ///     popfq
    ///     ret
    unsafe fn emit_trigger(&self, stack_base: *mut u64) -> Option<usize> {
        let base_ip = self.trigger_mem.addr();
        let mut e = Emitter::new(self.trigger_mem.as_ptr(), self.trigger_mem.size());

        // trigger:
        // pushfq (9C)
        e.bytes(&[0x9C]);
        // push callee-saved regs (in consistent order so epilogue mirrors)
        e.push_reg64(Reg64::Rbx);
        e.push_reg64(Reg64::Rbp);
        e.push_reg64(Reg64::R12);
        e.push_reg64(Reg64::R13);
        e.push_reg64(Reg64::R14);
        e.push_reg64(Reg64::R15);
        e.push_reg64(Reg64::Rsi);
        e.push_reg64(Reg64::Rdi);

        // Reserve inline data slot for saved RSP (8 bytes, filled at runtime).
        // Skip 8 bytes (leave NOPs so disassembly still walks). We emit the
        // `mov [rip+disp], rsp` first, then jump over the data.
        // For simplicity, inline the save slot immediately after a jmp
        // over it: jmp short 8; <8 bytes data>; …
        //
        // Actually in x86-64, you can't easily `mov [rip+disp], reg` with
        // runtime disp resolution in a single instruction — the disp is a
        // 32-bit value encoded into the instruction. We resolve statically:
        // compute save_rsp_slot_offset after emitting the save instruction.

        // Compute current position + 7 bytes for the upcoming MOV [rip+disp], rsp
        // plus the jmp+data+epilogue ahead. Simpler: use a known offset.
        // MOV [rip+disp32], rsp encoding: 48 89 25 <disp32>
        // Next rip after this 7-byte insn = e.len()+7.
        // save_slot is AFTER a 2-byte jmp short (to skip over save_slot itself).
        //
        // Layout plan from this point:
        //   [7 bytes] MOV [rip+disp], rsp   disp = +2 (to point past jmp short)
        //   [2 bytes] jmp short +8          skip the 8-byte slot
        //   [8 bytes] save_rsp_slot (data)
        //   ... continue trigger / epilogue ...

        // Emit MOV [rip+disp32], rsp with disp = +2
        e.bytes(&[0x48, 0x89, 0x25]);
        e.u32_le(2); // disp32 = 2 → next_rip + 2 = save_rsp_slot

        // Emit jmp short +8 (EB 08)
        e.bytes(&[0xEB, 0x08]);

        // Reserve 8 bytes for save_rsp_slot.
        let save_rsp_slot_offset = e.len();
        e.bytes(&[0u8; 8]);

        // Now emit: mov rsp, stack_base; ret — to trigger the chain
        e.mov_reg64_imm64(Reg64::Rsp, stack_base as u64);
        e.ret();

        // epilogue: after chain returns
        let epilogue_offset = e.len();
        // mov rsp, [rip+disp32], disp = negative (points back to save_rsp_slot)
        // Instruction: 48 8B 25 <disp32>. next_rip = e.len()+7.
        // save_rsp_slot is at base_ip + save_rsp_slot_offset.
        // disp = save_rsp_slot_absolute - (e.len()+7) after emit.
        let _ = save_rsp_slot_offset;
        // Emit placeholder then patch.
        e.bytes(&[0x48, 0x8B, 0x25]);
        let disp_patch_offset = e.len();
        e.u32_le(0); // placeholder disp32
                     // next_rip at this point = e.len()
        let next_rip_after_mov = e.len();
        let disp_target_offset = save_rsp_slot_offset as i64;
        let disp_from_offset = next_rip_after_mov as i64;
        let disp_val = (disp_target_offset - disp_from_offset) as i32;
        // Patch disp
        let patch_ptr = self.trigger_mem.as_ptr().add(disp_patch_offset) as *mut i32;
        core::ptr::write_unaligned(patch_ptr, disp_val);

        // pop rdi; pop rsi
        e.pop_reg64(Reg64::Rdi);
        e.pop_reg64(Reg64::Rsi);
        // pop r15; r14; r13; r12; rbp; rbx
        e.pop_reg64(Reg64::R15);
        e.pop_reg64(Reg64::R14);
        e.pop_reg64(Reg64::R13);
        e.pop_reg64(Reg64::R12);
        e.pop_reg64(Reg64::Rbp);
        e.pop_reg64(Reg64::Rbx);
        // popfq (9D)
        e.bytes(&[0x9D]);
        // ret
        e.ret();

        Some(base_ip + epilogue_offset)
    }
}

// The Emitter only exposed high-level methods earlier; expose byte-level
// helpers here via inherent-ish extension on *mut u8 callers. We just wrap
// in local helpers that duplicate the Emitter private methods.

// (Done via inherent methods on Emitter — added below via `impl Emitter`
// in a separate block.)

// Note: Emitter::{byte, bytes, u32_le} are pub as of this module's landing —
// rop_chain calls them directly via `bytes`/`u32_le`. No extension impl needed.
