package run

import (
	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/area"
	"local/internal/svc/internal/gamelib/data/quest"
	"local/internal/svc/internal/action"
	"local/internal/svc/internal/config"
	"local/internal/svc/internal/context"
)

type ArachnidLair struct {
	ctx *context.Status
}

func NewArachnidLair() *ArachnidLair {
	return &ArachnidLair{
		ctx: context.Get(),
	}
}

func (a ArachnidLair) Name() string {
	return string(config.ArachnidLairRun)
}

func (a ArachnidLair) CheckConditions(parameters *RunParameters) SequencerResult {
	if IsQuestRun(parameters) {
		return SequencerError
	}
	if !a.ctx.Data.Quests[quest.Act2TheSevenTombs].Completed() {
		return SequencerSkip
	}
	return SequencerOk
}

func (a ArachnidLair) Run(parameters *RunParameters) error {
	filter := data.MonsterAnyFilter()
	if a.ctx.CharacterCfg.Game.ArachnidLair.FocusOnElitePacks {
		filter = data.MonsterEliteFilter()
	}

	err := action.WayPoint(area.SpiderForest)
	if err != nil {
		return err
	}

	err = action.MoveToArea(area.SpiderCave)
	if err != nil {
		return err
	}

	action.OpenTPIfLeader()

	// Clear ArachnidLair
	return action.ClearCurrentLevel(a.ctx.CharacterCfg.Game.ArachnidLair.OpenChests, filter)
}
