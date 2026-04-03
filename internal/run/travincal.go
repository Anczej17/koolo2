package run

import (
	"errors"
	"log/slog"

	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/area"
	"local/internal/svc/internal/gamelib/data/npc"
	"local/internal/svc/internal/gamelib/data/object"
	"local/internal/svc/internal/gamelib/data/quest"
	"local/internal/svc/internal/gamelib/data/stat"
	"local/internal/svc/internal/action"
	"local/internal/svc/internal/character"
	"local/internal/svc/internal/config"
	"local/internal/svc/internal/context"
	"local/internal/svc/internal/game"
	"local/internal/svc/internal/utils"
)

type Travincal struct {
	ctx *context.Status
}

func NewTravincal() *Travincal {
	return &Travincal{
		ctx: context.Get(),
	}
}

func (t *Travincal) Name() string {
	return string(config.TravincalRun)
}

func (t *Travincal) CheckConditions(parameters *RunParameters) SequencerResult {
	farmingRun := IsFarmingRun(parameters)
	questCompleted := t.ctx.Data.Quests[quest.Act3TheBlackenedTemple].Completed() && t.ctx.Data.Quests[quest.Act3KhalimsWill].Completed()
	if (farmingRun && !questCompleted) || (!farmingRun && questCompleted) {
		return SequencerSkip
	}
	return SequencerOk
}

func (t *Travincal) Run(parameters *RunParameters) error {
	defer func() {
		t.ctx.CurrentGame.AreaCorrection.Enabled = false
	}()

	// Check if the character is a Berserker and swap to combat gear
	if berserker, ok := t.ctx.Char.(*character.Berserker); ok {
		if t.ctx.CharacterCfg.Character.BerserkerBarb.FindItemSwitch {
			berserker.SwapToSlot(0) // Swap to combat gear (lowest Gold Find)
		}
	}

	err := action.WayPoint(area.Travincal)
	if err != nil {
		return err
	}

	// Only Enable Area Correction for Travincal
	t.ctx.CurrentGame.AreaCorrection.ExpectedArea = area.Travincal
	t.ctx.CurrentGame.AreaCorrection.Enabled = true

	//TODO This is temporary needed for barb because have no cta; isrebuffrequired not working for him. We have ActiveWeaponSlot in gamelib ready for that
	action.Buff()

	// Blacklist the Durance of Hate entrance to prevent accidental entry during combat
	if !IsQuestRun(parameters) {
		t.blacklistDuranceEntrance()
	}

	councilPosition := t.findCouncilPosition()

	err = action.MoveToCoords(councilPosition)
	if err != nil {
		t.ctx.Logger.Warn("Error moving to council area", "error", err)
		return err
	}

	// If no council members are visible, try moving to Compelling Orb as fallback
	if !t.anyCouncilAlive() {
		t.ctx.Logger.Warn("Council not found at initial position, trying Compelling Orb")
		compellingOrb, found := t.ctx.Data.Objects.FindOne(object.CompellingOrb)
		if found {
			if moveErr := action.MoveToCoords(compellingOrb.Position); moveErr != nil {
				t.ctx.Logger.Warn("Error moving to Compelling Orb fallback", "error", moveErr)
			}
		} else {
			t.ctx.Logger.Warn("Compelling Orb not found for fallback council position")
		}
	}

	// Safety check: if we accidentally entered Durance, return to Travincal
	t.returnToTravIfNeeded()

	if err := t.ctx.Char.KillCouncil(); err != nil {
		return err
	}

	action.ItemPickup(30)

	t.ctx.CurrentGame.AreaCorrection.Enabled = false

	if IsQuestRun(parameters) {
		if !t.ctx.Data.Quests[quest.Act3KhalimsWill].Completed() {
			compellingorb, found := t.ctx.Data.Objects.FindOne(object.CompellingOrb)
			if !found {
				return errors.New("compelling orb not found")
			}

			if err := action.MoveToCoords(compellingorb.Position); err != nil {
				return err
			}
			action.ClearAreaAroundPosition(t.ctx.Data.PlayerUnit.Position, 20)
			if err := action.MoveToCoords(compellingorb.Position); err != nil {
				return err
			}

			if err := action.ReturnTown(); err != nil {
				return err
			}

			if err := t.prepareWill(); err != nil {
				return err
			}
			if err := t.equipWill(); err != nil {
				return err
			}
			if err := action.UsePortalInTown(); err != nil {
				return err
			}
			utils.PingSleep(utils.Critical, 500)
			if err := t.smashOrb(); err != nil {
				return err
			}
			utils.Sleep(12000)
		}
		if err := t.tryReachDuranceWp(); err != nil {
			return err
		}
	}
	return nil
}

func (t *Travincal) findCouncilPosition() data.Position {
	for _, al := range t.ctx.Data.AdjacentLevels {
		if al.Area == area.DuranceOfHateLevel1 {
			return data.Position{
				X: al.Position.X - 1,
				Y: al.Position.Y + 4,
			}
		}
	}

	return data.Position{}
}

func (t Travincal) prepareWill() error {
	return prepareKhalimsWill(t.ctx)
}

func (t Travincal) equipWill() error {
	if t.ctx.Data.Quests[quest.Act3KhalimsWill].Completed() {
		return nil
	}
	_, _, err := ensureQuestWeaponEquipped(t.ctx, "KhalimsWill", swapWeaponSlot)
	return err
}

func (t Travincal) smashOrb() error {
	if t.ctx.Data.Quests[quest.Act3KhalimsWill].Completed() {
		return nil
	}

	// Interact with the Compelling Orb to open the stairs
	compellingorb, found := t.ctx.Data.Objects.FindOne(object.CompellingOrb)
	if !found {
		return errors.New("compelling orb not found")
	}

	if _, _, err := ensureQuestWeaponEquipped(t.ctx, "KhalimsWill", swapWeaponSlot); err != nil {
		return err
	}

	if err := action.MoveToCoords(compellingorb.Position); err != nil {
		return err
	}
	return withQuestWeaponSlot(t.ctx, "KhalimsWill", func() error {
		if err := action.InteractObject(compellingorb, func() bool {
			o, _ := t.ctx.Data.Objects.FindOne(object.CompellingOrb)
			return !o.Selectable
		}); err != nil {
			return err
		}
		utils.Sleep(300)
		return nil
	})
}

// blacklistDuranceEntrance marks a 7x7 area around the Durance of Hate entrance
// as non-walkable on the collision grid, preventing the bot from accidentally
// walking into Durance during combat.
func (t *Travincal) blacklistDuranceEntrance() {
	for _, al := range t.ctx.Data.AdjacentLevels {
		if al.Area != area.DuranceOfHateLevel1 {
			continue
		}

		relPos := t.ctx.Data.AreaData.Grid.RelativePosition(al.Position)

		// Validate bounds
		if relPos.X < 0 || relPos.X >= t.ctx.Data.AreaData.Grid.Width ||
			relPos.Y < 0 || relPos.Y >= t.ctx.Data.AreaData.Grid.Height {
			t.ctx.Logger.Warn("Durance entrance outside grid bounds",
				slog.Any("worldPos", al.Position),
				slog.Any("gridPos", relPos))
			return
		}

		const blacklistRadius = 3
		count := 0
		for dx := -blacklistRadius; dx <= blacklistRadius; dx++ {
			for dy := -blacklistRadius; dy <= blacklistRadius; dy++ {
				x := relPos.X + dx
				y := relPos.Y + dy
				if x >= 0 && x < t.ctx.Data.AreaData.Grid.Width &&
					y >= 0 && y < t.ctx.Data.AreaData.Grid.Height {
					t.ctx.Data.AreaData.Grid.Set(x, y, game.CollisionTypeNonWalkable)
					count++
				}
			}
		}
		t.ctx.Logger.Debug("Blacklisted Durance entrance",
			slog.Any("worldPos", al.Position),
			slog.Int("tilesBlocked", count))
		return
	}
	t.ctx.Logger.Warn("Durance of Hate entrance not found in adjacent levels")
}

// anyCouncilAlive checks if any council member is alive and visible.
func (t *Travincal) anyCouncilAlive() bool {
	for _, m := range t.ctx.Data.Monsters.Enemies() {
		if (m.Name == npc.CouncilMember || m.Name == npc.CouncilMember2 || m.Name == npc.CouncilMember3) && m.Stats[stat.Life] > 0 {
			return true
		}
	}
	return false
}

// returnToTravIfNeeded checks if the bot accidentally entered Durance of Hate
// and moves back to Travincal if so.
func (t *Travincal) returnToTravIfNeeded() {
	if t.ctx.Data.PlayerUnit.Area != area.DuranceOfHateLevel1 {
		return
	}
	t.ctx.Logger.Warn("Accidentally entered Durance of Hate, returning to Travincal")
	if err := action.MoveToArea(area.Travincal); err != nil {
		t.ctx.Logger.Warn("Failed to return to Travincal from Durance", slog.Any("error", err))
	}
	utils.PingSleep(utils.Medium, 300)
}

func (t Travincal) tryReachDuranceWp() error {
	// Interact with the stairs to go to Durance of Hate Level 1
	_, found := t.ctx.Data.Objects.FindOne(object.StairSR)
	if !found && !t.ctx.Data.Quests[quest.Act3KhalimsWill].Completed() {
		return errors.New("failed to open the Durance stairs")
	}
	if !found {
		t.ctx.Logger.Debug("Stairs to Durance not found")
	}

	err := action.MoveToArea(area.DuranceOfHateLevel1)
	if err != nil {
		return err
	}

	// Move to Durance of Hate Level 2 and discover the waypoint
	err = action.MoveToArea(area.DuranceOfHateLevel2)
	if err != nil {
		return err
	}
	err = action.DiscoverWaypoint()
	if err != nil {
		return err
	}
	return nil
}
