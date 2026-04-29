package action

import (
	"reflect"
	"testing"

	"local/internal/svc/internal/gamelib/data"
)

func TestFindFreeCubeSlotPrefersCapturedOneByOneTransmuteShape(t *testing.T) {
	var grid [4][3]bool
	gem := data.Item{ID: 600}

	got := make([]data.Position, 0, 3)
	for range 3 {
		x, y, ok := findFreeCubeSlot(&grid, gem)
		if !ok {
			t.Fatalf("findFreeCubeSlot returned no slot")
		}
		got = append(got, data.Position{X: x, Y: y})
		markCubeSlotAt(&grid, x, y, 1, 1)
	}

	want := []data.Position{{X: 2, Y: 1}, {X: 2, Y: 2}, {X: 2, Y: 3}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("preferred cube slots mismatch: got %+v want %+v", got, want)
	}
}

func TestCubeRemainingIngredientGIDsDetectsNoOpTransmute(t *testing.T) {
	before := []data.Item{{UnitID: 0x66}, {UnitID: 0x67}, {UnitID: 0x68}}
	after := []data.Item{{UnitID: 0x67}, {UnitID: 0x99}}

	got := cubeRemainingIngredientGIDs(before, after)
	want := []int{0x67}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("remaining GIDs mismatch: got %+v want %+v", got, want)
	}
}
