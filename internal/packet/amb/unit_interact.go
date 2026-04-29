package amb

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// UnitInteract represents packet 0x40 (Simple Unit Interaction)
// Size: 5 bytes
// Purpose: General interactions - NPCs, waypoints, portals, shrines, chests, entrances
//
// This is the simpler interaction packet compared to 0x41 (UnitInteractEx)
//
// Use 0x40 for:
// - Waypoints (opening menu)
// - Town portals and red portals
// - Shrines
// - Chests
// - NPCs
// - Entrances/level transitions
// - Most object interactions
//
// Use 0x41 ONLY for:
// - Doors (stateful objects that need ObjectState)
type UnitInteract struct {
	PacketID byte
	UnitID   uint32
}

// NewUnitInteract creates a simple 0x40 interaction packet
func NewUnitInteract(unitID data.UnitID) *UnitInteract {
	return &UnitInteract{
		PacketID: 0x40,
		UnitID:   uint32(unitID),
	}
}

// NewSimpleObjectInteraction creates a 0x40 packet for simple object interactions
// Use this for: waypoints, portals, shrines, chests, entrances, NPCs
// For doors, use NewDoorInteraction (0x41) instead
func NewSimpleObjectInteraction(obj data.Object) *UnitInteract {
	return &UnitInteract{
		PacketID: 0x40,
		UnitID:   uint32(obj.ID),
	}
}

// GetPayload converts the UnitInteract struct to bytes (5 bytes)
func (p *UnitInteract) GetPayload() []byte {
	buf := make([]byte, 5)
	buf[0] = p.PacketID
	binary.LittleEndian.PutUint32(buf[1:5], p.UnitID)

	return buf
}

// ShouldUseSimpleInteraction determines if an object should use 0x40 (simple) or 0x41 (extended)
// Returns true for 0x40, false for 0x41
//
// Based on verified D2R packet analysis:
// - 0x40 UnitInteract: General interactions (NPCs, waypoints, portals, shrines, chests, entrances) - just UnitID
// - 0x41 UnitInteractEx: Stateful object interactions (doors) - needs UnitID + ObjectState + FixFF
func ShouldUseSimpleInteraction(obj data.Object) bool {
	// 0x41 (extended) ONLY for doors
	// Doors need ObjectState to properly toggle open/closed
	if obj.IsDoor() {
		return false
	}

	// 0x41 (extended) for doors and portals (portals use object state in practice)
	if obj.IsPortal() || obj.IsRedPortal() {
		return false
	}

	// 0x40 (simple) for everything else:
	// - Waypoints
	// - Shrines
	// - Chests
	// - Entrances/area transitions
	// - NPCs
	return true
}
