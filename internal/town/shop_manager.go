package town

import (
	"encoding/binary"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"local/internal/svc/internal/context"
	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/item"
	"local/internal/svc/internal/gamelib/data/stat"
	"local/internal/svc/internal/gamelib/memory"
	"local/internal/svc/internal/gamelib/nip"
	packet "local/internal/svc/internal/packet"
	"local/internal/svc/internal/packet/amb"
	"local/internal/svc/internal/ui"
	"local/internal/svc/internal/utils"
)

var questItems = []item.Name{
	"StaffOfKings",
	"HoradricStaff",
	"AmuletOfTheViper",
	"KhalimsFlail",
	"KhalimsWill",
	"HellforgeHammer",
}

const (
	ambVendorToPosX = 9
	ambVendorToPosY = 8
	ambVendorGridW  = 10
	ambVendorGridH  = 10

	ambVendorBuySourceTab       = 0
	ambVendorBuyTargetLocation  = 0
	ambVendorBuyTransactionMode = 0

	ambVendorSellTargetTab       = 3
	ambVendorSellTargetLocation  = 0
	ambVendorSellTransactionMode = 0
)

func BuyConsumables(forceRefill bool) {
	ctx := context.Get()

	missingHealingPotionInBelt := ctx.BeltManager.GetMissingCount(data.HealingPotion)
	missingManaPotiontInBelt := ctx.BeltManager.GetMissingCount(data.ManaPotion)
	missingHealingPotionInInventory := ctx.Data.MissingPotionCountInInventory(data.HealingPotion)
	missingManaPotionInInventory := ctx.Data.MissingPotionCountInInventory(data.ManaPotion)

	// We traverse the items in reverse order because vendor has the best potions at the end
	healingPot, healingPotfound := findFirstMatch("superhealingpotion", "greaterhealingpotion", "healingpotion", "lighthealingpotion", "minorhealingpotion")
	manaPot, manaPotfound := findFirstMatch("supermanapotion", "greatermanapotion", "manapotion", "lightmanapotion", "minormanapotion")

	ctx.Logger.Debug(fmt.Sprintf("Buying: %d Healing potions and %d Mana potions for belt", missingHealingPotionInBelt, missingManaPotiontInBelt))

	if ShouldBuyTPs() || forceRefill {
		if _, found := ctx.Data.Inventory.Find(item.TomeOfTownPortal, item.LocationInventory); !found && ctx.Data.PlayerUnit.TotalPlayerGold() > 450 {
			ctx.Logger.Info("TP Tome not found, buying one...")
			if itm, itmFound := ctx.Data.Inventory.Find(item.TomeOfTownPortal, item.LocationVendor); itmFound {
				BuyItem(itm, 1)
			}
		}
	}

	// buy for belt first
	if healingPotfound && missingHealingPotionInBelt > 0 {
		BuyItem(healingPot, missingHealingPotionInBelt)
		missingHealingPotionInBelt = 0
	}

	if manaPotfound && missingManaPotiontInBelt > 0 {
		BuyItem(manaPot, missingManaPotiontInBelt)
		missingManaPotiontInBelt = 0
	}

	ctx.Logger.Debug(fmt.Sprintf("Buying: %d Healing potions and %d Mana potions for inventory", missingHealingPotionInInventory, missingManaPotionInInventory))

	// then buy for inventory
	if healingPotfound && missingHealingPotionInInventory > 0 {
		BuyItem(healingPot, missingHealingPotionInInventory)
		missingHealingPotionInInventory = 0
	}

	if manaPotfound && missingManaPotionInInventory > 0 {
		BuyItem(manaPot, missingManaPotionInInventory)
		missingManaPotionInInventory = 0
	}

	if ShouldBuyTPs() || forceRefill {
		ctx.Logger.Debug("Filling TP Tome...")
		if itm, found := ctx.Data.Inventory.Find(item.ScrollOfTownPortal, item.LocationVendor); found {
			if ctx.Data.PlayerUnit.TotalPlayerGold() > 6000 {
				buyFullStack(itm, -1) // -1 for irrelevant currentKeysInInventory
			} else {
				BuyItem(itm, 1)
			}
		}
	}

	disableIDs := false
	if ctx.CharacterCfg.Game.DisableIdentifyTome {
		isLeveling := false
		if ctx.IsLevelingCharacter != nil {
			isLeveling = *ctx.IsLevelingCharacter
		} else {
			isLeveling = ctx.Data.IsLevelingCharacter
		}
		disableIDs = !isLeveling
	}

	if disableIDs {
		ctx.Logger.Debug("DisableIdentifyTome enabled – skipping ID tome/scroll purchases.")
	} else if ShouldBuyIDs() || forceRefill {

		if _, found := ctx.Data.Inventory.Find(item.TomeOfIdentify, item.LocationInventory); !found && ctx.Data.PlayerUnit.TotalPlayerGold() > 360 {
			ctx.Logger.Info("ID Tome not found, buying one...")
			if itm, itmFound := ctx.Data.Inventory.Find(item.TomeOfIdentify, item.LocationVendor); itmFound {
				BuyItem(itm, 1)
			}
		}
		ctx.Logger.Debug("Filling IDs Tome...")
		if itm, found := ctx.Data.Inventory.Find(item.ScrollOfIdentify, item.LocationVendor); found {
			if ctx.Data.PlayerUnit.TotalPlayerGold() > 16000 {
				buyFullStack(itm, -1) // -1 for irrelevant currentKeysInInventory
			} else {
				BuyItem(itm, 1)
			}
		}
	}

	keyQuantity, shouldBuyKeys := ShouldBuyKeys() // keyQuantity is total keys in inventory
	if ctx.Data.PlayerUnit.Class != data.Assassin && (shouldBuyKeys || forceRefill) {
		if itm, found := ctx.Data.Inventory.Find(item.Key, item.LocationVendor); found {
			ctx.Logger.Debug("Vendor with keys detected, provisioning...")

			// Only buy if vendor has keys and we have less than 12
			qtyVendor, _ := itm.FindStat(stat.Quantity, 0)
			if (qtyVendor.Value > 0) && (keyQuantity < 12) {
				// Pass keyQuantity to buyFullStack so it knows how many keys we had initially
				buyFullStack(itm, keyQuantity)
			}
		}
	}
}

func findFirstMatch(itemNames ...string) (data.Item, bool) {
	ctx := context.Get()
	for _, name := range itemNames {
		if itm, found := ctx.Data.Inventory.Find(item.Name(name), item.LocationVendor); found {
			return itm, true
		}
	}

	return data.Item{}, false
}

func ShouldBuyTPs() bool {
	portalTome, found := context.Get().Data.Inventory.Find(item.TomeOfTownPortal, item.LocationInventory)
	if !found {
		return true
	}

	qty, found := portalTome.FindStat(stat.Quantity, 0)

	return qty.Value < 5 || !found
}

func ShouldBuyIDs() bool {
	ctx := context.Get()

	_, isLevelingChar := ctx.Char.(context.LevelingCharacter)

	// Respect end-game setting: completely disable ID tome purchasing
	if ctx.CharacterCfg.Game.DisableIdentifyTome && !isLevelingChar {
		// Do not buy Tome of Identify nor ID scrolls at all
		ctx.Logger.Debug("DisableIdentifyTome enabled – skipping ID tome/scroll purchases.")
		return false
	}

	// Original behaviour: keep at least 10 IDs in the tome
	idTome, found := ctx.Data.Inventory.Find(item.TomeOfIdentify, item.LocationInventory)
	if !found {
		return true
	}

	qty, found := idTome.FindStat(stat.Quantity, 0)
	return !found || qty.Value < 10
}

func ShouldBuyKeys() (int, bool) {
	// Re-calculating total keys each time ShouldBuyKeys is called for accuracy
	ctx := context.Get()
	totalKeys := 0
	for _, itm := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
		if itm.Name == item.Key {
			if qty, found := itm.FindStat(stat.Quantity, 0); found {
				totalKeys += qty.Value
			}
		}
	}

	if totalKeys == 0 {
		return 0, true // No keys found, so we should buy
	}

	// We only need to buy if we have less than 12 keys.
	return totalKeys, totalKeys < 12
}

func SellJunk(lockConfig ...[][]int) {
	ctx := context.Get()
	ctx.Logger.Debug("--- SellJunk() function entered ---")
	ctx.Logger.Debug("Selling junk items and excess keys...")

	// --- OPTIMIZED LOGIC FOR SELLING EXCESS KEYS ---
	var allKeyStacks []data.Item
	totalKeys := 0

	// Iterate through ALL items in the inventory to find all key stacks
	// Make sure to re-fetch inventory data before this loop if it hasn't been refreshed recently
	ctx.RefreshGameData() // Crucial to have up-to-date inventory
	for _, itm := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
		if itm.Name == item.Key {
			if qty, found := itm.FindStat(stat.Quantity, 0); found {
				allKeyStacks = append(allKeyStacks, itm)
				totalKeys += qty.Value
			}
		}
	}

	ctx.Logger.Debug(fmt.Sprintf("Total keys found across all stacks in inventory: %d", totalKeys))

	if totalKeys > 12 {
		excessCount := totalKeys - 12
		ctx.Logger.Info(fmt.Sprintf("Found %d excess keys (total %d). Selling them.", excessCount, totalKeys))

		keysSold := 0

		// Sort key stacks by quantity in descending order to sell larger stacks first
		slices.SortFunc(allKeyStacks, func(a, b data.Item) int {
			qtyA, _ := a.FindStat(stat.Quantity, 0)
			qtyB, _ := b.FindStat(stat.Quantity, 0)
			return qtyB.Value - qtyA.Value // Descending order
		})

		// 1. Sell full stacks until we are close to the target
		stacksToProcess := make([]data.Item, len(allKeyStacks))
		copy(stacksToProcess, allKeyStacks)

		for _, keyStack := range stacksToProcess {
			if keysSold >= excessCount {
				break // We've sold enough
			}

			qtyInStack, found := keyStack.FindStat(stat.Quantity, 0)
			if !found {
				continue
			}

			// If selling this entire stack still leaves us with at least 12 keys
			// Or if this stack exactly equals the remaining excess to sell
			if (totalKeys-qtyInStack.Value >= 12) || (qtyInStack.Value == excessCount-keysSold) {
				ctx.Logger.Debug(fmt.Sprintf("Selling full stack of %d keys from %v", qtyInStack.Value, keyStack.Position))
				SellItemFullStack(keyStack)
				keysSold += qtyInStack.Value
				totalKeys -= qtyInStack.Value     // Update total keys count
				ctx.RefreshGameData()             // Refresh after selling a full stack
				utils.PingSleep(utils.Light, 200) // Light operation: Short delay for UI update
			}
		}

		// Re-evaluate total keys after selling full stacks
		ctx.RefreshGameData()
		totalKeys = 0
		allKeyStacks = []data.Item{} // Clear and re-populate allKeyStacks
		for _, itm := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
			if itm.Name == item.Key {
				if qty, found := itm.FindStat(stat.Quantity, 0); found {
					allKeyStacks = append(allKeyStacks, itm)
					totalKeys += qty.Value
				}
			}
		}

		// 2. If there's still excess, sell individual keys from one of the remaining stacks
		if totalKeys > 12 {
			excessCount = totalKeys - 12 // Recalculate excess after full stack sales
			ctx.Logger.Info(fmt.Sprintf("Still have %d excess keys. Selling individually from a remaining stack.", excessCount))

			// Find *any* remaining key stack to sell from
			var remainingKeyStack data.Item
			for _, itm := range allKeyStacks {
				if itm.Name == item.Key {
					remainingKeyStack = itm
					break
				}
			}

			if remainingKeyStack.Name != "" { // Check if a stack was found
				for i := 0; i < excessCount; i++ {
					SellItem(remainingKeyStack)
					keysSold++
					ctx.RefreshGameData()
					utils.PingSleep(utils.Light, 100) // Light operation: Individual sell delay
				}
			} else {
				ctx.Logger.Warn("No remaining key stacks found to sell individual keys from, despite excess reported.")
			}
		}

		ctx.Logger.Info(fmt.Sprintf("Finished selling excess keys. Keys sold: %d. Estimated remaining: %d", keysSold, totalKeys-keysSold))
	} else {
		ctx.Logger.Debug("No excess keys to sell (12 or less).")
	}
	// --- END OPTIMIZED LOGIC ---

	// Existing logic to sell other junk items, now with lockConfig support
	for _, i := range ItemsToBeSold(lockConfig...) {
		SellItem(i)
	}
}

// findActiveMerchant returns the GID of the merchant whose trade menu is
// currently open. Heuristic: scan Monsters and pick the NPC closest to the
// player. Returns 0 if none found within a reasonable distance.
func findActiveMerchant(ctx *context.Status) data.UnitID {
	const maxDistanceSq = 20 * 20
	playerPos := ctx.Data.PlayerUnit.Position
	bestDistSq := maxDistanceSq + 1
	var best data.UnitID

	for _, m := range ctx.Data.Monsters {
		if m.Type != data.MonsterTypeNone {
			continue
		}
		dx := m.Position.X - playerPos.X
		dy := m.Position.Y - playerPos.Y
		distSq := dx*dx + dy*dy
		if distSq < bestDistSq {
			bestDistSq = distSq
			best = m.UnitID
		}
	}
	return best
}

// SellItem sells a single item. When PacketSender is available, AMB 0x33 is
// authoritative and we do not mask packet failures with HID.
func SellItem(i data.Item) {
	ctx := context.Get()

	if ctx.PacketSender != nil {
		if !ctx.Data.OpenMenus.NPCShop {
			ctx.Logger.Warn("Sell packet path: NPCShop is not open, skipping send", "item", i.Name, "itemGID", i.UnitID)
			return
		}
		merchantGID := findActiveMerchant(ctx)
		if merchantGID == 0 {
			ctx.Logger.Warn("Sell packet path: active merchant not found, skipping HID fallback", "item", i.Name, "itemGID", i.UnitID)
			return
		}
		merchantMonster, mFound := ctx.Data.Monsters.FindByID(merchantGID)
		if !mFound {
			ctx.Logger.Warn("Sell packet path: merchant monster not found by GID, skipping HID fallback", "merchantGID", merchantGID, "item", i.Name, "itemGID", i.UnitID)
			return
		}
		sellPrice, priceKnown := vendorSellPrice(ctx, i)
		if !priceKnown {
			ctx.Logger.Warn("Sell packet path: native sell price unavailable, refusing guessed price", "item", i.Name, "itemGID", i.UnitID)
			return
		}
		if sellItemPacketAllocated(ctx, i, merchantMonster, sellPrice, "sell") {
			return
		}
		ctx.Logger.Warn("Sell packet sent but item still present after refresh", "item", i.Name, "itemGID", i.UnitID, "sellPrice", sellPrice)
		return
	}
	ctx.Logger.Warn("Sell packet path unavailable: PacketSender nil, refusing HID fallback", "item", i.Name, "itemGID", i.UnitID)
}

func sellItemViaCursorPacket(ctx *context.Status, i data.Item, merchant data.Monster, sellPrice uint32) bool {
	ctx.Logger.Warn("Sell AMB 0x33 had no effect; trying AMB cursor sell 0x19 -> 0x33",
		"item", i.Name,
		"itemGID", i.UnitID,
		"fromX", i.Position.X,
		"fromY", i.Position.Y,
		"merchantGID", merchant.UnitID)
	if err := ctx.PacketSender.PickItemFromContainer(i.UnitID, i.Position.X, i.Position.Y, amb.ContainerInventory); err != nil {
		ctx.Logger.Warn("Sell cursor pick failed", "item", i.Name, "itemGID", i.UnitID, "error", err)
		return false
	}
	utils.PingSleep(utils.Light, 150)
	if sellItemPacketVariants(ctx, i, merchant, sellPrice, "cursor") {
		return true
	}
	if err := ctx.PacketSender.NPCSellAMB(
		i,
		merchant,
		sellPrice,
		ambVendorToPosX,
		ambVendorToPosY,
		ambVendorSellTargetTab,
		ambVendorSellTargetLocation,
		ambVendorSellTransactionMode,
	); err != nil {
		ctx.Logger.Warn("Sell cursor 0x33 failed", "item", i.Name, "itemGID", i.UnitID, "error", err)
		_ = ctx.PacketSender.PutItemToInventory(uint32(i.UnitID), uint32(i.Position.X), uint32(i.Position.Y), uint32(amb.ContainerInventory))
		return false
	}
	utils.PingSleep(utils.Medium, 700)
	ctx.RefreshGameData()
	if !itemStillPlayerHeld(ctx, i.UnitID) {
		ctx.Logger.Info("Sell cursor packet OK - item gone", "item", i.Name, "itemGID", i.UnitID, "sellPrice", sellPrice)
		return true
	}
	ctx.Logger.Debug("Sell cursor packet left item present; restoring inventory slot", "item", i.Name, "itemGID", i.UnitID)
	_ = ctx.PacketSender.PutItemToInventory(uint32(i.UnitID), uint32(i.Position.X), uint32(i.Position.Y), uint32(amb.ContainerInventory))
	utils.PingSleep(utils.Light, 150)
	ctx.RefreshGameData()
	return false
}

func sellItemPacketVariants(ctx *context.Status, i data.Item, merchant data.Monster, sellPrice uint32, stage string) bool {
	if ctx == nil || ctx.PacketSender == nil {
		return false
	}

	term := sellDispatchTerm(i)
	price := sellPrice
	variants := []struct {
		name string
		send func() error
	}{
		{
			name: "dispatch-pos",
			send: func() error {
				return ctx.PacketSender.NPCSellDispatch(price, i.UnitID, merchant.UnitID, uint16(i.Position.X), uint16(i.Position.Y), term)
			},
		},
		{
			name: "amb-pos",
			send: func() error {
				return ctx.PacketSender.NPCSellAMB(
					i,
					merchant,
					price,
					ambVendorToPosX,
					ambVendorToPosY,
					ambVendorSellTargetTab,
					ambVendorSellTargetLocation,
					ambVendorSellTransactionMode,
				)
			},
		},
		{
			name: "amb-zero-source",
			send: func() error {
				return ctx.PacketSender.NPCSellAMBAt(
					i,
					merchant,
					price,
					ambVendorToPosX,
					ambVendorToPosY,
					0,
					0,
					ambVendorSellTargetTab,
					ambVendorSellTargetLocation,
					ambVendorSellTransactionMode,
				)
			},
		},
		{
			name: "dispatch-zero-source",
			send: func() error {
				return ctx.PacketSender.NPCSellDispatch(price, i.UnitID, merchant.UnitID, 0, 0, term)
			},
		},
	}

	for _, variant := range variants {
		ctx.Logger.Debug("Trying sell packet variant",
			"stage", stage,
			"variant", variant.name,
			"item", i.Name,
			"itemGID", i.UnitID,
			"price", price,
			"term", term)
		if err := variant.send(); err != nil {
			ctx.Logger.Warn("Sell packet variant failed to send",
				"stage", stage,
				"variant", variant.name,
				"item", i.Name,
				"itemGID", i.UnitID,
				"price", price,
				"error", err)
			continue
		}
		utils.PingSleep(utils.Medium, 650)
		ctx.RefreshGameData()
		if !itemStillPlayerHeld(ctx, i.UnitID) {
			ctx.Logger.Info("Sell packet variant OK - item gone",
				"stage", stage,
				"variant", variant.name,
				"item", i.Name,
				"itemGID", i.UnitID,
				"price", price)
			keepMerchantShopOpen(ctx, merchant)
			return true
		}
	}

	return false
}

func keepMerchantShopOpen(ctx *context.Status, merchant data.Monster) {
	ctx.RefreshGameData()
	if ctx.Data.OpenMenus.NPCShop {
		return
	}
	ctx.Logger.Debug("Reopening NPC shop after sell packet", "merchantGID", merchant.UnitID, "npc", merchant.Name)
	if err := ctx.PacketSender.NPCInit(merchant.UnitID); err != nil {
		ctx.Logger.Warn("Reopen shop NPCInit failed", "merchantGID", merchant.UnitID, "error", err)
		return
	}
	utils.PingSleep(utils.Light, 250)
	if err := ctx.PacketSender.NPCTradeFor(uint32(merchant.Name), merchant.UnitID); err != nil {
		ctx.Logger.Warn("Reopen shop trade action failed", "merchantGID", merchant.UnitID, "npc", merchant.Name, "error", err)
		return
	}
	utils.PingSleep(utils.Medium, 700)
	ctx.RefreshGameData()
	if !ctx.Data.OpenMenus.NPCShop {
		ctx.Logger.Warn("Reopen shop packet sequence did not restore NPCShop", "merchantGID", merchant.UnitID, "npc", merchant.Name)
	}
}

func sellDispatchTerm(i data.Item) byte {
	if i.IsPotion() || i.Name == item.ScrollOfTownPortal || i.Name == item.ScrollOfIdentify || i.Name == item.Key || i.Name == item.TomeOfTownPortal || i.Name == item.TomeOfIdentify {
		return packet.NPCSellTermConsumable
	}
	return packet.NPCSellTermEquipment
}

type vendorSellSlot struct {
	x              uint16
	y              uint16
	tab            byte
	targetLocation byte
	transaction    byte
	w              int
	h              int
}

type vendorSellPageGrid struct {
	occupied [ambVendorGridH][ambVendorGridW]bool
}

type vendorSellGridCache struct {
	valid         bool
	merchantID    data.UnitID
	baseItemCount int
	localAdds     int
	pages         map[byte]*vendorSellPageGrid
	namePages     map[item.Name][]byte
	typePages     map[string][]byte
	familyPages   map[string][]byte
	allPages      []byte
}

type vendorSellCategory string

const (
	vendorSellCategoryArmor  vendorSellCategory = "armor"
	vendorSellCategoryWeapon vendorSellCategory = "weapon"
	vendorSellCategoryMisc   vendorSellCategory = "misc"
)

var (
	vendorSellCacheMu    sync.Mutex
	vendorSellCacheState vendorSellGridCache
)

func sellItemPacketAllocated(ctx *context.Status, i data.Item, merchant data.Monster, sellPrice uint32, stage string) bool {
	if vendorSellUsesNoBuyback(i) {
		ctx.Logger.Debug("Selling consumable via no-buyback vendor packet", "stage", stage, "item", i.Name, "itemGID", i.UnitID, "sellPrice", sellPrice, "merchantGID", merchant.UnitID, "fromX", i.Position.X, "fromY", i.Position.Y, "targetTab", 0xFF)
		if err := ctx.PacketSender.NPCSellAMB(i, merchant, sellPrice, 0, 0, 0xFF, 0, 0); err != nil {
			ctx.Logger.Warn("No-buyback sell packet failed", "stage", stage, "error", err, "item", i.Name, "itemGID", i.UnitID, "sellPrice", sellPrice)
			return false
		}
		if waitForItemSold(ctx, i, sellPrice, stage, "ff-no-buyback") {
			return true
		}
		ctx.Logger.Warn("No-buyback sell packet had no effect", "stage", stage, "item", i.Name, "itemGID", i.UnitID, "sellPrice", sellPrice)
		return false
	}

	slot, ok := findVendorSellSlot(ctx, merchant.UnitID, i)
	if !ok {
		ctx.Logger.Warn("Vendor sell allocator found no native slot; refusing guessed sell packet", "stage", stage, "item", i.Name, "itemGID", i.UnitID, "itemW", i.Desc().InventoryWidth, "itemH", i.Desc().InventoryHeight)
		return false
	}

	ctx.Logger.Debug("Selling via allocated vendor slot", "stage", stage, "item", i.Name, "itemGID", i.UnitID, "sellPrice", sellPrice, "merchantGID", merchant.UnitID, "fromX", i.Position.X, "fromY", i.Position.Y, "toX", slot.x, "toY", slot.y, "targetTab", slot.tab, "targetLocation", slot.targetLocation, "transactionMode", slot.transaction)
	if err := ctx.PacketSender.NPCSellAMB(i, merchant, sellPrice, slot.x, slot.y, slot.tab, slot.targetLocation, slot.transaction); err != nil {
		ctx.Logger.Warn("Allocated sell packet failed", "stage", stage, "error", err, "item", i.Name, "itemGID", i.UnitID, "sellPrice", sellPrice)
		return false
	}
	if waitForItemSold(ctx, i, sellPrice, stage, "allocated") {
		markVendorSellSlotOccupied(merchant.UnitID, i, slot)
		return true
	}

	invalidateVendorSellCache(merchant.UnitID)
	ctx.RefreshGameData()
	ctx.Logger.Warn("Allocated sell packet had no effect; no fallback packets sent", "stage", stage, "item", i.Name, "itemGID", i.UnitID, "sellPrice", sellPrice, "toX", slot.x, "toY", slot.y, "targetTab", slot.tab)
	return false
}

func vendorSellUsesNoBuyback(i data.Item) bool {
	return sellDispatchTerm(i) == packet.NPCSellTermConsumable
}

func sellItemBurstSlots(ctx *context.Status, i data.Item, merchant data.Monster, sellPrice uint32, stage string, slots []vendorSellSlot) bool {
	if len(slots) == 0 {
		return false
	}
	ctx.Logger.Debug("Allocated sell left item present; bursting vendor slot probes", "stage", stage, "item", i.Name, "itemGID", i.UnitID, "count", len(slots))
	sent := 0
	for idx, altSlot := range slots {
		if err := ctx.PacketSender.NPCSellAMB(i, merchant, sellPrice, altSlot.x, altSlot.y, altSlot.tab, altSlot.targetLocation, altSlot.transaction); err != nil {
			ctx.Logger.Warn("Burst sell packet failed", "stage", stage, "error", err, "item", i.Name, "itemGID", i.UnitID, "sellPrice", sellPrice, "attempt", idx+1, "toX", altSlot.x, "toY", altSlot.y, "targetTab", altSlot.tab)
			continue
		}
		sent++
	}
	if sent == 0 {
		return false
	}
	return waitForItemSold(ctx, i, sellPrice, stage, "burst")
}

func sellItemNoBuybackFallback(ctx *context.Status, i data.Item, merchant data.Monster, sellPrice uint32, stage string, tried []vendorSellSlot) bool {
	type coord struct {
		x uint16
		y uint16
	}
	coords := make([]coord, 0, 6)
	add := func(x, y uint16) {
		for _, c := range coords {
			if c.x == x && c.y == y {
				return
			}
		}
		coords = append(coords, coord{x: x, y: y})
	}
	add(0, 0)
	add(uint16(i.Position.X), uint16(i.Position.Y))
	for _, slot := range tried {
		add(slot.x, slot.y)
		if len(coords) >= 6 {
			break
		}
	}

	for idx, c := range coords {
		ctx.Logger.Debug("Trying no-buyback vendor sell fallback", "stage", stage, "item", i.Name, "itemGID", i.UnitID, "attempt", idx+1, "toX", c.x, "toY", c.y, "targetTab", 0xFF)
		if err := ctx.PacketSender.NPCSellAMB(i, merchant, sellPrice, c.x, c.y, 0xFF, 0, 0); err != nil {
			ctx.Logger.Warn("No-buyback sell fallback failed to send", "stage", stage, "item", i.Name, "itemGID", i.UnitID, "sellPrice", sellPrice, "toX", c.x, "toY", c.y, "error", err)
			continue
		}
		if waitForItemSoldQuick(ctx, i, sellPrice, stage, "ff-no-buyback") {
			return true
		}
	}
	return false
}

func waitForItemSold(ctx *context.Status, i data.Item, sellPrice uint32, stage, variant string) bool {
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			utils.PingSleep(utils.Light, 80)
		}
		ctx.RefreshGameData()
		if !itemStillPlayerHeld(ctx, i.UnitID) {
			ctx.Logger.Info("Sell packet OK - item gone", "stage", stage, "variant", variant, "item", i.Name, "itemGID", i.UnitID, "sellPrice", sellPrice)
			return true
		}
	}
	return false
}

func waitForItemSoldQuick(ctx *context.Status, i data.Item, sellPrice uint32, stage, variant string) bool {
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			utils.PingSleep(utils.Light, 50)
		}
		ctx.RefreshGameData()
		if !itemStillPlayerHeld(ctx, i.UnitID) {
			ctx.Logger.Info("Sell packet OK - item gone", "stage", stage, "variant", variant, "item", i.Name, "itemGID", i.UnitID, "sellPrice", sellPrice)
			return true
		}
	}
	return false
}

func findVendorSellSlot(ctx *context.Status, merchantID data.UnitID, i data.Item) (vendorSellSlot, bool) {
	w, h := i.Desc().InventoryWidth, i.Desc().InventoryHeight
	if w <= 0 || h <= 0 || w > ambVendorGridW || h > ambVendorGridH {
		return vendorSellSlot{}, false
	}
	snap, ok := vendorSellSnapshot(ctx, merchantID)
	if !ok {
		return vendorSellSlot{}, false
	}
	pages := vendorSellCandidatePagesFromSnapshot(snap, i)
	if os.Getenv("VENDOR_NATIVE_SHOPROOT") == "1" {
		if x, y, tab, ok := ctx.GameReader.NativeVendorSellSlot(i, vendorVisibleItems(ctx), pages); ok {
			return vendorSellSlot{x: x, y: y, tab: tab, w: w, h: h}, true
		}
	}
	for _, page := range pages {
		if x, y, ok := firstFreeVendorCellFromSnapshot(snap, page, w, h); ok {
			ctx.Logger.Debug("Using live visible vendor snapshot slot", "item", i.Name, "itemGID", i.UnitID, "page", page, "x", x, "y", y, "w", w, "h", h)
			return vendorSellSlot{x: uint16(x), y: uint16(y), tab: page, w: w, h: h}, true
		}
	}
	return vendorSellSlot{}, false
}

func findVendorSellAppendSlots(ctx *context.Status, merchantID data.UnitID, i data.Item, skip vendorSellSlot, limit int) []vendorSellSlot {
	w, h := i.Desc().InventoryWidth, i.Desc().InventoryHeight
	if w <= 0 || h <= 0 || w > ambVendorGridW || h > ambVendorGridH || limit <= 0 {
		return nil
	}
	snap, ok := vendorSellSnapshot(ctx, merchantID)
	if !ok {
		return nil
	}
	slots := make([]vendorSellSlot, 0, limit)
	pages := vendorSellCandidatePagesFromSnapshot(snap, i)
	perPageLimit := vendorSellAppendPerPageLimit(limit, len(pages))
	for _, page := range pages {
		pageCount := 0
		for _, cell := range appendVendorCellsFromSnapshot(snap, page, w, h) {
			slot := vendorSellSlot{x: uint16(cell.x), y: uint16(cell.y), tab: page, w: w, h: h}
			if slot.x == skip.x && slot.y == skip.y && slot.tab == skip.tab {
				continue
			}
			slots = append(slots, slot)
			pageCount++
			if len(slots) >= limit {
				return slots
			}
			if pageCount >= perPageLimit {
				break
			}
		}
	}
	return slots
}

func vendorSellAppendPerPageLimit(limit, pageCount int) int {
	if limit <= 0 {
		return 0
	}
	if pageCount <= 1 {
		return limit
	}
	perPageLimit := (limit + pageCount - 1) / pageCount
	if perPageLimit < ambVendorGridW*ambVendorGridH {
		return ambVendorGridW * ambVendorGridH
	}
	return perPageLimit
}

func vendorSellSnapshot(ctx *context.Status, merchantID data.UnitID) (vendorSellGridCache, bool) {
	if ctx == nil || ctx.GameReader == nil || ctx.GameReader.Process == nil || !ctx.Data.OpenMenus.NPCShop {
		return vendorSellGridCache{}, false
	}
	vendorItems := vendorVisibleItems(ctx)
	currentCount := len(vendorItems)

	vendorSellCacheMu.Lock()
	defer vendorSellCacheMu.Unlock()
	if vendorSellCacheState.valid &&
		vendorSellCacheState.merchantID == merchantID &&
		currentCount == vendorSellCacheState.baseItemCount+vendorSellCacheState.localAdds {
		return vendorSellCacheState, true
	}

	snap := vendorSellGridCache{
		valid:         true,
		merchantID:    merchantID,
		baseItemCount: currentCount,
		pages:         make(map[byte]*vendorSellPageGrid),
		namePages:     make(map[item.Name][]byte),
		typePages:     make(map[string][]byte),
		familyPages:   make(map[string][]byte),
	}
	for _, vendorItem := range vendorItems {
		page, ok := vendorItemPage(ctx, vendorItem)
		if !ok {
			continue
		}
		grid := snap.pages[page]
		if grid == nil {
			grid = &vendorSellPageGrid{}
			snap.pages[page] = grid
			snap.allPages = addVendorSellPage(snap.allPages, page)
		}
		vw, vh := vendorItem.Desc().InventoryWidth, vendorItem.Desc().InventoryHeight
		markVendorSellGrid(grid, vendorItem.Position.X, vendorItem.Position.Y, vw, vh)
		snap.namePages[vendorItem.Name] = addVendorSellPage(snap.namePages[vendorItem.Name], page)
		itemType := vendorItem.Type().Code
		snap.typePages[itemType] = addVendorSellPage(snap.typePages[itemType], page)
		category := vendorSellItemCategory(vendorItem)
		snap.familyPages[string(category)] = addVendorSellPage(snap.familyPages[string(category)], page)
	}
	vendorSellCacheState = snap
	return snap, true
}

func vendorSellCandidatePagesFromSnapshot(snap vendorSellGridCache, i data.Item) []byte {
	pages := make([]byte, 0, 4)
	add := func(page byte) {
		if !slices.Contains(pages, page) {
			pages = append(pages, page)
		}
	}

	for _, page := range sortedVendorPages(snap.namePages[i.Name]) {
		add(page)
	}
	category := vendorSellItemCategory(i)
	itemType := i.Type().Code

	for _, page := range sortedVendorPages(snap.typePages[itemType]) {
		add(page)
	}
	for _, page := range sortedVendorPages(snap.familyPages[string(category)]) {
		add(page)
	}

	switch category {
	case "misc":
		add(3)
	case "weapon":
		add(1)
		add(2)
	case "armor":
		add(0)
	default:
		add(0)
		add(1)
		add(2)
		add(3)
	}
	for _, page := range sortedVendorPages(snap.allPages) {
		add(page)
	}
	add(0)
	add(1)
	add(2)
	add(3)
	return pages
}

func firstFreeVendorCellFromSnapshot(snap vendorSellGridCache, page byte, w, h int) (int, int, bool) {
	grid := snap.pages[page]
	if grid == nil {
		grid = &vendorSellPageGrid{}
	}
	cells := appendNativeVendorCellsFromGrid(grid, w, h, true)
	if len(cells) == 0 {
		return 0, 0, false
	}
	return cells[0].x, cells[0].y, true
}

type vendorSellCell struct {
	x int
	y int
}

func appendVendorCellsFromSnapshot(snap vendorSellGridCache, page byte, w, h int) []vendorSellCell {
	grid := snap.pages[page]
	if grid == nil {
		grid = &vendorSellPageGrid{}
	}
	return appendNativeVendorCellsFromGrid(grid, w, h, false)
}

func appendNativeVendorCellsFromGrid(grid *vendorSellPageGrid, w, h int, firstOnly bool) []vendorSellCell {
	cells := make([]vendorSellCell, 0, ambVendorGridW*ambVendorGridH)

	// Recovered from D2R shop_grid_find_single_row_slot: height-1 items scan
	// from the right edge, then downward.
	if h == 1 {
		for x := ambVendorGridW - w; x >= 0; x-- {
			for y := 0; y <= ambVendorGridH-h; y++ {
				if vendorCellFree(grid, x, y, w, h) {
					cells = append(cells, vendorSellCell{x: x, y: y})
					if firstOnly {
						return cells
					}
				}
			}
		}
		return cells
	}

	// Current vendor roots observed live use the native column-major first-fit
	// path for non-single-row items.
	for x := 0; x <= ambVendorGridW-w; x++ {
		for y := 0; y <= ambVendorGridH-h; y++ {
			if vendorCellFree(grid, x, y, w, h) {
				cells = append(cells, vendorSellCell{x: x, y: y})
				if firstOnly {
					return cells
				}
			}
		}
	}
	return cells
}

func vendorCellFree(grid *vendorSellPageGrid, x, y, w, h int) bool {
	for dy := 0; dy < h; dy++ {
		for dx := 0; dx < w; dx++ {
			if grid.occupied[y+dy][x+dx] {
				return false
			}
		}
	}
	return true
}

func markVendorSellSlotOccupied(merchantID data.UnitID, i data.Item, slot vendorSellSlot) {
	vendorSellCacheMu.Lock()
	defer vendorSellCacheMu.Unlock()
	if !vendorSellCacheState.valid || vendorSellCacheState.merchantID != merchantID {
		return
	}
	if vendorSellCacheState.pages == nil {
		vendorSellCacheState.pages = make(map[byte]*vendorSellPageGrid)
	}
	if vendorSellCacheState.namePages == nil {
		vendorSellCacheState.namePages = make(map[item.Name][]byte)
	}
	if vendorSellCacheState.typePages == nil {
		vendorSellCacheState.typePages = make(map[string][]byte)
	}
	if vendorSellCacheState.familyPages == nil {
		vendorSellCacheState.familyPages = make(map[string][]byte)
	}
	grid := vendorSellCacheState.pages[slot.tab]
	if grid == nil {
		grid = &vendorSellPageGrid{}
		vendorSellCacheState.pages[slot.tab] = grid
		vendorSellCacheState.allPages = addVendorSellPage(vendorSellCacheState.allPages, slot.tab)
	}
	markVendorSellGrid(grid, int(slot.x), int(slot.y), slot.w, slot.h)
	vendorSellCacheState.namePages[i.Name] = addVendorSellPage(vendorSellCacheState.namePages[i.Name], slot.tab)
	vendorSellCacheState.typePages[i.Type().Code] = addVendorSellPage(vendorSellCacheState.typePages[i.Type().Code], slot.tab)
	category := vendorSellItemCategory(i)
	vendorSellCacheState.familyPages[string(category)] = addVendorSellPage(vendorSellCacheState.familyPages[string(category)], slot.tab)
	vendorSellCacheState.localAdds++
}

func invalidateVendorSellCache(merchantID data.UnitID) {
	vendorSellCacheMu.Lock()
	defer vendorSellCacheMu.Unlock()
	if vendorSellCacheState.merchantID == merchantID {
		vendorSellCacheState = vendorSellGridCache{}
	}
}

func vendorVisibleItems(ctx *context.Status) []data.Item {
	items := make([]data.Item, 0, len(ctx.Data.Inventory.AllItems))
	for _, vendorItem := range ctx.Data.Inventory.AllItems {
		if isVendorVisibleLocation(vendorItem.Location.LocationType) {
			items = append(items, vendorItem)
		}
	}
	return items
}

func markVendorSellGrid(grid *vendorSellPageGrid, x, y, w, h int) {
	for yy := y; yy < y+h && yy < ambVendorGridH; yy++ {
		for xx := x; xx < x+w && xx < ambVendorGridW; xx++ {
			if xx >= 0 && yy >= 0 {
				grid.occupied[yy][xx] = true
			}
		}
	}
}

func addVendorSellPage(pages []byte, page byte) []byte {
	if slices.Contains(pages, page) {
		return pages
	}
	return append(pages, page)
}

func sortedVendorPages(pages []byte) []byte {
	if len(pages) < 2 {
		return pages
	}
	out := slices.Clone(pages)
	slices.Sort(out)
	return out
}

func vendorItemPage(ctx *context.Status, i data.Item) (byte, bool) {
	if ctx == nil || ctx.GameReader == nil || ctx.GameReader.Process == nil || i.UnitDataPtr == 0 {
		return 0, false
	}
	return byte(ctx.GameReader.Process.ReadUInt(i.UnitDataPtr+0x55, memory.Uint8)), true
}

func isVendorVisibleLocation(loc item.LocationType) bool {
	return loc == item.LocationVendor ||
		loc == item.LocationMaterialsTab ||
		loc == item.LocationGemsTab ||
		loc == item.LocationRunesTab
}

func vendorSellItemCategory(i data.Item) vendorSellCategory {
	switch i.Type().Code {
	case item.TypeArmor,
		item.TypeShield,
		item.TypeHelm,
		item.TypeBelt,
		item.TypeBoots,
		item.TypeGloves,
		item.TypeCirclet,
		item.TypePrimalHelm,
		item.TypePelt,
		item.TypeCloak,
		item.TypeAnyArmor,
		item.TypeAnyShield,
		item.TypeVoodooHeads,
		item.TypeAuricShields:
		return vendorSellCategoryArmor

	case item.TypeScepter,
		item.TypeWand,
		item.TypeStaff,
		item.TypeBow,
		item.TypeAxe,
		item.TypeClub,
		item.TypeSword,
		item.TypeHammer,
		item.TypeKnife,
		item.TypeSpear,
		item.TypePolearm,
		item.TypeCrossbow,
		item.TypeMace,
		item.TypeThrowingKnife,
		item.TypeThrowingAxe,
		item.TypeJavelin,
		item.TypeWeapon,
		item.TypeMeleeWeapon,
		item.TypeMissileWeapon,
		item.TypeThrownWeapon,
		item.TypeComboWeapon,
		item.TypeStavesAndRods,
		item.TypeBlunt,
		item.TypeClassSpecific,
		item.TypeAmazonItem,
		item.TypeBarbarianItem,
		item.TypeNecromancerItem,
		item.TypePaladinItem,
		item.TypeSorceressItem,
		item.TypeAssassinItem,
		item.TypeDruidItem,
		item.TypeWarlockItem,
		item.TypeHandtoHand,
		item.TypeHandtoHand2,
		item.TypeOrb,
		item.TypeAmazonBow,
		item.TypeAmazonSpear,
		item.TypeAmazonJavelin,
		item.TypeSwordsandKnives,
		item.TypeSpearsandPolearms,
		item.TypeGrimoire:
		return vendorSellCategoryWeapon

	default:
		return vendorSellCategoryMisc
	}
}

func itemStillPlayerHeld(ctx *context.Status, unitID data.UnitID) bool {
	if ctx == nil {
		return false
	}
	it, found := ctx.Data.Inventory.FindByID(unitID)
	if !found {
		return false
	}
	return it.Location.LocationType == item.LocationInventory || it.Location.LocationType == item.LocationCursor
}

type vendorPriceIntent string

const (
	vendorPriceBuy  vendorPriceIntent = "buy"
	vendorPriceSell vendorPriceIntent = "sell"
)

var vendorPriceNumber = regexp.MustCompile(`\d[\d,.]*`)

func vendorBuyPrice(ctx *context.Status, i data.Item) (uint32, bool) {
	return vendorTooltipPrice(ctx, i, vendorPriceBuy)
}

func vendorSellPrice(ctx *context.Status, i data.Item) (uint32, bool) {
	if price, ok := vendorNativeComputedSellPrice(ctx, i); ok {
		return price, true
	}
	return 0, false
}

func DebugVendorBuyPrice(i data.Item) (uint32, bool) {
	return vendorBuyPrice(context.Get(), i)
}

func DebugVendorSellPrice(i data.Item) (uint32, bool) {
	return vendorSellPrice(context.Get(), i)
}

func vendorMemorySellPrice(ctx *context.Status, i data.Item) (uint32, bool) {
	if ctx == nil || ctx.GameReader == nil || !ctx.Data.OpenMenus.NPCShop {
		return 0, false
	}
	merchantGID := findActiveMerchant(ctx)
	if merchantGID == 0 {
		return 0, false
	}
	probe, err := ctx.GameReader.VendorPriceProbeForItem(i, merchantGID, ctx.Data.PlayerUnit.ID)
	if err != nil {
		ctx.Logger.Debug("Vendor memory price unavailable", "item", i.Name, "itemGID", i.UnitID, "merchantGID", merchantGID, "error", err)
		return 0, false
	}
	if !probe.FormulaComplete || probe.UIContext.BestContextPtr == 0 {
		ctx.Logger.Debug("Vendor memory price rejected: no verified UI context", "item", i.Name, "itemGID", i.UnitID, "merchantGID", merchantGID, "mode6Source", probe.Mode6Source, "mode6Value", probe.Mode6Value, "priceA", probe.PriceA, "priceB", probe.PriceB)
		return 0, false
	}
	ctx.Logger.Debug("Vendor memory sell price resolved", "item", i.Name, "itemGID", i.UnitID, "merchantGID", merchantGID, "price", probe.PriceA, "mode6Source", probe.Mode6Source, "mode6Value", probe.Mode6Value, "uiContext", fmt.Sprintf("0x%X", probe.UIContext.BestContextPtr))
	return probe.PriceA, true
}

func vendorNativeComputedSellPrice(ctx *context.Status, i data.Item) (uint32, bool) {
	if ctx == nil || ctx.MemoryInjector == nil || ctx.GameReader == nil || !ctx.Data.OpenMenus.NPCShop || i.UnitPtr == 0 {
		return 0, false
	}
	pres := ctx.MemoryInjector.GetPresenter()
	if pres == nil {
		return 0, false
	}
	merchantGID := findActiveMerchant(ctx)
	if merchantGID == 0 {
		return 0, false
	}
	merchantPtr := ctx.GameReader.FindUnitPtrByID(1, merchantGID)
	if merchantPtr == 0 {
		ctx.Logger.Debug("Vendor native price unavailable: merchant unit pointer not found", "item", i.Name, "itemGID", i.UnitID, "merchantGID", merchantGID)
		return 0, false
	}
	playerPtr := uintptr(ctx.Data.PlayerUnit.Address)
	result, err := pres.VendorNativePriceMode(i.UnitPtr, merchantPtr, 1, -1, 0, -1, playerPtr)
	if err != nil {
		ctx.Logger.Debug("Vendor native price unavailable", "item", i.Name, "itemGID", i.UnitID, "merchantGID", merchantGID, "itemPtr", fmt.Sprintf("0x%X", i.UnitPtr), "merchantPtr", fmt.Sprintf("0x%X", merchantPtr), "error", err)
		return 0, false
	}
	if result.NativeCost == 0 {
		ctx.Logger.Debug("Vendor native price returned zero", "item", i.Name, "itemGID", i.UnitID, "merchantGID", merchantGID, "mode", result.Mode, "nativeCost", result.NativeCost, "value", result.Value, "rateA", result.RateA, "rateB", result.RateB, "modeValue", result.ModeValue, "stat5B", result.Stat5B, "txtID", result.TxtID, "difficulty", result.Difficulty, "record", fmt.Sprintf("0x%X", result.Record), "costCtx", fmt.Sprintf("0x%X", result.CostCtx), "npcTxtID", result.NPCTxtID)
		return 0, false
	}
	ctx.Logger.Debug("Vendor native sell price resolved", "item", i.Name, "itemGID", i.UnitID, "merchantGID", merchantGID, "price", result.NativeCost, "mode", result.Mode, "costCtx", fmt.Sprintf("0x%X", result.CostCtx), "npcTxtID", result.NPCTxtID, "costFn", fmt.Sprintf("0x%X", result.CostFn), "oldPriceA", result.PriceA, "oldRateA", result.RateA)
	return result.NativeCost, true
}

func vendorNativeDryRunSellPrice(ctx *context.Status, i data.Item) (uint32, bool) {
	if ctx == nil || ctx.MemoryInjector == nil || ctx.GameReader == nil || !ctx.Data.OpenMenus.NPCShop {
		return 0, false
	}
	pres := ctx.MemoryInjector.GetPresenter()
	if pres == nil {
		return 0, false
	}
	merchantGID := findActiveMerchant(ctx)
	if merchantGID == 0 {
		return 0, false
	}
	if err := pres.CapHookSuppressVendor(true); err != nil {
		ctx.Logger.Debug("Vendor native dry-run unavailable", "item", i.Name, "itemGID", i.UnitID, "error", err)
		return 0, false
	}
	defer func() {
		if err := pres.CapHookSuppressVendor(false); err != nil {
			ctx.Logger.Warn("Vendor native dry-run: failed to disable send suppression", "error", err)
		}
	}()
	if err := pres.CapHookInstall(); err != nil {
		ctx.Logger.Warn("Vendor native dry-run: caphook install failed", "item", i.Name, "itemGID", i.UnitID, "error", err)
		return 0, false
	}
	_ = pres.CapHookDrain()

	for _, pos := range ui.GetScreenCoordCandidatesForItem(i) {
		if err := pres.NativeClick(int32(pos.X), int32(pos.Y), 1); err != nil {
			ctx.Logger.Warn("Vendor native dry-run: native click failed", "item", i.Name, "itemGID", i.UnitID, "clientX", pos.X, "clientY", pos.Y, "error", err)
			continue
		}
		for attempt := 0; attempt < 5; attempt++ {
			utils.PingSleep(utils.Light, 20)
			for _, e := range pres.CapHookDrain() {
				if price, ok := vendorDryRunPriceFromPacket(e.Payload, i.UnitID, merchantGID); ok {
					ctx.Logger.Debug("Vendor native dry-run price captured", "item", i.Name, "itemGID", i.UnitID, "merchantGID", merchantGID, "price", price, "packetSize", e.Size, "clientX", pos.X, "clientY", pos.Y)
					return price, true
				}
			}
		}
	}
	ctx.Logger.Warn("Vendor native dry-run: no matching 0x33 captured", "item", i.Name, "itemGID", i.UnitID, "merchantGID", merchantGID)
	return 0, false
}

func vendorDryRunPriceFromPacket(payload []byte, itemGID, merchantGID data.UnitID) (uint32, bool) {
	if len(payload) < 13 || payload[0] != packet.OpNPCSellItem {
		return 0, false
	}
	price := binary.LittleEndian.Uint32(payload[1:5])
	gotItem := data.UnitID(binary.LittleEndian.Uint32(payload[5:9]))
	gotMerchant := data.UnitID(binary.LittleEndian.Uint32(payload[9:13]))
	if price == 0 || gotItem != itemGID || gotMerchant != merchantGID {
		return 0, false
	}
	return price, true
}

func vendorTooltipPrice(ctx *context.Status, i data.Item, intent vendorPriceIntent) (uint32, bool) {
	if ctx == nil || ctx.GameReader == nil || ctx.MemoryInjector == nil {
		return 0, false
	}

	if ctx.PacketSender != nil {
		if err := ctx.PacketSender.RequestUnitUpdate(4, i.UnitID); err != nil {
			ctx.Logger.Debug("Vendor tooltip price: item unit update request failed", "item", i.Name, "itemGID", i.UnitID, "error", err)
		}
	}

	var lastTexts []string
	for _, pos := range ui.GetScreenCoordCandidatesForItem(i) {
		for _, method := range []string{"window"} {
			if err := vendorMoveHover(ctx, pos, method); err != nil {
				ctx.Logger.Debug("Vendor tooltip price: hover move failed", "item", i.Name, "itemGID", i.UnitID, "clientX", pos.X, "clientY", pos.Y, "method", method, "error", err)
				continue
			}
			if price, ok, texts := vendorReadTooltipPriceAfterHover(ctx, i, intent, 55); ok {
				return price, true
			} else if len(texts) > 0 {
				lastTexts = texts
			}
		}
	}

	ctx.Logger.Warn("Vendor tooltip price: no live D2R hover-confirmed labelled price", "item", i.Name, "itemGID", i.UnitID, "intent", string(intent), "hovered", ctx.Data.HoverData.IsHovered, "hoverGID", ctx.Data.HoverData.UnitID, "hoverType", ctx.Data.HoverData.UnitType, "texts", strings.Join(lastTexts, " | "))
	return 0, false
}

func vendorMoveHover(ctx *context.Status, pos data.Position, method string) error {
	screenX := ctx.GameReader.WindowLeftX + pos.X
	screenY := ctx.GameReader.WindowTopY + pos.Y
	switch method {
	case "queued":
		return ctx.MemoryInjector.PostMoveQueued(uintptr(ctx.GameReader.HWND), int32(pos.X), int32(pos.Y), int32(screenX), int32(screenY))
	case "window":
		return ctx.MemoryInjector.MoveCursorWindowMessages(uintptr(ctx.GameReader.HWND), int32(screenX), int32(screenY), int32(pos.X), int32(pos.Y))
	case "koolo":
		return ctx.MemoryInjector.MoveCursorMessages(uintptr(ctx.GameReader.HWND), int32(screenX), int32(screenY))
	default:
		return fmt.Errorf("unknown hover method %q", method)
	}
}

func vendorReadTooltipPriceAfterHover(ctx *context.Status, i data.Item, intent vendorPriceIntent, waitMs int) (uint32, bool, []string) {
	utils.PingSleep(utils.Light, waitMs)
	ctx.RefreshGameData()
	hoverMatches := vendorHoverMatches(ctx, i)
	texts := ctx.GameReader.VisiblePanelTexts()
	renderTexts := ctx.GameReader.TooltipRenderTextsFast()
	if hoverMatches {
		if price, ok := parseVendorTooltipPrice(renderTexts, intent); ok {
			ctx.Logger.Debug("Vendor tooltip price resolved from live hover render cache", "item", i.Name, "identifiedName", i.IdentifiedName, "itemGID", i.UnitID, "intent", string(intent), "price", price, "hoverGID", ctx.Data.HoverData.UnitID, "hoverType", ctx.Data.HoverData.UnitType)
			return price, true, texts
		}
		if price, ok := parseVendorTooltipPrice(texts, intent); ok {
			ctx.Logger.Debug("Vendor tooltip price resolved from live hover panel", "item", i.Name, "itemGID", i.UnitID, "intent", string(intent), "price", price, "hoverGID", ctx.Data.HoverData.UnitID, "hoverType", ctx.Data.HoverData.UnitType)
			return price, true, texts
		}
		if price, ok := vendorGoldAmountPanelPrice(ctx); ok {
			ctx.Logger.Debug("Vendor tooltip price resolved from live hover gold_amount panel", "item", i.Name, "itemGID", i.UnitID, "intent", string(intent), "price", price, "hoverGID", ctx.Data.HoverData.UnitID, "hoverType", ctx.Data.HoverData.UnitType)
			return price, true, texts
		}
	}
	if price, ok := parseVendorRenderCachePrice(renderTexts, i, intent); ok {
		ctx.Logger.Debug("Vendor tooltip price resolved from item-matched render cache", "item", i.Name, "identifiedName", i.IdentifiedName, "itemGID", i.UnitID, "intent", string(intent), "price", price, "hovered", ctx.Data.HoverData.IsHovered, "hoverGID", ctx.Data.HoverData.UnitID, "hoverType", ctx.Data.HoverData.UnitType)
		return price, true, texts
	}
	return 0, false, texts
}

func vendorHoverMatches(ctx *context.Status, i data.Item) bool {
	if ctx == nil {
		return false
	}
	if ctx.Data.HoverData.IsHovered && ctx.Data.HoverData.UnitType == 4 && ctx.Data.HoverData.UnitID == i.UnitID {
		return true
	}
	if hovered, found := ctx.Data.Inventory.FindByID(i.UnitID); found {
		return hovered.IsHovered
	}
	return false
}

func parseVendorRenderCachePrice(texts []string, i data.Item, intent vendorPriceIntent) (uint32, bool) {
	for _, text := range texts {
		if !isVendorPriceLabel(text, intent) {
			continue
		}
		if !renderCacheTextMatchesItem(text, i) {
			continue
		}
		if price, ok := vendorPriceNumberAfterLabel(text, intent); ok {
			return price, true
		}
	}
	return 0, false
}

func renderCacheTextMatchesItem(text string, i data.Item) bool {
	haystack := normalizeVendorPriceText(text)
	if haystack == "" {
		return false
	}
	strongCandidates := []string{
		i.IdentifiedName,
		string(i.RunewordName),
	}
	for _, candidate := range strongCandidates {
		needle := normalizeVendorPriceText(candidate)
		if len(needle) >= 4 && strings.Contains(haystack, needle) {
			return true
		}
	}
	switch i.Quality {
	case item.QualityUnique, item.QualitySet, item.QualityRare, item.QualityCrafted:
		return false
	}
	weakCandidates := []string{
		string(i.Name),
		i.Desc().Name,
	}
	for _, candidate := range weakCandidates {
		needle := normalizeVendorPriceText(candidate)
		if len(needle) >= 4 && strings.Contains(haystack, needle) {
			if i.Quality == item.QualityMagic && !renderCacheTextMatchesItemStats(haystack, i) {
				continue
			}
			return true
		}
	}
	return false
}

func renderCacheTextMatchesItemStats(haystack string, i data.Item) bool {
	for _, candidate := range vendorItemStatTextCandidates(i) {
		needle := normalizeVendorPriceText(candidate)
		if len(needle) >= 6 && strings.Contains(haystack, needle) {
			return true
		}
	}
	return false
}

func vendorItemStatTextCandidates(i data.Item) []string {
	out := make([]string, 0, len(i.Stats)+len(i.BaseStats))
	appendStat := func(st stat.Data) {
		if st.Value == 0 {
			return
		}
		layers, ok := stat.StatStringMap[int(st.ID)]
		if !ok {
			return
		}
		tmpl, ok := layers[st.Layer]
		if !ok || tmpl == "" || strings.Contains(tmpl, "????") {
			return
		}
		text := strings.Replace(tmpl, "#", strconv.Itoa(st.Value), 1)
		if strings.Contains(text, "#") {
			return
		}
		out = append(out, text)
	}
	for _, st := range i.Stats {
		appendStat(st)
	}
	for _, st := range i.BaseStats {
		appendStat(st)
	}
	return out
}

func normalizeVendorPriceText(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func vendorGoldAmountPanelPrice(ctx *context.Status) (uint32, bool) {
	if ctx == nil || ctx.GameReader == nil {
		return 0, false
	}
	panels := ctx.GameReader.ReadAllPanels()
	for _, p := range panels {
		if price, ok := vendorGoldAmountPanelPriceFromPanel(p); ok {
			return price, true
		}
	}
	return 0, false
}

func vendorGoldAmountPanelPriceFromPanel(p data.Panel) (uint32, bool) {
	if p.PanelVisible && p.PanelEnabled && p.PanelName == "gold_amount" && p.PanelParent == "PlayerInventoryExpansionLayout" {
		return firstPriceNumber(memory.GetText(p))
	}
	for _, child := range p.PanelChildren {
		if price, ok := vendorGoldAmountPanelPriceFromPanel(child); ok {
			return price, true
		}
	}
	return 0, false
}

func parseVendorTooltipPrice(texts []string, intent vendorPriceIntent) (uint32, bool) {
	for idx, text := range texts {
		if !isVendorPriceLabel(text, intent) {
			continue
		}
		if price, ok := vendorPriceNumberAfterLabel(text, intent); ok {
			return price, true
		}
		if price, ok := firstPriceNumber(text); ok {
			return price, true
		}
		for j := idx + 1; j < len(texts) && j <= idx+3; j++ {
			if price, ok := firstPriceNumber(texts[j]); ok {
				return price, true
			}
		}
	}
	return 0, false
}

func vendorPriceNumberAfterLabel(text string, intent vendorPriceIntent) (uint32, bool) {
	lower := strings.ToLower(text)
	var labels []string
	switch intent {
	case vendorPriceSell:
		labels = []string{"sell value", "sell price", "wartosc sprzed", "wartość sprzed", "sprzed"}
	case vendorPriceBuy:
		labels = []string{"cost", "buy price", "price", "koszt", "cena"}
	default:
		return 0, false
	}
	best := -1
	for _, label := range labels {
		if idx := strings.Index(lower, label); idx >= 0 && (best < 0 || idx < best) {
			best = idx + len(label)
		}
	}
	if best < 0 || best >= len(text) {
		return 0, false
	}
	return firstPriceNumber(text[best:])
}

func isVendorPriceLabel(text string, intent vendorPriceIntent) bool {
	s := strings.ToLower(strings.TrimSpace(text))
	switch intent {
	case vendorPriceSell:
		return strings.Contains(s, "sell value") ||
			strings.Contains(s, "sell price") ||
			strings.Contains(s, "sprzed") ||
			strings.Contains(s, "wartosc sprzed") ||
			strings.Contains(s, "wartość sprzed")
	case vendorPriceBuy:
		return strings.Contains(s, "cost") ||
			strings.Contains(s, "buy price") ||
			strings.Contains(s, "price") ||
			strings.Contains(s, "koszt") ||
			strings.Contains(s, "cena")
	default:
		return false
	}
}

func firstPriceNumber(text string) (uint32, bool) {
	raw := vendorPriceNumber.FindString(text)
	if raw == "" {
		return 0, false
	}
	raw = strings.NewReplacer(",", "", ".", "").Replace(raw)
	v, err := strconv.ParseUint(raw, 10, 32)
	if err != nil || v == 0 {
		return 0, false
	}
	return uint32(v), true
}

func vendorGameBaseCost(i data.Item) (int, bool) {
	desc := i.Desc()
	if desc.Code == "" || !prefersLiveMiscBaseCost(i) {
		return 0, false
	}
	ctx := context.Get()
	if ctx == nil || ctx.GameReader == nil {
		return 0, false
	}
	return ctx.GameReader.ItemBaseCostFromGameData(desc.Code)
}

func prefersLiveMiscBaseCost(i data.Item) bool {
	t := i.Type().Code
	return t == item.TypeHealingPotion ||
		t == item.TypeManaPotion ||
		t == item.TypeRejuvPotion ||
		t == item.TypeStaminaPotion ||
		t == item.TypeAntidotePotion ||
		t == item.TypeThawingPotion ||
		t == item.TypeBook ||
		t == item.TypeScroll ||
		t == item.TypeKey ||
		t == item.TypeRune ||
		strings.HasPrefix(t, "gem")
}

func vendorPriceReductionPercent() int {
	ctx := context.Get()
	if ctx == nil {
		return 0
	}
	if st, found := ctx.Data.PlayerUnit.Stats.FindStat(stat.ReducePrices, 0); found {
		return st.Value
	}
	if st, found := ctx.Data.PlayerUnit.BaseStats.FindStat(stat.ReducePrices, 0); found {
		return st.Value
	}
	return 0
}

// SellItemFullStack sells an entire stack of items. Same packet policy as SellItem.
func SellItemFullStack(i data.Item) {
	ctx := context.Get()

	if ctx.PacketSender != nil {
		if !ctx.Data.OpenMenus.NPCShop {
			ctx.Logger.Warn("Sell stack packet path: NPCShop is not open, skipping send", "item", i.Name, "itemGID", i.UnitID)
			return
		}
		merchantGID := findActiveMerchant(ctx)
		if merchantGID == 0 {
			ctx.Logger.Warn("Sell stack packet path: active merchant not found, skipping HID fallback", "item", i.Name, "itemGID", i.UnitID)
			return
		}
		merchantMonster, mFound := ctx.Data.Monsters.FindByID(merchantGID)
		if !mFound {
			ctx.Logger.Warn("Sell stack packet path: merchant monster not found, skipping HID fallback", "merchantGID", merchantGID, "item", i.Name, "itemGID", i.UnitID)
			return
		}
		sellPrice, priceKnown := vendorSellPrice(ctx, i)
		if !priceKnown {
			ctx.Logger.Warn("Sell stack packet path: native sell price unavailable, refusing guessed price", "item", i.Name, "itemGID", i.UnitID)
			return
		}
		if sellItemPacketAllocated(ctx, i, merchantMonster, sellPrice, "sell-stack") {
			return
		}
		ctx.Logger.Warn("Sell stack packet sent but item still present after refresh", "item", i.Name, "itemGID", i.UnitID, "sellPrice", sellPrice)
		return
	}
	ctx.Logger.Warn("Sell stack packet path unavailable: PacketSender nil, refusing HID fallback", "item", i.Name, "itemGID", i.UnitID)
}

func BuyItem(i data.Item, quantity int) {
	ctx := context.Get()

	if ctx.PacketSender != nil {
		if !ctx.Data.OpenMenus.NPCShop {
			ctx.Logger.Warn("Buy packet path: NPCShop is not open, skipping send", "item", i.Name, "itemGID", i.UnitID, "qty", quantity)
			return
		}
		merchantGID := findActiveMerchant(ctx)
		if merchantGID == 0 {
			ctx.Logger.Warn("Buy packet path: active merchant not found, skipping HID fallback", "item", i.Name, "itemGID", i.UnitID, "qty", quantity)
			return
		}
		merchantMonster, mFound := ctx.Data.Monsters.FindByID(merchantGID)
		if !mFound {
			ctx.Logger.Warn("Buy packet path: merchant monster not found, skipping HID fallback", "merchantGID", merchantGID, "item", i.Name, "itemGID", i.UnitID, "qty", quantity)
			return
		}
		price, priceKnown := vendorBuyPrice(ctx, i)
		if !priceKnown {
			ctx.Logger.Warn("Buy packet path: live tooltip price unavailable, refusing guessed price", "item", i.Name, "itemGID", i.UnitID)
			return
		}
		ctx.Logger.Debug("Buying via AMB 0x32 NPCBuy", "item", i.Name, "itemGID", i.UnitID, "price", price, "merchantGID", merchantGID, "qty", quantity)
		for k := 0; k < quantity; k++ {
			if err := ctx.PacketSender.NPCBuyAMB(
				i,
				merchantMonster,
				price,
				ambVendorToPosX,
				ambVendorToPosY,
				ambVendorBuySourceTab,
				ambVendorBuyTargetLocation,
				ambVendorBuyTransactionMode,
			); err != nil {
				ctx.Logger.Warn("Buy packet failed, skipping HID fallback", "error", err, "iteration", k, "item", i.Name, "itemGID", i.UnitID, "qty", quantity)
				return
			}
			utils.PingSleep(utils.Medium, 600)
		}
		return
	}
	ctx.Logger.Warn("Buy packet path unavailable: PacketSender nil, refusing HID fallback", "item", i.Name, "itemGID", i.UnitID, "qty", quantity)
}

// buyFullStack is for buying full stacks of items from a vendor (e.g., potions, scrolls, keys)
// For keys, currentKeysInInventory determines if a special double-click behavior is needed.
func buyFullStack(i data.Item, currentKeysInInventory int) {
	ctx := context.Get()
	if ctx.PacketSender != nil {
		if !ctx.Data.OpenMenus.NPCShop {
			ctx.Logger.Warn("Buy stack packet path: NPCShop is not open, skipping HID fallback", "item", i.Name, "itemGID", i.UnitID)
			return
		}
		merchantGID := findActiveMerchant(ctx)
		if merchantGID == 0 {
			ctx.Logger.Warn("Buy stack packet path: active merchant not found, skipping HID fallback", "item", i.Name, "itemGID", i.UnitID)
			return
		}
		merchantMonster, mFound := ctx.Data.Monsters.FindByID(merchantGID)
		if !mFound {
			ctx.Logger.Warn("Buy stack packet path: merchant monster not found by GID, skipping HID fallback", "merchantGID", merchantGID, "item", i.Name, "itemGID", i.UnitID)
			return
		}
		price, priceKnown := vendorBuyPrice(ctx, i)
		if !priceKnown {
			ctx.Logger.Warn("Buy stack packet path: live tooltip price unavailable, refusing guessed price", "item", i.Name, "itemGID", i.UnitID)
			return
		}
		ctx.Logger.Debug("Buying stack via AMB 0x32 NPCBuy", "item", i.Name, "itemGID", i.UnitID, "price", price, "merchantGID", merchantGID, "currentKeys", currentKeysInInventory)
		if err := ctx.PacketSender.NPCBuyAMB(
			i,
			merchantMonster,
			price,
			ambVendorToPosX,
			ambVendorToPosY,
			ambVendorBuySourceTab,
			ambVendorBuyTargetLocation,
			ambVendorBuyTransactionMode,
		); err != nil {
			ctx.Logger.Warn("Buy stack packet failed, skipping HID fallback", "error", err, "item", i.Name, "itemGID", i.UnitID)
			return
		}
		utils.PingSleep(utils.Medium, 600)
		return
	}
	ctx.Logger.Warn("Buy stack packet path unavailable: PacketSender nil, refusing HID fallback", "item", i.Name, "itemGID", i.UnitID, "currentKeys", currentKeysInInventory)
}

func ItemsToBeSold(lockConfig ...[][]int) (items []data.Item) {
	ctx := context.Get()
	_, portalTomeFound := ctx.Data.Inventory.Find(item.TomeOfTownPortal, item.LocationInventory)
	healingPotionCountToKeep := ctx.Data.ConfiguredInventoryPotionCount(data.HealingPotion)
	manaPotionCountToKeep := ctx.Data.ConfiguredInventoryPotionCount(data.ManaPotion)
	rejuvPotionCountToKeep := ctx.Data.ConfiguredInventoryPotionCount(data.RejuvenationPotion)

	var currentLockConfig [][]int
	if len(lockConfig) > 0 {
		currentLockConfig = lockConfig[0]
	} else {
		currentLockConfig = ctx.CharacterCfg.Inventory.InventoryLock
	}

	// Count ALL non-NIP jewels (stash + inventory) to determine how many we can keep
	totalNonNIPJewels := 0

	// Count in stash
	for _, stashed := range ctx.Data.Inventory.ByLocation(item.LocationStash, item.LocationSharedStash) {
		if string(stashed.Name) == "Jewel" {
			if _, res := ctx.CharacterCfg.Runtime.Rules.EvaluateAll(stashed); res != nip.RuleResultFullMatch {
				totalNonNIPJewels++
			}
		}
	}

	// Count in inventory
	for _, invItem := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
		if string(invItem.Name) == "Jewel" {
			if _, res := ctx.CharacterCfg.Runtime.Rules.EvaluateAll(invItem); res != nip.RuleResultFullMatch {
				totalNonNIPJewels++
			}
		}
	}

	ctx.Logger.Debug(fmt.Sprintf("Total non-NIP jewels (stash + inventory): %d, Configured limit: %d",
		totalNonNIPJewels, ctx.CharacterCfg.CubeRecipes.JewelsToKeep))

	// Determine whether any jewel-using recipes are enabled
	maxJewelsToKeep := ctx.CharacterCfg.CubeRecipes.JewelsToKeep
	craftingEnabled := false
	for _, r := range ctx.CharacterCfg.CubeRecipes.EnabledRecipes {
		if strings.HasPrefix(r, "Caster ") ||
			strings.HasPrefix(r, "Blood ") ||
			strings.HasPrefix(r, "Safety ") ||
			strings.HasPrefix(r, "Hitpower ") {
			craftingEnabled = true
			break
		}
	}

	// Track how many jewels we've decided to keep so far (starting with those in stash)
	jewelsKeptCount := totalNonNIPJewels
	// Now subtract inventory jewels as we'll re-evaluate them below
	for _, invItem := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
		if string(invItem.Name) == "Jewel" {
			if _, res := ctx.CharacterCfg.Runtime.Rules.EvaluateAll(invItem); res != nip.RuleResultFullMatch {
				jewelsKeptCount-- // We'll re-count them as we process
			}
		}
	}

	for _, itm := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
		// Check if the item is in a locked slot, and if so, skip it.
		if len(currentLockConfig) > itm.Position.Y && len(currentLockConfig[itm.Position.Y]) > itm.Position.X {
			if currentLockConfig[itm.Position.Y][itm.Position.X] == 0 {
				continue
			}
		}

		isQuestItem := slices.Contains(questItems, itm.Name)
		if itm.IsFromQuest() || isQuestItem {
			continue
		}

		if itm.Name == item.TomeOfTownPortal || itm.Name == item.TomeOfIdentify || itm.Name == item.Key || itm.Name == "WirtsLeg" {
			continue
		}

		//Don't sell scroll of town portal if tome isn't found
		if !portalTomeFound && itm.Name == item.ScrollOfTownPortal {
			continue
		}

		if itm.IsRuneword {
			continue
		}

		if _, result := ctx.CharacterCfg.Runtime.Rules.EvaluateAllIgnoreTiers(itm); result == nip.RuleResultFullMatch && !itm.IsPotion() {
			continue
		}

		// Handle jewels: keep up to the configured limit of non-NIP jewels
		if craftingEnabled && string(itm.Name) == "Jewel" {
			// Only consider jewels that are not covered by a NIP rule
			if _, res := ctx.CharacterCfg.Runtime.Rules.EvaluateAll(itm); res != nip.RuleResultFullMatch {
				if jewelsKeptCount < maxJewelsToKeep {
					jewelsKeptCount++ // Keep this jewel
					ctx.Logger.Debug(fmt.Sprintf("Keeping jewel #%d (under limit of %d)", jewelsKeptCount, maxJewelsToKeep))
					continue
				} else {
					ctx.Logger.Debug(fmt.Sprintf("Selling jewel - already at limit (%d/%d)", jewelsKeptCount, maxJewelsToKeep))
					// This jewel exceeds the limit, so it will be added to items to sell below
				}
			}
		}

		if itm.IsHealingPotion() {
			if healingPotionCountToKeep > 0 {
				healingPotionCountToKeep--
				continue
			}
		}

		if itm.IsManaPotion() {
			if manaPotionCountToKeep > 0 {
				manaPotionCountToKeep--
				continue
			}
		}

		if itm.IsRejuvPotion() {
			if rejuvPotionCountToKeep > 0 {
				rejuvPotionCountToKeep--
				continue
			}
		}

		if itm.Name == "StaminaPotion" && ctx.HealthManager.ShouldKeepStaminaPot() {
			continue
		}

		items = append(items, itm)
	}

	return
}
