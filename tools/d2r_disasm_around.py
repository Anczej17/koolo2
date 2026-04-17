#!/usr/bin/env python3
"""Disassemble a window of bytes around a target VA."""
import json, sys
import capstone

D2R_BASE = 0x7ff79d3d0000
D2R_END  = 0x7ff79fb52000

def main():
    target = int(sys.argv[1], 16)
    before = int(sys.argv[2]) if len(sys.argv) > 2 else 64
    after  = int(sys.argv[3]) if len(sys.argv) > 3 else 256

    with open("logs/d2r_exec.json") as f:
        meta = json.load(f)
    with open("logs/d2r_exec.bin","rb") as f:
        data = f.read()
    chunks = meta["chunks"]
    for c in chunks:
        if c["va"] <= target < c["va"]+c["size"]:
            base = c["va"]
            off = c["file_off"]
            size = c["size"]
            start_va = max(base, target - before)
            end_va   = min(base + size, target + after)
            slice_off = off + (start_va - base)
            slice_len = end_va - start_va
            d = data[slice_off:slice_off+slice_len]
            md = capstone.Cs(capstone.CS_ARCH_X86, capstone.CS_MODE_64)
            md.detail = False
            for ins in md.disasm(d, start_va):
                marker = " <==" if ins.address == target else ""
                print(f"  0x{ins.address:x}:  {ins.mnemonic:7s} {ins.op_str}{marker}")
            return
    print("VA not in dump")

if __name__=="__main__":
    main()
