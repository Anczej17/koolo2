package run

import (
	"errors"

	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/area"
	"local/internal/svc/internal/gamelib/data/npc"
	"local/internal/svc/internal/gamelib/data/quest"
	"local/internal/svc/internal/gamelib/data/stat"
	"local/internal/svc/internal/action"
	"local/internal/svc/internal/config"
	"local/internal/svc/internal/context"
	"local/internal/svc/internal/game"
)

type Eldritch struct {
	ctx *context.Status
}

func NewEldritch() *Eldritch {
	return &Eldritch{
		ctx: context.Get(),
	}
}

func (e Eldritch) Name() string {
	return string(config.EldritchRun)
}

func (a Eldritch) CheckConditions(parameters *RunParameters) SequencerResult {
	if IsQuestRun(parameters) {
		return SequencerError
	}
	if !a.ctx.Data.Quests[quest.Act4TerrorsEnd].Completed() {
		return SequencerSkip
	}
	return SequencerOk
}

func (e Eldritch) Run(parameters *RunParameters) error {
	// Travel to FrigidHighlands
	err := action.WayPoint(area.FrigidHighlands)
	if err != nil {
		return err
	}

	// Kill Eldritch
	e.ctx.Char.KillMonsterSequence(func(d game.Data) (data.UnitID, bool) {
		if m, found := d.Monsters.FindOne(npc.MinionExp, data.MonsterTypeSuperUnique); found {
			return m.UnitID, true
		}

		return 0, false
	}, nil)

	action.ItemPickup(30)

	// Move to Shenk and kill him, if enabled
	if e.ctx.CharacterCfg.Game.Eldritch.KillShenk {
		// Move into position
		if err = action.MoveToCoords(data.Position{X: 3876, Y: 5130}); err != nil {
			return errors.New("failed to move to shenk")
		}

		// Kill Shenk
		if err := e.ctx.Char.KillMonsterSequence(func(d game.Data) (data.UnitID, bool) {
			if m, found := d.Monsters.FindOne(npc.OverSeer, data.MonsterTypeSuperUnique); found {
				if m.Stats[stat.Life] > 0 {
					return m.UnitID, true
				}
				return 0, false
			}

			return 0, false
		}, nil); err != nil {
			return err
		}

		action.ItemPickup(30)
		return nil
	}

	return nil
}
