package action

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"local/internal/svc/internal/action/step"
	"local/internal/svc/internal/context"
	"local/internal/svc/internal/drop"
	"local/internal/svc/internal/event"
	"local/internal/svc/internal/game"
	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/area"
	"local/internal/svc/internal/gamelib/data/npc"
	"local/internal/svc/internal/utils"
)

const npcTradeTargetDistance = 3

func InteractNPC(npcID npc.ID) (err error) {
	defer deferredTrace(fmt.Sprintf("InteractNPC:%d", npcID), &err)()

	ctx := context.Get()
	ctx.SetLastAction("InteractNPC")

	pos, found := getNPCPosition(npcID, ctx.Data)
	if !found {
		return fmt.Errorf("npc with ID %d not found", npcID)
	}

	for range 5 {
		err = MoveToCoords(pos)
		if err != nil {
			continue
		}

		err = step.InteractNPC(npcID)
		if err != nil {
			continue
		}
		break
	}
	if err != nil {
		return err
	}

	event.Send(event.InteractedTo(event.Text(ctx.Name, ""), int(npcID), event.InteractionTypeNPC))

	return nil
}

func InteractObject(o data.Object, isCompletedFn func() bool) error {
	ctx := context.Get()
	ctx.SetLastAction("InteractObject")

	startingArea := ctx.Data.PlayerUnit.Area

	pos := o.Position
	distFinish := step.DistanceToFinishMoving
	if ctx.Data.PlayerUnit.Area == area.RiverOfFlame && o.IsWaypoint() {
		pos = data.Position{X: 7800, Y: 5919}
		o.ID = 0
		// Special case for seals: we cant teleport directly to center. Interaction range is bigger then DistanceToFinishMoving so we modify it
	} else if strings.Contains(o.Desc().Name, "Seal") {
		distFinish = 10
	}

	var err error
	for range 5 {
		if o.IsWaypoint() && !ctx.Data.AreaData.Area.IsTown() {
			err = MoveToCoords(pos)
			if err != nil {
				if errors.Is(err, drop.ErrInterrupt) {
					return err
				}
				continue
			}
		} else {
			err = step.MoveTo(pos, step.WithDistanceToFinish(distFinish), step.WithIgnoreMonsters())
			if err != nil {
				if errors.Is(err, drop.ErrInterrupt) {
					return err
				}
				continue
			}
		}

		err = step.InteractObject(o, isCompletedFn)
		if err != nil {
			if errors.Is(err, drop.ErrInterrupt) {
				return err
			}
			continue
		}
		break
	}

	if err != nil {
		ctx.Logger.Debug("InteractObject step.InteractObject returned error",
			"object", o.Name,
			"error", err)
		return err
	}

	// Refresh game data to get the final area state after interaction
	ctx.RefreshGameData()

	// If we transitioned to a new area (portal interaction), ensure collision data is loaded
	if ctx.Data.PlayerUnit.Area != startingArea {

		// Initial delay to allow server to fully sync area data
		utils.Sleep(500)
		ctx.RefreshGameData()

		// Wait up to 3 seconds for collision grid to load and be valid
		deadline := time.Now().Add(3 * time.Second)
		gridLoaded := false
		for time.Now().Before(deadline) {
			ctx.RefreshGameData()

			// Verify collision grid exists, is not nil, and has valid dimensions
			if ctx.Data.AreaData.Grid != nil &&
				ctx.Data.AreaData.Grid.CollisionGrid != nil &&
				len(ctx.Data.AreaData.Grid.CollisionGrid) > 0 {
				gridLoaded = true
				break
			}
			utils.Sleep(100)
		}

		if !gridLoaded {
			ctx.Logger.Warn("Collision grid did not load within timeout",
				"area", ctx.Data.PlayerUnit.Area,
				"timeout", "3s")
		}
	}

	return nil
}

func InteractObjectByID(id data.UnitID, isCompletedFn func() bool) error {
	ctx := context.Get()
	ctx.SetLastAction("InteractObjectByID")

	o, found := ctx.Data.Objects.FindByID(id)
	if !found {
		return fmt.Errorf("object with ID %d not found", id)
	}

	return InteractObject(o, isCompletedFn)
}

// SelectNPCOption writes the raw ActionID field into AMB NPCAction (0x38).
// Standard service menus should use SelectNPCTradeOption/SelectNPCGambleOption
// or the PacketSender service wrappers, which resolve ActionID from the AMB
// NPC-service map. This raw helper remains for quest-specific dialog entries.
func SelectNPCOption(option uint32, npcID npc.ID) {
	ctx := context.Get()
	if ctx.PacketSender == nil {
		return
	}
	npcUnit, found := ctx.Data.Monsters.FindOne(npcID, data.MonsterTypeNone)
	if !found {
		ctx.Logger.Warn("SelectNPCOption: NPC not found", "npc", npcID, "option", option)
		return
	}
	settleNPCDialogBeforeAction(ctx, npcID, npcUnit.UnitID, 850*time.Millisecond)
	if err := ctx.PacketSender.NPCAction(option, npcUnit.UnitID); err != nil {
		ctx.Logger.Warn("SelectNPCOption packet failed", "npc", npcID, "option", option, "err", err)
	}
}

// SelectNPCTradeOption selects the Trade service from an already-open NPC
// dialog via AMB NPCAction: opcode 0x38 with ActionID=0x01. No HID fallback.
//
// Per AMB NPCAction docs: Trade is at menu position 0x01 for ALL trade NPCs
// (Gheed/Charsi/Akara/Fara/Hratli/Jamella/Halbu/Larzuk/Anya).
func SelectNPCTradeOption(npcID npc.ID) bool {
	ctx := context.Get()
	if ctx.PacketSender == nil {
		ctx.Logger.Warn("SelectNPCTradeOption: PacketSender nil", "npc", npcID)
		return false
	}
	townNPC, distance, err := standNextToNPC(ctx, npcID, npcTradeTargetDistance)
	if err != nil {
		ctx.Logger.Warn("SelectNPCTradeOption: cannot stand next to NPC before trade",
			"npc", npcID, "err", err)
		return false
	}
	if ctx.Data.OpenMenus.NPCShop {
		ctx.Logger.Info("SelectNPCTradeOption: NPCShop already open",
			"npc", npcID, "npcGID", townNPC.UnitID, "distance", distance)
		return true
	}
	if !ctx.Data.OpenMenus.NPCInteract {
		townNPC, distance, err = openTradeNPCDialog(ctx, npcID)
		if err != nil {
			ctx.Logger.Warn("SelectNPCTradeOption: NPC dialog is not open and AMB open flow failed",
				"npc", npcID, "err", err)
			return false
		}
	}
	option := uint32(1)
	beforeNPC := ctx.Data.OpenMenus.NPCInteract
	beforeShop := ctx.Data.OpenMenus.NPCShop
	ctx.Logger.Info("SelectNPCTradeOption: selecting Trade via AMB 0x38 ActionID=1",
		"npc", npcID, "option", option, "npcGID", townNPC.UnitID, "distance", distance,
		"NPCInteract", beforeNPC, "NPCShop", beforeShop)
	settleNPCDialogBeforeAction(ctx, npcID, townNPC.UnitID, 850*time.Millisecond)
	if err := ctx.PacketSender.NPCTradeFor(uint32(npcID), townNPC.UnitID); err != nil {
		ctx.Logger.Warn("SelectNPCTradeOption: AMB 0x38 send failed",
			"npc", npcID, "option", option, "npcGID", townNPC.UnitID, "err", err)
		return false
	}
	afterShop := waitForNPCShop(ctx, 30)
	if !afterShop {
		ctx.Logger.Warn("SelectNPCTradeOption: 0x38 sent at close range but NPCShop did NOT open",
			"npc", npcID, "option", option, "distance", distance,
			"NPCShop_before", beforeShop, "NPCShop_after", afterShop)
	} else {
		ctx.Logger.Info("SelectNPCTradeOption: NPCShop opened ✓",
			"npc", npcID, "option", option)
	}
	return afterShop
}

func settleNPCDialogBeforeAction(ctx *context.Status, npcID npc.ID, unitID data.UnitID, delay time.Duration) {
	ctx.Logger.Debug("NPC dialog settle before 0x38",
		"npc", npcID,
		"npcGID", unitID,
		"delay", delay.String(),
		"NPCInteract", ctx.Data.OpenMenus.NPCInteract,
		"NPCShop", ctx.Data.OpenMenus.NPCShop)
	deadline := time.Now().Add(delay)
	for time.Now().Before(deadline) {
		utils.Sleep(100)
		ctx.RefreshGameData()
		if ctx.Data.OpenMenus.NPCShop {
			return
		}
	}
}

func waitForNPCDialog(ctx *context.Status, attempts int) bool {
	for i := 0; i < attempts; i++ {
		utils.Sleep(100)
		ctx.RefreshGameData()
		if ctx.Data.OpenMenus.NPCInteract || ctx.Data.OpenMenus.NPCShop {
			return true
		}
	}
	return false
}

func waitForNPCMenusClosed(ctx *context.Status, attempts int) bool {
	for i := 0; i < attempts; i++ {
		utils.Sleep(100)
		ctx.RefreshGameData()
		if !ctx.Data.OpenMenus.NPCInteract && !ctx.Data.OpenMenus.NPCShop {
			return true
		}
	}
	return false
}

func openTradeNPCDialog(ctx *context.Status, npcID npc.ID) (data.Monster, int, error) {
	townNPC, distance, err := standNextToNPC(ctx, npcID, npcTradeTargetDistance)
	if err != nil {
		return data.Monster{}, distance, err
	}

	ctx.Logger.Debug("Trade NPC dialog open: AMB 0x2F NPCInit",
		"npc", npcID, "npcGID", townNPC.UnitID, "distance", distance)
	playerPos := ctx.Data.PlayerUnit.Position
	ctx.Logger.Debug("Trade NPC dialog open: AMB 0x04 RunToUnit prime",
		"npc", npcID, "npcGID", townNPC.UnitID, "playerX", playerPos.X, "playerY", playerPos.Y)
	if err := ctx.PacketSender.NPCPrimeInteraction(townNPC.UnitID, ctx.Data.PlayerUnit.ID, playerPos, townNPC.Position); err != nil {
		return data.Monster{}, distance, fmt.Errorf("NPCPrimeInteraction failed: %w", err)
	}
	utils.Sleep(150)
	ctx.RefreshGameData()
	if refreshedNPC, ok := ctx.Data.Monsters.FindOne(npcID, data.MonsterTypeNone); ok {
		townNPC = refreshedNPC
	}
	distance = ctx.PathFinder.DistanceFromMe(townNPC.Position)
	if err := ctx.PacketSender.NPCInit(townNPC.UnitID); err != nil {
		return data.Monster{}, distance, fmt.Errorf("NPCInit failed: %w", err)
	}
	utils.Sleep(100)
	ctx.RefreshGameData()
	playerPos = ctx.Data.PlayerUnit.Position
	ctx.Logger.Debug("Trade NPC dialog open: AMB 0x03 dialog position sync",
		"npc", npcID, "npcGID", townNPC.UnitID, "playerX", playerPos.X, "playerY", playerPos.Y)
	if err := ctx.PacketSender.NPCDialogPositionSync(playerPos); err != nil {
		return data.Monster{}, distance, fmt.Errorf("NPCDialogPositionSync failed: %w", err)
	}
	if !waitForNPCDialog(ctx, 20) {
		return data.Monster{}, distance, fmt.Errorf("NPC dialog did not open after NPCInit")
	}
	ctx.RefreshGameData()
	if refreshedNPC, ok := ctx.Data.Monsters.FindOne(npcID, data.MonsterTypeNone); ok {
		townNPC = refreshedNPC
	}
	distance = ctx.PathFinder.DistanceFromMe(townNPC.Position)
	return townNPC, distance, nil
}

func waitForNPCShop(ctx *context.Status, attempts int) bool {
	for i := 0; i < attempts; i++ {
		utils.Sleep(100)
		ctx.RefreshGameData()
		if ctx.Data.OpenMenus.NPCShop {
			return true
		}
	}
	return false
}

func standNextToNPC(ctx *context.Status, npcID npc.ID, targetDistance int) (data.Monster, int, error) {
	ctx.RefreshGameData()
	townNPC, found := ctx.Data.Monsters.FindOne(npcID, data.MonsterTypeNone)
	if !found {
		return data.Monster{}, 0, fmt.Errorf("npc %d not found", npcID)
	}
	distance := ctx.PathFinder.DistanceFromMe(townNPC.Position)
	if distance <= targetDistance {
		return townNPC, distance, nil
	}

	if ctx.Data.OpenMenus.NPCInteract || ctx.Data.OpenMenus.NPCShop {
		if ctx.PacketSender != nil {
			_ = ctx.PacketSender.NPCCancel(townNPC.UnitID)
			utils.Sleep(150)
			ctx.RefreshGameData()
		}
	}

	if err := step.MoveTo(townNPC.Position, step.WithDistanceToFinish(targetDistance), step.WithIgnoreMonsters()); err != nil {
		if packetNPC, packetDistance, ok := packetRunToNPC(ctx, npcID, townNPC, targetDistance); ok {
			return packetNPC, packetDistance, nil
		}
		return data.Monster{}, distance, err
	}
	ctx.RefreshGameData()
	if refreshedNPC, ok := ctx.Data.Monsters.FindOne(npcID, data.MonsterTypeNone); ok {
		townNPC = refreshedNPC
	}
	distance = ctx.PathFinder.DistanceFromMe(townNPC.Position)
	if distance > targetDistance {
		if packetNPC, packetDistance, ok := packetRunToNPC(ctx, npcID, townNPC, targetDistance); ok {
			return packetNPC, packetDistance, nil
		}
		return townNPC, distance, fmt.Errorf("npc %d still too far after move (distance: %d)", npcID, distance)
	}
	return townNPC, distance, nil
}

func packetRunToNPC(ctx *context.Status, npcID npc.ID, townNPC data.Monster, targetDistance int) (data.Monster, int, bool) {
	if ctx.PacketSender == nil {
		return townNPC, ctx.PathFinder.DistanceFromMe(townNPC.Position), false
	}
	playerPos := ctx.Data.PlayerUnit.Position
	ctx.Logger.Warn("standNextToNPC: step move did not close distance; trying AMB 0x04 RunToUnit packet",
		"npc", npcID, "npcGID", townNPC.UnitID, "playerX", playerPos.X, "playerY", playerPos.Y)
	if err := ctx.PacketSender.RunToUnit(1, townNPC.UnitID, uint16(playerPos.X), uint16(playerPos.Y)); err != nil {
		ctx.Logger.Warn("standNextToNPC: RunToUnit packet fallback failed",
			"npc", npcID, "npcGID", townNPC.UnitID, "err", err)
		return townNPC, ctx.PathFinder.DistanceFromMe(townNPC.Position), false
	}
	for i := 0; i < 30; i++ {
		utils.Sleep(100)
		ctx.RefreshGameData()
		if refreshedNPC, ok := ctx.Data.Monsters.FindOne(npcID, data.MonsterTypeNone); ok {
			townNPC = refreshedNPC
		}
		distance := ctx.PathFinder.DistanceFromMe(townNPC.Position)
		if distance <= targetDistance {
			return townNPC, distance, true
		}
	}
	return townNPC, ctx.PathFinder.DistanceFromMe(townNPC.Position), false
}

// CloseNPCDialog sends the NPC-close packet 0x30 via DualSend.
// Full-packet bot: no HID ESC fallback (user 2026-04-19).
func CloseNPCDialog(npcID npc.ID) {
	ctx := context.Get()
	if ctx.PacketSender == nil {
		return
	}
	townNPC, found := ctx.Data.Monsters.FindOne(npcID, data.MonsterTypeNone)
	if !found {
		ctx.Logger.Warn("CloseNPCDialog: NPC not found", "npc", npcID)
		return
	}
	if err := ctx.PacketSender.NPCCancel(townNPC.UnitID); err != nil {
		ctx.Logger.Warn("CloseNPCDialog packet failed", "err", err)
	}
	utils.Sleep(100)
}

// SelectNPCGambleOption selects the Gamble service via AMB NPCAction
// ActionID=0x02. Full-packet bot: no HID fallback.
func SelectNPCGambleOption(npcID npc.ID) {
	ctx := context.Get()
	if ctx.PacketSender == nil {
		return
	}
	townNPC, found := ctx.Data.Monsters.FindOne(npcID, data.MonsterTypeNone)
	if !found {
		ctx.Logger.Warn("SelectNPCGambleOption: NPC not found", "npc", npcID)
		return
	}
	if err := ctx.PacketSender.NPCGambleFor(uint32(npcID), townNPC.UnitID); err != nil {
		ctx.Logger.Warn("SelectNPCGambleOption packet failed", "err", err)
	}
	utils.Sleep(100)
}

func getNPCPosition(npc npc.ID, d *game.Data) (data.Position, bool) {
	monster, found := d.Monsters.FindOne(npc, data.MonsterTypeNone)
	if found {
		return monster.Position, true
	}

	n, found := d.NPCs.FindOne(npc)
	if !found {
		return data.Position{}, false
	}

	return data.Position{X: n.Positions[0].X, Y: n.Positions[0].Y}, true
}
