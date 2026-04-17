"""
walk_builder_flat.py — scan flat .text dump for walk packet builders.

Input:  build/dumps/text_live.bin   (8 MB, contiguous, starts at base+0x5C000)
Output: logs/walk_builder_flat.txt

Strategy:
  1. Find every `E8 disp32` and `FF 15 disp32` referencing send_fn (RVA 0x146600).
  2. For each call site, look at 96 bytes BEFORE for indicators of a 5-byte
     walk packet:
       - `BA 05 00 00 00`     mov edx, 5
       - `xor edx,edx; mov dl, 5`  (33 D2 B2 05)
       - `41 B8 05 00 00 00`  mov r8d, 5  (size in r8 instead?)
  3. Also classify all callers by which `mov edx, imm32` value precedes them.
  4. Report enclosing function (walk back to function prologue or padding).
"""

import struct
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
DUMP = REPO / "build" / "build" / "dumps" / "text_live.bin"
REPORT = REPO / "logs" / "walk_builder_flat.txt"

D2R_IMAGE_BASE = 0x7FF79D3D0000
DUMP_BASE_RVA = 0x5C000  # .text section start in PE
DUMP_BASE_VA = D2R_IMAGE_BASE + DUMP_BASE_RVA  # 0x7FF79D42C000
SEND_FN_RVA = 0x146600
SEND_FN_VA = D2R_IMAGE_BASE + SEND_FN_RVA


def va_to_off(va):
    return va - DUMP_BASE_VA


def off_to_va(off):
    return DUMP_BASE_VA + off


def find_call_sites_to(data, target_va):
    """Find E8/E9 disp32 instructions whose target is target_va."""
    sites = []
    L = len(data) - 5
    target_bytes = target_va
    for i in range(L):
        b = data[i]
        if b == 0xE8 or b == 0xE9:
            disp = struct.unpack_from("<i", data, i + 1)[0]
            site_va = off_to_va(i)
            if site_va + 5 + disp == target_bytes:
                sites.append({"va": site_va, "off": i, "kind": "call" if b == 0xE8 else "jmp"})
    return sites


def find_size_setup(data, off, win_back=96):
    """Look for size=N setup instructions in the window before `off`.
    Returns the detected size or None."""
    start = max(0, off - win_back)
    window = data[start:off]
    # Pattern A: BA imm32  (mov edx, imm32)  → BA followed by 4 bytes
    for i in range(len(window) - 4):
        if window[i] == 0xBA:
            val = struct.unpack_from("<I", window, i + 1)[0]
            if val < 0x10000:  # plausible packet size
                return ("BA imm32 (mov edx)", val)
    # Pattern B: 41 B8 imm32  (mov r8d, imm32)
    for i in range(len(window) - 5):
        if window[i] == 0x41 and window[i + 1] == 0xB8:
            val = struct.unpack_from("<I", window, i + 2)[0]
            if val < 0x10000:
                return ("41 B8 imm32 (mov r8d)", val)
    # Pattern C: B2 imm8  (mov dl, imm8)
    for i in range(len(window) - 1):
        if window[i] == 0xB2:
            return ("B2 imm8 (mov dl)", window[i + 1])
    return None


def find_opcode_store(data, off, win_back=96):
    """Look for `C6 /m imm8` byte stores in the window. Return list of (rel_off, imm)."""
    start = max(0, off - win_back)
    window = data[start:off]
    hits = []
    for i in range(len(window) - 2):
        if window[i] == 0xC6:
            modrm = window[i + 1]
            mod = (modrm >> 6) & 3
            rm = modrm & 7
            if mod == 3:
                continue
            ins_len = 2
            if rm == 4:
                ins_len += 1  # SIB
            if mod == 0 and rm == 5:
                ins_len += 4
            elif mod == 1:
                ins_len += 1
            elif mod == 2:
                ins_len += 4
            if i + ins_len < len(window):
                hits.append((i, window[i + ins_len]))
    return hits


def walk_back_to_fn_start(data, off, max_back=0x1000):
    """Walk backwards looking for function start. Heuristic: 1+ CC byte then
    a Win64 prologue (push reg / mov [rsp+..], reg / sub rsp,..)."""
    cur = off
    end = max(0, off - max_back)
    while cur > end:
        if data[cur - 1] == 0xCC:
            # Skip CC padding, return next non-CC offset
            while cur > 0 and data[cur - 1] == 0xCC:
                cur -= 1
            return cur
        cur -= 1
    return None


def main():
    print(f"[load] {DUMP}")
    data = DUMP.read_bytes()
    print(f"[load] {len(data)/1024/1024:.1f} MB at base 0x{DUMP_BASE_VA:X}")

    print(f"[scan] target send_fn = 0x{SEND_FN_VA:X}")
    sites = find_call_sites_to(data, SEND_FN_VA)
    print(f"[scan] found {len(sites)} call/jmp sites to send_fn")

    walk_candidates = []
    for s in sites:
        size_info = find_size_setup(data, s["off"])
        opcode_hits = find_opcode_store(data, s["off"])
        s["size_info"] = size_info
        s["opcode_hits"] = opcode_hits
        s["fn_start_off"] = walk_back_to_fn_start(data, s["off"])
        if size_info and size_info[1] == 5:
            walk_candidates.append(s)

    lines = []
    lines.append("=== walk_builder_flat ===")
    lines.append(f"target send_fn = 0x{SEND_FN_VA:X} (RVA 0x{SEND_FN_RVA:X})")
    lines.append(f"dump base      = 0x{DUMP_BASE_VA:X}")
    lines.append(f"dump size      = {len(data)/1024/1024:.1f} MB")
    lines.append(f"total send_fn callers: {len(sites)}")
    lines.append(f"WALK CANDIDATES (size=5): {len(walk_candidates)}")
    lines.append("")

    if walk_candidates:
        lines.append("=== WALK CANDIDATES ===")
        for s in walk_candidates:
            site_rva = s["va"] - D2R_IMAGE_BASE
            fn_off = s["fn_start_off"]
            fn_va = off_to_va(fn_off) if fn_off else 0
            fn_rva = (fn_va - D2R_IMAGE_BASE) if fn_va else 0
            lines.append(f"  call @ 0x{s['va']:X}  RVA 0x{site_rva:X}  fn @ RVA 0x{fn_rva:X}")
            lines.append(f"    size_info: {s['size_info']}")
            lines.append(f"    opcode stores: {[(h[0], hex(h[1])) for h in s['opcode_hits']]}")
            # Dump 96B before + 32B after
            ctx_start = max(0, s["off"] - 96)
            ctx = data[ctx_start:s["off"] + 32]
            lines.append(f"    bytes: {ctx.hex()}")
            lines.append("")

    # Show all callers with their size info for context
    lines.append("=== ALL send_fn callers ===")
    for s in sites:
        site_rva = s["va"] - D2R_IMAGE_BASE
        si = s.get("size_info")
        size_str = f"{si[0]}={si[1]}" if si else "no-size"
        op_count = len(s.get("opcode_hits", []))
        lines.append(f"  call @ 0x{s['va']:X} RVA 0x{site_rva:X}  {size_str}  opcode_stores={op_count}")

    REPORT.write_text("\n".join(lines))
    print(f"[done] {REPORT}")
    print(f"[done] {len(walk_candidates)} walk candidates")


if __name__ == "__main__":
    main()
