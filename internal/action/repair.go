package action

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"local/internal/svc/internal/action/step"
	"local/internal/svc/internal/context"
	botCtx "local/internal/svc/internal/context"
	"local/internal/svc/internal/game"
	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/item"
	"local/internal/svc/internal/gamelib/data/npc"
	"local/internal/svc/internal/gamelib/data/stat"
	"local/internal/svc/internal/gamelib/memory"
	"local/internal/svc/internal/town"
	"local/internal/svc/internal/ui"
	"local/internal/svc/internal/utils"
)

var repairAllCostText = regexp.MustCompile(`(?i)repair\s+all\s+equipment:?\s*([0-9][0-9,.]*)`)

func RepairTownRoutine() error {
	ctx := context.Get()
	ctx.SetLastAction("RepairTownRoutine")

	if !ctx.Data.PlayerUnit.Area.IsTown() {
		return nil
	}

	force, reason := shouldForceRepairAllForJavazonDkQuantity(ctx)
	if force {
		ctx.Logger.Info(reason)
		repairNPC := town.GetTownByArea(ctx.Data.PlayerUnit.Area).RepairNPC()
		return repairAllAtNPC(repairNPC)
	}

	return Repair()
}

func shouldForceRepairAllForJavazonDkQuantity(ctx *context.Status) (bool, string) {
	if ctx.CharacterCfg.Character.Class != "javazon" {
		return false, ""
	}
	if !ctx.CharacterCfg.Character.Javazon.DensityKillerEnabled {
		return false, ""
	}

	threshold := ctx.CharacterCfg.Character.Javazon.DensityKillerForceRefillBelowPercent
	if threshold <= 0 || threshold > 100 {
		threshold = 50
	}

	for _, itm := range ctx.Data.Inventory.ByLocation(item.LocationEquipped) {
		if itm.Location.BodyLocation != item.LocLeftArm && itm.Location.BodyLocation != item.LocRightArm {
			continue
		}

		if itm.Ethereal {
			continue
		}

		itmType := itm.Type()
		if !itmType.IsType(item.TypeJavelin) && !itmType.IsType(item.TypeAmazonJavelin) {
			continue
		}

		qty, qtyFound := itm.FindStat(stat.Quantity, 0)
		if !qtyFound {
			continue
		}

		maxQty := getMaxJavelinQuantity(itm)
		if maxQty <= 0 {
			continue
		}

		if qty.Value < 0 {
			qty.Value = 0
		}

		pct := (qty.Value * 100) / maxQty

		if pct < threshold {
			return true, fmt.Sprintf("Force RepairAll for javelin refill: %s %d/%d (%d%%) < %d%%",
				itm.Name, qty.Value, maxQty, pct, threshold)
		}
	}

	return false, ""
}

func getMaxJavelinQuantity(itm data.Item) int {
	qty, qtyFound := itm.FindStat(stat.Quantity, 0)
	currentQty := 0
	if qtyFound {
		currentQty = qty.Value
	}

	name := strings.ToLower(string(itm.Name))

	// Titan's Revenge: 180 max (Ceremonial Javelin base)
	if strings.Contains(name, "titan") {
		return 180
	}
	if strings.Contains(name, "ceremonial") && currentQty > 80 {
		return 180
	}

	// Thunderstroke: 80 max (Matriarchal Javelin base)
	if strings.Contains(name, "thunder") || strings.Contains(name, "tstroke") {
		return 80
	}

	// Check for Replenishes Quantity stat (Titan's Revenge)
	for _, s := range itm.Stats {
		if s.ID == 252 {
			return 180
		}
	}

	// Fallback: base type defaults
	itmType := itm.Type()
	if itmType.IsType(item.TypeAmazonJavelin) {
		return 80
	}
	if itmType.IsType(item.TypeJavelin) {
		return 60
	}

	return 0
}

func repairAllAtNPC(repairNPC npc.ID) error {
	ctx := context.Get()

	if repairNPC == npc.Larzuk {
		MoveToCoords(data.Position{X: 5135, Y: 5046})
	}
	if repairNPC == npc.Hratli {
		if err := FindHratliEverywhere(); err != nil {
			return err
		}
	}

	if err := InteractNPC(repairNPC); err != nil {
		return err
	}

	if !SelectNPCTradeOption(repairNPC) {
		return fmt.Errorf("repair trade window did not open for npc %d", repairNPC)
	}

	utils.Sleep(100)
	clickRepairButton(ctx, repairNPC)
	utils.Sleep(500)

	return step.CloseAllMenus()
}

// clickRepairButton triggers the "repair all" action. When PacketSender is
// available, packet 0x35 is authoritative and HID is used only when no sender
// exists at all.
func clickRepairButton(ctx *context.Status, repairNPC npc.ID) {
	if ctx.PacketSender != nil {
		npcUnit, found := ctx.Data.Monsters.FindOne(repairNPC, data.MonsterTypeNone)
		if found {
			repairCost, costFound := repairAllCostFromPanels(ctx)
			if !costFound {
				ctx.Logger.Warn("Repair packet path: repair-all cost not found in panels, skipping HID fallback", "npc", repairNPC, "gid", npcUnit.UnitID)
				return
			}
			if repairCost == 0 {
				ctx.Logger.Debug("Repair packet path: repair-all cost is 0", "npc", repairNPC, "gid", npcUnit.UnitID)
				return
			}

			ctx.Logger.Debug("Sending AMB 0x35 NPCRepairAll", "npc", repairNPC, "gid", npcUnit.UnitID, "cost", repairCost)
			if err := ctx.PacketSender.NPCRepairAll(npcUnit.UnitID, repairCost); err != nil {
				ctx.Logger.Warn("NPCRepairAll packet failed, skipping HID fallback", "error", err, "npc", repairNPC, "gid", npcUnit.UnitID, "cost", repairCost)
				return
			}

			utils.Sleep(300)
			ctx.RefreshGameData()
			if afterCost, ok := repairAllCostFromPanels(ctx); ok && afterCost > 0 {
				ctx.Logger.Warn("NPCRepairAll packet sent but repair-all cost is still non-zero", "npc", repairNPC, "gid", npcUnit.UnitID, "beforeCost", repairCost, "afterCost", afterCost)
			}
			return
		} else {
			ctx.Logger.Warn("Repair packet path: NPC unit not found, skipping HID fallback", "npc", repairNPC)
		}
		return
	}

	if ctx.Data.LegacyGraphics {
		ctx.HID.Click(game.LeftButton, ui.RepairButtonXClassic, ui.RepairButtonYClassic)
	} else {
		ctx.HID.Click(game.LeftButton, ui.RepairButtonX, ui.RepairButtonY)
	}
}

func repairAllCostFromPanels(ctx *context.Status) (uint32, bool) {
	if ctx == nil || ctx.GameReader == nil {
		return 0, false
	}
	panels := ctx.GameReader.ReadAllPanels()
	for _, p := range panels {
		if price, ok := repairAllCostFromPanel(p); ok {
			return price, true
		}
	}
	return 0, false
}

func repairAllCostFromPanel(p data.Panel) (uint32, bool) {
	if p.PanelVisible && p.PanelEnabled {
		text := memory.GetText(p)
		if price, ok := parseRepairAllCostText(text); ok {
			return price, true
		}
		if p.PanelName == "button_repair_all" {
			if price, ok := firstRepairCostNumber(text); ok {
				return price, true
			}
		}
	}

	for _, child := range p.PanelChildren {
		if price, ok := repairAllCostFromPanel(child); ok {
			return price, true
		}
	}
	return 0, false
}

func parseRepairAllCostText(text string) (uint32, bool) {
	m := repairAllCostText.FindStringSubmatch(text)
	if len(m) != 2 {
		return 0, false
	}
	return parseRepairCostNumber(m[1])
}

func firstRepairCostNumber(text string) (uint32, bool) {
	start := -1
	end := -1
	for i, r := range text {
		if (r >= '0' && r <= '9') || (start >= 0 && (r == ',' || r == '.')) {
			if start < 0 {
				start = i
			}
			end = i + len(string(r))
			continue
		}
		if start >= 0 {
			break
		}
	}
	if start < 0 {
		return 0, false
	}
	return parseRepairCostNumber(text[start:end])
}

func parseRepairCostNumber(raw string) (uint32, bool) {
	raw = strings.NewReplacer(",", "", ".", "").Replace(raw)
	v, err := strconv.ParseUint(raw, 10, 32)
	if err != nil {
		return 0, false
	}
	return uint32(v), true
}

func Repair() error {
	ctx := context.Get()
	ctx.SetLastAction("Repair")

	for _, i := range ctx.Data.Inventory.ByLocation(item.LocationEquipped) {
		triggerRepair := false
		logMessage := ""

		_, indestructible := i.FindStat(stat.Indestructible, 0)
		quantity, quantityFound := i.FindStat(stat.Quantity, 0)

		if indestructible && !quantityFound {
			continue
		}

		if i.Ethereal && !quantityFound {
			continue
		}

		if quantityFound {
			if ctx.CharacterCfg.Character.Class == "javazon" &&
				ctx.CharacterCfg.Character.Javazon.DensityKillerEnabled {
				itmType := i.Type()
				if itmType.IsType(item.TypeJavelin) || itmType.IsType(item.TypeAmazonJavelin) {
					continue
				}
			}

			if quantity.Value < 15 || i.IsBroken {
				triggerRepair = true
				logMessage = fmt.Sprintf("Replenishing %s, quantity is %d", i.Name, quantity.Value)
			}
		} else {
			durability, found := i.FindStat(stat.Durability, 0)
			maxDurability, maxDurabilityFound := i.FindStat(stat.MaxDurability, 0)
			durabilityPercent := -1

			if maxDurabilityFound && found {
				durabilityPercent = int((float64(durability.Value) / float64(maxDurability.Value)) * 100)
			}

			if i.IsBroken || (durabilityPercent != -1 && durabilityPercent <= 20) {
				triggerRepair = true
				logMessage = fmt.Sprintf("Repairing %s, item durability is %d percent", i.Name, durabilityPercent)
			}
		}

		if triggerRepair {
			ctx.Logger.Info(logMessage)

			repairNPC := town.GetTownByArea(ctx.Data.PlayerUnit.Area).RepairNPC()
			if repairNPC == npc.Larzuk {
				MoveToCoords(data.Position{X: 5135, Y: 5046})
			}
			if repairNPC == npc.Hratli {
				if err := FindHratliEverywhere(); err != nil {
					return err
				}
			}

			if err := InteractNPC(repairNPC); err != nil {
				return err
			}

			if !SelectNPCTradeOption(repairNPC) {
				return fmt.Errorf("repair trade window did not open for npc %d", repairNPC)
			}

			utils.Sleep(100)
			clickRepairButton(ctx, repairNPC)
			utils.Sleep(500)

			return step.CloseAllMenus()
		}
	}

	return nil
}

func RepairRequired() bool {
	ctx := context.Get()
	ctx.SetLastAction("RepairRequired")

	for _, i := range ctx.Data.Inventory.ByLocation(item.LocationEquipped) {
		_, indestructible := i.FindStat(stat.Indestructible, 0)
		quantity, quantityFound := i.FindStat(stat.Quantity, 0)

		if indestructible && !quantityFound {
			continue
		}

		if i.Ethereal && !quantityFound {
			continue
		}

		if quantityFound {
			if quantity.Value < 15 || i.IsBroken {
				return true
			}
		} else {
			durability, found := i.FindStat(stat.Durability, 0)
			maxDurability, maxDurabilityFound := i.FindStat(stat.MaxDurability, 0)

			if i.IsBroken || (maxDurabilityFound && !found) {
				return true
			}

			if found && maxDurabilityFound {
				durabilityPercent := int((float64(durability.Value) / float64(maxDurability.Value)) * 100)
				if durabilityPercent <= 20 {
					return true
				}
			}
		}
	}

	return false
}

func IsEquipmentBroken() bool {
	ctx := context.Get()
	ctx.SetLastAction("EquipmentBroken")

	for _, i := range ctx.Data.Inventory.ByLocation(item.LocationEquipped) {
		_, indestructible := i.FindStat(stat.Indestructible, 0)
		_, quantityFound := i.FindStat(stat.Quantity, 0)

		if i.Ethereal && !quantityFound {
			continue
		}

		if indestructible && !quantityFound {
			continue
		}

		if i.IsBroken {
			ctx.Logger.Debug("Equipment is broken, returning to town", "item", i.Name)
			return true
		}
	}

	return false
}

func FindHratliEverywhere() error {
	ctx := botCtx.Get()
	ctx.SetLastStep("FindHratliEverywhere")

	finalPos := data.Position{X: 5224, Y: 5045}
	MoveToCoords(finalPos)

	_, found := ctx.Data.Monsters.FindOne(npc.Hratli, data.MonsterTypeNone)

	if !found {
		ctx.Logger.Warn("Hratli not found at final position. Moving to start position to trigger quest update and force quitting game.")

		startPos := data.Position{X: 5116, Y: 5167}
		MoveToCoords(startPos)

		if err := InteractNPC(npc.Hratli); err != nil {
			ctx.Logger.Warn("Failed to interact with Hratli at start position.", "error", err)
		}

		step.CloseAllMenus()
		return nil
	}

	return nil
}
