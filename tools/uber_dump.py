"""uber_dump.py — single-shot stash flow capture for 0x19 packet RE.

Captures 4 timepoints of D2R memory + bufpoll continuous + VEH register dump.
Driven by user prompts. Output to build/uber_dumps/uber_<TS>/.

Phase: T0 baseline -> open stash (packet 0x41) -> T1
       -> HID Ctrl+Click manual -> T2
       -> bot send 0x19 -> T3 + VEH grab
Then: binary diff T1->T2 (HID effect) and T2->T3 (bot send effect).

Usage:
    python tools/uber_dump.py [--port N] [--char Blizzard]
"""
from __future__ import annotations

import argparse
import binascii
import json
import os
import shutil
import subprocess
import sys
import time
import urllib.request
import urllib.parse
from pathlib import Path
from datetime import datetime

# Memory regions to dump per timepoint. Each is (offset_from_base, size_bytes, name).
# Covers: networkMgr, UI/widget state, unitTable, panelMgr, gameMgr, playerPos,
# mirror buffer, automap. ~7 MB per timepoint * 4 = ~28 MB total on disk.
REGIONS = [
    (0x019E0000, 0x010000, "network_19e"),       # networkMgr 0x19ED860
    (0x01D00000, 0x100000, "ui_state_1d0"),      # gameTick 0x1D59420, hover, exp
    (0x01E00000, 0x100000, "panel_inv_1e0"),     # panelMgr 0x1E11E40, roster, charData
    (0x01EA0000, 0x010000, "unit_table_1ea"),    # unitTable, serverUnitTable
    (0x01EC0000, 0x020000, "player_widget_1ec"), # playerPos 0x1EC3FCC, gameQuit
    (0x01ED0000, 0x020000, "game_mgr_1ed"),      # gameManager 0x1EDF6F8, WidgetStates 0x1EDF700
    (0x01F20000, 0x040000, "mirror_automap_1f2"),# automap 0x1F29C00, mirror 0x1F51330
    (0x01F50000, 0x010000, "mirror_buf_1f5"),    # focused mirror buffer area
]

# Bot endpoints.
def url(port: int, endpoint: str, **q) -> str:
    qs = urllib.parse.urlencode(q)
    return f"http://127.0.0.1:{port}{endpoint}?{qs}"

def get_json(port: int, endpoint: str, **q) -> dict:
    try:
        with urllib.request.urlopen(url(port, endpoint, **q), timeout=30) as r:
            return json.loads(r.read().decode("utf-8", "replace"))
    except Exception as e:
        return {"error": str(e)}

def get_text(port: int, endpoint: str, **q) -> str:
    try:
        with urllib.request.urlopen(url(port, endpoint, **q), timeout=30) as r:
            return r.read().decode("utf-8", "replace")
    except Exception as e:
        return json.dumps({"error": str(e)})

def bot_log_path() -> Path:
    """Bot log lives at $TEMP/bot.log (cross-shell-friendly resolution)."""
    candidates = [
        Path(os.environ.get("TEMP", "")) / "bot.log",
        Path("C:/Users/Administrator/AppData/Local/Temp/bot.log"),
        Path("/tmp/bot.log"),
    ]
    for c in candidates:
        try:
            if c.exists():
                return c
        except OSError:
            pass
    return candidates[0]

def get_active_d2r_pid() -> int:
    """Get the ACTIVE D2R PID from /tmp/bot.log most recent 'alive pid=' or
    'init presenter pid=' line. PID numbers in Windows can be reused so we
    can't just take max(tasklist) — bot's watchdog logs are authoritative."""
    log_path = bot_log_path()
    if not log_path.exists():
        return 0
    last_pid = 0
    for line in log_path.read_text(encoding="utf-8", errors="replace").splitlines():
        for token in ("alive pid=", "init presenter\" pid=", "init presenter pid=", " pid="):
            if token in line and ("watchdog" in line or "presenter" in line):
                idx = line.find(token) + len(token)
                rest = line[idx:].split()[0].rstrip(",\"")
                try:
                    last_pid = int(rest)
                except ValueError:
                    pass
                break
        # also handle "EXITED pid=N" — invalidate
        if "D2R EXITED" in line and "pid=" in line:
            idx = line.find("pid=") + 4
            rest = line[idx:].split()[0].rstrip(" ")
            try:
                exited = int(rest)
                if exited == last_pid:
                    last_pid = 0
            except ValueError:
                pass
    return last_pid

def fetch_d2r_pid_and_base(port: int, char: str) -> tuple[int, int]:
    """PID = latest D2R from tasklist; base addr = LAST 'send fn' line in bot.log."""
    pid = get_active_d2r_pid()
    base = 0
    log_path = bot_log_path()
    if log_path.exists():
        for line in log_path.read_text(encoding="utf-8", errors="replace").splitlines():
            if "base=0x" in line and "send fn" in line.lower():
                idx = line.find("base=0x")
                if idx >= 0:
                    s = line[idx+5:].split()[0].rstrip(" +")
                    try:
                        base = int(s, 16)  # take LAST occurrence (loop continues)
                    except ValueError:
                        pass
    return pid, base

def find_dump_file(filename: str) -> Path | None:
    """Bot CWD is build/, so dumprange writes to build/build/dumps/."""
    candidates = [
        Path("build/build/dumps") / filename,
        Path("build/dumps") / filename,
    ]
    for c in candidates:
        if c.exists():
            return c
    return None

def dump_region(port: int, char: str, off: int, size: int, out_name: str, out_dir: Path) -> bool:
    """Call /debug/dumprange in 64MB chunks. Endpoint writes to bot-CWD/build/dumps/."""
    chunk = 0x4000000  # 64 MB
    parts = []
    idx = 0
    remaining = size
    cur_off = off
    while remaining > 0:
        take = min(chunk, remaining)
        out_file = f"_uber_{out_name}_{idx:02d}.bin"
        resp = get_json(port, "/debug/dumprange", character=char,
                        offset=hex(cur_off), size=hex(take), out=out_file)
        if resp.get("error"):
            print(f"  ! dump {out_name} chunk {idx} failed: {resp['error']}")
            return False
        src = find_dump_file(out_file)
        if src:
            parts.append(src)
        else:
            print(f"  ! dumprange resp ok but file not found: {out_file}")
            return False
        idx += 1
        remaining -= take
        cur_off += take
    final = out_dir / f"{out_name}.bin"
    with open(final, "wb") as fout:
        for p in parts:
            with open(p, "rb") as fin:
                shutil.copyfileobj(fin, fout)
            try:
                p.unlink()
            except OSError:
                pass
    return True

def dump_all_regions(port: int, char: str, out_dir: Path, label: str) -> dict:
    """Snapshot all regions to out_dir/<label>/."""
    target = out_dir / label
    target.mkdir(parents=True, exist_ok=True)
    print(f"[{label}] dumping {len(REGIONS)} regions...")
    sizes = {}
    t0 = time.time()
    for off, size, name in REGIONS:
        ok = dump_region(port, char, off, size, name, target)
        if ok:
            f = target / f"{name}.bin"
            sizes[name] = f.stat().st_size if f.exists() else 0
        else:
            sizes[name] = 0
    dt = time.time() - t0
    print(f"[{label}] dumped in {dt:.1f}s, total {sum(sizes.values())/1024/1024:.1f} MB")
    return sizes

def snapshot_state(port: int, char: str, out_dir: Path, label: str):
    """Grab inventory + gamestate + crash-info + snifflog snapshot."""
    target = out_dir / label
    target.mkdir(parents=True, exist_ok=True)
    for ep in ["gamestate", "inventory", "npcs", "crash-info"]:
        text = get_text(port, f"/debug/{ep}", character=char)
        (target / f"{ep}.json").write_text(text, encoding="utf-8")

def start_bufpoll(d2r_pid: int, out_dir: Path, duration: int = 600) -> subprocess.Popen | None:
    """Spawn bufpoll.exe in background, write to uber dir."""
    log = out_dir / "bufpoll.log"
    bufpoll = Path("build/bufpoll.exe")
    if not bufpoll.exists():
        print(f"  ! bufpoll.exe not found at {bufpoll}")
        return None
    proc = subprocess.Popen(
        [str(bufpoll), "-pid", str(d2r_pid), "-duration", str(duration),
         "-out", str(log), "-interval", "5", "-window", "256"],
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
    )
    print(f"[bufpoll] started pid={proc.pid} duration={duration}s -> {log}")
    return proc

def diff_region(a: Path, b: Path) -> list[tuple[int, int]]:
    """Byte-level diff. Returns list of (offset, run_length) for differing runs."""
    if not a.exists() or not b.exists():
        return []
    da = a.read_bytes()
    db = b.read_bytes()
    n = min(len(da), len(db))
    runs = []
    i = 0
    while i < n:
        if da[i] != db[i]:
            start = i
            while i < n and da[i] != db[i]:
                i += 1
            runs.append((start, i - start))
        else:
            i += 1
    return runs

def diff_dir(d1: Path, d2: Path, label: str, report_lines: list[str]):
    """Diff every region between two snapshot dirs, emit summary."""
    report_lines.append(f"\n## Diff {d1.name} -> {d2.name}\n")
    total_changed = 0
    for off, size, name in REGIONS:
        a = d1 / f"{name}.bin"
        b = d2 / f"{name}.bin"
        runs = diff_region(a, b)
        bytes_changed = sum(r[1] for r in runs)
        total_changed += bytes_changed
        if runs:
            report_lines.append(f"### {name} (region base+0x{off:08X}, size 0x{size:X})")
            report_lines.append(f"- runs: {len(runs)}, bytes changed: {bytes_changed}")
            shown = 0
            for ro, rl in runs:
                if shown >= 32:
                    report_lines.append(f"- ... and {len(runs)-shown} more runs")
                    break
                abs_off = off + ro
                # show first 16 bytes of the diff in both files
                da = a.read_bytes()
                db = b.read_bytes()
                ha = binascii.hexlify(da[ro:ro+min(rl,16)]).decode()
                hb = binascii.hexlify(db[ro:ro+min(rl,16)]).decode()
                report_lines.append(f"  - +0x{ro:08X} (abs base+0x{abs_off:08X}) len={rl}")
                report_lines.append(f"    A: {ha}")
                report_lines.append(f"    B: {hb}")
                shown += 1
    report_lines.append(f"\n**Total bytes changed: {total_changed}**\n")

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--port", type=int, default=0, help="bot port (default: read /tmp/bot.port)")
    ap.add_argument("--char", default="Blizzard")
    ap.add_argument("--out-base", default="build/uber_dumps")
    ap.add_argument("--bufpoll-duration", type=int, default=900)
    ap.add_argument("--no-bot-send", action="store_true",
                    help="skip the final bot send 0x19 (use to capture only HID baseline)")
    ap.add_argument("--t1-mode", choices=["hid", "packet"], default="hid",
                    help="how to open stash for T1: hid (user opens between T0 and T1) or packet (try 0x41)")
    ap.add_argument("--wait-after-t0-sec", type=int, default=20,
                    help="seconds to wait after T0 baseline (gives user time to open stash via HID)")
    ap.add_argument("--wait-after-t1-sec", type=int, default=0,
                    help="seconds to wait after T1 (gives user time to manually Ctrl+Click an item). If 0, uses /debug/clickitem auto.")
    ap.add_argument("--target-item", default="",
                    help="comma-separated substrings to look for in inventory item names for HID/T3 target (default: gems/runes/jewels)")
    ap.add_argument("--phase", choices=["all", "t0", "t1", "t2", "t3", "diff"], default="all",
                    help="run only one phase (for stepwise user-driven runs). Reuses --session-id dir.")
    ap.add_argument("--session-id", default="",
                    help="reuse an existing uber dump session dir (timestamp). Required for phase != all.")
    args = ap.parse_args()

    if args.port == 0:
        port_file = Path("/tmp/bot.port")
        if port_file.exists():
            args.port = int(port_file.read_text().strip())
        else:
            print("--port not given and /tmp/bot.port missing"); sys.exit(2)
    print(f"[init] port={args.port} char={args.char}")

    pid, base = fetch_d2r_pid_and_base(args.port, args.char)
    print(f"[init] D2R pid={pid} base=0x{base:X}")
    if pid == 0:
        print("D2R not detected from gamestate"); sys.exit(2)

    ts = datetime.now().strftime("%Y%m%d_%H%M%S")
    out_dir = Path(args.out_base) / f"uber_{ts}"
    out_dir.mkdir(parents=True, exist_ok=True)
    print(f"[init] output dir: {out_dir}")

    # Save initial info
    init_info = {
        "ts": ts, "port": args.port, "char": args.char,
        "d2r_pid": pid, "d2r_base_hex": f"0x{base:X}",
        "regions": [{"offset_hex": f"0x{o:X}", "size_hex": f"0x{s:X}", "name": n} for o,s,n in REGIONS],
    }
    (out_dir / "init.json").write_text(json.dumps(init_info, indent=2))

    bufpoll_proc = start_bufpoll(pid, out_dir, args.bufpoll_duration)

    try:
        # PRE-CHECK: walk to bank if needed (1 click at screen ~700,200 walks
        # NE world ~7 tiles from spawn — empirically tested 2026-04-15).
        gs = get_json(args.port, "/debug/gamestate", character=args.char)
        px = gs.get("player_pos", {}).get("x", 0)
        py = gs.get("player_pos", {}).get("y", 0)
        dist = ((px - 4466)**2 + (py - 4629)**2) ** 0.5
        print(f"\n[pre] player at ({px}, {py}), dist to bank ~{dist:.1f} tiles")
        walk_attempts = 0
        while dist > 2.5 and walk_attempts < 4:
            print(f"[pre] walking toward bank — click (700, 200) attempt {walk_attempts+1}")
            r = get_json(args.port, "/debug/walkpacket",
                         character=args.char, x=700, y=200)
            time.sleep(2)
            gs = get_json(args.port, "/debug/gamestate", character=args.char)
            new_px = gs.get("player_pos", {}).get("x", 0)
            new_py = gs.get("player_pos", {}).get("y", 0)
            new_dist = ((new_px - 4466)**2 + (new_py - 4629)**2) ** 0.5
            print(f"[pre] now at ({new_px}, {new_py}), dist={new_dist:.1f}")
            if new_dist >= dist:
                print("[pre] !! walk did not reduce distance, aborting walk loop")
                break
            px, py, dist = new_px, new_py, new_dist
            walk_attempts += 1
        if dist > 2.5:
            print(f"[pre] !! still too far from bank ({dist:.1f}). Continuing anyway.")

        # T0 — baseline (player at bank, NO stash open)
        print("\n=== T0: baseline (player at bank, stash CLOSED) ===")
        snapshot_state(args.port, args.char, out_dir, "t0_baseline")
        dump_all_regions(args.port, args.char, out_dir, "t0_baseline")

        # T1 — stash OPEN. First try packet 0x41 (auto). If the diff T0->T1 is
        # tiny (suggesting stash didn't actually open), give user a chance.
        if args.t1_mode == "packet":
            print("\n=== T1: open stash via 0x41 packet ===")
            open_pkt = "411100000000000000FFFFFFFF"
            r = get_json(args.port, "/debug/sendpacket",
                         character=args.char, hex=open_pkt, path="game")
            print(f"  0x41 packet result: {r}")
            time.sleep(1.5)
            if args.wait_after_t0_sec > 0:
                wait = args.wait_after_t0_sec
                print(f"  >>> Stash should now be open. If NOT visible, open it manually within {wait}s.")
                for sec in range(wait, 0, -1):
                    if sec % 5 == 0 or sec <= 5:
                        print(f"  ... {sec}s remaining", flush=True)
                    time.sleep(1)
        else:
            wait = args.wait_after_t0_sec
            print(f"\n=== Waiting {wait}s for user to open stash via HID ===")
            print(">>> USER: open stash NOW (HID click on bank object). Skript will continue automatically.")
            for sec in range(wait, 0, -1):
                if sec % 5 == 0 or sec <= 5:
                    print(f"  ... {sec}s remaining", flush=True)
                time.sleep(1)
        print("\n=== T1: stash open snapshot ===")
        snapshot_state(args.port, args.char, out_dir, "t1_stash_open")
        dump_all_regions(args.port, args.char, out_dir, "t1_stash_open")

        # T2 — HID Ctrl+Click move
        print("\n=== T2: HID Ctrl+Click move ===")
        inv_pre = get_json(args.port, "/debug/inventory", character=args.char)
        items = inv_pre.get("items", []) if isinstance(inv_pre.get("items"), list) else []
        # priority list: small replaceable items
        hid_target = None
        if args.target_item:
            tokens = [t.strip() for t in args.target_item.split(",") if t.strip()]
            for tk in tokens:
                for it in items:
                    if it.get("location", "") == "inventory" and tk in it.get("name", ""):
                        hid_target = it
                        break
                if hid_target:
                    break
        if hid_target is None:
            priority_names = ["Topaz", "Sapphire", "Ruby", "Emerald", "Amethyst", "Diamond", "Skull",
                              "Rune", "Jewel"]
            for pn in priority_names:
                for it in items:
                    if it.get("location", "") == "inventory" and pn in it.get("name", ""):
                        hid_target = it
                        break
                if hid_target:
                    break
        if hid_target is None:
            for it in items:
                if it.get("location", "") == "inventory" and "Charm" not in it.get("name", ""):
                    hid_target = it
                    break
        target_gid = None
        target_loc_pre = None
        if hid_target is None:
            print("  ! no good HID-click target; T2 will be a no-op")
        else:
            target_gid = hid_target.get("gid", 0)
            target_loc_pre = hid_target.get("location", "?")
            print(f"  target item: {hid_target.get('name','?')} gid={target_gid} loc={target_loc_pre} at ({hid_target.get('x')},{hid_target.get('y')})")

        if args.wait_after_t1_sec > 0:
            print(f"  >>> USER: Ctrl+Click manually on item gid={target_gid} ({hid_target.get('name','?') if hid_target else '?'}). Skript waits {args.wait_after_t1_sec}s.")
            for sec in range(args.wait_after_t1_sec, 0, -1):
                if sec % 5 == 0 or sec <= 5:
                    print(f"  ... {sec}s remaining", flush=True)
                time.sleep(1)
        elif hid_target is not None:
            r = get_json(args.port, "/debug/clickitem",
                         character=args.char, itemGID=hex(target_gid), modifier="ctrl")
            print(f"  /debug/clickitem result: {r}")
            time.sleep(1.5)

        # verify
        inv_post = get_json(args.port, "/debug/inventory", character=args.char)
        items_post = inv_post.get("items", []) if isinstance(inv_post.get("items"), list) else []
        moved_to = None
        if target_gid is not None:
            for it in items_post:
                if it.get("gid") == target_gid:
                    moved_to = it.get("location", "?")
                    break
        print(f"  target item now at location: {moved_to} (was: {target_loc_pre})")
        if moved_to == target_loc_pre:
            print("  !! item did NOT move — stash may be closed or HID/clickitem failed")
        else:
            print(f"  item MOVED: {target_loc_pre} -> {moved_to}")
        # snapshot post-HID
        snapshot_state(args.port, args.char, out_dir, "t2_post_hid")
        dump_all_regions(args.port, args.char, out_dir, "t2_post_hid")

        # T3 — bot sends 0x19 packet, expect crash
        if not args.no_bot_send:
            print("\n=== T3: bot send 0x19 packet ===")
            # Snapshot PID + VEH counters BEFORE send so we can detect a D2R restart
            # (which would invalidate the T3 dump as a "post-send state")
            pre_pid = get_active_d2r_pid()
            pre_crash = get_json(args.port, "/debug/crash-info", character=args.char)
            pre_av = pre_crash.get("av", -1)
            pre_so = pre_crash.get("so", -1)
            pre_count = pre_crash.get("count", -1)
            print(f"  pre-send: D2R pid={pre_pid} VEH av={pre_av} so={pre_so} count={pre_count}")
            inv = get_json(args.port, "/debug/inventory", character=args.char)
            items3 = inv.get("items", []) if isinstance(inv.get("items"), list) else []
            target_item = None
            for it in items3:
                if it.get("location", "") == "inventory" and "Charm" not in it.get("name", ""):
                    target_item = it
                    break
            if target_item is None:
                for it in items3:
                    if it.get("location", "") == "inventory":
                        target_item = it
                        break
            if target_item is None:
                print("  ! no inventory item left for T3 send; skipping")
            else:
                gid = target_item.get("gid", 0)
                if isinstance(gid, str):
                    gid = int(gid, 0)
                # build 0x19 21B packet: [opcode][gid:u32][marker=06:u32][col=2:u32][row=2:u32][pad:u32=0]
                col = 3; row = 3
                pkt = bytearray(21)
                pkt[0] = 0x19
                pkt[1:5] = gid.to_bytes(4, "little")
                pkt[5:9] = (6).to_bytes(4, "little")
                pkt[9:13] = col.to_bytes(4, "little")
                pkt[13:17] = row.to_bytes(4, "little")
                hex_pkt = pkt.hex()
                print(f"  target item GID=0x{gid:X} name={target_item.get('name','?')}")
                print(f"  sending: {hex_pkt} (path=dual)")
                send_resp = get_json(args.port, "/debug/sendpacket",
                                     character=args.char, hex=hex_pkt, path="dual")
                send_ts = time.time()
                print(f"  send result: {send_resp}")
                # immediate post-send PID and VEH counters to detect crash
                post_pid = get_active_d2r_pid()
                post_crash = get_json(args.port, "/debug/crash-info", character=args.char)
                post_av = post_crash.get("av", -1)
                post_so = post_crash.get("so", -1)
                post_count = post_crash.get("count", -1)
                print(f"  post-send: D2R pid={post_pid} VEH av={post_av} so={post_so} count={post_count}")
                pid_changed = (pre_pid != post_pid)
                veh_av_delta = post_av - pre_av if pre_av >= 0 and post_av >= 0 else None
                veh_count_delta = post_count - pre_count if pre_count >= 0 and post_count >= 0 else None
                print(f"  D2R pid_changed={pid_changed} av_delta={veh_av_delta} count_delta={veh_count_delta}")
                if pid_changed:
                    print("  !! D2R RESTARTED — T3 dump will be from a NEW process, not post-send state")
                # quick T3 grabs at increasing intervals to capture failure window
                for i, sleep_ms in enumerate([50, 200, 500, 1500]):
                    time.sleep(sleep_ms / 1000)
                    snapshot_state(args.port, args.char, out_dir, f"t3_post_send_{i}")
                # one big region dump (best-effort, may partially fail if D2R dying)
                dump_all_regions(args.port, args.char, out_dir, "t3_post_send")
                # crash info
                ci = get_text(args.port, "/debug/crash-info", character=args.char)
                (out_dir / "t3_crash_info.json").write_text(ci, encoding="utf-8")
                # save send timing
                (out_dir / "t3_send_meta.json").write_text(json.dumps({
                    "packet_hex": hex_pkt, "send_ts": send_ts,
                    "send_resp": send_resp,
                    "target_item": {"gid": f"0x{gid:X}", "name": target_item.get("name", "?")},
                    "col": col, "row": row,
                    "pre_send": {"d2r_pid": pre_pid, "veh_av": pre_av, "veh_so": pre_so, "veh_count": pre_count},
                    "post_send": {"d2r_pid": post_pid, "veh_av": post_av, "veh_so": post_so, "veh_count": post_count},
                    "pid_changed": pid_changed,
                    "veh_av_delta": veh_av_delta,
                    "veh_count_delta": veh_count_delta,
                }, indent=2))
        else:
            print("\n=== T3 SKIPPED (--no-bot-send) ===")

    finally:
        if bufpoll_proc:
            try:
                bufpoll_proc.terminate()
                bufpoll_proc.wait(timeout=3)
            except Exception:
                pass

    # Auto diff
    print("\n=== Auto diff ===")
    report = ["# Uber dump report", f"Generated: {ts}", f"Port: {args.port} Char: {args.char}",
              f"D2R pid={pid} base=0x{base:X}", ""]
    snaps = ["t0_baseline", "t1_stash_open", "t2_post_hid"]
    if not args.no_bot_send:
        snaps.append("t3_post_send")
    for i in range(len(snaps) - 1):
        a = out_dir / snaps[i]
        b = out_dir / snaps[i+1]
        if a.exists() and b.exists():
            diff_dir(a, b, f"{snaps[i]} vs {snaps[i+1]}", report)
    # also: t1 vs t3 (full effect of "stash open then bot send", excluding hid baseline)
    if not args.no_bot_send:
        t1d = out_dir / "t1_stash_open"
        t3d = out_dir / "t3_post_send"
        if t1d.exists() and t3d.exists():
            diff_dir(t1d, t3d, "t1_stash_open vs t3_post_send", report)
    rep_path = out_dir / "report.md"
    rep_path.write_text("\n".join(report), encoding="utf-8")
    print(f"  report: {rep_path}")
    print(f"  output dir: {out_dir.resolve()}")

if __name__ == "__main__":
    main()
