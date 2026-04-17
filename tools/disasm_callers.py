"""
disasm_callers.py — Disassemble ALL known send_fn callers from the decrypted
D2R memory dump. Extract packet opcode, size, and full assembly for each.

Uses capstone for x86-64 disassembly on the RUNTIME memory dump (decrypted
by Arxan), not the encrypted on-disk binary.
"""
import json
import struct
from pathlib import Path
from capstone import Cs, CS_ARCH_X86, CS_MODE_64

REPO = Path(__file__).resolve().parent.parent
DUMP_BIN = REPO / "logs" / "d2r_exec.bin"
DUMP_JSON = REPO / "logs" / "d2r_exec.json"
D2R_IMAGE_BASE = 0x7FF79D3D0000
SEND_FN_VA = D2R_IMAGE_BASE + 0x146600

# Known call site VAs (from walk_builder_scan.py)
CALL_SITES = [
    D2R_IMAGE_BASE + 0x08058C,
    D2R_IMAGE_BASE + 0x081F9A,
    D2R_IMAGE_BASE + 0x1468A7,
    D2R_IMAGE_BASE + 0x146972,
    D2R_IMAGE_BASE + 0x146D5A,
    D2R_IMAGE_BASE + 0x146DDB,
    D2R_IMAGE_BASE + 0x146E5B,
    D2R_IMAGE_BASE + 0x146ED4,
]


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
            out.extend(b'\x00' * remaining)
            break
        offset_in_chunk = cur - c["va"]
        avail = c["size"] - offset_in_chunk
        take = min(avail, remaining)
        file_off = c["file_off"] + offset_in_chunk
        out.extend(data[file_off:file_off + take])
        cur += take
        remaining -= take
    return bytes(out)


def find_function_start(va, data, d2r, max_back=0x2000):
    """Walk backwards looking for CC CC CC padding (function boundary)."""
    for off in range(1, max_back):
        b = read_at(va - off, 3, data, d2r)
        if b == b'\xCC\xCC\xCC':
            return va - off + 3
    return va - 256  # fallback


def disasm_function(start_va, data, d2r, max_bytes=2048):
    """Disassemble from start_va, stop at RET or max_bytes."""
    code = read_at(start_va, max_bytes, data, d2r)
    md = Cs(CS_ARCH_X86, CS_MODE_64)
    md.detail = True

    lines = []
    for insn in md.disasm(code, start_va):
        rva = insn.address - D2R_IMAGE_BASE
        lines.append((insn.address, rva, insn.mnemonic, insn.op_str, insn.bytes))

        # Stop at ret
        if insn.mnemonic == 'ret':
            break

    return lines


def analyze_caller(call_site_va, data, d2r):
    """Find the enclosing function for a call site and disassemble it."""
    func_start = find_function_start(call_site_va, data, d2r)
    insns = disasm_function(func_start, data, d2r)

    # Find interesting patterns
    result = {
        'call_site_rva': call_site_va - D2R_IMAGE_BASE,
        'func_start_rva': func_start - D2R_IMAGE_BASE,
        'insns': insns,
        'mov_edx': [],  # size arguments
        'mov_byte': [],  # opcode writes
        'call_send_fn': [],  # actual calls to send_fn
    }

    for addr, rva, mnemonic, op_str, raw_bytes in insns:
        # mov edx, imm32 (BA xx xx xx xx)
        if raw_bytes[0] == 0xBA and len(raw_bytes) == 5:
            val = struct.unpack_from("<I", bytes(raw_bytes), 1)[0]
            if 1 <= val <= 2048:
                result['mov_edx'].append((rva, val))

        # lea edx, [reg+imm8]
        if mnemonic == 'lea' and op_str.startswith('edx'):
            # Try to extract immediate
            for b in raw_bytes:
                pass  # complex, skip for now

        # mov byte ptr [xxx], imm8 — opcode writes
        if mnemonic == 'mov' and 'byte ptr' in op_str:
            # Last byte of instruction is the immediate
            imm = raw_bytes[-1]
            if imm != 0:
                result['mov_byte'].append((rva, imm))

        # call to send_fn
        if mnemonic == 'call' and len(raw_bytes) == 5 and raw_bytes[0] == 0xE8:
            disp = struct.unpack_from("<i", bytes(raw_bytes), 1)[0]
            target = addr + 5 + disp
            if target == SEND_FN_VA:
                result['call_send_fn'].append(rva)

    return result


def main():
    print("Loading dump...", flush=True)
    data, d2r = load_dump()
    print(f"D2R chunks: {len(d2r)}")

    out_lines = []
    out_lines.append("=" * 80)
    out_lines.append("D2R PACKET CALLER DISASSEMBLY (from decrypted memory dump)")
    out_lines.append(f"Image base: 0x{D2R_IMAGE_BASE:X}")
    out_lines.append(f"send_fn: 0x{SEND_FN_VA:X} (RVA 0x146600)")
    out_lines.append("=" * 80)

    for call_va in CALL_SITES:
        rva = call_va - D2R_IMAGE_BASE
        print(f"\nAnalyzing caller at RVA 0x{rva:X}...", flush=True)

        result = analyze_caller(call_va, data, d2r)

        out_lines.append(f"\n{'='*60}")
        out_lines.append(f"CALL SITE RVA: 0x{rva:X}")
        out_lines.append(f"FUNCTION START RVA: 0x{result['func_start_rva']:X}")
        out_lines.append(f"send_fn calls in function: {result['call_send_fn']}")
        out_lines.append(f"mov edx (size candidates): {result['mov_edx']}")
        out_lines.append(f"mov byte (opcode candidates): {[(hex(r), hex(v)) for r,v in result['mov_byte']]}")
        out_lines.append(f"{'='*60}")

        # Print full disassembly (up to 100 instructions)
        for addr, func_rva, mnemonic, op_str, raw_bytes in result['insns'][:150]:
            hexb = raw_bytes.hex()
            marker = ""
            if func_rva in [r for r, _ in result['mov_edx']]:
                marker = "  <-- SIZE"
            if func_rva in [r for r, _ in result['mov_byte']]:
                marker = "  <-- OPCODE BYTE"
            if func_rva in result['call_send_fn']:
                marker = "  <-- CALL send_fn"
            out_lines.append(f"  0x{func_rva:06X}  {hexb:<24s}  {mnemonic:<8s} {op_str}{marker}")

    out_path = REPO / "logs" / "disasm_send_fn_callers.txt"
    out_path.write_text("\n".join(out_lines))
    print(f"\nOutput: {out_path}")

    # Summary
    print("\n=== SUMMARY ===")
    for call_va in CALL_SITES:
        rva = call_va - D2R_IMAGE_BASE
        result = analyze_caller(call_va, data, d2r)
        sizes = [v for _, v in result['mov_edx']]
        ops = [hex(v) for _, v in result['mov_byte'] if v > 0x10]
        calls = result['call_send_fn']
        print(f"  RVA 0x{rva:06X}: sizes={sizes} opcodes={ops} send_fn_calls={[hex(c) for c in calls]}")


if __name__ == "__main__":
    main()
