package amb

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// LeftSkillAtUnitStandStill represents packet 0x07 (LeftSkillAtUnitStandStill)
// Casts left skill on unit without moving (stand still)
type LeftSkillAtUnitStandStill struct {
	PacketID byte
	UnitType uint32
	UnitID   uint32
}

// NewLeftSkillAtUnitStandStill creates packet 0x07 for casting left skill on unit while standing still
// unitType: 0=Player, 1=Monster, 2=Object, 3=Missile, 4=Item, 5=Tile
func NewLeftSkillAtUnitStandStill(unitType int, unitID data.UnitID) *LeftSkillAtUnitStandStill {
	return &LeftSkillAtUnitStandStill{
		PacketID: 0x07,
		UnitType: uint32(unitType),
		UnitID:   uint32(unitID),
	}
}

// GetPayload converts the LeftSkillAtUnitStandStill struct to bytes (9 bytes)
func (p *LeftSkillAtUnitStandStill) GetPayload() []byte {
	buf := make([]byte, 9)
	buf[0] = p.PacketID
	binary.LittleEndian.PutUint32(buf[1:5], p.UnitType)
	binary.LittleEndian.PutUint32(buf[5:9], p.UnitID)
	return buf
}
