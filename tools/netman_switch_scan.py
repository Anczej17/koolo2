#!/usr/bin/env python3
"""
Audit F1 - find all opcode-switch dispatcher patterns across the full D2R
executable dump (logs/d2r_exec.bin + sidecar .json).

MSVC x64 dense-switch canonical pattern:
    movzx  eax, byte ptr [??]           ; or movzx eax, al
    sub    eax, <base>                  ; optional
    cmp    eax, <maxIdx>
    ja     <default>
    lea    rcx, [rip + tableA]          ; table of DWORD offsets (jumptable)
    movsxd rax, dword ptr [rcx + rax*4]
    add    rax, rcx
    jmp    rax

We scan every 4KB code page for this pattern by sliding a capstone window and
looking for the movzx -> cmp(imm<=0x200) -> lea(rip+disp) -> movsxd[..*4] -> add
-> jmp sequence within ~16 instructions.  Every hit is saved with:
  function start VA  (best effort - walk back to nearest 0xCC pad)
  switch-head VA      (the movzx)
  cmp_imm
  table_va
  default_va
Then we rank by cmp_imm (bigger = fatter switch = better chance of opcode dispatch)
and write logs/AUDIT_F1_switch_hits.json.

This dump is stale-ASLR (captured under a different D2R base). We report
addresses as RVAs relative to 0x7ff79d3d0000 (the dump's base).
"""
import json
import struct
import sys
import os
import capstone

DUMP_BIN  = r"C:\Users\Administrator\Desktop\Audyt Koolo\koolo2-rebranding\logs\d2r_exec.bin"
DUMP_JSON = r"C:\Users\Administrator\Desktop\Audyt Koolo\koolo2-rebranding\logs\d2r_exec.json"
OUT       = r"C:\Users\Administrator\Desktop\Audyt Koolo\koolo2-rebranding\logs\AUDIT_F1_switch_hits.json"

D2R_DUMP_BASE = 0x7ff79d3d0000
D2R_DUMP_END  = 0x7ff79fb52000

def main():
    print("[*] loading dump sidecar")
    meta = json.load(open(DUMP_JSON))
    chunks_all = sorted(
        [c for c in meta["chunks"] if D2R_DUMP_BASE <= c["va"] < D2R_DUMP_END],
        key=lambda c: c["va"],
    )
    print("[*] raw d2r chunks:", len(chunks_all))

    with open(DUMP_BIN, "rb") as f:
        blob = f.read()

    # Merge contiguous 4KB pages into big virtual blobs so switch patterns
    # don't get chopped at page boundaries.
    chunks = []
    cur = None
    cur_buf = bytearray()
    for c in chunks_all:
        if cur is not None and c["va"] == cur["va"] + len(cur_buf):
            cur_buf += blob[c["file_off"]:c["file_off"]+c["size"]]
        else:
            if cur is not None:
                chunks.append({"va": cur["va"], "size": len(cur_buf), "bytes": bytes(cur_buf)})
            cur = c
            cur_buf = bytearray(blob[c["file_off"]:c["file_off"]+c["size"]])
    if cur is not None:
        chunks.append({"va": cur["va"], "size": len(cur_buf), "bytes": bytes(cur_buf)})
    print("[*] merged chunks:", len(chunks))
    print("    total code bytes:", sum(c["size"] for c in chunks))

    md = capstone.Cs(capstone.CS_ARCH_X86, capstone.CS_MODE_64)
    md.detail = True
    X86 = capstone.x86

    hits = []
    scanned = 0
    for ci, c in enumerate(chunks):
        va   = c["va"]
        size = c["size"]
        code = c["bytes"]
        scanned += size

        # Sliding window: start capstone disasm at each aligned 0x10 offset
        # (function alignment). For every starting point, disasm up to 32 ins
        # and look for the switch pattern. This is slow but we only run once.
        start = 0
        while start < len(code):
            # Fast pre-filter: need `movzx` (0f b6 or 0f b7) somewhere within 64b
            # and `jmp rax` / `jmp r??` within 200b. Bail on any window missing
            # these.
            window = code[start:start+0x300]
            if len(window) < 40:
                break
            if b"\x0f\xb6" not in window[:0x100] and b"\x0f\xb7" not in window[:0x100]:
                start += 0x80
                continue
            # disasm the window
            insns = list(md.disasm(window, va + start))
            if not insns:
                start += 0x10
                continue
            # Walk insns, looking for pattern sequence
            for i in range(len(insns) - 6):
                movzx = insns[i]
                if movzx.mnemonic != "movzx":
                    continue
                if "byte ptr" not in movzx.op_str:
                    continue
                # search next 14 insns for cmp reg,imm  with imm in 0x05..0x2FF
                cmp_imm = None
                cmp_idx = None
                for j in range(i + 1, min(i + 15, len(insns))):
                    ins = insns[j]
                    if ins.mnemonic == "cmp":
                        if len(ins.operands) == 2 and ins.operands[1].type == X86.X86_OP_IMM:
                            v = ins.operands[1].imm
                            if 0x04 <= v <= 0x2FF:
                                cmp_imm = v
                                cmp_idx = j
                                break
                if cmp_imm is None:
                    continue
                # search after cmp for lea reg,[rip+disp]  then movsxd [.*4]  then jmp reg
                lea_va = None
                movsxd_ok = False
                jmp_reg = False
                for k in range(cmp_idx + 1, min(cmp_idx + 12, len(insns))):
                    ins = insns[k]
                    if (ins.mnemonic == "lea"
                            and len(ins.operands) == 2
                            and ins.operands[1].type == X86.X86_OP_MEM
                            and ins.operands[1].mem.base == X86.X86_REG_RIP):
                        disp = ins.operands[1].mem.disp
                        lea_va = ins.address + ins.size + disp
                    elif (ins.mnemonic == "movsxd"
                            and "*4" in ins.op_str):
                        movsxd_ok = True
                    elif (ins.mnemonic == "jmp"
                            and len(ins.operands) == 1
                            and ins.operands[0].type == X86.X86_OP_REG):
                        jmp_reg = True
                        break
                if lea_va and movsxd_ok and jmp_reg:
                    hits.append({
                        "movzx_va":  hex(movzx.address),
                        "movzx_str": movzx.mnemonic + " " + movzx.op_str,
                        "cmp_imm":   cmp_imm,
                        "cmp_va":    hex(insns[cmp_idx].address),
                        "table_va":  hex(lea_va),
                        "table_rva": hex(lea_va - D2R_DUMP_BASE),
                        "head_rva":  hex(movzx.address - D2R_DUMP_BASE),
                    })
            # advance
            start += 0x80
        if (ci + 1) % 2000 == 0:
            print("  [%d/%d] hits=%d" % (ci + 1, len(chunks), len(hits)))

    print("[*] total hits:", len(hits))
    # Deduplicate by movzx_va
    seen = set()
    uniq = []
    for h in hits:
        if h["movzx_va"] in seen:
            continue
        seen.add(h["movzx_va"])
        uniq.append(h)
    print("[*] unique hits:", len(uniq))
    # Sort by cmp_imm desc (fat switches first)
    uniq.sort(key=lambda h: -h["cmp_imm"])

    with open(OUT, "w") as f:
        json.dump({"total": len(uniq), "hits": uniq}, f, indent=2)
    print("[+] wrote", OUT)

    print("\nTop 30 by switch size:")
    for h in uniq[:30]:
        print("  imm=0x%03x  head_rva=%-10s  table_rva=%-10s  movzx=%s" %
              (h["cmp_imm"], h["head_rva"], h["table_rva"], h["movzx_str"]))


if __name__ == "__main__":
    main()
