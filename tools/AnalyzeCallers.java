// Create functions at known send_fn callers and decompile
// @category D2R
import ghidra.app.script.GhidraScript;
import ghidra.program.model.listing.*;
import ghidra.app.cmd.disassemble.DisassembleCommand;
import ghidra.app.cmd.function.CreateFunctionCmd;
import ghidra.app.decompiler.*;
import java.io.*;

public class AnalyzeCallers extends GhidraScript {
    @Override
    public void run() throws Exception {
        long ghidraBase = 0x140000000L;
        // New send_fn and all callers from scan
        long[] addresses = {
            0x116600,  // send_fn
            0x5058C,   // caller 1
            0x51F9A,   // caller 2 (walk?)
            0x1168A7,  // caller 3
            0x116972,  // caller 4 (chunked)
            0x116D5A,  // caller 5
            0x116DDB,  // caller 6
            0x116E5B,  // caller 7
            0x116ED4,  // caller 8
            0x11770A,  // caller 9 - NEW!
        };

        PrintWriter out = new PrintWriter("logs/vendor_decompile.txt");
        DecompInterface decomp = new DecompInterface();
        decomp.openProgram(currentProgram);

        for (long rva : addresses) {
            long addr = ghidraBase + rva;
            ghidra.program.model.address.Address a = toAddr(addr);

            // Disassemble if needed
            DisassembleCommand disCmd = new DisassembleCommand(a, null, true);
            disCmd.applyTo(currentProgram);

            // Find or create function
            // Walk backwards to find function start
            long funcStart = rva;
            for (long scan = rva; scan > rva - 0x2000 && scan > 0; scan--) {
                ghidra.program.model.address.Address sa = toAddr(ghidraBase + scan);
                byte[] b = new byte[1];
                try { currentProgram.getMemory().getBytes(sa, b); } catch(Exception e) { break; }
                // Look for common function prologue
                if (scan < rva - 4) {
                    byte[] prev = new byte[4];
                    try {
                        currentProgram.getMemory().getBytes(toAddr(ghidraBase + scan - 1), prev);
                        if (prev[0] == (byte)0xCC || prev[0] == (byte)0xC3 || prev[0] == (byte)0x90) {
                            funcStart = scan;
                            break;
                        }
                    } catch(Exception e) { break; }
                }
            }

            CreateFunctionCmd fCmd = new CreateFunctionCmd(toAddr(ghidraBase + funcStart));
            fCmd.applyTo(currentProgram);

            Function fn = getFunctionAt(toAddr(ghidraBase + funcStart));
            if (fn == null) {
                fn = getFunctionContaining(a);
            }

            String header = "=== RVA 0x" + Long.toHexString(rva) + " (func @ 0x" + Long.toHexString(funcStart) + ") ===";
            println(header);
            out.println(header);

            if (fn != null) {
                DecompileResults res = decomp.decompileFunction(fn, 30, null);
                if (res.getDecompiledFunction() != null) {
                    String code = res.getDecompiledFunction().getC();
                    println("  OK: " + fn.getName() + " (" + code.length() + " chars)");
                    out.println(code);
                } else {
                    println("  Decompile failed for " + fn.getName());
                    out.println("// decompile failed");
                }
            } else {
                println("  No function found");
                out.println("// no function");
            }
            out.println();
        }

        decomp.dispose();
        out.close();
        println("[*] Output: logs/vendor_decompile.txt");
    }
}
