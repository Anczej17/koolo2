package amb

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// RightSkillAtUnitStandStill represents packet 0x0E (RightSkillAtUnitStandStill)
// Casts right skill on unit without moving (stand still)
type RightSkillAtUnitStandStill struct {
	PacketID byte
	UnitType uint32
	UnitID   uint32
}

// NewRightSkillAtUnitStandStill creates packet 0x0E for casting right skill on unit while standing still
// unitType: 0=Player, 1=Monster, 2=Object, 3=Missile, 4=Item, 5=Tile
func NewRightSkillAtUnitStandStill(unitType int, unitID data.UnitID) *RightSkillAtUnitStandStill {
	return &RightSkillAtUnitStandStill{
		PacketID: 0x0E,
		UnitType: uint32(unitType),
		UnitID:   uint32(unitID),
	}
}

// GetPayload converts the RightSkillAtUnitStandStill struct to bytes (9 bytes)
func (p *RightSkillAtUnitStandStill) GetPayload() []byte {
	buf := make([]byte, 9)
	buf[0] = p.PacketID
	binary.LittleEndian.PutUint32(buf[1:5], p.UnitType)
	binary.LittleEndian.PutUint32(buf[5:9], p.UnitID)
	return buf
}
