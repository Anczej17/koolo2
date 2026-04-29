package amb

import (
	"encoding/hex"
	"testing"

	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/area"
	"local/internal/svc/internal/gamelib/data/item"
)

func assertPayloadHex(t *testing.T, name string, payload []byte, want string) {
	t.Helper()
	got := hex.EncodeToString(payload)
	if got != want {
		t.Fatalf("%s payload mismatch:\n got  %s\n want %s", name, got, want)
	}
}

func TestNPCDialogPacketsMatchAmbLayout(t *testing.T) {
	const npcID data.UnitID = 0x0e

	assertPayloadHex(t, "NPCInit", NewNPCInit(npcID).GetPayload(), "2f0e000000")
	assertPayloadHex(t, "NPCCancel", NewNPCCancel(npcID).GetPayload(), "300e000000")
	assertPayloadHex(t, "NPCAction trade", NewNPCAction(0x01, npcID).GetPayload(), "38010000000e000000")
	assertPayloadHex(t, "NPCAction second option", NewNPCAction(0x02, npcID).GetPayload(), "38020000000e000000")
}

func TestNPCIdentifyItemsMatchesAmbLayout(t *testing.T) {
	assertPayloadHex(
		t,
		"NPCIdentifyItems with cube",
		NewNPCIdentifyItems(0x02, 0x1234, 0x0004, 0x0003).GetPayload(),
		"340200000000000000341200000000000004000300",
	)

	assertPayloadHex(
		t,
		"NPCIdentifyItems without cube",
		NewNPCIdentifyItems(0x02, 0, 0, 0).GetPayload(),
		"340200000000000000ffffffffff00000000000000",
	)
}

func TestMoveItemToCubeMatchesAmbLayout(t *testing.T) {
	assertPayloadHex(
		t,
		"MoveItemToCube",
		NewMoveItemToCube(0x11, 0x22, 0x0001, 0x0002).GetPayload(),
		"2a1100000022000000000000000000020001000200",
	)
}

func TestUseCubeTransmuteMatchesAmbLayout(t *testing.T) {
	cube := data.Item{
		UnitID:   0x59,
		Location: item.Location{LocationType: item.LocationStash},
	}
	items := []data.Item{
		{UnitID: 0x58, Position: data.Position{X: 2, Y: 1}},
		{UnitID: 0x71, Position: data.Position{X: 2, Y: 2}},
		{UnitID: 0x62, Position: data.Position{X: 2, Y: 3}},
	}
	assertPayloadHex(
		t,
		"UseCubeTransmute",
		NewUseCubeTransmute(cube, items).GetPayload(),
		"2059000000048802580000001271000000226200000032",
	)
}

func TestWaypointInteractionMatchesAmbLayout(t *testing.T) {
	assertPayloadHex(
		t,
		"Waypoint Catacombs Level 2",
		NewWaypointInteraction(area.CatacombsLevel2).GetPayload(),
		"4b23000000",
	)
}
