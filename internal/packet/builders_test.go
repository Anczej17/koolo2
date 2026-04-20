package packet

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"testing"

	"local/internal/svc/internal/gamelib/data"
)

// Verifies that each builder emits the exact byte layout documented in its
// file header (which traces back to live capture in
// logs/live_captures_2026_04_15/*). Catches any future regression where a
// builder's format silently drifts away from the captured wire shape.

func TestNewMoveToEntity_0x04(t *testing.T) {
	p := NewMoveToEntity(MoveToEntityActionWalk, 0x12FB1101, 1) // NPC unit type
	got := p.GetPayload()
	if len(got) != 18 {
		t.Fatalf("0x04 length: got %d want 18", len(got))
	}
	if got[0] != 0x04 {
		t.Fatalf("0x04 opcode byte: got 0x%02X want 0x04", got[0])
	}
	if binary.LittleEndian.Uint32(got[1:5]) != MoveToEntityActionWalk {
		t.Fatalf("0x04 action: got 0x%08X want 0x%08X",
			binary.LittleEndian.Uint32(got[1:5]), MoveToEntityActionWalk)
	}
	if binary.LittleEndian.Uint32(got[5:9]) != 0x12FB1101 {
		t.Fatalf("0x04 targetGID: got 0x%08X want 0x12FB1101",
			binary.LittleEndian.Uint32(got[5:9]))
	}
	if got[9] != 1 {
		t.Fatalf("0x04 srcUnitType: got %d want 1", got[9])
	}
}

func TestNewItemFromStash_0x18(t *testing.T) {
	got := NewItemFromStash(data.UnitID(0x53), 4, 1)
	if len(got) != 22 {
		t.Fatalf("0x18 length: got %d want 22 (live capture 22B, was 21 prior to 04-15 fix)", len(got))
	}
	if got[0] != 0x18 {
		t.Fatalf("0x18 opcode: got 0x%02X want 0x18", got[0])
	}
	if binary.LittleEndian.Uint32(got[1:5]) != 0x53 {
		t.Fatalf("0x18 itemGID: got 0x%08X want 0x53", binary.LittleEndian.Uint32(got[1:5]))
	}
}

func TestNewItemToStash_0x19(t *testing.T) {
	got := NewItemToStash(data.UnitID(0x53), 7, 1)
	if len(got) != 22 {
		t.Fatalf("0x19 length: got %d want 22 (live capture format)", len(got))
	}
	if got[0] != 0x19 {
		t.Fatalf("0x19 opcode: got 0x%02X want 0x19", got[0])
	}
	if binary.LittleEndian.Uint32(got[1:5]) != 0x53 {
		t.Fatalf("0x19 gid: got 0x%08X want 0x53", binary.LittleEndian.Uint32(got[1:5]))
	}
	if got[17] != 7 || got[19] != 1 {
		t.Fatalf("0x19 dest: got col=%d row=%d want col=7 row=1", got[17], got[19])
	}
	if got[21] != 0x03 {
		t.Fatalf("0x19 term: got 0x%02X want 0x03", got[21])
	}
}

func TestNewGoldTransfer_0x27(t *testing.T) {
	p := NewGoldTransfer(GoldTransferDeposit, 0x3CAE7, 0xBBA5)
	got := p.GetPayload()
	if len(got) != 17 {
		t.Fatalf("0x27 length: got %d want 17", len(got))
	}
	if got[0] != 0x27 {
		t.Fatalf("0x27 opcode: got 0x%02X want 0x27", got[0])
	}
	if binary.LittleEndian.Uint32(got[1:5]) != GoldTransferDeposit {
		t.Fatalf("0x27 action: got 0x%08X want 0x%08X (deposit)",
			binary.LittleEndian.Uint32(got[1:5]), GoldTransferDeposit)
	}
	if binary.LittleEndian.Uint32(got[5:9]) != 0x3CAE7 {
		t.Fatalf("0x27 balance: got 0x%X want 0x3CAE7", binary.LittleEndian.Uint32(got[5:9]))
	}
	if binary.LittleEndian.Uint32(got[9:13]) != 0xBBA5 {
		t.Fatalf("0x27 amount: got 0x%X want 0xBBA5", binary.LittleEndian.Uint32(got[9:13]))
	}
	// Trailer 0x5B44FFFF from live capture
	if binary.LittleEndian.Uint32(got[13:17]) != 0x5B44FFFF {
		t.Fatalf("0x27 trailer: got 0x%X want 0x5B44FFFF", binary.LittleEndian.Uint32(got[13:17]))
	}
}

func TestNewNPCSellItem_0x33(t *testing.T) {
	got := NewNPCSellItem(0x1D4C, data.UnitID(0x38), data.UnitID(0x0E), 5, 2, 0x03)
	if len(got) != 22 {
		t.Fatalf("0x33 length: got %d want 22 (capture_buysell.log last_diff=21)", len(got))
	}
	if got[0] != 0x33 {
		t.Fatalf("0x33 opcode: got 0x%02X want 0x33", got[0])
	}
	if binary.LittleEndian.Uint32(got[13:17]) != 0x00080009 {
		t.Fatalf("0x33 constant bytes 13-16: want 09 00 08 00, got %X", got[13:17])
	}
	if binary.LittleEndian.Uint16(got[17:19]) != 5 {
		t.Fatalf("0x33 slot: got %d want 5", binary.LittleEndian.Uint16(got[17:19]))
	}
	if got[21] != 0x03 {
		t.Fatalf("0x33 term: got 0x%02X want 0x03", got[21])
	}
}

func TestNewTpConfirmTravel_0x43(t *testing.T) {
	p := NewTpConfirmTravel()
	got := p.GetPayload()
	if len(got) != 13 {
		t.Fatalf("0x43 length: got %d want 13 (was 9B prior to 04-18 fix)", len(got))
	}
	if got[0] != 0x43 {
		t.Fatalf("0x43 opcode: got 0x%02X want 0x43", got[0])
	}
	if binary.LittleEndian.Uint32(got[1:5]) != 1 {
		t.Fatalf("0x43 act: got %d want 1", binary.LittleEndian.Uint32(got[1:5]))
	}
	if binary.LittleEndian.Uint32(got[5:9]) != 1 {
		t.Fatalf("0x43 arg: got %d want 1", binary.LittleEndian.Uint32(got[5:9]))
	}
	if binary.LittleEndian.Uint32(got[9:13]) != 0x00000000 {
		t.Fatalf("0x43 trailer: got 0x%X want 0x00000000 (live capture 04-19 audit)", binary.LittleEndian.Uint32(got[9:13]))
	}
}

func TestNewTpDestinationSelect_0x4B(t *testing.T) {
	p := NewTpDestinationSelect(6) // dest 6 from live 05_waypoint_travel.json
	got := p.GetPayload()
	if len(got) != 13 {
		t.Fatalf("0x4B length: got %d want 13 (was 5B prior to 04-18 fix)", len(got))
	}
	if got[0] != 0x4B {
		t.Fatalf("0x4B opcode: got 0x%02X want 0x4B", got[0])
	}
	if binary.LittleEndian.Uint32(got[1:5]) != 6 {
		t.Fatalf("0x4B dest: got %d want 6", binary.LittleEndian.Uint32(got[1:5]))
	}
	if binary.LittleEndian.Uint32(got[5:9]) != 0x00000002 {
		t.Fatalf("0x4B action: got 0x%X want 0x2", binary.LittleEndian.Uint32(got[5:9]))
	}
	if binary.LittleEndian.Uint32(got[9:13]) != 0xFFFFFFFF {
		t.Fatalf("0x4B trailer: got 0x%X want 0xFFFFFFFF", binary.LittleEndian.Uint32(got[9:13]))
	}
}

func TestNewItemMoveStash_0x54(t *testing.T) {
	// From live 03_stash_jewel.json: 5453000000000000000700010004000000090007
	// Opcode 0x54, itemGID 0x53, srcCtx=0(inv) col=7 row=1, dstCtx=4(stash) col=0 row=0.
	p := NewItemMoveStash(data.UnitID(0x53), InvCtxInventory, 7, 1, InvCtxStashMain, 0, 0)
	got := p.GetPayload()
	if len(got) != 20 {
		t.Fatalf("0x54 length: got %d want 20 (item move, NOT 34B cube transmute)", len(got))
	}
	if got[0] != 0x54 {
		t.Fatalf("0x54 opcode: got 0x%02X want 0x54", got[0])
	}
	if binary.LittleEndian.Uint32(got[1:5]) != 0x53 {
		t.Fatalf("0x54 itemGID: got 0x%X want 0x53", binary.LittleEndian.Uint32(got[1:5]))
	}
	if got[9] != InvCtxInventory {
		t.Fatalf("0x54 srcCtx: got %d want 0 (InvCtxInventory)", got[9])
	}
	if got[10] != 7 {
		t.Fatalf("0x54 srcCol: got %d want 7", got[10])
	}
	if got[12] != 1 {
		t.Fatalf("0x54 srcRow: got %d want 1", got[12])
	}
	if got[13] != InvCtxStashMain {
		t.Fatalf("0x54 dstCtx: got %d want 4 (InvCtxStashMain)", got[13])
	}
}

func TestNewWeaponSwap_0x50(t *testing.T) {
	// 0x50 weapon swap - ground truth 30B from logs/sec_*.log
	got := NewWeaponSwap(data.UnitID(0x5C), data.UnitID(0x56), data.UnitID(0x4B), data.UnitID(0x50), 0)
	if len(got) != 30 {
		t.Fatalf("0x50 length: got %d want 30 (ground truth)", len(got))
	}
	if got[0] != 0x50 {
		t.Fatalf("0x50 opcode: got 0x%02X want 0x50", got[0])
	}
}

func TestNewCubeTransmute_0x54_legacy_34B(t *testing.T) {
	// The 34B cube-transmute variant — distinct from NewItemMoveStash.
	got := NewCubeTransmute(data.UnitID(0x100))
	if len(got) != 34 {
		t.Fatalf("cube 0x54 legacy length: got %d want 34", len(got))
	}
	if got[0] != 0x54 {
		t.Fatalf("cube 0x54 opcode: got 0x%02X want 0x54", got[0])
	}
}

// TestNewNPCBuy_0x32 asserts the byte-15 constant is 0x08 (not 0x06 as
// stale code comment claimed), matching live 08_npc_with_trade.json buf=1
// entry#44 tick=40785078: `321c060000530000000e000000090008000700010003`.
func TestNewNPCBuy_0x32(t *testing.T) {
	got := NewNPCBuy(0x061C, data.UnitID(0x53), data.UnitID(0x0E), 7, 1, 0x03)
	wantHex := "321c060000530000000e000000090008000700010003"
	want, _ := hex.DecodeString(wantHex)
	if !bytes.Equal(got, want) {
		t.Fatalf("0x32 NPCBuy layout mismatch:\n got %s\nwant %s",
			hex.EncodeToString(got), wantHex)
	}
	if got[15] != 0x08 {
		t.Fatalf("0x32 byte 15 (buy constant): got 0x%02X want 0x08 (stale 0x06 was wrong per CAPTURE_AUDIT_2026_04_19)", got[15])
	}
}

// TestNewNPCChatInit_0x2F — 5B form. 13B form (derived from buf=1 mirror)
// CRASHED D2R ~100ms after emit in live test 2026-04-20 18:43/18:44. buf=1
// was showing D2R's INTERNAL mirror write, not an externally-valid packet
// shape. 5B = koolo-original safe form; server silently drops but D2R
// stays alive; HID fallback handles dialog open.
func TestNewNPCChatInit_0x2F(t *testing.T) {
	got := NewNPCChatInit(data.UnitID(0x0E), 0, 0)
	wantHex := "2f0e000000"
	want, _ := hex.DecodeString(wantHex)
	if !bytes.Equal(got, want) {
		t.Fatalf("0x2F NPCChatInit layout mismatch:\n got %s\nwant %s",
			hex.EncodeToString(got), wantHex)
	}
	if len(got) != 5 {
		t.Fatalf("0x2F length: got %d want 5 (13B variant crashed D2R 2026-04-20)", len(got))
	}
}

// TestNewNPCChatTerminate_0x30 — 5B form. 13B variant CRASHED D2R
// ~100-150ms after emit in back-to-back runs 2026-04-20 18:43:32 and
// 18:44:07. The 13B [opcode][npcGID][npcGID][npcPos] shape is D2R's
// internal mirror-buffer write, not an externally-valid external packet.
func TestNewNPCChatTerminate_0x30(t *testing.T) {
	got := NewNPCChatTerminate(data.UnitID(0x0E), 0, 0)
	wantHex := "300e000000"
	want, _ := hex.DecodeString(wantHex)
	if !bytes.Equal(got, want) {
		t.Fatalf("0x30 NPCChatTerminate layout mismatch:\n got %s\nwant %s",
			hex.EncodeToString(got), wantHex)
	}
	if len(got) != 5 {
		t.Fatalf("0x30 length: got %d want 5 (13B variant crashed D2R 2026-04-20)", len(got))
	}
}

// TestNewNPCChatTerminatePostTrade_0x30_22B asserts the 22-byte post-trade
// form matches live 08_npc_with_trade.json buf=1 entry#54 tick=40788671:
// `300e000000 07000000 01000000 00000000 0700 0100 03`.
func TestNewNPCChatTerminatePostTrade_0x30_22B(t *testing.T) {
	got := NewNPCChatTerminatePostTrade(data.UnitID(0x0E), 7, 1, 0x03)
	wantHex := "300e00000007000000010000000000000007000100030000"
	// Note: live shows `03` at byte 21 plus `00 00` residue — capture trims
	// those. Test 22B output exactly (no trailing residue).
	wantHex = wantHex[:22*2]
	want, _ := hex.DecodeString(wantHex)
	if !bytes.Equal(got, want) {
		t.Fatalf("0x30 PostTrade layout mismatch:\n got %s\nwant %s",
			hex.EncodeToString(got), wantHex)
	}
}

// TestNewNPCEntityAction_0x4D asserts the 5-byte form after the 29B
// live test 14:29:58 crash (exitCode=0xc0000005). The 29B claim was from
// buf=0 residue, not a real packet. Server silently drops 5B; callers
// HID-fallback when dialog doesn't open.
func TestNewNPCEntityAction_0x4D(t *testing.T) {
	got := NewNPCEntityAction(data.UnitID(0x0E), data.UnitID(0xD6), 0, 0, 0, 0)
	wantHex := "4d0e000000"
	want, _ := hex.DecodeString(wantHex)
	if !bytes.Equal(got, want) {
		t.Fatalf("0x4D NPCEntityAction layout mismatch:\n got  %s\nwant %s",
			hex.EncodeToString(got), wantHex)
	}
	if len(got) != 5 {
		t.Fatalf("0x4D length: got %d want 5 (29B variant crashed D2R 2026-04-20 14:29)", len(got))
	}
}
