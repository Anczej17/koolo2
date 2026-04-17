#!/usr/bin/env python3
"""
Audit F1 - live scan of the D2R.exe process for opcode-switch patterns.

Unlike netman_switch_scan.py this reads straight out of the running process
so we get the FULL .text / .rdata without gaps from the old exec dump.

Three pattern classes we hunt for in every D2R.exe committed page:

  (A) MSVC dense dword-offset table:
        movzx eax, byte ptr [??]    ; or after sub
        cmp   eax, <imm>
        ja    <def>
        lea   rcx, [rip + T_A]
        movsxd rax, dword ptr [rcx + rax*4]
        add   rax, rcx
        jmp   rax
  (B) qword ptr table:
        movzx eax, byte ptr [??]
        cmp   eax, <imm>
        ja    <def>
        lea   rcx, [rip + T_B]
        jmp   qword ptr [rcx + rax*8]
  (C) reg-direct call table:
        movzx eax, byte ptr [??]
        cmp   eax, <imm>
        ja    <def>
        lea   rcx, [rip + T_C]
        call  qword ptr [rcx + rax*8]

Outputs:
  logs/AUDIT_F1_live_hits.json  - raw list ordered by cmp_imm desc
"""
from __future__ import annotations
import argparse
import ctypes
import ctypes.wintypes as wt
import json
import struct
import sys

import capstone

PAGE_NOACCESS = 0x01
PAGE_GUARD    = 0x100
EXEC_PROTS = (0x10, 0x20, 0x40, 0x80)

kernel32 = ctypes.WinDLL('kernel32', use_last_error=True)
psapi    = ctypes.WinDLL('psapi',    use_last_error=True)

kernel32.OpenProcess.restype  = wt.HANDLE
kernel32.OpenProcess.argtypes = [wt.DWORD, wt.BOOL, wt.DWORD]
kernel32.ReadProcessMemory.restype  = wt.BOOL
kernel32.ReadProcessMemory.argtypes = [wt.HANDLE, ctypes.c_void_p, ctypes.c_void_p,
                                       ctypes.c_size_t, ctypes.POINTER(ctypes.c_size_t)]

class MBI(ctypes.Structure):
    _fields_ = [("BaseAddress", ctypes.c_void_p),
                ("AllocationBase", ctypes.c_void_p),
                ("AllocationProtect", wt.DWORD),
                ("PartitionId", wt.WORD),
                ("__pad", wt.WORD),
                ("RegionSize", ctypes.c_size_t),
                ("State", wt.DWORD),
                ("Protect", wt.DWORD),
                ("Type", wt.DWORD)]

kernel32.VirtualQueryEx.restype  = ctypes.c_size_t
kernel32.VirtualQueryEx.argtypes = [wt.HANDLE, ctypes.c_void_p,
                                    ctypes.POINTER(MBI), ctypes.c_size_t]

def rpm(h, addr, n):
    buf = (ctypes.c_ubyte * n)()
    got = ctypes.c_size_t(0)
    ok = kernel32.ReadProcessMemory(h, ctypes.c_void_p(addr), buf, n, ctypes.byref(got))
    if not ok or got.value == 0:
        return None
    return bytes(buf[:got.value])


def find_d2r_base_and_exec_regions(h):
    HMODULE = ctypes.c_void_p
    mods = (HMODULE * 2048)()
    cb = ctypes.c_ulong(0)
    psapi.EnumProcessModulesEx.argtypes = [wt.HANDLE, ctypes.POINTER(HMODULE), wt.DWORD, ctypes.POINTER(wt.DWORD), wt.DWORD]
    psapi.GetModuleBaseNameW.argtypes = [wt.HANDLE, HMODULE, wt.LPWSTR, wt.DWORD]
    class MI(ctypes.Structure):
        _fields_ = [('BaseOfDll', ctypes.c_void_p),('SizeOfImage', ctypes.c_ulong),('EntryPoint', ctypes.c_void_p)]
    psapi.GetModuleInformation.argtypes = [wt.HANDLE, HMODULE, ctypes.POINTER(MI), wt.DWORD]
    psapi.EnumProcessModulesEx(h, mods, ctypes.sizeof(mods), ctypes.byref(cb), 0x03)
    n = cb.value // ctypes.sizeof(HMODULE)
    base = None
    size = None
    for i in range(n):
        buf = ctypes.create_unicode_buffer(260)
        psapi.GetModuleBaseNameW(h, mods[i], buf, 260)
        if buf.value.lower() == "d2r.exe":
            mi = MI()
            psapi.GetModuleInformation(h, mods[i], ctypes.byref(mi), ctypes.sizeof(mi))
            base, size = mi.BaseOfDll, mi.SizeOfImage
            break
    if base is None:
        return None, None, []
    # Walk the image range and keep executable + committed pages
    regions = []
    p = base
    end = base + size
    while p < end:
        mbi = MBI()
        got = kernel32.VirtualQueryEx(h, ctypes.c_void_p(p), ctypes.byref(mbi), ctypes.sizeof(mbi))
        if got == 0:
            break
        b = mbi.BaseAddress or 0
        s = mbi.RegionSize
        if (mbi.State == 0x1000
                and mbi.Protect not in (0, PAGE_NOACCESS)
                and not (mbi.Protect & PAGE_GUARD)
                and mbi.Protect & 0xF0):
            regions.append((b, s, mbi.Protect))
        p = b + s
    return base, size, regions


def read_region(h, base, size, chunk=0x10000):
    out = bytearray(size)
    for off in range(0, size, chunk):
        n = min(chunk, size - off)
        d = rpm(h, base + off, n)
        if d:
            out[off:off+len(d)] = d
    return bytes(out)


def scan_blob(md, va_base, code):
    X86 = capstone.x86
    hits = []
    # Pre-filter using byte signature of movzx r32, byte ptr ...: 0F B6
    # We attempt disasm at each 0F B6 position.
    i = 0
    L = len(code)
    while True:
        j = code.find(b"\x0f\xb6", i)
        if j < 0:
            break
        # Disasm a window starting from j
        insns = list(md.disasm(code[j:j+0x180], va_base + j))
        if insns:
            # Check first insn is movzx byte ptr
            first = insns[0]
            if first.mnemonic == "movzx" and "byte ptr" in first.op_str:
                # Find cmp imm within next 14 ins
                cmp_imm = None
                cmp_idx = None
                for k in range(1, min(15, len(insns))):
                    ins = insns[k]
                    if ins.mnemonic == "cmp":
                        if len(ins.operands) == 2 and ins.operands[1].type == X86.X86_OP_IMM:
                            v = ins.operands[1].imm
                            if 4 <= v <= 0x1FF:
                                cmp_imm = v
                                cmp_idx = k
                                break
                    if ins.mnemonic in ("call", "ret", "jmp"):
                        break
                if cmp_imm is not None:
                    # Search for (lea rip+) and (jmp/call/movsxd ..*4 or *8)
                    lea_tgt = None
                    pattern_type = None
                    for k in range(cmp_idx + 1, min(cmp_idx + 12, len(insns))):
                        ins = insns[k]
                        if (ins.mnemonic == "lea"
                                and len(ins.operands) == 2
                                and ins.operands[1].type == X86.X86_OP_MEM
                                and ins.operands[1].mem.base == X86.X86_REG_RIP):
                            lea_tgt = ins.address + ins.size + ins.operands[1].mem.disp
                        if (ins.mnemonic == "movsxd" and "*4" in ins.op_str):
                            pattern_type = "A"
                        if (ins.mnemonic == "jmp" and "*8" in ins.op_str and "[" in ins.op_str):
                            pattern_type = "B"
                        if (ins.mnemonic == "call" and "*8" in ins.op_str and "[" in ins.op_str):
                            pattern_type = "C"
                        if ins.mnemonic in ("ret",):
                            break
                    if lea_tgt is not None and pattern_type is not None:
                        hits.append({
                            "pattern": pattern_type,
                            "head_va": hex(first.address),
                            "head_str": first.mnemonic + " " + first.op_str,
                            "cmp_imm": cmp_imm,
                            "table_va": hex(lea_tgt),
                        })
        i = j + 2
    return hits


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--pid", type=int, required=True)
    ap.add_argument("--out", default=r"C:\Users\Administrator\Desktop\Audyt Koolo\koolo2-rebranding\logs\AUDIT_F1_live_hits.json")
    args = ap.parse_args()

    h = kernel32.OpenProcess(0x0410, False, args.pid)
    if not h:
        print("OpenProcess failed")
        sys.exit(1)

    base, size, regions = find_d2r_base_and_exec_regions(h)
    print("[*] D2R.exe base=%016x size=%08x   exec regions=%d" %
          (base, size, len(regions)))
    for (b, s, p) in regions:
        print("    %016x %08x  prot=0x%x" % (b, s, p))

    md = capstone.Cs(capstone.CS_ARCH_X86, capstone.CS_MODE_64)
    md.detail = True

    all_hits = []
    for (b, s, p) in regions:
        print("[*] reading region %016x size %08x" % (b, s))
        buf = read_region(h, b, s)
        hits = scan_blob(md, b, buf)
        print("    %d hits" % len(hits))
        for hh in hits:
            hh["rva"] = hex(int(hh["head_va"], 16) - base)
            hh["table_rva"] = hex(int(hh["table_va"], 16) - base)
        all_hits.extend(hits)

    # Deduplicate
    seen = set()
    uniq = []
    for hh in all_hits:
        if hh["head_va"] in seen: continue
        seen.add(hh["head_va"])
        uniq.append(hh)
    uniq.sort(key=lambda hh: -hh["cmp_imm"])

    with open(args.out, "w") as f:
        json.dump({
            "d2r_base": hex(base),
            "d2r_size": size,
            "n_hits": len(uniq),
            "hits": uniq,
        }, f, indent=2)
    print("\n[+] unique hits: %d -> %s" % (len(uniq), args.out))

    print("\nTop 40 by switch size (fattest = most likely opcode dispatch):")
    for hh in uniq[:40]:
        print("  %s imm=0x%03x  rva=%-10s  table_rva=%-10s  %s" %
              (hh["pattern"], hh["cmp_imm"], hh["rva"], hh["table_rva"], hh["head_str"]))


if __name__ == "__main__":
    main()
