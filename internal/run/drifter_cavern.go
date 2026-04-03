package run

import (
	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/area"
	"local/internal/svc/internal/gamelib/data/quest"
	"local/internal/svc/internal/action"
	"local/internal/svc/internal/config"
	"local/internal/svc/internal/context"
)

type DrifterCavern struct {
	ctx *context.Status
}

func NewDriverCavern() *DrifterCavern {
	return &DrifterCavern{
		ctx: context.Get(),
	}
}

func (s DrifterCavern) Name() string {
	return string(config.DrifterCavernRun)
}

func (a DrifterCavern) CheckConditions(parameters *RunParameters) SequencerResult {
	if IsQuestRun(parameters) {
		return SequencerError
	}
	if !a.ctx.Data.Quests[quest.Act4TerrorsEnd].Completed() {
		return SequencerSkip
	}
	return SequencerOk
}

func (s DrifterCavern) Run(parameters *RunParameters) error {
	// Define a default monster filter
	monsterFilter := data.MonsterAnyFilter()

	// Update filter if we selected to clear only elites
	if s.ctx.CharacterCfg.Game.DrifterCavern.FocusOnElitePacks {
		monsterFilter = data.MonsterEliteFilter()
	}

	// Use the waypoint
	err := action.WayPoint(area.GlacialTrail)
	if err != nil {
		return err
	}

	// Move to the correct area
	if err = action.MoveToArea(area.DrifterCavern); err != nil {
		return err
	}

	// Clear the area
	return action.ClearCurrentLevel(s.ctx.CharacterCfg.Game.DrifterCavern.OpenChests, monsterFilter)
}
