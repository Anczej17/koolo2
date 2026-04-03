package run

import (
	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/area"
	"local/internal/svc/internal/gamelib/data/quest"
	"local/internal/svc/internal/action"
	"local/internal/svc/internal/config"
	"local/internal/svc/internal/context"
)

type RiverOfFlame struct {
	ctx *context.Status
}

func NewRiverOfFlame() *RiverOfFlame {
	return &RiverOfFlame{
		ctx: context.Get(),
	}
}

func (a RiverOfFlame) Name() string {
	return string(config.RiverOfFlameRun)
}

func (a RiverOfFlame) CheckConditions(parameters *RunParameters) SequencerResult {
	if IsQuestRun(parameters) {
		return SequencerError
	}
	if !a.ctx.Data.Quests[quest.Act3TheGuardian].Completed() {
		return SequencerSkip
	}
	return SequencerOk
}

func (a RiverOfFlame) Run(parameters *RunParameters) error {
	// Use waypoint to City of the Damned
	if err := action.WayPoint(area.CityOfTheDamned); err != nil {
		return err
	}

	// Move to River of Flame
	if err := action.MoveToArea(area.RiverOfFlame); err != nil {
		return err
	}

	// Clear River of Flame
	return action.ClearCurrentLevel(true, data.MonsterAnyFilter())
}
