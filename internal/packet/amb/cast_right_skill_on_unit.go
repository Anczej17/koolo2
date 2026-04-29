package amb

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// CastRightSkillOnUnit packet structure
// Used to cast a skill with the right hand on a specific unit (monster, player, etc.)
// UnitType: 0 = Player, 1 = Monster, 2 = Object, 3 = Missile, 4 = Item, 5 = Tile
type CastRightSkillOnUnit struct {
	PacketID byte
	UnitType uint32
	UnitID   uint32
}

// NewCastRightSkillOnUnit creates a CastRightSkillOnUnit packet
func NewCastRightSkillOnUnit(unitType int, unitID data.UnitID) *CastRightSkillOnUnit {
	return &CastRightSkillOnUnit{
		PacketID: 0x0D, // Cast right skill on unit packet ID
		UnitType: uint32(unitType),
		UnitID:   uint32(unitID),
	}
}

func (p *CastRightSkillOnUnit) GetPayload() []byte {
	buf := make([]byte, 9)
	buf[0] = p.PacketID
	binary.LittleEndian.PutUint32(buf[1:], p.UnitType)
	binary.LittleEndian.PutUint32(buf[5:], p.UnitID)
	return buf
}
