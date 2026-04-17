#!/usr/bin/env python3
"""Disassemble the wndproc dispatcher chain functions from merged dump.

Targets:
  0x7ff79df7a7f0 — mouse-button dispatcher (called from wndproc with
                   button_bits, action 10=DOWN/11=UP/12=DBLCLK)
  0x7ff79df7a920 — keyboard handler 1
  0x7ff79df7a9e0 — keyboard handler 2

Output: linear disasm + call/jmp targets, written to a text report.
"""

import json
import sys
from collections import defaultdict

import capstone

DUMP_BIN = "logs/d2r_exec_merged.bin"
DUMP_JSON = "logs/d2r_exec_merged.json"

TARGETS = {
    0x7ff79df795d0: ("real_click_worker", 0x600),
    0x7ff79df796c0: ("worker_796c0", 0x300),
    0x7ff79df7a880: ("nonclient_handler", 0x100),
    0x7ff79df7a920: ("keyboard_handler", 0xc0),
    0x7ff79df7a9e0: ("keyboard_handler_2", 0xc0),
}

def load_chunks():
    meta = json.load(open(DUMP_JSON))
    chunks = meta["chunks"]
    return chunks


def read_at(va, size, chunks, fh):
    for c in chunks:
        if c["va"] <= va < c["va"] + c["size"]:
            off = c["file_off"] + (va - c["va"])
            avail = c["va"] + c["size"] - va
            n = min(size, avail)
            fh.seek(off)
            return fh.read(n)
    return None


def main():
    chunks = load_chunks()
    md = capstone.Cs(capstone.CS_ARCH_X86, capstone.CS_MODE_64)
    md.detail = True

    out = []
    out.append("# wndproc dispatcher chain disassembly")
    out.append("# generated from logs/d2r_exec_merged.bin")
    out.append("")

    with open(DUMP_BIN, "rb") as fh:
        for va, (name, size) in TARGETS.items():
            data = read_at(va, size, chunks, fh)
            if data is None:
                out.append(f"## {name} @ 0x{va:x}: NOT IN DUMP")
                out.append("")
                continue
            out.append(f"## {name} @ 0x{va:x} (size {size}b)")
            out.append("```")
            calls = []
            jmps = []
            ret_seen = False
            count = 0
            for ins in md.disasm(data, va):
                if count >= 200:
                    out.append(f"  ... (truncated at {count} instructions)")
                    break
                out.append(f"  0x{ins.address:08x}  {ins.mnemonic:8s} {ins.op_str}")
                count += 1
                if ins.mnemonic == "call":
                    if ins.operands and ins.operands[0].type == capstone.x86.X86_OP_IMM:
                        calls.append((ins.address, ins.operands[0].imm))
                if ins.mnemonic in ("jmp", "je", "jne", "jz", "jnz"):
                    if ins.operands and ins.operands[0].type == capstone.x86.X86_OP_IMM:
                        jmps.append((ins.address, ins.operands[0].imm))
                if ins.mnemonic == "ret":
                    ret_seen = True
                    out.append("  ; --- RET ---")
                    if not jmps:
                        break
            out.append("```")
            out.append("")
            if calls:
                out.append("### direct calls")
                for src, tgt in calls[:30]:
                    out.append(f"  0x{src:x} -> 0x{tgt:x}")
                out.append("")

    open("logs/disasm_dispatcher.txt", "w").write("\n".join(out))
    print(f"wrote logs/disasm_dispatcher.txt ({sum(len(l) for l in out)} bytes)")


if __name__ == "__main__":
    main()
