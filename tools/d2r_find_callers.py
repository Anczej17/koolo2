#!/usr/bin/env python3
"""
Loads d2r_exec.bin + d2r_exec.json and disassembles all D2R chunks via
capstone. Finds:
  1. Whether send_packet (D2R+0x146600) is in any captured chunk.
  2. All direct CALL instructions targeting send_packet.
  3. For each call site, the surrounding instructions (packet construction).
"""

import sys
import json
import argparse
from collections import defaultdict

import capstone
from capstone.x86 import (
    X86_INS_CALL, X86_INS_LEA, X86_INS_MOV,
    X86_OP_IMM, X86_OP_REG, X86_OP_MEM,
    X86_REG_RCX, X86_REG_ECX,
    X86_REG_RDX, X86_REG_EDX,
    X86_REG_R8, X86_REG_R8D,
    X86_REG_RSP,
)

D2R_BASE = 0x7ff79d3d0000
D2R_END  = 0x7ff79fb52000
SEND_PACKET = D2R_BASE + 0x146600


def load(prefix):
    with open(prefix + ".json") as f:
        meta = json.load(f)
    with open(prefix + ".bin", "rb") as f:
        data = f.read()
    chunks = [c for c in meta["chunks"] if D2R_BASE <= c["va"] < D2R_END]
    print(f"# loaded {len(chunks)} D2R chunks", file=sys.stderr)
    print(f"# D2R bytes: {sum(c['size'] for c in chunks):,}", file=sys.stderr)
    return data, chunks


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--prefix", default="logs/d2r_exec")
    args = ap.parse_args()

    data, chunks = load(args.prefix)

    md = capstone.Cs(capstone.CS_ARCH_X86, capstone.CS_MODE_64)
    md.detail = True

    # Check if send_packet itself is captured
    send_chunk = next((c for c in chunks if c["va"] <= SEND_PACKET < c["va"] + c["size"]), None)
    if send_chunk:
        off = SEND_PACKET - send_chunk["va"]
        bytes_at_send = data[send_chunk["file_off"] + off : send_chunk["file_off"] + off + 32]
        print(f"# send_packet @ 0x{SEND_PACKET:x} CAPTURED, first 32 bytes: {bytes_at_send.hex()}")
    else:
        print(f"# WARNING: send_packet @ 0x{SEND_PACKET:x} NOT in dump (page was NA at dump time)")

    # Disassemble each chunk and look for calls to SEND_PACKET
    call_sites = []
    insns_by_va = {}  # va -> instruction object (for backward walk)

    for c in chunks:
        d = data[c["file_off"] : c["file_off"] + c["size"]]
        for ins in md.disasm(d, c["va"]):
            insns_by_va[ins.address] = ins
            if ins.id == X86_INS_CALL and ins.operands:
                op = ins.operands[0]
                if op.type == X86_OP_IMM and op.imm == SEND_PACKET:
                    call_sites.append(ins.address)

    print(f"# direct CALL sites to send_packet: {len(call_sites)}", file=sys.stderr)
    if not call_sites:
        # Look for ALL calls to anything within 0x10 of send_packet (paranoia)
        near = []
        for c in chunks:
            d = data[c["file_off"] : c["file_off"] + c["size"]]
            for ins in md.disasm(d, c["va"]):
                if ins.id == X86_INS_CALL and ins.operands:
                    op = ins.operands[0]
                    if op.type == X86_OP_IMM and abs(op.imm - SEND_PACKET) < 0x100:
                        near.append((ins.address, op.imm))
        if near:
            print(f"# near calls (within ±0x100): {len(near)}", file=sys.stderr)
            for addr, tgt in near[:10]:
                print(f"   call @ 0x{addr:x} -> 0x{tgt:x} (delta {tgt - SEND_PACKET:+d})")
        return

    # For each call site, walk backwards in the same chunk's instruction stream
    sorted_addrs = sorted(insns_by_va.keys())

    print(f"\n# === call site analysis ({len(call_sites)} sites) ===\n")
    for ci, call_va in enumerate(call_sites):
        # Find call_va in sorted list, walk back ~80 instructions
        try:
            ci_idx = sorted_addrs.index(call_va)
        except ValueError:
            continue
        info = analyze(insns_by_va, sorted_addrs, ci_idx, lookback=120)
        op = info.get("opcode")
        sz = info.get("size")
        flag = info.get("flags")
        print(f"[{ci+1}] call @ 0x{call_va:x}  opcode={('0x%02X' % op) if op is not None else '?'}  size={sz if sz is not None else '?'}  flags={('0x%x' % flag) if flag is not None else '?'}")
        if info.get("byte_writes"):
            for off, val, addr in info["byte_writes"][:8]:
                print(f"     [+0x{off:02x}] = 0x{val:02x}  ({addr:#x})")
        print()


def analyze(insns_by_va, sorted_addrs, call_idx, lookback=120):
    info = {"opcode": None, "size": None, "flags": None, "byte_writes": []}
    found = {"rcx": False, "rdx": False, "r8": False}

    start = max(0, call_idx - lookback)
    for i in range(call_idx - 1, start - 1, -1):
        ins = insns_by_va[sorted_addrs[i]]
        if not ins.operands:
            continue

        # LEA RCX, [rsp+N]
        if ins.id == X86_INS_LEA and not found["rcx"]:
            if (len(ins.operands) >= 2
                    and ins.operands[0].type == X86_OP_REG
                    and ins.operands[0].reg in (X86_REG_RCX, X86_REG_ECX)
                    and ins.operands[1].type == X86_OP_MEM):
                mem = ins.operands[1].mem
                if mem.base == X86_REG_RSP:
                    info["buf_off"] = mem.disp
                    found["rcx"] = True

        # MOV EDX, <imm>
        if ins.id == X86_INS_MOV and not found["rdx"]:
            if (len(ins.operands) >= 2
                    and ins.operands[0].type == X86_OP_REG
                    and ins.operands[0].reg in (X86_REG_RDX, X86_REG_EDX)
                    and ins.operands[1].type == X86_OP_IMM):
                info["size"] = ins.operands[1].imm
                found["rdx"] = True

        # MOV R8D, <imm>
        if ins.id == X86_INS_MOV and not found["r8"]:
            if (len(ins.operands) >= 2
                    and ins.operands[0].type == X86_OP_REG
                    and ins.operands[0].reg in (X86_REG_R8, X86_REG_R8D)
                    and ins.operands[1].type == X86_OP_IMM):
                info["flags"] = ins.operands[1].imm
                found["r8"] = True

        # MOV byte ptr [rsp+N], <imm8>
        if ins.id == X86_INS_MOV:
            if (len(ins.operands) >= 2
                    and ins.operands[0].type == X86_OP_MEM
                    and ins.operands[1].type == X86_OP_IMM):
                mem = ins.operands[0].mem
                if (mem.base == X86_REG_RSP
                        and ins.operands[1].imm < 0x10000
                        and "buf_off" in info):
                    rel = mem.disp - info["buf_off"]
                    if 0 <= rel < 64:
                        info["byte_writes"].append((rel, ins.operands[1].imm, ins.address))
                        if rel == 0 and info["opcode"] is None:
                            info["opcode"] = ins.operands[1].imm

    info["byte_writes"].sort(key=lambda x: x[0])
    return info


if __name__ == "__main__":
    main()
