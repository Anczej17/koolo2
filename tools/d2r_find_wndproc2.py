#!/usr/bin/env python3
"""
Pass 2: find WindowProc candidates by looking for jump-table style dispatch
on rdx/edx parameter (msg). MSVC commonly emits:

    sub  edx, 0x100      ; or some base WM_*
    cmp  edx, <range>
    ja   default
    movzx eax, byte ptr [tab + rdx*1]
    jmp  [tab2 + rax*8]

OR a chained `cmp`/`je` ladder. We already saw the latter. So this pass scans
for the JUMP TABLE form: `sub edx, imm32` followed shortly by `cmp edx, imm32`.

We also identify functions whose ENTRY shows the calling-convention prologue
typical of a WindowProc: arg2 (rdx) is preserved early to a stack/non-volatile
location, hwnd (rcx) likewise. Many wndprocs immediately stash all 4 args.

Strategy: linear scan for `sub edx/rdx, imm` where 0x100 <= imm <= 0x500. Then
verify a `cmp edx, imm` within next 5 instructions where imm is small (<=0x40).
"""
import json, sys
import capstone
from capstone.x86 import (X86_INS_SUB, X86_INS_CMP, X86_OP_REG, X86_OP_IMM,
                          X86_REG_RDX, X86_REG_EDX, X86_REG_DX, X86_INS_MOV,
                          X86_REG_RCX, X86_REG_R8, X86_REG_R9)

D2R_BASE = 0x7ff79d3d0000
D2R_END  = 0x7ff79fb52000

def main():
    with open("logs/d2r_exec.json") as f:
        meta = json.load(f)
    with open("logs/d2r_exec.bin","rb") as f:
        data = f.read()
    chunks = [c for c in meta["chunks"] if D2R_BASE <= c["va"] < D2R_END]

    md = capstone.Cs(capstone.CS_ARCH_X86, capstone.CS_MODE_64)
    md.detail = True

    DX = (X86_REG_RDX, X86_REG_EDX, X86_REG_DX)

    candidates = []
    for c in chunks:
        d = data[c["file_off"]:c["file_off"]+c["size"]]
        insns = list(md.disasm(d, c["va"]))
        n = len(insns)
        for i,ins in enumerate(insns):
            if ins.id != X86_INS_SUB: continue
            if len(ins.operands) < 2: continue
            op0,op1 = ins.operands[0], ins.operands[1]
            if op0.type != X86_OP_REG or op0.reg not in DX: continue
            if op1.type != X86_OP_IMM: continue
            base = op1.imm & 0xFFFFFFFF
            if not (0x100 <= base <= 0x500): continue
            # look forward up to 6 insns for cmp edx, small
            for j in range(i+1, min(i+7, n)):
                jj = insns[j]
                if jj.id != X86_INS_CMP: continue
                if len(jj.operands) < 2: continue
                if jj.operands[0].type != X86_OP_REG: continue
                if jj.operands[0].reg not in DX: continue
                if jj.operands[1].type != X86_OP_IMM: continue
                rng = jj.operands[1].imm & 0xFFFFFFFF
                if rng > 0x80: break
                candidates.append((ins.address, base, rng))
                break

    print(f"# {len(candidates)} jump-table dispatch candidates on edx")
    for addr, base, rng in candidates:
        print(f"  0x{addr:x}  sub edx,0x{base:x}  cmp edx,0x{rng:x}  -> handles WM_0x{base:x}..0x{base+rng:x}")

if __name__ == "__main__":
    main()
