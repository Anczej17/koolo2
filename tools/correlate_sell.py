"""
correlate_sell.py — Dump ALL item fields, start bufpoll, wait for sell,
then correlate which field matches the sell packet's first u32.

Usage: python tools/correlate_sell.py --pid <PID>
Then sell ONE item at the vendor. Script will auto-detect and correlate.
"""
import struct, ctypes, ctypes.wintypes, sys, time, threading, os

kernel32 = ctypes.WinDLL('kernel32', use_last_error=True)
OpenProcess = kernel32.OpenProcess; OpenProcess.restype = ctypes.wintypes.HANDLE
ReadProcessMemory = kernel32.ReadProcessMemory; CloseHandle = kernel32.CloseHandle

def rpm(h,a,s):
    b=ctypes.create_string_buffer(s);r=ctypes.c_size_t()
    if not ReadProcessMemory(h,ctypes.c_void_p(a),b,s,ctypes.byref(r)):return None
    return b.raw[:r.value]
def r64(h,a):b=rpm(h,a,8);return struct.unpack_from('<Q',b)[0] if b and len(b)>=8 else None
def r32(h,a):b=rpm(h,a,4);return struct.unpack_from('<I',b)[0] if b and len(b)>=4 else None

UNIT_TABLE = 0x1EA73D0
BUF0_OFF = 0x19ED886
BUF1_OFF = 0x1F51330

def find_base(pid):
    TH32 = 0x00000008 | 0x00000010
    class ME(ctypes.Structure):
        _fields_ = [("dwSize",ctypes.wintypes.DWORD),("th32ModuleID",ctypes.wintypes.DWORD),
            ("th32ProcessID",ctypes.wintypes.DWORD),("GlblcntUsage",ctypes.wintypes.DWORD),
            ("ProccntUsage",ctypes.wintypes.DWORD),("modBaseAddr",ctypes.c_void_p),
            ("modBaseSize",ctypes.wintypes.DWORD),("hModule",ctypes.wintypes.HANDLE),
            ("szModule",ctypes.c_wchar*256),("szExePath",ctypes.c_wchar*260)]
    snap = kernel32.CreateToolhelp32Snapshot(TH32, pid)
    me = ME(); me.dwSize = ctypes.sizeof(me)
    if not kernel32.Module32FirstW(snap, ctypes.byref(me)): CloseHandle(snap); return None
    while True:
        if me.szModule == "D2R.exe": b=me.modBaseAddr; CloseHandle(snap); return b
        if not kernel32.Module32NextW(snap, ctypes.byref(me)): break
    CloseHandle(snap); return None

def dump_all_items(h, base):
    """Return dict: unitID -> {all u32 values from unit struct + data struct + deeper}"""
    items = {}
    for bucket in range(128):
        ptr = r64(h, base + UNIT_TABLE + (4*128+bucket)*8)
        vis = set()
        while ptr and ptr > 0x10000 and ptr not in vis:
            vis.add(ptr)
            if r32(h, ptr) != 4: ptr = r64(h, ptr+0x150); continue
            uid = r32(h, ptr+0x08)
            txt = r32(h, ptr+0x04)

            vals = {}
            # Unit struct: read 0x160 bytes
            raw = rpm(h, ptr, 0x160)
            if raw:
                for off in range(0, len(raw), 4):
                    v = struct.unpack_from('<I', raw, off)[0]
                    if v > 0:
                        vals[f'unit+0x{off:03X}'] = v

            # Data struct
            dp = r64(h, ptr+0x10)
            if dp and dp > 0x10000:
                draw = rpm(h, dp, 0x100)
                if draw:
                    for off in range(0, len(draw), 4):
                        v = struct.unpack_from('<I', draw, off)[0]
                        if v > 0:
                            vals[f'data+0x{off:03X}'] = v

            # Inventory record
            ip = r64(h, ptr+0x90)
            if ip and ip > 0x10000:
                iraw = rpm(h, ip, 0x80)
                if iraw:
                    for off in range(0, len(iraw), 4):
                        v = struct.unpack_from('<I', iraw, off)[0]
                        if v > 0:
                            vals[f'inv+0x{off:03X}'] = v

            # Stats list - check for Value stat
            slp = r64(h, ptr+0x88)
            if slp and slp > 0x10000:
                for label, soff in [('bstat', 0x30), ('fstat', 0xA8)]:
                    arr = r64(h, slp+soff)
                    cnt = r64(h, slp+soff+0x08)
                    if arr and arr > 0x10000 and cnt and 0 < cnt < 500:
                        sraw = rpm(h, arr, int(cnt)*8)
                        if sraw:
                            for i in range(min(int(cnt), len(sraw)//8)):
                                o = i*8
                                sid = struct.unpack_from('<H', sraw, o+2)[0]
                                sv = struct.unpack_from('<i', sraw, o+4)[0]
                                vals[f'{label}[{sid}]'] = sv

            items[uid] = {'txt': txt, 'ptr': ptr, 'vals': vals}
            ptr = r64(h, ptr+0x150)
    return items

def poll_sell(h, base, duration=90):
    """Poll buf1 for 0x33 sell packet. Return (first_u32, item_u32) or None."""
    addr = base + BUF1_OFF
    prev = rpm(h, addr, 512)
    if not prev: return None

    end = time.time() + duration
    while time.time() < end:
        cur = rpm(h, addr, 512)
        if not cur: time.sleep(0.005); continue

        # Check if byte 0 changed to 0x33
        if cur[0] == 0x33 and prev[0] != 0x33:
            first_u32 = struct.unpack_from('<I', cur, 1)[0]
            item_u32 = struct.unpack_from('<I', cur, 5)[0]
            return (first_u32, item_u32, cur[:22].hex())

        # Also check if opcode stayed 0x33 but content changed
        if cur[0] == 0x33 and prev[0] == 0x33 and cur[1:9] != prev[1:9]:
            first_u32 = struct.unpack_from('<I', cur, 1)[0]
            item_u32 = struct.unpack_from('<I', cur, 5)[0]
            return (first_u32, item_u32, cur[:22].hex())

        prev = cur
        time.sleep(0.005)
    return None

def main():
    pid = int(sys.argv[2]) if len(sys.argv) > 2 else None
    if not pid:
        print("Usage: python correlate_sell.py --pid <PID>")
        sys.exit(1)

    base = find_base(pid)
    if not base: print("D2R not found"); sys.exit(1)
    h = OpenProcess(0x0410, False, pid)
    if not h: print("OpenProcess failed"); sys.exit(1)

    print(f"D2R base: 0x{base:X}")

    # Step 1: Dump all items
    print("\n[1] Dumping all items from memory...")
    items = dump_all_items(h, base)
    print(f"    Found {len(items)} items")

    # Step 2: Poll for sell packet
    print("\n[2] Waiting for sell packet (sell ONE item at vendor)...")
    print("    Polling buf1 for opcode 0x33...")

    result = poll_sell(h, base, duration=90)

    if not result:
        print("    No sell packet detected in 90 seconds")
        CloseHandle(h)
        sys.exit(1)

    first_u32, item_u32, hexstr = result
    print(f"\n[3] SELL DETECTED!")
    print(f"    hex: {hexstr}")
    print(f"    first_u32 = {first_u32} (0x{first_u32:X})")
    print(f"    item_u32  = {item_u32} (0x{item_u32:X})")

    # Step 3: Correlate
    print(f"\n[4] CORRELATING first_u32={first_u32} with item data...")

    # Find which item was sold (by item_u32 = UnitID)
    sold_item = items.get(item_u32)
    if sold_item:
        print(f"\n    Sold item: UnitID={item_u32} txtID={sold_item['txt']}")
        print(f"    Searching for value {first_u32} (0x{first_u32:X}) in item fields:")

        found = False
        for field, val in sorted(sold_item['vals'].items()):
            if val == first_u32:
                print(f"    >>> MATCH: {field} = {val} (0x{val:X}) <<<")
                found = True

        if not found:
            print(f"    NO EXACT MATCH found for {first_u32}")
            print(f"\n    All values in range {first_u32-10} to {first_u32+10}:")
            for field, val in sorted(sold_item['vals'].items()):
                if abs(val - first_u32) <= 10:
                    print(f"    NEAR: {field} = {val} (0x{val:X}) [diff={val-first_u32}]")

            print(f"\n    All values 10-100000:")
            for field, val in sorted(sold_item['vals'].items()):
                if 10 < val < 100000:
                    print(f"    {field} = {val} (0x{val:X})")
    else:
        print(f"    Item UnitID={item_u32} NOT found in pre-sell dump!")
        print(f"    Searching ALL items for first_u32={first_u32}...")
        for uid, data in sorted(items.items()):
            for field, val in data['vals'].items():
                if val == first_u32:
                    print(f"    MATCH in item UnitID={uid}: {field} = {val}")

    # Also dump ALL items that have this value anywhere
    print(f"\n[5] Global search: any item with value {first_u32}...")
    for uid, data in sorted(items.items()):
        for field, val in data['vals'].items():
            if val == first_u32:
                print(f"    Item UnitID={uid} txtID={data['txt']}: {field} = {val}")

    CloseHandle(h)
    print("\nDone.")

if __name__ == "__main__":
    main()
