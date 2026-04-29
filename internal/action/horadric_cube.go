package action

import (
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"

	"github.com/lxn/win"
	"local/internal/svc/internal/action/step"
	"local/internal/svc/internal/context"
	"local/internal/svc/internal/game"
	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/item"
	"local/internal/svc/internal/gamelib/data/object"
	"local/internal/svc/internal/packet/amb"
	"local/internal/svc/internal/ui"
	"local/internal/svc/internal/utils"
)

func CubeAddItems(items ...data.Item) error {
	ctx := context.Get()
	ctx.SetLastAction("CubeAddItems")

	// Ensure stash is open
	if !ctx.Data.OpenMenus.Stash {
		bank, found := ctx.Data.Objects.FindOne(object.Bank)
		if !found {
			return fmt.Errorf("CubeAddItems: bank object not found")
		}
		err := InteractObject(bank, func() bool {
			return ctx.Data.OpenMenus.Stash
		})
		if err != nil {
			return err
		}
		// The first stash open each game lands on personal; subsequent opens
		// remember the last tab/page.
		if !ctx.CurrentGame.HasOpenedStash {
			ctx.CurrentGame.CurrentStashTab = 1
			ctx.CurrentGame.HasOpenedStash = true
		}
	}
	// Clear messages like TZ change or public game spam.  Prevent bot from clicking on messages
	ClearMessages()
	ctx.Logger.Info("Adding items to the Horadric Cube", slog.Any("items", items))

	if ctx.PacketSender != nil {
		return cubeAddItemsPacket(ctx, items...)
	}

	// If items are on the Stash, pickup them to the inventory
	for _, itm := range items {
		nwIt := itm
		// Check if item is in any stash location (personal, shared, or DLC tabs)
		if nwIt.Location.LocationType != item.LocationStash &&
			nwIt.Location.LocationType != item.LocationSharedStash &&
			nwIt.Location.LocationType != item.LocationGemsTab &&
			nwIt.Location.LocationType != item.LocationMaterialsTab &&
			nwIt.Location.LocationType != item.LocationRunesTab {
			continue
		}

		if requiresPersonalStash(nwIt) {
			if nwIt.Location.LocationType == item.LocationSharedStash {
				return fmt.Errorf("quest item %s must be in personal stash to use the cube", nwIt.Name)
			}
			SwitchStashTab(1)
		} else {
			// Check in which tab the item is and switch to it
			switch nwIt.Location.LocationType {
			case item.LocationStash:
				SwitchStashTab(1)
			case item.LocationSharedStash:
				SwitchStashTab(nwIt.Location.Page + 1)
			case item.LocationGemsTab:
				SwitchStashTab(StashTabGems)
			case item.LocationMaterialsTab:
				SwitchStashTab(StashTabMaterials)
			case item.LocationRunesTab:
				SwitchStashTab(StashTabRunes)
			}
		}

		ctx.Logger.Debug("Item found on the stash, picking it up",
			slog.String("Item", string(nwIt.Name)),
			slog.String("Location", string(nwIt.Location.LocationType)),
			slog.Int("MemPosX", nwIt.Position.X),
			slog.Int("MemPosY", nwIt.Position.Y),
		)

		screenPos := ui.GetScreenCoordsForItem(nwIt)
		ctx.Logger.Debug("Clicking item at computed screen position",
			slog.String("Item", string(nwIt.Name)),
			slog.Int("ScreenX", screenPos.X),
			slog.Int("ScreenY", screenPos.Y),
		)
		ctx.HID.ClickWithModifier(game.LeftButton, screenPos.X, screenPos.Y, game.CtrlKey)
		utils.Sleep(300)
	}

	err := ensureCubeIsOpen()
	if err != nil {
		return err
	}

	err = ensureCubeIsEmpty()
	if err != nil {
		return err
	}

	// Refresh game data so items reflect their current inventory positions,
	// not their original stash/DLC tab positions from before the pickup phase.
	ctx.RefreshGameData()

	// Track DLC items already matched by their new UnitID to avoid matching
	// the same inventory item twice when multiple identical items are needed
	// (e.g., 3x PerfectAmethyst for a grand charm reroll).
	usedUnitIDs := make(map[data.UnitID]struct{})

	for _, itm := range items {
		var found *data.Item

		// DLC tab items (gems, runes, materials) get new UnitIDs when moved to
		// inventory, so we must match by Name in inventory instead of by UnitID.
		isDLC := itm.Location.LocationType == item.LocationGemsTab ||
			itm.Location.LocationType == item.LocationMaterialsTab ||
			itm.Location.LocationType == item.LocationRunesTab

		for _, updatedItem := range ctx.Data.Inventory.AllItems {
			if isDLC {
				if _, used := usedUnitIDs[updatedItem.UnitID]; used {
					continue
				}
				if updatedItem.Name == itm.Name && updatedItem.Location.LocationType == item.LocationInventory {
					found = &updatedItem
					break
				}
			} else {
				if updatedItem.UnitID == itm.UnitID {
					found = &updatedItem
					break
				}
			}
		}

		if found != nil {
			usedUnitIDs[found.UnitID] = struct{}{}
		} else {
			ctx.Logger.Warn("Item not found in inventory for cube",
				slog.String("Item", string(itm.Name)),
				slog.Int("UnitID", int(itm.UnitID)),
			)
			continue
		}

		ctx.Logger.Debug("Moving Item to the Horadric Cube",
			slog.String("Item", string(found.Name)),
			slog.String("Location", string(found.Location.LocationType)),
			slog.Int("PosX", found.Position.X),
			slog.Int("PosY", found.Position.Y),
		)

		screenPos := ui.GetScreenCoordsForItem(*found)
		ctx.HID.ClickWithModifier(game.LeftButton, screenPos.X, screenPos.Y, game.CtrlKey)
		utils.Sleep(500)
	}

	return nil
}

func CubeTransmute() error {
	ctx := context.Get()

	if ctx.PacketSender == nil {
		err := ensureCubeIsOpen()
		if err != nil {
			return err
		}
	}

	ctx.Logger.Debug("Transmuting items in the Horadric Cube")
	utils.Sleep(150)

	// Snapshot cube contents BEFORE so packet success is verified against the
	// actual cube state rather than silently masked by a UI click.
	cubeIngredientsBefore := len(ctx.Data.Inventory.ByLocation(item.LocationCube))

	if ctx.PacketSender != nil {
		cube, ok := ctx.Data.Inventory.Find("HoradricCube", item.LocationInventory, item.LocationStash, item.LocationSharedStash)
		if !ok {
			return errors.New("CubeTransmute: horadric cube not found for packet path")
		}
		// AMB 0x20 UseCubeTransmute is a single atomic packet — it carries
		// the cube GID plus every ingredient GID, ActionType=0x88 tells
		// D2R to commit the transmute recipe in one shot (no separate
		// Transmute+Commit handshake like the legacy 0x54 path required).
		itemsInCube := ctx.Data.Inventory.ByLocation(item.LocationCube)
		if len(itemsInCube) == 0 {
			return errors.New("CubeTransmute: cube has no packet-visible ingredients")
		}
		payload := amb.NewUseCubeTransmute(cube, itemsInCube).GetPayload()
		ctx.Logger.Debug("Sending AMB 0x20 UseCubeTransmute",
			slog.Int("cubeGID", int(cube.UnitID)),
			slog.String("hex", hex.EncodeToString(payload)),
			slog.Any("items", cubeTransmuteItemSnapshot(itemsInCube)))
		if err := ctx.PacketSender.UseCubeTransmute(cube, itemsInCube); err != nil {
			return fmt.Errorf("CubeTransmute: packet send failed: %w", err)
		}
		utils.Sleep(800)
		ctx.RefreshGameData()
		cubeIngredientsAfter := len(ctx.Data.Inventory.ByLocation(item.LocationCube))
		remainingBeforeGIDs := cubeRemainingIngredientGIDs(itemsInCube, ctx.Data.Inventory.ByLocation(item.LocationCube))
		if len(remainingBeforeGIDs) > 0 || cubeIngredientsAfter >= cubeIngredientsBefore {
			return fmt.Errorf("CubeTransmute: packet sent but cube still has ingredients (before=%d after=%d remaining_gids=%v)",
				cubeIngredientsBefore, cubeIngredientsAfter, remainingBeforeGIDs)
		}
	} else {
		// HID.Click — in-process SendMessageW on transmute button.
		if ctx.Data.LegacyGraphics {
			ctx.HID.Click(game.LeftButton, ui.CubeTransmuteBtnXClassic, ui.CubeTransmuteBtnYClassic)
		} else {
			ctx.HID.Click(game.LeftButton, ui.CubeTransmuteBtnX, ui.CubeTransmuteBtnY)
		}
	}

	utils.Sleep(2000)

	if ctx.PacketSender != nil {
		if err := moveCubeItemsToInventoryPacket(ctx); err != nil {
			return err
		}
	} else {
		// Take the items out of the cube
		for _, itm := range ctx.Data.Inventory.ByLocation(item.LocationCube) {
			ctx.Logger.Debug("Moving Item to the inventory", slog.String("Item", string(itm.Name)))

			screenPos := ui.GetScreenCoordsForItem(itm)

			ctx.HID.ClickWithModifier(game.LeftButton, screenPos.X, screenPos.Y, game.CtrlKey)
			utils.Sleep(500)
		}
	}

	return step.CloseAllMenus()
}

func cubeAddItemsPacket(ctx *context.Status, items ...data.Item) error {
	cube, found := ctx.Data.Inventory.Find("HoradricCube", item.LocationInventory, item.LocationStash, item.LocationSharedStash)
	if !found {
		return errors.New("CubeAddItems: horadric cube not found for packet path")
	}

	if err := ensureCubeIsEmptyPacket(ctx); err != nil {
		return err
	}
	ctx.RefreshGameData()

	occupied := cubeOccupancy(ctx.Data.Inventory.ByLocation(item.LocationCube), nil)
	usedUnitIDs := make(map[data.UnitID]struct{})

	for _, requested := range items {
		itm, ok := findCubeSourceItem(ctx, requested, usedUnitIDs)
		if !ok {
			return fmt.Errorf("CubeAddItems: item %s (%d) not found for packet cube move", requested.Name, requested.UnitID)
		}
		usedUnitIDs[itm.UnitID] = struct{}{}

		if itm.Location.LocationType == item.LocationCube {
			markCubeSlot(&occupied, itm)
			continue
		}

		toX, toY, ok := findFreeCubeSlot(&occupied, itm)
		if !ok {
			return fmt.Errorf("CubeAddItems: no free cube slot for %s (%dx%d)", itm.Name, itm.Desc().InventoryWidth, itm.Desc().InventoryHeight)
		}

		ctx.Logger.Debug("Moving item to cube via AMB 0x19/0x46 -> 0x18",
			slog.String("item", string(itm.Name)),
			slog.Int("itemGID", int(itm.UnitID)),
			slog.String("from", string(itm.Location.LocationType)),
			slog.Int("fromX", itm.Position.X),
			slog.Int("fromY", itm.Position.Y),
			slog.Int("cubeGID", int(cube.UnitID)),
			slog.Int("toX", toX),
			slog.Int("toY", toY))

		if err := pickItemToCursorPacket(ctx, itm); err != nil {
			return fmt.Errorf("CubeAddItems: pick %s failed: %w", itm.Name, err)
		}
		utils.PingSleep(utils.Light, 150)

		if err := ctx.PacketSender.PutItemToInventory(uint32(itm.UnitID), uint32(toX), uint32(toY), uint32(amb.ContainerCube)); err != nil {
			return fmt.Errorf("CubeAddItems: move %s to cube failed: %w", itm.Name, err)
		}
		markCubeSlotAt(&occupied, toX, toY, itm.Desc().InventoryWidth, itm.Desc().InventoryHeight)
		utils.PingSleep(utils.Medium, 350)
		ctx.RefreshGameData()
	}

	return nil
}

func ensureCubeIsEmptyPacket(ctx *context.Status) error {
	if len(ctx.Data.Inventory.ByLocation(item.LocationCube)) == 0 {
		return nil
	}
	ctx.Logger.Debug("Emptying Horadric Cube via AMB cursor packets")
	return moveCubeItemsToInventoryPacket(ctx)
}

func moveCubeItemsToInventoryPacket(ctx *context.Status) error {
	cubeItems := ctx.Data.Inventory.ByLocation(item.LocationCube)
	if len(cubeItems) == 0 {
		return nil
	}

	occupied := inventoryOccupancy(ctx.Data.Inventory.ByLocation(item.LocationInventory), cubeItems)
	for _, cubeItem := range cubeItems {
		toX, toY, ok := findFreeInventorySlot(&occupied, cubeItem)
		if !ok {
			return fmt.Errorf("CubeTransmute: no free inventory slot for cube item %s", cubeItem.Name)
		}

		ctx.Logger.Debug("Moving cube item to inventory via AMB 0x19 -> 0x18",
			slog.String("item", string(cubeItem.Name)),
			slog.Int("itemGID", int(cubeItem.UnitID)),
			slog.Int("fromX", cubeItem.Position.X),
			slog.Int("fromY", cubeItem.Position.Y),
			slog.Int("toX", toX),
			slog.Int("toY", toY))

		if err := ctx.PacketSender.PickItemFromContainer(cubeItem.UnitID, cubeItem.Position.X, cubeItem.Position.Y, amb.ContainerCube); err != nil {
			return fmt.Errorf("CubeTransmute: pick cube result failed: %w", err)
		}
		utils.PingSleep(utils.Light, 150)

		if err := ctx.PacketSender.PutItemToInventory(uint32(cubeItem.UnitID), uint32(toX), uint32(toY), uint32(amb.ContainerInventory)); err != nil {
			return fmt.Errorf("CubeTransmute: put cube result to inventory failed: %w", err)
		}
		markGridSlotAt(&occupied, toX, toY, cubeItem.Desc().InventoryWidth, cubeItem.Desc().InventoryHeight)
		utils.PingSleep(utils.Medium, 350)
		ctx.RefreshGameData()
	}
	return nil
}

func findCubeSourceItem(ctx *context.Status, requested data.Item, used map[data.UnitID]struct{}) (data.Item, bool) {
	if requested.UnitID != 0 {
		if updated, ok := ctx.Data.Inventory.FindByID(requested.UnitID); ok {
			if _, alreadyUsed := used[updated.UnitID]; !alreadyUsed {
				return updated, true
			}
		}
	}

	for _, updated := range ctx.Data.Inventory.AllItems {
		if _, alreadyUsed := used[updated.UnitID]; alreadyUsed {
			continue
		}
		if updated.Name != requested.Name {
			continue
		}
		switch updated.Location.LocationType {
		case item.LocationInventory, item.LocationStash, item.LocationSharedStash, item.LocationCube:
			return updated, true
		}
	}
	return data.Item{}, false
}

func pickItemToCursorPacket(ctx *context.Status, itm data.Item) error {
	switch itm.Location.LocationType {
	case item.LocationInventory:
		return ctx.PacketSender.PickItemFromContainer(itm.UnitID, itm.Position.X, itm.Position.Y, amb.ContainerInventory)
	case item.LocationStash:
		return ctx.PacketSender.PickItemFromContainer(itm.UnitID, itm.Position.X, itm.Position.Y, amb.ContainerStash)
	case item.LocationCube:
		return ctx.PacketSender.PickItemFromContainer(itm.UnitID, itm.Position.X, itm.Position.Y, amb.ContainerCube)
	case item.LocationSharedStash:
		ownerIndex := itm.Location.Page
		if ownerIndex <= 0 || ownerIndex >= len(ctx.Data.Inventory.StashTabUnitIDs) {
			return fmt.Errorf("shared stash owner unit id unavailable for page %d", itm.Location.Page)
		}
		return ctx.PacketSender.PullItemFromSharedStash(itm, uint32(ctx.Data.Inventory.StashTabUnitIDs[ownerIndex]))
	case item.LocationGemsTab, item.LocationMaterialsTab, item.LocationRunesTab:
		return fmt.Errorf("DLC stash tab packet pull is not wired for %s", itm.Location.LocationType)
	default:
		return fmt.Errorf("unsupported source location %s", itm.Location.LocationType)
	}
}

func cubeOccupancy(items []data.Item, ignore map[data.UnitID]struct{}) [4][3]bool {
	var grid [4][3]bool
	for _, itm := range items {
		if ignore != nil {
			if _, ok := ignore[itm.UnitID]; ok {
				continue
			}
		}
		markCubeSlot(&grid, itm)
	}
	return grid
}

func markCubeSlot(grid *[4][3]bool, itm data.Item) {
	markCubeSlotAt(grid, itm.Position.X, itm.Position.Y, itm.Desc().InventoryWidth, itm.Desc().InventoryHeight)
}

func markCubeSlotAt(grid *[4][3]bool, x, y, w, h int) {
	for yy := y; yy < y+h && yy < len(grid); yy++ {
		for xx := x; xx < x+w && xx < len(grid[yy]); xx++ {
			if yy >= 0 && xx >= 0 {
				grid[yy][xx] = true
			}
		}
	}
}

func findFreeCubeSlot(grid *[4][3]bool, itm data.Item) (int, int, bool) {
	w, h := itm.Desc().InventoryWidth, itm.Desc().InventoryHeight
	if w == 1 && h == 1 {
		for _, pos := range preferredCubeOneByOneSlots() {
			if canPlaceCube(grid, pos.X, pos.Y, w, h) {
				return pos.X, pos.Y, true
			}
		}
	}
	for y := 0; y <= 4-h; y++ {
		for x := 0; x <= 3-w; x++ {
			if canPlaceCube(grid, x, y, w, h) {
				return x, y, true
			}
		}
	}
	return 0, 0, false
}

func preferredCubeOneByOneSlots() []data.Position {
	// Live 2026-04-26 cube-transmute success used a vertical 1x1 stack in
	// column 2: packed positions 0x12, 0x22, 0x32. Keep that shape first for
	// gem/rune recipes; fall back to row-major below if these cells are busy.
	return []data.Position{
		{X: 2, Y: 1},
		{X: 2, Y: 2},
		{X: 2, Y: 3},
	}
}

func canPlaceCube(grid *[4][3]bool, x, y, w, h int) bool {
	if x < 0 || y < 0 || x+w > 3 || y+h > 4 {
		return false
	}
	for yy := y; yy < y+h; yy++ {
		for xx := x; xx < x+w; xx++ {
			if grid[yy][xx] {
				return false
			}
		}
	}
	return true
}

func inventoryOccupancy(items []data.Item, ignore []data.Item) [4][10]bool {
	ignored := make(map[data.UnitID]struct{}, len(ignore))
	for _, itm := range ignore {
		ignored[itm.UnitID] = struct{}{}
	}

	var grid [4][10]bool
	for _, itm := range items {
		if _, skip := ignored[itm.UnitID]; skip {
			continue
		}
		markGridSlotAt(&grid, itm.Position.X, itm.Position.Y, itm.Desc().InventoryWidth, itm.Desc().InventoryHeight)
	}
	return grid
}

func markGridSlotAt(grid *[4][10]bool, x, y, w, h int) {
	for yy := y; yy < y+h && yy < len(grid); yy++ {
		for xx := x; xx < x+w && xx < len(grid[yy]); xx++ {
			if yy >= 0 && xx >= 0 {
				grid[yy][xx] = true
			}
		}
	}
}

func findFreeInventorySlot(grid *[4][10]bool, itm data.Item) (int, int, bool) {
	w, h := itm.Desc().InventoryWidth, itm.Desc().InventoryHeight
	for y := 0; y <= 4-h; y++ {
		for x := 0; x <= 10-w; x++ {
			if canPlaceInventory(grid, x, y, w, h) {
				return x, y, true
			}
		}
	}
	return 0, 0, false
}

func cubeTransmuteItemSnapshot(items []data.Item) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, itm := range items {
		out = append(out, map[string]any{
			"name": string(itm.Name),
			"gid":  int(itm.UnitID),
			"x":    itm.Position.X,
			"y":    itm.Position.Y,
		})
	}
	return out
}

func cubeRemainingIngredientGIDs(before, after []data.Item) []int {
	beforeGIDs := make(map[data.UnitID]struct{}, len(before))
	for _, itm := range before {
		beforeGIDs[itm.UnitID] = struct{}{}
	}

	var remaining []int
	for _, itm := range after {
		if _, ok := beforeGIDs[itm.UnitID]; ok {
			remaining = append(remaining, int(itm.UnitID))
		}
	}
	return remaining
}

func canPlaceInventory(grid *[4][10]bool, x, y, w, h int) bool {
	if x < 0 || y < 0 || x+w > 10 || y+h > 4 {
		return false
	}
	for yy := y; yy < y+h; yy++ {
		for xx := x; xx < x+w; xx++ {
			if grid[yy][xx] {
				return false
			}
		}
	}
	return true
}

func EmptyCube() error {
	err := ensureCubeIsOpen()
	if err != nil {
		return err
	}

	err = ensureCubeIsEmpty()
	if err != nil {
		return err
	}

	return step.CloseAllMenus()
}

func ensureCubeIsEmpty() error {
	ctx := context.Get()
	if !ctx.Data.OpenMenus.Cube {
		return errors.New("horadric Cube window not detected")
	}

	cubeItems := ctx.Data.Inventory.ByLocation(item.LocationCube)
	if len(cubeItems) == 0 {
		return nil
	}

	ctx.Logger.Debug("Emptying the Horadric Cube")
	for _, itm := range cubeItems {
		ctx.Logger.Debug("Moving Item to the inventory", slog.String("Item", string(itm.Name)))

		screenPos := ui.GetScreenCoordsForItem(itm)

		ctx.HID.ClickWithModifier(game.LeftButton, screenPos.X, screenPos.Y, game.CtrlKey)
		utils.Sleep(700)

		itm, _ = ctx.Data.Inventory.FindByID(itm.UnitID)
		if itm.Location.LocationType == item.LocationCube {
			return fmt.Errorf("item %s could not be removed from the cube", itm.Name)
		}
	}

	ctx.HID.PressKey(win.VK_ESCAPE)
	utils.Sleep(300)

	stashInventory(true)

	return ensureCubeIsOpen()
}

func ensureCubeIsOpen() error {
	ctx := context.Get()
	ctx.Logger.Debug("Opening Horadric Cube...")

	if ctx.Data.OpenMenus.Cube {
		ctx.Logger.Debug("Horadric Cube window already open")
		return nil
	}

	maxAttempts := 3
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			ctx.Logger.Info("Retrying to open Horadric Cube", slog.Int("attempt", attempt))
			ctx.RefreshGameData()

			// Check if it opened after the refresh
			if ctx.Data.OpenMenus.Cube {
				ctx.Logger.Debug("Horadric Cube window detected after refresh")
				return nil
			}
		}

		cube, found := ctx.Data.Inventory.Find("HoradricCube", item.LocationInventory, item.LocationStash)
		if !found {
			return errors.New("horadric cube not found in inventory")
		}

		// If cube is in stash, switch to the correct tab
		if cube.Location.LocationType == item.LocationStash || cube.Location.LocationType == item.LocationSharedStash {
			ctx := context.Get()

			// Ensure stash is open
			if !ctx.Data.OpenMenus.Stash {
				bank, found := ctx.Data.Objects.FindOne(object.Bank)
				if !found {
					return fmt.Errorf("openCube: bank object not found")
				}
				err := InteractObject(bank, func() bool {
					return ctx.Data.OpenMenus.Stash
				})
				if err != nil {
					return err
				}
			}

			SwitchStashTab(cube.Location.Page + 1)
		}

		screenPos := ui.GetScreenCoordsForItem(cube)

		utils.Sleep(300)
		ctx.HID.Click(game.RightButton, screenPos.X, screenPos.Y)
		utils.Sleep(500)

		ctx.RefreshGameData()
		if ctx.Data.OpenMenus.Cube {
			ctx.Logger.Debug("Horadric Cube window detected")
			return nil
		}

		ctx.Logger.Warn("Horadric Cube window not detected after click", slog.Int("attempt", attempt))
	}

	return errors.New("horadric Cube window not detected after 3 attempts")
}
