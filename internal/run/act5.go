package run

import (
	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/koolo/internal/action"
	"github.com/hectorgimenez/koolo/internal/config"
	"github.com/hectorgimenez/koolo/internal/context"
)

type Act5 struct {
	ctx *context.Status
}

func NewAct5() *Act5 {
	return &Act5{
		ctx: context.Get(),
	}
}

func (a Act5) Name() string {
	return string(config.Act5Run)
}

func (a Act5) CheckConditions(parameters *RunParameters) SequencerResult {
	if IsQuestRun(parameters) {
		return SequencerError
	}
	return SequencerOk
}

func (a Act5) Run(parameters *RunParameters) error {
	cfg := a.ctx.CharacterCfg.Game.ActRuns.Act5

	monsterFilter := data.MonsterAnyFilter()
	if cfg.FocusOnElitePacks {
		monsterFilter = data.MonsterEliteFilter()
	}

	a.ctx.Logger.Info("Starting Act 5 full clear run")

	// Ensure we're in Act 5 town (Harrogath)
	if a.ctx.Data.PlayerUnit.Area != area.Harrogath {
		a.ctx.Logger.Info("Not in Act 5 town, using waypoint to Harrogath")
		if err := action.WayPoint(area.Harrogath); err != nil {
			return err
		}
	}

	// Try to use World Stone Shard if configured
	// Act 5 is last act - if extraction fails, pause supervisor (all acts exhausted)
	if cfg.UseWorldStoneShard {
		a.ctx.Logger.Info("Attempting to use World Stone Shard for Act 5")
		if err := action.UseWorldStoneShard(5); err != nil {
			a.ctx.Logger.Error("Failed to use World Stone Shard for Act 5 - ALL acts skipped, PAUSING supervisor",
				"error", err.Error())
			a.ctx.StopSupervisor()
			return nil
		}
	}

	// PRIORITY: Kill Baal first (boosted by WSS)
	// NOTE: Baal run handles WSK Level 2-3 clearing + throne + waves + boss
	// We only clear WSK Level 1 here since Baal.Run() starts from Level 2
	a.ctx.Logger.Info("Priority: Worldstone Keep Level 1 + Baal run (boosted by WSS)")
	if err := action.WayPoint(area.TheWorldStoneKeepLevel2); err != nil {
		return err
	}
	// Move back to Worldstone Keep Level 1
	if err := action.MoveToArea(area.TheWorldStoneKeepLevel1); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}

	// Now run Baal (handles WSK 2-3, throne + waves + boss)
	a.ctx.Logger.Info("Running Baal run (WSK 2-3 + throne + waves + boss, boosted)")
	baalRun := NewBaal(monsterFilter)
	if err := baalRun.Run(parameters); err != nil {
		return err
	}

	if err := action.ReturnTown(); err != nil {
		return err
	}

	// Segment 1: Bloody Foothills + Frigid Highlands (Abaddon skipped - not supported by bot)
	a.ctx.Logger.Info("Segment 1: Bloody Foothills + Frigid Highlands")
	if err := action.ReturnTown(); err != nil {
		return err
	}
	// Walk from Harrogath to Bloody Foothills
	if err := action.MoveToArea(area.BloodyFoothills); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.FrigidHighlands); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.ReturnTown(); err != nil {
		return err
	}

	// Segment 2: Arreat Plateau (Pit of Acheron removed - not supported by bot)
	a.ctx.Logger.Info("Segment 2: Arreat Plateau")
	if err := action.WayPoint(area.ArreatPlateau); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.ReturnTown(); err != nil {
		return err
	}

	// Segment 3: Crystalline Passage + Frozen River
	a.ctx.Logger.Info("Segment 3: Crystalline Passage + Frozen River")
	if err := action.WayPoint(area.CrystallinePassage); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.FrozenRiver); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.ReturnTown(); err != nil {
		return err
	}

	// Segment 4: Glacial Trail + Drifter Cavern
	a.ctx.Logger.Info("Segment 4: Glacial Trail + Drifter Cavern")
	if err := action.WayPoint(area.CrystallinePassage); err != nil {
		return err
	}
	if err := action.MoveToArea(area.GlacialTrail); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.DrifterCavern); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.ReturnTown(); err != nil {
		return err
	}

	// Segment 5: Frozen Tundra + The Ancients Way + Icy Cellar
	a.ctx.Logger.Info("Segment 5: Frozen Tundra + The Ancients Way + Icy Cellar")
	if err := action.WayPoint(area.GlacialTrail); err != nil {
		return err
	}
	if err := action.MoveToArea(area.FrozenTundra); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.TheAncientsWay); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.IcyCellar); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.ReturnTown(); err != nil {
		return err
	}

	// Segment 6: Arreat Summit + Nihlathak's Temple
	// Note: Skipping Crystallized Caverns (IDs 113-116) as they don't have proper area constants defined
	a.ctx.Logger.Info("Segment 7: Arreat Summit + Nihlathak's Temple")
	if err := action.WayPoint(area.TheAncientsWay); err != nil {
		return err
	}
	if err := action.MoveToArea(area.ArreatSummit); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.ReturnTown(); err != nil {
		return err
	}

	// Nihlathak's Temple
	if err := action.WayPoint(area.HallsOfPain); err != nil {
		return err
	}
	// Move back to Nihlathak's Temple
	if err := action.MoveToArea(area.NihlathaksTemple); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.HallsOfAnguish); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.HallsOfPain); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}
	if err := action.MoveToArea(area.HallsOfVaught); err != nil {
		return err
	}
	if err := action.ClearCurrentLevel(cfg.OpenChests, monsterFilter); err != nil {
		return err
	}

	// Kill Nihlathak
	a.ctx.Logger.Info("Killing Nihlathak")
	if err := a.ctx.Char.KillNihlathak(); err != nil {
		return err
	}
	a.ctx.EnableItemPickup()
	action.ItemPickup(30)

	if err := action.ReturnTown(); err != nil {
		return err
	}

	a.ctx.Logger.Info("Act 5 full clear completed")

	if cfg.PauseAfterRun {
		a.ctx.Logger.Info("Stopping supervisor after Act 5 run as configured")
		if err := action.ReturnTown(); err != nil {
			return err
		}
		a.ctx.StopSupervisor()
	}

	return nil
}
