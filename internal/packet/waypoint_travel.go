package packet

import (
	"encoding/binary"
)

// NewWaypointTravel creates a waypoint travel packet (0x49).
// [0x49][WaypointObjectID:u32 LE][Destination:u8][0x00][0x00][0x00] — 9 bytes.
// Must interact with WP object (0x13) first to open WP menu.
// Destination is the area/level ID. Destination=0 closes the WP menu.
func NewWaypointTravel(waypointObjectID uint32, destination byte) []byte {
	buf := make([]byte, 9)
	buf[0] = 0x49
	binary.LittleEndian.PutUint32(buf[1:], waypointObjectID)
	buf[5] = destination
	// buf[6..8] = 0x00 padding (already zero from make)
	return buf
}
