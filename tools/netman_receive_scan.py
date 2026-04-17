#!/usr/bin/env python3
"""
Audit F1 - NetMan incoming packet dispatcher/receiver hunt.

Reads required data live from the running D2R.exe (vtables live in .rdata,
instances in .data - neither is in the logs/d2r_exec.bin executable-only dump).

Steps:
  1. Open D2R by pid, query module base.
  2. Read Game NetMan instance  @ 0x7ff79edbd8c0   -> vtable pointer -> 16 slots
     Read UI  NetMan instance   @ 0x7ff79edbd860   -> vtable pointer -> 16 slots
     Also dump a window of nearby globals around both instances so a separate
     "server-side" NetMan can be spotted.
  3. For every vtable slot function, disassemble the first ~512 bytes via
     capstone and run pattern heuristics for an opcode dispatcher:
       - switch/jumptable indexed by byte [rdx] or movzx eax,byte ptr [...]
       - long cmp/jcc ladder on a byte value
       - large jump table in rdata referenced by `lea rcx,[rip+disp]; jmp [rcx+rax*8]`
  4. Also scan every reachable function from the vtable slot's CFG for the
     same dispatcher patterns (walk up to depth 3 calls).
  5. Write raw results to logs/AUDIT_F1_raw.json for the Ghidra follow-up
     script to pick winners and do proper decompilation.

Usage:
  python tools/netman_receive_scan.py --pid <d2r pid>
"""
from __future__ import annotations
import argparse
import ctypes
import ctypes.wintypes as wt
import json
import os
import struct
import sys

import capstone

PROCESS_VM_READ           = 0x0010
PROCESS_QUERY_INFORMATION = 0x0400

PAGE_NOACCESS = 0x01
PAGE_GUARD    = 0x100

kernel32 = ctypes.WinDLL('kernel32', use_last_error=True)
psapi    = ctypes.WinDLL('psapi',    use_last_error=True)

OpenProcess = kernel32.OpenProcess
OpenProcess.restype  = wt.HANDLE
OpenProcess.argtypes = [wt.DWORD, wt.BOOL, wt.DWORD]

CloseHandle = kernel32.CloseHandle
CloseHandle.argtypes = [wt.HANDLE]

ReadProcessMemory = kernel32.ReadProcessMemory
ReadProcessMemory.restype  = wt.BOOL
ReadProcessMemory.argtypes = [wt.HANDLE, ctypes.c_void_p, ctypes.c_void_p,
                              ctypes.c_size_t, ctypes.POINTER(ctypes.c_size_t)]

class MEMORY_BASIC_INFORMATION(ctypes.Structure):
    _fields_ = [
        ("BaseAddress",       ctypes.c_void_p),
        ("AllocationBase",    ctypes.c_void_p),
        ("AllocationProtect", wt.DWORD),
        ("PartitionId",       wt.WORD),
        ("__pad",             wt.WORD),
        ("RegionSize",        ctypes.c_size_t),
        ("State",             wt.DWORD),
        ("Protect",           wt.DWORD),
        ("Type",              wt.DWORD),
    ]

VirtualQueryEx = kernel32.VirtualQueryEx
VirtualQueryEx.restype  = ctypes.c_size_t
VirtualQueryEx.argtypes = [wt.HANDLE, ctypes.c_void_p,
                           ctypes.POINTER(MEMORY_BASIC_INFORMATION), ctypes.c_size_t]


# RVAs taken DIRECTLY from prior RE memory notes in the task prompt.
# Absolute VAs cannot be used because ASLR moves the image every launch.
RVA_GAME_NETMAN       = 0x1A0D8C0
RVA_GAME_NETMAN_VT    = 0x1722B00
RVA_UI_NETMAN         = 0x19ED860
RVA_KNOWN_SEND        = 0x146600
RVA_KNOWN_V6          = 0x0E6B50
RVA_SEND_QUEUE        = 0x0E6650
RVA_ON_WIRE           = 0x24B6DD

# Filled in at runtime from EnumProcessModulesEx.
D2R_BASE        = None
D2R_SIZE        = None


def rpm(h, addr, n):
    buf = (ctypes.c_ubyte * n)()
    got = ctypes.c_size_t(0)
    ok = ReadProcessMemory(h, ctypes.c_void_p(addr), buf, n, ctypes.byref(got))
    if not ok or got.value == 0:
        return None
    return bytes(buf[:got.value])


def rpm_safe_window(h, addr, total, chunk=0x1000):
    """Reads [addr, addr+total) page-by-page, substituting zeros on bad pages."""
    out = bytearray(total)
    for off in range(0, total, chunk):
        n = min(chunk, total - off)
        d = rpm(h, addr + off, n)
        if d:
            out[off:off + len(d)] = d
    return bytes(out)


def rpm_qword(h, addr):
    d = rpm(h, addr, 8)
    if d is None or len(d) < 8:
        return None
    return struct.unpack("<Q", d)[0]


def walk_vtable(h, vt_va, n=16):
    slots = []
    for i in range(n):
        p = rpm_qword(h, vt_va + 8*i)
        slots.append(p)
    return slots


def collect_memory_regions(h):
    regions = []
    addr = 0
    mbi = MEMORY_BASIC_INFORMATION()
    while True:
        got = VirtualQueryEx(h, ctypes.c_void_p(addr), ctypes.byref(mbi), ctypes.sizeof(mbi))
        if got == 0:
            break
        base = mbi.BaseAddress or 0
        size = mbi.RegionSize
        prot = mbi.Protect
        state = mbi.State
        if state == 0x1000 and not (prot & PAGE_NOACCESS) and not (prot & PAGE_GUARD) and prot != 0:
            regions.append((base, size, prot))
        addr = base + size
        if addr == 0:
            break
    return regions


def try_read_function(h, va, maxlen=0x1800):
    """Read up to maxlen bytes starting at va, stop at page boundary on fail."""
    return rpm_safe_window(h, va, maxlen, chunk=0x400)


DISP_OPCODES = {
    capstone.x86.X86_INS_JMP,
    capstone.x86.X86_INS_CALL,
    capstone.x86.X86_INS_RET,
}


def analyze_function(md, va, code):
    """Quick heuristic scan for dispatcher patterns in a single function."""
    if code is None or len(code) < 8:
        return {"va": hex(va), "error": "no code"}
    insns = list(md.disasm(code, va))
    if not insns:
        return {"va": hex(va), "error": "disasm failed"}
    # keep only first ~200 insns or until obvious function exit ladder
    body = insns[:400]
    byte_load = False       # movzx rAX, byte ptr [rdx/rcx/...]
    byte_load_detail = None
    cmp_count = 0
    jcc_count = 0
    max_cmp_imm = -1
    jumptable = None        # (lea_disp_abs, jmp_insn_va)
    last_lea = None         # (reg, abs_target, insn_va)
    calls = []
    rets = 0
    for ins in body:
        m = ins.mnemonic
        op = ins.op_str
        if m == "movzx" and "byte ptr" in op and "[r" in op:
            byte_load = True
            if byte_load_detail is None:
                byte_load_detail = "%s  %s" % (m, op)
        if m == "movzx" and "byte ptr" in op and "[r" in op:
            pass
        if m == "cmp":
            cmp_count += 1
            # cmp xxx, imm
            parts = op.split(",")
            if len(parts) == 2:
                rhs = parts[1].strip()
                try:
                    if rhs.startswith("0x"):
                        v = int(rhs, 16)
                        if 0 <= v <= 0xff and v > max_cmp_imm:
                            max_cmp_imm = v
                    elif rhs.isdigit():
                        v = int(rhs)
                        if 0 <= v <= 0xff and v > max_cmp_imm:
                            max_cmp_imm = v
                except Exception:
                    pass
        if m.startswith("j") and m not in ("jmp",):
            jcc_count += 1
        if m == "lea":
            # lea reg, [rip + disp]  — rip-relative table ptr
            parts = op.split(",", 1)
            if len(parts) == 2 and "rip" in parts[1]:
                try:
                    # capstone encodes as "reg, [rip + 0xXXXX]"
                    # operand_size: next instruction address + disp
                    # use ins.operands if available
                    if len(ins.operands) >= 2 and ins.operands[1].type == capstone.x86.X86_OP_MEM:
                        disp = ins.operands[1].mem.disp
                        abs_target = ins.address + ins.size + disp
                        reg = ins.reg_name(ins.operands[0].reg)
                        last_lea = (reg, abs_target, ins.address)
                except Exception:
                    pass
        if m == "jmp":
            # Classic MSVC x64 jumptable patterns:
            #   (A) movsxd rax, dword ptr [reg1 + rax*4]; add rax, reg1; jmp rax
            #   (B) jmp qword ptr [reg + rax*8]
            # We flag either.
            if "ptr [" in op and ("*8" in op or "*4" in op):
                jumptable = (last_lea, ins.address, op)
            elif op.strip() in ("rax", "rcx", "rdx", "r8", "r9", "r10", "r11"):
                # `jmp rax` right after `add rax, rcx` + `movsxd rax, [rcx+rax*4]`
                jumptable = (last_lea, ins.address, "jmp " + op.strip())
        if m == "movsxd" and "[" in op and ("*4" in op or "*8" in op):
            # record that we saw a jumptable index read
            byte_load = byte_load  # no-op; real flag below
            jumptable = (last_lea, ins.address, m + " " + op)
        if m == "call":
            parts = op.split()
            tgt = parts[-1]
            if tgt.startswith("0x"):
                try:
                    calls.append(int(tgt, 16))
                except Exception:
                    pass
        if m == "ret":
            rets += 1
            if rets >= 2 and len(body) > body.index(ins):
                pass
    return {
        "va": hex(va),
        "insn_count": len(body),
        "byte_load": byte_load,
        "byte_load_detail": byte_load_detail,
        "cmp_count": cmp_count,
        "jcc_count": jcc_count,
        "max_cmp_imm": max_cmp_imm,
        "jumptable": (None if jumptable is None else
                      {"lea": (None if jumptable[0] is None else
                               {"reg": jumptable[0][0],
                                "abs_target": hex(jumptable[0][1]),
                                "insn_va": hex(jumptable[0][2])}),
                       "jmp_va": hex(jumptable[1]),
                       "jmp_text": jumptable[2]}),
        "calls": [hex(c) for c in calls[:40]],
    }


def dispatcher_score(a):
    """Higher = more likely opcode dispatcher."""
    if not isinstance(a, dict) or "error" in a:
        return 0
    score = 0
    if a.get("jumptable"):
        score += 50
    if a.get("byte_load"):
        score += 20
    cmps = a.get("cmp_count", 0)
    maxi = a.get("max_cmp_imm", -1)
    if cmps >= 6 and 0x10 <= maxi <= 0xff:
        score += 15 + min(cmps, 30)
    if a.get("jcc_count", 0) >= 8:
        score += 5
    return score


def walk_callees(h, md, root_va, depth=2, fanout=6, seen=None):
    """
    Returns a list of (depth, va) reachable via static direct calls from root_va.
    Extremely coarse - we only follow direct call immediates; we cap fanout so a
    single leaf doesn't explode the search space.
    """
    if seen is None:
        seen = {}
    frontier = [(0, root_va)]
    while frontier:
        d, va = frontier.pop(0)
        if va in seen:
            continue
        if va < D2R_BASE or va > D2R_BASE + 0x4000000:
            continue
        code = try_read_function(h, va, maxlen=0x1800)
        a = analyze_function(md, va, code)
        seen[va] = (d, a)
        if d >= depth:
            continue
        # enqueue direct callees
        for chex in a.get("calls", [])[:fanout]:
            try:
                cva = int(chex, 16)
            except Exception:
                continue
            if cva not in seen:
                frontier.append((d + 1, cva))
    return seen


def resolve_d2r_base(h):
    """Find D2R.exe base + size via EnumProcessModulesEx."""
    HMODULE = ctypes.c_void_p
    mods = (HMODULE * 1024)()
    cb = ctypes.c_ulong(0)
    psapi.EnumProcessModulesEx.argtypes = [wt.HANDLE, ctypes.POINTER(HMODULE),
                                           wt.DWORD, ctypes.POINTER(wt.DWORD), wt.DWORD]
    psapi.GetModuleBaseNameW.argtypes   = [wt.HANDLE, HMODULE, wt.LPWSTR, wt.DWORD]
    class MI(ctypes.Structure):
        _fields_ = [('BaseOfDll', ctypes.c_void_p),
                    ('SizeOfImage', ctypes.c_ulong),
                    ('EntryPoint', ctypes.c_void_p)]
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


def main():
    global D2R_BASE, D2R_SIZE
    ap = argparse.ArgumentParser()
    ap.add_argument("--pid", type=int, required=True)
    ap.add_argument("--out", default=r"C:\Users\Administrator\Desktop\Audyt Koolo\koolo2-rebranding\logs\AUDIT_F1_raw.json")
    args = ap.parse_args()

    h = OpenProcess(PROCESS_VM_READ | PROCESS_QUERY_INFORMATION, False, args.pid)
    if not h:
        print("OpenProcess failed:", ctypes.get_last_error())
        sys.exit(1)

    D2R_BASE, D2R_SIZE = resolve_d2r_base(h)
    if not D2R_BASE:
        print("could not find D2R.exe in process modules")
        sys.exit(1)
    print("[+] D2R.exe base=%016x size=%08x" % (D2R_BASE, D2R_SIZE))

    GAME_NETMAN = D2R_BASE + RVA_GAME_NETMAN
    UI_NETMAN   = D2R_BASE + RVA_UI_NETMAN
    GAME_NETMAN_VT = D2R_BASE + RVA_GAME_NETMAN_VT

    md = capstone.Cs(capstone.CS_ARCH_X86, capstone.CS_MODE_64)
    md.detail = True

    result = {"pid": args.pid, "d2r_base": hex(D2R_BASE)}

    # 1. Read both netman instances (first 0x100 bytes each)
    gm_bytes = rpm(h, GAME_NETMAN, 0x200) or b""
    ui_bytes = rpm(h, UI_NETMAN, 0x200) or b""
    result["game_netman_bytes"] = gm_bytes.hex()
    result["ui_netman_bytes"]   = ui_bytes.hex()
    gm_vt = struct.unpack("<Q", gm_bytes[:8])[0] if len(gm_bytes) >= 8 else None
    ui_vt = struct.unpack("<Q", ui_bytes[:8])[0] if len(ui_bytes) >= 8 else None
    result["game_netman_vt"] = hex(gm_vt) if gm_vt else None
    result["ui_netman_vt"]   = hex(ui_vt) if ui_vt else None
    print("[*] game netman vt =", hex(gm_vt) if gm_vt else "FAIL")
    print("[*] ui   netman vt =", hex(ui_vt) if ui_vt else "FAIL")

    # 2. Walk both vtables (24 slots just to be safe)
    gm_slots = walk_vtable(h, gm_vt, 24) if gm_vt else []
    ui_slots = walk_vtable(h, ui_vt, 24) if ui_vt else []
    result["game_netman_slots"] = [hex(s) if s else None for s in gm_slots]
    result["ui_netman_slots"]   = [hex(s) if s else None for s in ui_slots]

    # 3. Analyze each slot function
    def analyze_slot_set(name, slots):
        out = []
        for i, s in enumerate(slots):
            if not s or s < D2R_BASE or s > D2R_BASE + 0x4000000:
                out.append({"index": i, "va": hex(s) if s else None, "bad": True})
                continue
            code = try_read_function(h, s, maxlen=0x1800)
            a = analyze_function(md, s, code)
            a["index"] = i
            a["hex_head"] = (code[:64].hex() if code else "")
            a["score"] = dispatcher_score(a)
            out.append(a)
            print("  [%s %02d] %016x  score=%d  jt=%s  byteload=%s  cmp=%d  maxImm=%s" %
                  (name, i, s, a["score"],
                   "Y" if a.get("jumptable") else "n",
                   "Y" if a.get("byte_load") else "n",
                   a.get("cmp_count", 0), hex(a.get("max_cmp_imm", -1))))
        return out

    print("\n[*] Game NetMan vtable slots:")
    result["game_slot_analysis"] = analyze_slot_set("G", gm_slots)
    print("\n[*] UI NetMan vtable slots:")
    result["ui_slot_analysis"]   = analyze_slot_set("U", ui_slots)

    # 4. For each slot, walk callees depth 2 - the dispatcher might not BE the
    #    vtable slot itself, it may be one level down (e.g. receive_queue_pop ->
    #    dispatch_packet).
    print("\n[*] Walking callees depth=2 for top scoring slots...")
    all_ranked = []
    def key(s): return s.get("score", 0)
    for set_name, slot_set in (("G", result["game_slot_analysis"]),
                                ("U", result["ui_slot_analysis"])):
        # take ALL slots that aren't obvious bad, not just top N
        for a in slot_set:
            if a.get("bad"):
                continue
            all_ranked.append((set_name, a))
    all_ranked.sort(key=lambda x: -key(x[1]))

    deep = {}
    for set_name, slot in all_ranked[:24]:
        va = int(slot["va"], 16)
        seen = walk_callees(h, md, va, depth=3, fanout=12)
        # keep only scored entries >= 25
        cands = []
        for cva, (d, a) in seen.items():
            s = dispatcher_score(a)
            if s >= 20:
                cands.append((s, d, cva, a))
        cands.sort(key=lambda x: -x[0])
        deep["%s_%d" % (set_name, slot["index"])] = [
            {"score": s, "depth": d, "va": hex(cva),
             "byte_load": a.get("byte_load"),
             "cmp_count": a.get("cmp_count"),
             "max_cmp_imm": a.get("max_cmp_imm"),
             "jumptable": a.get("jumptable")}
            for (s, d, cva, a) in cands[:20]
        ]
        print("  [%s slot %d] va=%s  candidates=%d" %
              (set_name, slot["index"], slot["va"], len(cands)))
    result["deep_candidates"] = deep

    # 5. Dump the data around each NetMan instance so we can eyeball for a
    #    second "server side" NetMan instance (same vtable ptr).
    gm_window = rpm_safe_window(h, (GAME_NETMAN & ~0xFFF) - 0x2000, 0x8000)
    ui_window = rpm_safe_window(h, (UI_NETMAN   & ~0xFFF) - 0x2000, 0x8000)
    # search for other copies of the vtable ptr within ~64k around
    def find_vt_copies(window, base, vt):
        if not vt:
            return []
        needle = struct.pack("<Q", vt)
        hits = []
        i = 0
        while True:
            j = window.find(needle, i)
            if j < 0:
                break
            hits.append(hex(base + j))
            i = j + 8
        return hits
    gm_window_base = (GAME_NETMAN & ~0xFFF) - 0x2000
    ui_window_base = (UI_NETMAN   & ~0xFFF) - 0x2000
    result["game_vt_copies_near_instance"] = find_vt_copies(gm_window, gm_window_base, gm_vt)
    result["ui_vt_copies_near_instance"]   = find_vt_copies(ui_window, ui_window_base, ui_vt)
    print("[*] Game vtable ptr copies near instance:", result["game_vt_copies_near_instance"])
    print("[*] UI   vtable ptr copies near instance:", result["ui_vt_copies_near_instance"])

    # broad scan for vtable ptr copies across ALL committed pages in D2R's
    # data range (this finds server-side NetMan globals if they exist)
    print("[*] Broad scan for vtable copies in D2R data range...")
    def broad_scan(vt, label):
        if not vt:
            return []
        hits = []
        needle = struct.pack("<Q", vt)
        base = D2R_BASE
        end  = D2R_BASE + 0x2A00000  # 42 MB covers .data and BSS plausibly
        pagesz = 0x10000
        p = base
        while p < end:
            buf = rpm(h, p, pagesz)
            if buf:
                i = 0
                while True:
                    j = buf.find(needle, i)
                    if j < 0:
                        break
                    hits.append(p + j)
                    i = j + 8
            p += pagesz
        print("   %s: %d hits" % (label, len(hits)))
        return [hex(x) for x in hits]
    result["game_vt_all_copies"] = broad_scan(gm_vt, "game")
    result["ui_vt_all_copies"]   = broad_scan(ui_vt, "ui")

    CloseHandle(h)
    with open(args.out, "w") as f:
        json.dump(result, f, indent=2)
    print("[+] wrote", args.out)


if __name__ == "__main__":
    main()
