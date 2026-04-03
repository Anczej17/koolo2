package run

import (
	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/area"
	"local/internal/svc/internal/gamelib/data/quest"
	"local/internal/svc/internal/action"
	"local/internal/svc/internal/config"
	"local/internal/svc/internal/context"
)

type AncientTunnels struct {
	ctx *context.Status
}

func NewAncientTunnels() *AncientTunnels {
	return &AncientTunnels{
		ctx: context.Get(),
	}
}

func (a AncientTunnels) Name() string {
	return string(config.AncientTunnelsRun)
}

func (a AncientTunnels) CheckConditions(parameters *RunParameters) SequencerResult {
	if IsQuestRun(parameters) {
		return SequencerError
	}
	if !a.ctx.Data.Quests[quest.Act1SistersToTheSlaughter].Completed() {
		return SequencerSkip
	}
	return SequencerStop
}

func (a AncientTunnels) Run(parameters *RunParameters) error {
	openChests := a.ctx.CharacterCfg.Game.AncientTunnels.OpenChests
	onlyElites := a.ctx.CharacterCfg.Game.AncientTunnels.FocusOnElitePacks
	filter := data.MonsterAnyFilter()

	if onlyElites {
		filter = data.MonsterEliteFilter()
	}

	err := action.WayPoint(area.LostCity) // Moving to starting point (Lost City)
	if err != nil {
		return err
	}

	err = action.MoveToArea(area.AncientTunnels) // Travel to ancient tunnels
	if err != nil {
		return err
	}
	action.OpenTPIfLeader()

	// Clear Ancient Tunnels

	return action.ClearCurrentLevel(openChests, filter)
}
