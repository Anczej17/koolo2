#!/usr/bin/env python3
"""
Dumps the .text section of a running D2R.exe to a file.

Why: D2R.exe on disk has .text encrypted by Arxan/GuardIT.
The decrypted code only exists in process memory at runtime.

Usage: python tools/d2r_dump_text.py --pid <D2R pid> --out d2r_text.bin
"""

import argparse
import ctypes
import ctypes.wintypes as wt
import sys

PROCESS_VM_READ           = 0x0010
PROCESS_QUERY_INFORMATION = 0x0400

kernel32 = ctypes.WinDLL('kernel32', use_last_error=True)
psapi    = ctypes.WinDLL('psapi', use_last_error=True)

OpenProcess = kernel32.OpenProcess
OpenProcess.restype  = wt.HANDLE
OpenProcess.argtypes = [wt.DWORD, wt.BOOL, wt.DWORD]

CloseHandle = kernel32.CloseHandle
CloseHandle.argtypes = [wt.HANDLE]

ReadProcessMemory = kernel32.ReadProcessMemory
ReadProcessMemory.restype  = wt.BOOL
ReadProcessMemory.argtypes = [wt.HANDLE, ctypes.c_void_p, ctypes.c_void_p,
                              ctypes.c_size_t, ctypes.POINTER(ctypes.c_size_t)]

class MODULEINFO(ctypes.Structure):
    _fields_ = [
        ("lpBaseOfDll", ctypes.c_void_p),
        ("SizeOfImage", wt.DWORD),
        ("EntryPoint",  ctypes.c_void_p),
    ]

EnumProcessModules = psapi.EnumProcessModules
EnumProcessModules.restype  = wt.BOOL
EnumProcessModules.argtypes = [wt.HANDLE, ctypes.POINTER(wt.HMODULE),
                               wt.DWORD, ctypes.POINTER(wt.DWORD)]

GetModuleFileNameExW = psapi.GetModuleFileNameExW
GetModuleFileNameExW.restype  = wt.DWORD
GetModuleFileNameExW.argtypes = [wt.HANDLE, wt.HMODULE, wt.LPWSTR, wt.DWORD]

GetModuleInformation = psapi.GetModuleInformation
GetModuleInformation.restype  = wt.BOOL
GetModuleInformation.argtypes = [wt.HANDLE, wt.HMODULE, ctypes.POINTER(MODULEINFO),
                                 wt.DWORD]


def find_d2r_module(h):
    needed = wt.DWORD(0)
    EnumProcessModules(h, None, 0, ctypes.byref(needed))
    n = needed.value // ctypes.sizeof(wt.HMODULE)
    arr = (wt.HMODULE * n)()
    if not EnumProcessModules(h, arr, needed.value, ctypes.byref(needed)):
        return None, None
    for hmod in arr:
        name = ctypes.create_unicode_buffer(260)
        if GetModuleFileNameExW(h, hmod, name, 260):
            if name.value.lower().endswith('d2r.exe'):
                mi = MODULEINFO()
                if GetModuleInformation(h, hmod, ctypes.byref(mi), ctypes.sizeof(mi)):
                    return mi.lpBaseOfDll, mi.SizeOfImage
    return None, None


def read_chunked(h, addr, total):
    """Read in 64KB chunks, allow partial — Arxan / VAD splits may make
    some sub-pages unreadable; we want to capture as much as possible."""
    out = bytearray()
    chunk = 0x10000
    pos = 0
    fail_runs = 0
    while pos < total:
        n = min(chunk, total - pos)
        buf = (ctypes.c_ubyte * n)()
        read = ctypes.c_size_t(0)
        ok = ReadProcessMemory(h, addr + pos, buf, n, ctypes.byref(read))
        if ok and read.value > 0:
            out.extend(bytes(buf[:read.value]))
            if read.value < n:
                # Partial read — pad rest with zeros so offsets stay aligned
                out.extend(b'\x00' * (n - read.value))
            fail_runs = 0
        else:
            # Page unreadable — pad with zeros, keep going
            out.extend(b'\x00' * n)
            fail_runs += 1
        pos += n
        if pos % (1024 * 1024) == 0:
            print(f"  read {pos // (1024*1024)} MB / {total // (1024*1024)} MB", file=sys.stderr)
    return bytes(out)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--pid", type=int, required=True)
    ap.add_argument("--out", required=True, help="Output file")
    ap.add_argument("--text-rva", type=lambda s: int(s, 0), default=0x1000,
                    help=".text RVA (default 0x1000)")
    ap.add_argument("--text-size", type=lambda s: int(s, 0), default=0x15c6400,
                    help=".text size in bytes (default from disk)")
    args = ap.parse_args()

    h = OpenProcess(PROCESS_VM_READ | PROCESS_QUERY_INFORMATION, False, args.pid)
    if not h:
        print(f"OpenProcess failed: {ctypes.get_last_error()}", file=sys.stderr)
        sys.exit(1)

    base, sz = find_d2r_module(h)
    if base is None:
        print("D2R.exe module not found", file=sys.stderr)
        sys.exit(1)
    print(f"D2R.exe base = 0x{base:x}, size = 0x{sz:x}", file=sys.stderr)

    text_va = base + args.text_rva
    print(f"reading .text from VA 0x{text_va:x}, {args.text_size:#x} bytes...", file=sys.stderr)
    data = read_chunked(h, text_va, args.text_size)

    with open(args.out, 'wb') as f:
        f.write(data)
    print(f"wrote {len(data):,} bytes to {args.out}", file=sys.stderr)

    # Quick sanity: print first 32 bytes hex
    print(f"first 32 bytes: {data[:32].hex()}", file=sys.stderr)
    nonzero = sum(1 for b in data if b != 0)
    print(f"non-zero bytes: {nonzero:,} ({100*nonzero/len(data):.1f}%)", file=sys.stderr)

    CloseHandle(h)


if __name__ == "__main__":
    main()
