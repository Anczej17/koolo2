package run

import (
	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/area"
	"local/internal/svc/internal/gamelib/data/npc"
	"local/internal/svc/internal/action"
	"local/internal/svc/internal/config"
	"local/internal/svc/internal/context"
	"local/internal/svc/internal/game"
)

type BoneAsh struct {
	ctx *context.Status
}

func NewBoneAsh() *BoneAsh {
	return &BoneAsh{
		ctx: context.Get(),
	}
}

func (b BoneAsh) Name() string {
	return string(config.BoneAshRun)
}

func (b BoneAsh) CheckConditions(parameters *RunParameters) SequencerResult {
	if IsQuestRun(parameters) {
		return SequencerError
	}
	return SequencerOk
}

func (b BoneAsh) Run(parameters *RunParameters) error {
	if err := action.WayPoint(area.InnerCloister); err != nil {
		return err
	}

	if err := action.MoveToArea(area.Cathedral); err != nil {
		return err
	}

	if err := action.MoveToCoords(data.Position{X: 20047, Y: 4898}); err != nil {
		b.ctx.Logger.Warn("Bone Ash run: failed moving to Bone Ash", "error", err)
	}

	if err := b.ctx.Char.KillMonsterSequence(func(d game.Data) (data.UnitID, bool) {
		if m, found := d.Monsters.FindOne(npc.BurningDeadMage, data.MonsterTypeSuperUnique); found {
			return m.UnitID, true
		}
		return 0, false
	}, nil); err != nil {
		return err
	}

	action.ItemPickup(30)

	return nil
}
