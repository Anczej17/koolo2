package amb

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// UnitInteraction0x13 represents the packet for interacting with units (objects, NPCs, etc)
// Packet structure: OUT 0x13 = InteractWithUnit (size=9)
// Format: [PacketID:0x13][TargetUnitId:uint32][ExecuteeUnitId:uint32]
type UnitInteraction0x13 struct {
	PacketID       byte
	TargetUnitId   uint32
	ExecuteeUnitId uint32
}

// NewObjectInteraction0x13 creates a unit interaction packet for objects
func NewObjectInteraction0x13(obj data.Object, playerUnitID data.UnitID) *UnitInteraction0x13 {
	return &UnitInteraction0x13{
		PacketID:       0x13,
		TargetUnitId:   uint32(obj.ID),
		ExecuteeUnitId: uint32(playerUnitID),
	}
}

// NewNPCInteraction0x13 creates a unit interaction packet for NPCs
func NewNPCInteraction0x13(monster data.Monster, playerUnitID data.UnitID) *UnitInteraction0x13 {
	return &UnitInteraction0x13{
		PacketID:       0x13,
		TargetUnitId:   uint32(monster.UnitID),
		ExecuteeUnitId: uint32(playerUnitID),
	}
}

// GetPayload converts the UnitInteraction0x13 struct to bytes
func (p *UnitInteraction0x13) GetPayload() []byte {
	buf := make([]byte, 9)
	buf[0] = byte(p.PacketID)
	binary.LittleEndian.PutUint32(buf[1:5], p.TargetUnitId)
	binary.LittleEndian.PutUint32(buf[5:9], p.ExecuteeUnitId)
	return buf
}
