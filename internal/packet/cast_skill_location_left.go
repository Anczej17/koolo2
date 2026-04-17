package packet

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// NewCastLeftSkillLocation creates a left-skill-at-location packet (0x05).
// D2R format (9 bytes): [05][targetX:u16][targetY:u16][playerX:u16][playerY:u16]
// Ground truth verified in logs/sec_lmb_skill.log.
func NewCastLeftSkillLocation(target, playerPos data.Position) []byte {
	buf := make([]byte, 9)
	buf[0] = OpCastSkillLeftLoc
	binary.LittleEndian.PutUint16(buf[1:], uint16(target.X))
	binary.LittleEndian.PutUint16(buf[3:], uint16(target.Y))
	binary.LittleEndian.PutUint16(buf[5:], uint16(playerPos.X))
	binary.LittleEndian.PutUint16(buf[7:], uint16(playerPos.Y))
	return buf
}
