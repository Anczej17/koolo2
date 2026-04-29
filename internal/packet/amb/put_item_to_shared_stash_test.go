package amb

import (
	"encoding/binary"
	"testing"

	"local/internal/svc/internal/gamelib/data"
)

func TestPutItemToSharedStashMatchesLiveCapture(t *testing.T) {
	item := data.Item{
		UnitID:   0x01,
		Position: data.Position{X: 4, Y: 3},
	}
	const ownerID = 0x02

	got := NewPutItemToSharedStash(item, ownerID, 9, 1).GetPayload()
	if len(got) != 29 {
		t.Fatalf("0x55 length: got %d want 29", len(got))
	}
	want := []byte{
		0x55,
		0x01, 0x00, 0x00, 0x00,
		0x02, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
		0x04, 0x00,
		0x03, 0x00,
		0x04, 0x00, 0x00, 0x00,
		0x09, 0x00,
		0x01, 0x00,
		0x00, 0x00, 0x00, 0x00,
	}
	if string(got) != string(want) {
		t.Fatalf("0x55 live payload mismatch:\n got % X\nwant % X", got, want)
	}
}

func TestTakeItemFromSharedStashUsesOwnerIDAndSharedContainer(t *testing.T) {
	item := data.Item{
		UnitID:   0x1234,
		Position: data.Position{X: 2, Y: 3},
	}
	const ownerID = 0xABCDEF01

	got := NewTakeItemFromSharedStash(item, ownerID, 7, 1).GetPayload()
	if len(got) != 29 {
		t.Fatalf("0x55 length: got %d want 29", len(got))
	}
	if binary.LittleEndian.Uint32(got[5:9]) != ownerID {
		t.Fatalf("0x55 owner ID: got 0x%X want 0x%X", binary.LittleEndian.Uint32(got[5:9]), ownerID)
	}
	if binary.LittleEndian.Uint32(got[9:13]) != uint32(ContainerStash) {
		t.Fatalf("0x55 FromInvPage: got %d want shared stash page %d", binary.LittleEndian.Uint32(got[9:13]), ContainerStash)
	}
	if binary.LittleEndian.Uint32(got[17:21]) != uint32(ContainerInventory) {
		t.Fatalf("0x55 ToInvPage: got %d want inventory %d", binary.LittleEndian.Uint32(got[17:21]), ContainerInventory)
	}
	if binary.LittleEndian.Uint32(got[25:29]) != 1 {
		t.Fatalf("0x55 IsStashPage: got %d want 1 for shared->inventory", binary.LittleEndian.Uint32(got[25:29]))
	}
}
