#@author packet-rebrand
#@category D2R
#
# Headless script: builds proper memory map from a d2r_dump_exec.py output
# (.bin + .json), disassembles + decompiles the identify packet builders,
# follows the vtable send call, and writes everything to a text report.
#
# Inputs are passed via environment variables (so the script is reusable
# without GUI prompts):
#
#   D2R_DUMP_BIN  — absolute path to the .bin
#   D2R_TARGETS   — comma-separated VAs in hex (e.g. "0x7ff79d736032,0x7ff79d6fe2e2")
#   D2R_REPORT    — absolute path to the output text file
#
# Run via:
#   analyzeHeadless <project_dir> D2R_RE -import <bin> -loader BinaryLoader \
#     -loader-language x86:LE:64:default -postScript ghidra_decompile_identify.py
#
# Or, if a project + empty program already exist, use -process instead.

import os
import json

from ghidra.program.model.address import AddressFactory
from ghidra.program.model.mem import MemoryConflictException
from ghidra.program.model.lang import OperandType
from ghidra.app.decompiler import DecompInterface, DecompileOptions
from ghidra.util.task import ConsoleTaskMonitor


D2R_BASE = 0x7ff79d3d0000
D2R_END = 0x7ff79fb52000


def env(name, default=None):
    v = os.environ.get(name)
    if v is None:
        return default
    return v


def load_chunks_into_memory(bin_path, json_path):
    print("[load] reading sidecar: " + json_path)
    with open(json_path, "r") as f:
        meta = json.load(f)
    chunks = meta["chunks"]
    d2r = [c for c in chunks if D2R_BASE <= c["va"] < D2R_END]
    print("[load] D2R chunks: " + str(len(d2r)))

    print("[load] reading bin: " + bin_path)
    with open(bin_path, "rb") as f:
        data = f.read()

    program = currentProgram
    af = program.getAddressFactory()
    space = af.getDefaultAddressSpace()
    mem = program.getMemory()

    txn = program.startTransaction("Load D2R dump")
    added = 0
    skipped = 0
    try:
        for i, c in enumerate(d2r):
            va = c["va"]
            size = c["size"]
            off = c["file_off"]
            chunk_bytes = data[off:off + size]

            addr = space.getAddress(va)
            block_name = "d2r_%016x" % va
            try:
                blk = mem.createInitializedBlock(
                    block_name, addr, size, 0, ConsoleTaskMonitor(), False
                )
                blk.putBytes(addr, chunk_bytes)
                blk.setRead(True)
                blk.setWrite(False)
                blk.setExecute(True)
                added += 1
            except MemoryConflictException:
                skipped += 1
            except Exception as e:
                msg = str(e)
                if "conflict" in msg.lower() or "overlap" in msg.lower():
                    skipped += 1
                else:
                    print("[load] ERR @ " + hex(va) + ": " + msg)
            if (i + 1) % 500 == 0:
                print("[load] " + str(i + 1) + "/" + str(len(d2r)) + "  added=" + str(added) + " skipped=" + str(skipped))
    finally:
        program.endTransaction(txn, True)
    print("[load] DONE  added=" + str(added) + "  skipped=" + str(skipped))


def disasm_at(va):
    program = currentProgram
    addr = program.getAddressFactory().getDefaultAddressSpace().getAddress(va)
    if program.getListing().getInstructionAt(addr) is not None:
        return  # already disasmed
    txn = program.startTransaction("disasm")
    try:
        from ghidra.app.cmd.disassemble import DisassembleCommand
        cmd = DisassembleCommand(addr, None, True)
        cmd.applyTo(program, ConsoleTaskMonitor())
    finally:
        program.endTransaction(txn, True)


def find_function_containing(va):
    program = currentProgram
    addr = program.getAddressFactory().getDefaultAddressSpace().getAddress(va)
    fm = program.getFunctionManager()
    fn = fm.getFunctionContaining(addr)
    if fn is not None:
        return fn

    # Create a function at the nearest disasmed instruction
    listing = program.getListing()
    ins = listing.getInstructionContaining(addr)
    if ins is None:
        # try to disasm at this va first
        disasm_at(va)
        ins = listing.getInstructionContaining(addr)
    if ins is None:
        return None

    # Walk backwards looking for a function start (or just create at current addr)
    txn = program.startTransaction("create_fn")
    try:
        from ghidra.app.cmd.function import CreateFunctionCmd
        cmd = CreateFunctionCmd(addr)
        cmd.applyTo(program, ConsoleTaskMonitor())
    finally:
        program.endTransaction(txn, True)

    return fm.getFunctionContaining(addr)


def decompile_function(fn):
    if fn is None:
        return "<no function>"
    iface = DecompInterface()
    iface.setOptions(DecompileOptions())
    iface.openProgram(currentProgram)
    res = iface.decompileFunction(fn, 60, ConsoleTaskMonitor())
    if res is None or not res.decompileCompleted():
        return "<decompile failed>"
    return res.getDecompiledFunction().getC()


def main():
    bin_path = env("D2R_DUMP_BIN")
    if not bin_path:
        print("ERROR: D2R_DUMP_BIN not set")
        return
    json_path = bin_path[:-4] + ".json" if bin_path.endswith(".bin") else bin_path + ".json"
    targets_str = env("D2R_TARGETS", "0x7ff79d736032,0x7ff79d6fe2e2")
    report_path = env("D2R_REPORT", os.path.join(os.path.dirname(bin_path), "ghidra_identify_report.txt"))

    targets = [int(t.strip(), 16) for t in targets_str.split(",") if t.strip()]
    print("[main] targets: " + ", ".join(hex(t) for t in targets))
    print("[main] report:  " + report_path)

    load_chunks_into_memory(bin_path, json_path)

    out_lines = []
    out_lines.append("# D2R identify packet RE report")
    out_lines.append("# generated headlessly via Ghidra")
    out_lines.append("")

    for va in targets:
        out_lines.append("=" * 78)
        out_lines.append("TARGET: " + hex(va))
        out_lines.append("=" * 78)

        disasm_at(va)
        fn = find_function_containing(va)
        if fn is None:
            out_lines.append("  could not locate or create function")
            continue
        out_lines.append("function entry: " + str(fn.getEntryPoint()))
        out_lines.append("function name:  " + fn.getName())
        out_lines.append("")
        out_lines.append("--- decompiled C ---")
        out_lines.append(decompile_function(fn))
        out_lines.append("")

    with open(report_path, "w") as f:
        f.write("\n".join(out_lines))
    print("[main] wrote report: " + report_path)


main()
