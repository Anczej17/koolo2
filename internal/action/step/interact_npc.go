package step

import (
	"fmt"

	"local/internal/svc/internal/context"
	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/npc"
	"local/internal/svc/internal/utils"
)

func InteractNPC(npcID npc.ID) error {
	ctx := context.Get()
	ctx.SetLastStep("InteractNPC")

	const (
		maxDistance   = 6
		standDistance = 2
	)

	// Packet-based NPC interaction — use AMB's canonical NPCInit flow when a
	// sender is available. We intentionally do not mask packet failures with
	// HID so live validation stays unambiguous.
	if ctx.PacketSender != nil {
		for attempt := 0; attempt < 4; attempt++ {
			ctx.RefreshGameData()
			townNPC, found := ctx.Data.Monsters.FindOne(npcID, data.MonsterTypeNone)
			if !found {
				return fmt.Errorf("packet NPC interaction failed: npc %d not found", npcID)
			}
			distance := ctx.PathFinder.DistanceFromMe(townNPC.Position)
			if distance > standDistance {
				ctx.Logger.Debug("NPC packet interaction: moving next to NPC",
					"npc", npcID, "unitID", townNPC.UnitID, "distance", distance, "targetDistance", standDistance, "attempt", attempt+1)
				if err := MoveTo(townNPC.Position, WithDistanceToFinish(standDistance), WithIgnoreMonsters()); err != nil {
					return fmt.Errorf("packet NPC interaction failed: move next to npc %d failed: %w", npcID, err)
				}
				ctx.RefreshGameData()
				if refreshedNPC, ok := ctx.Data.Monsters.FindOne(npcID, data.MonsterTypeNone); ok {
					townNPC = refreshedNPC
				}
				distance = ctx.PathFinder.DistanceFromMe(townNPC.Position)
			}
			if distance > maxDistance {
				return fmt.Errorf("packet NPC interaction failed: npc %d out of range (distance: %d)", npcID, distance)
			}

			playerPos := ctx.Data.PlayerUnit.Position
			ctx.Logger.Debug("NPC packet interaction: AMB 0x04 RunToUnit prime",
				"npc", npcID, "unitID", townNPC.UnitID, "playerX", playerPos.X, "playerY", playerPos.Y, "attempt", attempt+1)
			if err := ctx.PacketSender.NPCPrimeInteraction(townNPC.UnitID, ctx.Data.PlayerUnit.ID, playerPos, townNPC.Position); err != nil {
				return fmt.Errorf("packet NPC interaction failed: NPCPrimeInteraction send failed for npc %d: %w", npcID, err)
			}
			utils.Sleep(150)
			ctx.RefreshGameData()
			if refreshedNPC, ok := ctx.Data.Monsters.FindOne(npcID, data.MonsterTypeNone); ok {
				townNPC = refreshedNPC
			}
			distance = ctx.PathFinder.DistanceFromMe(townNPC.Position)

			stage := "direct 0x2F"
			var interactErr error
			switch attempt {
			case 0:
				// This is the proven 2026-04-25/26 Akara path: prime movement,
				// then NPCInit directly. Extra 0x40/0x41/0x13 pre-open packets
				// are only retries because they can perturb the native NPC state.
			case 1:
				stage = "0x41 NPCInteractEx"
				interactErr = ctx.PacketSender.NPCInteractEx(townNPC)
			case 2:
				stage = "0x40 UnitInteract"
				interactErr = ctx.PacketSender.UnitInteract(townNPC.UnitID)
			default:
				stage = "0x13 NPCInteract"
				interactErr = ctx.PacketSender.NPCInteract0x13(townNPC, ctx.Data.PlayerUnit.ID)
			}
			if attempt > 0 {
				ctx.Logger.Debug("NPC packet interaction: AMB pre-open interaction",
					"npc", npcID, "unitID", townNPC.UnitID, "stage", stage, "distance", distance, "attempt", attempt+1)
				if interactErr != nil {
					return fmt.Errorf("packet NPC interaction failed: %s send failed for npc %d: %w", stage, npcID, interactErr)
				}
				for i := 0; i < 3; i++ {
					utils.Sleep(50)
					ctx.RefreshGameData()
					if ctx.Data.OpenMenus.NPCInteract || ctx.Data.OpenMenus.NPCShop {
						ctx.Logger.Info("NPC dialog opened via packet",
							"npc", npcID, "stage", stage, "waitMs", (i+1)*50, "distance", distance, "attempt", attempt+1)
						return nil
					}
				}
			}

			if refreshedNPC, ok := ctx.Data.Monsters.FindOne(npcID, data.MonsterTypeNone); ok {
				townNPC = refreshedNPC
			}
			distance = ctx.PathFinder.DistanceFromMe(townNPC.Position)

			ctx.Logger.Debug("NPC packet interaction: 0x2F NPCInit",
				"npc", npcID, "unitID", townNPC.UnitID, "distance", distance, "attempt", attempt+1)
			if err := ctx.PacketSender.NPCInit(townNPC.UnitID); err != nil {
				return fmt.Errorf("packet NPC interaction failed: NPCInit send failed for npc %d: %w", npcID, err)
			}
			utils.Sleep(100)
			ctx.RefreshGameData()
			playerPos = ctx.Data.PlayerUnit.Position
			ctx.Logger.Debug("NPC packet interaction: AMB 0x03 dialog position sync",
				"npc", npcID, "unitID", townNPC.UnitID, "playerX", playerPos.X, "playerY", playerPos.Y, "attempt", attempt+1)
			if err := ctx.PacketSender.NPCDialogPositionSync(playerPos); err != nil {
				return fmt.Errorf("packet NPC interaction failed: NPCDialogPositionSync failed for npc %d: %w", npcID, err)
			}

			utils.Sleep(200)
			for i := 0; i < 20; i++ {
				ctx.RefreshGameData()
				if ctx.Data.OpenMenus.NPCInteract || ctx.Data.OpenMenus.NPCShop {
					ctx.Logger.Info("NPC dialog opened via packet",
						"npc", npcID, "stage", stage+" + 0x2F", "waitMs", (i+1)*100+200, "distance", distance, "attempt", attempt+1)
					return nil
				}
				utils.Sleep(100)
			}
			ctx.Logger.Warn("NPC packet interaction: dialog did not open after NPCInit",
				"npc", npcID, "unitID", townNPC.UnitID, "distance", distance, "attempt", attempt+1)
		}
		return fmt.Errorf("packet NPC interaction failed: dialog did not open for npc %d after AMB retry flow", npcID)
	}

	return fmt.Errorf("packet NPC interaction unavailable for npc %d: refusing HID fallback", npcID)
}
