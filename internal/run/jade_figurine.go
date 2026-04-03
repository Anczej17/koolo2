package run

import (
	"local/internal/svc/internal/gamelib/data/area"
	"local/internal/svc/internal/gamelib/data/item"
	"local/internal/svc/internal/gamelib/data/npc"
	"local/internal/svc/internal/gamelib/data/quest"
	"local/internal/svc/internal/action"
	"local/internal/svc/internal/action/step"
	"local/internal/svc/internal/config"
	"local/internal/svc/internal/context"
	"local/internal/svc/internal/game"
	"local/internal/svc/internal/ui"
	"local/internal/svc/internal/utils"
)

type JadeFigurine struct {
	ctx *context.Status
}

func NewJadeFigurine() *JadeFigurine {
	return &JadeFigurine{
		ctx: context.Get(),
	}
}

func (jf JadeFigurine) Name() string {
	return string(config.JadeFigurineRun)
}

func (jf JadeFigurine) CheckConditions(parameters *RunParameters) SequencerResult {
	if IsFarmingRun(parameters) {
		return SequencerError
	}
	if !jf.ctx.Data.Quests[quest.Act2TheSevenTombs].Completed() {
		return SequencerStop
	}
	if _, potionFound := jf.ctx.Data.Inventory.Find("PotionOfLife", item.LocationInventory); potionFound {
		return SequencerOk
	}
	q := jf.ctx.Data.Quests[quest.Act3TheGoldenBird]
	if q.NotStarted() || q.Completed() {
		return SequencerSkip
	}
	return SequencerOk
}

func (jf JadeFigurine) Run(parameters *RunParameters) error {
	if jf.ctx.Data.PlayerUnit.Area != area.KurastDocks {
		action.WayPoint(area.KurastDocks)
	}

	_, jadefigureFound := jf.ctx.Data.Inventory.Find("AJadeFigurine", item.LocationInventory)
	if jadefigureFound {
		action.InteractNPC(npc.Meshif2)
	}

	_, goldenbirdFound := jf.ctx.Data.Inventory.Find("TheGoldenBird", item.LocationInventory)
	if goldenbirdFound {
		// Talk to Alkor
		action.InteractNPC(npc.Alkor)
		action.InteractNPC(npc.Ormus)
		action.InteractNPC(npc.Alkor)
		utils.Sleep(500)
	}

	lifepotion, lifepotfound := jf.ctx.Data.Inventory.Find("PotionOfLife", item.LocationInventory)
	if lifepotfound {
		jf.ctx.HID.PressKeyBinding(jf.ctx.Data.KeyBindings.Inventory)
		screenPos := ui.GetScreenCoordsForItem(lifepotion)
		jf.ctx.HID.Click(game.RightButton, screenPos.X, screenPos.Y)
		step.CloseAllMenus()
	}
	return nil
}
