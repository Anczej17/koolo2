package amb

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// NPCActionType represents the NPC menu position (not service type!)
// CRITICAL: ActionID represents the MENU POSITION, not the service type.
// Different NPCs have different menu layouts, so the same ActionID means different things!
//
// ActionID values from PACKET_MOVEMENT_AND_ITEMS_GUIDE.md:
//
//	0x01 (1) = First menu option
//	0x02 (2) = Second menu option
//	0x03 (3) = Third menu option
//	etc.
//
// NPC ID Reference (from libs/d2go/pkg/data/npc/npc.go):
//
//	146 = DeckardCain (All Acts)
//	147 = Gheed (Act 1)
//	154 = Charsi (Act 1)
//	178 = Fara (Act 2)
//	253 = Hratli (Act 3)
//	257 = Halbu (Act 4)
//	405 = Jamella (Act 4)
//	511 = Larzuk (Act 5)
//	512 = Anya (Act 5)
type NPCActionType uint32

// NPCAction represents the packet for NPC menu actions
// Packet structure: OUT 0x38 = NPCAction (size=9)
// Format: [PacketID:0x38][ActionId:uint32][NPCUnitId:uint32]
//
// NPC Menu Layouts (from PACKET_MOVEMENT_AND_ITEMS_GUIDE.md):
//
// Act 1 - Rogue Encampment:
//
//	Gheed:       0x01=Trade, 0x02=Gamble
//	Charsi:      0x01=Trade, 0x02=Repair, 0x03=Imbue
//	Akara:       0x01=Trade
//	Cain:        0x01=Identify, 0x02=Quest info
//
// Act 2 - Lut Gholein:
//
//	Fara:        0x01=Trade, 0x02=Repair
//
// Act 3 - Kurast Docks:
//
//	Hratli:      0x01=Trade, 0x02=Repair
//
// Act 4 - Pandemonium Fortress:
//
//	Jamella:     0x01=Trade, 0x02=Gamble
//	Halbu:       0x01=Trade, 0x02=Repair
//
// Act 5 - Harrogath:
//
//	Larzuk:      0x01=Trade, 0x02=Repair, 0x03=Socket
//	Anya:        0x01=Trade, 0x02=Gamble, 0x03=Personalize
//
// Usage flow:
//
//	NPCInit(npcId) → NPCAction(actionId, npcId)
type NPCAction struct {
	PacketID byte
	ActionID uint32
	UnitID   uint32
}

// NewNPCAction creates an NPCAction packet
func NewNPCAction(actionID NPCActionType, npcID data.UnitID) *NPCAction {
	return &NPCAction{
		PacketID: 0x38,
		ActionID: uint32(actionID),
		UnitID:   uint32(npcID),
	}
}

// NewNPCTrade creates an NPCAction packet for opening trade window
// Uses ActionID=0x01 (first menu option) - works for ALL trade NPCs
func NewNPCTrade(npcID data.UnitID) *NPCAction {
	return NewNPCAction(0x01, npcID)
}

// NewNPCGamble creates an NPCAction packet for opening gambling window
// Uses ActionID=0x02 (second menu option) - works for ALL gambling NPCs
func NewNPCGamble(npcID data.UnitID) *NPCAction {
	return NewNPCAction(0x02, npcID)
}

// GetNPCTradeAction returns the correct ActionID for Trade
// From guide: ALL NPCs have Trade at ActionID 0x01 (first menu option)
func GetNPCTradeAction(npcName uint32) NPCActionType {
	return 0x01
}

// GetNPCRepairAction returns the correct ActionID for Repair
// From guide: ALL repair NPCs have Repair at ActionID 0x02 (second menu option)
// NPCs: Charsi (154), Fara (178), Hratli (253), Halbu (257), Larzuk (511)
func GetNPCRepairAction(npcName uint32) NPCActionType {
	return 0x02
}

// GetNPCGambleAction returns the correct ActionID for Gamble
// From guide: ALL gambling NPCs have Gamble at ActionID 0x02 (second menu option)
// NPCs: Gheed (147), Jamella (405), Anya (512)
func GetNPCGambleAction(npcName uint32) NPCActionType {
	return 0x02
}

// GetNPCIdentifyAction returns the correct ActionID for Identify
// From guide: Cain has Identify at ActionID 0x01 (first menu option)
func GetNPCIdentifyAction(npcName uint32) NPCActionType {
	return 0x01
}

// GetPayload converts the NPCAction struct to bytes
func (p *NPCAction) GetPayload() []byte {
	buf := make([]byte, 9)
	buf[0] = p.PacketID
	binary.LittleEndian.PutUint32(buf[1:], p.ActionID)
	binary.LittleEndian.PutUint32(buf[5:], p.UnitID)
	return buf
}
