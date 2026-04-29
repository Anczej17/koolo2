package packet

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

type CastSkillLocation struct {
	PacketID byte
	X        uint16
	Y        uint16
}

// NewCastSkillLocation builds packet 0x0C (cast currently-equipped right
// skill at a map location). 5 bytes fixed, per AMB reference + our own
// OpCastSkillRightLoc comment + OpcodeLengths[0x0C]={5, 5}.
//
//	[0]      0x0C
//	[1..3]   target X (u16 LE) — world coordinates
//	[3..5]   target Y (u16 LE) — world coordinates
//
// Previous 9-byte variant also carried playerX/playerY tails; the server
// ignores them, but matching AMB's minimal form keeps us on the same bytes
// the reference implementation ships and avoids any signature divergence.
// The playerPos argument is accepted for caller compatibility but unused.
func NewCastSkillLocation(target, _ data.Position) *CastSkillLocation {
	return &CastSkillLocation{
		PacketID: 0x0C,
		X:        uint16(target.X),
		Y:        uint16(target.Y),
	}
}

// NewTeleport is an alias for NewCastSkillLocation — teleport is simply
// "cast right-click skill at location" when the active right skill is Teleport.
func NewTeleport(target, playerPos data.Position) *CastSkillLocation {
	return NewCastSkillLocation(target, playerPos)
}

func (p *CastSkillLocation) GetPayload() []byte {
	buf := make([]byte, 5)
	buf[0] = p.PacketID
	binary.LittleEndian.PutUint16(buf[1:3], p.X)
	binary.LittleEndian.PutUint16(buf[3:5], p.Y)
	return buf
}
