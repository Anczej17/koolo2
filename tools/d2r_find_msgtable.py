#!/usr/bin/env python3
"""
Search the entire D2R dump for data patterns of the shape:
   uint32 msg_id (== 0x100..0x300, e.g. 0x201)
   uint32 padding
   uint64 fn_ptr   (in D2R image range)

i.e. a struct { UINT msg; void* handler; } table that maps WM_* messages to
handler functions.
"""
import json, struct, sys

D2R_BASE = 0x7ff79d3d0000
D2R_END  = 0x7ff79fb52000

WM_RANGE = set([
    0x0001,0x0002,0x0010,0x0014,0x0020,0x0024,0x0046,0x0047,
    0x0100,0x0101,0x0102,0x0104,0x0105,
    0x0200,0x0201,0x0202,0x0203,0x0204,0x0205,0x020A,0x020E,
    0x0231,0x0232,0x0282,
])

def main():
    with open("logs/d2r_exec.json") as f:
        meta = json.load(f)
    chunks = meta["chunks"]
    with open("logs/d2r_exec.bin","rb") as f:
        data = f.read()

    # Walk every 4-byte aligned slot inside every chunk searching for WM_LBUTTONDOWN.
    # We want u32==0x201 followed within 16 bytes by a u64 in D2R image range.
    found = []
    for c in chunks:
        d = data[c["file_off"]:c["file_off"]+c["size"]]
        for i in range(0, len(d)-12, 4):
            v = struct.unpack_from("<I", d, i)[0]
            if v != 0x201:
                continue
            # check candidate fn ptrs at +0, +4, +8, +12 (qword)
            for k in (4, 8):
                if i + k + 8 > len(d): continue
                p = struct.unpack_from("<Q", d, i+k)[0]
                if D2R_BASE <= p < D2R_END:
                    va = c["va"] + i
                    found.append((va, k, p))
    print(f"# {len(found)} candidate (0x201, fn_ptr) pairs")
    for va, k, p in found[:50]:
        print(f"  data 0x{va:x}  +0x{k:x} -> fn 0x{p:x}")

    # Same for 0x200 (WM_MOUSEMOVE) - rarer
    print("\n# also 0x200 entries adjacent to D2R fn ptrs:")
    found2 = []
    for c in chunks:
        d = data[c["file_off"]:c["file_off"]+c["size"]]
        for i in range(0, len(d)-12, 4):
            v = struct.unpack_from("<I", d, i)[0]
            if v != 0x200: continue
            for k in (4, 8):
                if i + k + 8 > len(d): continue
                p = struct.unpack_from("<Q", d, i+k)[0]
                if D2R_BASE <= p < D2R_END:
                    found2.append((c["va"]+i, k, p))
    for va,k,p in found2[:30]:
        print(f"  data 0x{va:x}  +0x{k:x} -> fn 0x{p:x}")

if __name__ == "__main__":
    main()
