package amb

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// EntranceInteraction represents the simple UnitInteract packet (0x40) used for area transitions
// Packet structure: OUT 0x40 = UnitInteract (size=5)
// Format: [PacketID:0x40][UnitId:uint32]
// This is used specifically for entrance/level transitions (stairs, doors, cave entrances)
type EntranceInteraction struct {
	PacketID byte
	UnitID   uint32
}

// NewEntranceInteraction creates an entrance/transition interaction packet
// Area transitions use simple UnitInteract (0x40), NOT UnitInteractEx (0x41)
// Example: Blood Moor → Den of Evil, Tower Level 1 → Tower Level 2
func NewEntranceInteraction(entrance data.Entrance) *EntranceInteraction {
	return &EntranceInteraction{
		PacketID: 0x40,
		UnitID:   uint32(entrance.ID),
	}
}

// GetPayload converts the EntranceInteraction struct to bytes
func (p *EntranceInteraction) GetPayload() []byte {
	buf := make([]byte, 5)
	buf[0] = p.PacketID
	binary.LittleEndian.PutUint32(buf[1:5], p.UnitID)
	return buf
}
