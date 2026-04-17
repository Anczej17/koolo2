package action

import (
	"local/internal/svc/internal/gamelib/data/item"
	"local/internal/svc/internal/gamelib/data/stat"
	"local/internal/svc/internal/action/step"
	botCtx "local/internal/svc/internal/context"
	"local/internal/svc/internal/town"
	"local/internal/svc/internal/utils"
)

func ReviveMerc() {
	status := botCtx.Get()

	status.SetLastAction("ReviveMerc") // SetLastAction is a method on Status

	if status.CharacterCfg.Character.UseMerc && status.Data.MercHPPercent() <= 0 && NeedsTPsToContinue(status.Context) {

		status.Logger.Info("Merc is dead, let's revive it!")

		mercNPC := town.GetTownByArea(status.Data.PlayerUnit.Area).MercContractorNPC()

		InteractNPC(mercNPC)

		// Resurrect option + close dialog
		SelectNPCTradeOption(mercNPC) // "resurrect" is same position as "trade" for merc NPCs
		utils.Sleep(200)
		step.CloseAllMenus()
	}
}

// NeedsTPsToContinue now correctly accepts *botCtx.Context and checks for at least 1 TP
func NeedsTPsToContinue(ctx *botCtx.Context) bool {
	portalTome, found := ctx.Data.Inventory.Find(item.TomeOfTownPortal, item.LocationInventory)
	if !found {
		return false // No portal tome found, so no TPs, can't go to town.
	}

	qty, found := portalTome.FindStat(stat.Quantity, 0)
	// If quantity stat isn't found, or if quantity is exactly 0, then we can't make a TP.
	return qty.Value > 0 && found
}
