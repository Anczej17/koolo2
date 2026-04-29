package amb

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// RequestUnitUpdate represents the packet for requesting unit state updates
// Packet structure: OUT 0x43 = RequestUnitUpdate (size=9)
// Format: [PacketID:0x43][UnitType:uint32][UnitID:uint32]
//
// UnitType values:
// 0 = Player
// 1 = Monster
// 2 = Object
// 3 = Missile
// 4 = Item
// 5 = Tile
//
// Used to request the server to send updated information about a unit.
// Should be sent after interaction packets to ensure client receives latest state.
type RequestUnitUpdate struct {
	PacketID byte
	UnitType uint32
	UnitID   uint32
}

// NewRequestPlayerUpdate creates a RequestUnitUpdate packet for the player
func NewRequestPlayerUpdate(playerUnitID data.UnitID) *RequestUnitUpdate {
	return &RequestUnitUpdate{
		PacketID: 0x43,
		UnitType: 0, // Player type
		UnitID:   uint32(playerUnitID),
	}
}

// NewRequestObjectUpdate creates a RequestUnitUpdate packet for an object
func NewRequestObjectUpdate(objectID data.UnitID) *RequestUnitUpdate {
	return &RequestUnitUpdate{
		PacketID: 0x43,
		UnitType: 2, // Object type
		UnitID:   uint32(objectID),
	}
}

// NewRequestMonsterUpdate creates a RequestUnitUpdate packet for a monster/NPC
func NewRequestMonsterUpdate(monsterID data.UnitID) *RequestUnitUpdate {
	return &RequestUnitUpdate{
		PacketID: 0x43,
		UnitType: 1, // Monster type
		UnitID:   uint32(monsterID),
	}
}

// NewRequestUnitUpdate creates a generic RequestUnitUpdate packet for any unit type
// unitType: 0=Player, 1=Monster, 2=Object, 3=Missile, 4=Item, 5=Tile
func NewRequestUnitUpdate(unitType uint32, unitID data.UnitID) *RequestUnitUpdate {
	return &RequestUnitUpdate{
		PacketID: 0x43,
		UnitType: unitType,
		UnitID:   uint32(unitID),
	}
}

// GetPayload converts the RequestUnitUpdate struct to bytes
func (p *RequestUnitUpdate) GetPayload() []byte {
	buf := make([]byte, 9)
	buf[0] = byte(p.PacketID)
	binary.LittleEndian.PutUint32(buf[1:5], p.UnitType)
	binary.LittleEndian.PutUint32(buf[5:9], p.UnitID)
	return buf
}
