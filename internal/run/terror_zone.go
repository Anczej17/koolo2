package run

import (
	"fmt"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/d2go/pkg/data/object"
	"github.com/hectorgimenez/koolo/internal/action"
	"github.com/hectorgimenez/koolo/internal/context"
	terrorzones "github.com/hectorgimenez/koolo/internal/terrorzone"
)

type TerrorZone struct {
	ctx *context.Status
}

func NewTerrorZone() *TerrorZone {
	return &TerrorZone{
		ctx: context.Get(),
	}
}

func (tz TerrorZone) Name() string {
	tzNames := make([]string, 0)
	for _, tzArea := range tz.AvailableTZs() {
		tzNames = append(tzNames, tzArea.Area().Name)
	}

	return fmt.Sprintf("TerrorZone Run: %v", tzNames)
}

func (tz TerrorZone) CheckConditions(parameters *RunParameters) SequencerResult {
	return SequencerError
}

func (tz TerrorZone) Run(parameters *RunParameters) error {

	availableTzs := tz.AvailableTZs()
	if len(availableTzs) == 0 {
		return nil
	}

	// --- Special-case TZs that already have dedicated runs ---
	switch availableTzs[0] {
	case area.PitLevel1, area.PitLevel2:
		return NewPit().Run(parameters)
	case area.Tristram:
		return NewTristram().Run(parameters)
	case area.MooMooFarm:
		return NewCows().Run(parameters)
	case area.TalRashasTomb1:
		return NewTalRashaTombs().Run(parameters)
	case area.AncientTunnels:
		return NewAncientTunnels().Run(parameters)
	case area.ArcaneSanctuary:
		return NewSummonerTZ(tz.customTZEnemyFilter()).Run(parameters)
	case area.Travincal:
		return NewTravincal().Run(parameters)
	case area.DuranceOfHateLevel1:
		return NewMephisto(tz.customTZEnemyFilter()).Run(parameters)
	case area.ChaosSanctuary:
		return NewDiablo().Run(parameters)
	case area.NihlathaksTemple:
		return NewNihlathakTZ(tz.customTZEnemyFilter()).Run(parameters)
	case area.TheWorldStoneKeepLevel1:
		return NewBaal(tz.customTZEnemyFilter()).Run(parameters)

		//custom tz boss runs
		//custom made areas for tz boss hunt - diablo already has default config so there is no need to add any additional config for it
	case area.CatacombsLevel4:
		return NewAndariel().Run(parameters)
	case area.TalRashasTomb4:
		return NewDuriel().Run(parameters)
	case area.DuranceOfHateLevel3:
		return NewMephisto(nil).Run(parameters)
	case area.ThroneOfDestruction:
		return NewBaal(nil).Run(parameters)
	}

	// --- Generic TZ handling via centralized routes ---
	primary := availableTzs[0]

	routes := terrorzones.RoutesFor(primary)
	if len(routes) == 0 {
		tz.ctx.Logger.Debug("No terror zone route defined for %v", primary.Area().Name)
		return nil
	}

	for _, route := range routes {
		for idx, step := range route {
			tz.ctx.Logger.Info("TZ route step",
				"idx", idx,
				"area", step.Area.Area().Name,
				"kind", step.Kind,
				"currentArea", tz.ctx.Data.PlayerUnit.Area.Area().Name)

			// Navigation depends on step kind
			switch step.Kind {
			case terrorzones.StepPortal:
				// Find and interact with red portal to reach the target area
				if err := tz.enterRedPortal(step.Area); err != nil {
					tz.ctx.Logger.Warn("TZ route: red portal failed", "area", step.Area.Area().Name, "error", err)
					return err
				}
				// Clear after entering
				if err := action.ClearCurrentLevel(
					tz.ctx.CharacterCfg.Game.TerrorZone.OpenChests,
					tz.customTZEnemyFilter(),
				); err != nil {
					tz.ctx.Logger.Warn("TZ route: ClearCurrentLevel failed", "area", step.Area.Area().Name, "error", err)
					return err
				}
			default:
				// StepMove and StepClear: first step via waypoint, rest via MoveToArea
				if idx == 0 {
					if err := action.WayPoint(step.Area); err != nil {
						tz.ctx.Logger.Warn("TZ route: WayPoint failed", "area", step.Area.Area().Name, "error", err)
						return err
					}
				} else {
					if err := action.MoveToArea(step.Area); err != nil {
						tz.ctx.Logger.Warn("TZ route: MoveToArea failed", "area", step.Area.Area().Name, "error", err)
						return err
					}
				}

				// Clearing: only if the route explicitly says so.
				if step.Kind == terrorzones.StepClear {
					if err := action.ClearCurrentLevel(
						tz.ctx.CharacterCfg.Game.TerrorZone.OpenChests,
						tz.customTZEnemyFilter(),
					); err != nil {
						tz.ctx.Logger.Warn("TZ route: ClearCurrentLevel failed", "area", step.Area.Area().Name, "error", err)
						return err
					}
				}
			}

			tz.ctx.Logger.Info("TZ route step completed", "idx", idx, "area", step.Area.Area().Name)
		}
	}

	return nil
}

// enterRedPortal finds a red portal (PermanentTownPortal) on the current map,
// moves to it, and interacts with it to enter the target area (e.g. Pit of Acheron).
func (tz TerrorZone) enterRedPortal(targetArea area.ID) error {
	ctx := tz.ctx
	ctx.RefreshGameData()

	// Find the red portal on the current map
	portal, found := ctx.Data.Objects.FindOne(object.PermanentTownPortal)
	if !found {
		return fmt.Errorf("red portal not found for %s", targetArea.Area().Name)
	}

	ctx.Logger.Info("Found red portal, moving to interact",
		"targetArea", targetArea.Area().Name,
		"portalPos", portal.Position)

	// Move to the portal
	if err := action.MoveToCoords(portal.Position); err != nil {
		return fmt.Errorf("failed to move to red portal: %w", err)
	}

	// Interact with the portal
	return action.InteractObject(portal, func() bool {
		return ctx.Data.PlayerUnit.Area == targetArea
	})
}

func (tz TerrorZone) AvailableTZs() []area.ID {
	tz.ctx.RefreshGameData()
	var availableTZs []area.ID
	for _, tzone := range tz.ctx.Data.TerrorZones {
		for _, tzArea := range tz.ctx.CharacterCfg.Game.TerrorZone.Areas {
			if tzone == tzArea {
				availableTZs = append(availableTZs, tzone)
			}
		}
	}

	return availableTZs
}

func (tz TerrorZone) customTZEnemyFilter() data.MonsterFilter {
	return func(m data.Monsters) []data.Monster {
		var filteredMonsters []data.Monster
		monsterFilter := data.MonsterAnyFilter()
		if tz.ctx.CharacterCfg.Game.TerrorZone.FocusOnElitePacks {
			monsterFilter = data.MonsterEliteFilter()
		}

		for _, mo := range m.Enemies(monsterFilter) {
			isImmune := false
			for _, resist := range tz.ctx.CharacterCfg.Game.TerrorZone.SkipOnImmunities {
				if mo.IsImmune(resist) {
					isImmune = true
				}
			}
			if !isImmune {
				filteredMonsters = append(filteredMonsters, mo)
			}
		}

		return filteredMonsters
	}
}
