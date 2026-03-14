package action

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/npc"
	"github.com/hectorgimenez/d2go/pkg/data/object"
	"github.com/hectorgimenez/d2go/pkg/data/stat"
	"github.com/hectorgimenez/koolo/internal/action/step"
	"github.com/hectorgimenez/koolo/internal/context"
	"github.com/hectorgimenez/koolo/internal/game"
	"github.com/hectorgimenez/koolo/internal/utils"
)

var errRoomUnreachable = fmt.Errorf("room center unreachable after all attempts")

var interactableShrines = []object.ShrineType{
	object.ExperienceShrine,
	object.StaminaShrine,
	object.ManaRegenShrine,
	object.SkillShrine,
	object.RefillShrine,
	object.HealthShrine,
	object.ManaShrine,
}

func ClearCurrentLevel(openChests bool, filter data.MonsterFilter) error {
	return ClearCurrentLevelEx(openChests, filter, nil)
}

func ClearCurrentLevelEx(openChests bool, filter data.MonsterFilter, shouldInterrupt func() bool) error {
	ctx := context.Get()
	ctx.SetLastAction("ClearCurrentLevel")

	// Per-run OpenChests=false overrides global chest settings for this run.
	// Temporarily disable globals so MoveTo navigation also respects per-run setting.
	if !openChests {
		origChests := ctx.CharacterCfg.Game.InteractWithChests
		origSuperChests := ctx.CharacterCfg.Game.InteractWithSuperChests
		ctx.CharacterCfg.Game.InteractWithChests = false
		ctx.CharacterCfg.Game.InteractWithSuperChests = false
		defer func() {
			ctx.CharacterCfg.Game.InteractWithChests = origChests
			ctx.CharacterCfg.Game.InteractWithSuperChests = origSuperChests
		}()
	}

	openAllChests := ctx.CharacterCfg.Game.InteractWithChests
	openSuperOnly := ctx.CharacterCfg.Game.InteractWithSuperChests && !openAllChests

	// We can make this configurable later, but 20 is a good starting radius.
	const pickupRadius = 20

	traverser := ctx.PathFinder.NewRoomTraverser(filter)
	for {
		r, hasMore := traverser.NextRoom()
		if !hasMore {
			break
		}

		if errDeath := checkPlayerDeath(ctx); errDeath != nil {
			return errDeath
		}
		if shouldInterrupt != nil && shouldInterrupt() {
			return nil
		}

		// First, clear the room of monsters
		err := clearRoom(r, filter)
		if err == errRoomUnreachable {
			// Skip entire cluster of rooms near the unreachable target
			// to avoid wasting minutes trying adjacent rooms in the same area
			const skipRadius = 30
			skipped := traverser.SkipNearbyRooms(r.GetCenter(), skipRadius)
			ctx.Logger.Warn("Room unreachable, skipping nearby cluster",
				slog.Any("roomCenter", r.GetCenter()),
				slog.Int("skippedRooms", skipped),
				slog.Int("skipRadius", skipRadius))
		} else if err != nil {
			ctx.Logger.Warn("Failed to clear room", slog.Any("error", err))
		}

		//ctx.Logger.Debug(fmt.Sprintf("Clearing room complete, attempting to pickup items in a radius of %d", pickupRadius))
		err = ItemPickup(pickupRadius)
		if err != nil {
			ctx.Logger.Warn("Failed to pickup items", slog.Any("error", err))
		}

		// Iterate through objects in the current room
		for _, o := range ctx.Data.Objects {
			if r.IsInside(o.Position) {
				shouldOpen := false
				if o.Selectable {
					// Global settings override per-run openChests.
					switch {
					case openSuperOnly:
						shouldOpen = o.IsSuperChest()
					case openAllChests:
						shouldOpen = o.IsChest() || o.IsSuperChest()
					case openChests:
						shouldOpen = o.IsChest()
					}
				}

				if shouldOpen {
					ctx.Logger.Debug(fmt.Sprintf(
						"Found chest. attempting to interact. Name=%s.\nID=%v UnitID=%v Pos=%v,%v Area='%s' InteractType=%v",
						o.Desc().Name,
						o.Name,
						o.ID,
						o.Position.X,
						o.Position.Y,
						ctx.Data.PlayerUnit.Area.Area().Name,
						o.InteractType,
					))

					err = MoveToCoords(o.Position)
					if err != nil {
						ctx.Logger.Warn("Failed moving to chest", slog.Any("error", err))
						continue
					}

					// Clear nearby monsters before opening (prevents stuck when filter skipped white mobs)
					if enemyFound, _ := IsAnyEnemyAroundPlayer(10); enemyFound {
						ClearAreaAroundPlayer(10, nil)
					}

					err = InteractObject(o, func() bool {
						chest, _ := ctx.Data.Objects.FindByID(o.ID)
						return !chest.Selectable
					})
					if err != nil {
						ctx.Logger.Warn("Failed interacting with chest", slog.Any("error", err))
					}

					// Add small delay to allow the game to open the chest and drop the content
					utils.Sleep(500)
				}
			}
		}
	}

	return nil
}

func clearRoom(room data.Room, filter data.MonsterFilter) error {
	ctx := context.Get()
	ctx.SetLastAction("clearRoom")

	const (
		maxRoomCenterAttempts  = 2              // Max attempts to reach room center (reduced from 3)
		maxClearIterations     = 50             // Safety limit: max monster kill iterations per room
		stuckPositionThreshold = 5              // If in same position for this many iterations, we're stuck
		stuckTimeoutSeconds    = 60             // If position doesn't change for 60s, skip room
		maxRoomTotalSeconds    = 120            // Absolute max time per room (immune to town trip resets)
	)

	roomStartTime := time.Now()

	// Attempt to move to room center with retries
	movedToCenter := false
	for attempt := 0; attempt < maxRoomCenterAttempts; attempt++ {
		path, _, found := ctx.PathFinder.GetClosestWalkablePath(room.GetCenter())
		if !found {
			ctx.Logger.Debug("No path to room center, will clear from current position",
				slog.Int("attempt", attempt+1),
				slog.Any("roomCenter", room.GetCenter()))
			break
		}

		to := data.Position{
			X: path.To().X + ctx.Data.AreaOrigin.X,
			Y: path.To().Y + ctx.Data.AreaOrigin.Y,
		}

		err := MoveToCoords(to, step.WithMonsterFilter(filter), step.WithTimeout(20*time.Second))
		if err == nil {
			movedToCenter = true
			break
		}

		ctx.Logger.Debug("Failed moving to room center, retrying",
			slog.Int("attempt", attempt+1),
			slog.String("error", err.Error()))
	}

	if !movedToCenter {
		// Check if there are any monsters we can clear from current position
		nearbyMonsters := getMonstersInRoom(room, filter)
		if len(nearbyMonsters) == 0 {
			ctx.Logger.Warn("Room unreachable and no nearby monsters, skipping",
				slog.Any("roomCenter", room.GetCenter()))
			return errRoomUnreachable
		}
		ctx.Logger.Debug("Could not reach room center, clearing nearby monsters from current position",
			slog.Int("monstersFound", len(nearbyMonsters)))
	}

	// Track stuck detection (both iteration-based and time-based)
	var lastPlayerPos data.Position
	stuckCounter := 0
	iterationCount := 0
	lastPositionChangeTime := time.Now()
	var lastRecordedPos data.Position

	// Path cache: avoid expensive A* for the same target on consecutive iterations
	var cachedPathTargetID data.UnitID
	var cachedPathValid bool
	var cachedPathTime time.Time

	// Main clearing loop with safety limits
	for {
		ctx.PauseIfNotPriority()
		if err := checkPlayerDeath(ctx); err != nil {
			return err
		}

		// Get monsters once per iteration — reused for all checks below
		monsters := getMonstersInRoom(room, filter)

		// Absolute room timeout — immune to town trip position resets
		if time.Since(roomStartTime) > maxRoomTotalSeconds*time.Second {
			ctx.Logger.Warn("Room total timeout reached, skipping room",
				slog.Duration("elapsed", time.Since(roomStartTime)),
				slog.Int("monstersRemaining", len(monsters)),
				slog.Any("roomCenter", room.GetCenter()))
			return nil
		}

		// Track position for time-based stuck detection
		// Safety: only apply timeout when NOT in town (defensive check)
		currentPos := ctx.Data.PlayerUnit.Position
		inTown := ctx.Data.PlayerUnit.Area.IsTown()

		if !inTown {
			if currentPos != lastRecordedPos {
				lastRecordedPos = currentPos
				lastPositionChangeTime = time.Now()
			} else {
				// Position hasn't changed, check timeout
				stuckDuration := time.Since(lastPositionChangeTime)
				if stuckDuration > stuckTimeoutSeconds*time.Second {
					ctx.Logger.Warn("Position stuck timeout - skipping room",
						slog.Duration("stuckFor", stuckDuration),
						slog.Int("monstersRemaining", len(monsters)),
						slog.Any("stuckPosition", currentPos))
					return nil
				}
			}
		}

		// Safety: prevent infinite loop
		iterationCount++
		if iterationCount > maxClearIterations {
			ctx.Logger.Warn("Room clear iteration limit reached, moving on",
				slog.Int("iterations", iterationCount),
				slog.Int("monstersRemaining", len(monsters)))
			return nil
		}

		if len(monsters) == 0 {
			return nil
		}

		SortEnemiesByPriority(&monsters)

		// Find valid target (priority already handled by SortEnemiesByPriority)
		// Herald > Monster Raisers > Others (by distance)
		targetMonster := data.Monster{}
		for _, m := range monsters {
			if !ctx.Char.ShouldIgnoreMonster(m) {
				targetMonster = m
				break // Take first non-ignored monster (already sorted by priority)
			}
		}

		if targetMonster.UnitID == 0 {
			// No valid targets (all ignored/unreachable), done
			return nil
		}

		// Check if we can path to the monster — skip expensive A* if same target checked recently
		if targetMonster.UnitID != cachedPathTargetID || time.Since(cachedPathTime) > 2*time.Second {
			_, _, mPathFound := ctx.PathFinder.GetPath(targetMonster.Position)
			cachedPathTargetID = targetMonster.UnitID
			cachedPathValid = mPathFound
			cachedPathTime = time.Now()
		}
		if !cachedPathValid {
			ctx.Logger.Debug("No path to monster, skipping",
				slog.String("monster", string(targetMonster.Name)),
				slog.Any("position", targetMonster.Position))

			// Skip this monster and continue to next
			// Mark position to detect if we're looping on unreachable monsters
			currentPos := ctx.Data.PlayerUnit.Position
			if currentPos == lastPlayerPos {
				stuckCounter++
				if stuckCounter >= stuckPositionThreshold {
					ctx.Logger.Warn("Stuck trying to reach unreachable monsters, skipping room",
						slog.Int("stuckCount", stuckCounter))
					return nil
				}
			} else {
				stuckCounter = 0
				lastPlayerPos = currentPos
			}
			continue
		}

		// Handle doors blocking path
		if !ctx.Data.CanTeleport() {
			hasDoorBetween, door := ctx.PathFinder.HasDoorBetween(ctx.Data.PlayerUnit.Position, targetMonster.Position)
			if hasDoorBetween && door.Selectable {
				ctx.Logger.Debug("Door is blocking the path to the monster, moving closer")
				MoveTo(func() (data.Position, bool) { return door.Position, true })
			}
		}

		// Kill the monster
		ctx.Char.KillMonsterSequence(func(d game.Data) (data.UnitID, bool) {
			m, found := d.Monsters.FindByID(targetMonster.UnitID)
			if found && m.Stats[stat.Life] > 0 {
				return targetMonster.UnitID, true
			}
			return 0, false
		}, nil)

		// Reset iteration-based stuck counter after successful kill attempt
		// (time-based tracking is handled at top of loop)
		newPos := ctx.Data.PlayerUnit.Position
		if newPos != lastPlayerPos {
			stuckCounter = 0
			lastPlayerPos = newPos
		}
	}
}

func getMonstersInRoom(room data.Room, filter data.MonsterFilter) []data.Monster {
	ctx := context.Get()
	ctx.SetLastAction("getMonstersInRoom")

	monstersInRoom := make([]data.Monster, 0)
	for _, m := range ctx.Data.Monsters.Enemies(filter) {
		// Fix operator precedence: alive AND (in room OR close to player).
		if m.Stats[stat.Life] <= 0 {
			continue
		}
		if !(room.IsInside(m.Position) || ctx.PathFinder.DistanceFromMe(m.Position) < 30) {
			continue
		}

		// Skip monsters that exist in data but are placed on non-walkable tiles (often "underwater/off-grid").
		// Keep Vizier exception (Chaos Sanctuary).
		isVizier := m.Type == data.MonsterTypeSuperUnique && m.Name == npc.StormCaster
		if !isVizier && !ctx.Data.AreaData.IsWalkable(m.Position) {
			continue
		}

		monstersInRoom = append(monstersInRoom, m)
	}

	return monstersInRoom
}
