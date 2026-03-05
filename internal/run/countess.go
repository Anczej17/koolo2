package run

import (
	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/d2go/pkg/data/quest"
	"github.com/hectorgimenez/koolo/internal/action"
	"github.com/hectorgimenez/koolo/internal/config"
	"github.com/hectorgimenez/koolo/internal/context"
)

type Countess struct {
	ctx *context.Status
}

func NewCountess() *Countess {
	return &Countess{
		ctx: context.Get(),
	}
}

func (c Countess) Name() string {
	return string(config.CountessRun)
}

func (a Countess) CheckConditions(parameters *RunParameters) SequencerResult {
	farmingRun := IsFarmingRun(parameters)
	questCompleted := a.ctx.Data.Quests[quest.Act1TheForgottenTower].Completed()
	if (farmingRun && !questCompleted) || (!farmingRun && questCompleted) {
		return SequencerSkip
	}
	return SequencerOk
}

func (c Countess) Run(parameters *RunParameters) error {
	// Travel to boss level
	err := action.WayPoint(area.BlackMarsh)
	if err != nil {
		return err
	}

	areas := []area.ID{
		area.ForgottenTower,
		area.TowerCellarLevel1,
		area.TowerCellarLevel2,
		area.TowerCellarLevel3,
		area.TowerCellarLevel4,
		area.TowerCellarLevel5,
	}
	clearFloors := c.ctx.CharacterCfg.Game.Countess.ClearFloors

	for _, a := range areas {
		err = action.MoveToArea(a)
		if err != nil {
			return err
		}

		if clearFloors && a != area.TowerCellarLevel5 {
			if err = action.ClearCurrentLevel(false, data.MonsterAnyFilter()); err != nil {
				return err
			}
		}
	}

	err = action.MoveTo(func() (data.Position, bool) {
		areaData := c.ctx.Data.Areas[area.TowerCellarLevel5]
		countessNPC, found := areaData.NPCs.FindOne(740)
		if !found {
			return data.Position{}, false
		}

		return countessNPC.Positions[0], true
	})
	if err != nil {
		return err
	}

	// Kill Countess
	if err := c.ctx.Char.KillCountess(); err != nil {
		return err
	}

	action.ItemPickup(30)

	if clearFloors {
		return action.ClearCurrentLevel(false, data.MonsterAnyFilter())
	}
	return nil
}
