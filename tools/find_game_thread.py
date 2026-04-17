"""
Identify D2R game thread by sampling RIP across all threads.
Run while D2R is in-game and active (not idle menu).

Usage: python find_game_thread.py [samples=200]
"""
import ctypes, ctypes.wintypes as wt, struct, sys, time, subprocess, json
from collections import Counter, defaultdict

k32 = ctypes.windll.kernel32
ntdll = ctypes.windll.ntdll

class TE32(ctypes.Structure):
    _fields_ = [('dwSize', wt.DWORD),('cntUsage',wt.DWORD),('th32ThreadID',wt.DWORD),
                ('th32OwnerProcessID',wt.DWORD),('tpBasePri',ctypes.c_long),
                ('tpDeltaPri',ctypes.c_long),('dwFlags',wt.DWORD)]

CONTEXT_SIZE = 1232
OFF_RIP = 0xF8
OFF_RSP = 0x98
OFF_FLAGS = 0x30

def get_d2r_pid():
    r = subprocess.run(['tasklist','/FI','IMAGENAME eq D2R.exe','/FO','CSV','/NH'],
                       capture_output=True, text=True)
    for line in r.stdout.strip().split('\n'):
        if 'D2R' in line:
            return int(line.split(',')[1].strip('"'))
    return 0

def get_threads(pid):
    snap = k32.CreateToolhelp32Snapshot(0x4, 0)
    te = TE32(); te.dwSize = ctypes.sizeof(te)
    threads = []
    if k32.Thread32First(snap, ctypes.byref(te)):
        while True:
            if te.th32OwnerProcessID == pid:
                threads.append(te.th32ThreadID)
            if not k32.Thread32Next(snap, ctypes.byref(te)): break
    k32.CloseHandle(snap)
    return threads

def get_module_base(pid):
    """Get D2R.exe module base via readmem endpoint if available, else via EnumProcessModulesEx."""
    try:
        r = subprocess.run(['curl','-s','http://127.0.0.1:52559/debug/readmem?character=Blizzard&offset=0x0&len=2'],
                          capture_output=True, text=True, timeout=2)
        d = json.loads(r.stdout)
        if 'addr' in d:
            return int(d['addr'], 16)
    except:
        pass
    # Fallback: use psapi
    psapi = ctypes.windll.psapi
    h = k32.OpenProcess(0x0410, False, pid)  # PROCESS_QUERY_INFORMATION | PROCESS_VM_READ
    if not h: return 0
    mods = (ctypes.c_void_p * 1024)()
    needed = wt.DWORD()
    psapi.EnumProcessModulesEx(h, mods, ctypes.sizeof(mods), ctypes.byref(needed), 3)
    base = mods[0] if needed.value > 0 else 0
    k32.CloseHandle(h)
    return base or 0

def sample_thread_rip(tid):
    h = k32.OpenThread(0x004A, False, tid)  # GET_CONTEXT|SUSPEND|QUERY
    if not h: return None
    k32.SuspendThread(h)
    ctx = (ctypes.c_ubyte * CONTEXT_SIZE)()
    struct.pack_into('<I', ctx, OFF_FLAGS, 0x100001 | 0x100002)  # CONTROL | INTEGER
    ok = ntdll.NtGetContextThread(h, ctx)
    rip = struct.unpack_from('<Q', ctx, OFF_RIP)[0] if ok == 0 else 0
    rsp = struct.unpack_from('<Q', ctx, OFF_RSP)[0] if ok == 0 else 0
    k32.ResumeThread(h)
    k32.CloseHandle(h)
    return (rip, rsp) if rip else None

def main():
    samples = int(sys.argv[1]) if len(sys.argv) > 1 else 200
    pid = get_d2r_pid()
    if not pid:
        print("D2R not found")
        return
    
    base = get_module_base(pid)
    threads = get_threads(pid)
    print(f"D2R PID={pid} base=0x{base:X} threads={len(threads)} samples={samples}")
    
    d2r_end = base + 0x3000000  # approximate module end
    
    # Track: which threads have RIP inside D2R code
    in_d2r_count = Counter()      # tid -> count
    in_d2r_rvas = defaultdict(set) # tid -> set of RVAs seen
    in_kernel_count = Counter()   # tid -> count in kernel wait
    total_samples = Counter()     # tid -> total samples
    
    print(f"Sampling {samples} times...")
    for s in range(samples):
        for tid in threads:
            result = sample_thread_rip(tid)
            if result is None: continue
            rip, rsp = result
            total_samples[tid] += 1
            rva = rip - base
            if base <= rip < d2r_end:
                in_d2r_count[tid] += 1
                in_d2r_rvas[tid].add(rva)
            else:
                in_kernel_count[tid] += 1
    
    print(f"\n{'='*70}")
    print(f"{'TID':>6} {'D2R%':>6} {'Hits':>5} {'Kernel':>7} {'Unique RVAs':>12}  Top RVAs")
    print(f"{'='*70}")
    
    # Sort by D2R hit count (game thread = most time in D2R code)
    for tid in sorted(threads, key=lambda t: in_d2r_count[t], reverse=True):
        total = total_samples[tid]
        if total == 0: continue
        d2r = in_d2r_count[tid]
        kern = in_kernel_count[tid]
        pct = d2r * 100 / total if total else 0
        rvas = sorted(in_d2r_rvas[tid])
        rva_str = ', '.join(f'0x{r:X}' for r in rvas[:5])
        if d2r > 0:
            # Check if any RVA is near game loop (0x89000-0x8A000)
            near_game = any(0x89000 <= r <= 0x8A000 for r in rvas)
            tag = " <<< GAME LOOP" if near_game else ""
            print(f"{tid:>6} {pct:>5.1f}% {d2r:>5} {kern:>7} {len(rvas):>12}  [{rva_str}]{tag}")
    
    # Summary
    top = max(threads, key=lambda t: in_d2r_count[t]) if threads else 0
    print(f"\nMost likely GAME THREAD: TID={top} ({in_d2r_count[top]} D2R hits)")
    print(f"Use: curl /debug/set-game-tid?character=Blizzard&tid={top}")

if __name__ == '__main__':
    main()
