package packet

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// captureEntry mirrors the schema of logs/live_captures_2026_04_15/*.json.
type captureEntry struct {
	Buf    int    `json:"buf"`
	Opcode string `json:"opcode"` // e.g. "0x4D"
	Hex    string `json:"hex"`    // 512 hex chars = 256 bytes
}

type captureFile struct {
	Entries []captureEntry `json:"entries"`
}

// Path to the 04-15 mirror buffer corpus. Tests skip if absent (CI-safe).
const captureDir = `C:\Users\Administrator\Desktop\Audyt Koolo\koolo2-rebranding\logs\live_captures_2026_04_15`

func loadCaptures(t *testing.T) []captureEntry {
	t.Helper()
	entries, err := os.ReadDir(captureDir)
	if err != nil {
		t.Skipf("capture corpus not available (%v); skipping", err)
		return nil
	}
	var all []captureEntry
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(captureDir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		var cf captureFile
		if err := json.Unmarshal(raw, &cf); err != nil {
			t.Fatalf("parse %s: %v", e.Name(), err)
		}
		all = append(all, cf.Entries...)
	}
	return all
}

// TestTrimPacket_BufMirrorMatchesLUT walks every buf=1 entry and verifies that
// TrimPacket produces a length within the LUT's declared [Min, Max] range for
// the opcode. A failure signals either a corrupt capture or a drifted LUT.
func TestTrimPacket_BufMirrorMatchesLUT(t *testing.T) {
	entries := loadCaptures(t)
	if len(entries) == 0 {
		return
	}

	var unknown, outOfRange int
	unknownSet := map[byte]int{}

	for _, e := range entries {
		if e.Buf != 1 {
			continue
		}
		raw, err := hex.DecodeString(e.Hex)
		if err != nil {
			t.Fatalf("hex decode %q: %v", e.Opcode, err)
		}
		if len(raw) == 0 {
			continue
		}
		trimmed := TrimPacket(raw)
		op := raw[0]
		info, known := OpcodeLengths[op]
		if !known {
			unknown++
			unknownSet[op]++
			continue
		}
		if len(trimmed) < int(info.Min) || len(trimmed) > int(info.Max) {
			outOfRange++
			t.Errorf("op=0x%02X trimmed=%d out of LUT [%d..%d] (hex head: %s...)",
				op, len(trimmed), info.Min, info.Max, e.Hex[:40])
		}
	}

	if unknown > 0 {
		t.Logf("unknown opcodes in corpus (add to LUT): %v (%d entries)", unknownSet, unknown)
	}
	if outOfRange > 0 {
		t.Errorf("%d entries outside LUT range", outOfRange)
	}
}

// TestTrimPacket_KnownFixedOpcodes verifies that fixed-length opcodes in the
// LUT reduce to exactly the declared length on a maximally-padded buffer.
func TestTrimPacket_KnownFixedOpcodes(t *testing.T) {
	fixtures := []struct {
		op      byte
		wantLen int
	}{
		{0x04, 18},
		{0x18, 22},
		{0x32, 22},
		{0x33, 24},
		{0x38, 6},
		{0x3C, 15},
		{0x41, 13},
		{0x43, 13},
		{0x4B, 13},
		{0x54, 20},
	}
	for _, f := range fixtures {
		buf := make([]byte, 256)
		buf[0] = f.op
		// Fill [1..wantLen) with sentinel 0xAB so trimmer can't drop them.
		for i := 1; i < f.wantLen; i++ {
			buf[i] = 0xAB
		}
		got := TrimPacket(buf)
		if len(got) != f.wantLen {
			t.Errorf("op=0x%02X: trimmed len=%d want=%d", f.op, len(got), f.wantLen)
		}
	}
}

// TestTrimPacket_UnknownOpcode exercises the fallback — trim trailing zeros,
// keep opcode byte alone if everything else is zero.
func TestTrimPacket_UnknownOpcode(t *testing.T) {
	buf := make([]byte, 256)
	buf[0] = 0x99 // not in LUT
	buf[1] = 0xAB
	buf[2] = 0xCD
	got := TrimPacket(buf)
	if len(got) != 3 {
		t.Errorf("unknown opcode fallback: len=%d want=3", len(got))
	}
	buf2 := make([]byte, 256)
	buf2[0] = 0xEE // unknown; only opcode
	got2 := TrimPacket(buf2)
	if len(got2) != 1 {
		t.Errorf("unknown opcode alone: len=%d want=1", len(got2))
	}
}
