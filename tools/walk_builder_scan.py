"""
walk_builder_scan.py — find all call sites to D2R send_fn (RVA 0x146600) and
classify them by what byte-literal they push as the packet opcode in the ~96
bytes before the call. The goal: locate walk-packet builders (opcode 0x01 or
0x03) and the high-level "request_walk" function that wraps them.

Inputs:
  logs/d2r_exec.bin
  logs/d2r_exec.json  (chunks: [{va, file_off, size}, ...])

Output:
  logs/walk_builder_scan.txt

Usage:
  python tools/walk_builder_scan.py
"""

import json
import os
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
DUMP_BIN = REPO / "logs" / "d2r_exec.bin"
DUMP_JSON = REPO / "logs" / "d2r_exec.json"
REPORT = REPO / "logs" / "walk_builder_scan.txt"

# D2R base address from inspection of the dump (lowest VA in 0x7FF7...)
# .text starts at base + 0x5C000; image base derived by stripping that.
SEND_FN_RVA = 0x146600  # send_fn (friendly wrapper around vtable[6])
D2R_IMAGE_BASE = 0x7FF79D3D0000  # observed in earlier session


def load_dump():
    print(f"[load] {DUMP_JSON}", flush=True)
    meta = json.loads(DUMP_JSON.read_text())
    chunks = meta["chunks"]
    print(f"[load] {DUMP_BIN}", flush=True)
    data = DUMP_BIN.read_bytes()
    # filter to D2R range
    d2r = [c for c in chunks if 0x7FF700000000 <= c["va"] < 0x7FF800000000]
    print(f"[load] D2R chunks: {len(d2r)}, total {sum(c['size'] for c in d2r)/1024/1024:.1f} MB")
    return data, d2r


def chunk_at(va, d2r):
    for c in d2r:
        if c["va"] <= va < c["va"] + c["size"]:
            return c
    return None


def read_at(va, n, data, d2r):
    """Read n bytes at virtual address va, walking across chunk boundaries."""
    out = bytearray()
    cur = va
    remaining = n
    while remaining > 0:
        c = chunk_at(cur, d2r)
        if c is None:
            return None
        offset_in_chunk = cur - c["va"]
        avail = c["size"] - offset_in_chunk
        take = min(avail, remaining)
        file_off = c["file_off"] + offset_in_chunk
        out.extend(data[file_off:file_off + take])
        cur += take
        remaining -= take
    return bytes(out)


def find_call_sites(data, d2r, target_va):
    """Find all `E8 disp32` and `E9 disp32` (call/jmp rel32) targeting target_va."""
    sites = []
    for c in d2r:
        chunk_data = data[c["file_off"]:c["file_off"] + c["size"]]
        base = c["va"]
        # Walk byte-by-byte. Naive but fine for <10 MB.
        L = len(chunk_data) - 5
        i = 0
        while i < L:
            b = chunk_data[i]
            if b == 0xE8 or b == 0xE9:
                disp = int.from_bytes(chunk_data[i + 1:i + 5], "little", signed=True)
                next_ip = base + i + 5
                if next_ip + disp == target_va:
                    sites.append({
                        "va": base + i,
                        "kind": "call" if b == 0xE8 else "jmp",
                    })
            i += 1
    return sites


# Heuristic patterns for "writes byte 0x01 (or 0x03) to a packet buffer"
# We look for a literal opcode byte being moved into memory in the ~96 bytes
# before each send_fn call.
def has_size_5_setup(window):
    """Look for `BA 05 00 00 00` (mov edx, 5) within window — strong indicator
    of a 5-byte walk/run packet send_fn(pkt, 5, 0)."""
    return b"\xBA\x05\x00\x00\x00" in window


def opcode_in_window(window, opcodes=(0x01, 0x03)):
    """Return list of (offset, opcode, kind) for any byte-store with literal opcode."""
    hits = []
    L = len(window)
    for i in range(L):
        b = window[i]
        # Pattern A: C6 /m imm8     mov byte ptr [rXX], imm8
        # opcode C6 takes a ModR/M and 1-byte imm.
        if b == 0xC6 and i + 2 < L:
            modrm = window[i + 1]
            mod = (modrm >> 6) & 3
            rm = modrm & 7
            # Compute total instruction length to find the imm byte
            # mod=00, no SIB, no disp (unless rm==4 SIB or rm==5 disp32)
            # mod=01, 1B disp; mod=10, 4B disp; mod=11, register-direct (skip)
            if mod == 3:
                pass
            else:
                ins_len = 2  # opcode + modrm
                if rm == 4 and i + 2 < L:
                    ins_len += 1  # SIB
                if mod == 0 and rm == 5:
                    ins_len += 4  # disp32
                elif mod == 1:
                    ins_len += 1
                elif mod == 2:
                    ins_len += 4
                if i + ins_len < L:
                    imm = window[i + ins_len]
                    if imm in opcodes:
                        hits.append((i, imm, "C6"))
        # Pattern B: B0 imm8        mov al, imm8  (followed soon by mov [..], al)
        if b == 0xB0 and i + 1 < L:
            imm = window[i + 1]
            if imm in opcodes:
                hits.append((i, imm, "B0_al"))
        # Pattern C: 6A imm8        push imm8
        if b == 0x6A and i + 1 < L:
            imm = window[i + 1]
            if imm in opcodes:
                hits.append((i, imm, "push"))
    return hits


def hex_window(window, max_len=64):
    return window[-max_len:].hex()


def classify_call_site(site_va, data, d2r):
    """Read 96B before the call, look for opcode literal patterns AND size=5 setup."""
    window_size = 96
    start = site_va - window_size
    window = read_at(start, window_size, data, d2r)
    if window is None:
        return None
    hits = opcode_in_window(window)
    return {
        "site_va": site_va,
        "hits": hits,
        "size5": has_size_5_setup(window),
        "window_hex": window.hex(),
    }


def find_function_start(va, data, d2r, max_back=0x800):
    """Walk backwards looking for a function prologue. Returns VA or None.

    Heuristics:
      - 48 89 5C 24 ??     mov [rsp+??], rbx    (very common Win64 prologue)
      - 48 83 EC ??        sub rsp, imm8
      - 40 53              push rbx
      - CC CC CC ...       padding before function
    """
    cur = va
    for _ in range(max_back):
        b3 = read_at(cur - 3, 3, data, d2r)
        if b3 is None:
            return None
        # Padding before function (3 CCs in a row often precedes a func)
        if b3 == b"\xCC\xCC\xCC":
            return cur
        cur -= 1
    return None


def main():
    data, d2r = load_dump()
    target = D2R_IMAGE_BASE + SEND_FN_RVA
    print(f"[scan] target send_fn = 0x{target:X}")
    sites = find_call_sites(data, d2r, target)
    print(f"[scan] found {len(sites)} call/jmp sites to send_fn")

    # Classify each site
    results = []
    for s in sites:
        cls = classify_call_site(s["va"], data, d2r)
        if cls is None:
            continue
        s["hits"] = cls["hits"]
        s["size5"] = cls["size5"]
        s["window_hex"] = cls["window_hex"]
        s["fn_start"] = find_function_start(s["va"], data, d2r)
        results.append(s)

    # Walk-builder = call site with size=5 in setup window (5-byte walk packet)
    walk_builders = [r for r in results if r.get("size5")]

    # Group walk_builders by enclosing function start
    by_fn = {}
    for r in walk_builders:
        k = r["fn_start"] or 0
        by_fn.setdefault(k, []).append(r)

    lines = []
    lines.append("=== walk_builder_scan ===")
    lines.append(f"target send_fn   = 0x{target:X} (RVA 0x{SEND_FN_RVA:X})")
    lines.append(f"D2R image base   = 0x{D2R_IMAGE_BASE:X}")
    lines.append(f"total send_fn callers (any opcode): {len(results)}")
    lines.append(f"walk-builder candidates (opcode 01 or 03): {len(walk_builders)}")
    lines.append(f"unique enclosing functions: {len(by_fn)}")
    lines.append("")

    # Print all callers grouped by enclosing function
    for fn_va, items in sorted(by_fn.items()):
        rva = fn_va - D2R_IMAGE_BASE if fn_va else 0
        lines.append(f"--- FN @ 0x{fn_va:X}  (RVA 0x{rva:X})  callers={len(items)}")
        for it in items:
            site_rva = it["va"] - D2R_IMAGE_BASE
            opcodes_seen = sorted({h[1] for h in it["hits"] if h[1] in (0x01, 0x03)})
            lines.append(
                f"  call @ 0x{it['va']:X} (RVA 0x{site_rva:X})  "
                f"opcodes_in_window={opcodes_seen}  hits={len(it['hits'])}"
            )
            lines.append(f"    window: {it['window_hex']}")
        lines.append("")

    # Also print a summary of all 0x01 send sites that are NOT walk packet candidates,
    # so we can see if there's something else
    lines.append("=== ALL send_fn callers (top 20) ===")
    for r in results:
        rva = r["va"] - D2R_IMAGE_BASE
        ops = [(h[0], hex(h[1]), h[2]) for h in r["hits"]]
        lines.append(f"  call @ 0x{r['va']:X} (RVA 0x{rva:X})  hits={ops}")

    REPORT.write_text("\n".join(lines))
    print(f"[done] report -> {REPORT}")
    print(f"[done] {len(walk_builders)} walk-builder candidates in {len(by_fn)} functions")


if __name__ == "__main__":
    main()
