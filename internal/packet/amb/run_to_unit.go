package amb

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// RunToUnit represents packet 0x04 (RunToUnit)
// Moves character to specified unit at running speed
//
// 🚨 CRITICAL: Movement packets REQUIRE 201 bytes of padding!
// OpCode Size: 13 bytes (1 opcode + 12 data)
// ACTUAL Size: 214 bytes (13 data + 201 padding)
//
// Without padding = packet rejected = no movement!
//
// Packet Structure:
//
//	[0]      PacketID (0x04)
//	[1:5]    UnitType (uint32) - 0=Player, 1=Monster, 2=Object, 3=Missile, 4=Item, 5=Tile
//	[5:9]    UnitId (uint32) - target unit's ID
//	[9:11]   OriginX (uint16) - player's current X position
//	[11:13]  OriginY (uint16) - player's current Y position
//	[13:214] Padding (201 bytes of zeros) - REQUIRED!
type RunToUnit struct {
	PacketID byte
	UnitType uint32
	UnitId   uint32
	OriginX  uint16
	OriginY  uint16
	// Padding is added in GetPayload, not stored in struct
}

// NewRunToUnit creates packet 0x04 for running to a unit
// unitType: 0=Player, 1=Monster, 2=Object, 3=Missile, 4=Item, 5=Tile
// originX/Y must be player's current position in game world coordinates
func NewRunToUnit(unitType int, unitId data.UnitID, originX, originY uint16) *RunToUnit {
	return &RunToUnit{
		PacketID: 0x04,
		UnitType: uint32(unitType),
		UnitId:   uint32(unitId),
		OriginX:  originX,
		OriginY:  originY,
	}
}

// GetPayload converts the RunToUnit struct to bytes (214 bytes with padding)
// CRITICAL: Buffer must be 214 bytes, not 13!
func (p *RunToUnit) GetPayload() []byte {
	buf := make([]byte, 214) // 13 data + 201 padding
	buf[0] = p.PacketID
	binary.LittleEndian.PutUint32(buf[1:5], p.UnitType)
	binary.LittleEndian.PutUint32(buf[5:9], p.UnitId)
	binary.LittleEndian.PutUint16(buf[9:11], p.OriginX)
	binary.LittleEndian.PutUint16(buf[11:13], p.OriginY)
	// buf[13:214] automatically zeroed by make() - this is the required padding
	return buf
}
