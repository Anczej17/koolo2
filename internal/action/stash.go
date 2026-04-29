package action

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	"github.com/lxn/win"
	"local/internal/svc/internal/action/step"
	"local/internal/svc/internal/context"
	"local/internal/svc/internal/event"
	"local/internal/svc/internal/game"
	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/area"
	"local/internal/svc/internal/gamelib/data/item"
	"local/internal/svc/internal/gamelib/data/object"
	"local/internal/svc/internal/gamelib/data/stat"
	"local/internal/svc/internal/gamelib/nip"
	"local/internal/svc/internal/packet/amb"
	"local/internal/svc/internal/ui"
	"local/internal/svc/internal/utils"
)

const (
	maxGoldPerStashTab    = 2500000
	maxGoldPerSharedStash = 7500000 // Combined gold cap for all shared stash pages

	// NEW CONSTANTS FOR IMPROVED GOLD STASHING
	minInventoryGoldForStashAggressiveLeveling = 1000   // Stash if inventory gold exceeds 1k during leveling when total gold is low
	maxTotalGoldForAggressiveLevelingStash     = 150000 // Trigger aggressive stashing if total gold (inventory + stashed) is below this

	// DLC-specific stash tab constants. These use high values to avoid collision
	// with shared stash page numbers (tabs 2..6).
	StashTabGems      = 100
	StashTabMaterials = 101
	StashTabRunes     = 102
)

func Stash(forceStash bool) error {
	ctx := context.Get()
	ctx.SetLastAction("Stash")

	// Never attempt to stash outside of town — bank object doesn't exist in dungeons
	// and trying to path to it causes the bot to run around aimlessly.
	if !ctx.Data.PlayerUnit.Area.IsTown() {
		return nil
	}

	ctx.Logger.Debug("Checking for items to stash...")
	if !isStashingRequired(forceStash) {
		return nil
	}

	ctx.Logger.Info("Stashing items...")

	switch ctx.Data.PlayerUnit.Area {
	case area.KurastDocks:
		MoveToCoords(data.Position{X: 5146, Y: 5067})
	case area.LutGholein:
		MoveToCoords(data.Position{X: 5130, Y: 5086})
	}

	bank, found := ctx.Data.Objects.FindOne(object.Bank)
	if !found {
		ctx.Logger.Warn("Bank object not found in current area, skipping stash")
		return nil
	}
	if err := InteractObject(bank,
		func() bool {
			return ctx.Data.OpenMenus.Stash
		},
	); err != nil {
		return err
	}
	ctx.RefreshGameData()
	markStashOpened(ctx)
	// Clear messages like TZ change or public game spam. Prevent bot from clicking on messages
	ClearMessages()
	stashGold()
	stashInventory(forceStash)
	// Add call to dropExcessItems after stashing
	dropExcessItems()
	step.CloseAllMenus()

	return nil
}

func isStashingRequired(firstRun bool) bool {
	ctx := context.Get()
	ctx.SetLastStep("isStashingRequired")

	// Check if the character is currently leveling
	_, isLevelingChar := ctx.Char.(context.LevelingCharacter)

	for _, i := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
		if i.IsPotion() {
			continue
		}
		// Skip items that already failed to stash this game
		if ctx.CurrentGame.UnstashableItems[i.UnitID] {
			continue
		}

		stashIt, dropIt, _, _ := shouldStashIt(i, firstRun)
		if stashIt || dropIt { // Check for dropIt as well
			return true
		}
	}

	// Check if all stash tabs are full of gold
	// Personal stash: 2.5M cap, Shared stash: 7.5M combined cap
	isStashFull := ctx.Data.Inventory.StashedGold[0] >= maxGoldPerStashTab &&
		ctx.Data.Inventory.StashedGold[1] >= maxGoldPerSharedStash

	// Calculate total gold (inventory + stashed) for the new aggressive stashing rule
	// StashedGold[0] = personal, StashedGold[1] = combined shared gold
	totalGold := ctx.Data.Inventory.Gold + ctx.Data.Inventory.StashedGold[0] + ctx.Data.Inventory.StashedGold[1]

	// 1. AGGRESSIVE STASHING for leveling characters with LOW TOTAL GOLD
	if isLevelingChar && totalGold < maxTotalGoldForAggressiveLevelingStash && ctx.Data.Inventory.Gold >= minInventoryGoldForStashAggressiveLeveling && !isStashFull {
		ctx.Logger.Debug(fmt.Sprintf("Leveling char with LOW TOTAL GOLD (%.2fk < %.2fk) and INV GOLD (%.2fk) above aggressive threshold (%.2fk). Stashing gold.",
			float64(totalGold)/1000, float64(maxTotalGoldForAggressiveLevelingStash)/1000,
			float64(ctx.Data.Inventory.Gold)/1000, float64(minInventoryGoldForStashAggressiveLeveling)/1000))
		return true
	}

	// 2. STANDARD STASHING for all other cases (non-leveling, or leveling with sufficient total gold)
	if ctx.Data.Inventory.Gold > ctx.Data.PlayerUnit.MaxGold()/3 && !isStashFull {
		ctx.Logger.Debug(fmt.Sprintf("Inventory gold (%.2fk) is above standard threshold (%.2fk). Stashing gold.",
			float64(ctx.Data.Inventory.Gold)/1000, float64(ctx.Data.PlayerUnit.MaxGold())/3/1000))
		return true
	}

	return false
}

func stashGold() {
	ctx := context.Get()
	ctx.SetLastAction("stashGold")

	if ctx.Data.Inventory.Gold == 0 {
		return
	}

	ctx.Logger.Info("Stashing gold...", slog.Int("gold", ctx.Data.Inventory.Gold))

	// Try personal stash first (tab 1, max 2.5M)
	ctx.RefreshGameData()
	if ctx.Data.Inventory.Gold > 0 && ctx.Data.Inventory.StashedGold[0] < maxGoldPerStashTab {
		SwitchStashTab(1)
		clickStashGoldBtn()
		utils.PingSleep(utils.Critical, 1000)
		ctx.RefreshGameData()
		if ctx.Data.Inventory.Gold == 0 {
			ctx.Logger.Info("All inventory gold stashed.")
			return
		}
	}

	// Try shared stash (tab 2, combined max 7.5M)
	// Gold is shared across all pages, so depositing on any page works
	if ctx.Data.Inventory.Gold > 0 && ctx.Data.Inventory.StashedGold[1] < maxGoldPerSharedStash {
		SwitchStashTab(2)
		clickStashGoldBtn()
		utils.PingSleep(utils.Critical, 1000)
		ctx.RefreshGameData()
		if ctx.Data.Inventory.Gold == 0 {
			ctx.Logger.Info("All inventory gold stashed.")
			return
		}
	}

	ctx.Logger.Info("All stash tabs are full of gold :D")
}

func stashInventory(firstRun bool) {
	ctx := context.Get()
	ctx.SetLastAction("stashInventory")

	// Determine starting tab based on configuration
	startTab := 1 // Personal stash by default (tab 1)
	if ctx.CharacterCfg.Character.StashToShared {
		startTab = 2 // Start with first shared stash tab if configured (tabs 2-4 are shared)
	}

	currentTab := startTab
	SwitchStashTab(currentTab)

	// Make a copy of inventory items to avoid issues if the slice changes during iteration
	itemsToProcess := make([]data.Item, 0)
	for _, i := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
		if i.IsPotion() {
			continue
		}

		itemsToProcess = append(itemsToProcess, i)
	}

	for _, i := range itemsToProcess {
		stashIt, dropIt, matchedRule, ruleFile := shouldStashIt(i, firstRun)

		if dropIt {
			ctx.Logger.Info(fmt.Sprintf("Dropping item %s [%s] due to MaxQuantity rule.", i.Desc().Name, i.Quality.ToString()))
			blacklistItem(i)
			utils.PingSleep(utils.Medium, 500) // Medium operation: Prepare for item drop
			DropItem(i)
			utils.PingSleep(utils.Medium, 500) // Medium operation: Wait for drop to complete
			step.CloseAllMenus()
			continue
		}

		if !stashIt {
			continue
		}

		// Skip items already marked unstashable this game (all tabs were full on a prior attempt)
		if ctx.CurrentGame.UnstashableItems[i.UnitID] {
			continue
		}

		stashed := stashItemAcrossTabs(i, matchedRule, ruleFile, firstRun)
		if !stashed {
			ctx.CurrentGame.UnstashableItems[i.UnitID] = true
			ctx.Logger.Warn(fmt.Sprintf("Item %s [%s] could not be stashed into any tab, skipping for rest of game.", i.Desc().Name, i.Quality.ToString()))
		}
	}
	step.CloseAllMenus()
}

// stashItemAcrossTabs attempts to stash the given item across available tabs, applying the same logic
// used by the main stash routine. It returns true if the item was stashed successfully.
func stashItemAcrossTabs(i data.Item, matchedRule string, ruleFile string, firstRun bool) bool {
	ctx := context.Get()
	displayName := formatItemName(i)

	startTab := 1
	if ctx.CharacterCfg.Character.StashToShared {
		startTab = 2
	}

	targetStartTab := startTab
	if (i.Name == "grandcharm" || i.Name == "smallcharm" || i.Name == "largecharm") && i.Quality == item.QualityUnique {
		targetStartTab = 2
	}

	itemStashed := false
	// Tab 1=Personal, Tabs 2..N=Shared stash pages.
	// Non-DLC: 3 shared pages (tabs 2-4). DLC: 5 shared pages (tabs 2-6).
	// Use SharedStashPages from memory to determine actual count.
	sharedPages := ctx.Data.Inventory.SharedStashPages
	if sharedPages == 0 {
		// Fallback: assume 3 pages if not detected
		sharedPages = 3
	}
	maxTab := 1 + sharedPages // personal (1) + all shared pages

	for tabAttempt := targetStartTab; tabAttempt <= maxTab; tabAttempt++ {
		SwitchStashTab(tabAttempt)

		if stashItemAction(i, matchedRule, ruleFile, firstRun) {
			itemStashed = true
			r, res := ctx.CharacterCfg.Runtime.Rules.EvaluateAll(i)

			if res != nip.RuleResultFullMatch && firstRun {
				ctx.Logger.Info(
					fmt.Sprintf("Item %s [%s] stashed to tab %d because it was found in the inventory during the first run.", displayName, i.Quality.ToString(), tabAttempt),
				)
			} else {
				ctx.Logger.Info(
					fmt.Sprintf("Item %s [%s] stashed to tab %d", displayName, i.Quality.ToString(), tabAttempt),
					slog.String("nipFile", fmt.Sprintf("%s:%d", r.Filename, r.LineNumber)),
					slog.String("rawRule", r.RawLine),
				)
			}
			break
		}
		ctx.Logger.Debug(fmt.Sprintf("Item %s could not be stashed on tab %d. Trying next.", displayName, tabAttempt))
	}

	if !itemStashed && targetStartTab == 2 {
		ctx.Logger.Debug(fmt.Sprintf("All shared stash tabs full for %s, trying personal stash as fallback", displayName))
		SwitchStashTab(1)
		if stashItemAction(i, matchedRule, ruleFile, firstRun) {
			itemStashed = true
			ctx.Logger.Info(fmt.Sprintf("Item %s [%s] stashed to personal stash (tab 1) as fallback", displayName, i.Quality.ToString()))
		}
	}

	return itemStashed
}

// shouldStashIt now returns stashIt, dropIt, matchedRule, ruleFile
func shouldStashIt(i data.Item, firstRun bool) (bool, bool, string, string) {
	ctx := context.Get()
	ctx.SetLastStep("shouldStashIt")

	// Don't stash items in protected slots (highest priority exclusion)
	if ctx.CharacterCfg.Inventory.InventoryLock[i.Position.Y][i.Position.X] == 0 {
		return false, false, "", ""
	}

	// These items should NEVER be stashed, regardless of quest status, pickit rules, or first run.
	if i.Name == "horadricstaff" { // This is the simplest way given your logs
		return false, false, "", "" // Explicitly do NOT stash the Horadric Staff
	}

	if i.Name == "TomeOfTownPortal" || i.Name == "TomeOfIdentify" || i.Name == "Key" || i.Name == "WirtsLeg" {
		return false, false, "", ""
	}

	if _, isLevelingChar := ctx.Char.(context.LevelingCharacter); (isLevelingChar && i.IsFromQuest() && i.Name != "HoradricCube") || strings.EqualFold(string(i.Name), "horadricstaff") {
		return false, false, "", ""
	}

	if firstRun {
		return true, false, "FirstRun", ""
	}

	// Stash items that are part of a recipe which are not covered by the NIP rules
	if shouldKeepRecipeItem(i) {
		return true, false, "Item is part of a enabled recipe", ""
	}

	// Location/position checks
	if i.Position.Y >= len(ctx.CharacterCfg.Inventory.InventoryLock) || i.Position.X >= len(ctx.CharacterCfg.Inventory.InventoryLock[0]) {
		return false, false, "", ""
	}

	if i.Location.LocationType == item.LocationInventory && ctx.CharacterCfg.Inventory.InventoryLock[i.Position.Y][i.Position.X] == 0 || i.IsPotion() {
		return false, false, "", ""
	}

	// NOW, evaluate pickit rules.
	tierRule, mercTierRule := ctx.CharacterCfg.Runtime.Rules.EvaluateTiers(i, ctx.CharacterCfg.Runtime.TierRules)
	if tierRule.Tier() > 0.0 && IsBetterThanEquipped(i, false, PlayerScore) {
		return true, false, tierRule.RawLine, tierRule.Filename + ":" + strconv.Itoa(tierRule.LineNumber)
	}

	if mercTierRule.Tier() > 0.0 && IsBetterThanEquipped(i, true, MercScore) {
		return true, false, mercTierRule.RawLine, mercTierRule.Filename + ":" + strconv.Itoa(mercTierRule.LineNumber)
	}

	// NOW, evaluate pickit rules.
	rule, res := ctx.CharacterCfg.Runtime.Rules.EvaluateAllIgnoreTiers(i)

	if res == nip.RuleResultFullMatch {
		if doesExceedQuantity(rule) {
			// If it matches a rule but exceeds quantity, we want to drop it, not stash.
			return false, true, rule.RawLine, rule.Filename + ":" + strconv.Itoa(rule.LineNumber)
		} else {
			// If it matches a rule and quantity is fine, stash it.
			return true, false, rule.RawLine, rule.Filename + ":" + strconv.Itoa(rule.LineNumber)
		}
	}

	if i.IsRuneword {
		return true, false, "Runeword", ""
	}

	return false, false, "", "" // Default if no other rule matches
}

// shouldKeepRecipeItem decides whether the bot should stash a low-quality item that is part of an enabled cube recipe.
// It now supports keeping multiple jewels for crafting via maxJewelsKept.
// shouldKeepRecipeItem decides whether the bot should stash a low-quality item that is part of an enabled cube recipe.
// It now supports keeping multiple jewels for crafting via JewelsToKeep.
// shouldKeepRecipeItem decides whether the bot should stash a low-quality item that is part of an enabled cube recipe.
// It now supports keeping multiple jewels (of any quality) for crafting via JewelsToKeep.
func shouldKeepRecipeItem(i data.Item) bool {
	ctx := context.Get()
	ctx.SetLastStep("shouldKeepRecipeItem")

	// For non-jewel items: only normal/magic quality can be part of recipes
	// For jewels: any quality (magic, rare, unique, etc.) can be used in crafting recipes
	if string(i.Name) != "Jewel" && i.Quality > item.QualityMagic {
		return false
	}

	itemInStashNotMatchingRule := false
	jewelCount := 0

	// Count ALL non-NIP jewels in stash (regardless of quality: magic, rare, unique, etc.)
	for _, it := range ctx.Data.Inventory.ByLocation(item.LocationStash, item.LocationSharedStash) {
		if string(it.Name) == "Jewel" {
			if _, res := ctx.CharacterCfg.Runtime.Rules.EvaluateAll(it); res != nip.RuleResultFullMatch {
				jewelCount++
			}
		}
		// For OTHER recipe items (not jewels): match on base name and require magic quality
		// so only another magic item of the same base blocks us
		if string(it.Name) != "Jewel" && strings.EqualFold(string(it.Name), string(i.Name)) && it.Quality == item.QualityMagic {
			_, res := ctx.CharacterCfg.Runtime.Rules.EvaluateAll(it)
			if res != nip.RuleResultFullMatch {
				itemInStashNotMatchingRule = true
			}
		}
	}

	// CRITICAL: Also count ALL non-NIP jewels currently in inventory (any quality, excluding the one we're evaluating)
	// because they will also be stashed in the same run
	for _, it := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
		if string(it.Name) == "Jewel" && it.UnitID != i.UnitID {
			if _, res := ctx.CharacterCfg.Runtime.Rules.EvaluateAll(it); res != nip.RuleResultFullMatch {
				jewelCount++
			}
		}
	}

	recipeMatch := false

	// Check if the item is part of an enabled recipe
	for _, recipe := range Recipes {
		if slices.Contains(recipe.Items, string(i.Name)) &&
			slices.Contains(ctx.CharacterCfg.CubeRecipes.EnabledRecipes, recipe.Name) {
			recipeMatch = true
			break
		}
	}

	// Special-case: For jewels of ANY quality used in crafting recipes, stash up to JewelsToKeep copies.
	if string(i.Name) == "Jewel" {
		if recipeMatch && jewelCount < ctx.CharacterCfg.CubeRecipes.JewelsToKeep {
			ctx.Logger.Debug(fmt.Sprintf("Keeping jewel (quality: %s) for recipe - current count: %d, limit: %d",
				i.Quality.ToString(), jewelCount, ctx.CharacterCfg.CubeRecipes.JewelsToKeep))
			return true
		}
		ctx.Logger.Debug(fmt.Sprintf("NOT keeping jewel (quality: %s) - count: %d, limit: %d, recipeMatch: %v",
			i.Quality.ToString(), jewelCount, ctx.CharacterCfg.CubeRecipes.JewelsToKeep, recipeMatch))
		return false
	}

	// For all other recipe items, keep one copy in the stash if none exists
	if recipeMatch && !itemInStashNotMatchingRule {
		return true
	}

	return false
}

// findFreeStashSlot walks the currently visible stash tab and returns the
// top-left (col, row) of the first rectangle large enough to fit `it`.
// Grid is 10 wide × 10 tall (personal stash + DLC shared stash pages share
// the same dimensions). Returns ok=false if nothing fits.
func findFreeStashSlot(ctx *context.Status, it data.Item) (col, row uint8, ok bool) {
	const gridW, gridH = 10, 10
	var occupied [gridH][gridW]bool

	// Mark existing stashed items as occupied on the active tab/page.
	currentTab := ctx.CurrentGame.CurrentStashTab
	for _, si := range ctx.Data.Inventory.ByLocation(item.LocationStash, item.LocationSharedStash, item.LocationGemsTab, item.LocationMaterialsTab, item.LocationRunesTab) {
		// Only consider items on the current visible page/tab.
		switch si.Location.LocationType {
		case item.LocationStash:
			if currentTab != 1 {
				continue
			}
		case item.LocationSharedStash:
			if si.Location.Page+1 != currentTab {
				continue
			}
		case item.LocationGemsTab:
			if currentTab != StashTabGems {
				continue
			}
		case item.LocationMaterialsTab:
			if currentTab != StashTabMaterials {
				continue
			}
		case item.LocationRunesTab:
			if currentTab != StashTabRunes {
				continue
			}
		default:
			continue
		}
		d := si.Desc()
		for dy := 0; dy < d.InventoryHeight; dy++ {
			for dx := 0; dx < d.InventoryWidth; dx++ {
				y, x := si.Position.Y+dy, si.Position.X+dx
				if y >= 0 && y < gridH && x >= 0 && x < gridW {
					occupied[y][x] = true
				}
			}
		}
	}

	w := it.Desc().InventoryWidth
	h := it.Desc().InventoryHeight
	if w <= 0 || h <= 0 {
		return 0, 0, false
	}
	for y := 0; y+h <= gridH; y++ {
		for x := 0; x+w <= gridW; x++ {
			fits := true
			for dy := 0; dy < h && fits; dy++ {
				for dx := 0; dx < w && fits; dx++ {
					if occupied[y+dy][x+dx] {
						fits = false
					}
				}
			}
			if fits {
				return uint8(x), uint8(y), true
			}
		}
	}
	return 0, 0, false
}

func stashItemAction(i data.Item, rule string, ruleFile string, skipLogging bool) bool {
	ctx := context.Get()
	ctx.SetLastAction("stashItemAction")
	displayName := formatItemName(i)

	screenshot := ctx.GameReader.Screenshot() // Take screenshot *before* attempting stash
	utils.PingSleep(utils.Medium, 150)        // Medium operation: Wait for screenshot

	if fullPacketHIDDisabled(ctx) {
		if !stashItemActionPacket(ctx, i, displayName) {
			return false
		}
	} else {
		screenPos := ui.GetScreenCoordsForItem(i)
		ctx.HID.MovePointer(screenPos.X, screenPos.Y)
		utils.PingSleep(utils.Medium, 170)       // Medium operation: Move pointer to item
		screenshot = ctx.GameReader.Screenshot() // Take screenshot *before* attempting stash
		utils.PingSleep(utils.Medium, 150)       // Medium operation: Wait for screenshot
		// Ctrl+click via HID.ClickWithModifier — but HID.Click itself now
		// dispatches via IN-PROCESS SendMessageW (APC into D2R's own thread).
		// So "HID" here means Windows message routing, not SendInput — WndProc
		// fires from D2R's legitimate stack and routes Ctrl+click to the
		// stash-move dispatcher with correct session state. No cross-process
		// OS message queue, no bot-detectable HID footprint.
		ctx.HID.ClickWithModifier(game.LeftButton, screenPos.X, screenPos.Y, game.CtrlKey)
		utils.PingSleep(utils.Medium, 500)

		// Verify if the item is no longer in inventory
		ctx.RefreshGameData() // Crucial: Refresh data to see if item moved
		for _, it := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
			if it.UnitID == i.UnitID {
				ctx.Logger.Debug(fmt.Sprintf("Failed to stash item %s (UnitID: %d), still in inventory.", i.Name, i.UnitID))
				return false // Item is still in inventory, stash failed
			}
		}
	}

	dropLocation := "unknown"

	// log the contents of picked up items
	ctx.Logger.Debug(fmt.Sprintf("Checking PickedUpItems for %s (UnitID: %d)", displayName, i.UnitID)) // Changed to Debug as this is internal state
	if _, found := ctx.CurrentGame.PickedUpItems[int(i.UnitID)]; found {
		areaId := ctx.CurrentGame.PickedUpItems[int(i.UnitID)]
		dropLocation = area.ID(areaId).Area().Name // Corrected to use areaId variable

		if slices.Contains(ctx.Data.TerrorZones, area.ID(areaId)) {
			dropLocation += " (terrorized)"
		}
	}

	// Don't log items that we already have in inventory during first run or that we don't want to notify about (gems, low runes .. etc)
	if !skipLogging && shouldNotifyAboutStashing(i) && ruleFile != "" {
		dropItem := i
		if dropItem.IsRuneword && dropItem.IdentifiedName == "" {
			dropItem.IdentifiedName = displayName
		}
		event.Send(event.ItemStashed(
			event.WithScreenshot(ctx.Name, fmt.Sprintf("Item %s [%d] stashed", displayName, i.Quality), screenshot),
			data.Drop{Item: dropItem, Rule: rule, RuleFile: ruleFile, DropLocation: dropLocation},
		))
	}

	return true // Item successfully stashed
}

func stashItemActionPacket(ctx *context.Status, i data.Item, displayName string) bool {
	if ctx.PacketSender == nil {
		return false
	}

	tab := ctx.CurrentGame.CurrentStashTab
	if tab == 0 {
		tab = 1
		ctx.CurrentGame.CurrentStashTab = 1
	}
	if tab >= StashTabGems {
		ctx.Logger.Warn("Packet stash move for DLC stash tabs is not wired; refusing HID fallback",
			slog.String("item", displayName),
			slog.Int("tab", tab))
		return false
	}

	col, row, ok := findFreeStashSlot(ctx, i)
	if !ok {
		ctx.Logger.Debug("No free packet stash slot on current tab",
			slog.String("item", displayName),
			slog.Int("tab", tab))
		return false
	}

	var err error
	if tab == 1 {
		ctx.Logger.Debug("Stashing via AMB cursor packet move 0x19 -> 0x18",
			slog.String("item", displayName),
			slog.Int("itemGID", int(i.UnitID)),
			slog.Int("tab", tab),
			slog.Int("toX", int(col)),
			slog.Int("toY", int(row)))
		return stashItemActionAMBCursorMove(ctx, i, displayName, col, row, tab)
	} else {
		ownerID, ok := sharedStashOwnerID(ctx, tab)
		if !ok {
			ctx.Logger.Warn("Packet shared stash move missing phantom tab unit ID; refusing HID fallback",
				slog.String("item", displayName),
				slog.Int("tab", tab),
				slog.Int("stashTabUnitIDs", len(ctx.Data.Inventory.StashTabUnitIDs)))
			return false
		}
		ctx.Logger.Debug("Stashing via AMB 0x55 PutItemToSharedStash",
			slog.String("item", displayName),
			slog.Int("itemGID", int(i.UnitID)),
			slog.Int("tab", tab),
			slog.Uint64("stashTabOwnerID", uint64(ownerID)),
			slog.Int("toX", int(col)),
			slog.Int("toY", int(row)))
		err = ctx.PacketSender.PutItemToSharedStash(i, ownerID, uint16(col), uint16(row))
	}
	if err != nil {
		ctx.Logger.Warn("Packet stash move failed; refusing HID fallback",
			slog.String("item", displayName),
			slog.Int("tab", tab),
			slog.Any("error", err))
		return false
	}

	utils.PingSleep(utils.Medium, 500)
	ctx.RefreshGameData()
	for _, it := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
		if it.UnitID == i.UnitID {
			ctx.Logger.Debug(fmt.Sprintf("Failed to stash item %s (UnitID: %d), still in inventory.", i.Name, i.UnitID))
			return false
		}
	}
	return true
}

func sharedStashOwnerID(ctx *context.Status, tab int) (uint32, bool) {
	if ctx == nil || tab < 2 {
		return 0, false
	}
	ownerIndex := tab - 1
	if ownerIndex <= 0 || ownerIndex >= len(ctx.Data.Inventory.StashTabUnitIDs) {
		return 0, false
	}
	ownerID := ctx.Data.Inventory.StashTabUnitIDs[ownerIndex]
	if ownerID == 0 {
		return 0, false
	}
	return uint32(ownerID), true
}

func stashItemActionAMBCursorMove(ctx *context.Status, i data.Item, displayName string, col, row uint8, tab int) bool {
	if ctx == nil || ctx.PacketSender == nil {
		return false
	}

	ctx.Logger.Debug("Stashing via AMB cursor move 0x19 -> 0x18",
		slog.String("item", displayName),
		slog.Int("itemGID", int(i.UnitID)),
		slog.Int("tab", tab),
		slog.Int("fromX", i.Position.X),
		slog.Int("fromY", i.Position.Y),
		slog.Int("toX", int(col)),
		slog.Int("toY", int(row)))

	if err := ctx.PacketSender.PickItemFromContainer(i.UnitID, i.Position.X, i.Position.Y, amb.ContainerInventory); err != nil {
		ctx.Logger.Warn("AMB cursor stash pick failed",
			slog.String("item", displayName),
			slog.Any("error", err))
		return false
	}
	utils.PingSleep(utils.Light, 150)

	if err := ctx.PacketSender.PutItemToInventory(uint32(i.UnitID), uint32(col), uint32(row), uint32(amb.ContainerStash)); err != nil {
		ctx.Logger.Warn("AMB cursor stash put failed",
			slog.String("item", displayName),
			slog.Any("error", err))
		return false
	}

	utils.PingSleep(utils.Medium, 500)
	ctx.RefreshGameData()
	for _, it := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
		if it.UnitID == i.UnitID {
			ctx.Logger.Debug("AMB cursor stash move left item in inventory",
				slog.String("item", displayName),
				slog.Int("itemGID", int(i.UnitID)))
			return false
		}
	}
	return true
}

func stashItemActionUIPacketMove(ctx *context.Status, i data.Item, displayName string, col, row uint8, tab int) bool {
	if ctx == nil || ctx.PacketSender == nil {
		return false
	}

	ctx.Logger.Warn("AMB stash packet had no effect; trying packet 0x19 UI move",
		slog.String("item", displayName),
		slog.Int("itemGID", int(i.UnitID)),
		slog.Int("tab", tab),
		slog.Int("toX", int(col)),
		slog.Int("toY", int(row)))
	if err := ctx.PacketSender.ItemToStash(i.UnitID, col, row); err != nil {
		ctx.Logger.Warn("Packet 0x19 UI stash move failed",
			slog.String("item", displayName),
			slog.Any("error", err))
		return false
	}

	utils.PingSleep(utils.Medium, 500)
	ctx.RefreshGameData()
	for _, it := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
		if it.UnitID == i.UnitID {
			ctx.Logger.Debug("Packet 0x19 UI stash move left item in inventory",
				slog.String("item", displayName),
				slog.Int("itemGID", int(i.UnitID)))
			return false
		}
	}
	return true
}

func stashItemActionNativeQueuedCtrlClick(ctx *context.Status, i data.Item, displayName string) bool {
	if ctx == nil || ctx.PacketSender == nil || ctx.MemoryInjector == nil || ctx.GameReader == nil {
		return false
	}

	screenPos := ui.GetScreenCoordsForItem(i)
	ctx.Logger.Warn("AMB stash packet had no effect; trying D2R real-click ctrl handler",
		slog.String("item", displayName),
		slog.Int("itemGID", int(i.UnitID)),
		slog.Int("clientX", screenPos.X),
		slog.Int("clientY", screenPos.Y))

	if err := ctx.MemoryInjector.OverrideGetKeyState(byte(game.CtrlKey)); err != nil {
		ctx.Logger.Warn("Native stash ctrl-click failed to arm Ctrl key state",
			slog.String("item", displayName),
			slog.Any("error", err))
		return false
	}
	defer func() {
		if err := ctx.MemoryInjector.RestoreGetKeyState(); err != nil {
			ctx.Logger.Warn("Native stash ctrl-click failed to restore key state",
				slog.String("item", displayName),
				slog.Any("error", err))
		}
	}()

	_ = ctx.MemoryInjector.CursorPos(ctx.GameReader.WindowLeftX+screenPos.X, ctx.GameReader.WindowTopY+screenPos.Y)
	if err := ctx.PacketSender.ClickAt(int32(screenPos.X), int32(screenPos.Y), game.MouseLeft); err != nil {
		ctx.Logger.Warn("Native stash ctrl-click failed",
			slog.String("item", displayName),
			slog.Any("error", err))
		return false
	}

	utils.PingSleep(utils.Medium, 500)
	ctx.RefreshGameData()
	for _, it := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
		if it.UnitID == i.UnitID {
			ctx.Logger.Debug("Native stash ctrl-click left item in inventory",
				slog.String("item", displayName),
				slog.Int("itemGID", int(i.UnitID)))
			return false
		}
	}
	return true
}

func formatItemName(i data.Item) string {
	if i.IsRuneword && i.RunewordName != item.RunewordNone {
		if rwName := string(item.Name(i.RunewordName)); rwName != "" {
			return rwName
		}
	}

	if i.IdentifiedName != "" {
		return i.IdentifiedName
	}

	if desc := i.Desc().Name; desc != "" {
		return desc
	}

	return string(i.Name)
}

// dropExcessItems iterates through inventory and drops items marked for dropping
func dropExcessItems() {
	ctx := context.Get()
	ctx.SetLastAction("dropExcessItems")

	itemsToDrop := make([]data.Item, 0)
	for _, i := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
		if i.IsPotion() {
			continue
		}

		_, dropIt, _, _ := shouldStashIt(i, false) // Re-evaluate if it should be dropped (not firstRun)
		if dropIt {
			itemsToDrop = append(itemsToDrop, i)
		}
	}

	if len(itemsToDrop) > 0 {
		ctx.Logger.Info(fmt.Sprintf("Dropping %d excess items from inventory.", len(itemsToDrop)))
		// Ensure we are not in a menu before dropping
		step.CloseAllMenus()

		for _, i := range itemsToDrop {
			DropItem(i)
		}
	}
}

func blacklistItem(i data.Item) {
	ctx := context.Get()
	ctx.CurrentGame.BlacklistedItems = append(ctx.CurrentGame.BlacklistedItems, i)
	ctx.Logger.Info(fmt.Sprintf("Blacklisted item %s (UnitID: %d) to prevent immediate re-pickup.", i.Name, i.UnitID))
}

// DropItem handles moving an item from inventory to the ground
func DropItem(i data.Item) {
	ctx := context.Get()
	ctx.SetLastAction("DropItem")
	if fullPacketHIDDisabled(ctx) {
		if ctx.PacketSender == nil {
			ctx.Logger.Warn("DropItem requested in full-packet mode without PacketSender; refusing HID fallback",
				slog.String("item", formatItemName(i)),
				slog.Int("itemGID", int(i.UnitID)))
			return
		}
		ctx.Logger.Debug("Dropping item via AMB 0x5C QuickItemDrop",
			slog.String("item", formatItemName(i)),
			slog.Int("itemGID", int(i.UnitID)))
		if err := ctx.PacketSender.QuickItemDropAMB(i); err != nil {
			ctx.Logger.Warn("Packet item drop failed; refusing HID fallback",
				slog.String("item", formatItemName(i)),
				slog.Int("itemGID", int(i.UnitID)),
				slog.Any("error", err))
			return
		}
		utils.PingSleep(utils.Medium, 500)
		ctx.RefreshGameData()
		for _, it := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
			if it.UnitID == i.UnitID {
				ctx.Logger.Warn(fmt.Sprintf("Failed to drop item %s (UnitID: %d), still in inventory.", i.Name, i.UnitID))
				return
			}
		}
		ctx.Logger.Debug(fmt.Sprintf("Successfully dropped item %s (UnitID: %d).", i.Name, i.UnitID))
		return
	}

	utils.PingSleep(utils.Medium, 170) // Medium operation: Prepare for drop
	step.CloseAllMenus()
	utils.PingSleep(utils.Medium, 170) // Medium operation: Wait for menus to close
	ctx.HID.PressKeyBinding(ctx.Data.KeyBindings.Inventory)
	utils.PingSleep(utils.Medium, 170) // Medium operation: Wait for inventory to open
	screenPos := ui.GetScreenCoordsForItem(i)
	ctx.HID.MovePointer(screenPos.X, screenPos.Y)
	utils.PingSleep(utils.Medium, 170) // Medium operation: Position pointer on item
	// HID.ClickWithModifier — HID.Click routes via in-process SendMessageW.
	ctx.HID.ClickWithModifier(game.LeftButton, screenPos.X, screenPos.Y, game.CtrlKey)
	utils.PingSleep(utils.Medium, 500) // Medium operation: Wait for item to drop
	step.CloseAllMenus()
	utils.PingSleep(utils.Medium, 170) // Medium operation: Clean up UI
	ctx.RefreshGameData()
	for _, it := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
		if it.UnitID == i.UnitID {
			ctx.Logger.Warn(fmt.Sprintf("Failed to drop item %s (UnitID: %d), still in inventory. Inventory might be full or area restricted.", i.Name, i.UnitID))
			return
		}
	}
	ctx.Logger.Debug(fmt.Sprintf("Successfully dropped item %s (UnitID: %d).", i.Name, i.UnitID))

	step.CloseAllMenus()
}

func shouldNotifyAboutStashing(i data.Item) bool {
	ctx := context.Get()

	if ctx.IsBossEquipmentActive {
		return false
	}

	ctx.Logger.Debug(fmt.Sprintf("Checking if we should notify about stashing %s %v", i.Name, i.Desc()))
	// Don't notify about gems
	if strings.Contains(i.Desc().Type, "gem") {
		return false
	}

	// Skip low runes (below lem)
	lowRunes := []string{"elrune", "eldrune", "tirrune", "nefrune", "ethrune", "ithrune", "talrune", "ralrune", "ortrune", "thulrune", "amnrune", "solrune", "shaelrune", "dolrune", "helrune", "iorune", "lumrune", "korune", "falrune"}
	if i.Desc().Type == item.TypeRune {
		itemName := strings.ToLower(string(i.Name))
		for _, runeName := range lowRunes {
			if itemName == runeName {
				if !(i.Name == "tirrune" || i.Name == "talrune" || i.Name == "ralrune" || i.Name == "ortrune" || i.Name == "thulrune" || i.Name == "amnrune" || i.Name == "solrune" || i.Name == "lumrune" || i.Name == "nefrune") { // Exclude specific runes from low rune skip logic if they are part of a recipe you want to keep
					return false
				}
			}
		}
	}

	return true
}

func fullPacketHIDDisabled(ctx *context.Status) bool {
	return ctx != nil && ctx.PacketSender != nil && ctx.HID != nil && ctx.HID.IsDisabled()
}

func markStashOpened(ctx *context.Status) {
	if ctx == nil || ctx.CurrentGame == nil {
		return
	}
	if !ctx.CurrentGame.HasOpenedStash || ctx.CurrentGame.CurrentStashTab == 0 {
		ctx.CurrentGame.CurrentStashTab = 1
	}
	ctx.CurrentGame.HasOpenedStash = true
}

func clickStashGoldBtn() {
	ctx := context.Get()
	ctx.SetLastStep("clickStashGoldBtn")

	utils.PingSleep(utils.Medium, 170) // Medium operation: Prepare for gold button click

	if ctx.PacketSender != nil {
		goldStat, _ := ctx.Data.PlayerUnit.FindStat(stat.Gold, 0)
		invGold := uint32(goldStat.Value)
		if invGold > 0 {
			// AMB 0x27 DepositGoldToStash — transfers the full inventory
			// gold balance into the player's personal stash tab.
			if err := ctx.PacketSender.DepositGoldToStash(ctx.Data.PlayerUnit.ID, 0, invGold, invGold); err == nil {
				utils.PingSleep(utils.Critical, 500)
				return
			} else {
				ctx.Logger.Warn("stash gold deposit packet failed, skipping HID fallback", slog.Any("error", err), slog.Uint64("inventoryGold", uint64(invGold)))
			}
		}
		return
	}

	// HID.Click — in-process SendMessageW for gold deposit dialog.
	if ctx.GameReader.LegacyGraphics() {
		ctx.HID.Click(game.LeftButton, ui.StashGoldBtnXClassic, ui.StashGoldBtnYClassic)
		utils.PingSleep(utils.Critical, 1000)
		ctx.HID.Click(game.LeftButton, ui.StashGoldBtnConfirmXClassic, ui.StashGoldBtnConfirmYClassic)
	} else {
		ctx.HID.Click(game.LeftButton, ui.StashGoldBtnX, ui.StashGoldBtnY)
		utils.PingSleep(utils.Critical, 1000)
		ctx.HID.Click(game.LeftButton, ui.StashGoldBtnConfirmX, ui.StashGoldBtnConfirmY)
	}
}

// SwitchStashTab switches to the specified stash tab.
// Tab mapping:
//
//	Tab 1          = Personal stash
//	Tab 2..N       = Shared stash pages (non-DLC: 2-4, DLC: 2-6)
//	StashTabGems   = DLC Gems tab (100)
//	StashTabMaterials = DLC Materials tab (101)
//	StashTabRunes  = DLC Runes tab (102)
func SwitchStashTab(tab int) {
	ctx := context.Get()
	if tab == ctx.CurrentGame.CurrentStashTab {
		return // Already on this tab
	}

	if fullPacketHIDDisabled(ctx) {
		ctx.Logger.Debug("SwitchStashTab: packet mode virtual tab switch",
			slog.Int("from", ctx.CurrentGame.CurrentStashTab),
			slog.Int("to", tab))
		ctx.CurrentGame.CurrentStashTab = tab
		return
	}

	// Ensure any chat messages that could prevent clicking on the tab are cleared
	ClearMessages()
	utils.Sleep(200)

	ctx.SetLastStep("switchTab")

	if ctx.GameReader.LegacyGraphics() {
		switchStashTabLegacy(ctx, tab)
	} else {
		switchStashTabHD(ctx, tab)
	}
	ctx.CurrentGame.CurrentStashTab = tab
}

func switchStashTabHD(ctx *context.Status, tab int) {
	// DLC-specific tabs: click directly, no page navigation
	switch tab {
	case StashTabGems:
		ctx.HID.Click(game.LeftButton, ui.DLCGemsTabX, ui.DLCGemsTabY)
		utils.PingSleep(utils.Medium, 500)
		return
	case StashTabMaterials:
		ctx.HID.Click(game.LeftButton, ui.DLCMaterialsTabX, ui.DLCMaterialsTabY)
		utils.PingSleep(utils.Medium, 500)
		return
	case StashTabRunes:
		ctx.HID.Click(game.LeftButton, ui.DLCRunesTabX, ui.DLCRunesTabY)
		utils.PingSleep(utils.Medium, 500)
		return
	}

	prev := ctx.CurrentGame.CurrentStashTab

	// If switching between Personal (1) and Shared (2+), or from a DLC tab,
	// we need to click the UI tab button.
	needTabClick := (prev < 2 && tab >= 2) || (prev >= 2 && tab < 2) || prev == 0 || prev >= StashTabGems

	if tab == 1 || needTabClick {
		uiTab := 1
		if tab >= 2 {
			uiTab = 2
		}
		x := ui.SwitchStashTabBtnX + ui.SwitchStashTabBtnTabSize*uiTab - ui.SwitchStashTabBtnTabSize/2
		ctx.HID.Click(game.LeftButton, x, ui.SwitchStashTabBtnY)
		utils.PingSleep(utils.Medium, 500)
	}

	// Navigate shared stash pages
	if tab >= 2 {
		// If coming from a known shared page, navigate incrementally
		if prev >= 2 && prev < StashTabGems && !needTabClick {
			delta := tab - prev
			if delta > 0 {
				for i := 0; i < delta; i++ {
					ctx.HID.Click(game.LeftButton, ui.SharedStashNextPageX, ui.SharedStashNextPageY)
					utils.PingSleep(utils.Medium, 250)
				}
			} else if delta < 0 {
				for i := 0; i < -delta; i++ {
					ctx.HID.Click(game.LeftButton, ui.SharedStashPrevPageX, ui.SharedStashPrevPageY)
					utils.PingSleep(utils.Medium, 250)
				}
			}
		} else {
			// The game remembers the last shared page visited, so clicking
			// the Shared tab does NOT always land on page 1. Force-reset to
			// page 1 by clicking prev the maximum number of times, then
			// navigate forward to the target page.
			sharedPages := ctx.Data.Inventory.SharedStashPages
			if sharedPages == 0 {
				if ctx.Data.IsDLC() {
					sharedPages = 5
				} else {
					sharedPages = 3
				}
			}
			for i := 0; i < sharedPages-1; i++ {
				ctx.HID.Click(game.LeftButton, ui.SharedStashPrevPageX, ui.SharedStashPrevPageY)
				utils.PingSleep(utils.Medium, 250)
			}
			nextClicks := tab - 2
			for i := 0; i < nextClicks; i++ {
				ctx.HID.Click(game.LeftButton, ui.SharedStashNextPageX, ui.SharedStashNextPageY)
				utils.PingSleep(utils.Medium, 250)
			}
		}
	}
}

func switchStashTabLegacy(ctx *context.Status, tab int) {
	// DLC-specific tabs
	switch tab {
	case StashTabGems:
		ctx.HID.Click(game.LeftButton, ui.DLCGemsTabXClassic, ui.DLCGemsTabYClassic)
		utils.PingSleep(utils.Medium, 500)
		return
	case StashTabMaterials:
		ctx.HID.Click(game.LeftButton, ui.DLCMaterialsTabXClassic, ui.DLCMaterialsTabYClassic)
		utils.PingSleep(utils.Medium, 500)
		return
	case StashTabRunes:
		ctx.HID.Click(game.LeftButton, ui.DLCRunesTabXClassic, ui.DLCRunesTabYClassic)
		utils.PingSleep(utils.Medium, 500)
		return
	}

	prev := ctx.CurrentGame.CurrentStashTab
	needTabClick := (prev < 2 && tab >= 2) || (prev >= 2 && tab < 2) || prev == 0 || prev >= StashTabGems

	if tab == 1 || needTabClick {
		uiTab := 1
		if tab >= 2 {
			uiTab = 2
		}
		x := ui.SwitchStashTabBtnXClassic + ui.SwitchStashTabBtnTabSizeClassic*uiTab - ui.SwitchStashTabBtnTabSizeClassic/2
		ctx.HID.Click(game.LeftButton, x, ui.SwitchStashTabBtnYClassic)
		utils.PingSleep(utils.Medium, 500)
	}

	if tab >= 2 {
		if prev >= 2 && prev < StashTabGems && !needTabClick {
			delta := tab - prev
			if delta > 0 {
				for i := 0; i < delta; i++ {
					ctx.HID.Click(game.LeftButton, ui.SharedStashNextPageXClassic, ui.SharedStashNextPageYClassic)
					utils.PingSleep(utils.Medium, 250)
				}
			} else if delta < 0 {
				for i := 0; i < -delta; i++ {
					ctx.HID.Click(game.LeftButton, ui.SharedStashPrevPageXClassic, ui.SharedStashPrevPageYClassic)
					utils.PingSleep(utils.Medium, 250)
				}
			}
		} else {
			// The game remembers the last shared page visited, so clicking
			// the Shared tab does NOT always land on page 1. Force-reset to
			// page 1 by clicking prev the maximum number of times, then
			// navigate forward to the target page.
			sharedPages := ctx.Data.Inventory.SharedStashPages
			if sharedPages == 0 {
				sharedPages = 3
			}
			for i := 0; i < sharedPages-1; i++ {
				ctx.HID.Click(game.LeftButton, ui.SharedStashPrevPageXClassic, ui.SharedStashPrevPageYClassic)
				utils.PingSleep(utils.Medium, 250)
			}
			nextClicks := tab - 2
			for i := 0; i < nextClicks; i++ {
				ctx.HID.Click(game.LeftButton, ui.SharedStashNextPageXClassic, ui.SharedStashNextPageYClassic)
				utils.PingSleep(utils.Medium, 250)
			}
		}
	}
}

func OpenStash() error {
	ctx := context.Get()
	ctx.SetLastAction("OpenStash")

	// The first stash open each game always lands on the personal tab.
	// Subsequent opens remember the last tab/page, so we keep CurrentStashTab as-is.
	if !ctx.CurrentGame.HasOpenedStash {
		ctx.CurrentGame.CurrentStashTab = 1
		ctx.CurrentGame.HasOpenedStash = true
	}

	bank, found := ctx.Data.Objects.FindOne(object.Bank)
	if !found {
		return errors.New("stash not found")
	}
	if err := InteractObject(bank,
		func() bool {
			return ctx.Data.OpenMenus.Stash
		},
	); err != nil {
		return err
	}
	ctx.RefreshGameData()
	markStashOpened(ctx)

	return nil
}

func CloseStash() error {
	ctx := context.Get()
	ctx.SetLastAction("CloseStash")

	// Do NOT reset CurrentStashTab — the game remembers the last tab/page
	// when the stash is reopened within the same game.

	if ctx.Data.OpenMenus.Stash {
		if fullPacketHIDDisabled(ctx) {
			return step.CloseAllMenus()
		}
		ctx.HID.PressKey(win.VK_ESCAPE)
	} else {
		return errors.New("stash is not open")
	}

	return nil
}

func TakeItemsFromStash(stashedItems []data.Item) error {
	ctx := context.Get()
	ctx.SetLastAction("TakeItemsFromStash")

	if !ctx.Data.OpenMenus.Stash {
		err := OpenStash()
		if err != nil {
			return err
		}
	}

	utils.PingSleep(utils.Medium, 250) // Medium operation: Wait for stash to open

	for _, i := range stashedItems {

		// Determine the tab to switch to based on location type
		var targetTab int
		switch i.Location.LocationType {
		case item.LocationStash:
			targetTab = 1 // Personal stash
		case item.LocationSharedStash:
			targetTab = i.Location.Page + 1 // Page 1=tab 2, Page 2=tab 3, etc.
		case item.LocationGemsTab:
			targetTab = StashTabGems
		case item.LocationMaterialsTab:
			targetTab = StashTabMaterials
		case item.LocationRunesTab:
			targetTab = StashTabRunes
		default:
			continue
		}

		SwitchStashTab(targetTab)

		if fullPacketHIDDisabled(ctx) {
			if err := takeItemFromStashPacket(ctx, i, targetTab); err != nil {
				return err
			}
			continue
		}

		// HID.ClickWithModifier — HID.Click now routes via in-process
		// SendMessageW (APC into D2R thread), no OS message queue.
		screenPos := ui.GetScreenCoordsForItem(i)
		ctx.HID.ClickWithModifier(game.LeftButton, screenPos.X, screenPos.Y, game.CtrlKey)
		utils.PingSleep(utils.Medium, 500)
	}

	return nil
}

func takeItemFromStashPacket(ctx *context.Status, i data.Item, targetTab int) error {
	if ctx == nil || ctx.PacketSender == nil {
		return fmt.Errorf("packet sender unavailable")
	}

	col, row, ok := findFreeInventorySlotForCursorItem(ctx, i)
	if !ok {
		return fmt.Errorf("no free inventory slot for %s", formatItemName(i))
	}

	switch i.Location.LocationType {
	case item.LocationStash:
		if err := ctx.PacketSender.PickItemFromContainer(i.UnitID, i.Position.X, i.Position.Y, amb.ContainerStash); err != nil {
			return err
		}
		utils.PingSleep(utils.Light, 150)
		if err := ctx.PacketSender.PutItemToInventory(uint32(i.UnitID), uint32(col), uint32(row), uint32(amb.ContainerInventory)); err != nil {
			return err
		}
	case item.LocationSharedStash:
		ownerID, ok := sharedStashOwnerID(ctx, targetTab)
		if !ok {
			return fmt.Errorf("missing shared stash owner ID for tab %d", targetTab)
		}
		if err := ctx.PacketSender.PullItemFromSharedStash(i, ownerID); err != nil {
			return err
		}
		utils.PingSleep(utils.Light, 150)
		if err := ctx.PacketSender.PutItemToInventory(uint32(i.UnitID), uint32(col), uint32(row), uint32(amb.ContainerInventory)); err != nil {
			return err
		}
	case item.LocationGemsTab, item.LocationMaterialsTab, item.LocationRunesTab:
		return fmt.Errorf("packet take from DLC stash tab %s is not wired", i.Location.LocationType)
	default:
		return fmt.Errorf("unsupported stash location %s", i.Location.LocationType)
	}

	utils.PingSleep(utils.Medium, 500)
	ctx.RefreshGameData()
	for _, invItem := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
		if invItem.UnitID == i.UnitID {
			return nil
		}
	}
	return fmt.Errorf("packet take from stash did not move %s to inventory", formatItemName(i))
}
