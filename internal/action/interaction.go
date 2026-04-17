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
	"github.com/lxn/win"
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
// tradeIndex: 0 = first option (Jamella), 1 = second option (most vendors).
func SelectNPCTradeOption(npcID npc.ID) {
	ctx := context.Get()

	if ctx.CharacterCfg.PacketCasting.UseForNPCInteraction && ctx.PacketSender != nil {
		townNPC, found := ctx.Data.Monsters.FindOne(npcID, data.MonsterTypeNone)
		if found {
			if err := ctx.PacketSender.NPCDialogOption(1, townNPC.UnitID); err == nil {
				utils.Sleep(100)
				return
			}
			ctx.Logger.Warn("NPC dialog option packet failed, falling back to HID")
		}
	}

	// HID fallback
	if npcID == npc.Jamella {
		ctx.HID.KeySequence(win.VK_HOME, win.VK_RETURN)
	} else {
		ctx.HID.KeySequence(win.VK_HOME, win.VK_DOWN, win.VK_RETURN)
	}
}

// CloseNPCDialog sends the NPC close packet 0x30 via packet.
// Falls back to ESC key if packet fails.
func CloseNPCDialog(npcID npc.ID) {
	ctx := context.Get()

	if ctx.CharacterCfg.PacketCasting.UseForNPCInteraction && ctx.PacketSender != nil {
		townNPC, found := ctx.Data.Monsters.FindOne(npcID, data.MonsterTypeNone)
		if found {
			if err := ctx.PacketSender.TerminateNPCChat(townNPC.UnitID); err == nil {
				utils.Sleep(100)
				return
			}
		}
	}

	// HID fallback
	step.CloseAllMenus()
}

// SelectNPCGambleOption sends the "Gamble" dialog option via packet.
// Gamble is option=2 for most NPCs, option=1 for Jamella.
// Falls back to HID KeySequence if packet fails.
func SelectNPCGambleOption(npcID npc.ID) {
	ctx := context.Get()

	if ctx.CharacterCfg.PacketCasting.UseForGamble && ctx.PacketSender != nil {
		townNPC, found := ctx.Data.Monsters.FindOne(npcID, data.MonsterTypeNone)
		if found {
			option := uint32(2) // gamble is usually 2nd option
			if npcID == npc.Jamella {
				option = 1
			}
			if err := ctx.PacketSender.NPCDialogOption(option, townNPC.UnitID); err == nil {
				utils.Sleep(100)
				return
			}
			ctx.Logger.Warn("NPC gamble option packet failed, falling back to HID")
		}
	}

	// HID fallback
	if npcID == npc.Jamella {
		ctx.HID.KeySequence(win.VK_HOME, win.VK_DOWN, win.VK_RETURN)
	} else {
		ctx.HID.KeySequence(win.VK_HOME, win.VK_DOWN, win.VK_DOWN, win.VK_RETURN)
	}
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
