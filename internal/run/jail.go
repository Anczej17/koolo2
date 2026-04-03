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

type Jail struct {
	ctx *context.Status
}

func NewJail() *Jail {
	return &Jail{
		ctx: context.Get(),
	}
}

func (j Jail) Name() string {
	return string(config.JailRun)
}

func (j Jail) CheckConditions(parameters *RunParameters) SequencerResult {
	if IsQuestRun(parameters) {
		return SequencerError
	}
	return SequencerOk
}

func (j Jail) Run(parameters *RunParameters) error {
	if err := action.WayPoint(area.InnerCloister); err != nil {
		return err
	}

	if err := action.MoveToArea(area.JailLevel3); err != nil {
		return err
	}

	if err := action.MoveToArea(area.JailLevel2); err != nil {
		return err
	}

	if err := j.killPitspawn(); err != nil {
		return err
	}

	action.ItemPickup(30)

	if err := action.MoveToArea(area.JailLevel1); err != nil {
		return err
	}

	return action.MoveToArea(area.Barracks)
}

func (j Jail) killPitspawn() error {
	areaData, ok := j.ctx.Data.Areas[area.JailLevel2]
	if ok {
		if npcData, found := areaData.NPCs.FindOne(npc.Tainted); found && len(npcData.Positions) > 0 {
			if err := action.MoveToCoords(npcData.Positions[0]); err != nil {
				j.ctx.Logger.Warn("Jail run: failed moving to Pitspawn Fouldog", "error", err)
			}
		} else {
			j.ctx.Logger.Warn("Jail run: Pitspawn Fouldog position not found")
		}
	} else {
		j.ctx.Logger.Warn("Jail run: map data missing for Jail Level 2")
	}

	return j.ctx.Char.KillMonsterSequence(func(d game.Data) (data.UnitID, bool) {
		if m, found := d.Monsters.FindOne(npc.Tainted, data.MonsterTypeSuperUnique); found {
			return m.UnitID, true
		}
		return 0, false
	}, nil)
}
