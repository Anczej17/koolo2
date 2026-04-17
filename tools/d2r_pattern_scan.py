#!/usr/bin/env python3
"""
Pattern-based packet builder finder.

Doesn't require direct calls to send_packet. Instead finds the pattern:
   mov byte ptr [rsp+X], <imm8>     ; write opcode to stack buffer
   ... up to ~30 insns ...
   lea rcx, [rsp+X]                 ; buf ptr = same offset
   ... up to ~10 insns ...
   call <anything>                  ; direct or indirect

When this triplet is found, it's a packet builder. The opcode is the imm8.
Any subsequent stack writes between opcode write and lea are payload bytes.
"""

import argparse
import json
import os
import sys

import capstone
from capstone.x86 import (
    X86_INS_CALL, X86_INS_LEA, X86_INS_MOV,
    X86_OP_IMM, X86_OP_REG, X86_OP_MEM,
    X86_REG_RCX, X86_REG_RSP,
)

D2R_BASE = 0x7ff79d3d0000
D2R_END  = 0x7ff79fb52000


def load_dump(prefix):
    with open(prefix + ".json") as f:
        meta = json.load(f)
    with open(prefix + ".bin", "rb") as f:
        data = f.read()
    chunks = [c for c in meta["chunks"] if D2R_BASE <= c["va"] < D2R_END]
    return data, chunks


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--prefix", required=True)
    ap.add_argument("--section", required=True)
    ap.add_argument("--known", default="logs/builders_known.json")
    args = ap.parse_args()

    known = {}
    if os.path.exists(args.known):
        with open(args.known) as f:
            known = json.load(f)

    print(f"# loading {args.prefix}", file=sys.stderr)
    data, chunks = load_dump(args.prefix)

    md = capstone.Cs(capstone.CS_ARCH_X86, capstone.CS_MODE_64)
    md.detail = True

    # Walk each chunk LINEARLY (not interleaved) since builders are local to a function.
    new_builders = []
    for c in chunks:
        d = data[c["file_off"] : c["file_off"] + c["size"]]
        insns = list(md.disasm(d, c["va"]))
        scan_chunk_for_builders(insns, new_builders, known)

    print(f"\n# === SECTION {args.section}: {len(new_builders)} new builders ===\n")
    for b in new_builders:
        op_s = f"0x{b['opcode']:02X}"
        print(f"  builder @ 0x{b['opcode_addr']:x}  opcode={op_s}  size={b['size']}  buf=[rsp+0x{b['buf_off']:x}]  call @ 0x{b['call_addr']:x}")
        if b["payload_writes"]:
            for off, val, addr in b["payload_writes"][:8]:
                print(f"      [+0x{off:02x}] = 0x{val:02x}  @ 0x{addr:x}")
        key = f"0x{b['opcode_addr']:x}"
        known[key] = {
            "section": args.section,
            "opcode": b["opcode"],
            "buf_off": b["buf_off"],
            "call_addr": f"0x{b['call_addr']:x}",
            "payload": b["payload_writes"][:8],
        }

    with open(args.known, "w") as f:
        json.dump(known, f, indent=2, default=str)

    # Print opcode summary
    by_opcode = {}
    for k, v in known.items():
        op = v.get("opcode")
        if op is None: continue
        by_opcode.setdefault(op, []).append(v)
    print(f"\n# cumulative builders by opcode:")
    for op in sorted(by_opcode.keys()):
        secs = sorted(set(v["section"] for v in by_opcode[op]))
        print(f"   0x{op:02X}  ({len(by_opcode[op])} sites)  sections: {','.join(secs)}")


def scan_chunk_for_builders(insns, out_list, known):
    """Linear scan: for each `mov [rsp+X], imm` (any size — byte/word/dword/qword),
    treat the LOW byte of the immediate as the opcode candidate. Then look
    forward for `lea rcx, [rsp+X]` (matching offset) then a `call`."""
    n = len(insns)
    for i, ins in enumerate(insns):
        if ins.id != X86_INS_MOV or len(ins.operands) < 2:
            continue
        op_dst = ins.operands[0]
        op_src = ins.operands[1]
        if op_dst.type != X86_OP_MEM or op_src.type != X86_OP_IMM:
            continue
        # Accept any size 1, 2, 4, or 8 (qword imm32 sign-extended)
        if op_dst.size not in (1, 2, 4, 8):
            continue
        if op_dst.mem.base != X86_REG_RSP:
            continue
        # Low byte of immediate = candidate opcode
        opcode_imm = op_src.imm & 0xFF
        opcode_off = op_dst.mem.disp
        opcode_addr = ins.address

        # Filters to cut noise
        if opcode_imm == 0x00:
            continue  # almost never a real packet opcode
        if opcode_off < 0x20:
            continue  # below stack home/shadow space — not a packet buffer
        if opcode_off > 0x200:
            continue  # too large — probably random local var

        # Skip if we already saw this exact opcode write
        if f"0x{opcode_addr:x}" in known:
            continue

        # Look forward up to 60 instructions for `lea rcx, [rsp+opcode_off]`
        lea_idx = None
        for j in range(i + 1, min(i + 60, n)):
            ji = insns[j]
            if ji.id == X86_INS_LEA and len(ji.operands) >= 2:
                if (ji.operands[0].type == X86_OP_REG
                        and ji.operands[0].reg == X86_REG_RCX
                        and ji.operands[1].type == X86_OP_MEM
                        and ji.operands[1].mem.base == X86_REG_RSP
                        and ji.operands[1].mem.disp == opcode_off):
                    lea_idx = j
                    break
        if lea_idx is None:
            continue

        # Find call within ~20 instructions after lea
        call_idx = None
        for j in range(lea_idx + 1, min(lea_idx + 20, n)):
            if insns[j].id == X86_INS_CALL:
                call_idx = j
                break
        if call_idx is None:
            continue

        # Look ANYWHERE in window [i .. call_idx] for `mov edx/dx/dl, <small imm>`
        # (size argument). Capstone register IDs: RDX/EDX/DX/DL.
        from capstone.x86 import X86_REG_RDX as RDX, X86_REG_EDX as EDX, X86_REG_DX as DX, X86_REG_DL as DL
        size_set = None
        for j in range(i, call_idx):
            ji = insns[j]
            if ji.id == X86_INS_MOV and len(ji.operands) >= 2:
                if (ji.operands[0].type == X86_OP_REG
                        and ji.operands[0].reg in (RDX, EDX, DX, DL)
                        and ji.operands[1].type == X86_OP_IMM
                        and 0 < ji.operands[1].imm <= 256):
                    size_set = ji.operands[1].imm
        if size_set is None:
            continue

        # Collect any other `mov [rsp+X], imm` writes between the opcode write
        # and the lea — those are payload bytes.
        payload = []
        for j in range(i + 1, lea_idx):
            ji = insns[j]
            if ji.id != X86_INS_MOV or len(ji.operands) < 2:
                continue
            if ji.operands[0].type != X86_OP_MEM:
                continue
            if ji.operands[0].mem.base != X86_REG_RSP:
                continue
            if ji.operands[1].type != X86_OP_IMM:
                continue
            rel = ji.operands[0].mem.disp - opcode_off
            if 0 < rel < 64:
                payload.append((rel, ji.operands[1].imm, ji.address))

        out_list.append({
            "opcode": opcode_imm,
            "size": size_set,
            "opcode_addr": opcode_addr,
            "buf_off": opcode_off,
            "call_addr": insns[call_idx].address,
            "payload_writes": sorted(payload, key=lambda x: x[0]),
        })


if __name__ == "__main__":
    main()
