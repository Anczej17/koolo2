package run

import (
	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/koolo/internal/action"
	"github.com/hectorgimenez/koolo/internal/config"
	"github.com/hectorgimenez/koolo/internal/context"
)

type Act3 struct {
	ctx *context.Status
}

func NewAct3() *Act3 {
	return &Act3{
		ctx: context.Get(),
	}
}

func (a Act3) Name() string {
	return string(config.Act3Run)
}

func (a Act3) CheckConditions(parameters *RunParameters) SequencerResult {
	if IsQuestRun(parameters) {
		return SequencerError
	}
	return SequencerOk
}

func (a Act3) Run(parameters *RunParameters) error {
	cfg := a.ctx.CharacterCfg.Game.ActRuns.Act3

	monsterFilter := data.MonsterAnyFilter()
	if cfg.FocusOnElitePacks {
		monsterFilter = data.MonsterEliteFilter()
	}

	a.ctx.Logger.Info("Starting Act 3 full clear run")

	// Ensure we're in Act 3 town (Kurast Docks)
	if a.ctx.Data.PlayerUnit.Area != area.KurastDocks {
		a.ctx.Logger.Info("Not in Act 3 town, using waypoint to Kurast Docks")
		if err := action.WayPoint(area.KurastDocks); err != nil {
			return err
		}
	}

	// Try to use World Stone Shard if configured
	// If extraction fails (shard not in stash), skip run instead of crashing
	if cfg.UseWorldStoneShard {
		a.ctx.Logger.Info("Attempting to use World Stone Shard for Act 3")
		if err := action.UseWorldStoneShard(3); err != nil {
			a.ctx.Logger.Warn("Failed to use World Stone Shard - skipping to next run",
				"error", err.Error())
			return nil // Skip to next run
		}
	}

	// PRIORITY: Kill Mephisto first (boosted by WSS)
	a.ctx.Logger.Info("Priority: Travincal + Durance + Mephisto (boosted by WSS)")
	if err := action.WayPoint(area.Travincal); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.DuranceOfHateLevel1); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.DuranceOfHateLevel2); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.DuranceOfHateLevel3); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}

	// Kill Mephisto
	a.ctx.Logger.Info("Killing Mephisto (boosted)")
	if err := a.ctx.Char.KillMephisto(); err != nil {
		return err
	}
	a.ctx.EnableItemPickup()
	action.ItemPickup(30)

	if err := action.ReturnTown(); err != nil {
		return err
	}

	// Segment 1: Spider Forest + Spider Cavern
	a.ctx.Logger.Info("Segment 1: Spider Forest + Spider Cavern")
	if err := action.WayPoint(area.SpiderForest); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.SpiderCavern); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.ReturnTown(); err != nil {
		return err
	}

	// Segment 2: Great Marsh
	a.ctx.Logger.Info("Segment 2: Great Marsh")
	if err := action.WayPoint(area.GreatMarsh); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.ReturnTown(); err != nil {
		return err
	}

	// Segment 3: Flayer Jungle + Swampy Pit + Flayer Dungeon
	a.ctx.Logger.Info("Segment 3: Flayer Jungle + Swampy Pit + Flayer Dungeon")
	if err := action.WayPoint(area.FlayerJungle); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	// Clear Swampy Pit
	if err := action.MoveToArea(area.SwampyPitLevel1); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.SwampyPitLevel2); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.SwampyPitLevel3); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	// TP to town after Swampy Pit
	if err := action.ReturnTown(); err != nil {
		return err
	}

	// Now do Flayer Dungeon
	if err := action.WayPoint(area.FlayerJungle); err != nil {
		return err
	}
	if err := action.MoveToArea(area.FlayerDungeonLevel1); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.FlayerDungeonLevel2); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.FlayerDungeonLevel3); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.ReturnTown(); err != nil {
		return err
	}

	// Segment 4: Lower Kurast
	a.ctx.Logger.Info("Segment 4: Lower Kurast")
	if err := action.WayPoint(area.LowerKurast); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}

	// Segment 5: Kurast Bazaar + Temples
	a.ctx.Logger.Info("Segment 5: Kurast Bazaar + Temples")
	if err := action.MoveToArea(area.KurastBazaar); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.RuinedTemple); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	// Return to Kurast Bazaar
	if err := action.MoveToArea(area.KurastBazaar); err != nil {
		return err
	}
	if err := action.MoveToArea(area.DisusedFane); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.ReturnTown(); err != nil {
		return err
	}

	// Segment 6: Upper Kurast + Sewers + Temples
	a.ctx.Logger.Info("Segment 6: Upper Kurast + Sewers + Temples")
	if err := action.WayPoint(area.UpperKurast); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.SewersLevel1Act3); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.SewersLevel2Act3); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	// TP to town after Sewers
	if err := action.ReturnTown(); err != nil {
		return err
	}

	// Clear the 4 Kurast temples
	if err := action.WayPoint(area.UpperKurast); err != nil {
		return err
	}
	temples := []area.ID{
		area.ForgottenTemple,
		area.RuinedFane,
		area.ForgottenReliquary,
		area.DisusedReliquary,
	}

	for _, temple := range temples {
		if err := action.MoveToArea(temple); err != nil {
			a.ctx.Logger.Warn("Could not enter temple", "temple", temple)
			continue
		}
		if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
			return err
		}
		// Return to Upper Kurast after each temple
		if err := action.MoveToArea(area.UpperKurast); err != nil {
			return err
		}
	}

	// TP to town after all temples
	if err := action.ReturnTown(); err != nil {
		return err
	}

	a.ctx.Logger.Info("Act 3 full clear completed")

	if cfg.PauseAfterRun {
		a.ctx.Logger.Info("Stopping supervisor after Act 3 run as configured")
		if err := action.ReturnTown(); err != nil {
			return err
		}
		a.ctx.StopSupervisor()
	}

	return nil
}
