#!/usr/bin/env python3
"""
Continuous RPM dumper with union-merge semantics.

While running, keeps a per-page buffer of the FIRST non-zero content seen.
Arxan decrypts pages on demand and re-encrypts quickly — one-shot dumps miss
most handlers. Running a tight loop during a specific user action maximizes
the odds of capturing that action's handler in its decrypted state.

The loop runs until SIGINT (Ctrl-C) OR until a stop-signal file appears.

Usage:
  # Start in background (phase label determines output file name)
  python tools/d2r_live_capture.py --pid <D2R_PID> --phase 0x33_sell --duration 30

  # Or use a signal file to stop:
  python tools/d2r_live_capture.py --pid <D2R_PID> --phase 0x33_sell \
        --stop-file /tmp/stop_dump

  # Signal stop:
  touch /tmp/stop_dump
"""
from __future__ import annotations

import argparse
import ctypes
import ctypes.wintypes as wt
import os
import signal
import sys
import time
from pathlib import Path

PROCESS_VM_READ = 0x0010
PROCESS_QUERY_INFORMATION = 0x0400

kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)

OpenProcess = kernel32.OpenProcess
OpenProcess.restype = wt.HANDLE
OpenProcess.argtypes = [wt.DWORD, wt.BOOL, wt.DWORD]

CloseHandle = kernel32.CloseHandle
CloseHandle.argtypes = [wt.HANDLE]

ReadProcessMemory = kernel32.ReadProcessMemory
ReadProcessMemory.restype = wt.BOOL
ReadProcessMemory.argtypes = [
    wt.HANDLE,
    ctypes.c_void_p,
    ctypes.c_void_p,
    ctypes.c_size_t,
    ctypes.POINTER(ctypes.c_size_t),
]


_stop = False


def _handle_sigint(signum, frame):
    global _stop
    _stop = True


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--pid", type=int, required=True)
    ap.add_argument(
        "--base",
        type=lambda x: int(x, 0),
        default=0x7FF7B7710000,
        help="D2R base VA (read from /debug/gamestate or bot log)",
    )
    ap.add_argument(
        "--rva-lo",
        type=lambda x: int(x, 0),
        default=0x140000,
        help="Start RVA to cover (default covers 0x14014XXX packet region)",
    )
    ap.add_argument(
        "--rva-hi",
        type=lambda x: int(x, 0),
        default=0x200000,
        help="End RVA",
    )
    ap.add_argument("--phase", required=True, help="Phase label for output filename")
    ap.add_argument("--out-dir", default="logs/dumps", help="Output directory")
    ap.add_argument(
        "--duration",
        type=float,
        default=0,
        help="Max duration in seconds (0 = run forever / until stop signal)",
    )
    ap.add_argument(
        "--stop-file",
        default="",
        help="If this file exists, stop the capture loop",
    )
    ap.add_argument(
        "--sleep-ms",
        type=float,
        default=10,
        help="Sleep between full passes (default 10ms = 100 passes/s)",
    )
    args = ap.parse_args()

    out_dir = Path(args.out_dir)
    out_dir.mkdir(parents=True, exist_ok=True)
    out_bin = out_dir / f"{args.phase}.bin"
    out_meta = out_dir / f"{args.phase}.meta"

    h = OpenProcess(PROCESS_VM_READ | PROCESS_QUERY_INFORMATION, False, args.pid)
    if not h:
        err = ctypes.get_last_error()
        print(f"OpenProcess({args.pid}) failed, GetLastError={err}")
        sys.exit(1)

    signal.signal(signal.SIGINT, _handle_sigint)

    PAGE = 0x1000
    total_bytes = args.rva_hi - args.rva_lo
    page_count = total_bytes // PAGE

    # Union buffer: zero-initialized. Each page slot is filled the first time
    # we read it with non-zero content. Re-reads that are zero don't clobber.
    union = bytearray(total_bytes)
    page_first_seen_ms: list[int | None] = [None] * page_count
    pages_captured = 0

    buf = (ctypes.c_ubyte * PAGE)()
    bytes_read = ctypes.c_size_t(0)

    start = time.monotonic()
    passes = 0
    reads_ok = 0
    reads_fail = 0

    print(
        f"[capture] phase={args.phase} pid={args.pid} range=0x{args.rva_lo:x}..0x{args.rva_hi:x} "
        f"pages={page_count} sleep_ms={args.sleep_ms}"
    )

    try:
        while not _stop:
            elapsed = time.monotonic() - start
            if args.duration > 0 and elapsed >= args.duration:
                break
            if args.stop_file and Path(args.stop_file).exists():
                print(f"[capture] stop-file {args.stop_file} detected — halting")
                break

            for pi in range(page_count):
                if page_first_seen_ms[pi] is not None:
                    continue  # already captured
                rva = args.rva_lo + pi * PAGE
                addr = args.base + rva
                ok = ReadProcessMemory(
                    h, addr, ctypes.byref(buf), PAGE, ctypes.byref(bytes_read)
                )
                if ok and bytes_read.value == PAGE:
                    raw = bytes(buf)
                    if raw != b"\x00" * PAGE:
                        off = pi * PAGE
                        union[off : off + PAGE] = raw
                        page_first_seen_ms[pi] = int(elapsed * 1000)
                        pages_captured += 1
                    reads_ok += 1
                else:
                    reads_fail += 1

            passes += 1
            # Print progress every second
            if passes % max(1, int(1000 / max(args.sleep_ms, 1))) == 0:
                print(
                    f"[capture] t={elapsed:.1f}s pass={passes} "
                    f"pages={pages_captured}/{page_count} rok={reads_ok} rfail={reads_fail}"
                )

            time.sleep(args.sleep_ms / 1000)

    finally:
        CloseHandle(h)

    # Save final union buffer + meta
    out_bin.write_bytes(bytes(union))
    with out_meta.open("w") as f:
        f.write(f"phase={args.phase}\n")
        f.write(f"pid={args.pid}\n")
        f.write(f"base=0x{args.base:x}\n")
        f.write(f"rva_lo=0x{args.rva_lo:x}\n")
        f.write(f"rva_hi=0x{args.rva_hi:x}\n")
        f.write(f"pages_total={page_count}\n")
        f.write(f"pages_captured={pages_captured}\n")
        f.write(f"coverage_pct={100 * pages_captured / page_count:.1f}\n")
        f.write(f"duration_s={time.monotonic() - start:.2f}\n")
        f.write(f"passes={passes}\n")
        f.write(f"reads_ok={reads_ok}\n")
        f.write(f"reads_fail={reads_fail}\n")
        f.write("first_seen_ms:\n")
        for pi, ms in enumerate(page_first_seen_ms):
            if ms is not None:
                rva = args.rva_lo + pi * PAGE
                f.write(f"  rva=0x{rva:x} t_ms={ms}\n")

    print(
        f"[capture] DONE phase={args.phase} pages={pages_captured}/{page_count} "
        f"({100 * pages_captured / page_count:.1f}%) → {out_bin}"
    )


if __name__ == "__main__":
    main()
