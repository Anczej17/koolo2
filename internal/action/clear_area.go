package action

import (
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/npc"
	"local/internal/svc/internal/gamelib/data/stat"
	"local/internal/svc/internal/action/step"
	"local/internal/svc/internal/context"
	"local/internal/svc/internal/game"
	"local/internal/svc/internal/pather"
)

func ClearAreaAroundPlayer(radius int, filter data.MonsterFilter) error {
	if filter == nil {
		return ClearAreaAroundPosition(context.Get().Data.PlayerUnit.Position, radius)
	}
	return ClearAreaAroundPosition(context.Get().Data.PlayerUnit.Position, radius, filter)
}

// isHerald detects Herald BOSS via stat.HeraldTier (stat 367).
// Both boss and minions get stat 367, but Herald boss is always Unique.
func isHerald(m data.Monster) bool {
	if m.Stats[stat.HeraldTier] > 0 {
		return m.Type == data.MonsterTypeUnique || m.Type == data.MonsterTypeSuperUnique
	}
	return false
}

func IsPriorityMonster(m data.Monster) bool {
	priorityMonsters := []npc.ID{
		npc.FallenShaman,
		npc.CarverShaman,
		npc.DevilkinShaman,
		npc.DarkShaman,
		npc.WarpedShaman,
		npc.MummyGenerator,
		npc.BaalSubjectMummy,
		npc.FetishShaman,
	}

	for _, priorityMonster := range priorityMonsters {
		if m.Name == priorityMonster {
			return true
		}
	}
	return false
}

func SortEnemiesByPriority(enemies *[]data.Monster) {
	ctx := context.Get()
	sort.Slice(*enemies, func(i, j int) bool {
		monsterI := (*enemies)[i]
		monsterJ := (*enemies)[j]

		// HIGHEST PRIORITY: Heralds (dangerous DLC bosses)
		// Always target Herald first when one is present
		isHeraldI := isHerald(monsterI)
		isHeraldJ := isHerald(monsterJ)

		if isHeraldI && !isHeraldJ {
			return true // Herald i has priority
		} else if !isHeraldI && isHeraldJ {
			return false // Herald j has priority
		}

		// MEDIUM PRIORITY: Monster raisers (shamans, generators)
		isPriorityI := IsPriorityMonster(monsterI)
		isPriorityJ := IsPriorityMonster(monsterJ)

		distanceI := ctx.PathFinder.DistanceFromMe(monsterI.Position)
		distanceJ := ctx.PathFinder.DistanceFromMe(monsterJ.Position)

		if distanceI > 2 && distanceJ > 2 {
			if isPriorityI && !isPriorityJ {
				return true
			} else if !isPriorityI && isPriorityJ {
				return false
			}
		}

		// DEFAULT: Sort by distance (closest first)
		return distanceI < distanceJ
	})
}

func ClearAreaAroundPosition(pos data.Position, radius int, filters ...data.MonsterFilter) error {
	ctx := context.Get()
	ctx.SetLastAction("ClearAreaAroundPosition")

	// Disable item pickup at the beginning of the function
	ctx.DisableItemPickup()

	// Defer the re-enabling of item pickup to ensure it happens regardless of how the function exits
	defer ctx.EnableItemPickup()

	return ctx.Char.KillMonsterSequence(func(d game.Data) (data.UnitID, bool) {
		enemies := d.Monsters.Enemies(filters...)

		SortEnemiesByPriority(&enemies)

		for _, m := range enemies {
			distanceToTarget := pather.DistanceFromPoint(pos, m.Position)
			if distanceToTarget > radius {
				continue
			}

			// Special case: Vizier can spawn on weird/off-grid tiles in Chaos Sanctuary.
			isVizier := m.Type == data.MonsterTypeSuperUnique && m.Name == npc.StormCaster

			// Skip monsters that exist in data but are placed on non-walkable tiles (often "underwater/off-grid").
			if !isVizier && !ctx.Data.AreaData.IsWalkable(m.Position) {
				continue
			}

			validEnemy := true
			if !ctx.Data.CanTeleport() {
				// If no path exists, do not target it (prevents chasing "ghost" monsters).
				_, _, pathFound := ctx.PathFinder.GetPath(m.Position)
				if !pathFound {
					validEnemy = false
				}

				// Keep the door check to avoid targeting monsters behind closed doors.
				if hasDoorBetween, _ := ctx.PathFinder.HasDoorBetween(ctx.Data.PlayerUnit.Position, m.Position); hasDoorBetween {
					validEnemy = false
				}
			}

			if validEnemy {
				return m.UnitID, true
			}
		}

		return data.UnitID(0), false
	}, nil)
}

func ClearThroughPath(pos data.Position, radius int, filter data.MonsterFilter) error {
	ctx := context.Get()

	const maxStuckRetries = 5

	// Adaptive timeout based on distance and movement mode.
	// Teleporters cover ground faster, walkers need more time.
	distance := pather.DistanceFromPoint(ctx.Data.PlayerUnit.Position, pos)
	timeout := time.Duration(distance) * time.Second // ~1s per tile as base
	if timeout < 20*time.Second {
		timeout = 20 * time.Second
	}
	if ctx.Data.CanTeleport() {
		// Teleporters are ~3x faster, but still need time for clearing
		if timeout > 30*time.Second {
			timeout = 30 * time.Second
		}
	} else {
		if timeout > 60*time.Second {
			timeout = 60 * time.Second
		}
	}

	lastMovement := false
	stuckRetries := 0
	startTime := time.Now()
	lastProgressPos := ctx.Data.PlayerUnit.Position
	lastProgressTime := time.Now()
	for {
		ctx.PauseIfNotPriority()

		// Absolute timeout
		if time.Since(startTime) > timeout {
			ctx.Logger.Warn("ClearThroughPath: timeout reaching destination",
				slog.Any("destination", pos),
				slog.Duration("elapsed", time.Since(startTime)),
				slog.Duration("timeout", timeout))
			return fmt.Errorf("ClearThroughPath timeout after %v", timeout)
		}

		// Progress check: if we haven't moved >5 tiles in the last 15s, we're stuck
		if pather.DistanceFromPoint(ctx.Data.PlayerUnit.Position, lastProgressPos) > 5 {
			lastProgressPos = ctx.Data.PlayerUnit.Position
			lastProgressTime = time.Now()
		} else if time.Since(lastProgressTime) > 15*time.Second {
			ctx.Logger.Warn("ClearThroughPath: no progress for 15s, giving up",
				slog.Any("position", ctx.Data.PlayerUnit.Position),
				slog.Any("destination", pos))
			return fmt.Errorf("ClearThroughPath no progress for 15s")
		}

		ClearAreaAroundPosition(ctx.Data.PlayerUnit.Position, radius, filter)

		if lastMovement {
			return nil
		}

		path, _, found := ctx.PathFinder.GetPath(pos)
		if !found {
			return fmt.Errorf("path could not be calculated")
		}

		movementDistance := radius
		if radius > len(path) {
			movementDistance = len(path)
		}

		dest := data.Position{
			X: path[movementDistance-1].X + ctx.Data.AreaData.OffsetX,
			Y: path[movementDistance-1].Y + ctx.Data.AreaData.OffsetY,
		}

		// Let's handle the last movement logic to MoveTo function, we will trust the pathfinder because
		// it can finish within a bigger distance than we expect (because blockers), so we will just check how far
		// we should be after the latest movement in a theoretical way
		if len(path)-movementDistance <= step.DistanceToFinishMoving {
			lastMovement = true
		}
		// Increasing DistanceToFinishMoving prevent not being to able to finish movement if our destination is center of a large object like Seal in diablo run.
		// is used only for pathing, attack.go will use default DistanceToFinishMoving
		err := step.MoveTo(dest, step.WithDistanceToFinish(7))
		if err != nil {

			if strings.Contains(err.Error(), "monsters detected in movement path") {
				ctx.Logger.Debug("ClearThroughPath: Movement failed due to monsters, attempting to clear them")
				clearErr := ClearAreaAroundPosition(ctx.Data.PlayerUnit.Position, radius+5, filter)
				if clearErr != nil {
					ctx.Logger.Error(fmt.Sprintf("ClearThroughPath: Failed to clear monsters after movement failure: %v", clearErr))
				} else {
					ctx.Logger.Debug("ClearThroughPath: Successfully cleared monsters, continuing with next iteration")
					continue
				}
			}

			// Stuck recovery with escalation: mild → aggressive based on retry count
			if errors.Is(err, step.ErrPlayerStuck) || errors.Is(err, step.ErrPlayerRoundTrip) {
				stuckRetries++
				if stuckRetries > maxStuckRetries {
					ctx.Logger.Warn("ClearThroughPath: stuck after max retries, giving up",
						slog.Int("retries", stuckRetries),
						slog.Any("destination", pos))
					return err
				}
				ctx.Logger.Debug("ClearThroughPath: stuck, attempting recovery",
					slog.Int("retry", stuckRetries),
					slog.Any("position", ctx.Data.PlayerUnit.Position))

				if ctx.Data.CanTeleport() && !ctx.Data.PlayerUnit.Area.IsTown() {
					if stuckRetries >= 3 {
						// Escalated recovery: teleport farther in direction of destination
						ctx.PathFinder.DirectionalTeleport(pos)
					} else {
						ctx.PathFinder.RandomTeleport()
					}
				} else {
					ctx.PathFinder.RandomMovement()
					time.Sleep(200 * time.Millisecond)
					if stuckRetries >= 3 {
						// Extra random moves to break out of tight spots
						ctx.PathFinder.RandomMovement()
						time.Sleep(300 * time.Millisecond)
					}
				}
				continue
			}

			return err
		}
		// Successful step — reset stuck counter
		stuckRetries = 0
	}
}
