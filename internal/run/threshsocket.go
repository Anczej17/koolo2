package run

import (
	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/area"
	"local/internal/svc/internal/gamelib/data/npc"
	"local/internal/svc/internal/gamelib/data/quest"
	"local/internal/svc/internal/action"
	"local/internal/svc/internal/config"
	"local/internal/svc/internal/context"
	"local/internal/svc/internal/game"
)

type Threshsocket struct {
	ctx *context.Status
}

func NewThreshsocket() *Threshsocket {
	return &Threshsocket{
		ctx: context.Get(),
	}
}

func (t Threshsocket) Name() string {
	return string(config.ThreshsocketRun)
}

func (t Threshsocket) CheckConditions(parameters *RunParameters) SequencerResult {
	if IsQuestRun(parameters) {
		return SequencerError
	}
	if !t.ctx.Data.Quests[quest.Act4TerrorsEnd].Completed() {
		return SequencerSkip
	}
	return SequencerOk
}

func (t Threshsocket) Run(parameters *RunParameters) error {

	// Use waypoint to crystalinepassage
	err := action.WayPoint(area.CrystallinePassage)
	if err != nil {
		return err
	}

	// Move to ArreatPlateau
	if err = action.MoveToArea(area.ArreatPlateau); err != nil {
		return err
	}

	// Kill Threshsocket
	if err := t.ctx.Char.KillMonsterSequence(func(d game.Data) (data.UnitID, bool) {
		if m, found := d.Monsters.FindOne(npc.BloodBringer, data.MonsterTypeSuperUnique); found {
			return m.UnitID, true
		}

		return 0, false
	}, nil); err != nil {
		return err
	}

	action.ItemPickup(30)
	return nil
}
