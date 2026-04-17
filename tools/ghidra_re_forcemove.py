#@author audit-e-forcemove
#@category D2R
#
# Headless Ghidra script: ForceMove handler RE.
#
# Strategy:
#   1. The Ghidra project already has the d2r_exec.bin dump loaded as memory blocks
#      (no need to re-load).
#   2. For each candidate VA from the static scan (capstone), locate or create the
#      function, decompile it, list direct callers, and recursively decompile callers
#      one level up.
#   3. Output a structured report ready to be inlined into AUDIT_E_forcemove.md.
#
# Targets:
#   * 0x7ff79d52a6b0  - reads keyBindings at offsets 0,0x560,0x564,0x566 (multi-bind)
#   * 0x7ff79d52e400  - reads + writes cursor X/Y (cursor handler)
#   * 0x7ff79d530180, 0x7ff79d530350 - cursor pointer manipulation
#   * 0x7ff79e0821c0, 0x7ff79e088d30, 0x7ff79e094630,
#     0x7ff79e0b89f0, 0x7ff79e0badd0 - additional keybinding-reading functions
#   * 0x7ff79d4cf980, 0x7ff79d5a83fd, 0x7ff79d5a8431 - playerPos writers (movement?)
#
# Inputs (env vars):
#   D2R_REPORT - absolute path to the output text file

import os

from ghidra.app.decompiler import DecompInterface, DecompileOptions
from ghidra.util.task import ConsoleTaskMonitor


TARGETS = [
    ("KB_dispatcher_0x52a6b0",      0x7ff79d52a6b0),
    ("CURSOR_handler_0x52e400",     0x7ff79d52e400),
    ("CURSOR_block_0x530180",       0x7ff79d530180),
    ("CURSOR_block_0x530350",       0x7ff79d530350),
    ("KB_fn_0x82_0x821c0",          0x7ff79e0821c0),
    ("KB_fn_0x88_0x88d30",          0x7ff79e088d30),
    ("KB_fn_0x94_0x94630",          0x7ff79e094630),
    ("KB_fn_0xb8_0xb89f0",          0x7ff79e0b89f0),
    ("KB_fn_0xba_0xbadd0",          0x7ff79e0badd0),
    ("PPOS_writer_0x4cf980",        0x7ff79d4cf980),
    ("PPOS_writer_0x5a83fd",        0x7ff79d5a83fd),
    ("PPOS_writer_0x5a8431",        0x7ff79d5a8431),
]


def env(name, default=None):
    v = os.environ.get(name)
    return default if v is None else v


def addr_for(va):
    return currentProgram.getAddressFactory().getDefaultAddressSpace().getAddress(va)


def disasm_at(va):
    addr = addr_for(va)
    listing = currentProgram.getListing()
    if listing.getInstructionAt(addr) is not None:
        return
    txn = currentProgram.startTransaction("disasm")
    try:
        from ghidra.app.cmd.disassemble import DisassembleCommand
        cmd = DisassembleCommand(addr, None, True)
        cmd.applyTo(currentProgram, ConsoleTaskMonitor())
    finally:
        currentProgram.endTransaction(txn, True)


def find_or_create_function(va):
    fm = currentProgram.getFunctionManager()
    addr = addr_for(va)
    fn = fm.getFunctionContaining(addr)
    if fn is not None:
        return fn

    disasm_at(va)
    listing = currentProgram.getListing()
    if listing.getInstructionContaining(addr) is None:
        return None

    txn = currentProgram.startTransaction("create_fn")
    try:
        from ghidra.app.cmd.function import CreateFunctionCmd
        cmd = CreateFunctionCmd(addr)
        cmd.applyTo(currentProgram, ConsoleTaskMonitor())
    finally:
        currentProgram.endTransaction(txn, True)
    return fm.getFunctionContaining(addr)


_iface = None
def decompile(fn):
    global _iface
    if fn is None:
        return "<no function>"
    if _iface is None:
        _iface = DecompInterface()
        _iface.setOptions(DecompileOptions())
        _iface.openProgram(currentProgram)
    res = _iface.decompileFunction(fn, 180, ConsoleTaskMonitor())
    if res is None or not res.decompileCompleted():
        return "<decompile failed>"
    return res.getDecompiledFunction().getC()


def callers_of(fn):
    if fn is None:
        return []
    rm = currentProgram.getReferenceManager()
    fm = currentProgram.getFunctionManager()
    out = []
    for r in rm.getReferencesTo(fn.getEntryPoint()):
        if r.getReferenceType().isCall():
            from_addr = r.getFromAddress()
            from_fn = fm.getFunctionContaining(from_addr)
            out.append((from_addr, from_fn))
    return out


def main():
    report_path = env(
        "D2R_REPORT",
        os.path.join(
            os.path.dirname(env("D2R_DUMP_BIN", ".")),
            "ghidra_forcemove_report.txt",
        ),
    )
    print("[forcemove] report -> " + report_path)

    out = []
    out.append("# D2R ForceMove RE - Ghidra decompile dump")
    out.append("# targets: %d" % len(TARGETS))
    out.append("")

    for label, va in TARGETS:
        out.append("=" * 78)
        out.append("TARGET %s @ 0x%x" % (label, va))
        out.append("=" * 78)
        fn = find_or_create_function(va)
        if fn is None:
            out.append("  could not locate or create function")
            out.append("")
            continue
        out.append("function entry: %s" % fn.getEntryPoint())
        out.append("function name:  %s" % fn.getName())
        body = fn.getBody()
        out.append("body size: %d bytes" % body.getNumAddresses())
        out.append("")

        cs = callers_of(fn)
        out.append("--- direct callers (%d) ---" % len(cs))
        for from_addr, from_fn in cs[:25]:
            nm = from_fn.getName() if from_fn else "<no fn>"
            ep = str(from_fn.getEntryPoint()) if from_fn else "?"
            out.append("  %s  in %s @ %s" % (str(from_addr), nm, ep))
        out.append("")

        out.append("--- decompiled C ---")
        out.append(decompile(fn))
        out.append("")

        # Decompile up to 3 unique callers
        seen = set()
        for from_addr, from_fn in cs:
            if from_fn is None:
                continue
            key = str(from_fn.getEntryPoint())
            if key in seen:
                continue
            seen.add(key)
            if len(seen) > 3:
                break
            out.append("--- caller %s @ %s ---" % (from_fn.getName(), key))
            out.append(decompile(from_fn))
            out.append("")

    with open(report_path, "w") as f:
        f.write("\n".join(out))
    print("[forcemove] wrote " + report_path)


main()
