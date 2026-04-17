# Ghidra headless script: find all callers of send_fn and decompile them.
# Run via: analyzeHeadless <project> <name> -import D2R.exe -postScript ghidra_extract_packets.py
#
# Output: logs/ghidra_send_fn_callers.txt with pseudocode for every function
# that calls send_fn (RVA 0x146600).

import ghidra.app.decompiler.DecompInterface as DecompInterface
from ghidra.program.model.symbol import RefType
import java.io.File as File

SEND_FN_RVA = 0x146600
OUTPUT_FILE = "ghidra_send_fn_callers.txt"

def get_image_base():
    return currentProgram.getImageBase().getOffset()

def get_function_at(addr):
    fm = currentProgram.getFunctionManager()
    return fm.getFunctionContaining(addr)

def decompile_function(func, decomp):
    results = decomp.decompileFunction(func, 120, monitor)
    if results and results.depiledFunction():
        return results.getDecompiledFunction().getC()
    return None

def main():
    base = get_image_base()
    send_fn_addr = currentProgram.getAddressFactory().getDefaultAddressSpace().getAddress(base + SEND_FN_RVA)

    print("[*] Image base: 0x%x" % base)
    print("[*] send_fn at: %s" % send_fn_addr)

    # Initialize decompiler
    decomp = DecompInterface()
    decomp.openProgram(currentProgram)

    # Find the function at send_fn
    send_fn_func = get_function_at(send_fn_addr)
    if send_fn_func:
        print("[*] send_fn function: %s" % send_fn_func.getName())
    else:
        print("[!] No function at send_fn address, trying to create one")
        # Try creating function
        from ghidra.app.cmd.function import CreateFunctionCmd
        cmd = CreateFunctionCmd(send_fn_addr)
        cmd.applyTo(currentProgram)
        send_fn_func = get_function_at(send_fn_addr)

    # Get all references TO send_fn (callers)
    ref_mgr = currentProgram.getReferenceManager()
    refs = ref_mgr.getReferencesTo(send_fn_addr)

    callers = set()
    for ref in refs:
        if ref.getReferenceType().isCall() or ref.getReferenceType().isJump():
            caller_addr = ref.getFromAddress()
            caller_func = get_function_at(caller_addr)
            if caller_func:
                callers.add(caller_func)
                print("[*] Found caller: %s @ %s (call from %s)" % (
                    caller_func.getName(), caller_func.getEntryPoint(), caller_addr))

    # Also check for indirect references - scan for the send_fn address pattern
    # in case there are computed calls
    print("[*] Total unique caller functions: %d" % len(callers))

    # Decompile each caller and the functions that CALL those callers (2 levels deep)
    output_lines = []
    output_lines.append("=" * 80)
    output_lines.append("D2R send_fn CALLER DECOMPILATION")
    output_lines.append("send_fn RVA: 0x%x" % SEND_FN_RVA)
    output_lines.append("send_fn VA: %s" % send_fn_addr)
    output_lines.append("Total callers: %d" % len(callers))
    output_lines.append("=" * 80)

    # First decompile send_fn itself
    if send_fn_func:
        output_lines.append("\n### send_fn itself ###")
        output_lines.append("Function: %s @ %s" % (send_fn_func.getName(), send_fn_func.getEntryPoint()))
        code = decompile_function(send_fn_func, decomp)
        if code:
            output_lines.append(code)
        else:
            output_lines.append("(decompilation failed)")

    # Decompile each direct caller
    level1_callers = {}
    for func in sorted(callers, key=lambda f: f.getEntryPoint().getOffset()):
        rva = func.getEntryPoint().getOffset() - base
        output_lines.append("\n" + "=" * 80)
        output_lines.append("### DIRECT CALLER: %s @ %s (RVA 0x%x) ###" % (
            func.getName(), func.getEntryPoint(), rva))
        output_lines.append("=" * 80)

        code = decompile_function(func, decomp)
        if code:
            output_lines.append(code)
            level1_callers[func] = code
        else:
            output_lines.append("(decompilation failed)")

    # Find callers of callers (level 2) - these are the packet builder functions
    output_lines.append("\n\n" + "#" * 80)
    output_lines.append("# LEVEL 2: Callers of callers (packet builder functions)")
    output_lines.append("#" * 80)

    for caller_func in sorted(callers, key=lambda f: f.getEntryPoint().getOffset()):
        refs2 = ref_mgr.getReferencesTo(caller_func.getEntryPoint())
        for ref2 in refs2:
            if ref2.getReferenceType().isCall():
                l2_addr = ref2.getFromAddress()
                l2_func = get_function_at(l2_addr)
                if l2_func and l2_func not in callers:
                    rva = l2_func.getEntryPoint().getOffset() - base
                    output_lines.append("\n" + "-" * 60)
                    output_lines.append("### L2 CALLER: %s @ %s (RVA 0x%x) ###" % (
                        l2_func.getName(), l2_func.getEntryPoint(), rva))
                    output_lines.append("  (calls %s which calls send_fn)" % caller_func.getName())
                    output_lines.append("-" * 60)

                    code = decompile_function(l2_func, decomp)
                    if code:
                        output_lines.append(code)
                    else:
                        output_lines.append("(decompilation failed)")

    decomp.dispose()

    # Write output
    import os
    script_dir = os.path.dirname(os.path.abspath(sourceFile.getAbsolutePath())) if hasattr(sourceFile, 'getAbsolutePath') else "."
    out_path = os.path.join("C:\\Users\\Administrator\\Desktop\\Audyt Koolo\\koolo2-rebranding\\logs", OUTPUT_FILE)

    with open(out_path, "w") as f:
        f.write("\n".join(output_lines))

    print("[*] Output written to: %s" % out_path)
    print("[*] Done!")

main()
