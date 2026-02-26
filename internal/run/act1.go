package run

import (
	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/d2go/pkg/data/object"
	"github.com/hectorgimenez/koolo/internal/action"
	"github.com/hectorgimenez/koolo/internal/config"
	"github.com/hectorgimenez/koolo/internal/context"
)

type Act1 struct {
	ctx *context.Status
}

func NewAct1() *Act1 {
	return &Act1{
		ctx: context.Get(),
	}
}

func (a Act1) Name() string {
	return string(config.Act1Run)
}

func (a Act1) CheckConditions(parameters *RunParameters) SequencerResult {
	if IsQuestRun(parameters) {
		return SequencerError
	}
	return SequencerOk
}

func (a Act1) Run(parameters *RunParameters) error {
	// Get configuration
	cfg := a.ctx.CharacterCfg.Game.ActRuns.Act1

	// Define monster filter based on configuration
	monsterFilter := data.MonsterAnyFilter()
	if cfg.FocusOnElitePacks {
		monsterFilter = data.MonsterEliteFilter()
	}

	a.ctx.Logger.Info("Starting Act 1 full clear run")

	// Ensure we're in Act 1 town (Rogue Encampment)
	if a.ctx.Data.PlayerUnit.Area != area.RogueEncampment {
		a.ctx.Logger.Info("Not in Act 1 town, using waypoint to Rogue Encampment")
		if err := action.WayPoint(area.RogueEncampment); err != nil {
			return err
		}
	}

	// Try to use World Stone Shard if configured
	// If extraction fails (shard not in stash), skip run instead of crashing
	if cfg.UseWorldStoneShard {
		a.ctx.Logger.Info("Attempting to use World Stone Shard for Act 1")
		if err := action.UseWorldStoneShard(1); err != nil {
			a.ctx.Logger.Warn("Failed to use World Stone Shard - skipping to next run",
				"error", err.Error())
			return nil // Skip to next run
		}
	}

	// PRIORITY: Kill Andariel first (boosted by WSS)
	a.ctx.Logger.Info("Priority: Catacombs + Andariel (boosted by WSS)")
	if err := action.WayPoint(area.CatacombsLevel2); err != nil {
		return err
	}
	// Move back to Catacombs Level 1
	if err := action.MoveToArea(area.CatacombsLevel1); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	// Return to Catacombs Level 2
	if err := action.MoveToArea(area.CatacombsLevel2); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.CatacombsLevel3); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.CatacombsLevel4); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}

	// Kill Andariel
	a.ctx.Logger.Info("Killing Andariel (boosted)")
	if err := a.ctx.Char.KillAndariel(); err != nil {
		return err
	}
	a.ctx.EnableItemPickup()
	action.ItemPickup(30)

	if err := action.ReturnTown(); err != nil {
		return err
	}

	// Segment 1: Blood Moor -> Cold Plains (no town return, walk directly from town)
	a.ctx.Logger.Info("Segment 1: Blood Moor -> Cold Plains")
	// Walk from town to Blood Moor
	if err := action.MoveToArea(area.BloodMoor); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	// Move directly to Cold Plains (no town return)
	if err := action.MoveToArea(area.ColdPlains); err != nil {
		return err
	}

	// Segment 2: Cold Plains + Cave + Burial Grounds
	a.ctx.Logger.Info("Segment 2: Cold Plains + Cave + Burial Grounds")
	// Already in Cold Plains, continue clearing
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.CaveLevel1); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.CaveLevel2); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	// TP to town, WP to Cold Plains
	if err := action.ReturnTown(); err != nil {
		return err
	}
	if err := action.WayPoint(area.ColdPlains); err != nil {
		return err
	}
	if err := action.MoveToArea(area.BurialGrounds); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.Crypt); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	// Walk back to Burial Grounds
	if err := action.MoveToArea(area.BurialGrounds); err != nil {
		return err
	}
	if err := action.MoveToArea(area.Mausoleum); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	// TP to town, WP to Stony Field
	if err := action.ReturnTown(); err != nil {
		return err
	}

	// Segment 3: Stony Field + Underground Passage
	a.ctx.Logger.Info("Segment 3: Stony Field + Underground Passage")
	if err := action.WayPoint(area.StonyField); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.UndergroundPassageLevel1); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.UndergroundPassageLevel2); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	// TP to town, WP to Dark Wood
	if err := action.ReturnTown(); err != nil {
		return err
	}

	// Segment 4: Dark Wood + Black Marsh + Hole
	a.ctx.Logger.Info("Segment 4: Dark Wood + Black Marsh + Hole")
	if err := action.WayPoint(area.DarkWood); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.BlackMarsh); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.HoleLevel1); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.HoleLevel2); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	// TP to town, WP to Black Marsh
	if err := action.ReturnTown(); err != nil {
		return err
	}

	// Segment 5: Forgotten Tower
	a.ctx.Logger.Info("Segment 5: Forgotten Tower")
	if err := action.WayPoint(area.BlackMarsh); err != nil {
		return err
	}
	if err := action.MoveToArea(area.ForgottenTower); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.TowerCellarLevel1); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.TowerCellarLevel2); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.TowerCellarLevel3); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.TowerCellarLevel4); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.TowerCellarLevel5); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	// TP to town, WP to Black Marsh
	if err := action.ReturnTown(); err != nil {
		return err
	}

	// Segment 6: Tamoe Highland + Pit
	a.ctx.Logger.Info("Segment 6: Tamoe Highland + Pit")
	if err := action.WayPoint(area.BlackMarsh); err != nil {
		return err
	}
	if err := action.MoveToArea(area.TamoeHighland); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.PitLevel1); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.PitLevel2); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	// TP to town, WP to Outer Cloister
	if err := action.ReturnTown(); err != nil {
		return err
	}

	// Segment 7: Barracks (skip Monastery Gate and Outer Cloister)
	a.ctx.Logger.Info("Segment 7: Barracks (direct path)")
	if err := action.WayPoint(area.OuterCloister); err != nil {
		return err
	}
	// Go directly to Barracks without clearing Gate/Cloister
	if err := action.MoveToArea(area.Barracks); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	// TP to town, WP to Jail Level 1
	if err := action.ReturnTown(); err != nil {
		return err
	}

	// Segment 8: Jail Levels
	a.ctx.Logger.Info("Segment 8: Jail Levels")
	if err := action.WayPoint(area.JailLevel1); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.JailLevel2); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.JailLevel3); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	// TP to town, WP to Inner Cloister
	if err := action.ReturnTown(); err != nil {
		return err
	}

	// Segment 9: Inner Cloister + Cathedral
	a.ctx.Logger.Info("Segment 9: Inner Cloister + Cathedral")
	if err := action.WayPoint(area.InnerCloister); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.Cathedral); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	// TP to town for Tristram
	if err := action.ReturnTown(); err != nil {
		return err
	}

	// Segment 10: Tristram (endgame portal - no need to activate Cairn Stones)
	a.ctx.Logger.Info("Segment 11: Tristram")
	if err := action.WayPoint(area.StonyField); err != nil {
		return err
	}

	// Find Tristram portal (in endgame it exists without activating Cairn Stones)
	portal, found := a.ctx.Data.Objects.FindOne(object.PermanentTownPortal)
	if !found {
		a.ctx.Logger.Warn("Tristram portal not found in Stony Field - skipping Tristram")
		return action.ReturnTown()
	}

	// Enter Tristram portal
	err := action.InteractObject(portal, func() bool {
		return a.ctx.Data.AreaData.Area == area.Tristram && a.ctx.Data.AreaData.IsInside(a.ctx.Data.PlayerUnit.Position)
	})
	if err != nil {
		a.ctx.Logger.Warn("Failed to enter Tristram portal", "error", err)
		return action.ReturnTown()
	}

	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}

	a.ctx.Logger.Info("Act 1 full clear completed")

	// Handle pause after run if configured
	if cfg.PauseAfterRun {
		a.ctx.Logger.Info("Stopping supervisor after Act 1 run as configured")
		if err := action.ReturnTown(); err != nil {
			return err
		}
		// Stop the supervisor (player needs to manually restart)
		a.ctx.StopSupervisor()
	}

	return nil
}
