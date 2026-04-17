#@author packet-rebrand
#@category D2R
#
# Headless Ghidra script for WindowProc / left-click chain RE.
#
# Inputs (env vars):
#   D2R_DUMP_BIN  — absolute path to d2r_exec.bin
#   D2R_TARGETS   — comma-separated VAs in hex
#   D2R_REPORT    — absolute path to the output text file
#
# Like ghidra_decompile_walk.py but also walks callees one level deep
# for each target so we can map the click→walk chain.

import os, json

from ghidra.program.model.address import AddressFactory
from ghidra.program.model.mem import MemoryConflictException
from ghidra.program.model.symbol import RefType
from ghidra.app.decompiler import DecompInterface, DecompileOptions
from ghidra.util.task import ConsoleTaskMonitor


D2R_BASE = 0x7ff79d3d0000
D2R_END  = 0x7ff79fb52000


def env(name, default=None):
    v = os.environ.get(name)
    if v is None:
        return default
    return v


def load_chunks_into_memory(bin_path, json_path):
    print("[load] sidecar: " + json_path)
    with open(json_path, "r") as f:
        meta = json.load(f)
    chunks = meta["chunks"]
    d2r = [c for c in chunks if D2R_BASE <= c["va"] < D2R_END]
    print("[load] D2R chunks: " + str(len(d2r)))
    with open(bin_path, "rb") as f:
        data = f.read()

    program = currentProgram
    space = program.getAddressFactory().getDefaultAddressSpace()
    mem = program.getMemory()
    txn = program.startTransaction("Load D2R dump")
    added = 0
    skipped = 0
    try:
        for i, c in enumerate(d2r):
            va = c["va"]; size = c["size"]; off = c["file_off"]
            chunk_bytes = data[off:off + size]
            addr = space.getAddress(va)
            block_name = "d2r_%016x" % va
            try:
                blk = mem.createInitializedBlock(block_name, addr, size, 0,
                                                 ConsoleTaskMonitor(), False)
                blk.putBytes(addr, chunk_bytes)
                blk.setRead(True); blk.setWrite(False); blk.setExecute(True)
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
                print("[load] " + str(i+1) + "/" + str(len(d2r)) +
                      "  +" + str(added) + " ~" + str(skipped))
    finally:
        program.endTransaction(txn, True)
    print("[load] DONE +" + str(added) + " ~" + str(skipped))


def disasm_at(va):
    program = currentProgram
    addr = program.getAddressFactory().getDefaultAddressSpace().getAddress(va)
    if program.getListing().getInstructionAt(addr) is not None:
        return
    txn = program.startTransaction("disasm")
    try:
        from ghidra.app.cmd.disassemble import DisassembleCommand
        DisassembleCommand(addr, None, True).applyTo(program, ConsoleTaskMonitor())
    finally:
        program.endTransaction(txn, True)


def find_function_containing(va):
    program = currentProgram
    addr = program.getAddressFactory().getDefaultAddressSpace().getAddress(va)
    fm = program.getFunctionManager()
    fn = fm.getFunctionContaining(addr)
    if fn is not None:
        return fn
    listing = program.getListing()
    ins = listing.getInstructionContaining(addr)
    if ins is None:
        disasm_at(va)
        ins = listing.getInstructionContaining(addr)
    if ins is None:
        return None
    txn = program.startTransaction("create_fn")
    try:
        from ghidra.app.cmd.function import CreateFunctionCmd
        CreateFunctionCmd(addr).applyTo(program, ConsoleTaskMonitor())
    finally:
        program.endTransaction(txn, True)
    return fm.getFunctionContaining(addr)


_iface = None
def decomp():
    global _iface
    if _iface is None:
        _iface = DecompInterface()
        _iface.setOptions(DecompileOptions())
        _iface.openProgram(currentProgram)
    return _iface


def decompile_function(fn):
    if fn is None:
        return "<no function>"
    res = decomp().decompileFunction(fn, 240, ConsoleTaskMonitor())
    if res is None or not res.decompileCompleted():
        return "<decompile failed>"
    return res.getDecompiledFunction().getC()


def list_callers(fn):
    if fn is None: return []
    program = currentProgram
    rm = program.getReferenceManager()
    callers = []
    refs = rm.getReferencesTo(fn.getEntryPoint())
    for r in refs:
        if r.getReferenceType().isCall():
            from_addr = r.getFromAddress()
            from_fn = program.getFunctionManager().getFunctionContaining(from_addr)
            callers.append((from_addr, from_fn))
    return callers


def list_callees(fn):
    """Direct callees by walking the function body."""
    if fn is None: return []
    program = currentProgram
    fm = program.getFunctionManager()
    out = []
    body = fn.getBody()
    listing = program.getListing()
    it = listing.getInstructions(body, True)
    seen = set()
    while it.hasNext():
        ins = it.next()
        if ins.getFlowType().isCall():
            for ref in ins.getReferencesFrom():
                if ref.getReferenceType().isCall():
                    a = ref.getToAddress()
                    if a in seen: continue
                    seen.add(a)
                    callee = fm.getFunctionAt(a)
                    out.append((ins.getAddress(), a, callee))
    return out


def main():
    bin_path = env("D2R_DUMP_BIN")
    if not bin_path:
        print("ERROR: D2R_DUMP_BIN not set"); return
    json_path = bin_path[:-4] + ".json" if bin_path.endswith(".bin") else bin_path + ".json"
    targets_str = env("D2R_TARGETS", "")
    report_path = env("D2R_REPORT", os.path.join(os.path.dirname(bin_path), "ghidra_wndproc_report.txt"))
    targets = [int(t.strip(), 16) for t in targets_str.split(",") if t.strip()]
    print("[main] targets: " + ", ".join(hex(t) for t in targets))

    load_chunks_into_memory(bin_path, json_path)

    out = []
    out.append("# D2R wndproc / click chain RE report")
    out.append("")

    for va in targets:
        out.append("=" * 78)
        out.append("TARGET: " + hex(va))
        out.append("=" * 78)
        disasm_at(va)
        fn = find_function_containing(va)
        if fn is None:
            out.append("  could not locate function"); continue
        out.append("function entry: " + str(fn.getEntryPoint()))
        out.append("function name : " + fn.getName())
        out.append("")

        callers = list_callers(fn)
        out.append("--- direct callers (" + str(len(callers)) + ") ---")
        for from_addr, from_fn in callers[:20]:
            n = from_fn.getName() if from_fn else "<no fn>"
            e = str(from_fn.getEntryPoint()) if from_fn else "?"
            out.append("  " + str(from_addr) + "  " + n + " @ " + e)
        out.append("")

        callees = list_callees(fn)
        out.append("--- direct callees (" + str(len(callees)) + ") ---")
        for site, addr, cf in callees[:60]:
            n = cf.getName() if cf else "<unnamed>"
            out.append("  " + str(site) + " -> " + str(addr) + "  " + n)
        out.append("")

        out.append("--- decompiled C ---")
        out.append(decompile_function(fn))
        out.append("")

    with open(report_path, "w") as f:
        f.write("\n".join(out))
    print("[main] wrote " + report_path)


main()
