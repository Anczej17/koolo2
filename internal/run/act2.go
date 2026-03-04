package run

import (
	"fmt"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/koolo/internal/action"
	"github.com/hectorgimenez/koolo/internal/config"
	"github.com/hectorgimenez/koolo/internal/context"
)

type Act2 struct {
	ctx *context.Status
}

func NewAct2() *Act2 {
	return &Act2{
		ctx: context.Get(),
	}
}

func (a Act2) Name() string {
	return string(config.Act2Run)
}

func (a Act2) CheckConditions(parameters *RunParameters) SequencerResult {
	if IsQuestRun(parameters) {
		return SequencerError
	}
	return SequencerOk
}

func (a Act2) Run(parameters *RunParameters) error {
	cfg := a.ctx.CharacterCfg.Game.ActRuns.Act2

	monsterFilter := data.MonsterAnyFilter()
	if cfg.FocusOnElitePacks {
		monsterFilter = data.MonsterEliteFilter()
	}

	a.ctx.Logger.Info("Starting Act 2 full clear run")

	// Ensure we're in Act 2 town (Lut Gholein)
	if a.ctx.Data.PlayerUnit.Area != area.LutGholein {
		a.ctx.Logger.Info("Not in Act 2 town, using waypoint to Lut Gholein")
		if err := action.WayPoint(area.LutGholein); err != nil {
			return err
		}
	}

	// Try to use World Stone Shard if configured
	// If extraction fails (shard not in stash), skip run instead of crashing
	if cfg.UseWorldStoneShard {
		a.ctx.Logger.Info("Attempting to use World Stone Shard for Act 2")
		if err := action.UseWorldStoneShard(2); err != nil {
			a.ctx.Logger.Warn("Failed to use World Stone Shard - skipping to next run",
				"error", err.Error())
			return nil // Skip to next run
		}
	}

	// PRIORITY 1: Kill Duriel FIRST (maximize WSS buff time)
	// Use existing DurielRun which knows immediately which tomb is correct
	a.ctx.Logger.Info("PRIORITY: Killing Duriel first (WSS boosted)")
	durielRun := NewDuriel()
	if err := durielRun.Run(parameters); err != nil {
		return fmt.Errorf("failed to kill Duriel: %w", err)
	}

	// PRIORITY 2: Clear Canyon + Tombs (full clear)
	a.ctx.Logger.Info("Clearing Canyon of the Magi and Tal Rasha's Tombs")
	if err := action.WayPoint(area.CanyonOfTheMagi); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}

	// Clear all 7 Tal Rasha's Tombs
	tombs := []area.ID{
		area.TalRashasTomb1,
		area.TalRashasTomb2,
		area.TalRashasTomb3,
		area.TalRashasTomb4,
		area.TalRashasTomb5,
		area.TalRashasTomb6,
		area.TalRashasTomb7,
	}

	for _, tomb := range tombs {
		// Move back to canyon if needed
		if a.ctx.Data.PlayerUnit.Area != area.CanyonOfTheMagi {
			if err := action.MoveToArea(area.CanyonOfTheMagi); err != nil {
				return err
			}
		}
		if err := action.MoveToArea(tomb); err != nil {
			a.ctx.Logger.Warn("Could not enter tomb", "tomb", tomb)
			continue
		}
		if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
			return err
		}
	}

	if err := action.ReturnTown(); err != nil {
		return err
	}

	// Segment 1: Sewers
	a.ctx.Logger.Info("Segment 1: Sewers")
	if err := action.ReturnTown(); err != nil {
		return err
	}
	// Move to sewers entrance
	if err := action.MoveToArea(area.SewersLevel1Act2); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.SewersLevel2Act2); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.SewersLevel3Act2); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.ReturnTown(); err != nil {
		return err
	}

	// Segment 2: Rocky Waste + Stony Tomb
	a.ctx.Logger.Info("Segment 2: Rocky Waste + Stony Tomb")
	if err := action.WayPoint(area.DryHills); err != nil {
		return err
	}
	// Walk back to Rocky Waste
	if err := action.MoveToArea(area.RockyWaste); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.StonyTombLevel1); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.StonyTombLevel2); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.ReturnTown(); err != nil {
		return err
	}

	// Segment 3: Dry Hills + Halls of the Dead
	a.ctx.Logger.Info("Segment 3: Dry Hills + Halls of the Dead")
	if err := action.WayPoint(area.DryHills); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.HallsOfTheDeadLevel1); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.HallsOfTheDeadLevel2); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.HallsOfTheDeadLevel3); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.ReturnTown(); err != nil {
		return err
	}

	// Segment 4: Far Oasis + Maggot Lair
	a.ctx.Logger.Info("Segment 4: Far Oasis + Maggot Lair")
	if err := action.WayPoint(area.FarOasis); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.MaggotLairLevel1); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.MaggotLairLevel2); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.MaggotLairLevel3); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.ReturnTown(); err != nil {
		return err
	}

	// Segment 5: Lost City + Ancient Tunnels + Claw Viper Temple
	a.ctx.Logger.Info("Segment 5: Lost City + Ancient Tunnels + Claw Viper Temple")
	if err := action.WayPoint(area.LostCity); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.AncientTunnels); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	// TP to town after Ancient Tunnels
	if err := action.ReturnTown(); err != nil {
		return err
	}

	// Now do Valley of Snakes + Claw Viper Temple
	if err := action.WayPoint(area.LostCity); err != nil {
		return err
	}
	if err := action.MoveToArea(area.ValleyOfSnakes); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.ClawViperTempleLevel1); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.ClawViperTempleLevel2); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.ReturnTown(); err != nil {
		return err
	}

	// Segment 6: Arcane Sanctuary
	a.ctx.Logger.Info("Segment 7: Arcane Sanctuary")
	// Enter Harem from palace in town
	if err := action.MoveToArea(area.HaremLevel1); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.HaremLevel2); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.PalaceCellarLevel1); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.PalaceCellarLevel2); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.PalaceCellarLevel3); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.ArcaneSanctuary); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}

	a.ctx.Logger.Info("Act 2 full clear completed")

	if cfg.PauseAfterRun {
		a.ctx.Logger.Info("Stopping supervisor after Act 2 run as configured")
		if err := action.ReturnTown(); err != nil {
			return err
		}
		a.ctx.StopSupervisor()
	}

	return nil
}
