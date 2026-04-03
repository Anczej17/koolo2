package step

import (
	"errors"

	"local/internal/svc/internal/context"
	"local/internal/svc/internal/utils"
)

func OpenInventory() error {
	ctx := context.Get()
	ctx.SetLastStep("OpenInventory")

	attempts := 0
	for !ctx.Data.OpenMenus.Inventory {
		// Pause the execution if the priority is not the same as the execution priority
		ctx.PauseIfNotPriority()
		ctx.RefreshGameData()
		if attempts > 10 {
			return errors.New("failed opening inventory")
		}
		ctx.HID.PressKeyBinding(ctx.Data.KeyBindings.Inventory)
		utils.Sleep(200)
		attempts++
	}

	return nil
}
