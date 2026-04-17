#@author packet-rebrand
#@category D2R
#@menupath Tools.D2R.Load Dump
#@toolbar
#
# Loads a d2r_dump_exec.py output (.bin + .json sidecar) into the
# currently-open Ghidra program. Each chunk from the JSON becomes its own
# MemoryBlock placed at the correct virtual address.
#
# Usage:
#   1. File -> New Project (any kind, name it "D2R_RE")
#   2. File -> New -> Empty File ("D2R_runtime", language x86:LE:64:default,
#      compiler "windows")
#   3. Open the empty program in CodeBrowser.
#   4. Window -> Script Manager -> Run this script.
#   5. When prompted, pick logs/section_id_hot.bin
#   6. Script auto-loads all 1930 chunks (~12 MB code) at proper VAs.
#   7. Run Analysis (Analysis -> Auto Analyze) afterwards.

import json
import os

from ghidra.program.model.address import AddressFactory
from ghidra.program.model.mem import MemoryBlockType, MemoryConflictException
from ghidra.app.util.importer import MessageLog
from ghidra.util.task import TaskMonitor


def main():
    bin_path = askFile("Select D2R dump .bin", "Load").getAbsolutePath()
    json_path = bin_path[:-4] + ".json" if bin_path.endswith(".bin") else bin_path + ".json"

    if not os.path.exists(json_path):
        print("ERROR: sidecar not found: " + json_path)
        return

    print("Loading sidecar: " + json_path)
    with open(json_path, "r") as f:
        meta = json.load(f)
    chunks = meta["chunks"]
    print("  total chunks: " + str(len(chunks)))

    # We only want D2R.exe range
    D2R_BASE = 0x7ff79d3d0000
    D2R_END  = 0x7ff79fb52000
    d2r_chunks = [c for c in chunks if D2R_BASE <= c["va"] < D2R_END]
    print("  D2R chunks:   " + str(len(d2r_chunks)))

    print("Reading bin: " + bin_path)
    with open(bin_path, "rb") as f:
        data = f.read()
    print("  bin size:     " + str(len(data)))

    program = currentProgram
    af = program.getAddressFactory()
    space = af.getDefaultAddressSpace()
    mem = program.getMemory()

    txn = program.startTransaction("Load D2R dump")
    try:
        added = 0
        for i, c in enumerate(d2r_chunks):
            va = c["va"]
            size = c["size"]
            off = c["file_off"]
            chunk_bytes = data[off:off + size]

            addr = space.getAddress(va)
            block_name = "d2r_%08x" % va
            try:
                blk = mem.createInitializedBlock(
                    block_name, addr, size, b"\x00", TaskMonitor.DUMMY, False
                )
                blk.putBytes(addr, chunk_bytes)
                blk.setRead(True)
                blk.setWrite(False)
                blk.setExecute(True)
                added += 1
            except MemoryConflictException as e:
                # Adjacent chunks may overlap; just skip — ASLR and Arxan
                # commonly produce duplicate ranges across dumps.
                pass
            except Exception as e:
                print("  ERROR @ " + hex(va) + ": " + str(e))

            if (i + 1) % 200 == 0:
                print("  ... " + str(i + 1) + "/" + str(len(d2r_chunks)) + " (added " + str(added) + ")")

        print("DONE: " + str(added) + " blocks added")
    finally:
        program.endTransaction(txn, True)


main()
