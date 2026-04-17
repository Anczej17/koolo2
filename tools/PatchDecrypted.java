// Patch decrypted D2R bytes — multi-dump, auto-detect base per dump
// @category D2R
import ghidra.app.script.GhidraScript;
import ghidra.program.model.mem.*;
import ghidra.program.model.address.*;
import java.io.*;
import java.nio.file.*;

public class PatchDecrypted extends GhidraScript {
    @Override
    public void run() throws Exception {
        String[] bins = {
            "C:/Users/Administrator/Desktop/Audyt Koolo/koolo2-rebranding/logs/d2r_exec.bin",
            "C:/Users/Administrator/Desktop/Audyt Koolo/koolo2-rebranding/logs/d2r_exec_npcdlg.bin",
            "C:/Users/Administrator/Desktop/Audyt Koolo/koolo2-rebranding/logs/d2r_exec_vendor.bin",
            "C:/Users/Administrator/Desktop/Audyt Koolo/koolo2-rebranding/logs/d2r_merged_all.bin"
        };
        String[] jsons = {
            "C:/Users/Administrator/Desktop/Audyt Koolo/koolo2-rebranding/logs/d2r_exec.json",
            "C:/Users/Administrator/Desktop/Audyt Koolo/koolo2-rebranding/logs/d2r_exec_npcdlg.json",
            "C:/Users/Administrator/Desktop/Audyt Koolo/koolo2-rebranding/logs/d2r_exec_vendor.json",
            "C:/Users/Administrator/Desktop/Audyt Koolo/koolo2-rebranding/logs/d2r_merged_all.json"
        };
        // Each dump may have different ASLR base. PE .text starts at RVA 0x1000.
        // We detect base as (lowest D2R VA) rounded down to 0x10000 boundary.
        long[] dumpBases = { 0x7FF79D3D0000L, 0x7FF760600000L, 0x7FF760600000L, 0x7FF7605D0000L };
        long imageSize = 0x2800000L;

        long ghidraBase = currentProgram.getImageBase().getOffset();
        Memory mem = currentProgram.getMemory();
        for (MemoryBlock block : mem.getBlocks()) {
            if (!block.isWrite()) block.setWrite(true);
        }

        // Collect all chunks keyed by RVA (later dumps override earlier)
        java.util.Map<Long, long[]> byRva = new java.util.LinkedHashMap<>();
        for (int d = 0; d < bins.length; d++) {
            String json = new String(Files.readAllBytes(Paths.get(jsons[d])));
            long base = dumpBases[d];
            int idx = 0, count = 0;
            while ((idx = json.indexOf("\"file_off\"", idx)) != -1) {
                long fileOff = extractLong(json, idx);
                int vaIdx = json.indexOf("\"va\"", idx);
                long va = extractLong(json, vaIdx);
                int szIdx = json.indexOf("\"size\"", vaIdx);
                long size = extractLong(json, szIdx);
                if (va >= base && va < base + imageSize) {
                    long rva = va - base;
                    byRva.put(rva, new long[]{fileOff, rva, size, d});
                    count++;
                }
                idx = szIdx + 1;
            }
            println("[*] " + bins[d] + ": base=0x" + Long.toHexString(base) + " d2r_chunks=" + count);
        }
        println("[*] Total unique RVAs: " + byRva.size());

        // Clear listing
        AddressSet clearSet = new AddressSet();
        for (long[] c : byRva.values()) {
            Address start = toAddr(ghidraBase + c[1]);
            Address end = toAddr(ghidraBase + c[1] + c[2] - 1);
            if (mem.contains(start)) clearSet.add(start, end);
        }
        println("[*] Clearing " + clearSet.getNumAddressRanges() + " ranges...");
        clearListing(clearSet);

        RandomAccessFile[] rafs = new RandomAccessFile[bins.length];
        for (int i = 0; i < bins.length; i++) rafs[i] = new RandomAccessFile(bins[i], "r");

        int patched = 0, errors = 0;
        for (long[] c : byRva.values()) {
            long fileOff = c[0], rva = c[1], size = c[2];
            int dIdx = (int) c[3];
            Address addr = toAddr(ghidraBase + rva);
            if (!mem.contains(addr)) continue;
            byte[] buf = new byte[(int) size];
            rafs[dIdx].seek(fileOff);
            rafs[dIdx].readFully(buf);
            try { mem.setBytes(addr, buf); patched++; }
            catch (Exception e) { errors++; }
        }
        for (RandomAccessFile r : rafs) r.close();
        println("[*] Patched: " + patched + ", Errors: " + errors);
    }

    private long extractLong(String s, int fromIdx) {
        int colon = s.indexOf(':', fromIdx);
        int start = colon + 1;
        while (start < s.length() && s.charAt(start) == ' ') start++;
        int end = start;
        while (end < s.length() && Character.isDigit(s.charAt(end))) end++;
        return Long.parseLong(s.substring(start, end));
    }
}
