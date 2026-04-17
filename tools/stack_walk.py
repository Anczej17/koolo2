"""
Stack walk all D2R threads to find hookable non-D2R return addresses.
Identifies which system DLL functions the game thread calls.
"""
import ctypes, ctypes.wintypes as wt, struct, subprocess, json
from collections import defaultdict

k32 = ctypes.windll.kernel32
ntdll = ctypes.windll.ntdll
psapi = ctypes.windll.psapi

class MODULEINFO(ctypes.Structure):
    _fields_ = [("lpBaseOfDll", ctypes.c_void_p),
                ("SizeOfImage", wt.DWORD),
                ("EntryPoint", ctypes.c_void_p)]

r = subprocess.run(['tasklist', '/FI', 'IMAGENAME eq D2R.exe', '/FO', 'CSV', '/NH'],
                   capture_output=True, text=True)
pid = int(r.stdout.strip().split(',')[1].strip('"'))

hp = k32.OpenProcess(0x0410, False, pid)

mods = (ctypes.c_void_p * 512)()
needed = wt.DWORD()
psapi.EnumProcessModulesEx(hp, mods, ctypes.sizeof(mods), ctypes.byref(needed), 3)
nmod = needed.value // 8

modules = []
for i in range(min(nmod, 512)):
    base = mods[i]
    if base is None or base == 0:
        continue
    hmod = ctypes.c_void_p(base)
    name = (ctypes.c_char * 256)()
    psapi.GetModuleFileNameExA(hp, hmod, name, 256)
    mi = MODULEINFO()
    psapi.GetModuleInformation(hp, hmod, ctypes.byref(mi), ctypes.sizeof(mi))
    short = name.value.decode('ascii', 'replace').replace('\\', '/').split('/')[-1]
    modules.append((base, mi.SizeOfImage, short))

d2r_base = modules[0][0]
d2r_size = modules[0][1]
print(f"D2R PID={pid} base=0x{d2r_base:X} size=0x{d2r_size:X}")
print(f"{len(modules)} modules loaded")

class TE(ctypes.Structure):
    _fields_ = [('dwSize', wt.DWORD), ('u', wt.DWORD), ('tid', wt.DWORD),
                ('pid', wt.DWORD), ('bp', ctypes.c_long), ('dp', ctypes.c_long),
                ('f', wt.DWORD)]

snap = k32.CreateToolhelp32Snapshot(4, 0)
te = TE()
te.dwSize = ctypes.sizeof(te)
tids = []
if k32.Thread32First(snap, ctypes.byref(te)):
    while True:
        if te.pid == pid:
            tids.append(te.tid)
        if not k32.Thread32Next(snap, ctypes.byref(te)):
            break
k32.CloseHandle(snap)

print(f"{len(tids)} threads\n")

CONTEXT_SIZE = 1232
for idx, tid in enumerate(tids[:10]):
    ht = k32.OpenThread(0x004A, False, tid)
    if not ht:
        continue
    k32.SuspendThread(ht)

    ctx = (ctypes.c_ubyte * CONTEXT_SIZE)()
    struct.pack_into('<I', ctx, 0x30, 0x10001F)
    ntdll.NtGetContextThread(ht, ctx)
    rip = struct.unpack_from('<Q', ctx, 0xF8)[0]
    rsp = struct.unpack_from('<Q', ctx, 0x98)[0]

    stack = (ctypes.c_ubyte * 1024)()
    nread = ctypes.c_size_t()
    k32.ReadProcessMemory(hp, ctypes.c_void_p(rsp), stack, 1024, ctypes.byref(nread))

    ret_addrs = []
    for i in range(0, min(nread.value, 1024), 8):
        addr = struct.unpack_from('<Q', stack, i)[0]
        for mbase, msize, mname in modules:
            if mbase <= addr < mbase + msize:
                rva = addr - mbase
                in_d2r = (mbase == d2r_base)
                ret_addrs.append((addr, rva, mname, in_d2r))
                break

    k32.ResumeThread(ht)
    k32.CloseHandle(ht)

    # Identify RIP module
    rip_mod = "??"
    for mbase, msize, mname in modules:
        if mbase <= rip < mbase + msize:
            rip_mod = mname
            break

    d2r_rets = [r for r in ret_addrs if r[3]]
    non_d2r_rets = [r for r in ret_addrs if not r[3]]

    print(f"TID {tid:5d} [{idx}]: RIP in {rip_mod} (0x{rip:X})")
    if d2r_rets:
        print(f"  D2R returns: {len(d2r_rets)}")
        for addr, rva, mname, _ in d2r_rets[:3]:
            print(f"    0x{rva:X}")
    if non_d2r_rets:
        print(f"  Non-D2R returns (HOOKABLE):")
        for addr, rva, mname, _ in non_d2r_rets[:5]:
            print(f"    0x{addr:X} RVA 0x{rva:X} in {mname}")
    print()

k32.CloseHandle(hp)
