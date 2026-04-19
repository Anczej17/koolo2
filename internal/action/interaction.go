package action

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/area"
	"local/internal/svc/internal/gamelib/data/npc"
	"local/internal/svc/internal/action/step"
	"local/internal/svc/internal/context"
	"local/internal/svc/internal/drop"
	"local/internal/svc/internal/event"
	"local/internal/svc/internal/game"
	"local/internal/svc/internal/utils"
)

func InteractNPC(npc npc.ID) error {
	ctx := context.Get()
	ctx.SetLastAction("InteractNPC")

	pos, found := getNPCPosition(npc, ctx.Data)
	if !found {
		return fmt.Errorf("npc with ID %d not found", npc)
	}

	var err error
	for range 5 {
		err = MoveToCoords(pos)
		if err != nil {
			continue
		}

		err = step.InteractNPC(npc)
		if err != nil {
			continue
		}
		break
	}
	if err != nil {
		return err
	}

	event.Send(event.InteractedTo(event.Text(ctx.Name, ""), int(npc), event.InteractionTypeNPC))

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

// SelectNPCTradeOption sends the "Trade" dialog option via packet 0x38.
// Falls back to HID KeySequence if packet fails or is not available.
// SelectNPCOption sends a generic NPC dialog option (0x38) for `npcID`.
// Replaces any HID.KeySequence(VK_HOME, VK_DOWN..., VK_RETURN) navigation
// pattern. option=0 → first menu item, 1 → second, 2 → third, etc.
// Full-packet bot: returns silently on missing PacketSender / NPC (no HID
// fallback per user mandate 2026-04-19).
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
	if err := ctx.PacketSender.NPCDialogOption(option, npcUnit.UnitID); err != nil {
		ctx.Logger.Warn("SelectNPCOption packet failed", "npc", npcID, "option", option, "err", err)
	}
}

// SelectNPCTradeOption sends the "Trade" dialog option via 0x38 packet.
// Full-packet bot: no HID fallback (user 2026-04-19).
// tradeIndex: 0 = first option (Jamella), 1 = second option (most vendors).
func SelectNPCTradeOption(npcID npc.ID) {
	ctx := context.Get()
	if ctx.PacketSender == nil {
		return
	}
	townNPC, found := ctx.Data.Monsters.FindOne(npcID, data.MonsterTypeNone)
	if !found {
		ctx.Logger.Warn("SelectNPCTradeOption: NPC not found in monster table", "npc", npcID)
		return
	}
	option := uint32(1) // most vendors: Trade is second option
	if npcID == npc.Jamella {
		option = 0 // Jamella: Trade is first
	}
	if err := ctx.PacketSender.NPCDialogOption(option, townNPC.UnitID); err != nil {
		ctx.Logger.Warn("SelectNPCTradeOption packet failed", "err", err)
	}
	utils.Sleep(100)
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
	if err := ctx.PacketSender.TerminateNPCChat(townNPC.UnitID); err != nil {
		ctx.Logger.Warn("CloseNPCDialog packet failed", "err", err)
	}
	utils.Sleep(100)
}

// SelectNPCGambleOption sends the "Gamble" dialog option via 0x38 packet.
// Gamble is option=2 for most NPCs, option=1 for Jamella.
// Full-packet bot: no HID fallback (user 2026-04-19).
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
	option := uint32(2) // gamble usually 2nd option
	if npcID == npc.Jamella {
		option = 1
	}
	if err := ctx.PacketSender.NPCDialogOption(option, townNPC.UnitID); err != nil {
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
