// Packet RE v2 — disassemble + create functions at known send_fn callers
// @category D2R

import ghidra.app.script.GhidraScript;
import ghidra.app.cmd.function.CreateFunctionCmd;
import ghidra.app.cmd.disassemble.DisassembleCommand;
import ghidra.app.decompiler.DecompInterface;
import ghidra.app.decompiler.DecompileResults;
import ghidra.app.services.AbstractAnalyzer;
import ghidra.app.plugin.core.analysis.AutoAnalysisManager;
import ghidra.program.model.address.*;
import ghidra.program.model.listing.*;
import ghidra.program.model.mem.*;
import ghidra.program.model.symbol.*;
import java.io.*;
import java.util.*;

public class PacketRE extends GhidraScript {
    static final long SEND_FN_RVA = 0x146600L;

    // All 9 known call sites to send_fn (from memory + disasm), plus known
    // key function starts to seed disassembly.
    static final long[] KNOWN_SITES = {
        0x146600L,  // send_fn itself
        0x147500L,  // dual_send_wrap approx start
        0x14770AL,  // dual_send_wrap call-site to send_fn
        0x5058CL,   // heartbeat caller
        0x51F9AL,   // movement/state caller
        0x1168A7L,  // simple send caller
        0x116972L,  // chunked 0x6C caller
        0x116D5AL,  // sharedstash-1
        0x116E00L,  // sharedstash-2 (guess)
        0x116EC0L,  // sharedstash-3 (guess)
        0x116ED4L,  // sharedstash-4
        0xBA95D0L,  // real_click_worker
        0x81B40L,   // request_walk
    };

    static final String OUT_PATH =
        "C:/Users/Administrator/Desktop/Audyt Koolo/koolo2-rebranding/logs/ghidra_packet_re.md";

    StringBuilder sb = new StringBuilder();
    long base;
    Memory mem;
    ReferenceManager refMgr;
    FunctionManager fm;
    DecompInterface decomp;
    Listing listing;

    void out(String s) { sb.append(s).append("\n"); println(s); }

    Address addrOf(long rva) {
        return currentProgram.getAddressFactory().getDefaultAddressSpace()
            .getAddress(base + rva);
    }

    String hex16(Address a) {
        try {
            byte[] buf = new byte[16];
            mem.getBytes(a, buf);
            StringBuilder h = new StringBuilder();
            for (byte b : buf) h.append(String.format("%02x ", b & 0xff));
            return h.toString().trim();
        } catch (Exception e) {
            return "<read error: " + e.getMessage() + ">";
        }
    }

    // Walk backwards up to 0x200 bytes to find a likely function start
    // (RET padding 0xCC 0xCC or standard x64 prologue).
    Address findFunctionStart(Address addr) {
        for (int i = 2; i < 0x200; i++) {
            try {
                Address probe = addr.subtract(i);
                byte b0 = mem.getByte(probe);
                byte b1 = mem.getByte(probe.add(1));
                byte b2 = mem.getByte(probe.add(2));
                // RET 0xC3 followed by CC padding → next instr is fn start
                if (b0 == (byte)0xC3 && b1 == (byte)0xCC) {
                    // skip to next non-CC
                    int j = 1;
                    while (mem.getByte(probe.add(j)) == (byte)0xCC) j++;
                    return probe.add(j);
                }
                // INT3 padding run → skip to non-CC
                if (b0 == (byte)0xCC && b1 == (byte)0xCC && b2 != (byte)0xCC) {
                    return probe.add(2);
                }
                // Common x64 prologue markers
                if (b0 == (byte)0x48 && b1 == (byte)0x89 && b2 == (byte)0x5c) return probe; // mov [rsp+X], rbx
                if (b0 == (byte)0x40 && b1 == (byte)0x53) return probe; // push rbx (REX)
                if (b0 == (byte)0x48 && b1 == (byte)0x83 && b2 == (byte)0xec) return probe; // sub rsp, X
                if (b0 == (byte)0x48 && b1 == (byte)0x81 && b2 == (byte)0xec) return probe; // sub rsp, X32
            } catch (Exception ignored) {}
        }
        return null;
    }

    void disassembleAt(Address a) {
        DisassembleCommand cmd = new DisassembleCommand(a, null, true);
        cmd.applyTo(currentProgram, monitor);
    }

    Function createFunctionAt(Address a) {
        Function f = fm.getFunctionAt(a);
        if (f != null) return f;
        CreateFunctionCmd cmd = new CreateFunctionCmd(a);
        cmd.applyTo(currentProgram, monitor);
        return fm.getFunctionAt(a);
    }

    String decompile(Function f) {
        if (f == null) return "(null function)";
        try {
            DecompileResults r = decomp.decompileFunction(f, 120, monitor);
            if (r != null && r.decompileCompleted()) {
                return r.getDecompiledFunction().getC();
            }
            return "(decompile failed: " + (r != null ? r.getErrorMessage() : "null results") + ")";
        } catch (Exception e) {
            return "(decompile exception: " + e.getMessage() + ")";
        }
    }

    Set<Function> collectCallers(Address target) {
        Set<Function> result = new LinkedHashSet<>();
        ReferenceIterator it = refMgr.getReferencesTo(target);
        while (it.hasNext()) {
            Reference r = it.next();
            RefType t = r.getReferenceType();
            if (t.isCall() || t.isJump()) {
                Function caller = fm.getFunctionContaining(r.getFromAddress());
                if (caller != null) result.add(caller);
            }
        }
        return result;
    }

    @Override
    public void run() throws Exception {
        base = currentProgram.getImageBase().getOffset();
        mem = currentProgram.getMemory();
        refMgr = currentProgram.getReferenceManager();
        fm = currentProgram.getFunctionManager();
        listing = currentProgram.getListing();
        decomp = new DecompInterface();
        decomp.openProgram(currentProgram);

        out("# D2R Packet RE Report (v2)");
        out("");
        out("**Image base**: 0x" + Long.toHexString(base));
        out("");

        // Phase 1: seed disassembly + create functions at known sites
        out("## Phase 1: Seeding disassembly at known sites");
        out("");
        for (long rva : KNOWN_SITES) {
            Address a = addrOf(rva);
            try {
                Address fnStart = a;
                byte b0 = mem.getByte(a);
                // If byte is not a typical function-start pattern, walk backwards
                if (b0 == (byte)0xe8 || b0 == (byte)0xff) {
                    // call instruction site — walk backwards to func start
                    Address fs = findFunctionStart(a);
                    if (fs != null) fnStart = fs;
                }
                disassembleAt(fnStart);
                Function f = createFunctionAt(fnStart);
                String msg = f != null
                    ? "created " + f.getName() + " @ " + f.getEntryPoint()
                    : "create failed";
                out("- RVA 0x" + Long.toHexString(rva) + " → fnStart " + fnStart + " → " + msg);
            } catch (Exception e) {
                out("- RVA 0x" + Long.toHexString(rva) + " → error: " + e.getMessage());
            }
        }
        out("");

        // Phase 2: run analyzer pass to propagate xrefs
        out("## Phase 2: Running analyzer to propagate references");
        out("");
        AutoAnalysisManager mgr = AutoAnalysisManager.getAnalysisManager(currentProgram);
        mgr.reAnalyzeAll(null);
        mgr.startAnalysis(monitor);
        out("Analyzer pass complete.");
        out("");

        // Phase 3: verify send_fn and get its callers
        Address sendFnAddr = addrOf(SEND_FN_RVA);
        Function sendFn = fm.getFunctionAt(sendFnAddr);
        if (sendFn == null) sendFn = fm.getFunctionContaining(sendFnAddr);
        out("## send_fn @ RVA 0x" + Long.toHexString(SEND_FN_RVA));
        out("");
        if (sendFn != null) {
            out("**" + sendFn.getName() + "** at " + sendFn.getEntryPoint());
            out("");
            out("```c");
            out(decompile(sendFn));
            out("```");
            out("");
        }

        // send_fn callers
        out("## send_fn callers");
        out("");
        Set<Function> callers = collectCallers(sendFnAddr);
        out("Total callers: **" + callers.size() + "**");
        out("");
        List<Function> sortedCallers = new ArrayList<>(callers);
        sortedCallers.sort(Comparator.comparingLong(f -> f.getEntryPoint().getOffset()));

        for (Function c : sortedCallers) {
            long rva = c.getEntryPoint().getOffset() - base;
            out("### " + c.getName() + " @ " + c.getEntryPoint()
                + " (RVA 0x" + Long.toHexString(rva) + ")");
            out("");
            out("```c");
            out(decompile(c));
            out("```");
            out("");
        }

        // Extra: find function containing 0x14770A (dual_send_wrap)
        Address dualCall = addrOf(0x14770AL);
        Function dualWrap = fm.getFunctionContaining(dualCall);
        out("## dual_send_wrap (function containing 0x14770a)");
        out("");
        if (dualWrap != null) {
            long rva = dualWrap.getEntryPoint().getOffset() - base;
            out("**" + dualWrap.getName() + "** at " + dualWrap.getEntryPoint()
                + " (RVA 0x" + Long.toHexString(rva) + ")");
            out("");
            out("```c");
            out(decompile(dualWrap));
            out("```");
            out("");
        } else {
            out("(no function found)");
        }

        // Level 2 packet builders
        out("## Level 2 — callers of each send_fn caller");
        out("");
        Set<Function> seenL2 = new HashSet<>(callers);
        int l2Count = 0;
        for (Function c : sortedCallers) {
            Set<Function> l2 = collectCallers(c.getEntryPoint());
            for (Function l2f : l2) {
                if (seenL2.contains(l2f)) continue;
                seenL2.add(l2f);
                l2Count++;
                if (l2Count > 25) {
                    out("_(truncated at 25 L2 functions)_");
                    break;
                }
                long rva = l2f.getEntryPoint().getOffset() - base;
                out("### L2: " + l2f.getName() + " @ " + l2f.getEntryPoint()
                    + " (RVA 0x" + Long.toHexString(rva) + ") — via " + c.getName());
                out("");
                out("```c");
                out(decompile(l2f));
                out("```");
                out("");
            }
            if (l2Count > 25) break;
        }

        decomp.dispose();

        try (FileWriter fw = new FileWriter(OUT_PATH)) {
            fw.write(sb.toString());
        }
        out("");
        out("[*] Report written to " + OUT_PATH);
    }
}
