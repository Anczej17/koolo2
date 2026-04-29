package packet

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// TestDiscordCaptureFormats originally asserted 5B 0x2F/0x4D outputs from a
// Discord plaintext claim (2026-04-15). Those 5B shapes contradicted the
// buf=1 mirror sniffer captures (logs/live_captures_2026_04_15/) which show
// 13B (0x2F/0x30) and 29B (0x4D). The 5B form was accepted by D2R's local
// dispatch but server silently dropped it — which is why "trade u Akary
// nie dziala" and full HID fallback was required throughout. CAPTURE_AUDIT
// _2026_04_19 restored the 13B/29B forms; see builders_test.go for the
// authoritative regression tests (TestNewNPCChatInit_0x2F etc.).
//
// Remaining tests in this file (0x35 repair, 0x19 pickup) keep their
// original Discord-plaintext-derived expectations — those opcodes remain
// live-unverified; they should be re-checked during Phase 5 live test pass.

// TestRepairAll_FormatStructure verifies the 16-byte layout: opcode + subcmd
// + pad + npcGID + live UI cost + sentinel.
func TestRepairAll_FormatStructure(t *testing.T) {
	p := NewRepairAll(2, 0x115B)
	if len(p) != 16 {
		t.Fatalf("expected 16 bytes, got %d", len(p))
	}
	want, _ := hex.DecodeString("35040000020000005B110000FFFFFFFF")
	if !bytes.Equal(p, want) {
		t.Errorf("layout mismatch:\n got: %s\nwant: %s",
			hex.EncodeToString(p), hex.EncodeToString(want))
	}
}

// TestPickupBufferItem_LayoutOnly verifies the builder emits 17 bytes with
// opcode 0x19 at position 0 and itemGID at 1..5. Discord captures show two
// different source/pos patterns so we test layout invariants, not exact
// equality.
func TestPickupBufferItem_LayoutOnly(t *testing.T) {
	p := NewPickupBufferItem(0x17, 2, 0)
	if len(p) != 17 {
		t.Fatalf("expected 17 bytes, got %d", len(p))
	}
	if p[0] != 0x19 {
		t.Fatalf("expected 0x19 opcode, got 0x%02x", p[0])
	}
	// itemGID at bytes 1..5 = 0x17 LE
	if p[1] != 0x17 || p[2] != 0 || p[3] != 0 || p[4] != 0 {
		t.Errorf("itemGID bytes wrong: %x", p[1:5])
	}
	// source marker at bytes 5..9 = 2 LE
	if p[5] != 0x02 {
		t.Errorf("source byte wrong: %x", p[5])
	}
}
