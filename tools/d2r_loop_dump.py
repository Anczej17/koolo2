#!/usr/bin/env python3
"""
Targeted dumper for the D2R 0x14014XXXX region (per-opcode handlers).
Saves snapshots labeled by iteration index. Run between user sell actions
to capture handlers that decrypt only when specific opcodes execute.

Usage:
  python tools/d2r_loop_dump.py --pid <D2R pid> --iter 0 --out logs/snap_iter0
"""
import argparse
import ctypes
import ctypes.wintypes as wt
import sys

PROCESS_VM_READ           = 0x0010
PROCESS_QUERY_INFORMATION = 0x0400

kernel32 = ctypes.WinDLL('kernel32', use_last_error=True)

OpenProcess = kernel32.OpenProcess
OpenProcess.restype  = wt.HANDLE
OpenProcess.argtypes = [wt.DWORD, wt.BOOL, wt.DWORD]

CloseHandle = kernel32.CloseHandle
CloseHandle.argtypes = [wt.HANDLE]

ReadProcessMemory = kernel32.ReadProcessMemory
ReadProcessMemory.restype  = wt.BOOL
ReadProcessMemory.argtypes = [wt.HANDLE, ctypes.c_void_p, ctypes.c_void_p,
                              ctypes.c_size_t, ctypes.POINTER(ctypes.c_size_t)]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--pid", type=int, required=True)
    ap.add_argument("--base", type=lambda x: int(x, 0), default=0x7ff7721e0000)
    ap.add_argument("--rva-lo", type=lambda x: int(x, 0), default=0x140000)
    ap.add_argument("--rva-hi", type=lambda x: int(x, 0), default=0x190000)
    ap.add_argument("--out", required=True)
    args = ap.parse_args()

    h = OpenProcess(PROCESS_VM_READ | PROCESS_QUERY_INFORMATION, False, args.pid)
    if not h:
        print(f"OpenProcess failed: {ctypes.get_last_error()}")
        sys.exit(1)

    page_size = 0x1000
    total_size = args.rva_hi - args.rva_lo
    buf = (ctypes.c_ubyte * page_size)()
    bytes_read = ctypes.c_size_t(0)

    output = bytearray(total_size)
    captured_pages = []

    for offset in range(0, total_size, page_size):
        addr = args.base + args.rva_lo + offset
        ok = ReadProcessMemory(h, addr, ctypes.byref(buf), page_size,
                               ctypes.byref(bytes_read))
        if ok and bytes_read.value == page_size:
            output[offset:offset+page_size] = bytes(buf)
            captured_pages.append(args.rva_lo + offset)

    CloseHandle(h)

    with open(args.out + ".bin", "wb") as f:
        f.write(bytes(output))
    with open(args.out + ".meta", "w") as f:
        f.write(f"base=0x{args.base:x}\n")
        f.write(f"rva_lo=0x{args.rva_lo:x}\n")
        f.write(f"rva_hi=0x{args.rva_hi:x}\n")
        f.write(f"captured_pages={len(captured_pages)}/{total_size//page_size}\n")
        for p in captured_pages:
            f.write(f"  rva=0x{p:x}\n")

    print(f"saved {args.out}.bin ({len(output)} bytes), captured {len(captured_pages)}/{total_size//page_size} pages")


if __name__ == "__main__":
    main()
