package amb

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data/area"
)

// WaypointInteraction represents the packet for interacting with waypoints
// Packet structure (5 bytes total):
// [PacketID:0x4B][LevelNo:uint32]
//
// Specification: OUT 0x4B = ToWaypoint (size=5)
// The LevelNo field is the Area ID to teleport to
//
// CRITICAL: The waypoint menu MUST be open and on the correct act tab
// before sending this packet!
//
// Correct flow:
// 1. Move within ~10 tiles of waypoint
// 2. Interact with waypoint (0x40) to open menu
// 3. Wait for OpenMenus.Waypoint to be true
// 4. Click the correct act tab
// 5. Send ToWaypoint(destinationAreaID) using this packet
//
// Example area IDs:
// - Rogue Encampment = 1
// - Harrogath = 109
// - Durance of Hate Level 2 = 101
// - Lost City = 44
type WaypointInteraction struct {
	PacketID byte
	LevelNo  uint32
}

// NewWaypointInteraction creates a waypoint interaction packet
// destinationArea: The area ID to teleport to via waypoint
func NewWaypointInteraction(destinationArea area.ID) *WaypointInteraction {
	return &WaypointInteraction{
		PacketID: 0x4B,                    // CORRECT opcode per PacketOffsetsGo.txt
		LevelNo:  uint32(destinationArea), // Area ID as uint32
	}
}

// GetPayload converts the WaypointInteraction struct to bytes
func (p *WaypointInteraction) GetPayload() []byte {
	buf := make([]byte, 5)
	buf[0] = p.PacketID
	binary.LittleEndian.PutUint32(buf[1:], p.LevelNo)

	return buf
}
