package amb

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data/stat"
)

// IncrementAttribute represents the packet for allocating stat points (Strength, Dexterity, Vitality, Energy)
// Packet structure (5 bytes total):
// [PacketID:0x3A][StatID:uint16][Padding:uint16]
//
// StatID mapping (d2go stat.ID matches packet stat ID):
// 0x00 = Strength
// 0x01 = Energy
// 0x02 = Dexterity
// 0x03 = Vitality
//
// IMPORTANT: This packet always allocates exactly 1 point. The last 2 bytes MUST be 0x0000 (padding).
// There is no "amount" field - each packet allocates 1 point only.
//
// Note: The game must have stat points available (StatPoints > 0) or the packet will be ignored
type IncrementAttribute struct {
	PacketID byte
	StatID   uint16 // stat.ID (Strength=0, Energy=1, Dexterity=2, Vitality=3)
	Padding  uint16 // Must be 0x0000
}

// NewIncrementAttribute creates a packet to allocate 1 stat point
// statType: The stat to increase (stat.Strength, stat.Dexterity, stat.Vitality, stat.Energy)
func NewIncrementAttribute(statType stat.ID) *IncrementAttribute {
	return &IncrementAttribute{
		PacketID: 0x3A,
		StatID:   uint16(statType),
		Padding:  0x0000,
	}
}

var getPayloadCallCountAttr int

// GetPayload converts the IncrementAttribute struct to bytes
func (p *IncrementAttribute) GetPayload() []byte {
	getPayloadCallCountAttr++

	buf := make([]byte, 5)
	buf[0] = p.PacketID
	binary.LittleEndian.PutUint16(buf[1:], p.StatID)
	binary.LittleEndian.PutUint16(buf[3:], p.Padding)

	return buf
}
