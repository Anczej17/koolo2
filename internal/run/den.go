package run

import (
	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/area"
	"local/internal/svc/internal/gamelib/data/item"
	"local/internal/svc/internal/gamelib/data/npc"
	"local/internal/svc/internal/gamelib/data/quest"
	"local/internal/svc/internal/gamelib/data/stat"
	"local/internal/svc/internal/action"
	"local/internal/svc/internal/action/step"
	"local/internal/svc/internal/config"
	"local/internal/svc/internal/context"
)

type Den struct {
	ctx *context.Status
}

func NewDen() *Den {
	return &Den{
		ctx: context.Get(),
	}
}

func (d Den) Name() string {
	return string(config.DenRun)
}

func (d Den) CheckConditions(parameters *RunParameters) SequencerResult {
	if IsQuestRun(parameters) && d.ctx.Data.Quests[quest.Act1DenOfEvil].Completed() {
		return SequencerSkip
	}
	return SequencerOk
}

func (d Den) Run(parameters *RunParameters) error {
	d.ctx.Logger.Info("Starting Den of Evil Quest...")

	oldClearPathDist := d.ctx.CharacterCfg.Character.ClearPathDist
	d.ctx.CharacterCfg.Character.ClearPathDist = 20
	defer func() {
		d.ctx.CharacterCfg.Character.ClearPathDist = oldClearPathDist
	}()

	_, isLevelingChar := d.ctx.Char.(context.LevelingCharacter)
	if isLevelingChar {
		lvl, found := d.ctx.Data.PlayerUnit.FindStat(stat.Level, 0)
		if found && lvl.Value == 1 {
			err := action.MoveToArea(area.BloodMoor)
			if err != nil {
				return err
			}
			err = action.MoveToArea(area.ColdPlains)
			if err != nil {
				return err
			}
			err = action.DiscoverWaypoint()
			if err != nil {
				return err
			}
		} else if err := action.WayPoint(area.ColdPlains); err != nil {
			return err
		}
	}

	err := action.MoveToArea(area.BloodMoor)
	if err != nil {
		return err
	}

	err = action.MoveToArea(area.DenOfEvil)
	if err != nil {
		return err
	}

	if err := action.ClearCurrentLevel(false, data.MonsterAnyFilter()); err != nil {
		return err
	}

	_, foundTP := d.ctx.Data.Inventory.Find("TomeOfTownPortal", item.LocationInventory)
	if !isLevelingChar || foundTP {

		err = action.ReturnTown()
		if err != nil {
			return err
		}

		err = action.InteractNPC(npc.Akara)
		if err != nil {
			return err
		}

		step.CloseAllMenus()

	}

	return nil
}
