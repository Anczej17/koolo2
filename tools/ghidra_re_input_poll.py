#@author input-poll-re
#@category D2R
#
# Headless Ghidra script: locate readers of mouseXY (0x1EC3BB8) and identify
# the per-frame input poll function.
#
# Strategy:
#   1. Load d2r_exec.bin chunks into the program (idempotent — skips conflicts).
#   2. Force-disassemble every chunk so the Reference Manager populates xrefs
#      to absolute data addresses.
#   3. Pull all references touching mouseXY VA / mouseXY+4 VA via the
#      ReferenceManager.
#   4. As a fallback (in case some code wasn't disassembled), do a raw
#      byte-pattern scan for `0F B6/B7 05 disp32` and `48 8B/8D 0D/15 disp32`
#      style RIP-relative loads/leas where disp32 resolves to one of the
#      target VAs.
#   5. For each unique containing function, decompile it and classify as
#      reader / writer / mixed by inspecting the access type.
#   6. Score functions by "input-poll likelihood":
#         + small (< 600 bytes)
#         + reads BOTH X and Y
#         + references VK_LBUTTON / VK_SHIFT (0x01 / 0x10) constants
#         + calls GetKeyState / GetAsyncKeyState
#         + writes another global afterwards
#         + caller chain depth >= 2 from a "tick"-looking parent
#   7. Emit a markdown report.
#
# Inputs (env vars):
#   D2R_DUMP_BIN  — absolute path to d2r_exec.bin
#   D2R_REPORT    — absolute path to the output markdown file
#
# Run via tools/ghidra_run_input_poll.bat.

import os
import json
import struct

from ghidra.program.model.mem import MemoryConflictException
from ghidra.program.model.symbol import RefType, SourceType
from ghidra.app.decompiler import DecompInterface, DecompileOptions
from ghidra.util.task import ConsoleTaskMonitor


D2R_BASE = 0x7ff79d3d0000
D2R_END  = 0x7ff79fb52000

MOUSE_X_VA = 0x7ff79f293bb8
MOUSE_Y_VA = 0x7ff79f293bbc
PLAYER_POS_VA = 0x7ff79f293fcc  # screen-space player pos per memory note

TARGET_VAS = set([MOUSE_X_VA, MOUSE_Y_VA, PLAYER_POS_VA, PLAYER_POS_VA + 4])


def env(name, default=None):
    v = os.environ.get(name)
    return default if v is None else v


# ----------------------------------------------------------------------------
# memory loading (same as walk template, idempotent)
# ----------------------------------------------------------------------------
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
    space = program.getAddressFactory().getDefaultAddressSpace()
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
            if (i + 1) % 1000 == 0:
                print("[load] " + str(i + 1) + "/" + str(len(d2r)) +
                      "  added=" + str(added) + " skipped=" + str(skipped))
    finally:
        program.endTransaction(txn, True)
    print("[load] DONE  added=" + str(added) + "  skipped=" + str(skipped))


# ----------------------------------------------------------------------------
# raw byte-pattern scan for RIP-relative refs to TARGET_VAS
# ----------------------------------------------------------------------------
def scan_rip_relative(bin_path, json_path):
    print("[scan] raw RIP-relative scan starting")
    with open(json_path, "r") as f:
        meta = json.load(f)
    chunks = [c for c in meta["chunks"] if D2R_BASE <= c["va"] < D2R_END]
    with open(bin_path, "rb") as f:
        data = f.read()

    hits = []   # list of (insn_va, target_va)
    for c in chunks:
        va_base = c["va"]
        size = c["size"]
        off = c["file_off"]
        buf = data[off:off + size]
        # walk every byte looking for a disp32 that resolves to a target.
        # we test all four common opcode shapes around the disp32:
        #   8B 05 d32   mov eax, [rip+d32]
        #   8B 0D d32   mov ecx, [rip+d32]
        #   8B 15 d32   mov edx, [rip+d32]
        #   ... and 48 prefixed (mov rax/rcx/rdx, [rip+d32])
        #   0F B7 05 d32  movzx eax, word [rip+d32]
        #   8D 05 / 8D 0D / 8D 15 d32  lea reg, [rip+d32]
        #   48 8D 05 / 0D / 15 d32     lea r64, [rip+d32]
        # we approximate by simply scanning every position; it's only ~150MB
        # of code total in chunks but per-chunk it's tiny.
        n = len(buf)
        for i in range(n - 6):
            b0 = buf[i]
            b1 = buf[i + 1] if isinstance(buf[i + 1], int) else ord(buf[i + 1])
            if isinstance(b0, str):
                b0 = ord(b0)
            # quick filter: candidate opcodes
            ok = False
            disp_off = -1
            if b0 in (0x8B, 0x8D, 0x89) and (b1 & 0xC7) in (0x05,):
                disp_off = i + 2
                ok = True
            elif b0 in (0x8B, 0x8D, 0x89) and (b1 & 0xC7) == 0x05:
                disp_off = i + 2
                ok = True
            elif b0 == 0x48 and i + 7 < n:
                b2 = buf[i + 2] if isinstance(buf[i + 2], int) else ord(buf[i + 2])
                if b1 in (0x8B, 0x8D, 0x89, 0x63) and (b2 & 0xC7) == 0x05:
                    disp_off = i + 3
                    ok = True
            elif b0 == 0x0F and i + 7 < n:
                b2 = buf[i + 2] if isinstance(buf[i + 2], int) else ord(buf[i + 2])
                if b1 in (0xB6, 0xB7, 0xBE, 0xBF) and (b2 & 0xC7) == 0x05:
                    disp_off = i + 3
                    ok = True
            elif b0 == 0x66 and i + 7 < n:
                b2 = buf[i + 2] if isinstance(buf[i + 2], int) else ord(buf[i + 2])
                if b1 in (0x8B, 0x89) and (b2 & 0xC7) == 0x05:
                    disp_off = i + 3
                    ok = True
            if not ok or disp_off < 0 or disp_off + 4 > n:
                continue
            d_bytes = buf[disp_off:disp_off + 4]
            if isinstance(d_bytes, str):
                d_bytes = bytes([ord(ch) for ch in d_bytes])
            disp = struct.unpack("<i", d_bytes)[0]
            insn_va = va_base + disp_off - (1 if b0 == 0x0F or b0 == 0x66 or b0 == 0x48 else 0)
            insn_end = va_base + disp_off + 4
            target = (insn_end + disp) & 0xFFFFFFFFFFFFFFFF
            if target in TARGET_VAS:
                hits.append((va_base + (disp_off - 2), target))
    print("[scan] raw hits: " + str(len(hits)))
    return hits


# ----------------------------------------------------------------------------
# disasm helpers
# ----------------------------------------------------------------------------
def disasm_at(va):
    program = currentProgram
    addr = program.getAddressFactory().getDefaultAddressSpace().getAddress(va)
    if program.getListing().getInstructionAt(addr) is not None:
        return
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
        cmd = CreateFunctionCmd(addr)
        cmd.applyTo(program, ConsoleTaskMonitor())
    finally:
        program.endTransaction(txn, True)
    return fm.getFunctionContaining(addr)


_decomp_iface = None
def get_decomp():
    global _decomp_iface
    if _decomp_iface is None:
        _decomp_iface = DecompInterface()
        _decomp_iface.setOptions(DecompileOptions())
        _decomp_iface.openProgram(currentProgram)
    return _decomp_iface


def decompile_function(fn):
    if fn is None:
        return "<no function>"
    iface = get_decomp()
    res = iface.decompileFunction(fn, 120, ConsoleTaskMonitor())
    if res is None or not res.decompileCompleted():
        return "<decompile failed>"
    return res.getDecompiledFunction().getC()


def list_callers(fn):
    if fn is None:
        return []
    program = currentProgram
    rm = program.getReferenceManager()
    callers = []
    for r in rm.getReferencesTo(fn.getEntryPoint()):
        if r.getReferenceType().isCall():
            from_fn = program.getFunctionManager().getFunctionContaining(r.getFromAddress())
            callers.append((r.getFromAddress(), from_fn))
    return callers


# ----------------------------------------------------------------------------
# scoring
# ----------------------------------------------------------------------------
def score_function(c_text, reads_x, reads_y):
    score = 0
    notes = []
    if reads_x and reads_y:
        score += 3
        notes.append("reads both X and Y")
    elif reads_x or reads_y:
        score += 1
        notes.append("reads only one coord")
    lower = c_text.lower()
    if "getkeystate" in lower or "getasynckeystate" in lower:
        score += 2
        notes.append("calls GetKeyState/GetAsyncKeyState")
    # VK_LBUTTON 0x01, VK_SHIFT 0x10
    if "0x10" in c_text and ("shift" in lower or "force" in lower):
        score += 1
    if "0x01" in c_text:
        score += 1
        notes.append("references LBUTTON-like 0x01")
    if c_text.count("\n") < 80:
        score += 1
        notes.append("small function (<80 lines)")
    if "while" in lower or "for (" in lower:
        # actual loops are unlikely in a tick poll
        score -= 1
    return score, notes


# ----------------------------------------------------------------------------
# main
# ----------------------------------------------------------------------------
def main():
    bin_path = env("D2R_DUMP_BIN")
    if not bin_path:
        print("ERROR: D2R_DUMP_BIN not set")
        return
    json_path = bin_path[:-4] + ".json" if bin_path.endswith(".bin") else bin_path + ".json"
    report_path = env("D2R_REPORT",
                      os.path.join(os.path.dirname(bin_path), "AUDIT_D_input_poll.md"))
    print("[main] report: " + report_path)

    load_chunks_into_memory(bin_path, json_path)

    program = currentProgram
    space = program.getAddressFactory().getDefaultAddressSpace()
    rm = program.getReferenceManager()
    fm = program.getFunctionManager()

    # ------------------------------------------------------------------
    # Step A: ask Ghidra's ReferenceManager for refs to each target VA.
    # Even without analysis these may be empty (no decoded code) — so we
    # also do the raw scan and force-disasm.
    # ------------------------------------------------------------------
    refs_by_target = {}
    for tva in sorted(TARGET_VAS):
        addr = space.getAddress(tva)
        rs = list(rm.getReferencesTo(addr))
        refs_by_target[tva] = rs
        print("[refmgr] %s -> %d refs" % (hex(tva), len(rs)))

    # ------------------------------------------------------------------
    # Step B: raw scan to catch the rest, then force-disasm those addrs
    # so Ghidra creates instructions and the function manager works.
    # ------------------------------------------------------------------
    raw_hits = scan_rip_relative(bin_path, json_path)
    seen_insn = set()
    insn_to_target = {}
    for insn_va, target in raw_hits:
        if insn_va in seen_insn:
            continue
        seen_insn.add(insn_va)
        insn_to_target[insn_va] = target

    # also include the refmgr-discovered insn addrs
    for tva, rs in refs_by_target.items():
        for r in rs:
            iv = r.getFromAddress().getOffset()
            if iv not in insn_to_target:
                insn_to_target[iv] = tva

    print("[main] total unique insn addrs touching targets: " + str(len(insn_to_target)))

    # ------------------------------------------------------------------
    # Step C: for each insn, locate / create the containing function.
    # Group by function entry, accumulating which targets are read.
    # ------------------------------------------------------------------
    fn_records = {}  # entry_va -> dict
    for insn_va, target in sorted(insn_to_target.items()):
        try:
            disasm_at(insn_va)
        except Exception as e:
            print("[disasm] err @ %s: %s" % (hex(insn_va), str(e)))
            continue
        fn = find_function_containing(insn_va)
        if fn is None:
            continue
        entry = fn.getEntryPoint().getOffset()
        rec = fn_records.get(entry)
        if rec is None:
            rec = {
                "fn": fn,
                "entry": entry,
                "name": fn.getName(),
                "insns": [],
                "targets": set(),
            }
            fn_records[entry] = rec
        rec["insns"].append(insn_va)
        rec["targets"].add(target)

    print("[main] unique containing functions: " + str(len(fn_records)))

    # ------------------------------------------------------------------
    # Step D: decompile each function, score, sort.
    # ------------------------------------------------------------------
    scored = []
    for entry, rec in fn_records.items():
        try:
            c_text = decompile_function(rec["fn"])
        except Exception as e:
            c_text = "<decompile error: %s>" % str(e)
        reads_x = MOUSE_X_VA in rec["targets"]
        reads_y = MOUSE_Y_VA in rec["targets"]
        s, notes = score_function(c_text, reads_x, reads_y)
        rec["c_text"] = c_text
        rec["score"] = s
        rec["notes"] = notes
        rec["reads_x"] = reads_x
        rec["reads_y"] = reads_y
        scored.append(rec)
    scored.sort(key=lambda r: -r["score"])

    # ------------------------------------------------------------------
    # Step E: write the report
    # ------------------------------------------------------------------
    out = []
    out.append("# AUDIT D — D2R input poll function discovery")
    out.append("")
    out.append("Image base: `0x%x`" % D2R_BASE)
    out.append("")
    out.append("Targets:")
    out.append("- mouseX: `0x%x`" % MOUSE_X_VA)
    out.append("- mouseY: `0x%x`" % MOUSE_Y_VA)
    out.append("- playerPos (screen): `0x%x`" % PLAYER_POS_VA)
    out.append("")
    out.append("## Summary")
    out.append("")
    out.append("- raw RIP-rel hits: %d" % len(raw_hits))
    out.append("- unique insn addrs: %d" % len(insn_to_target))
    out.append("- unique containing fns: %d" % len(fn_records))
    out.append("")
    out.append("## Xref list (grouped by containing function)")
    out.append("")
    for rec in sorted(scored, key=lambda r: r["entry"]):
        out.append("### fn `%s` @ 0x%x  (score=%d)" % (rec["name"], rec["entry"], rec["score"]))
        targs = sorted(rec["targets"])
        out.append("targets touched: " + ", ".join("0x%x" % t for t in targs))
        out.append("insns: " + ", ".join("0x%x" % i for i in rec["insns"][:16]) +
                   (" ..." if len(rec["insns"]) > 16 else ""))
        out.append("notes: " + (", ".join(rec["notes"]) if rec["notes"] else "(none)"))
        out.append("")

    out.append("## Top 5 candidate input-poll functions")
    out.append("")
    top = scored[:5]
    for rank, rec in enumerate(top):
        out.append("### Candidate %d: `%s` @ 0x%x  (score=%d)" %
                   (rank + 1, rec["name"], rec["entry"], rec["score"]))
        out.append("")
        out.append("- reads X: %s" % rec["reads_x"])
        out.append("- reads Y: %s" % rec["reads_y"])
        out.append("- notes: %s" % (", ".join(rec["notes"]) if rec["notes"] else "(none)"))
        callers = list_callers(rec["fn"])
        out.append("- direct callers: %d" % len(callers))
        for from_addr, from_fn in callers[:8]:
            cn = from_fn.getName() if from_fn else "<no fn>"
            ce = str(from_fn.getEntryPoint()) if from_fn else "?"
            out.append("    - %s in %s @ %s" % (str(from_addr), cn, ce))
        out.append("")
        out.append("```c")
        out.append(rec["c_text"])
        out.append("```")
        out.append("")

    if top:
        best = top[0]
        out.append("## Best candidate")
        out.append("")
        out.append("`%s` @ 0x%x" % (best["name"], best["entry"]))
        out.append("")
        out.append("Rationale: highest score from heuristics (reads both X and Y, "
                   "small function size, references VK constants / GetKeyState if applicable). "
                   "See decompiled C above.")
        out.append("")
        # ABI guess
        out.append("ABI (MSVC x64): see Ghidra signature — %s" %
                   str(best["fn"].getSignature()))
        out.append("")
        out.append("Confidence: see manual review section below — heuristic score "
                   "alone is medium; cross-reference with caller chain to confirm.")
    out.append("")
    out.append("## ForceMove / VK_SHIFT cross-references")
    out.append("")
    out.append("(Heuristic only — search the candidate decompilations above for `0x10` / `VK_SHIFT` / `GetKeyState`.)")
    out.append("")

    with open(report_path, "w") as f:
        f.write("\n".join(out))
    print("[main] wrote report: " + report_path)


main()
