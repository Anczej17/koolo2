package run

import (
	"fmt"

	"local/internal/svc/internal/gamelib/data/area"
	"local/internal/svc/internal/gamelib/data/difficulty"
	"local/internal/svc/internal/gamelib/data/quest"
	"local/internal/svc/internal/gamelib/data/skill"
	"local/internal/svc/internal/gamelib/memory"
	"local/internal/svc/internal/action"
	"local/internal/svc/internal/config"
	"local/internal/svc/internal/context"
	"local/internal/svc/internal/town"
	"local/internal/svc/internal/utils"
	"github.com/lxn/win"
)

type FrozenAuraMerc struct {
	ctx *context.Status
}

func NewFrozenAuraMerc() *FrozenAuraMerc {
	return &FrozenAuraMerc{
		ctx: context.Get(),
	}
}

func (fam FrozenAuraMerc) Name() string {
	return string(config.FrozenAuraMercRun)
}

func (fam FrozenAuraMerc) CheckConditions(parameters *RunParameters) SequencerResult {
	if !IsQuestRun(parameters) || fam.ctx.CharacterCfg.Game.Difficulty != difficulty.Nightmare {
		return SequencerError
	}

	if !fam.ctx.Data.Quests[quest.Act1SistersToTheSlaughter].Completed() {
		return SequencerStop
	}

	if fam.ctx.Data.MercHPPercent() < 0 || !fam.ctx.CharacterCfg.Character.ShouldHireAct2MercFrozenAura {
		return SequencerSkip
	}

	return SequencerOk
}

func (fam FrozenAuraMerc) Run(parameters *RunParameters) error {
	if fam.ctx.Data.PlayerUnit.Area != area.LutGholein {
		action.WayPoint(area.LutGholein)
	}

	fam.ctx.Logger.Info("Start Hiring merc with Frozen Aura")
	action.DrinkAllPotionsInInventory()

	fam.ctx.Logger.Info("Un-equipping merc")
	if err := action.UnEquipMercenary(); err != nil {
		fam.ctx.Logger.Error(fmt.Sprintf("Failed to unequip mercenary: %s", err.Error()))
		return err
	}

	fam.ctx.Logger.Info("Interacting with mercenary NPC")
	mercNPC := town.GetTownByArea(fam.ctx.Data.PlayerUnit.Area).MercContractorNPC()
	if err := action.InteractNPC(mercNPC); err != nil {
		return err
	}
	// Full-packet bot: 0x38 dialog option 1 = "Hire Mercenary" (user 2026-04-19).
	action.SelectNPCOption(1, mercNPC)
	utils.Sleep(2000)

	fam.ctx.Logger.Info("Getting merc list")
	mercList := fam.ctx.GameReader.GetMercList()

	// get the first with fronzen aura
	var mercToHire *memory.MercOption
	for i := range mercList {
		if mercList[i].Skill.ID == skill.HolyFreeze {
			mercToHire = &mercList[i]
			break
		}
	}

	if mercToHire == nil {
		fam.ctx.Logger.Info("No merc with Frozen Aura found, cannot hire")
		return nil
	}

	fam.ctx.Logger.Info(fmt.Sprintf("Hiring merc: %s with skill %s", mercToHire.Name, mercToHire.Skill.Name))
	keySequence := []byte{win.VK_HOME}
	for i := 0; i < mercToHire.Index; i++ {
		keySequence = append(keySequence, win.VK_DOWN)
	}
	keySequence = append(keySequence, win.VK_RETURN, win.VK_UP, win.VK_RETURN) // Select merc and confirm hire
	fam.ctx.HID.KeySequence(keySequence...)

	fam.ctx.CharacterCfg.Character.ShouldHireAct2MercFrozenAura = false

	if err := config.SaveSupervisorConfig(fam.ctx.CharacterCfg.ConfigFolderName, fam.ctx.CharacterCfg); err != nil {
		fam.ctx.Logger.Error(fmt.Sprintf("Failed to save character configuration: %s", err.Error()))
	}

	fam.ctx.Logger.Info("Merc hired successfully, re-equipping merc")
	action.AutoEquip()
	return nil
}
