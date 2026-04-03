package run

import (
	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/area"
	"local/internal/svc/internal/gamelib/data/quest"
	"local/internal/svc/internal/action"
	"local/internal/svc/internal/config"
	"local/internal/svc/internal/context"
)

type FlayerJungle struct {
	ctx *context.Status
}

func NewFlayerJungle() *FlayerJungle {
	return &FlayerJungle{
		ctx: context.Get(),
	}
}

func (a FlayerJungle) Name() string {
	return string(config.FlayerJungleRun)
}

func (a FlayerJungle) CheckConditions(parameters *RunParameters) SequencerResult {
	if IsQuestRun(parameters) {
		return SequencerError
	}
	if !a.ctx.Data.Quests[quest.Act2TheSevenTombs].Completed() {
		return SequencerSkip
	}
	return SequencerOk
}

func (a FlayerJungle) Run(parameters *RunParameters) error {
	// Use Waypoint to Flayer Jungle
	err := action.WayPoint(area.FlayerJungle)
	if err != nil {
		return err
	}

	// Clear Flayer Jungle
	return action.ClearCurrentLevel(true, data.MonsterAnyFilter())
}
