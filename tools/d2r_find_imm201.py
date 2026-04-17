#!/usr/bin/env python3
"""Find ALL instructions whose any operand is the immediate 0x201 — not just CMP."""
import json
import capstone
from capstone.x86 import X86_OP_IMM

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

    targets = {0x201: [], 0x202: [], 0x204: [], 0x205: [], 0x200: [], 0x20A: []}
    for c in chunks:
        d = data[c["file_off"]:c["file_off"]+c["size"]]
        for ins in md.disasm(d, c["va"]):
            for op in ins.operands:
                if op.type != X86_OP_IMM: continue
                v = op.imm & 0xFFFFFFFF
                if v in targets:
                    targets[v].append((ins.address, ins.mnemonic, ins.op_str))
                    break
    for k in sorted(targets):
        lst = targets[k]
        print(f"\n# imm 0x{k:x}: {len(lst)} hits")
        for a,m,o in lst[:20]:
            print(f"  0x{a:x}  {m} {o}")

if __name__ == "__main__":
    main()
