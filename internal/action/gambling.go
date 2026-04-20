package action

import (
	"errors"
	"log/slog"
	"strings"

	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/item"
	"local/internal/svc/internal/gamelib/data/npc"
	"local/internal/svc/internal/gamelib/nip"
	"local/internal/svc/internal/action/step"
	"local/internal/svc/internal/context"
	"local/internal/svc/internal/game"
	"local/internal/svc/internal/town"
	"local/internal/svc/internal/ui"
	"local/internal/svc/internal/utils"
)

func Gamble() error {
	ctx := context.Get()
	ctx.SetLastAction("Gamble")

	if ctx.CharacterCfg.Gambling.Enabled && ctx.Data.PlayerUnit.TotalPlayerGold() >= 2480000 {
		ctx.Logger.Info("Time to gamble! Visiting vendor...")

		vendorNPC := town.GetTownByArea(ctx.Data.PlayerUnit.Area).GamblingNPC()

		// Fix for Anya position
		if vendorNPC == npc.Drehya {
			_ = MoveToCoords(data.Position{
				X: 5107,
				Y: 5119,
			})
		}

		InteractNPC(vendorNPC)
		SelectNPCGambleOption(vendorNPC)

		if !ctx.Data.OpenMenus.NPCShop {
			return errors.New("failed opening gambling window")
		}

		return gambleItems()
	}

	return nil
}

func GambleSingleItem(items []string, desiredQuality item.Quality) error {
	ctx := context.Get()
	ctx.SetLastAction("GambleSingleItem")

	charGold := ctx.Data.PlayerUnit.TotalPlayerGold()
	var itemBought data.Item

	// Check if we have enough gold to gamble
	if charGold >= 150000 {
		ctx.Logger.Info("Gambling for items", slog.Any("items", items))

		vendorNPC := town.GetTownByArea(ctx.Data.PlayerUnit.Area).GamblingNPC()

		// Fix for Anya position
		if vendorNPC == npc.Drehya {
			_ = MoveToCoords(data.Position{
				X: 5107,
				Y: 5119,
			})
		}

		InteractNPC(vendorNPC)
		SelectNPCGambleOption(vendorNPC)

		if !ctx.Data.OpenMenus.NPCShop {
			return errors.New("failed opening gambling window")
		}
	}

	const maxRefreshAttempts = 25
	refreshAttempts := 0

	for {
		if itemBought.Name != "" {
			for _, itm := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
				if itm.UnitID == itemBought.UnitID {
					itemBought = itm
					ctx.Logger.Debug("Gambled for item", slog.Any("item", itemBought))
					break
				}
			}

			// Check if the item matches our NIP rules
			if _, result := ctx.Data.CharacterCfg.Runtime.Rules.EvaluateAll(itemBought); result == nip.RuleResultFullMatch {
				// Filter not pass, selling the item
				ctx.Logger.Info("Found item matching nip rules, will be kept", slog.Any("item", itemBought))
				itemBought = data.Item{}
				continue
			} else {
				// Doesn't match NIP rules but check if the item matches our desired quality
				if itemBought.Quality == desiredQuality {
					ctx.Logger.Info("Found item matching desired quality, will be kept", slog.Any("item", itemBought))
					return step.CloseAllMenus()
				} else {
					town.SellItem(itemBought)
					itemBought = data.Item{}
				}
			}
		}

		if ctx.Data.PlayerUnit.TotalPlayerGold() < 150000 {
			return errors.New("gold is below 150000, stopping gamble")
		}

		// Check for any of the desired items in the vendor's inventory
		for _, itmName := range items {
			itm, found := ctx.Data.Inventory.Find(item.Name(itmName), item.LocationVendor)
			if found {
				town.BuyItem(itm, 1)
				itemBought = itm
				break
			}
		}

		// If no desired item was found, refresh the gambling window
		if itemBought.Name == "" {
			refreshAttempts++
			if refreshAttempts >= maxRefreshAttempts {
				ctx.Logger.Warn("GambleSingleItem: max refresh attempts reached, stopping",
					slog.Int("attempts", refreshAttempts),
					slog.Any("items", items))
				return step.CloseAllMenus()
			}

			ctx.Logger.Debug("Desired items not found in gambling window, refreshing...",
				slog.Any("items", items),
				slog.Int("attempt", refreshAttempts))

			gX, gY := ui.GambleRefreshButtonX, ui.GambleRefreshButtonY
			if ctx.Data.LegacyGraphics {
				gX, gY = ui.GambleRefreshButtonXClassic, ui.GambleRefreshButtonYClassic
			}
			clicked := false
			if ctx.CharacterCfg.PacketCasting.UseForGamble && ctx.PacketSender != nil {
				if err := ctx.PacketSender.ClickAt(int32(gX), int32(gY), game.MouseLeft); err == nil {
					clicked = true
				}
			}
			if !clicked {
				ctx.HID.Click(game.LeftButton, gX, gY)
			}

			utils.Sleep(500)
		} else {
			refreshAttempts = 0
		}
	}
}

func gambleItems() error {
	ctx := context.Get()
	ctx.SetLastAction("gambleItems")

	var itemBought data.Item
	var refreshAttempts int
	const maxRefreshAttempts = 11
	const maxPurchasesPerItem = 5
	const coronetCircletGroup = "coronet_circlet_group"

	isGroupItem := func(name string) bool {
		n := strings.ToLower(name)
		return n == "coronet" || n == "circlet"
	}

	getCounterKey := func(itemName string) string {
		if isGroupItem(itemName) {
			return coronetCircletGroup
		}
		return strings.ToLower(itemName)
	}

	purchaseCounters := make(map[string]int, len(ctx.Data.CharacterCfg.Gambling.Items))
	for _, itemName := range ctx.Data.CharacterCfg.Gambling.Items {
		purchaseCounters[getCounterKey(itemName)] = 0
	}

	checkAndResetCounters := func() {
		for _, itemName := range ctx.Data.CharacterCfg.Gambling.Items {
			if purchaseCounters[getCounterKey(itemName)] < maxPurchasesPerItem {
				return
			}
		}
		for key := range purchaseCounters {
			purchaseCounters[key] = 0
		}
	}

	for {
		ctx.PauseIfNotPriority()
		ctx.RefreshGameData()

		// Abort gambling immediately when bonus run is cancelled (party AllDone, leader new game, etc.)
		if ctx.AbortBonusRun.Load() {
			ctx.Logger.Info("Gambling aborted: bonus run cancelled")
			return step.CloseAllMenus()
		}

		if ctx.Data.PlayerUnit.TotalPlayerGold() < 500000 {
			ctx.Logger.Info("Finished gambling - gold below 500k",
				slog.Int("currentGold", ctx.Data.PlayerUnit.TotalPlayerGold()))
			return step.CloseAllMenus()
		}

		checkAndResetCounters()

		if itemBought.Name != "" {
			originalItemName := string(itemBought.Name)
			for _, itm := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
				if itm.UnitID == itemBought.UnitID {
					itemBought = itm
					ctx.Logger.Debug("Gambled for item", slog.Any("item", itemBought))
					break
				}
			}

			if _, result := ctx.Data.CharacterCfg.Runtime.Rules.EvaluateAll(itemBought); result == nip.RuleResultFullMatch {
				ctx.Logger.Info("Found item matching NIP rules, keeping", slog.Any("item", itemBought))
				return step.CloseAllMenus()
			} else {
				ctx.Logger.Debug("Item doesn't match NIP rules, selling", slog.Any("item", itemBought))
				town.SellItem(itemBought)
			}

			purchaseCounters[getCounterKey(originalItemName)]++
			itemBought = data.Item{}
			refreshAttempts = 0
			continue
		}

		var bestItem data.Item
		vendorItems := ctx.Data.Inventory.ByLocation(item.LocationVendor)

		for _, itemName := range ctx.Data.CharacterCfg.Gambling.Items {
			if purchaseCounters[getCounterKey(itemName)] >= maxPurchasesPerItem {
				continue
			}

			if isGroupItem(itemName) {
				for _, vendorItem := range vendorItems {
					if isGroupItem(string(vendorItem.Name)) {
						bestItem = vendorItem
						break
					}
				}
			} else {
				bestItem, _ = ctx.Data.Inventory.Find(item.Name(itemName), item.LocationVendor)
			}

			if bestItem.Name != "" {
				break
			}
		}

		if bestItem.Name != "" {
			town.BuyItem(bestItem, 1)
			itemBought = bestItem
		} else {
			refreshAttempts++
			if refreshAttempts >= maxRefreshAttempts {
				ctx.Logger.Info("Too many refresh attempts without finding items, reopening gambling window")
				if err := step.CloseAllMenus(); err != nil {
					return err
				}
				utils.Sleep(200)

				vendorNPC := town.GetTownByArea(ctx.Data.PlayerUnit.Area).GamblingNPC()
				if err := InteractNPC(vendorNPC); err != nil {
					return err
				}

				SelectNPCGambleOption(vendorNPC)

				refreshAttempts = 0
				continue
			}

			ctx.Logger.Debug("Refreshing.. ", slog.Int("Attempt", refreshAttempts))
			RefreshGamblingWindow(ctx)
			utils.Sleep(500)
		}
	}
}
func RefreshGamblingWindow(ctx *context.Status) {
	gX, gY := ui.GambleRefreshButtonX, ui.GambleRefreshButtonY
	if ctx.Data.LegacyGraphics {
		gX, gY = ui.GambleRefreshButtonXClassic, ui.GambleRefreshButtonYClassic
	}
	if ctx.CharacterCfg.PacketCasting.UseForGamble && ctx.PacketSender != nil {
		if err := ctx.PacketSender.ClickAt(int32(gX), int32(gY), game.MouseLeft); err == nil {
			return
		}
	}
	ctx.HID.Click(game.LeftButton, gX, gY)
}
