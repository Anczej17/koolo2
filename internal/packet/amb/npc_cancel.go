package amb

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// NPCCancel represents the packet for closing NPC dialog
// Packet structure: OUT 0x30 = NPCCancel (size=5)
// Format: [PacketID:0x30][UnitID:uint32]
//
// This packet closes the NPC dialog interface.
// Send this to exit NPC interaction cleanly.
type NPCCancel struct {
	PacketID byte
	UnitID   uint32
}

// NewNPCCancel creates an NPCCancel packet to close NPC dialog
func NewNPCCancel(npcID data.UnitID) *NPCCancel {
	return &NPCCancel{
		PacketID: 0x30,
		UnitID:   uint32(npcID),
	}
}

// GetPayload converts the NPCCancel struct to bytes
func (p *NPCCancel) GetPayload() []byte {
	buf := make([]byte, 5)
	buf[0] = p.PacketID
	binary.LittleEndian.PutUint32(buf[1:], p.UnitID)
	return buf
}
