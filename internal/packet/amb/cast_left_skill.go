package amb

import (
	"encoding/binary"
)

// CastLeftSkill packet structure
// Used to cast a skill on the left hand at a specific position
type CastLeftSkill struct {
	PacketID byte
	X        uint16
	Y        uint16
}

// NewCastLeftSkill creates a CastLeftSkill packet
func NewCastLeftSkill(x, y uint16) *CastLeftSkill {
	return &CastLeftSkill{
		PacketID: 0x05, // CastLeftSkillPacketId
		X:        x,
		Y:        y,
	}
}

func (p *CastLeftSkill) GetPayload() []byte {
	buf := make([]byte, 5)
	buf[0] = p.PacketID
	binary.LittleEndian.PutUint16(buf[1:], p.X)
	binary.LittleEndian.PutUint16(buf[3:], p.Y)
	return buf
}
