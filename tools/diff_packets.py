#!/usr/bin/env python3
"""
Compares two bufpoll logs, prints unique packets in NEW that weren't in OLD.
A "packet" is identified by (opcode, full hex prefix up to first long zero run).
"""

import sys
import re

def trim(hex_str):
    # Drop trailing zeros — find first run of 6+ zeros
    m = re.search(r'(0{12,})', hex_str)
    return hex_str[:m.start()] if m else hex_str

def parse_log(path):
    """Returns set of trimmed unique packet hex strings + dict opcode->[trimmed packets]."""
    seen = set()
    by_op = {}
    with open(path) as f:
        for line in f:
            m = re.search(r'op=0x([0-9a-fA-F]+) hex=([0-9a-f]+)', line)
            if not m: continue
            op_int = int(m.group(1), 16)
            hex_full = m.group(2)
            t = trim(hex_full)
            if not t: continue
            seen.add(t)
            by_op.setdefault(op_int, set()).add(t)
    return seen, by_op

def main():
    if len(sys.argv) < 3:
        print("usage: diff_packets.py <baseline_log> <new_log>")
        sys.exit(2)
    base_seen, base_op = parse_log(sys.argv[1])
    new_seen, new_op = parse_log(sys.argv[2])

    # Opcodes that are NEW (didn't appear in baseline)
    new_opcodes = set(new_op.keys()) - set(base_op.keys())
    print(f"NEW opcodes (not in baseline): {sorted(f'0x{o:02X}' for o in new_opcodes)}")

    print(f"\nUnique packets per NEW opcode (max 5 examples each):")
    for op in sorted(new_op.keys()):
        marker = "*** NEW ***" if op in new_opcodes else ""
        examples = list(new_op[op])[:5]
        print(f"\n  opcode 0x{op:02X}  ({len(new_op[op])} unique forms)  {marker}")
        for ex in examples:
            print(f"    {ex}")

if __name__ == "__main__":
    main()
