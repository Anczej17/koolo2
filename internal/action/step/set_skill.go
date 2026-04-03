package step

import (
	"local/internal/svc/internal/gamelib/data/skill"
	"local/internal/svc/internal/context"
)

func SetSkill(id skill.ID) {
	ctx := context.Get()
	ctx.SetLastStep("SetSkill")

	if kb, found := ctx.Data.KeyBindings.KeyBindingForSkill(id); found {
		if ctx.Data.PlayerUnit.RightSkill != id {
			ctx.HID.PressKeyBinding(kb)
		}
	}
}
