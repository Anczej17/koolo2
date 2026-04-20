package action

import (
	"fmt"

	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/difficulty"
	"local/internal/svc/internal/gamelib/data/item"
	"local/internal/svc/internal/gamelib/data/stat"
	"local/internal/svc/internal/gamelib/nip"
	"local/internal/svc/internal/action/step"
	"local/internal/svc/internal/context"
	"local/internal/svc/internal/game"
	"local/internal/svc/internal/town"
	"local/internal/svc/internal/ui"
	"local/internal/svc/internal/utils"
)

func IdentifyAll(skipIdentify bool) error {
	ctx := context.Get()
	ctx.SetLastAction("IdentifyAll")

	items := itemsToIdentify()

	ctx.Logger.Debug("Checking for items to identify...")
	if len(items) == 0 || skipIdentify {
		ctx.Logger.Debug("No items to identify...")
		return nil
	}

	shouldUseCain := ctx.CharacterCfg.Game.UseCainIdentify

	// Check conditions to force "skip Cain" even if UseCainIdentify is true
	_, isLevelingChar := ctx.Char.(context.LevelingCharacter)
	currentAct := ctx.Data.PlayerUnit.Area.Act()
	currentDifficulty := ctx.CharacterCfg.Game.Difficulty

	if isLevelingChar && currentAct == 4 && (currentDifficulty == difficulty.Nightmare || currentDifficulty == difficulty.Normal) {
		if shouldUseCain { // Only log this if Cain *would* have been used
			ctx.Logger.Debug("Forcing skip of Cain Identify: Leveling character in Act 4 Nightmare.")
		}
		shouldUseCain = false // Force Cain to be skipped
	}

	if shouldUseCain {
		ctx.Logger.Debug("Identifying all item with Cain...")
		// Close any open menus first
		step.CloseAllMenus()
		utils.PingSleep(utils.Medium, 500) // Medium operation: Close menus before Cain

		err := CainIdentify()
		// if identifying with cain fails then we should continue to identify using tome
		if err == nil {
			return nil // Successfully identified with Cain, no need for tome
		}
		ctx.Logger.Debug("Identifying with Cain failed, continuing with identifying with tome", "err", err)
		// Execution will continue here to the tome identification section
	}

	// --- Tome Identification Starts Here ---
	idTome, found := ctx.Data.Inventory.Find(item.TomeOfIdentify, item.LocationInventory)
	if !found {
		ctx.Logger.Warn("ID Tome not found, not identifying items")
		return nil
	}

	if st, statFound := idTome.FindStat(stat.Quantity, 0); !statFound || st.Value < len(items) {
		ctx.Logger.Info("Not enough ID scrolls, refilling...")
		VendorRefill(VendorRefillOpts{ForceRefill: true, BuyConsumables: true})
	}

	ctx.Logger.Info(fmt.Sprintf("Identifying %d items...", len(items)))

	// Close all menus to prevent issues
	step.CloseAllMenus()
	const maxInventoryAttempts = 5
	for attempt := 0; !ctx.Data.OpenMenus.Inventory && attempt < maxInventoryAttempts; attempt++ {
		ctx.HID.PressKeyBinding(ctx.Data.KeyBindings.Inventory)
		utils.PingSleep(utils.Critical, 1000) // Critical operation: Wait for inventory to open
		ctx.RefreshGameData()
	}
	if !ctx.Data.OpenMenus.Inventory {
		ctx.Logger.Warn("Failed to open inventory after max attempts, skipping identification")
		return nil
	}

	for _, i := range items {
		identifyItem(idTome, i)
	}
	step.CloseAllMenus()

	return nil
}

func CainIdentify() error {
	ctx := context.Get()
	ctx.SetLastAction("CainIdentify")

	unidBefore := countUnidentifiedItems(ctx)
	if unidBefore == 0 {
		return nil
	}

	// Direct 0x34 CainIdentifyAll via SendDualPacket CRASHES D2R (test31
	// 2026-04-19). The working path is the NPC-interact flow: walk to Cain,
	// open his dialog (0x4D), and select the "Identify Items" menu entry
	// (0x38 option=0). D2R handles the identify server-side.
	cainID := town.GetTownByArea(ctx.Data.PlayerUnit.Area).IdentifyNPC()
	if err := InteractNPC(cainID); err != nil {
		return fmt.Errorf("CainIdentify: failed to interact with %v: %w", cainID, err)
	}
	utils.Sleep(400)
	ctx.RefreshGameData()

	// Cain's menu: option 0 = Identify Items. Send via NPCDialogOption (0x38
	// with 9B form — SendUIPacket path). If server drops silently, the HID
	// keyboard fallback inside SelectNPCOption (Home + Enter) picks it up.
	SelectNPCOption(0, cainID)
	utils.Sleep(800)
	ctx.RefreshGameData()

	// Verify: are items now identified?
	unidAfter := countUnidentifiedItems(ctx)
	ctx.Logger.Debug("CainIdentify: dialog flow completed",
		"unid_before", unidBefore,
		"unid_after", unidAfter)

	CloseNPCDialog(cainID)
	utils.Sleep(200)

	if unidAfter < unidBefore {
		return nil
	}
	return fmt.Errorf("CainIdentify: dialog sent but %d items still unidentified (before=%d)", unidAfter, unidBefore)
}

func countUnidentifiedItems(ctx *context.Status) int {
	count := 0
	for _, i := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
		if i.Identified || i.Quality == item.QualityNormal || i.Quality == item.QualitySuperior {
			continue
		}
		count++
	}
	return count
}

func itemsToIdentify() (items []data.Item) {
	ctx := context.Get()
	ctx.SetLastAction("itemsToIdentify")

	for _, i := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
		if i.Identified || i.Quality == item.QualityNormal || i.Quality == item.QualitySuperior {
			continue
		}

		// Skip identifying items that fully match a rule when unid and we're not leveling
		_, isLevelingChar := ctx.Char.(context.LevelingCharacter)

		if !isLevelingChar {

			if _, result := ctx.CharacterCfg.Runtime.Rules.EvaluateAll(i); result == nip.RuleResultFullMatch {
				continue
			}
		}

		items = append(items, i)
	}

	return
}

func HaveItemsToStashUnidentified() bool {
	ctx := context.Get()
	ctx.SetLastAction("HaveItemsToStashUnidentified")

	// Do not stash unid items when leveling
	_, isLevelingChar := ctx.Char.(context.LevelingCharacter)

	if !isLevelingChar {
		items := ctx.Data.Inventory.ByLocation(item.LocationInventory)
		for _, i := range items {

			if !i.Identified {
				if _, result := ctx.CharacterCfg.Runtime.Rules.EvaluateAll(i); result == nip.RuleResultFullMatch {
					return true
				}
			}
		}
	}

	return false
}

func identifyItem(idTome data.Item, i data.Item) {
	ctx := context.Get()

	if ctx.CharacterCfg.PacketCasting.UseForIdentify && ctx.PacketSender != nil && ctx.MemoryInjector != nil {
		// Packet 0x26 identifies the item UNDER THE CURSOR (trailer is
		// 0xFFFFFFFF = "use currently hovered"). Without moving the cursor
		// over the target item first, D2R silently no-ops the packet.
		//
		// We use in-proc CursorPos override (written to SHM, rmod's
		// GetCursorPos hook reads it) — no SendMessage / real HID mouse move.
		itemScreen := ui.GetScreenCoordsForItem(i)
		screenX := ctx.GameReader.WindowLeftX + itemScreen.X
		screenY := ctx.GameReader.WindowTopY + itemScreen.Y
		_ = ctx.MemoryInjector.CursorPos(screenX, screenY)
		utils.Sleep(120)
		ctx.Logger.Debug("Identifying item via packet", "item", i.Name, "itemGID", i.UnitID, "tomeGID", idTome.UnitID, "cursor", [2]int{screenX, screenY})
		if err := ctx.PacketSender.IdentifyItem(idTome.UnitID); err == nil {
			utils.PingSleep(utils.Critical, 350)
			ctx.RefreshGameData()
			// Verify item is now identified; if not, fall back to HID
			for _, inv := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
				if inv.UnitID == i.UnitID && inv.Identified {
					return
				}
			}
			ctx.Logger.Warn("Identify packet sent but item STILL unidentified, falling back to HID", "item", i.Name)
		} else {
			ctx.Logger.Warn("Identify packet failed, falling back to HID", "error", err)
		}
	}

	// HID.Click — in-process SendMessageW. Right-click tome → left-click item.
	tomeScreen := ui.GetScreenCoordsForItem(idTome)
	utils.PingSleep(utils.Medium, 500)
	ctx.HID.Click(game.RightButton, tomeScreen.X, tomeScreen.Y)
	utils.PingSleep(utils.Critical, 1000)
	itemScreen := ui.GetScreenCoordsForItem(i)
	ctx.HID.Click(game.LeftButton, itemScreen.X, itemScreen.Y)
	utils.PingSleep(utils.Critical, 350)
}
