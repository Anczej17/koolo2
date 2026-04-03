package run

import (
	"fmt"

	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/area"
	"local/internal/svc/internal/gamelib/data/npc"
	"local/internal/svc/internal/action"
	"local/internal/svc/internal/action/step"
	"local/internal/svc/internal/config"
	"local/internal/svc/internal/context"
)

type UberDuriel struct {
	ctx *context.Status
}

func NewUberDuriel() *UberDuriel {
	return &UberDuriel{
		ctx: context.Get(),
	}
}

func (u UberDuriel) Name() string {
	return string(config.UberDurielRun)
}

func (u UberDuriel) CheckConditions(parameters *RunParameters) SequencerResult {
	if u.ctx.Data.PlayerUnit.Area != area.ForgottenSands {
		return SequencerSkip
	}
	return SequencerOk
}

func (u UberDuriel) Run(parameters *RunParameters) error {
	action.Buff()

	areaData := u.ctx.Data.Areas[area.ForgottenSands]
	uberDurielNPC, found := areaData.NPCs.FindOne(npc.UberDuriel)
	if found && len(uberDurielNPC.Positions) > 0 {
		if err := action.MoveToCoords(uberDurielNPC.Positions[0], step.WithIgnoreMonsters()); err != nil {
			u.ctx.Logger.Warn(fmt.Sprintf("Failed to teleport to area data position: %v", err))
		}
	}

	exploreFilter := func(m data.Monsters) []data.Monster {
		return []data.Monster{}
	}

	bossFound := false
	err := action.ClearCurrentLevelEx(false, exploreFilter, func() bool {
		if boss, found := u.ctx.Data.Monsters.FindOne(npc.UberDuriel, data.MonsterTypeUnique); found {
			if err := action.MoveToCoords(boss.Position, step.WithIgnoreMonsters()); err != nil {
				u.ctx.Logger.Warn(fmt.Sprintf("Failed to teleport to boss: %v", err))
			}
			bossFound = true
			return true
		}
		return false
	})
	if err != nil {
		return err
	}

	if !bossFound {
		u.ctx.Logger.Warn("Uber Duriel not found during exploration")
		return fmt.Errorf("UberDuriel not found during exploration")
	}

	u.ctx.Logger.Info("Found Uber Duriel, starting fight")
	if err := u.ctx.Char.KillUberDuriel(); err != nil {
		return err
	}

	action.ItemPickup(30)

	u.ctx.Logger.Info("Successfully killed Uber Duriel")
	return nil
}
