#!/usr/bin/env python3
"""
Dumps ALL executable, readable memory pages from a running D2R process.

Strategy: VirtualQueryEx walks the virtual address space. For every region
that is COMMITTED, has executable protection, and isn't NOACCESS/GUARD,
read it page-by-page (4KB) so individual unreadable pages don't poison
the surrounding ones.

Output is a file containing the raw bytes plus a sidecar JSON describing
which absolute address each chunk in the file corresponds to. This lets
the disassembler reconstruct VAs accurately.

Usage:
  python tools/d2r_dump_exec.py --pid <D2R pid> --out logs/d2r_exec
"""

import argparse
import ctypes
import ctypes.wintypes as wt
import json
import sys

PROCESS_VM_READ           = 0x0010
PROCESS_QUERY_INFORMATION = 0x0400

PAGE_NOACCESS         = 0x01
PAGE_READONLY         = 0x02
PAGE_READWRITE        = 0x04
PAGE_WRITECOPY        = 0x08
PAGE_EXECUTE          = 0x10
PAGE_EXECUTE_READ     = 0x20
PAGE_EXECUTE_READWRITE = 0x40
PAGE_EXECUTE_WRITECOPY = 0x80
PAGE_GUARD            = 0x100

MEM_COMMIT  = 0x1000
MEM_RESERVE = 0x2000

EXEC_PROTS = (PAGE_EXECUTE, PAGE_EXECUTE_READ, PAGE_EXECUTE_READWRITE, PAGE_EXECUTE_WRITECOPY)

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


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--pid", type=int, required=True)
    ap.add_argument("--out", required=True, help="Output prefix (.bin + .json)")
    args = ap.parse_args()

    h = OpenProcess(PROCESS_VM_READ | PROCESS_QUERY_INFORMATION, False, args.pid)
    if not h:
        print(f"OpenProcess failed: {ctypes.get_last_error()}", file=sys.stderr)
        sys.exit(1)

    out_bin  = open(args.out + ".bin", "wb")
    chunks   = []  # list of (file_offset, va, size)

    addr = 0
    end  = 0x7FFFFFFFFFFF  # x64 user-mode max
    region_count = 0
    exec_region_count = 0
    total_read = 0

    mbi = MEMORY_BASIC_INFORMATION()
    while addr < end:
        ok = VirtualQueryEx(h, ctypes.c_void_p(addr), ctypes.byref(mbi), ctypes.sizeof(mbi))
        if ok == 0:
            # End of address space
            break
        region_count += 1
        next_addr = (mbi.BaseAddress or 0) + mbi.RegionSize

        is_committed = bool(mbi.State & MEM_COMMIT)
        is_exec      = mbi.Protect in EXEC_PROTS
        is_noaccess  = bool(mbi.Protect & PAGE_NOACCESS)
        is_guard     = bool(mbi.Protect & PAGE_GUARD)

        if is_committed and is_exec and not is_noaccess and not is_guard:
            exec_region_count += 1
            # Read page by page so individual NOACCESS pages don't kill the region
            page = 0x1000
            base = mbi.BaseAddress
            sz   = mbi.RegionSize
            print(f"  region 0x{base:x} sz=0x{sz:x} prot=0x{mbi.Protect:x}", file=sys.stderr)
            for off in range(0, sz, page):
                pg_addr = base + off
                buf = (ctypes.c_ubyte * page)()
                read = ctypes.c_size_t(0)
                rv = ReadProcessMemory(h, ctypes.c_void_p(pg_addr), buf, page, ctypes.byref(read))
                if rv and read.value > 0:
                    file_off = out_bin.tell()
                    raw = bytes(buf[:read.value])
                    out_bin.write(raw)
                    chunks.append({"file_off": file_off, "va": pg_addr, "size": read.value})
                    total_read += read.value
        addr = next_addr
        if addr == 0:
            break

    out_bin.close()
    with open(args.out + ".json", "w") as f:
        json.dump({"chunks": chunks}, f)

    print(f"\nregions={region_count} exec_regions={exec_region_count}", file=sys.stderr)
    print(f"total_read={total_read:,} bytes ({total_read//1024//1024} MB)", file=sys.stderr)
    print(f"chunks={len(chunks)}", file=sys.stderr)
    print(f"output: {args.out}.bin + {args.out}.json", file=sys.stderr)

    CloseHandle(h)


if __name__ == "__main__":
    main()
