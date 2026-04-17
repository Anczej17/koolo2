"""trace_decode.py — drain PacketTracer ring and pretty-print captures.

Outputs human-readable lines per entry plus a summary list of unique caller
RVAs (top frame). The unique-RVAs list is what you take into Ghidra to find
which D2R function calls send_fn / dual_send_wrap for a specific opcode.

Usage:
    python tools/trace_decode.py --port 49805 --char Blizzard
    python tools/trace_decode.py --port 49805 --watch 5      # poll every 5s
    python tools/trace_decode.py --port 49805 --filter 0x54  # only opcode 0x54

After capture, plug unique RVAs into Ghidra:
    mcp__ghidra__get_function_by_address(address=hex(rva))
"""
from __future__ import annotations

import argparse
import json
import sys
import time
import urllib.request
from collections import Counter, defaultdict
from pathlib import Path


def fetch_dump(port: int, char: str) -> dict:
    url = f"http://127.0.0.1:{port}/debug/packettrace/dump?character={char}"
    with urllib.request.urlopen(url, timeout=15) as r:
        return json.loads(r.read().decode("utf-8", "replace"))


def fetch_status(port: int, char: str) -> dict:
    url = f"http://127.0.0.1:{port}/debug/packettrace/status?character={char}"
    with urllib.request.urlopen(url, timeout=10) as r:
        return json.loads(r.read().decode("utf-8", "replace"))


def install(port: int, char: str) -> dict:
    url = f"http://127.0.0.1:{port}/debug/packettrace/install?character={char}"
    with urllib.request.urlopen(url, timeout=15) as r:
        return json.loads(r.read().decode("utf-8", "replace"))


def uninstall(port: int, char: str) -> dict:
    url = f"http://127.0.0.1:{port}/debug/packettrace/uninstall?character={char}"
    with urllib.request.urlopen(url, timeout=15) as r:
        return json.loads(r.read().decode("utf-8", "replace"))


def fmt_addr(va_str: str, d2r_base: int) -> str:
    """Format an absolute VA as D2R+0xRVA when in range, else mark."""
    try:
        va = int(va_str, 16) if isinstance(va_str, str) else int(va_str)
    except (TypeError, ValueError):
        return str(va_str)
    if (va >> 32) == 0xDEADBEEF:
        return f"OUTSIDE 0x{va & 0xFFFFFFFF:08X}"
    if d2r_base != 0 and va >= d2r_base and va < d2r_base + 0x4000000:
        return f"D2R+0x{va - d2r_base:07X}"
    return f"0x{va:016X}"


def print_entry(e: dict, d2r_base: int, idx: int):
    ts = e.get("ts", 0)
    hook = e.get("hook", "?")
    tid = e.get("tid", 0)
    op = e.get("opcode", "0x00")
    plen = e.get("len", 0)
    args = e.get("args", [])
    cs = e.get("callstack", [])
    payload = e.get("payload", "")
    annot = e.get("annot", "")

    print(f"[#{idx:04d} t={ts}ms tid={tid}] hook={hook} op={op} len={plen}")
    if annot:
        print(f"    annot: {annot}")
    if payload:
        # show first 64 bytes (32 hex chars)
        show = payload[:128]
        print(f"    payload: {show}{'...' if len(payload) > 128 else ''}")
    if args:
        rcx = args[0] if len(args) > 0 else "0"
        rdx = args[1] if len(args) > 1 else "0"
        r8 = args[2] if len(args) > 2 else "0"
        r9 = args[3] if len(args) > 3 else "0"
        print(f"    args: rcx={rcx} rdx={rdx} r8={r8} r9={r9}")
    print(f"    callstack ({len(cs)} frames):")
    for j, fr in enumerate(cs):
        if j >= 16:
            break
        print(f"      [{j:2d}] {fmt_addr(fr, d2r_base)}")
    print()


def summarize(entries: list, d2r_base: int):
    """Print summary: opcodes count + unique top-frame RVAs per opcode."""
    by_op = defaultdict(list)
    for e in entries:
        op = e.get("opcode", "0x00")
        by_op[op].append(e)

    print("=" * 60)
    print("SUMMARY")
    print("=" * 60)
    for op, lst in sorted(by_op.items()):
        print(f"\nopcode {op}: {len(lst)} captures")
        top_frames = Counter()
        for e in lst:
            cs = e.get("callstack", [])
            if cs:
                top_frames[cs[0]] += 1
        if top_frames:
            print(f"  unique top-frame RVAs (caller of hooked fn):")
            for fr, n in top_frames.most_common(10):
                rva_str = fmt_addr(fr, d2r_base)
                print(f"    {rva_str:<28} (×{n})")
    print()


def run_once(port: int, char: str, op_filter: int | None, save: Path | None):
    dump = fetch_dump(port, char)
    if "error" in dump:
        print(f"ERROR: {dump['error']}")
        return
    entries = dump.get("entries", [])
    d2r_base_str = dump.get("d2r_base", "0x0")
    try:
        d2r_base = int(d2r_base_str, 16)
    except ValueError:
        d2r_base = 0
    if op_filter is not None:
        entries = [e for e in entries if int(e.get("opcode", "0x00"), 16) == op_filter]
    print(f"=== {len(entries)} entries (d2r_base={d2r_base_str}) ===\n")
    for i, e in enumerate(entries):
        print_entry(e, d2r_base, i)
    summarize(entries, d2r_base)
    if save:
        save.write_text(json.dumps({"d2r_base": d2r_base_str, "entries": entries}, indent=2))
        print(f"Saved JSON dump to: {save}")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--port", type=int, required=True)
    ap.add_argument("--char", default="Blizzard")
    ap.add_argument("--watch", type=int, default=0,
                    help="poll every N seconds (0 = single shot)")
    ap.add_argument("--filter", default="",
                    help="only show this opcode (e.g. 0x54)")
    ap.add_argument("--install", action="store_true",
                    help="install PacketTracer hook before draining")
    ap.add_argument("--uninstall", action="store_true",
                    help="uninstall hook then exit")
    ap.add_argument("--status", action="store_true",
                    help="print status only")
    ap.add_argument("--save", default="",
                    help="save raw JSON to this path")
    args = ap.parse_args()

    if args.uninstall:
        r = uninstall(args.port, args.char)
        print("uninstall:", json.dumps(r, indent=2))
        return

    if args.install:
        r = install(args.port, args.char)
        print("install:", json.dumps(r, indent=2))

    if args.status:
        r = fetch_status(args.port, args.char)
        print("status:", json.dumps(r, indent=2))
        return

    op_filter = None
    if args.filter:
        op_filter = int(args.filter, 0)
    save = Path(args.save) if args.save else None

    if args.watch == 0:
        run_once(args.port, args.char, op_filter, save)
    else:
        try:
            while True:
                run_once(args.port, args.char, op_filter, save)
                time.sleep(args.watch)
        except KeyboardInterrupt:
            print("\nstopped")


if __name__ == "__main__":
    main()
