package run

import (
	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/koolo/internal/action"
	"github.com/hectorgimenez/koolo/internal/config"
	"github.com/hectorgimenez/koolo/internal/context"
)

type Act4 struct {
	ctx *context.Status
}

func NewAct4() *Act4 {
	return &Act4{
		ctx: context.Get(),
	}
}

func (a Act4) Name() string {
	return string(config.Act4Run)
}

func (a Act4) CheckConditions(parameters *RunParameters) SequencerResult {
	if IsQuestRun(parameters) {
		return SequencerError
	}
	return SequencerOk
}

func (a Act4) Run(parameters *RunParameters) error {
	cfg := a.ctx.CharacterCfg.Game.ActRuns.Act4

	monsterFilter := data.MonsterAnyFilter()
	if cfg.FocusOnElitePacks {
		monsterFilter = data.MonsterEliteFilter()
	}

	a.ctx.Logger.Info("Starting Act 4 full clear run")

	// Ensure we're in Act 4 town (Pandemonium Fortress)
	if a.ctx.Data.PlayerUnit.Area != area.ThePandemoniumFortress {
		a.ctx.Logger.Info("Not in Act 4 town, using waypoint to Pandemonium Fortress")
		if err := action.WayPoint(area.ThePandemoniumFortress); err != nil {
			return err
		}
	}

	// Try to use World Stone Shard if configured
	// If extraction fails (shard not in stash), skip run instead of crashing
	if cfg.UseWorldStoneShard {
		a.ctx.Logger.Info("Attempting to use World Stone Shard for Act 4")
		if err := action.UseWorldStoneShard(4); err != nil {
			a.ctx.Logger.Warn("Failed to use World Stone Shard - skipping to next run",
				"error", err.Error())
			return nil // Skip to next run
		}
	}

	// PRIORITY: Kill Diablo first (boosted by WSS)
	a.ctx.Logger.Info("Priority: River of Flame + Diablo (seals + boss, boosted by WSS)")
	if err := action.WayPoint(area.RiverOfFlame); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}

	// Now run Diablo (seals + boss)
	a.ctx.Logger.Info("Running Diablo (seals + boss, boosted)")
	diabloRun := NewDiablo()
	if err := diabloRun.Run(parameters); err != nil {
		return err
	}

	if err := action.ReturnTown(); err != nil {
		return err
	}

	// Segment 1: Outer Steppes + Plains of Despair
	a.ctx.Logger.Info("Segment 1: Outer Steppes + Plains of Despair")
	if err := action.ReturnTown(); err != nil {
		return err
	}
	// Walk from Pandemonium Fortress to Outer Steppes
	if err := action.MoveToArea(area.OuterSteppes); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.PlainsOfDespair); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.ReturnTown(); err != nil {
		return err
	}

	// Segment 2: City of the Damned
	a.ctx.Logger.Info("Segment 2: City of the Damned")
	if err := action.WayPoint(area.CityOfTheDamned); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	a.ctx.Logger.Info("Act 4 full clear completed")

	if cfg.PauseAfterRun {
		a.ctx.Logger.Info("Stopping supervisor after Act 4 run as configured")
		if err := action.ReturnTown(); err != nil {
			return err
		}
		a.ctx.StopSupervisor()
	}

	return nil
}
