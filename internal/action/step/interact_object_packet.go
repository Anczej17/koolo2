package step

import (
	"fmt"
	"time"

	"local/internal/svc/internal/context"
	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/area"
	"local/internal/svc/internal/gamelib/data/mode"
	"local/internal/svc/internal/gamelib/data/object"
	"local/internal/svc/internal/packet/amb"
	"local/internal/svc/internal/town"
	"local/internal/svc/internal/utils"
)

const (
	maxPacketInteractionAttempts = 5
	packetPortalSyncDelay        = 200
	maxPacketPortalSyncAttempts  = 15
)

func InteractObjectPacket(obj data.Object, isCompletedFn func() bool) error {
	interactionAttempts := 0
	waitingForInteraction := false
	lastRun := time.Time{}

	ctx := context.Get()
	ctx.SetLastStep("InteractObjectPacket")

	// Track starting area to detect portal transitions
	startingArea := ctx.Data.PlayerUnit.Area

	// If there is no completion check, just assume the interaction is completed after clicking
	if isCompletedFn == nil {
		isCompletedFn = func() bool {
			return waitingForInteraction
		}
	}

	// For portals, we need to ensure proper area sync
	expectedArea := area.ID(0)
	if obj.IsRedPortal() {
		// For red portals, we need to determine the expected destination
		switch {
		case obj.Name == object.PermanentTownPortal && ctx.Data.PlayerUnit.Area == area.StonyField:
			expectedArea = area.Tristram
		case obj.Name == object.PermanentTownPortal && ctx.Data.PlayerUnit.Area == area.RogueEncampment:
			expectedArea = area.MooMooFarm
		case obj.Name == object.PermanentTownPortal && ctx.Data.PlayerUnit.Area == area.Harrogath:
			expectedArea = area.NihlathaksTemple
		case obj.Name == object.PermanentTownPortal && ctx.Data.PlayerUnit.Area == area.FrigidHighlands:
			expectedArea = area.Abaddon
		case obj.Name == object.PermanentTownPortal && ctx.Data.PlayerUnit.Area == area.ArreatPlateau:
			expectedArea = area.PitOfAcheron
		case obj.Name == object.PermanentTownPortal && ctx.Data.PlayerUnit.Area == area.FrozenTundra:
			expectedArea = area.InfernalPit
		case obj.Name == object.PermanentTownPortal && ctx.Data.PlayerUnit.Area == area.ArcaneSanctuary:
			expectedArea = area.CanyonOfTheMagi
		case obj.Name == object.BaalsPortal && ctx.Data.PlayerUnit.Area == area.ThroneOfDestruction:
			expectedArea = area.TheWorldstoneChamber
		case obj.Name == object.DurielsLairPortal && (ctx.Data.PlayerUnit.Area >= area.TalRashasTomb1 && ctx.Data.PlayerUnit.Area <= area.TalRashasTomb7):
			expectedArea = area.DurielsLair
		}
	} else if obj.IsPortal() {
		// For blue town portals, determine the town area based on current area
		fromArea := ctx.Data.PlayerUnit.Area
		if !fromArea.IsTown() {
			expectedArea = town.GetTownByArea(fromArea).TownArea()
		} else {
			// When using portal from town, we need to wait for any non-town area
			isCompletedFn = func() bool {
				return !ctx.Data.PlayerUnit.Area.IsTown() &&
					ctx.Data.AreaData.IsInside(ctx.Data.PlayerUnit.Position) &&
					len(ctx.Data.Objects) > 0
			}
		}
	}

	for !isCompletedFn() {
		ctx.PauseIfNotPriority()

		if interactionAttempts >= maxPacketInteractionAttempts {
			return fmt.Errorf("[%s] failed interacting with object via packet [%v] in Area: [%s]", ctx.Name, obj.Name, ctx.Data.PlayerUnit.Area.Area().Name)
		}

		ctx.RefreshGameData()

		// If we've transitioned areas (portal interaction), the object no longer exists in current area
		// Stop trying to interact and let the completion function handle success
		if ctx.Data.PlayerUnit.Area != startingArea {
			// Don't return error - area transition is expected for portals
			// The isCompletedFn will determine if this was successful
			continue
		}

		// Give some time before retrying the interaction
		if waitingForInteraction && time.Since(lastRun) < time.Millisecond*200 {
			utils.Sleep(200)
			continue
		}

		var o data.Object
		var found bool
		if obj.ID != 0 {
			o, found = ctx.Data.Objects.FindByID(obj.ID)
		}
		if !found {
			// Fallback by name in case the object ID changes during sync (e.g., portal opening).
			o, found = ctx.Data.Objects.FindOne(obj.Name)
			if !found {
				return fmt.Errorf("object %v not found", obj)
			}
		}

		lastRun = time.Now()

		// Check portal states
		if o.IsPortal() || o.IsRedPortal() {
			// If portal is still being created, wait
			if o.Mode == mode.ObjectModeOperating {
				utils.Sleep(100)
				continue
			}

			// Only interact when portal is fully opened
			if o.Mode != mode.ObjectModeOpened {
				utils.Sleep(100)
				continue
			}

			// Send packet interaction (AMB 0x41 portal-interact, 13 B)
			ctx.Logger.Debug("Attempting TP interaction via AMB 0x41 PortalInteract")
			if err := ctx.PacketSender.PortalInteract(o); err != nil {
				ctx.Logger.Error("Packet TP interaction failed", "error", err)
				return fmt.Errorf("failed to interact with portal via packet: %w", err)
			}

			waitingForInteraction = true
			interactionAttempts++

			// For portals with expected area, we need to wait for proper area sync
			if expectedArea != 0 {
				utils.Sleep(500) // Initial delay for area transition
				for attempts := 0; attempts < maxPacketPortalSyncAttempts; attempts++ {
					ctx.RefreshGameData()
					if ctx.Data.PlayerUnit.Area == expectedArea {
						if areaData, ok := ctx.Data.Areas[expectedArea]; ok {
							if areaData.IsInside(ctx.Data.PlayerUnit.Position) {
								if expectedArea.IsTown() {
									return nil // For town areas, we can return immediately
								}
								// For special areas, ensure we have proper object data loaded
								if len(ctx.Data.Objects) > 0 {
									return nil
								}
							}
						}
					}
					utils.Sleep(packetPortalSyncDelay)
				}
				return fmt.Errorf("portal sync timeout - expected area: %v, current: %v", expectedArea, ctx.Data.PlayerUnit.Area)
			}
		} else if o.IsWaypoint() {
			ctx.Logger.Debug("Attempting waypoint interaction via AMB object packet",
				"object", o.Name,
				"objectID", o.ID,
				"attempt", interactionAttempts+1)
			if err := sendObjectInteractionPacket(ctx, o, interactionAttempts); err != nil {
				ctx.Logger.Error("Packet object interaction failed", "error", err, "object", o.Name, "objectID", o.ID)
				return fmt.Errorf("failed to interact with object via packet: %w", err)
			}

			waitingForInteraction = true
			interactionAttempts++
			utils.Sleep(250)
		} else {
			if ctx.PathFinder != nil {
				if distance := ctx.PathFinder.DistanceFromMe(o.Position); distance > 5 {
					playerPos := ctx.Data.PlayerUnit.Position
					ctx.Logger.Debug("Packet object interaction: AMB 0x04 RunToUnit prime",
						"object", o.Name,
						"objectID", o.ID,
						"distance", distance,
						"playerX", playerPos.X,
						"playerY", playerPos.Y)
					if err := ctx.PacketSender.RunToUnit(2, o.ID, uint16(playerPos.X), uint16(playerPos.Y)); err != nil {
						return fmt.Errorf("move to object via AMB 0x04: %w", err)
					}
					utils.Sleep(250)
					ctx.RefreshGameData()
					continue
				}
			}

			ctx.Logger.Debug("Attempting object interaction via AMB object packet",
				"object", o.Name,
				"objectID", o.ID,
				"mode", o.Mode,
				"attempt", interactionAttempts+1)
			if err := sendObjectInteractionPacket(ctx, o, interactionAttempts); err != nil {
				ctx.Logger.Error("Packet object interaction failed", "error", err, "object", o.Name, "objectID", o.ID)
				return fmt.Errorf("failed to interact with object via packet: %w", err)
			}

			waitingForInteraction = true
			interactionAttempts++
			utils.Sleep(250)
		}
	}

	return nil
}

func sendObjectInteractionPacket(ctx *context.Status, o data.Object, attempt int) error {
	preferSimple := amb.ShouldUseSimpleInteraction(o)

	switch {
	case attempt == 0 && preferSimple:
		ctx.Logger.Debug("Object interaction packet: AMB 0x40 SimpleObjectInteract", "object", o.Name, "objectID", o.ID)
		return ctx.PacketSender.SimpleObjectInteract(o)
	case attempt == 0:
		ctx.Logger.Debug("Object interaction packet: AMB 0x41 ObjectInteractEx", "object", o.Name, "objectID", o.ID)
		return ctx.PacketSender.ObjectInteractEx(o)
	case attempt == 1 && preferSimple:
		ctx.Logger.Debug("Object interaction retry packet: AMB 0x41 ObjectInteractEx", "object", o.Name, "objectID", o.ID)
		return ctx.PacketSender.ObjectInteractEx(o)
	case attempt == 1:
		ctx.Logger.Debug("Object interaction retry packet: AMB 0x40 SimpleObjectInteract", "object", o.Name, "objectID", o.ID)
		return ctx.PacketSender.SimpleObjectInteract(o)
	default:
		ctx.Logger.Debug("Object interaction retry packet: AMB 0x13 ObjectInteract", "object", o.Name, "objectID", o.ID)
		return ctx.PacketSender.ObjectInteract0x13(o, ctx.Data.PlayerUnit.ID)
	}
}
