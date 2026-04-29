package amb

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// NPCInit represents the packet for initializing NPC dialog
// Packet structure: OUT 0x2F = NPCInit (size=5)
// Format: [PacketID:0x2F][UnitID:uint32]
//
// This packet opens the NPC dialog interface. Send this before NPCAction.
// Example flow: NPCInit → NPCAction(1) for Trade, NPCAction(2) for Gamble
type NPCInit struct {
	PacketID byte
	UnitID   uint32
}

// NewNPCInit creates an NPCInit packet to open NPC dialog
func NewNPCInit(npcID data.UnitID) *NPCInit {
	return &NPCInit{
		PacketID: 0x2F,
		UnitID:   uint32(npcID),
	}
}

// GetPayload converts the NPCInit struct to bytes
func (p *NPCInit) GetPayload() []byte {
	buf := make([]byte, 5)
	buf[0] = p.PacketID
	binary.LittleEndian.PutUint32(buf[1:], p.UnitID)
	return buf
}
