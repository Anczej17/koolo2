#!/usr/bin/env python3
"""
Per-section analyzer. Loads a dump, finds all send_packet callers, compares
to a cumulative known-callers JSON file, prints only NEW callers, then
updates the cumulative file.

Output goes to logs/sections_known.json (cumulative across all sections).
"""

import argparse
import json
import os
import sys

import capstone
from capstone.x86 import (
    X86_INS_CALL, X86_INS_LEA, X86_INS_MOV, X86_INS_XOR,
    X86_OP_IMM, X86_OP_REG, X86_OP_MEM,
    X86_REG_RCX, X86_REG_ECX,
    X86_REG_RDX, X86_REG_EDX,
    X86_REG_R8, X86_REG_R8D,
    X86_REG_RSP,
)

D2R_BASE = 0x7ff79d3d0000
D2R_END  = 0x7ff79fb52000
SEND_PACKET = D2R_BASE + 0x146600

KNOWN_FILE = "logs/sections_known.json"


def load_dump(prefix):
    with open(prefix + ".json") as f:
        meta = json.load(f)
    with open(prefix + ".bin", "rb") as f:
        data = f.read()
    chunks = [c for c in meta["chunks"] if D2R_BASE <= c["va"] < D2R_END]
    return data, chunks


def disassemble_chunks(data, chunks):
    md = capstone.Cs(capstone.CS_ARCH_X86, capstone.CS_MODE_64)
    md.detail = True
    insns = {}
    for c in chunks:
        d = data[c["file_off"] : c["file_off"] + c["size"]]
        for ins in md.disasm(d, c["va"]):
            insns[ins.address] = ins
    return insns


def find_calls(insns):
    calls = []
    for addr, ins in insns.items():
        if ins.id == X86_INS_CALL and ins.operands:
            op = ins.operands[0]
            if op.type == X86_OP_IMM and op.imm == SEND_PACKET:
                calls.append(addr)
    return sorted(calls)


def analyze_call(insns, sorted_addrs, call_idx, lookback=120):
    info = {"opcode": None, "size": None, "flags": None, "buf_off": None,
            "byte_writes": []}
    found = {"rcx": False, "rdx": False, "r8": False}

    start = max(0, call_idx - lookback)
    for i in range(call_idx - 1, start - 1, -1):
        ins = insns[sorted_addrs[i]]
        if not ins.operands:
            continue

        if ins.id == X86_INS_LEA and not found["rcx"]:
            if (len(ins.operands) >= 2
                    and ins.operands[0].type == X86_OP_REG
                    and ins.operands[0].reg in (X86_REG_RCX, X86_REG_ECX)
                    and ins.operands[1].type == X86_OP_MEM):
                mem = ins.operands[1].mem
                if mem.base == X86_REG_RSP:
                    info["buf_off"] = mem.disp
                    found["rcx"] = True

        if ins.id == X86_INS_MOV and not found["rdx"]:
            if (len(ins.operands) >= 2
                    and ins.operands[0].type == X86_OP_REG
                    and ins.operands[0].reg in (X86_REG_RDX, X86_REG_EDX)
                    and ins.operands[1].type == X86_OP_IMM):
                info["size"] = ins.operands[1].imm
                found["rdx"] = True

        if ins.id == X86_INS_XOR and not found["rdx"]:
            if (len(ins.operands) >= 2
                    and ins.operands[0].type == X86_OP_REG
                    and ins.operands[0].reg in (X86_REG_RDX, X86_REG_EDX)
                    and ins.operands[1].type == X86_OP_REG
                    and ins.operands[1].reg == ins.operands[0].reg):
                info["size"] = 0  # xor self = 0 (probably means size from elsewhere)
                found["rdx"] = True

        if ins.id == X86_INS_MOV and not found["r8"]:
            if (len(ins.operands) >= 2
                    and ins.operands[0].type == X86_OP_REG
                    and ins.operands[0].reg in (X86_REG_R8, X86_REG_R8D)
                    and ins.operands[1].type == X86_OP_IMM):
                info["flags"] = ins.operands[1].imm
                found["r8"] = True

        if ins.id == X86_INS_XOR and not found["r8"]:
            if (len(ins.operands) >= 2
                    and ins.operands[0].type == X86_OP_REG
                    and ins.operands[0].reg in (X86_REG_R8, X86_REG_R8D)
                    and ins.operands[1].type == X86_OP_REG
                    and ins.operands[1].reg == ins.operands[0].reg):
                info["flags"] = 0
                found["r8"] = True

        # Captures opcode if [rsp+buf_off] is written with imm
        if ins.id == X86_INS_MOV and info["buf_off"] is not None:
            if (len(ins.operands) >= 2
                    and ins.operands[0].type == X86_OP_MEM
                    and ins.operands[1].type == X86_OP_IMM):
                mem = ins.operands[0].mem
                if (mem.base == X86_REG_RSP
                        and ins.operands[1].imm < 0x10000):
                    rel = mem.disp - info["buf_off"]
                    if 0 <= rel < 64:
                        info["byte_writes"].append((rel, ins.operands[1].imm, ins.address))
                        if rel == 0 and info["opcode"] is None:
                            info["opcode"] = ins.operands[1].imm

    info["byte_writes"].sort(key=lambda x: x[0])
    return info


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--prefix", required=True)
    ap.add_argument("--section", required=True)
    args = ap.parse_args()

    # Load known
    known = {}  # call_addr_str -> {section, opcode, size, flags}
    if os.path.exists(KNOWN_FILE):
        with open(KNOWN_FILE) as f:
            known = json.load(f)

    print(f"# loading dump {args.prefix}.bin/json", file=sys.stderr)
    data, chunks = load_dump(args.prefix)
    print(f"# {len(chunks)} D2R chunks, {sum(c['size'] for c in chunks):,} bytes", file=sys.stderr)

    print(f"# disassembling...", file=sys.stderr)
    insns = disassemble_chunks(data, chunks)
    sorted_addrs = sorted(insns.keys())
    print(f"# {len(insns):,} instructions", file=sys.stderr)

    calls = find_calls(insns)
    print(f"# total send_packet call sites in this dump: {len(calls)}", file=sys.stderr)

    new_calls = [c for c in calls if f"0x{c:x}" not in known]
    print(f"\n# === SECTION {args.section}: {len(new_calls)} NEW call sites ===\n")

    for call in new_calls:
        try:
            ci = sorted_addrs.index(call)
        except ValueError:
            continue
        info = analyze_call(insns, sorted_addrs, ci)

        op = info.get("opcode")
        sz = info.get("size")
        flag = info.get("flags")
        op_s = f"0x{op:02X}" if op is not None else "?"
        sz_s = str(sz) if sz is not None else "?"
        flag_s = f"0x{flag:x}" if flag is not None else "?"
        print(f"  call @ 0x{call:x}  opcode={op_s}  size={sz_s}  flags={flag_s}")
        if info["byte_writes"]:
            for off, val, addr in info["byte_writes"][:6]:
                print(f"      [+0x{off:02x}] = 0x{val:02x}")

        known[f"0x{call:x}"] = {
            "section": args.section,
            "opcode": op,
            "size": sz,
            "flags": flag,
            "byte_writes": info["byte_writes"][:8],
        }

    print(f"\n# cumulative total: {len(known)} unique call sites known")

    with open(KNOWN_FILE, "w") as f:
        json.dump(known, f, indent=2, default=str)


if __name__ == "__main__":
    main()
