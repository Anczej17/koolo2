package action

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/d2go/pkg/data/object"
	"github.com/hectorgimenez/koolo/internal/action/step"
	"github.com/hectorgimenez/koolo/internal/context"
	"github.com/hectorgimenez/koolo/internal/game"
	"github.com/hectorgimenez/koolo/internal/ui"
	"github.com/hectorgimenez/koolo/internal/utils"
)

// UseWorldStoneShard finds and uses a World Stone Shard for the specified act
// actNumber: 1 for Act 1, 2 for Act 2, etc.
func UseWorldStoneShard(actNumber int) error {
	ctx := context.Get()
	ctx.SetLastAction("UseWorldStoneShard")

	ctx.Logger.Info("Attempting to use World Stone Shard", slog.Int("act", actNumber))

	// Ensure we're in town
	if !ctx.Data.PlayerUnit.Area.IsTown() {
		return errors.New("must be in town to use World Stone Shard")
	}

	// Open stash
	if !ctx.Data.OpenMenus.Stash {
		bank, found := ctx.Data.Objects.FindOne(object.Bank)
		if !found {
			return errors.New("stash not found")
		}
		err := InteractObject(bank, func() bool {
			return ctx.Data.OpenMenus.Stash
		})
		if err != nil {
			return fmt.Errorf("failed to open stash: %w", err)
		}
	}

	// WSS are ALWAYS in Materials tab (DLC items)
	// No need to check personal/shared stash
	ctx.Logger.Debug("Searching for WSS in Materials tab (DLC items always go here)",
		slog.Int("act", actNumber),
		slog.Int("expected_id", 673+actNumber))

	SwitchStashTab(StashTabMaterials)
	utils.Sleep(500)
	ctx.RefreshGameData()

	materialsItems := ctx.Data.Inventory.ByLocation(item.LocationMaterialsTab)
	ctx.Logger.Info("Materials tab contents",
		slog.Int("total_items", len(materialsItems)))

	// Log ALL items in Materials tab for debugging
	var wssItem *data.Item
	expectedID := 673 + actNumber

	for idx, itm := range materialsItems {
		ctx.Logger.Debug("Materials tab item",
			slog.Int("index", idx),
			slog.Int("item_id", itm.ID),
			slog.String("name", string(itm.Name)),
			slog.String("desc", itm.Desc().Name),
			slog.Int("pos_x", itm.Position.X),
			slog.Int("pos_y", itm.Position.Y),
			slog.String("location", string(itm.Location.LocationType)))

		if isWorldStoneShardForAct(itm, actNumber) {
			wssItem = &itm
			ctx.Logger.Info("✓ Found World Stone Shard in Materials tab",
				slog.Int("item_id", itm.ID),
				slog.Int("expected_id", expectedID),
				slog.Int("list_index", idx),
				slog.String("name", string(itm.Name)))
		}
	}

	if wssItem == nil {
		return fmt.Errorf("World Stone Shard for Act %d not found in Materials tab", actNumber)
	}

	// WSS is in Materials tab, ensure we're on that tab and get fresh coordinates
	ctx.Logger.Info("Preparing to extract WSS from Materials tab")
	SwitchStashTab(StashTabMaterials)
	utils.Sleep(500)
	ctx.RefreshGameData()

	// Find WSS again to get fresh coordinates after tab switch
	var freshWSS *data.Item
	for _, itm := range ctx.Data.Inventory.ByLocation(item.LocationMaterialsTab) {
		if isWorldStoneShardForAct(itm, actNumber) {
			freshWSS = &itm
			break
		}
	}

	if freshWSS == nil {
		return errors.New("WSS disappeared from Materials tab after refresh")
	}

	// COORDINATES - Mix of confirmed and calculated
	// Shards 1,2,3 confirmed via testing | Shards 4,5 calculated with auto-search fallback
	confirmedCoords := map[int]data.Position{
		674: {X: 274, Y: 230}, // Western (Act 1) - CONFIRMED
		675: {X: 307, Y: 230}, // Eastern (Act 2) - CONFIRMED
		676: {X: 340, Y: 230}, // Southern (Act 3) - CONFIRMED
	}

	calculatedCoords := map[int]data.Position{
		677: {X: 373, Y: 230}, // Deep (Act 4) - Calculated (274 + 33*3)
		678: {X: 406, Y: 230}, // Northern (Act 5) - Calculated (274 + 33*4)
	}

	var extracted bool
	var attempts []data.Position

	// Try confirmed coords first, then calculated, then auto-search
	if pos, confirmed := confirmedCoords[freshWSS.ID]; confirmed {
		attempts = []data.Position{pos}
	} else if pos, calculated := calculatedCoords[freshWSS.ID]; calculated {
		// For calculated coords, try primary + nearby variants
		attempts = []data.Position{
			pos,                               // Primary calculated
			{X: pos.X - 3, Y: pos.Y},         // -3px
			{X: pos.X + 3, Y: pos.Y},         // +3px
			{X: pos.X - 6, Y: pos.Y},         // -6px
			{X: pos.X + 6, Y: pos.Y},         // +6px
			{X: pos.X, Y: pos.Y + 10},        // Y+10
			{X: pos.X, Y: pos.Y - 10},        // Y-10
		}
	} else {
		return fmt.Errorf("no coordinates available for WSS ID=%d", freshWSS.ID)
	}

	for attemptNum, coords := range attempts {
		ctx.Logger.Info("Trying WSS extraction",
			slog.Int("attempt", attemptNum+1),
			slog.Int("item_id", freshWSS.ID),
			slog.String("name", string(freshWSS.Name)),
			slog.Int("screen_x", coords.X),
			slog.Int("screen_y", coords.Y))

		// Extract: MovePointer → Click with Ctrl → Wait
		ctx.HID.MovePointer(coords.X, coords.Y)
		utils.Sleep(200)
		ctx.HID.ClickWithModifier(game.LeftButton, coords.X, coords.Y, game.CtrlKey)
		utils.Sleep(600)

		// Verify extraction succeeded
		ctx.RefreshGameData()
		for _, itm := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
			if isWorldStoneShardForAct(itm, actNumber) {
				extracted = true
				ctx.Logger.Info("✓ WSS extracted to inventory",
					slog.Int("final_x", coords.X),
					slog.Int("final_y", coords.Y))
				break
			}
		}

		if extracted {
			break
		}

		ctx.Logger.Debug("Extraction attempt failed, trying next coords")
	}

	if !extracted {
		return fmt.Errorf("failed to extract WSS (ID=%d) after %d attempts", freshWSS.ID, len(attempts))
	}

	// Close stash
	step.CloseAllMenus()
	utils.Sleep(300)

	// Use WSS from inventory
	ctx.RefreshGameData()
	var wssInInventory *data.Item
	for _, itm := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
		if isWorldStoneShardForAct(itm, actNumber) {
			wssInInventory = &itm
			break
		}
	}

	if wssInInventory == nil {
		return errors.New("WSS disappeared from inventory before use")
	}

	// Open inventory and right-click WSS
	ctx.HID.PressKeyBinding(ctx.Data.KeyBindings.Inventory)
	utils.Sleep(300)

	invScreenPos := ui.GetScreenCoordsForItem(*wssInInventory)
	ctx.Logger.Info("Using WSS from inventory",
		slog.Int("inv_screen_x", invScreenPos.X),
		slog.Int("inv_screen_y", invScreenPos.Y))

	ctx.HID.Click(game.RightButton, invScreenPos.X, invScreenPos.Y)
	utils.Sleep(1000)

	ctx.Logger.Info("World Stone Shard used successfully")

	// Close inventory
	return step.CloseAllMenus()
}

// isWorldStoneShardForAct checks if an item is a World Stone Shard for the specified act
// Uses item ID for reliable identification (much better than string matching)
// WSS IDs by act:
// Act 1: 674 (Western World Stone Shard)
// Act 2: 675 (Eastern World Stone Shard)
// Act 3: 676 (Southern World Stone Shard)
// Act 4: 677 (Deep World Stone Shard)
// Act 5: 678 (Northern World Stone Shard)
func isWorldStoneShardForAct(itm data.Item, actNumber int) bool {
	// Map act number to World Stone Shard item ID
	var expectedID int
	switch actNumber {
	case 1:
		expectedID = 674 // Western
	case 2:
		expectedID = 675 // Eastern
	case 3:
		expectedID = 676 // Southern
	case 4:
		expectedID = 677 // Deep
	case 5:
		expectedID = 678 // Northern
	default:
		return false
	}

	// Direct ID comparison - much more reliable than string matching
	return itm.ID == expectedID
}

// CheckWorldStoneShardAvailability checks if WSS is available for the specified act
// Returns true if shard exists in Materials tab, false otherwise
// Uses same simple pattern as uber organ detection - trusts game data after proper refresh
func CheckWorldStoneShardAvailability(actNumber int) bool {
	ctx := context.Get()

	// Simple check like uber organs - just scan inventory locations
	// Game data is reliable after stash is opened and tab is switched
	for _, itm := range ctx.Data.Inventory.ByLocation(item.LocationStash, item.LocationSharedStash, item.LocationMaterialsTab) {
		if isWorldStoneShardForAct(itm, actNumber) {
			ctx.Logger.Info("WSS found in stash",
				slog.Int("act", actNumber),
				slog.Int("item_id", itm.ID),
				slog.String("name", string(itm.Name)),
				slog.String("location", string(itm.Location.LocationType)))
			return true
		}
	}

	ctx.Logger.Warn("WSS not found in stash for act",
		slog.Int("act", actNumber),
		slog.Int("expected_id", 673+actNumber))
	return false
}
