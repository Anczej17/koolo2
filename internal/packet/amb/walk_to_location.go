package amb

import (
	"encoding/binary"
)

// WalkToLocation represents packet 0x01 (WalkToLocation)
// Moves character to specified location at walking speed
//
// 🚨 CRITICAL: Movement packets REQUIRE 201 bytes of padding!
// OpCode Size: 9 bytes (1 opcode + 8 data)
// ACTUAL Size: 210 bytes (9 data + 201 padding)
//
// Without padding = packet rejected = no movement!
//
// Packet Structure:
//
//	[0]     PacketID (0x01)
//	[1:3]   DestX (uint16) - destination X in game world coordinates
//	[3:5]   DestY (uint16) - destination Y in game world coordinates
//	[5:7]   OriginX (uint16) - player's current X position
//	[7:9]   OriginY (uint16) - player's current Y position
//	[9:210] Padding (201 bytes of zeros) - REQUIRED!
type WalkToLocation struct {
	PacketID byte
	DestX    uint16
	DestY    uint16
	OriginX  uint16
	OriginY  uint16
	// Padding is added in GetPayload, not stored in struct
}

// NewWalkToLocation creates packet 0x01 for walking to a location
// Coordinates must be in game world coordinates, NOT screen coordinates
func NewWalkToLocation(destX, destY, originX, originY uint16) *WalkToLocation {
	return &WalkToLocation{
		PacketID: 0x01,
		DestX:    destX,
		DestY:    destY,
		OriginX:  originX,
		OriginY:  originY,
	}
}

// GetPayload converts the WalkToLocation struct to bytes (210 bytes with padding)
// CRITICAL: Buffer must be 210 bytes, not 9!
func (p *WalkToLocation) GetPayload() []byte {
	buf := make([]byte, 210) // 9 data + 201 padding
	buf[0] = p.PacketID
	binary.LittleEndian.PutUint16(buf[1:3], p.DestX)
	binary.LittleEndian.PutUint16(buf[3:5], p.DestY)
	binary.LittleEndian.PutUint16(buf[5:7], p.OriginX)
	binary.LittleEndian.PutUint16(buf[7:9], p.OriginY)
	// buf[9:210] automatically zeroed by make() - this is the required padding
	return buf
}
