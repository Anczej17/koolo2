#!/usr/bin/env python3
"""
Static reverse engineering of D2R.exe to extract packet format spec.

What it does:
  1. Loads D2R.exe via pefile, locates .text section.
  2. Disassembles all of .text via capstone.
  3. Finds every direct/RIP-relative CALL whose target is the send_packet
     function at +0x146600 (verified live address).
  4. For each such call, scans the preceding 64 instructions for the
     packet construction pattern:
       - immediate byte stored at [rsp+N] / [rcx+N] / etc → opcode + fields
       - immediate value loaded into RDX (= packet size)
       - LEA RCX, [rsp+N] (= packet ptr)
  5. Reports: call site address, inferred opcode, inferred size, raw asm.

Usage:  python tools/d2r_re.py [path/to/D2R.exe]

No D2R running needed. No injection. No anti-debug. Pure static analysis.
"""

import sys
import struct
import re
from pathlib import Path

try:
    import pefile
    import capstone
    from capstone.x86 import (
        X86_INS_CALL, X86_INS_LEA, X86_INS_MOV, X86_INS_MOVZX,
        X86_OP_IMM, X86_OP_REG, X86_OP_MEM,
        X86_REG_RCX, X86_REG_ECX, X86_REG_CL,
        X86_REG_RDX, X86_REG_EDX, X86_REG_DL,
        X86_REG_R8, X86_REG_R8D, X86_REG_R8B,
        X86_REG_RSP, X86_REG_RBP,
    )
except ImportError as e:
    print(f"Missing dep: {e}. Install: pip install pefile capstone", file=sys.stderr)
    sys.exit(1)


SEND_PACKET_OFFSET = 0x146600  # verified live: D2R.exe base + 0x146600


def main():
    exe_path = Path(sys.argv[1] if len(sys.argv) > 1
                    else r"C:\Program Files (x86)\Diablo II Resurrected\D2R.exe")
    if not exe_path.exists():
        print(f"D2R.exe not found at {exe_path}", file=sys.stderr)
        sys.exit(1)

    print(f"# loading {exe_path}", file=sys.stderr)
    pe = pefile.PE(str(exe_path), fast_load=True)
    image_base = pe.OPTIONAL_HEADER.ImageBase
    print(f"# image base: 0x{image_base:x}", file=sys.stderr)

    # Find .text section
    text_section = None
    for sec in pe.sections:
        name = sec.Name.rstrip(b"\x00").decode("ascii", errors="ignore")
        if name == ".text":
            text_section = sec
            break
    if text_section is None:
        print("no .text section", file=sys.stderr)
        sys.exit(1)

    text_va = image_base + text_section.VirtualAddress
    text_data = text_section.get_data()
    print(f"# .text VA=0x{text_va:x} size=0x{len(text_data):x}", file=sys.stderr)

    send_packet_va = image_base + SEND_PACKET_OFFSET
    print(f"# send_packet VA=0x{send_packet_va:x}", file=sys.stderr)

    md = capstone.Cs(capstone.CS_ARCH_X86, capstone.CS_MODE_64)
    md.detail = True

    # First pass — disassemble everything, build linear instruction list
    print("# disassembling .text...", file=sys.stderr)
    insns = list(md.disasm(text_data, text_va))
    print(f"# {len(insns)} instructions", file=sys.stderr)

    # Find all CALL instructions targeting send_packet_va
    call_sites = []
    for idx, ins in enumerate(insns):
        if ins.id != X86_INS_CALL:
            continue
        if not ins.operands:
            continue
        op = ins.operands[0]
        # Direct call: imm operand = absolute target
        if op.type == X86_OP_IMM:
            target = op.imm
            if target == send_packet_va:
                call_sites.append((idx, ins, target))

    print(f"# found {len(call_sites)} direct call sites for send_packet", file=sys.stderr)

    if not call_sites:
        # Try wider net: check any near call within ±10 bytes (just in case offset is slightly off)
        print("# no direct hits — dumping any near-target callers as sanity check", file=sys.stderr)
        for idx, ins in enumerate(insns):
            if ins.id != X86_INS_CALL:
                continue
            op = ins.operands[0] if ins.operands else None
            if op and op.type == X86_OP_IMM:
                if abs(op.imm - send_packet_va) < 0x100:
                    print(f"  near call from 0x{ins.address:x} -> 0x{op.imm:x} (delta {op.imm - send_packet_va:+d})")

    # For each call site, scan backwards for the packet construction pattern.
    # Looking for:
    #   - LEA RCX, [rsp+N] or [rbp+N]                  → packet ptr (stack-allocated)
    #   - MOV EDX, <imm>                               → packet size
    #   - MOV byte ptr [rsp+N], <imm8>                 → opcode written to first byte
    print("\n# === packet call site analysis ===\n")
    for ci, (idx, call_ins, target) in enumerate(call_sites):
        info = analyze_call_site(insns, idx, lookback=80)
        if not info:
            continue

        print(f"[{ci+1}] call @ 0x{call_ins.address:x}")
        if info.get("opcode") is not None:
            print(f"     opcode = 0x{info['opcode']:02X}")
        if info.get("size") is not None:
            print(f"     size   = {info['size']}")
        if info.get("buf_offset") is not None:
            print(f"     buf    = [rsp+0x{info['buf_offset']:x}] (stack)")
        if info.get("flags") is not None:
            print(f"     flags  = 0x{info['flags']:x}")
        if info.get("byte_writes"):
            print(f"     bytes  =")
            for off, val, addr in info["byte_writes"]:
                print(f"              [+0x{off:02x}] = 0x{val:02x}  @ 0x{addr:x}")
        print()


def analyze_call_site(insns, call_idx, lookback=80):
    """Walk backwards from call_idx, extract packet construction info."""
    info = {
        "opcode": None,
        "size": None,
        "buf_offset": None,
        "flags": None,
        "byte_writes": [],
    }
    found_lea_rcx = False
    found_mov_edx = False
    found_xor_r8 = False

    start = max(0, call_idx - lookback)
    # Iterate from call backwards
    for i in range(call_idx - 1, start - 1, -1):
        ins = insns[i]
        if not ins.operands:
            continue

        # LEA RCX, [rsp+N]  → packet pointer
        if ins.id == X86_INS_LEA and not found_lea_rcx:
            if (len(ins.operands) >= 2
                    and ins.operands[0].type == X86_OP_REG
                    and ins.operands[0].reg in (X86_REG_RCX, X86_REG_ECX)
                    and ins.operands[1].type == X86_OP_MEM):
                mem = ins.operands[1].mem
                if mem.base == X86_REG_RSP:
                    info["buf_offset"] = mem.disp
                    found_lea_rcx = True

        # MOV EDX, <imm>  → size
        if ins.id == X86_INS_MOV and not found_mov_edx:
            if (len(ins.operands) >= 2
                    and ins.operands[0].type == X86_OP_REG
                    and ins.operands[0].reg in (X86_REG_RDX, X86_REG_EDX, X86_REG_DL)
                    and ins.operands[1].type == X86_OP_IMM):
                info["size"] = ins.operands[1].imm
                found_mov_edx = True

        # MOV/XOR R8D, ... → flags (might be xor self for 0)
        if ins.id == X86_INS_MOV and not found_xor_r8:
            if (len(ins.operands) >= 2
                    and ins.operands[0].type == X86_OP_REG
                    and ins.operands[0].reg in (X86_REG_R8, X86_REG_R8D, X86_REG_R8B)
                    and ins.operands[1].type == X86_OP_IMM):
                info["flags"] = ins.operands[1].imm
                found_xor_r8 = True

        # MOV byte ptr [rsp+N], <imm8>
        if ins.id == X86_INS_MOV:
            if (len(ins.operands) >= 2
                    and ins.operands[0].type == X86_OP_MEM
                    and ins.operands[1].type == X86_OP_IMM):
                mem = ins.operands[0].mem
                if mem.base == X86_REG_RSP and ins.operands[1].imm < 0x100:
                    # Write to stack at rsp+disp
                    if info["buf_offset"] is not None:
                        rel = mem.disp - info["buf_offset"]
                        if 0 <= rel < 64:
                            info["byte_writes"].append((rel, ins.operands[1].imm, ins.address))
                            if rel == 0 and info["opcode"] is None:
                                info["opcode"] = ins.operands[1].imm

    # Sort byte writes by offset
    info["byte_writes"].sort(key=lambda x: x[0])
    return info


if __name__ == "__main__":
    main()
