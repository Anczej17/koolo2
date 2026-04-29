package amb

import (
	"encoding/binary"
)

// CastRightSkill packet structure
// Used to cast a skill on the right hand at a specific position
type CastRightSkill struct {
	PacketID byte
	X        uint16
	Y        uint16
}

// NewCastRightSkill creates a CastRightSkill packet
func NewCastRightSkill(x, y uint16) *CastRightSkill {
	return &CastRightSkill{
		PacketID: 0x0C, // CastRightSkillPacketId
		X:        x,
		Y:        y,
	}
}

func (p *CastRightSkill) GetPayload() []byte {
	buf := make([]byte, 5)
	buf[0] = p.PacketID
	binary.LittleEndian.PutUint16(buf[1:], p.X)
	binary.LittleEndian.PutUint16(buf[3:], p.Y)
	return buf
}
