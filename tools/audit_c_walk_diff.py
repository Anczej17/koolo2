#!/usr/bin/env python
"""AUDIT C: PlayerUnit destination-field hunt via memory diffing.

Captures snapshots of PlayerUnit (and serverUnitTable region) over time.
Read-only. Never writes to D2R memory.
"""
import json
import sys
import time
import urllib.request
from pathlib import Path

PORT = 49719
CHAR = "Blizzard"
LOG_DIR = Path(r"C:\Users\Administrator\Desktop\Audyt Koolo\koolo2-rebranding\logs")
PU_SIZE = 8192   # bytes around PlayerUnit (was 4096; widen scan)
PATH_SIZE = 256  # path struct
SU_SIZE = 4096
SUT_OFFSET = 0x1EA8BD0  # Diobyte serverUnitTable RVA (currently empty)


def get(url):
    with urllib.request.urlopen(url, timeout=5) as r:
        return json.loads(r.read().decode())


def gamestate():
    return get(f"http://localhost:{PORT}/debug/gamestate?character={CHAR}")


def _readmem_abs_chunk(addr, length):
    url = f"http://localhost:{PORT}/debug/readmem?character={CHAR}&addr=0x{addr:X}&len={length}&abs=1"
    d = get(url)
    if not d.get("ok"):
        raise RuntimeError(f"readmem failed: {d}")
    return bytes.fromhex(d["hex"])


def readmem_abs(addr, length):
    """Stitch reads in <=4096 byte chunks (endpoint cap)."""
    out = bytearray()
    chunk = 4096
    remaining = length
    cur = addr
    while remaining > 0:
        n = min(chunk, remaining)
        out += _readmem_abs_chunk(cur, n)
        cur += n
        remaining -= n
    return bytes(out)


def readmem_off(off, length):
    url = f"http://localhost:{PORT}/debug/readmem?character={CHAR}&offset=0x{off:X}&len={length}"
    d = get(url)
    if not d.get("ok"):
        raise RuntimeError(f"readmem failed: {d}")
    return bytes.fromhex(d["hex"]), int(d["addr"], 16)


def take_snapshot(label, pu_addr, path_addr, sut_ptr_addr):
    pu = readmem_abs(pu_addr, PU_SIZE)
    (LOG_DIR / f"AUDIT_C_snap_{label}_pu.bin").write_bytes(pu)
    try:
        pa = readmem_abs(path_addr, PATH_SIZE)
        (LOG_DIR / f"AUDIT_C_snap_{label}_path.bin").write_bytes(pa)
    except Exception as e:
        print(f"  path read failed: {e}")
    # Try server unit table region: read pointer, then deref
    sut_chunk = b""
    sut_resolved = 0
    try:
        sut_ptr_bytes = readmem_abs(sut_ptr_addr, 8)
        sut_resolved = int.from_bytes(sut_ptr_bytes, "little")
        # serverUnitTable is a hash table base — read 4KB at base
        if sut_resolved and sut_resolved > 0x10000 and sut_resolved < 0x7FFFFFFFFFFF:
            try:
                sut_chunk = readmem_abs(sut_resolved, SU_SIZE)
            except Exception as e:
                print(f"  sut deref read failed: {e}")
    except Exception as e:
        print(f"  sut ptr read failed: {e}")
    if sut_chunk:
        (LOG_DIR / f"AUDIT_C_snap_{label}_sut.bin").write_bytes(sut_chunk)
    pa = (LOG_DIR / f"AUDIT_C_snap_{label}_path.bin").read_bytes() if (LOG_DIR / f"AUDIT_C_snap_{label}_path.bin").exists() else b""
    print(f"  snap {label}: PU {len(pu)}B, PATH {len(pa)}B, SUT {len(sut_chunk)}B (resolved=0x{sut_resolved:X})")
    return pu, sut_chunk, sut_resolved, pa


def diff_bytes(a, b, c, d=None):
    """Yield (offset, a_byte, b_byte, c_byte, d_byte) for offsets that differ."""
    n = min(len(a), len(b), len(c))
    if d is not None:
        n = min(n, len(d))
    for i in range(n):
        if d is not None:
            if not (a[i] == b[i] == c[i] == d[i]):
                yield i, a[i], b[i], c[i], d[i]
        else:
            if not (a[i] == b[i] == c[i]):
                yield i, a[i], b[i], c[i], None


def classify(a, b, c, d):
    """stable | noise | output | INPUT-CANDIDATE
    A vs B = baseline (still). C vs D = walking phase.
    INPUT: stable in A&B, changed in C (and possibly D).
    NOISE: differs in A&B as well.
    OUTPUT: stable in A&B, but D differs from C (rewritten every tick during walk)
            -- with no INPUT signature, treat as output.
    """
    ab = a == b
    cd = c == d if d is not None else True
    if not ab:
        return "noise"
    # ab stable
    if a == c:
        return "stable"
    # baseline stable, walking changed
    if cd:
        return "INPUT-CANDIDATE"  # changed once, then locked
    return "output"


def fmt_dword(buf, off):
    if off + 4 > len(buf):
        return "----"
    return f"0x{int.from_bytes(buf[off:off+4], 'little'):08X}"


def main():
    LOG_DIR.mkdir(exist_ok=True)
    gs = gamestate()
    print("gamestate:", json.dumps({k: gs[k] for k in ("player_unit_addr","path_addr","player_pos","area","in_town")}))
    pu_addr = int(gs["player_unit_addr"], 16)
    path_addr = int(gs["path_addr"], 16)

    # Find image base from offset=0 read
    _, image_base = readmem_off(0, 4)
    sut_ptr_addr = image_base + SUT_OFFSET
    print(f"D2R image_base = 0x{image_base:X}")
    print(f"sut_ptr addr   = 0x{sut_ptr_addr:X}")
    print(f"player_unit    = 0x{pu_addr:X}")
    print(f"path           = 0x{path_addr:X}")

    print("\n[A] snapshot (still)")
    a_pu, a_sut, sut_resolved, a_pa = take_snapshot("A_still1", pu_addr, path_addr, sut_ptr_addr)

    print("[wait 5s] (player still)")
    time.sleep(5)
    print("[B] snapshot (still 5s later)")
    b_pu, b_sut, _, b_pa = take_snapshot("B_still2", pu_addr, path_addr, sut_ptr_addr)

    # The bot is paused -> we cannot trigger walk via in-game logic.
    # Try /debug/sendpacket with the walk opcode if possible. Otherwise mark blocker.
    triggered = False
    # Walk-to packet: opcode 0x03 RUN-TO X(le16) Y(le16); use a delta of +5,+5
    px = gs["player_pos"]["x"]
    py = gs["player_pos"]["y"]
    tx = px + 5
    ty = py + 5
    walk_hex = f"03{tx & 0xFF:02x}{(tx>>8)&0xFF:02x}{ty & 0xFF:02x}{(ty>>8)&0xFF:02x}"
    try:
        url = f"http://localhost:{PORT}/debug/sendpacket?character={CHAR}&hex={walk_hex}"
        r = get(url)
        print(f"sendpacket(walk) -> {r}")
        triggered = bool(r.get("ok"))
    except Exception as e:
        print(f"sendpacket failed: {e}")

    # Snapshot C immediately
    time.sleep(0.05)
    print("[C] snapshot (just after walk trigger)")
    c_pu, c_sut, _, c_pa = take_snapshot("C_walk1", pu_addr, path_addr, sut_ptr_addr)

    time.sleep(0.3)
    print("[D] snapshot (mid walk)")
    d_pu, d_sut, _, d_pa = take_snapshot("D_walk2", pu_addr, path_addr, sut_ptr_addr)

    # Build diff table for PlayerUnit
    diff_rows = []
    for off, av, bv, cv, dv in diff_bytes(a_pu, b_pu, c_pu, d_pu):
        diff_rows.append((off, av, bv, cv, dv))

    # Build per-DWORD-aligned classification (more meaningful for fields)
    interesting = []
    seen_dword = set()
    for off, *_ in diff_rows:
        d4 = off & ~3
        if d4 in seen_dword:
            continue
        seen_dword.add(d4)
        a_d = int.from_bytes(a_pu[d4:d4+4], "little")
        b_d = int.from_bytes(b_pu[d4:d4+4], "little")
        c_d = int.from_bytes(c_pu[d4:d4+4], "little")
        dd = int.from_bytes(d_pu[d4:d4+4], "little")
        cls = classify(a_d, b_d, c_d, dd)
        interesting.append((d4, a_d, b_d, c_d, dd, cls))

    # SUT diff (if available)
    sut_interesting = []
    if a_sut and b_sut and c_sut and d_sut:
        seen_dword = set()
        for off, *_ in diff_bytes(a_sut, b_sut, c_sut, d_sut):
            d4 = off & ~3
            if d4 in seen_dword:
                continue
            seen_dword.add(d4)
            a_d = int.from_bytes(a_sut[d4:d4+4], "little")
            b_d = int.from_bytes(b_sut[d4:d4+4], "little")
            c_d = int.from_bytes(c_sut[d4:d4+4], "little")
            dd = int.from_bytes(d_sut[d4:d4+4], "little")
            cls = classify(a_d, b_d, c_d, dd)
            sut_interesting.append((d4, a_d, b_d, c_d, dd, cls))

    # Write report
    md = [
        "# AUDIT C — PlayerUnit destination-field diff",
        "",
        f"- player_unit_addr: 0x{pu_addr:X}",
        f"- path_addr:        0x{path_addr:X}",
        f"- D2R image base:   0x{image_base:X}",
        f"- serverUnitTable ptr addr: 0x{sut_ptr_addr:X}  (resolved=0x{sut_resolved:X})",
        f"- player position at A: ({px}, {py})  walk target tested: ({tx}, {ty})",
        f"- walk trigger via /debug/sendpacket: {'OK' if triggered else 'FAILED — see blocker'}",
        "",
        "## PlayerUnit DWORD-level diffs (any byte that ever changed)",
        "",
        "| offset | A_still1 | B_still2 | C_walk1 | D_walk2 | class |",
        "|-------:|---------:|---------:|--------:|--------:|------|",
    ]
    for off, a_d, b_d, c_d, dd, cls in interesting:
        md.append(f"| 0x{off:04X} | 0x{a_d:08X} | 0x{b_d:08X} | 0x{c_d:08X} | 0x{dd:08X} | {cls} |")

    md += [
        "",
        f"Total changed dwords in PlayerUnit: {len(interesting)}",
        "",
        "## Classification summary",
    ]
    counts = {}
    for *_, cls in interesting:
        counts[cls] = counts.get(cls, 0) + 1
    for k, v in sorted(counts.items()):
        md.append(f"- {k}: {v}")

    md += [
        "",
        "## INPUT-CANDIDATE fields (top picks)",
        "",
    ]
    cands = [r for r in interesting if r[5] == "INPUT-CANDIDATE"]
    cands.sort(key=lambda r: r[0])
    for off, a_d, b_d, c_d, dd, _ in cands[:20]:
        md.append(f"- +0x{off:04X}: A=B=0x{a_d:08X} -> C=0x{c_d:08X} D=0x{dd:08X}")

    # Path struct diff (every byte)
    md += [
        "",
        "## Path struct diff (byte-level, full 256B)",
        "",
        "| offset | A | B | C | D | class |",
        "|-------:|--:|--:|--:|--:|------|",
    ]
    if a_pa and b_pa and c_pa and d_pa:
        n = min(len(a_pa), len(b_pa), len(c_pa), len(d_pa))
        for i in range(0, n, 4):
            a_d = int.from_bytes(a_pa[i:i+4], "little")
            b_d = int.from_bytes(b_pa[i:i+4], "little")
            c_d = int.from_bytes(c_pa[i:i+4], "little")
            dd  = int.from_bytes(d_pa[i:i+4], "little")
            if a_d == b_d == c_d == dd:
                continue
            cls = classify(a_d, b_d, c_d, dd)
            md.append(f"| 0x{i:04X} | 0x{a_d:08X} | 0x{b_d:08X} | 0x{c_d:08X} | 0x{dd:08X} | {cls} |")

    md += [
        "",
        "## ServerUnitTable region diffs",
        "",
    ]
    if sut_interesting:
        md += [
            "| offset | A | B | C | D | class |",
            "|-------:|---:|---:|---:|---:|------|",
        ]
        for off, a_d, b_d, c_d, dd, cls in sut_interesting[:200]:
            md.append(f"| 0x{off:04X} | 0x{a_d:08X} | 0x{b_d:08X} | 0x{c_d:08X} | 0x{dd:08X} | {cls} |")
    else:
        md.append("(serverUnitTable region not captured — pointer deref empty)")

    (LOG_DIR / "AUDIT_C_playerunit_diff.md").write_text("\n".join(md), encoding="utf-8")
    print(f"\nWrote {LOG_DIR / 'AUDIT_C_playerunit_diff.md'}")
    print(f"PlayerUnit changed dwords: {len(interesting)} | INPUT-CANDIDATE: {sum(1 for r in interesting if r[5]=='INPUT-CANDIDATE')}")
    print(f"counts: {counts}")
    print(f"walk trigger ok: {triggered}")


if __name__ == "__main__":
    main()
