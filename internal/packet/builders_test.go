package packet

import (
	"encoding/binary"
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
	if len(got) != 21 {
		t.Fatalf("0x19 length: got %d want 21", len(got))
	}
	if got[0] != 0x19 {
		t.Fatalf("0x19 opcode: got 0x%02X want 0x19", got[0])
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
	got := NewNPCSellItem(0x68A, data.UnitID(0x17), data.UnitID(0x08), 0, 0, 0)
	if len(got) != 24 {
		t.Fatalf("0x33 length: got %d want 24 (cursor-sell format per Discord 04-15)", len(got))
	}
	if got[0] != 0x33 {
		t.Fatalf("0x33 opcode: got 0x%02X want 0x33", got[0])
	}
	if binary.LittleEndian.Uint32(got[1:5]) != 0x68A {
		t.Fatalf("0x33 sellPrice: got 0x%X want 0x68A", binary.LittleEndian.Uint32(got[1:5]))
	}
	if binary.LittleEndian.Uint32(got[5:9]) != 0x17 {
		t.Fatalf("0x33 itemGID: got 0x%X want 0x17", binary.LittleEndian.Uint32(got[5:9]))
	}
	if binary.LittleEndian.Uint32(got[9:13]) != 0x08 {
		t.Fatalf("0x33 npcGID: got 0x%X want 0x08", binary.LittleEndian.Uint32(got[9:13]))
	}
	// Bytes 13..23 are 11 zero bytes per cursor-sell format.
	for i := 13; i < 24; i++ {
		if got[i] != 0 {
			t.Fatalf("0x33 byte[%d]: got 0x%02X want 0x00 (zero-pad)", i, got[i])
		}
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
	if binary.LittleEndian.Uint32(got[9:13]) != 0xFFFFFFFF {
		t.Fatalf("0x43 trailer: got 0x%X want 0xFFFFFFFF", binary.LittleEndian.Uint32(got[9:13]))
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
	got := NewWeaponSwap(data.UnitID(0x5C), data.UnitID(0x56), data.UnitID(0x4B), data.UnitID(0x50))
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
