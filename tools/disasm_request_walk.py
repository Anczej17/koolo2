"""
disasm_request_walk.py — find xrefs to RVA 0x81B40 (request_walk candidate)
and dump the function body so we can identify its calling convention.

Output: logs/request_walk_disasm.txt
"""

import json
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
DUMP_BIN = REPO / "logs" / "d2r_exec.bin"
DUMP_JSON = REPO / "logs" / "d2r_exec.json"
REPORT = REPO / "logs" / "request_walk_disasm.txt"

D2R_IMAGE_BASE = 0x7FF79D3D0000
TARGET_RVA = 0x81BD0  # big function with mode-aware walk/run packet builder
TARGET_VA = D2R_IMAGE_BASE + TARGET_RVA
DUMP_SIZE = 0x800
WALK_CALL_RVA = 0x81F9A  # call site to send_fn we found in scanner


def load_dump():
    meta = json.loads(DUMP_JSON.read_text())
    chunks = [c for c in meta["chunks"] if 0x7FF700000000 <= c["va"] < 0x7FF800000000]
    data = DUMP_BIN.read_bytes()
    return data, chunks


def chunk_at(va, chunks):
    for c in chunks:
        if c["va"] <= va < c["va"] + c["size"]:
            return c
    return None


def read_at(va, n, data, chunks):
    """Read up to n bytes; returns whatever is available (may be shorter)."""
    out = bytearray()
    cur = va
    remaining = n
    while remaining > 0:
        c = chunk_at(cur, chunks)
        if c is None:
            break  # gap — return what we have
        offset = cur - c["va"]
        avail = c["size"] - offset
        take = min(avail, remaining)
        out.extend(data[c["file_off"] + offset:c["file_off"] + offset + take])
        cur += take
        remaining -= take
    return bytes(out) if out else None


def find_callers(target_va, data, chunks):
    """Find all `E8/E9 disp32` and `FF 15 disp32` referencing target_va."""
    sites = []
    for c in chunks:
        cd = data[c["file_off"]:c["file_off"] + c["size"]]
        base = c["va"]
        L = len(cd) - 5
        i = 0
        while i < L:
            b = cd[i]
            if b in (0xE8, 0xE9):
                disp = int.from_bytes(cd[i + 1:i + 5], "little", signed=True)
                if base + i + 5 + disp == target_va:
                    sites.append({"va": base + i, "kind": "call" if b == 0xE8 else "jmp"})
            i += 1
    return sites


def find_function_start_via_padding(va, data, chunks, max_back=0x1000):
    """Walk backwards looking for 3+ CC bytes (function alignment padding)."""
    cur = va
    for _ in range(max_back):
        b = read_at(cur - 3, 3, data, chunks)
        if b is None:
            return None
        if b == b"\xCC\xCC\xCC":
            return cur
        cur -= 1
    return None


def find_function_end(start_va, data, chunks, max_size=0x2000):
    """Walk forward looking for ret followed by padding."""
    cur = start_va
    for _ in range(max_size):
        b = read_at(cur, 4, data, chunks)
        if b is None:
            return None
        if b[0] == 0xC3:  # ret
            # Check for padding after
            nxt = read_at(cur + 1, 3, data, chunks)
            if nxt and nxt[0] == 0xCC:
                return cur + 1
        cur += 1
    return None


def hex_chunks(data, width=16):
    out = []
    for i in range(0, len(data), width):
        chunk = data[i:i + width]
        h = " ".join(f"{b:02x}" for b in chunk)
        out.append(f"  +0x{i:04x}  {h}")
    return out


def find_qword_xrefs(target_va, data, chunks):
    """Find every 8-byte qword inside the dump that equals target_va.
    These are vtable entries / function pointers."""
    needle = target_va.to_bytes(8, "little")
    sites = []
    for c in chunks:
        cd = data[c["file_off"]:c["file_off"] + c["size"]]
        idx = 0
        while True:
            j = cd.find(needle, idx)
            if j < 0:
                break
            sites.append(c["va"] + j)
            idx = j + 1
    return sites


def main():
    data, chunks = load_dump()
    print(f"[+] target = 0x{TARGET_VA:X}  RVA 0x{TARGET_RVA:X}")

    callers = find_callers(TARGET_VA, data, chunks)
    print(f"[+] direct callers (E8/E9): {len(callers)}")

    qrefs = find_qword_xrefs(TARGET_VA, data, chunks)
    print(f"[+] qword xrefs (vtable/funcptr): {len(qrefs)}")

    # Dump function body
    fn_bytes = read_at(TARGET_VA, DUMP_SIZE, data, chunks)
    fn_end = find_function_end(TARGET_VA, data, chunks, max_size=DUMP_SIZE)
    fn_size = (fn_end - TARGET_VA + 1) if fn_end else DUMP_SIZE
    fn_bytes = read_at(TARGET_VA, fn_size, data, chunks) or fn_bytes
    if fn_bytes is None:
        fn_bytes = b""

    lines = []
    lines.append(f"=== request_walk RE: fn @ RVA 0x{TARGET_RVA:X} ===")
    lines.append(f"VA: 0x{TARGET_VA:X}")
    lines.append(f"size: {fn_size} bytes (end 0x{(TARGET_VA+fn_size):X})")
    lines.append("")
    lines.append(f"=== qword xrefs (likely vtable/funcptr) ({len(qrefs)}) ===")
    for q in qrefs[:50]:
        rva = q - D2R_IMAGE_BASE if q >= D2R_IMAGE_BASE else q
        lines.append(f"  ptr @ 0x{q:X}  RVA 0x{rva:X}")
    lines.append("")
    lines.append(f"=== direct callers ({len(callers)}) ===")
    for s in callers:
        rva = s["va"] - D2R_IMAGE_BASE
        lines.append(f"  {s['kind']} @ 0x{s['va']:X}  RVA 0x{rva:X}")
        # 64B context before each caller
        ctx = read_at(s["va"] - 64, 64, data, chunks)
        if ctx:
            lines.append(f"    pre: {ctx.hex()}")
        # Find enclosing function
        fn = find_function_start_via_padding(s["va"], data, chunks)
        if fn:
            lines.append(f"    enclosing fn @ 0x{fn:X}  RVA 0x{fn-D2R_IMAGE_BASE:X}")
    lines.append("")
    lines.append(f"=== fn body (hex dump, {fn_size}B) ===")
    lines.extend(hex_chunks(fn_bytes))

    REPORT.write_text("\n".join(lines))
    print(f"[done] {REPORT}")


if __name__ == "__main__":
    main()
