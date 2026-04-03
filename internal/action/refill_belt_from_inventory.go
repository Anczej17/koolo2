package action

import (
	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/action/step"
	"local/internal/svc/internal/context"
	"local/internal/svc/internal/game"
	"local/internal/svc/internal/ui"
	"local/internal/svc/internal/utils"
)

func RefillBeltFromInventory() error {
	defer step.CloseAllMenus()

	ctx := context.Get()
	ctx.Logger.Info("Refilling belt from inventory")

	healingPotions := ctx.Data.PotionsInInventory(data.HealingPotion)
	manaPotions := ctx.Data.PotionsInInventory(data.ManaPotion)
	rejuvPotions := ctx.Data.PotionsInInventory(data.RejuvenationPotion)

	missingHealingPotionCount := ctx.BeltManager.GetMissingCount(data.HealingPotion)
	missingManaPotionCount := ctx.BeltManager.GetMissingCount(data.ManaPotion)
	missingRejuvPotionCount := ctx.BeltManager.GetMissingCount(data.RejuvenationPotion)

	if !((missingHealingPotionCount > 0 && len(healingPotions) > 0) || (missingManaPotionCount > 0 && len(manaPotions) > 0) || (missingRejuvPotionCount > 0 && len(rejuvPotions) > 0)) {
		ctx.Logger.Debug("No need to refill belt from inventory")
		return nil
	}

	// Add slight delay before opening inventory
	utils.Sleep(200)

	if err := step.OpenInventory(); err != nil {
		return err
	}

	// Refill healing potions
	for i := 0; i < missingHealingPotionCount && i < len(healingPotions); i++ {
		putPotionInBelt(ctx, healingPotions[i])
	}

	// Refill mana potions
	for i := 0; i < missingManaPotionCount && i < len(manaPotions); i++ {
		putPotionInBelt(ctx, manaPotions[i])
	}

	// Refill rejuvenation potions
	for i := 0; i < missingRejuvPotionCount && i < len(rejuvPotions); i++ {
		putPotionInBelt(ctx, rejuvPotions[i])
	}

	ctx.Logger.Info("Belt refilled from inventory")
	err := step.CloseAllMenus()
	if err != nil {
		return err
	}

	// Add slight delay after closing inventory
	utils.Sleep(200)
	return nil

}

func putPotionInBelt(ctx *context.Status, potion data.Item) {
	screenPos := ui.GetScreenCoordsForItem(potion)
	ctx.HID.ClickWithModifier(game.LeftButton, screenPos.X, screenPos.Y, game.ShiftKey)
	utils.Sleep(150)
}
