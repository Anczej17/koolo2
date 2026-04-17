"""
packet_size_extract.py — extract DEFINITIVE packet sizes from D2R's code.

Scans ALL callers of send_fn (RVA 0x146600) and for each caller:
1. Reads 256 bytes BEFORE the CALL instruction
2. Finds the `mov edx, <imm>` instruction that sets the packet size argument
3. Finds the opcode byte being written to the packet buffer
4. Reports: (caller_RVA, opcode, size) — the GROUND TRUTH from D2R's own code

send_fn signature: void send_fn(RCX=pkt_ptr, RDX=size, R8D=channel)
RDX patterns to search:
  BA xx xx xx xx        = mov edx, imm32
  41 BA xx xx xx xx     = mov r10d, imm32 (if later moved to rdx)
  48 C7 C2 xx xx xx xx  = mov rdx, imm64 (rare)
  B2 xx                 = mov dl, imm8

For opcode, search for C6 (mov byte ptr) with the opcode literal.

Usage:
  python tools/packet_size_extract.py
"""
import json
import struct
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
DUMP_BIN = REPO / "logs" / "d2r_exec.bin"
DUMP_JSON = REPO / "logs" / "d2r_exec.json"

SEND_FN_RVA = 0x146600
D2R_IMAGE_BASE = 0x7FF79D3D0000


def load_dump():
    meta = json.loads(DUMP_JSON.read_text())
    chunks = meta["chunks"]
    data = DUMP_BIN.read_bytes()
    d2r = [c for c in chunks if 0x7FF700000000 <= c["va"] < 0x7FF800000000]
    return data, d2r


def read_at(va, n, data, d2r):
    out = bytearray()
    cur = va
    remaining = n
    while remaining > 0:
        c = None
        for ch in d2r:
            if ch["va"] <= cur < ch["va"] + ch["size"]:
                c = ch
                break
        if c is None:
            return None
        offset_in_chunk = cur - c["va"]
        avail = c["size"] - offset_in_chunk
        take = min(avail, remaining)
        file_off = c["file_off"] + offset_in_chunk
        out.extend(data[file_off:file_off + take])
        cur += take
        remaining -= take
    return bytes(out)


def find_call_sites(data, d2r, target_va):
    sites = []
    for c in d2r:
        chunk_data = data[c["file_off"]:c["file_off"] + c["size"]]
        base = c["va"]
        L = len(chunk_data) - 5
        i = 0
        while i < L:
            b = chunk_data[i]
            if b == 0xE8:
                disp = struct.unpack_from("<i", chunk_data, i + 1)[0]
                next_ip = base + i + 5
                if next_ip + disp == target_va:
                    sites.append(base + i)
            i += 1
    return sites


def extract_size_from_window(window):
    """Find mov edx, imm32 (BA xx xx xx xx) in window. Return all matches."""
    sizes = []
    for i in range(len(window) - 4):
        # BA xx xx xx xx = mov edx, imm32
        if window[i] == 0xBA:
            val = struct.unpack_from("<I", window, i + 1)[0]
            if 1 <= val <= 1024:  # sane packet size range
                sizes.append((i, val, "mov_edx_imm32"))

        # 41 BA xx xx xx xx = mov r10d, imm32
        if i + 5 < len(window) and window[i] == 0x41 and window[i+1] == 0xBA:
            val = struct.unpack_from("<I", window, i + 2)[0]
            if 1 <= val <= 1024:
                sizes.append((i, val, "mov_r10d_imm32"))

        # 8D 56 xx = lea edx, [rsi+imm8]
        if i + 2 < len(window) and window[i] == 0x8D and window[i+1] == 0x56:
            val = window[i+2]
            if 1 <= val <= 255:
                sizes.append((i, val, "lea_edx_rsi_imm8"))

        # 48 8D 56 xx = lea rdx, [rsi+imm8]
        if i + 3 < len(window) and window[i] == 0x48 and window[i+1] == 0x8D and window[i+2] == 0x56:
            val = window[i+3]
            if 1 <= val <= 255:
                sizes.append((i, val, "lea_rdx_rsi_imm8"))

        # B2 xx = mov dl, imm8
        if window[i] == 0xB2:
            val = window[i+1]
            if 1 <= val <= 255:
                sizes.append((i, val, "mov_dl_imm8"))

    return sizes


def extract_opcodes_from_window(window):
    """Find all byte literals being written via C6 (mov byte ptr) instructions."""
    opcodes = []
    for i in range(len(window) - 2):
        if window[i] == 0xC6:
            modrm = window[i+1]
            mod = (modrm >> 6) & 3
            rm = modrm & 7
            if mod == 3:
                continue
            ins_len = 2
            if rm == 4:
                ins_len += 1  # SIB
            if mod == 0 and rm == 5:
                ins_len += 4
            elif mod == 1:
                ins_len += 1
            elif mod == 2:
                ins_len += 4
            if i + ins_len < len(window):
                imm = window[i + ins_len]
                if imm != 0:  # skip zero writes
                    opcodes.append((i, imm, "C6_mov_byte"))
    return opcodes


def main():
    print("Loading D2R dump...", flush=True)
    data, d2r = load_dump()
    target = D2R_IMAGE_BASE + SEND_FN_RVA
    print(f"Scanning callers of send_fn @ 0x{target:X}...")

    sites = find_call_sites(data, d2r, target)
    print(f"Found {len(sites)} call sites\n")

    # Also scan callers-of-callers: send_fn might be wrapped in a helper
    # that is itself called from packet builders. Find callers of each
    # direct caller's enclosing function.
    # For now, focus on direct callers.

    results = []
    for site_va in sorted(sites):
        rva = site_va - D2R_IMAGE_BASE
        # Read 256 bytes before the call
        window_size = 256
        window = read_at(site_va - window_size, window_size, data, d2r)
        if window is None:
            print(f"  0x{rva:06X}: cannot read window")
            continue

        sizes = extract_size_from_window(window)
        opcodes = extract_opcodes_from_window(window)

        # Pick the LAST size instruction (closest to the call) as most relevant
        best_size = sizes[-1] if sizes else None
        # Pick distinct opcodes
        unique_ops = sorted(set(o[1] for o in opcodes))

        results.append({
            "rva": rva,
            "va": site_va,
            "sizes": sizes,
            "opcodes": opcodes,
            "best_size": best_size,
            "unique_ops": unique_ops,
            "window": window,
        })

    print("=" * 72)
    print(f"{'CALLER RVA':<14} {'SIZE':>6}  {'SIZE_KIND':<20} {'OPCODES IN WINDOW'}")
    print("=" * 72)

    for r in results:
        sz = r["best_size"]
        sz_str = f"{sz[1]}" if sz else "?"
        sz_kind = sz[2] if sz else ""
        ops_str = ", ".join(f"0x{o:02X}" for o in r["unique_ops"])
        print(f"  0x{r['rva']:06X}    {sz_str:>4}  {sz_kind:<20} {ops_str}")

        # Print all size candidates
        for s in r["sizes"]:
            off, val, kind = s
            print(f"              size candidate: offset -{window_size - s[0]:3d}  "
                  f"value={val:4d}  kind={kind}")

    # Summary table
    print("\n" + "=" * 72)
    print("OPCODE => SIZE MAPPING (from D2R code)")
    print("=" * 72)

    # Collect all opcode=>size mappings
    op_size_map = {}
    for r in results:
        if r["best_size"] is None:
            continue
        sz = r["best_size"][1]
        for op in r["unique_ops"]:
            if op not in op_size_map:
                op_size_map[op] = set()
            op_size_map[op].add(sz)

    for op in sorted(op_size_map.keys()):
        sizes = sorted(op_size_map[op])
        sizes_str = ", ".join(str(s) for s in sizes)
        print(f"  0x{op:02X} => {sizes_str} bytes")

    # Also write to file
    out_path = REPO / "logs" / "packet_sizes_from_code.txt"
    with open(out_path, "w") as f:
        f.write("OPCODE => SIZE MAPPING (extracted from D2R.exe code)\n")
        f.write(f"send_fn RVA: 0x{SEND_FN_RVA:X}\n")
        f.write(f"Total callers: {len(sites)}\n\n")
        for op in sorted(op_size_map.keys()):
            sizes = sorted(op_size_map[op])
            f.write(f"0x{op:02X} => {', '.join(str(s) for s in sizes)} bytes\n")
        f.write("\nDetailed caller analysis:\n")
        for r in results:
            sz = r["best_size"]
            sz_str = f"{sz[1]}" if sz else "?"
            ops_str = ", ".join(f"0x{o:02X}" for o in r["unique_ops"])
            f.write(f"  RVA 0x{r['rva']:06X}  size={sz_str}  opcodes=[{ops_str}]\n")
            for s in r["sizes"]:
                f.write(f"    size@-{256-s[0]:3d}: {s[1]:4d} ({s[2]})\n")

    print(f"\nSaved to {out_path}")


if __name__ == "__main__":
    main()
