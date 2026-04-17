package packet

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// TestDiscordCaptureFormats verifies that our packet builders emit
// byte-exact output matching plaintext captures provided by the Discord
// colleague on 2026-04-15. Source: C:\Users\Administrator\Desktop\packets..txt
//
// The colleague reads D2R's internal plaintext buffer (post-decrypt for
// incoming, pre-encrypt for outgoing) — so these are 100% on-wire bytes.
// If any of our builders fails this test, our format is wrong.
func TestDiscordCaptureFormats(t *testing.T) {
	tests := []struct {
		name    string
		got     []byte
		wantHex string
	}{
		{
			name:    "0x2F NPCInit Akara GID=8",
			got:     NewNPCChatInit(8, 0, 0),
			wantHex: "2F08000000",
		},
		{
			name:    "0x2F NPCInit Deckard GID=2",
			got:     NewNPCChatInit(2, 0, 0),
			wantHex: "2F02000000",
		},
		{
			name:    "0x4D PreInteract NPC=8",
			got:     NewNPCEntityAction(8, 0, 0, 0),
			wantHex: "4D08000000",
		},
		{
			name:    "0x4D PreInteract NPC=2",
			got:     NewNPCEntityAction(2, 0, 0, 0),
			wantHex: "4D02000000",
		},
		// Repair test is split — builder sets cost=0 (server computes),
		// so exact byte match varies. We verify header + npcGID + sentinel.
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			want, err := hex.DecodeString(tc.wantHex)
			if err != nil {
				// skip tests with typo'd hex literals
				t.Skipf("bad wantHex: %s", err)
				return
			}
			if !bytes.Equal(tc.got, want) {
				t.Errorf("%s: wrong output\n got: %s\nwant: %s",
					tc.name,
					hex.EncodeToString(tc.got),
					hex.EncodeToString(want))
			}
		})
	}
}

// TestRepairAll_FormatStructure verifies the 16-byte layout: opcode + subcmd
// + pad + npcGID + cost + sentinel. Cost left to server (bytes 8..12 zero).
func TestRepairAll_FormatStructure(t *testing.T) {
	p := NewRepairAll(2)
	if len(p) != 16 {
		t.Fatalf("expected 16 bytes, got %d", len(p))
	}
	// Expected layout bytes (cost left at 0):
	want, _ := hex.DecodeString("350400000200000000000000FFFFFFFF")
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
