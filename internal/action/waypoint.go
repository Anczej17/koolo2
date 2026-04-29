package action

import (
	"fmt"
	"log/slog"
	"slices"

	"github.com/lxn/win"

	"local/internal/svc/internal/context"
	"local/internal/svc/internal/game"
	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/area"
	"local/internal/svc/internal/ui"
	"local/internal/svc/internal/utils"
)

func WayPoint(dest area.ID) error {
	ctx := context.Get()
	ctx.SetLastAction("WayPoint")

	if !ctx.Data.PlayerUnit.Area.IsTown() {
		if err := ReturnTown(); err != nil {
			return err
		}
	}

	if ctx.Data.PlayerUnit.Area == dest {
		ctx.WaitForGameToLoad()
		return nil
	}

	wpCoords, found := area.WPAddresses[dest]
	if !found {
		return fmt.Errorf("area destination %s is not mapped to a WayPoint (waypoint.go)", area.Areas[dest].Name)
	}

	for _, o := range ctx.Data.Objects {
		if o.IsWaypoint() {

			err := InteractObject(o, func() bool {
				return ctx.Data.OpenMenus.Waypoint
			})
			if err != nil {
				return err
			}
			if ctx.PacketSender == nil {
				if ctx.Data.LegacyGraphics {
					actTabX := ui.WpTabStartXClassic + (wpCoords.Tab-1)*ui.WpTabSizeXClassic + (ui.WpTabSizeXClassic / 2)
					ctx.HID.Click(game.LeftButton, actTabX, ui.WpTabStartYClassic)
				} else {
					actTabX := ui.WpTabStartX + (wpCoords.Tab-1)*ui.WpTabSizeX + (ui.WpTabSizeX / 2)
					ctx.HID.Click(game.LeftButton, actTabX, ui.WpTabStartY)
				}
			}
			utils.PingSleep(utils.Medium, 250) // Medium operation: Wait for waypoint tab to load
			// Just to make sure no message like TZ change or public game spam prevent bot from clicking on waypoint
			ClearMessages()
			ctx.RefreshGameData()
		}
	}

	err := useWP(dest)
	if err != nil {
		return err
	}

	if err := finishWaypointTravel(dest); err != nil {
		return err
	}

	// apply buffs after exiting a waypoint if configured
	if ctx.CharacterCfg.Character.BuffAfterWP && !dest.IsTown() {
		utils.PingSleep(utils.Light, 250)
		Buff()
	}

	return nil
}

func FieldWayPoint(dest area.ID) error {
	ctx := context.Get()
	ctx.SetLastAction("WayPoint")

	if ctx.Data.PlayerUnit.Area == dest {
		ctx.WaitForGameToLoad()
		return nil
	}

	wpCoords, found := area.WPAddresses[dest]
	if !found {
		return fmt.Errorf("area destination %s is not mapped to a WayPoint (waypoint.go)", area.Areas[dest].Name)
	}

	for _, o := range ctx.Data.Objects {
		if o.IsWaypoint() {

			err := InteractObject(o, func() bool {
				return ctx.Data.OpenMenus.Waypoint
			})
			if err != nil {
				return err
			}
			if ctx.PacketSender == nil {
				if ctx.Data.LegacyGraphics {
					actTabX := ui.WpTabStartXClassic + (wpCoords.Tab-1)*ui.WpTabSizeXClassic + (ui.WpTabSizeXClassic / 2)
					ctx.HID.Click(game.LeftButton, actTabX, ui.WpTabStartYClassic)
				} else {
					actTabX := ui.WpTabStartX + (wpCoords.Tab-1)*ui.WpTabSizeX + (ui.WpTabSizeX / 2)
					ctx.HID.Click(game.LeftButton, actTabX, ui.WpTabStartY)
				}
			}
			utils.Sleep(200)
			// Just to make sure no message like TZ change or public game spam prevent bot from clicking on waypoint
			ClearMessages()
			ctx.RefreshGameData()
		}
	}

	err := useWP(dest)
	if err != nil {
		return err
	}

	if err := finishWaypointTravel(dest); err != nil {
		return err
	}

	// apply buffs after exiting a waypoint if configured
	if ctx.CharacterCfg.Character.BuffAfterWP && !dest.IsTown() {
		utils.PingSleep(utils.Light, 250)
		Buff()
	}

	return nil
}

func useWP(dest area.ID) error {
	ctx := context.Get()
	ctx.SetLastAction("useWP")

	finalDestination := dest
	traverseAreas := make([]area.ID, 0)
	currentWP := area.WPAddresses[dest]
	if !slices.Contains(ctx.Data.PlayerUnit.AvailableWaypoints, dest) {
		for {
			traverseAreas = append(currentWP.LinkedFrom, traverseAreas...)

			if currentWP.LinkedFrom != nil {
				dest = currentWP.LinkedFrom[0]
			}

			if len(currentWP.LinkedFrom) == 0 {
				return fmt.Errorf("no available waypoint found to reach destination %s", area.Areas[finalDestination].Name)
			}

			currentWP = area.WPAddresses[currentWP.LinkedFrom[0]]

			if slices.Contains(ctx.Data.PlayerUnit.AvailableWaypoints, dest) {
				break
			}
		}
	}

	currentWP = area.WPAddresses[dest]

	// First use the previous available waypoint that we have discovered
	if ctx.PacketSender != nil {
		ctx.Logger.Info("Waypoint: sending AMB 0x4B destination packet", slog.String("destination", area.Areas[dest].Name))
		if err := ctx.PacketSender.WaypointInteractAMB(dest); err != nil {
			return err
		}
	} else if ctx.Data.LegacyGraphics {
		areaBtnY := ui.WpListStartYClassic + (currentWP.Row-1)*ui.WpAreaBtnHeightClassic + (ui.WpAreaBtnHeightClassic / 2)
		ctx.HID.Click(game.LeftButton, ui.WpListPositionXClassic, areaBtnY)
	} else {
		areaBtnY := ui.WpListStartY + (currentWP.Row-1)*ui.WpAreaBtnHeight + (ui.WpAreaBtnHeight / 2)
		ctx.HID.Click(game.LeftButton, ui.WpListPositionX, areaBtnY)
	}
	utils.PingSleep(utils.Critical, 1000) // Critical operation: Wait for waypoint travel to complete

	// We have the WP discovered, just use it
	if len(traverseAreas) == 0 {
		return nil
	}

	traverseAreas = append(traverseAreas, finalDestination)

	// Next keep traversing all the areas from the previous available waypoint until we reach the destination, trying to discover WPs during the way
	ctx.Logger.Info("Traversing areas to reach destination", slog.Any("areas", traverseAreas))

	for i, dst := range traverseAreas {
		if i > 0 {
			//Fix player great marsh / flayer jungle navigation, part 1
			if ctx.Data.AreaData.Area == area.GreatMarsh && dst == area.FlayerJungle {
				found := false
				for _, adjLvl := range ctx.Data.AreaData.AdjacentLevels {
					if adjLvl.Area == area.FlayerJungle {
						found = true
						break
					}
				}
				if !found {
					FieldWayPoint(area.SpiderForest)
					utils.Sleep(500)
				}
			}
			err := MoveToArea(dst)
			if err != nil {
				//Fix player great marsh / flayer jungle navigation, part 2
				if ctx.Data.AreaData.Area == area.GreatMarsh && dst == area.FlayerJungle {
					FieldWayPoint(area.SpiderForest)
					utils.Sleep(500)
					err = MoveToArea(dst)
					if err != nil {
						return err
					}
				} else {
					return err
				}
			}

			err = DiscoverWaypoint()
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func finishWaypointTravel(dest area.ID) error {
	ctx := context.Get()

	ctx.WaitForGameToLoad()
	utils.Sleep(500)

	ctx.RefreshGameData()
	if ctx.Data.PlayerUnit.Area != dest {
		return fmt.Errorf("failed to reach destination area %s using waypoint", area.Areas[dest].Name)
	}

	for attempt := 1; attempt <= 5; attempt++ {
		ctx.RefreshGameData()
		if !ctx.Data.OpenMenus.IsMenuOpen() {
			break
		}
		ctx.Logger.Warn("Menu still open after waypoint travel; selecting packet-aware close path",
			slog.Int("attempt", attempt),
			slog.String("destination", area.Areas[dest].Name),
			slog.Bool("waypoint", ctx.Data.OpenMenus.Waypoint),
			slog.Bool("inventory", ctx.Data.OpenMenus.Inventory),
			slog.Bool("npc", ctx.Data.OpenMenus.NPCInteract),
			slog.Bool("shop", ctx.Data.OpenMenus.NPCShop),
			slog.Bool("quit", ctx.Data.OpenMenus.QuitMenu))
		if ctx.PacketSender == nil {
			return fmt.Errorf("menu remained open after waypoint travel to %s and PacketSender is not initialized", area.Areas[dest].Name)
		}
		if ctx.Data.OpenMenus.NPCInteract || ctx.Data.OpenMenus.NPCShop {
			townNPC, ok := closestWaypointVisibleNPC(ctx)
			if !ok {
				return fmt.Errorf("NPC menu remained open after waypoint travel to %s but no visible NPC was found for NPCCancel", area.Areas[dest].Name)
			}
			ctx.Logger.Warn("Menu still open after waypoint travel; closing NPC menu via 0x30 NPCCancel",
				slog.Int("attempt", attempt),
				slog.String("destination", area.Areas[dest].Name),
				slog.Int("npcGID", int(townNPC.UnitID)),
				slog.Int("npcID", int(townNPC.Name)))
			if err := ctx.PacketSender.NPCCancel(townNPC.UnitID); err != nil {
				return fmt.Errorf("close NPC menu after waypoint travel via NPCCancel: %w", err)
			}
		} else if ctx.Data.OpenMenus.Waypoint {
			if ctx.GameReader != nil && ctx.GameReader.HWND != 0 {
				game.PostWindowKey(ctx.GameReader.HWND, byte(win.VK_ESCAPE))
			} else if err := ctx.PacketSender.PostKeyInProcess(byte(win.VK_ESCAPE)); err != nil {
				return fmt.Errorf("close waypoint menu via packet-safe ESC: %w", err)
			}
		} else {
			return fmt.Errorf("unexpected non-waypoint menu remained open after waypoint travel to %s", area.Areas[dest].Name)
		}
		utils.Sleep(250)
	}

	ctx.RefreshGameData()
	if ctx.Data.OpenMenus.IsMenuOpen() {
		return fmt.Errorf("menu remained open after waypoint travel to %s", area.Areas[dest].Name)
	}

	return nil
}

func closestWaypointVisibleNPC(ctx *context.Status) (data.Monster, bool) {
	var best data.Monster
	bestDistance := 1 << 30
	for _, m := range ctx.Data.Monsters {
		if m.Type != data.MonsterTypeNone {
			continue
		}
		distance := ctx.PathFinder.DistanceFromMe(m.Position)
		if distance < bestDistance {
			best = m
			bestDistance = distance
		}
	}
	return best, bestDistance != 1<<30
}
