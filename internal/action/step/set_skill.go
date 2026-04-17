package step

import (
	"local/internal/svc/internal/gamelib/data/skill"

	"local/internal/svc/internal/context"
)

func SetSkill(id skill.ID) {
	ctx := context.Get()
	ctx.SetLastStep("SetSkill")

	if ctx.Data.PlayerUnit.RightSkill != id {
		_ = SelectRightSkill(id)
	}
}
