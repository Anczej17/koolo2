#!/usr/bin/env python3
"""
Audit F1 - find all code sites that reference the Game NetMan global instance
(D2R + 0x1A0D8C0) or its vtable (D2R + 0x1722B00). These are the functions
that drive the NetMan from outside - one of them must be the packet pump /
dispatcher that peeks packets (vtable slot 0 or 1) and routes by opcode.

Strategy: scan live executable pages in D2R.exe for either:
  - `lea reg, [rip + disp]` where disp resolves to one of the target RVAs
  - `call qword ptr [rip + disp]` likewise (for the dispatch table)
and for every hit, disasm ~40 instructions of context, save the VA.
"""
import argparse
import ctypes
import ctypes.wintypes as wt
import json
import sys

import capstone

PAGE_NOACCESS = 0x01
PAGE_GUARD    = 0x100

kernel32 = ctypes.WinDLL('kernel32', use_last_error=True)
psapi    = ctypes.WinDLL('psapi',    use_last_error=True)

kernel32.OpenProcess.restype  = wt.HANDLE
kernel32.OpenProcess.argtypes = [wt.DWORD, wt.BOOL, wt.DWORD]
kernel32.ReadProcessMemory.restype = wt.BOOL
kernel32.ReadProcessMemory.argtypes = [wt.HANDLE, ctypes.c_void_p, ctypes.c_void_p,
                                       ctypes.c_size_t, ctypes.POINTER(ctypes.c_size_t)]

class MBI(ctypes.Structure):
    _fields_ = [("BaseAddress", ctypes.c_void_p),("AllocationBase", ctypes.c_void_p),
                ("AllocationProtect", wt.DWORD),("PartitionId", wt.WORD),("__pad", wt.WORD),
                ("RegionSize", ctypes.c_size_t),("State", wt.DWORD),("Protect", wt.DWORD),("Type", wt.DWORD)]
kernel32.VirtualQueryEx.restype  = ctypes.c_size_t
kernel32.VirtualQueryEx.argtypes = [wt.HANDLE, ctypes.c_void_p, ctypes.POINTER(MBI), ctypes.c_size_t]


def rpm(h, addr, n):
    buf = (ctypes.c_ubyte * n)()
    got = ctypes.c_size_t(0)
    ok = kernel32.ReadProcessMemory(h, ctypes.c_void_p(addr), buf, n, ctypes.byref(got))
    if not ok or got.value == 0:
        return None
    return bytes(buf[:got.value])


def find_d2r(h):
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
    for i in range(n):
        buf = ctypes.create_unicode_buffer(260)
        psapi.GetModuleBaseNameW(h, mods[i], buf, 260)
        if buf.value.lower() == "d2r.exe":
            mi = MI()
            psapi.GetModuleInformation(h, mods[i], ctypes.byref(mi), ctypes.sizeof(mi))
            return mi.BaseOfDll, mi.SizeOfImage
    return None, None


def exec_regions(h, base, size):
    regions = []
    p = base
    end = base + size
    while p < end:
        mbi = MBI()
        if kernel32.VirtualQueryEx(h, ctypes.c_void_p(p), ctypes.byref(mbi), ctypes.sizeof(mbi)) == 0:
            break
        b = mbi.BaseAddress or 0
        s = mbi.RegionSize
        if (mbi.State == 0x1000 and mbi.Protect not in (0, PAGE_NOACCESS)
                and not (mbi.Protect & PAGE_GUARD) and mbi.Protect & 0xF0):
            regions.append((b, s))
        p = b + s
    return regions


def read_region(h, base, size, chunk=0x10000):
    out = bytearray(size)
    for off in range(0, size, chunk):
        n = min(chunk, size - off)
        d = rpm(h, base + off, n)
        if d:
            out[off:off+len(d)] = d
    return bytes(out)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--pid", type=int, required=True)
    ap.add_argument("--targets", default="0x1A0D8C0,0x19ED860,0x1722B00,0x1722A68",
                    help="comma-separated RVAs to find xrefs for")
    ap.add_argument("--out", default=r"C:\Users\Administrator\Desktop\Audyt Koolo\koolo2-rebranding\logs\AUDIT_F1_xrefs.json")
    args = ap.parse_args()
    tgt_rvas = [int(x, 16) for x in args.targets.split(",")]

    h = kernel32.OpenProcess(0x0410, False, args.pid)
    if not h:
        print("OpenProcess failed")
        sys.exit(1)
    base, size = find_d2r(h)
    print("[*] D2R.exe base=%016x size=%08x" % (base, size))
    tgt_vas = {base + r: r for r in tgt_rvas}
    print("[*] looking for refs to:")
    for va, r in tgt_vas.items():
        print("    %016x  RVA=0x%X" % (va, r))

    regs = exec_regions(h, base, size)
    print("[*] exec regions: %d" % len(regs))

    md = capstone.Cs(capstone.CS_ARCH_X86, capstone.CS_MODE_64)
    md.detail = True
    X86 = capstone.x86

    hits_by_tgt = {r: [] for r in tgt_rvas}

    for (b, s) in regs:
        buf = read_region(h, b, s)
        # Disasm from the top of the region. This misses functions whose
        # prologue isn't at region start, but MSVC pads with 0xcc to 16b
        # so aligned-start disasm recovers most.
        insns = list(md.disasm(buf, b))
        for ins in insns:
            # Look at every instruction that has a memory operand with
            # base = RIP, and compute the absolute target.
            for op in ins.operands:
                if op.type == X86.X86_OP_MEM and op.mem.base == X86.X86_REG_RIP:
                    tgt = ins.address + ins.size + op.mem.disp
                    if tgt in tgt_vas:
                        rva = tgt_vas[tgt]
                        hits_by_tgt[rva].append({
                            "va": hex(ins.address),
                            "rva": hex(ins.address - base),
                            "mnem": ins.mnemonic,
                            "op_str": ins.op_str,
                        })
                elif op.type == X86.X86_OP_IMM:
                    # Also catch lea of 64-bit abs imms (rare)
                    v = op.imm
                    if v in tgt_vas:
                        rva = tgt_vas[v]
                        hits_by_tgt[rva].append({
                            "va": hex(ins.address),
                            "rva": hex(ins.address - base),
                            "mnem": ins.mnemonic,
                            "op_str": ins.op_str,
                        })

    print("\n[*] Results:")
    for rva, hits in hits_by_tgt.items():
        # dedupe by va
        seen = set(); uniq = []
        for hh in hits:
            if hh["va"] in seen: continue
            seen.add(hh["va"])
            uniq.append(hh)
        print("  RVA 0x%X  -> %d unique refs" % (rva, len(uniq)))
        for hh in uniq[:20]:
            print("    %s  %s %s" % (hh["rva"], hh["mnem"], hh["op_str"]))
        hits_by_tgt[rva] = uniq

    with open(args.out, "w") as f:
        json.dump({
            "d2r_base": hex(base),
            "targets": {hex(k): hits_by_tgt[k] for k in hits_by_tgt},
        }, f, indent=2)
    print("[+] wrote", args.out)


if __name__ == "__main__":
    main()
