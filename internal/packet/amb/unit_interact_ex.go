package amb

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// UnitInteractEx represents the packet for ALL unit interactions in D2R
// Packet structure: OUT 0x41 = UnitInteractEx (size=13)
// Format: [PacketID:0x41][UnitId:uint32][UnitType:uint32][ObjectState:0xFFFFFFFF]
//
// Used for:
// - ALL object interactions (waypoints, chests, shrines, doors, portals, stairs, etc.)
// - ALL NPC interactions (vendors, quest NPCs, etc.)
// - ALL entrance/exit interactions (level transitions)
//
// UnitType values:
// 0 = Player
// 1 = Monster/NPC
// 2 = Object
// 3 = Missile
// 4 = Item
// 5 = Tile
//
// ObjectState: Always use 0xFFFFFFFF (server auto-detects correct state)
type UnitInteractEx struct {
	PacketID    byte
	UnitId      uint32
	UnitType    uint32
	ObjectState uint32
}

// NewPortalInteraction creates a UnitInteractEx packet for portal interactions
func NewPortalInteraction(object data.Object) *UnitInteractEx {
	return &UnitInteractEx{
		PacketID:    0x41,
		UnitId:      uint32(object.ID),
		UnitType:    2,          // Object type
		ObjectState: 0xFFFFFFFF, // Auto-detect state
	}
}

// NewEntranceInteractionEx creates a UnitInteractEx packet for entrance/transition interactions
func NewEntranceInteractionEx(entrance data.Entrance) *UnitInteractEx {
	return &UnitInteractEx{
		PacketID:    0x41,
		UnitId:      uint32(entrance.ID),
		UnitType:    0,          // Use 0 like chests/shrines (observed pattern)
		ObjectState: 0xFFFFFFFF, // Auto-detect state
	}
}

// NewObjectInteraction creates a UnitInteractEx packet for object interactions
// Used for: waypoints, chests, shrines, doors, stairs, portals, bank/stash, etc.
// NOTE: Different object types require different UnitType values:
//   - Waypoints: UnitType 2 (proper Object type)
//   - Portals (town and red): UnitType 2 (proper Object type)
//   - Chests, Shrines, Bank, Doors, Entrances: UnitType 0 (interactive objects)
func NewObjectInteraction(object data.Object) *UnitInteractEx {
	// Determine correct UnitType based on object category
	// Based on packet captures and D2R behavior:
	unitType := uint32(0) // Default to 0 for most interactive objects

	// Waypoints and ALL Portals (town portals AND red portals) use UnitType 2 (proper Object type)
	// All other interactive objects (chests, shrines, doors, bank, etc.) use UnitType 0
	// This has been verified via packet captures
	if object.IsWaypoint() || object.IsPortal() || object.IsRedPortal() {
		unitType = 2
	}

	return &UnitInteractEx{
		PacketID:    0x41,
		UnitId:      uint32(object.ID), // Converts signed int to unsigned uint32
		UnitType:    unitType,
		ObjectState: 0xFFFFFFFF, // Auto-detect state
	}
}

// NewNPCInteraction creates a UnitInteractEx packet for NPC interactions
// Used for: vendors, quest NPCs, etc.
func NewNPCInteraction(monster data.Monster) *UnitInteractEx {
	return &UnitInteractEx{
		PacketID:    0x41,
		UnitId:      uint32(monster.UnitID),
		UnitType:    1,          // Monster/NPC type
		ObjectState: 0xFFFFFFFF, // Auto-detect state
	}
}

// GetPayload converts the UnitInteractEx struct to bytes
func (p *UnitInteractEx) GetPayload() []byte {
	buf := make([]byte, 13)
	buf[0] = byte(p.PacketID)
	binary.LittleEndian.PutUint32(buf[1:5], p.UnitId)
	binary.LittleEndian.PutUint32(buf[5:9], p.UnitType)
	binary.LittleEndian.PutUint32(buf[9:13], p.ObjectState)
	return buf
}
