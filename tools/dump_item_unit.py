"""
dump_item_unit.py — Dump FULL memory of ALL item units in D2R.
Prints every u32 at every offset so we can correlate with sell packet.

Usage: python tools/dump_item_unit.py --pid <PID>
Then sell an item and compare the sell packet's first u32 with values here.
"""
import struct, ctypes, ctypes.wintypes, argparse, sys

kernel32 = ctypes.WinDLL('kernel32', use_last_error=True)
OpenProcess = kernel32.OpenProcess
OpenProcess.restype = ctypes.wintypes.HANDLE
ReadProcessMemory = kernel32.ReadProcessMemory
CloseHandle = kernel32.CloseHandle

PROCESS_VM_READ = 0x0010
PROCESS_QUERY_INFORMATION = 0x0400
TH32CS_SNAPMODULE = 0x00000008 | 0x00000010

class MODULEENTRY32W(ctypes.Structure):
    _fields_ = [("dwSize", ctypes.wintypes.DWORD), ("th32ModuleID", ctypes.wintypes.DWORD),
        ("th32ProcessID", ctypes.wintypes.DWORD), ("GlblcntUsage", ctypes.wintypes.DWORD),
        ("ProccntUsage", ctypes.wintypes.DWORD), ("modBaseAddr", ctypes.c_void_p),
        ("modBaseSize", ctypes.wintypes.DWORD), ("hModule", ctypes.wintypes.HANDLE),
        ("szModule", ctypes.c_wchar * 256), ("szExePath", ctypes.c_wchar * 260)]

def find_base(pid):
    snap = kernel32.CreateToolhelp32Snapshot(TH32CS_SNAPMODULE, pid)
    me = MODULEENTRY32W(); me.dwSize = ctypes.sizeof(me)
    if not kernel32.Module32FirstW(snap, ctypes.byref(me)): CloseHandle(snap); return None
    while True:
        if me.szModule == "D2R.exe": b = me.modBaseAddr; CloseHandle(snap); return b
        if not kernel32.Module32NextW(snap, ctypes.byref(me)): break
    CloseHandle(snap); return None

def rpm(h, addr, sz):
    buf = ctypes.create_string_buffer(sz); rd = ctypes.c_size_t()
    if not ReadProcessMemory(h, ctypes.c_void_p(addr), buf, sz, ctypes.byref(rd)): return None
    return buf.raw[:rd.value]

def r64(h, a):
    b = rpm(h, a, 8)
    return struct.unpack_from("<Q", b)[0] if b and len(b) >= 8 else None

def r32(h, a):
    b = rpm(h, a, 4)
    return struct.unpack_from("<I", b)[0] if b and len(b) >= 4 else None

def main():
    p = argparse.ArgumentParser()
    p.add_argument("--pid", type=int, required=True)
    args = p.parse_args()

    base = find_base(args.pid)
    if not base: print("D2R not found"); sys.exit(1)
    h = OpenProcess(PROCESS_VM_READ | PROCESS_QUERY_INFORMATION, False, args.pid)
    if not h: print("OpenProcess failed"); sys.exit(1)

    UNIT_TABLE = 0x1EA73D0
    DUMP_SIZE = 0x160  # dump 352 bytes of each unit struct

    print(f"D2R base: 0x{base:X}")
    print(f"Dumping item units (type=4)...\n")

    for bucket in range(128):
        ptr = r64(h, base + UNIT_TABLE + (4 * 128 + bucket) * 8)
        visited = set()
        while ptr and ptr > 0x10000 and ptr not in visited:
            visited.add(ptr)
            utype = r32(h, ptr + 0x00)
            if utype != 4:
                ptr = r64(h, ptr + 0x150)
                continue

            uid = r32(h, ptr + 0x08)
            txt = r32(h, ptr + 0x04)
            raw = rpm(h, ptr, DUMP_SIZE)
            if not raw:
                ptr = r64(h, ptr + 0x150)
                continue

            # Read unit data ptr and dump it too
            data_ptr = struct.unpack_from("<Q", raw, 0x10)[0]

            # Location info from inventory
            inv_ptr = struct.unpack_from("<Q", raw, 0x90)[0]
            equip_flags = struct.unpack_from("<H", raw, 0x54)[0] if len(raw) > 0x55 else 0

            print(f"=== Item UnitID={uid} txtID={txt} ptr=0x{ptr:X} equipFlags=0x{equip_flags:04X} ===")

            # Print ALL u32 values at every 4-byte offset in unit struct
            print("  Unit struct u32 values (non-zero, <100000):")
            for off in range(0, min(len(raw), DUMP_SIZE), 4):
                val = struct.unpack_from("<I", raw, off)[0]
                if 0 < val < 100000:
                    print(f"    unit+0x{off:03X} = {val:8d} (0x{val:X})")

            # Dump unit data struct
            if data_ptr and data_ptr > 0x10000:
                data_raw = rpm(h, data_ptr, 0x80)
                if data_raw:
                    print("  Unit DATA struct u32 values (non-zero, <100000):")
                    for off in range(0, min(len(data_raw), 0x80), 4):
                        val = struct.unpack_from("<I", data_raw, off)[0]
                        if 0 < val < 100000:
                            print(f"    data+0x{off:03X} = {val:8d} (0x{val:X})")

            # Dump stats list if available
            stats_ptr_off = 0x88
            if len(raw) > stats_ptr_off + 8:
                stats_ptr = struct.unpack_from("<Q", raw, stats_ptr_off)[0]
                if stats_ptr and stats_ptr > 0x10000:
                    stats_raw = rpm(h, stats_ptr, 0x40)
                    if stats_raw:
                        print("  Stats ptr u32 values (non-zero, <100000):")
                        for off in range(0, min(len(stats_raw), 0x40), 4):
                            val = struct.unpack_from("<I", stats_raw, off)[0]
                            if 0 < val < 100000:
                                print(f"    stats+0x{off:03X} = {val:8d} (0x{val:X})")

            print()
            ptr = r64(h, ptr + 0x150)

    CloseHandle(h)

if __name__ == "__main__":
    main()
