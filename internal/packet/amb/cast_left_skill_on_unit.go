package amb

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// CastLeftSkillOnUnit packet structure
// Used to cast a skill with the left hand on a specific unit (monster, player, etc.)
// UnitType: 0 = Player, 1 = Monster, 2 = Object, 3 = Missile, 4 = Item, 5 = Tile
type CastLeftSkillOnUnit struct {
	PacketID byte
	UnitType uint32
	UnitID   uint32
}

// NewCastLeftSkillOnUnit creates a CastLeftSkillOnUnit packet
func NewCastLeftSkillOnUnit(unitType int, unitID data.UnitID) *CastLeftSkillOnUnit {
	return &CastLeftSkillOnUnit{
		PacketID: 0x06, // Cast left skill on unit packet ID
		UnitType: uint32(unitType),
		UnitID:   uint32(unitID),
	}
}

func (p *CastLeftSkillOnUnit) GetPayload() []byte {
	buf := make([]byte, 9)
	buf[0] = p.PacketID
	binary.LittleEndian.PutUint32(buf[1:], p.UnitType)
	binary.LittleEndian.PutUint32(buf[5:], p.UnitID)
	return buf
}
