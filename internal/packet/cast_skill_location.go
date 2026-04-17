package packet

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

type CastSkillLocation struct {
	PacketID byte
	X        uint16
	Y        uint16
	PlayerX  uint16
	PlayerY  uint16
}

// NewCastSkillLocation creates a "cast right-click skill at location" packet.
// D2R format (9 bytes): [0C][targetX:u16][targetY:u16][playerX:u16][playerY:u16]
// Ground truth verified in logs/sec_rmb_skill.log.
func NewCastSkillLocation(target, playerPos data.Position) *CastSkillLocation {
	return &CastSkillLocation{
		PacketID: 0x0C,
		X:        uint16(target.X),
		Y:        uint16(target.Y),
		PlayerX:  uint16(playerPos.X),
		PlayerY:  uint16(playerPos.Y),
	}
}

// NewTeleport is an alias for NewCastSkillLocation — teleport is simply
// "cast right-click skill at location" when the active right skill is Teleport.
func NewTeleport(target, playerPos data.Position) *CastSkillLocation {
	return NewCastSkillLocation(target, playerPos)
}

func (p *CastSkillLocation) GetPayload() []byte {
	buf := make([]byte, 9)
	buf[0] = p.PacketID
	binary.LittleEndian.PutUint16(buf[1:], p.X)
	binary.LittleEndian.PutUint16(buf[3:], p.Y)
	binary.LittleEndian.PutUint16(buf[5:], p.PlayerX)
	binary.LittleEndian.PutUint16(buf[7:], p.PlayerY)
	return buf
}
