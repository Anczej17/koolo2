#!/usr/bin/env python3
"""Merge multiple D2R memory dumps into a single .bin + .json.

Each input is a (.bin, .json) pair from tools/d2r_dump_exec.py. Different
dumps capture different decrypted code pages because Arxan/anti-tamper
on-demand-decrypts pages as they're executed. Merging by VA (latest write
wins for collisions) gives the most complete picture of D2R's .text.

Output format matches d2r_dump_exec.py (chunks list with file_off/va/size)
so all existing Ghidra/capstone scripts work unchanged.
"""

import json
import os
import sys

INPUTS = [
    # Historical dumps at base 0x7ff79d3d0000. Live d2r_exec_live is excluded
    # because D2R was relaunched after these and has a different ASLR base —
    # rebasing would corrupt RIP-relative refs and IAT entries.
    "logs/section_id_hunt",          # 12.9MB — biggest, most pages
    "logs/section_sec02_walk_during",
    "logs/section_sec01_walk",
    "logs/section_id_hot",
    "logs/section_baseline",
    "logs/d2r_exec",
]
OUT_PREFIX = "logs/d2r_exec_merged"
PAGE = 0x1000


def main():
    # Map: page_va -> bytes
    pages = {}
    src_count = {}

    for prefix in INPUTS:
        bin_path = prefix + ".bin"
        json_path = prefix + ".json"
        if not os.path.exists(bin_path) or not os.path.exists(json_path):
            print(f"  skip: {prefix} (missing)", file=sys.stderr)
            continue

        meta = json.load(open(json_path))
        chunks = meta.get("chunks", [])
        new_pages = 0
        with open(bin_path, "rb") as f:
            for c in chunks:
                f.seek(c["file_off"])
                data = f.read(c["size"])
                # Split into 4KB pages keyed by VA
                base = c["va"]
                for off in range(0, len(data), PAGE):
                    page_va = (base + off) & ~(PAGE - 1)
                    if page_va not in pages:
                        page_data = data[off:off + PAGE]
                        # Pad to 4KB if shortRead
                        if len(page_data) < PAGE:
                            page_data = page_data + b"\x00" * (PAGE - len(page_data))
                        pages[page_va] = page_data
                        new_pages += 1
        src_count[prefix] = new_pages
        print(f"  loaded {prefix}: +{new_pages} new pages (total now {len(pages)})", file=sys.stderr)

    # Sort by VA, write merged binary, build chunks index
    sorted_vas = sorted(pages.keys())
    out_chunks = []
    file_off = 0
    with open(OUT_PREFIX + ".bin", "wb") as f:
        # Coalesce contiguous pages into single chunks for compactness
        run_start = None
        run_data = bytearray()
        for i, va in enumerate(sorted_vas):
            if run_start is None:
                run_start = va
                run_data = bytearray(pages[va])
            elif va == run_start + len(run_data):
                run_data.extend(pages[va])
            else:
                # Flush previous run
                f.write(run_data)
                out_chunks.append({"file_off": file_off, "va": run_start, "size": len(run_data)})
                file_off += len(run_data)
                run_start = va
                run_data = bytearray(pages[va])
        if run_start is not None:
            f.write(run_data)
            out_chunks.append({"file_off": file_off, "va": run_start, "size": len(run_data)})
            file_off += len(run_data)

    with open(OUT_PREFIX + ".json", "w") as f:
        json.dump({"chunks": out_chunks}, f)

    print(f"\nmerged {len(pages)} pages into {len(out_chunks)} runs", file=sys.stderr)
    print(f"total bytes: {sum(c['size'] for c in out_chunks):,}", file=sys.stderr)
    print(f"output: {OUT_PREFIX}.bin + {OUT_PREFIX}.json", file=sys.stderr)
    print(f"\ncontribution per source:", file=sys.stderr)
    for k, v in src_count.items():
        print(f"  {k}: {v} unique pages", file=sys.stderr)


if __name__ == "__main__":
    main()
