package step

import (
	"time"

	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/item"
	"local/internal/svc/internal/gamelib/data/skill"
	"local/internal/svc/internal/context"
	"local/internal/svc/internal/utils"
)

func SwapToMainWeapon() error {
	return swapWeapon(false)
}

func SwapToCTA() error {
	return swapWeapon(true)
}

func swapWeapon(toCTA bool) error {
	lastRun := time.Time{}

	ctx := context.Get()
	ctx.SetLastStep("SwapToCTA")

	for {
		// Pause the execution if the priority is not the same as the execution priority
		ctx.PauseIfNotPriority()

		if time.Since(lastRun) < time.Millisecond*500 {
			continue
		}

		_, found := ctx.Data.PlayerUnit.Skills[skill.BattleOrders]
		if (toCTA && found) || (!toCTA && !found) {
			return nil
		}

		swapped := false
		if ctx.CharacterCfg.PacketCasting.UseForWeaponSwap && ctx.PacketSender != nil {
			fL, fR, tL, tR := swapWeaponGIDs(ctx)
			if fL != 0 || fR != 0 || tL != 0 || tR != 0 {
				if err := ctx.PacketSender.SwapWeapon(fL, fR, tL, tR); err == nil {
					swapped = true
				}
			}
		}
		if !swapped {
			ctx.HID.PressKeyBinding(ctx.Data.KeyBindings.SwapWeapons)
		}
		utils.PingSleep(utils.Light, 150)

		lastRun = time.Now()
	}
}

func swapWeaponGIDs(ctx *context.Status) (fromL, fromR, toL, toR data.UnitID) {
	ctx.RefreshGameData()
	equipped := ctx.Data.Inventory.ByLocation(item.LocationEquipped)
	ctx.Logger.Debug("swapWeaponGIDs scanning equipped items", "count", len(equipped), "activeSlot", ctx.Data.ActiveWeaponSlot)
	for _, itm := range equipped {
		ctx.Logger.Debug("  equipped item", "name", itm.Name, "gid", itm.UnitID, "bodyLoc", itm.Location.BodyLocation, "x", itm.Position.X)
		switch itm.Location.BodyLocation {
		case item.LocLeftArm:
			if ctx.Data.ActiveWeaponSlot == 0 {
				fromL = itm.UnitID
			} else {
				toL = itm.UnitID
			}
		case item.LocRightArm:
			if ctx.Data.ActiveWeaponSlot == 0 {
				fromR = itm.UnitID
			} else {
				toR = itm.UnitID
			}
		case item.LocLeftArmSecondary:
			if ctx.Data.ActiveWeaponSlot == 0 {
				toL = itm.UnitID
			} else {
				fromL = itm.UnitID
			}
		case item.LocRightArmSecondary:
			if ctx.Data.ActiveWeaponSlot == 0 {
				toR = itm.UnitID
			} else {
				fromR = itm.UnitID
			}
		}
	}
	return
}
