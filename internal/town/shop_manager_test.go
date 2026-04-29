package town

import (
	"testing"

	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/item"
	"local/internal/svc/internal/gamelib/data/stat"
)

func TestParseVendorTooltipPriceUsesNumberAfterSellLabel(t *testing.T) {
	texts := []string{
		"Glyph Blow\nMartel de Fer\nRequired Level: 25\nSell value: 1631",
	}

	price, ok := parseVendorTooltipPrice(texts, vendorPriceSell)
	if !ok {
		t.Fatal("expected sell price")
	}
	if price != 1631 {
		t.Fatalf("expected 1631, got %d", price)
	}
}

func TestParseVendorTooltipPriceSupportsSplitPriceLine(t *testing.T) {
	texts := []string{
		"Sell value:",
		"36,000",
	}

	price, ok := parseVendorTooltipPrice(texts, vendorPriceSell)
	if !ok {
		t.Fatal("expected split-line sell price")
	}
	if price != 36000 {
		t.Fatalf("expected 36000, got %d", price)
	}
}

func TestParseVendorRenderCachePriceRequiresMagicItemStatMatchForWeakBaseName(t *testing.T) {
	it := data.Item{
		Name:           item.Name("SmallCharm"),
		Quality:        item.QualityMagic,
		IdentifiedName: "Blazing Small Charm",
		Stats: stat.Stats{
			{ID: stat.PoisonResist, Value: 11, Layer: 0},
		},
	}
	texts := []string{
		"Small Charm\nAnnihilus\nSell value: 3502",
		"Poison Resist +11%\nRequired Level: 32\nKeep in Inventory to Gain Bonus\nEmerald Small Charm\nSell value: 1236",
	}

	price, ok := parseVendorRenderCachePrice(texts, it, vendorPriceSell)
	if !ok {
		t.Fatal("expected magic charm price")
	}
	if price != 1236 {
		t.Fatalf("expected 1236, got %d", price)
	}
}

func TestParseVendorRenderCachePriceRejectsMagicWeakBaseWithoutStatMatch(t *testing.T) {
	it := data.Item{
		Name:           item.Name("SmallCharm"),
		Quality:        item.QualityMagic,
		IdentifiedName: "Blazing Small Charm",
		Stats: stat.Stats{
			{ID: stat.PoisonResist, Value: 11, Layer: 0},
		},
	}
	texts := []string{
		"Small Charm\nAnnihilus\nSell value: 3502",
	}

	if price, ok := parseVendorRenderCachePrice(texts, it, vendorPriceSell); ok {
		t.Fatalf("expected no price, got %d", price)
	}
}

func TestVendorSellItemCategory(t *testing.T) {
	tests := []struct {
		name string
		id   int
		want vendorSellCategory
	}{
		{name: "morning star is weapon", id: 20, want: vendorSellCategoryWeapon},
		{name: "broad sword is weapon", id: 30, want: vendorSellCategoryWeapon},
		{name: "wand is weapon", id: 10, want: vendorSellCategoryWeapon},
		{name: "armor is armor", id: 313, want: vendorSellCategoryArmor},
		{name: "shield is armor", id: 328, want: vendorSellCategoryArmor},
		{name: "ring is misc", id: 537, want: vendorSellCategoryMisc},
		{name: "charm is misc", id: 618, want: vendorSellCategoryMisc},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := vendorSellItemCategory(data.Item{ID: tt.id})
			if got != tt.want {
				t.Fatalf("expected %s, got %s", tt.want, got)
			}
		})
	}
}

func TestVendorSellCandidatePagesPreferEarliestLegalCategoryPage(t *testing.T) {
	snap := vendorSellGridCache{
		namePages:   map[item.Name][]byte{},
		typePages:   map[string][]byte{},
		familyPages: map[string][]byte{"weapon": {2, 1}},
		allPages:    []byte{3, 2, 1, 0},
	}

	got := vendorSellCandidatePagesFromSnapshot(snap, data.Item{ID: 20})
	wantPrefix := []byte{1, 2}
	for idx, want := range wantPrefix {
		if idx >= len(got) || got[idx] != want {
			t.Fatalf("expected prefix %v, got %v", wantPrefix, got)
		}
	}
}

func TestVendorSellCandidatePagesPreferExactTypeBeforeWeaponDefaults(t *testing.T) {
	snap := vendorSellGridCache{
		namePages:   map[item.Name][]byte{},
		typePages:   map[string][]byte{item.TypeJavelin: {3}},
		familyPages: map[string][]byte{"weapon": {1, 2, 3}},
		allPages:    []byte{0, 1, 2, 3},
	}

	got := vendorSellCandidatePagesFromSnapshot(snap, data.Item{ID: 244})
	if len(got) == 0 || got[0] != 3 {
		t.Fatalf("expected javelin type page 3 first, got %v", got)
	}
}

func TestVendorSellCandidatePagesAllowsArmorPageZero(t *testing.T) {
	snap := vendorSellGridCache{
		namePages:   map[item.Name][]byte{},
		typePages:   map[string][]byte{},
		familyPages: map[string][]byte{"armor": {0}},
		allPages:    []byte{0, 1, 2, 3},
	}

	got := vendorSellCandidatePagesFromSnapshot(snap, data.Item{ID: 313})
	if len(got) == 0 || got[0] != 0 {
		t.Fatalf("expected armor page 0 first, got %v", got)
	}
}

func TestFirstFreeVendorCellUsesTopLeftFootprint(t *testing.T) {
	snap := vendorSellGridCache{pages: map[byte]*vendorSellPageGrid{1: {}}}
	grid := snap.pages[1]
	// Charsi tab-1 shape before the Maul placement: only a 2x4 top-left
	// footprint at x=3 can fit, and the top-most accepted square is y=4.
	markVendorSellGrid(grid, 0, 0, 2, 3)
	markVendorSellGrid(grid, 2, 0, 2, 4)
	markVendorSellGrid(grid, 4, 0, 2, 4)
	markVendorSellGrid(grid, 6, 0, 2, 3)
	markVendorSellGrid(grid, 8, 0, 2, 3)
	markVendorSellGrid(grid, 0, 3, 2, 3)
	markVendorSellGrid(grid, 7, 3, 2, 3)
	markVendorSellGrid(grid, 9, 3, 1, 4)
	markVendorSellGrid(grid, 2, 4, 1, 3)
	markVendorSellGrid(grid, 5, 4, 2, 3)
	markVendorSellGrid(grid, 0, 6, 2, 3)
	markVendorSellGrid(grid, 7, 6, 2, 3)
	markVendorSellGrid(grid, 2, 7, 1, 3)
	markVendorSellGrid(grid, 5, 7, 2, 3)
	markVendorSellGrid(grid, 9, 7, 1, 3)

	x, y, ok := firstFreeVendorCellFromSnapshot(snap, 1, 2, 4)
	if !ok {
		t.Fatal("expected free maul footprint")
	}
	if x != 3 || y != 4 {
		t.Fatalf("expected top-left footprint at 3,4, got %d,%d", x, y)
	}
}

func TestAppendVendorCellsUsesNativeSingleRowOrder(t *testing.T) {
	snap := vendorSellGridCache{pages: map[byte]*vendorSellPageGrid{0: {}}}
	markVendorSellGrid(snap.pages[0], 0, 0, 2, 1)

	cells := appendVendorCellsFromSnapshot(snap, 0, 2, 1)
	if len(cells) == 0 {
		t.Fatal("expected fallback cells")
	}
	if cells[0].x != 8 || cells[0].y != 0 {
		t.Fatalf("expected native single-row first cell at 8,0, got %d,%d", cells[0].x, cells[0].y)
	}
}

func TestVendorSellAppendPerPageLimitKeepsFullPageBudget(t *testing.T) {
	got := vendorSellAppendPerPageLimit(320, 4)
	if got != ambVendorGridW*ambVendorGridH {
		t.Fatalf("expected full page budget, got %d", got)
	}
}
