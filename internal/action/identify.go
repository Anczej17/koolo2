package action

import (
	"encoding/hex"
	"fmt"
	"os"

	"local/internal/svc/internal/action/step"
	"local/internal/svc/internal/context"
	"local/internal/svc/internal/game"
	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/difficulty"
	"local/internal/svc/internal/gamelib/data/item"
	"local/internal/svc/internal/gamelib/data/stat"
	"local/internal/svc/internal/gamelib/nip"
	"local/internal/svc/internal/packet/amb"
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
		if err == nil {
			return nil // Successfully identified with Cain, no need for tome
		}
		if ctx.PacketSender != nil {
			ctx.Logger.Warn("Cain identify packet route unresolved; falling back to packet tome identify", "err", err)
			ctx.RefreshGameData()
			items = itemsToIdentify()
			if len(items) == 0 {
				return nil
			}
		} else if ctx.HID != nil && ctx.HID.IsDisabled() {
			ctx.Logger.Warn("Cain identify failed in full-packet mode and PacketSender is nil; skipping tome fallback", "err", err)
			return err
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
		if err := VendorRefill(VendorRefillOpts{ForceRefill: true, BuyConsumables: true}); err != nil {
			return err
		}
	}

	ctx.Logger.Info(fmt.Sprintf("Identifying %d items...", len(items)))

	// Close all menus to prevent issues
	step.CloseAllMenus()
	const maxInventoryAttempts = 5
	for attempt := 0; !ctx.Data.OpenMenus.Inventory && attempt < maxInventoryAttempts; attempt++ {
		if ctx.PacketSender != nil {
			keys := game.KeyBindingKeys(ctx.Data.KeyBindings.Inventory)
			if keys[0] == 0 || keys[0] == 255 {
				ctx.Logger.Warn("Packet tome identify: inventory key binding unavailable")
				return nil
			}
			if err := ctx.PacketSender.PostKeyInProcess(keys[0]); err != nil {
				ctx.Logger.Warn("Packet tome identify: failed to open inventory", "error", err)
				return nil
			}
		} else {
			ctx.HID.PressKeyBinding(ctx.Data.KeyBindings.Inventory)
		}
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
	logCainUnidentifiedItems(ctx, "before")

	// Cain Identify All follows the live-confirmed 2026-04-27 capture:
	// open Cain dialog with 0x2F, then send no-cube 0x34 through dual-APC.
	// Keep this sequence clean: the successful capture was 0x2F -> 0x34.
	cainID := town.GetTownByArea(ctx.Data.PlayerUnit.Area).IdentifyNPC()

	cainMonster, found := ctx.Data.Monsters.FindOne(cainID, data.MonsterTypeNone)
	if !found {
		return fmt.Errorf("CainIdentify: %v not found in town", cainID)
	}

	if ctx.PacketSender == nil {
		return fmt.Errorf("CainIdentify: PacketSender nil")
	}

	if ctx.PathFinder.DistanceFromMe(cainMonster.Position) > 2 {
		if err := step.MoveTo(cainMonster.Position, step.WithDistanceToFinish(2), step.WithIgnoreMonsters()); err != nil {
			return fmt.Errorf("CainIdentify: move to Cain failed: %w", err)
		}
		utils.Sleep(200)
		ctx.RefreshGameData()
		if m, ok := ctx.Data.Monsters.FindOne(cainID, data.MonsterTypeNone); ok {
			cainMonster = m
		}
	}
	finalDistance := ctx.PathFinder.DistanceFromMe(cainMonster.Position)
	if finalDistance > 3 {
		return fmt.Errorf("CainIdentify: refusing 0x34 from distance %d", finalDistance)
	}

	npcInitPayload := amb.NewNPCInit(cainMonster.UnitID).GetPayload()
	identifyPayload := amb.NewNPCIdentifyItems(cainMonster.UnitID, 0, 0, 0).GetPayload()
	ctx.Logger.Info("CainIdentify: packet plan",
		"cain", cainMonster.UnitID,
		"distance", finalDistance,
		"npcInitRoute", "amb-dual-apc",
		"npcInitHex", hex.EncodeToString(npcInitPayload),
		"identifyRoute", "amb-dual-apc",
		"identifyHex", hex.EncodeToString(identifyPayload))
	if err := ctx.PacketSender.NPCInit(cainMonster.UnitID); err != nil {
		return fmt.Errorf("CainIdentify: NPCInit send failed: %w", err)
	}

	menuOpen := false
	for i := 0; i < 20; i++ {
		ctx.RefreshGameData()
		if ctx.Data.OpenMenus.NPCInteract {
			menuOpen = true
			break
		}
		utils.Sleep(100)
	}
	if !menuOpen {
		return fmt.Errorf("CainIdentify: NPC menu did not open after AMB NPCInit")
	}

	ctx.Logger.Info("CainIdentify: NPC dialog opened, stabilizing before 0x34",
		"cain", cainMonster.UnitID,
		"NPCInteract", ctx.Data.OpenMenus.NPCInteract,
		"NPCShop", ctx.Data.OpenMenus.NPCShop,
		"inventory", ctx.Data.OpenMenus.Inventory,
		"distance", ctx.PathFinder.DistanceFromMe(cainMonster.Position))
	utils.Sleep(300)
	ctx.RefreshGameData()
	if m, ok := ctx.Data.Monsters.FindOne(cainID, data.MonsterTypeNone); ok {
		cainMonster = m
	}
	if !ctx.Data.OpenMenus.NPCInteract {
		return fmt.Errorf("CainIdentify: NPC dialog closed before identify packet")
	}
	finalDistance = ctx.PathFinder.DistanceFromMe(cainMonster.Position)
	if finalDistance > 3 {
		return fmt.Errorf("CainIdentify: refusing 0x34 after dialog stabilization from distance %d", finalDistance)
	}

	identifyPayload = amb.NewNPCIdentifyItems(cainMonster.UnitID, 0, 0, 0).GetPayload()
	var packetRuntime any = "unavailable"
	if ctx.GameReader != nil && ctx.GameReader.Process != nil {
		packetRuntime = ctx.GameReader.Process.PacketRuntimeSnapshot(true)
	}
	ctx.Logger.Info("CainIdentify: sending AMB 0x34 identify-all",
		"cain", cainMonster.UnitID,
		"cube", data.UnitID(0),
		"cubeX", uint16(0),
		"cubeY", uint16(0),
		"route", "amb-dual-apc",
		"routeMatrix", ctx.PacketSender.RouteAvailability(),
		"packetRuntime", packetRuntime,
		"claudeModeActive", ctx.ClaudeModeActive,
		"claudeModeEnv", os.Getenv("CLAUDE_MODE"),
		"presenterDLL", os.Getenv("PRESENTER_DLL"),
		"claudeUseSniffer", os.Getenv("CLAUDE_USE_SNIFFER"),
		"hex", hex.EncodeToString(identifyPayload),
		"NPCInteract", ctx.Data.OpenMenus.NPCInteract,
		"NPCShop", ctx.Data.OpenMenus.NPCShop,
		"inventory", ctx.Data.OpenMenus.Inventory,
		"distance", finalDistance)
	if err := ctx.PacketSender.NPCIdentifyAll(cainMonster.UnitID, 0, 0, 0); err != nil {
		return fmt.Errorf("CainIdentify: NPCIdentifyAll send failed: %w", err)
	}

	utils.Sleep(1000)
	ctx.RefreshGameData()
	logCainUnidentifiedItems(ctx, "after")

	// Verify: are items now identified?
	unidAfter := countUnidentifiedItems(ctx)
	ctx.Logger.Debug("CainIdentify: dialog flow completed",
		"unid_before", unidBefore,
		"unid_after", unidAfter,
		"NPCInteract", ctx.Data.OpenMenus.NPCInteract,
		"NPCShop", ctx.Data.OpenMenus.NPCShop,
		"inventory", ctx.Data.OpenMenus.Inventory)

	if unidAfter > 0 {
		ctx.Logger.Warn("CainIdentify: AMB 0x34 left items unidentified",
			"unid_before", unidBefore,
			"unid_after", unidAfter)
	}

	CloseNPCDialog(cainID)
	utils.Sleep(200)

	if unidAfter < unidBefore {
		return nil
	}
	return fmt.Errorf("CainIdentify: dialog sent but %d items still unidentified (before=%d)", unidAfter, unidBefore)
}

func logCainUnidentifiedItems(ctx *context.Status, phase string) {
	for _, i := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
		if i.Identified || i.Quality == item.QualityNormal || i.Quality == item.QualitySuperior {
			continue
		}
		ctx.Logger.Info("CainIdentify: unidentified inventory item",
			"phase", phase,
			"item", i.Name,
			"gid", i.UnitID,
			"quality", i.Quality.ToString(),
			"x", i.Position.X,
			"y", i.Position.Y,
			"location", i.Location.LocationType)
	}
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

	if ctx.PacketSender != nil && ctx.MemoryInjector != nil {
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
			// Verify item is now identified; do not mask packet failures with HID.
			for _, inv := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
				if inv.UnitID == i.UnitID && inv.Identified {
					return
				}
			}
			ctx.Logger.Warn("Identify packet sent but item still unidentified, skipping HID fallback", "item", i.Name, "itemGID", i.UnitID)
			return
		} else {
			ctx.Logger.Warn("Identify packet failed, skipping HID fallback", "error", err, "item", i.Name, "itemGID", i.UnitID)
			return
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
