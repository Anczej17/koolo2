package town

import (
	"testing"

	"local/internal/svc/internal/context"
	"local/internal/svc/internal/game"
	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/item"
	"local/internal/svc/internal/gamelib/data/stat"
)

func vendorCalculatorTestContext() *context.Status {
	ctx := &context.Status{Context: &context.Context{Data: &game.Data{}}}
	ctx.Data.OpenMenus.NPCShop = true
	return ctx
}

func TestVendorComputedSellPriceKnownUniqueTargets(t *testing.T) {
	ctx := vendorCalculatorTestContext()

	tests := []struct {
		name string
		it   data.Item
		want uint32
	}{
		{
			name: "Crack of the Heavens",
			it:   data.Item{ID: 620, Quality: item.QualityUnique, UniqueSetID: 403},
			want: 2800,
		},
		{
			name: "Chance Guards",
			it:   data.Item{ID: 336, Quality: item.QualityUnique, UniqueSetID: 104},
			want: 2931,
		},
	}
	for _, tt := range tests {
		got, ok := vendorComputedSellPrice(ctx, tt.it)
		if !ok {
			t.Fatalf("%s: calculator returned !ok", tt.name)
		}
		if got.Price != tt.want {
			t.Fatalf("%s: price=%d want=%d detail=%+v", tt.name, got.Price, tt.want, got)
		}
	}
}

func TestVendorComputedSellPriceMagicSmallCharm(t *testing.T) {
	ctx := vendorCalculatorTestContext()
	it := data.Item{
		ID:      618,
		Quality: item.QualityMagic,
		Stats:   stat.Stats{{ID: stat.PoisonResist, Value: 11}},
	}
	got, ok := vendorComputedSellPrice(ctx, it)
	if !ok {
		t.Fatal("calculator returned !ok")
	}
	if got.Price != 1236 {
		t.Fatalf("price=%d want=1236 detail=%+v", got.Price, got)
	}
}

func TestVendorComputedSellPriceMagicSimbilanEstimate(t *testing.T) {
	ctx := vendorCalculatorTestContext()
	it := data.Item{
		ID:              142,
		Quality:         item.QualityMagic,
		StackedQuantity: 60,
		Stats:           stat.Stats{{ID: stat.IncreasedAttackSpeed, Value: 10}},
	}
	got, ok := vendorComputedSellPrice(ctx, it)
	if !ok {
		t.Fatal("calculator returned !ok")
	}
	// Prior live tooltip for Snake's Simbilan of Readiness was 4704. This
	// target catches the throwing-weapon branch; exact magic stat coverage is
	// still being verified against live stats before production wiring.
	if got.Price < 4650 || got.Price > 4760 {
		t.Fatalf("price=%d outside expected Simbilan band detail=%+v", got.Price, got)
	}
}
