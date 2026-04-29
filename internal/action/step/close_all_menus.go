package step

import (
	"errors"
	"fmt"

	"github.com/lxn/win"
	"local/internal/svc/internal/context"
	"local/internal/svc/internal/game"
	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/utils"
)

func CloseAllMenus() error {
	ctx := context.Get()
	ctx.SetLastStep("CloseAllMenus")

	attempts := 0
	for ctx.Data.OpenMenus.IsMenuOpen() {
		// Pause the execution if the priority is not the same as the execution priority
		ctx.PauseIfNotPriority()

		ctx.RefreshGameData()
		if attempts > 10 {
			return errors.New("failed closing game menu")
		}
		if err := closePacketAwareMenu(ctx); err != nil {
			return err
		}
		utils.Sleep(200)
		attempts++
	}

	return nil
}

func closePacketAwareMenu(ctx *context.Status) error {
	if ctx.PacketSender != nil && (ctx.Data.OpenMenus.NPCInteract || ctx.Data.OpenMenus.NPCShop) {
		if townNPC, ok := closestVisibleNPC(ctx); ok {
			ctx.Logger.Debug("CloseAllMenus: closing NPC menu via 0x30 NPCCancel",
				"npcGID", townNPC.UnitID,
				"npcName", townNPC.Name,
				"NPCInteract", ctx.Data.OpenMenus.NPCInteract,
				"NPCShop", ctx.Data.OpenMenus.NPCShop)
			if err := ctx.PacketSender.NPCCancel(townNPC.UnitID); err != nil {
				return fmt.Errorf("close NPC menu via NPCCancel: %w", err)
			}
			return nil
		}
	}

	if ctx.PacketSender != nil {
		if ctx.GameReader != nil && ctx.GameReader.HWND != 0 {
			ctx.Logger.Debug("CloseAllMenus: closing generic menu via direct window ESC",
				"waypoint", ctx.Data.OpenMenus.Waypoint,
				"inventory", ctx.Data.OpenMenus.Inventory,
				"stash", ctx.Data.OpenMenus.Stash,
				"quit", ctx.Data.OpenMenus.QuitMenu)
			game.PostWindowKey(ctx.GameReader.HWND, byte(win.VK_ESCAPE))
			return nil
		}
		ctx.Logger.Debug("CloseAllMenus: closing generic menu via in-process ESC",
			"waypoint", ctx.Data.OpenMenus.Waypoint,
			"inventory", ctx.Data.OpenMenus.Inventory,
			"stash", ctx.Data.OpenMenus.Stash,
			"quit", ctx.Data.OpenMenus.QuitMenu)
		if err := ctx.PacketSender.PostKeyInProcess(byte(win.VK_ESCAPE)); err != nil {
			return fmt.Errorf("close menu via in-process ESC: %w", err)
		}
		return nil
	}

	ctx.HID.PressKey(win.VK_ESCAPE)
	return nil
}

func closestVisibleNPC(ctx *context.Status) (data.Monster, bool) {
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
