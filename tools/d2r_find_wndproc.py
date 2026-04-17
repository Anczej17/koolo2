#!/usr/bin/env python3
"""
Locate D2R's WindowProc by scanning for distinctive immediate-compare patterns.

A WindowProc has signature (HWND, UINT msg, WPARAM, LPARAM). x64 ABI puts msg
in rdx (or edx). Inside the proc, there is typically either:
   - a chain of `cmp edx, <imm32>` against WM_* constants, OR
   - a `sub edx, <base>` then jump-table dispatch.

We also look for `cmp r/m32, 0x201` (WM_LBUTTONDOWN) as a high-signal anchor.

Output: a ranked list of candidate function entry VAs.
"""
import json, struct, sys

D2R_BASE = 0x7ff79d3d0000
D2R_END  = 0x7ff79fb52000

WM_CONSTS = {
    0x0001: "WM_CREATE",
    0x0002: "WM_DESTROY",
    0x0010: "WM_CLOSE",
    0x0014: "WM_ERASEBKGND",
    0x0020: "WM_SETCURSOR",
    0x0024: "WM_GETMINMAXINFO",
    0x0046: "WM_WINDOWPOSCHANGING",
    0x0047: "WM_WINDOWPOSCHANGED",
    0x0100: "WM_KEYDOWN",
    0x0101: "WM_KEYUP",
    0x0102: "WM_CHAR",
    0x0104: "WM_SYSKEYDOWN",
    0x0105: "WM_SYSKEYUP",
    0x0200: "WM_MOUSEMOVE",
    0x0201: "WM_LBUTTONDOWN",
    0x0202: "WM_LBUTTONUP",
    0x0204: "WM_RBUTTONDOWN",
    0x0205: "WM_RBUTTONUP",
    0x020A: "WM_MOUSEWHEEL",
    0x0231: "WM_ENTERSIZEMOVE",
    0x0232: "WM_EXITSIZEMOVE",
    0x0282: "WM_IME_*",
}

def load():
    with open("logs/d2r_exec.json") as f:
        meta = json.load(f)
    with open("logs/d2r_exec.bin","rb") as f:
        data = f.read()
    chunks = [c for c in meta["chunks"] if D2R_BASE <= c["va"] < D2R_END]
    return data, chunks

def main():
    data, chunks = load()

    # Build a flat (va -> (file_off, size)) so we can reach back into bytes by VA.
    chunk_index = []
    for c in chunks:
        chunk_index.append((c["va"], c["va"]+c["size"], c["file_off"]))

    def va_to_off(va):
        for s,e,o in chunk_index:
            if s <= va < e:
                return o + (va - s)
        return None

    import capstone
    md = capstone.Cs(capstone.CS_ARCH_X86, capstone.CS_MODE_64)
    md.detail = True
    from capstone.x86 import (X86_INS_CMP, X86_INS_SUB, X86_INS_MOV,
                              X86_OP_REG, X86_OP_IMM, X86_REG_RDX, X86_REG_EDX,
                              X86_REG_DX)

    # Scan every chunk linearly. For each instruction, check if it's
    # a CMP/SUB against rdx/edx with one of the WM_* constants.
    # Group hits by enclosing 4KB page and rank by distinct WM count.
    from collections import defaultdict
    hits_by_page = defaultdict(set)        # page_va -> set of (va, wm_const)
    hits_by_chunk = defaultdict(list)

    DX_REGS = (X86_REG_RDX, X86_REG_EDX, X86_REG_DX)

    for c in chunks:
        d = data[c["file_off"]:c["file_off"]+c["size"]]
        for ins in md.disasm(d, c["va"]):
            if ins.id not in (X86_INS_CMP, X86_INS_SUB):
                continue
            if len(ins.operands) < 2: continue
            op0, op1 = ins.operands[0], ins.operands[1]
            if op0.type != X86_OP_REG: continue
            if op0.reg not in DX_REGS: continue
            if op1.type != X86_OP_IMM: continue
            imm = op1.imm & 0xFFFFFFFF
            if imm in WM_CONSTS:
                page = ins.address & ~0xFFF
                hits_by_page[page].add((ins.address, imm))
                hits_by_chunk[c["va"]].append((ins.address, imm, WM_CONSTS[imm]))

    print(f"# pages with >=2 distinct WM_* compares: ")
    ranked = []
    for page, hs in hits_by_page.items():
        consts = set(h[1] for h in hs)
        ranked.append((len(consts), page, hs))
    ranked.sort(reverse=True)
    for score, page, hs in ranked[:30]:
        consts = sorted(set(WM_CONSTS[h[1]] for h in hs))
        print(f"  page 0x{page:x}  distinct={score}  {','.join(consts)}")
        for a,c in sorted(hs):
            print(f"     0x{a:x}  cmp dx,0x{c:04x}  ({WM_CONSTS[c]})")

    # Specifically: where is 0x201?
    print("\n# all WM_LBUTTONDOWN (0x201) compares:")
    for c in chunks:
        for a,wm,name in hits_by_chunk.get(c["va"], []):
            if wm == 0x201:
                print(f"  0x{a:x}")

if __name__ == "__main__":
    main()
