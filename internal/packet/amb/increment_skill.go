package amb

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data/skill"
)

// IncrementSkill represents the packet for learning/allocating skill points
// Packet structure (5 bytes total):
// [PacketID:0x3B][SkillID:uint16][Padding:uint16]
//
// SkillID is the skill.ID (e.g., skill.FireBolt=36, skill.Blizzard=59, skill.Teleport=54)
//
// IMPORTANT: This packet always allocates exactly 1 point. The last 2 bytes MUST be 0x0000 (padding).
// There is no "amount" field - each packet allocates 1 point only.
//
// Note: The game must have skill points available (SkillPoints > 0) and the skill must be
// available to the character class, or the packet will be ignored
type IncrementSkill struct {
	PacketID byte
	SkillID  uint16 // skill.ID for the skill to increase
	Padding  uint16 // Must be 0x0000
}

// NewIncrementSkill creates a packet to allocate 1 skill point
// skillID: The skill to increase (e.g., skill.FireBolt, skill.Blizzard, skill.Teleport)
func NewIncrementSkill(skillID skill.ID) *IncrementSkill {
	return &IncrementSkill{
		PacketID: 0x3B,
		SkillID:  uint16(skillID),
		Padding:  0x0000,
	}
}

var getPayloadCallCount int

// GetPayload converts the IncrementSkill struct to bytes
func (p *IncrementSkill) GetPayload() []byte {
	getPayloadCallCount++

	buf := make([]byte, 5)
	buf[0] = p.PacketID
	binary.LittleEndian.PutUint16(buf[1:], p.SkillID)
	binary.LittleEndian.PutUint16(buf[3:], p.Padding)

	return buf
}
