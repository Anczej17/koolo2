package game

import (
	"fmt"

	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/area"
	"local/internal/svc/internal/gamelib/data/skill"
	"local/internal/svc/internal/gamelib/data/stat"
	packet "local/internal/svc/internal/packet"
)

type ProcessSender interface {
	SendPacket([]byte) error
	SendUIPacket([]byte) error
	SendDualPacket([]byte) error
	ClickAt(x, y int32, btn byte) error
	ForceClick(x, y int32) error
}

// Mouse button bits as consumed by D2R real_click_worker (see Phase 9 spec).
const (
	MouseLeft   byte = 1
	MouseMiddle byte = 2
	MouseRight  byte = 4
)

type PacketSender struct {
	process ProcessSender
}

func NewPacketSender(process ProcessSender) *PacketSender {
	return &PacketSender{
		process: process,
	}
}

func (ps *PacketSender) SendPacket(packet []byte) error {
	return ps.process.SendPacket(packet)
}

// SendUIPacket sends a packet via the D2R UI NetMan path (vtable[5]).
// Required for identify/buy/sell/cube/gamble opcodes that crash when sent
// through the regular Game NetMan path.
func (ps *PacketSender) SendUIPacket(packet []byte) error {
	return ps.process.SendUIPacket(packet)
}

// SendDualPacket sends via the mirror buffer + send_fn dual path.
// This is how D2R's internal vendor wrapper dispatches packets.
func (ps *PacketSender) SendDualPacket(packet []byte) error {
	return ps.process.SendDualPacket(packet)
}

// ForceClick fires an in-process click with ForceMove key held.
// This is the primary non-HID movement method. D2R interprets it as
// "move to this screen position" regardless of UI elements.
func (ps *PacketSender) ForceClick(x, y int32) error {
	return ps.process.ForceClick(x, y)
}

// ClickAt fires a synthetic click via the in-process Phase 9 path
// (real_click_worker → vtable[1]). Bypasses OS input queue entirely; the
// user's hardware cursor is not touched. A WalkTo on the action layer is
// just `ClickAt(x, y, MouseLeft)`.
func (ps *PacketSender) ClickAt(x, y int32, btn byte) error {
	return ps.process.ClickAt(x, y, btn)
}

func (ps *PacketSender) PickUpItem(item data.Item) error {
	err := ps.SendPacket(packet.NewPickUpItem(item).GetPayload())
	if err != nil {
		return fmt.Errorf("failed to send pick item packet: %w", err)
	}

	return nil
}

func (ps *PacketSender) InteractWithTp(object data.Object) error {
	if err := ps.SendPacket(packet.NewTpInteraction(object).GetPayload()); err != nil {
		return fmt.Errorf("failed to send tp interaction packet: %w", err)
	}
	return nil
}

func (ps *PacketSender) InteractWithEntrance(entrance data.Entrance) error {
	if err := ps.SendPacket(packet.NewEntranceInteraction(entrance).GetPayload()); err != nil {
		return fmt.Errorf("failed to send entrance interaction packet: %w", err)
	}
	return nil
}

func (ps *PacketSender) Teleport(target, playerPos data.Position) error {
	payload := packet.NewTeleport(target, playerPos).GetPayload()

	if err := ps.SendPacket(payload); err != nil {
		return fmt.Errorf("failed to send teleport packet: %w", err)
	}
	return nil
}


// TelekinesisInteraction sends packet 0x0D for object interaction using telekinesis
// Use cases: waypoints, chests, shrines from distance (Sorceress only)
// Requires character to have Telekinesis skill and be within interaction range
func (ps *PacketSender) TelekinesisInteraction(objectGID data.UnitID) error {
	if err := ps.SendPacket(packet.NewTelekinesisInteraction(objectGID).GetPayload()); err != nil {
		return fmt.Errorf("failed to send telekinesis interaction packet: %w", err)
	}
	return nil
}

// CastSkillAtLocation sends packet 0x0C to cast a skill at a specific location
// Use cases: Blizzard, Meteor, Frozen Orb, or any location-targeted skill
// Useful for faster/more precise casting than HID mouse clicks
func (ps *PacketSender) CastSkillAtLocation(target, playerPos data.Position) error {
	payload := packet.NewCastSkillLocation(target, playerPos).GetPayload()

	if err := ps.SendPacket(payload); err != nil {
		return fmt.Errorf("failed to send cast skill at location packet: %w", err)
	}
	return nil
}

// SelectRightSkill sends packet 0x3C to change the active right-click skill
// Use cases: Switch skills via packet instead of clicking UI (F1-F9 functionality)
// Useful for quick skill switching during combat or automation
func (ps *PacketSender) SelectRightSkill(skillID skill.ID) error {
	if err := ps.SendPacket(packet.NewSkillSelection(skillID).GetPayload()); err != nil {
		return fmt.Errorf("failed to send skill selection packet: %w", err)
	}
	return nil
}

// SelectLeftSkill sends packet 0x3C to change the active left-click skill
// Use cases: Switch left-click skills via packet instead of clicking UI
// Useful for automation or quick skill switching
func (ps *PacketSender) SelectLeftSkill(skillID skill.ID) error {
	if err := ps.SendPacket(packet.NewLeftSkillSelection(skillID).GetPayload()); err != nil {
		return fmt.Errorf("failed to send left skill selection packet: %w", err)
	}
	return nil
}

// LearnSkill sends packet 0x3B to allocate a skill point
// Use cases: Faster skill point allocation during leveling (Sorceress leveling)
// Bypasses UI interaction for instant skill learning
func (ps *PacketSender) LearnSkill(skillID skill.ID) error {
	if err := ps.SendPacket(packet.NewLearnSkill(skillID).GetPayload()); err != nil {
		return fmt.Errorf("failed to send learn skill packet: %w", err)
	}
	return nil
}

// AllocateStatPoint sends packet 0x3A to allocate a stat point
// Use cases: Faster stat point allocation during leveling (Sorceress leveling)
// Bypasses UI interaction for instant stat allocation
func (ps *PacketSender) AllocateStatPoint(statID stat.ID) error {
	if err := ps.SendPacket(packet.NewAllocateStat(statID).GetPayload()); err != nil {
		return fmt.Errorf("failed to send allocate stat packet: %w", err)
	}
	return nil
}

// SwapWeapon sends a weapon swap packet (0x50, 30 bytes) via the dual-send
// path. Ground truth (sec_swap.log): 0x50 appears in BOTH buf0 and buf1,
// confirming D2R sends it through dual_send_wrap. Previous attempts via
// SendPacket (send_fn only) resulted in no server-side effect despite no crash
// with 30B format. The dual path writes mirror buffer + dispatches to Game
// NetMan, matching D2R's internal flow.
func (ps *PacketSender) SwapWeapon(fromLeftGID, fromRightGID, toLeftGID, toRightGID data.UnitID) error {
	if fromLeftGID == 0 && fromRightGID == 0 && toLeftGID == 0 && toRightGID == 0 {
		return fmt.Errorf("weapon swap: all GIDs are zero, cannot build packet")
	}
	if err := ps.SendDualPacket(packet.NewWeaponSwap(fromLeftGID, fromRightGID, toLeftGID, toRightGID)); err != nil {
		return fmt.Errorf("failed to send weapon swap packet: %w", err)
	}
	return nil
}

// InteractNPC attempts to open an NPC dialog via packet.
//
// DO NOT USE on its own — 0x2F sent via send_fn APC from the main thread
// CRASHES D2R (0xC0000005 AV). The server-side 0x2F handler touches NPC
// state only valid on the game thread. Callers should treat a non-nil return
// as a signal to fall back to HID click, and even a nil return does not
// guarantee the dialog opened. Keep CharacterCfg.PacketCasting.UseForNPCInteraction
// disabled until game-thread execution is solved.
func (ps *PacketSender) InteractNPC(npcGID, playerGID data.UnitID, npcX, npcY uint16) error {
	if err := ps.SendUIPacket(packet.NewNPCEntityAction(npcGID, playerGID, npcX, npcY)); err != nil {
		return fmt.Errorf("failed to send NPC entity action 0x4D: %w", err)
	}
	if err := ps.SendPacket(packet.NewNPCChatInit(npcGID, npcX, npcY)); err != nil {
		return fmt.Errorf("failed to send NPC chat init 0x2F: %w", err)
	}
	return nil
}

func (ps *PacketSender) InteractObject(objectGID data.UnitID) error {
	if err := ps.SendPacket(packet.NewObjectInteract(objectGID)); err != nil {
		return fmt.Errorf("failed to send object interact packet: %w", err)
	}
	return nil
}

func (ps *PacketSender) CastLeftSkillAtLocation(target, playerPos data.Position) error {
	if err := ps.SendPacket(packet.NewCastLeftSkillLocation(target, playerPos)); err != nil {
		return fmt.Errorf("failed to send left cast packet: %w", err)
	}
	return nil
}

func (ps *PacketSender) TravelWaypoint(waypointObjectID uint32, destination area.ID) error {
	if err := ps.SendPacket(packet.NewWaypointTravel(waypointObjectID, byte(destination))); err != nil {
		return fmt.Errorf("failed to send waypoint travel packet: %w", err)
	}
	return nil
}

// RepairAll sends the "repair all items" packet (0x35) via the UI NetMan
// path. D2R format: 18 bytes with playerGID + constant fields.
// Caller must already have the repair NPC's repair menu open.
func (ps *PacketSender) RepairAll(playerGID data.UnitID) error {
	if err := ps.SendUIPacket(packet.NewRepairAll(playerGID)); err != nil {
		return fmt.Errorf("failed to send repair packet: %w", err)
	}
	return nil
}

// IdentifyItem sends an "identify with tome" packet (0x26) via the UI NetMan
// path. D2R format: 34 bytes. Takes tomeGID only — the game identifies the
// currently selected/hovered item. Caller must have inventory open with tome.
func (ps *PacketSender) IdentifyItem(tomeGID data.UnitID) error {
	if err := ps.SendUIPacket(packet.NewIdentifyTomeOnItem(tomeGID)); err != nil {
		return fmt.Errorf("failed to send identify packet: %w", err)
	}
	return nil
}


// TerminateNPCChat closes the NPC dialog (0x30, 5 bytes, dual buffer).
// Authoritative format per bufpoll 2026-04-14.
func (ps *PacketSender) TerminateNPCChat(npcGID data.UnitID) error {
	if err := ps.SendDualPacket(packet.NewNPCChatTerminate(npcGID)); err != nil {
		return fmt.Errorf("failed to send npc chat terminate packet: %w", err)
	}
	return nil
}

// NPCDialogOption sends a dialog menu selection (0x38, 9 bytes, dual buffer).
// option=1 for Trade, 2 for Gamble on most NPCs. Authoritative format per
// bufpoll 2026-04-14.
func (ps *PacketSender) NPCDialogOption(option uint32, npcGID data.UnitID) error {
	if err := ps.SendDualPacket(packet.NewNPCDialogOption(option, npcGID)); err != nil {
		return fmt.Errorf("failed to send NPC dialog option 0x38: %w", err)
	}
	return nil
}

// ItemToStash moves an item from inventory to stash at (destCol, destRow).
// Uses 0x19 (OpItemMoveFrom = inv → buffer) per live capture sec_stash.log
// 2026-04-07. Server looks up the item's current location by GID; only the
// destination position is in the packet. Stash menu must be open.
func (ps *PacketSender) ItemToStash(itemGID data.UnitID, destCol, destRow uint8) error {
	if err := ps.SendDualPacket(packet.NewItemToStash(itemGID, destCol, destRow)); err != nil {
		return fmt.Errorf("failed to send item-to-stash packet: %w", err)
	}
	return nil
}

// ItemFromStash moves an item from stash back into inventory at (destCol,
// destRow). Uses 0x18 (OpItemMoveTo = buffer → inv).
func (ps *PacketSender) ItemFromStash(itemGID data.UnitID, destCol, destRow uint8) error {
	if err := ps.SendDualPacket(packet.NewItemFromStash(itemGID, destCol, destRow)); err != nil {
		return fmt.Errorf("failed to send item-from-stash packet: %w", err)
	}
	return nil
}

// NPCSell sends a 0x33 sell packet (22 bytes, dual buffer) matching the
// bufpoll 2026-04-14 layout. Caller must already have the merchant's trade
// menu open (via HID click — 0x2F chat-init crashes from main thread).
//
// Known limitation: only the FIRST sell per dialog session is reliable.
// A second 0x33 in the same open dialog deadlocks send_fn. Close the dialog
// (0x30) and re-open via HID between sells.
//
// slot and seq are the item's inventory grid column/row. term selects the
// vendor class: packet.NPCSellTermEquipment (0x03) for normal/magic/rare/
// unique equipment, packet.NPCSellTermConsumable (0xFF) for potions, scrolls,
// tomes and keys.
func (ps *PacketSender) NPCSell(sellPrice uint32, itemGID, npcGID data.UnitID, slot, seq uint16, term byte) error {
	if err := ps.SendDualPacket(packet.NewNPCSellItem(sellPrice, itemGID, npcGID, slot, seq, term)); err != nil {
		return fmt.Errorf("failed to send sell via dual path: %w", err)
	}
	return nil
}

// NPCBuy sends a 0x32 buy packet (22 bytes, dual buffer) matching the bufpoll
// 2026-04-14 layout. Caller must have the merchant's trade menu open.
//
// price is the gold cost the server verifies against the shop state. slot/seq
// are the vendor grid column/row of the item being bought. term follows the
// same class convention as NPCSell.
func (ps *PacketSender) NPCBuy(price uint32, itemGID, npcGID data.UnitID, slot, seq uint16, term byte) error {
	if err := ps.SendDualPacket(packet.NewNPCBuy(price, itemGID, npcGID, slot, seq, term)); err != nil {
		return fmt.Errorf("failed to send npc buy packet: %w", err)
	}
	return nil
}

// GambleBuy sends a "gamble item" packet (0x32 sub-form, type=0x06) via the
// UI NetMan path. Caller must already have the gamble menu open at an
// Anya/Gheed-style NPC.
func (ps *PacketSender) GambleBuy(playerGID, gambleItemGID data.UnitID, gambleSlot byte) error {
	if err := ps.SendUIPacket(packet.NewGambleBuy(playerGID, gambleItemGID, gambleSlot)); err != nil {
		return fmt.Errorf("failed to send gamble buy packet: %w", err)
	}
	return nil
}

// CubeTransmute sends a Horadric Cube transmute packet (0x20) via the UI
// NetMan path. Caller must already have the cube open with all ingredients
// placed inside the cube grid (drag-drop done via HID or item move packets
// first).
func (ps *PacketSender) CubeTransmute(cubeGID data.UnitID) error {
	if err := ps.SendUIPacket(packet.NewCubeTransmute(cubeGID)); err != nil {
		return fmt.Errorf("failed to send cube transmute packet: %w", err)
	}
	return nil
}
