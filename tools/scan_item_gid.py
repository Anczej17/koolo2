"""
scan_item_gid.py — Find where D2R stores the server-assigned item GID.

We know from bufpoll capture that item with local UnitID=75 (0x4B) has
server GID=43 (0x2B) in the sell packet. We also know the bot reads
items from the unit table via ReadProcessMemory.

Strategy: read the item unit structure at multiple offsets and find
where value 0x2B (43) appears. That offset is where the server GID lives.

Usage: python tools/scan_item_gid.py --pid <D2R_PID>
"""
import struct
import ctypes
import ctypes.wintypes
import argparse
import sys

kernel32 = ctypes.WinDLL('kernel32', use_last_error=True)

OpenProcess = kernel32.OpenProcess
OpenProcess.restype = ctypes.wintypes.HANDLE
ReadProcessMemory = kernel32.ReadProcessMemory
CloseHandle = kernel32.CloseHandle

PROCESS_VM_READ = 0x0010
PROCESS_QUERY_INFORMATION = 0x0400

TH32CS_SNAPMODULE = 0x00000008
TH32CS_SNAPMODULE32 = 0x00000010

class MODULEENTRY32W(ctypes.Structure):
    _fields_ = [
        ("dwSize", ctypes.wintypes.DWORD),
        ("th32ModuleID", ctypes.wintypes.DWORD),
        ("th32ProcessID", ctypes.wintypes.DWORD),
        ("GlblcntUsage", ctypes.wintypes.DWORD),
        ("ProccntUsage", ctypes.wintypes.DWORD),
        ("modBaseAddr", ctypes.c_void_p),
        ("modBaseSize", ctypes.wintypes.DWORD),
        ("hModule", ctypes.wintypes.HANDLE),
        ("szModule", ctypes.c_wchar * 256),
        ("szExePath", ctypes.c_wchar * 260),
    ]

def find_d2r_base(pid):
    snap = kernel32.CreateToolhelp32Snapshot(TH32CS_SNAPMODULE | TH32CS_SNAPMODULE32, pid)
    me = MODULEENTRY32W()
    me.dwSize = ctypes.sizeof(me)
    if not kernel32.Module32FirstW(snap, ctypes.byref(me)):
        CloseHandle(snap)
        return None
    while True:
        if me.szModule == "D2R.exe":
            base = me.modBaseAddr
            CloseHandle(snap)
            return base
        if not kernel32.Module32NextW(snap, ctypes.byref(me)):
            break
    CloseHandle(snap)
    return None

def rpm(handle, addr, size):
    buf = ctypes.create_string_buffer(size)
    read = ctypes.c_size_t()
    ok = ReadProcessMemory(handle, ctypes.c_void_p(addr), buf, size, ctypes.byref(read))
    if not ok or read.value == 0:
        return None
    return buf.raw[:read.value]

def read_u32(handle, addr):
    b = rpm(handle, addr, 4)
    if b is None or len(b) < 4:
        return None
    return struct.unpack_from("<I", b)[0]

def read_u64(handle, addr):
    b = rpm(handle, addr, 8)
    if b is None or len(b) < 8:
        return None
    return struct.unpack_from("<Q", b)[0]

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--pid", type=int, required=True)
    # Known offsets from koolo2 memory reader
    parser.add_argument("--unit-table-offset", type=lambda x: int(x, 0), default=0x1EA8BD0,
                        help="UnitTable offset from D2R base (default from koolo2)")
    args = parser.parse_args()

    base = find_d2r_base(args.pid)
    if not base:
        print("D2R.exe not found")
        sys.exit(1)
    print(f"D2R base: 0x{base:X}")

    handle = OpenProcess(PROCESS_VM_READ | PROCESS_QUERY_INFORMATION, False, args.pid)
    if not handle:
        print("OpenProcess failed")
        sys.exit(1)

    # Scan unit table for items (type=4)
    # Unit table has 128 hash buckets for each unit type
    # Items are type 4, so they start at bucket offset 4*128*8 = 4096 bytes into the table
    # Actually, the table is: base + unitTableOffset + (unitType * 128 + hashBucket) * 8

    print("\nScanning item units in unit table...")
    print("Looking for items and dumping their structure\n")

    items_found = []

    # Items are unit type 4. Scan all 128 hash buckets.
    for bucket in range(128):
        table_entry_addr = base + args.unit_table_offset + (4 * 128 + bucket) * 8
        unit_ptr = read_u64(handle, table_entry_addr)

        while unit_ptr and unit_ptr > 0x10000:
            # Read unit structure header
            unit_type = read_u32(handle, unit_ptr + 0x00)
            txt_id = read_u32(handle, unit_ptr + 0x04)
            unit_id = read_u32(handle, unit_ptr + 0x08)
            mode = read_u32(handle, unit_ptr + 0x0C)

            if unit_type != 4:  # not an item
                # Follow linked list
                next_ptr = read_u64(handle, unit_ptr + 0x150)
                if next_ptr == unit_ptr:
                    break
                unit_ptr = next_ptr
                continue

            # Read inventory/location info
            inv_ptr = read_u64(handle, unit_ptr + 0x90)

            # Dump bytes around unit for analysis
            raw = rpm(handle, unit_ptr, 0x20)
            if raw:
                hex_str = raw.hex()
            else:
                hex_str = "(read failed)"

            # Also read unit data ptr and check for alternative IDs
            unit_data_ptr = read_u64(handle, unit_ptr + 0x10)
            alt_id1 = read_u32(handle, unit_ptr + 0x0C) if unit_ptr else None  # mode

            # Read various offsets looking for server GID
            candidates = {}
            for off in [0x00, 0x04, 0x08, 0x0C, 0x10, 0x14, 0x18, 0x1C, 0x20, 0x24, 0x28, 0x2C, 0x30, 0x34, 0x38, 0x3C, 0x40]:
                val = read_u32(handle, unit_ptr + off)
                if val is not None and 0 < val < 10000:
                    candidates[f"+0x{off:02X}"] = val

            # Also check unit data structure
            if unit_data_ptr and unit_data_ptr > 0x10000:
                for off in [0x00, 0x04, 0x08, 0x0C, 0x10, 0x14, 0x18, 0x1C, 0x20, 0x24, 0x28]:
                    val = read_u32(handle, unit_data_ptr + off)
                    if val is not None and 0 < val < 10000:
                        candidates[f"data+0x{off:02X}"] = val

            items_found.append({
                'unit_ptr': unit_ptr,
                'unit_id': unit_id,
                'txt_id': txt_id,
                'candidates': candidates,
                'hex': hex_str,
            })

            print(f"Item UnitID={unit_id} (0x{unit_id:X}) txtID={txt_id} ptr=0x{unit_ptr:X}")
            print(f"  raw[0:32]: {hex_str}")
            for k, v in sorted(candidates.items()):
                marker = ""
                if v == 43:  # known server GID from capture
                    marker = " *** MATCH server GID 43! ***"
                elif v == 75:
                    marker = " (= local UnitID 75)"
                print(f"  {k} = {v} (0x{v:X}){marker}")
            print()

            # Follow linked list to next item in this bucket
            next_ptr = read_u64(handle, unit_ptr + 0x150)
            if next_ptr == unit_ptr or next_ptr == 0:
                break
            unit_ptr = next_ptr

    print(f"\nTotal items found: {len(items_found)}")

    # Summary: which offset has value 43 for item with UnitID 75?
    print("\n=== SEARCHING FOR SERVER GID OFFSET ===")
    print("Looking for value 43 (0x2B) in item with UnitID 75 (0x4B)...")
    for item in items_found:
        if item['unit_id'] == 75:
            print(f"\nItem UnitID=75 found at 0x{item['unit_ptr']:X}")
            for k, v in sorted(item['candidates'].items()):
                if v == 43:
                    print(f"  >>> FOUND! {k} = 43 (0x2B) <<< THIS IS THE SERVER GID OFFSET")
            break
    else:
        print("Item with UnitID=75 not found in unit table")
        print("(Item may have been sold/dropped since capture)")

    CloseHandle(handle)

if __name__ == "__main__":
    main()
