"""
Systematically test thread hijack with SAFE 0x3C on each D2R thread.
Finds which thread can successfully call dual_send_wrap.

Usage: python test_hijack_threads.py PORT
"""
import sys, subprocess, json, time
import ctypes, ctypes.wintypes as wt

PORT = int(sys.argv[1]) if len(sys.argv) > 1 else 52990

def curl(path):
    try:
        r = subprocess.run(['curl', '-s', f'http://127.0.0.1:{PORT}{path}'],
                          capture_output=True, text=True, timeout=5)
        return json.loads(r.stdout) if r.stdout.strip() else {}
    except:
        return {}

def get_threads():
    k32 = ctypes.windll.kernel32
    class TE(ctypes.Structure):
        _fields_ = [('dwSize',wt.DWORD),('u',wt.DWORD),('tid',wt.DWORD),('pid',wt.DWORD),
                    ('bp',ctypes.c_long),('dp',ctypes.c_long),('f',wt.DWORD)]
    r = subprocess.run(['tasklist','/FI','IMAGENAME eq D2R.exe','/FO','CSV','/NH'],
                       capture_output=True, text=True)
    pid = int(r.stdout.strip().split(',')[1].strip('"'))
    snap = k32.CreateToolhelp32Snapshot(4, 0)
    te = TE(); te.dwSize = ctypes.sizeof(te)
    tids = []
    if k32.Thread32First(snap, ctypes.byref(te)):
        while True:
            if te.pid == pid: tids.append(te.tid)
            if not k32.Thread32Next(snap, ctypes.byref(te)): break
    k32.CloseHandle(snap)
    return pid, tids

# Check D2R alive and get initial skill
gs = curl('/debug/gamestate?character=Blizzard')
if not gs.get('area'):
    print("ERROR: D2R not in game")
    sys.exit(1)

initial_skill = gs.get('right_skill', 0)
print(f"Initial right_skill = {initial_skill}")

# Verify dual_send_wrap decrypted
dsw = curl('/debug/readmem?character=Blizzard&offset=0x147110&len=4')
if dsw.get('hex', '') == '00000000':
    print("ERROR: dual_send_wrap NOT decrypted. Open vendor first!")
    sys.exit(1)
print(f"dual_send_wrap: {dsw.get('hex', '?')}")

pid, tids = get_threads()
print(f"\nD2R PID={pid}, {len(tids)} threads")
print(f"Testing 0x3C hijack on each thread (Frozen Armor=40 vs current={initial_skill})...")
print(f"{'='*60}")

# Target skill: toggle between Frozen Armor (40=0x28) and current
target_skill = 40 if initial_skill != 40 else 59  # Blizzard=59
target_hex = f"3C{target_skill:02X}000000FFFFFFFF"

for i, tid in enumerate(tids[:15]):  # Test first 15 threads
    # Set game TID
    curl(f'/debug/set-game-tid?character=Blizzard&tid={tid}')

    # Check D2R alive before test
    gs = curl('/debug/gamestate?character=Blizzard')
    if not gs.get('area'):
        print(f"  [{i:2d}] TID={tid:5d}: D2R DEAD — aborting")
        break

    before = gs.get('right_skill', -1)

    # Send 0x3C via dualwrap-gt (thread hijack)
    result = curl(f'/debug/sendpacket?character=Blizzard&hex={target_hex}&path=dualwrap-gt')

    time.sleep(0.3)  # Wait for state update

    # Check result
    gs2 = curl('/debug/gamestate?character=Blizzard')
    if not gs2.get('area'):
        print(f"  [{i:2d}] TID={tid:5d}: D2R CRASHED or FROZEN after hijack!")
        break

    after = gs2.get('right_skill', -1)
    ok = result.get('ok', False)
    err = result.get('error', '')

    status = "TIMEOUT" if 'timeout' in err.lower() else ("OK" if ok else f"ERR: {err[:40]}")
    changed = "SKILL CHANGED!" if after != before else "no change"

    print(f"  [{i:2d}] TID={tid:5d}: {status:45s} skill {before}->{after} {changed}")

    if after != before:
        print(f"\n*** FOUND WORKING THREAD: TID={tid} ***")
        print(f"Use: curl /debug/set-game-tid?character=Blizzard&tid={tid}")
        break

print("\nDone.")
