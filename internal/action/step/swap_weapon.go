package step

import (
	"time"

	"local/internal/svc/internal/context"
	"local/internal/svc/internal/gamelib/data/skill"
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
		ctx.PauseIfNotPriority()

		if time.Since(lastRun) < time.Millisecond*500 {
			continue
		}

		_, found := ctx.Data.PlayerUnit.Skills[skill.BattleOrders]
		if (toCTA && found) || (!toCTA && !found) {
			return nil
		}

		if ctx.PacketSender == nil {
			return nil
		}
		if err := ctx.PacketSender.SwapWeaponFromData(ctx.Data); err != nil {
			ctx.Logger.Warn("SwapWeapon key handler failed", "err", err)
		}
		utils.PingSleep(utils.Light, 150)

		lastRun = time.Now()
	}
}
