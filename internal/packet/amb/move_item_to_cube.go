package amb

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// MoveItemToCube represents packet 0x2A (MoveItemToCube)
// Specifically for placing items INTO the Horadric Cube from cursor
// Must have item on cursor first (use packet 0x19 to pick up)
type MoveItemToCube struct {
	PacketID   byte
	ItemUnitID uint32
	CubeUnitID uint32
	Unknown1   uint32 // Always 0x00000004 in captures
	Unknown2   uint16 // Position related?
	Unknown3   uint16 // Position related?
	ToPosX     uint16
	ToPosY     uint16
}

// NewMoveItemToCube creates packet 0x2A for placing item into cube
// Item must already be on cursor (picked up with packet 0x19)
func NewMoveItemToCube(itemUnitID, cubeUnitID data.UnitID, toPosX, toPosY uint16) *MoveItemToCube {
	return &MoveItemToCube{
		PacketID:   0x2A,
		ItemUnitID: uint32(itemUnitID),
		CubeUnitID: uint32(cubeUnitID),
		Unknown1:   0x00000000, // Per documentation: Constant 0
		Unknown2:   0x0000,     // Per documentation: Constant 0
		Unknown3:   0x0002,     // Per documentation: Constant 2
		ToPosX:     toPosX,
		ToPosY:     toPosY,
	}
}

// GetPayload converts the MoveItemToCube struct to bytes (21 bytes)
func (p *MoveItemToCube) GetPayload() []byte {
	buf := make([]byte, 21)
	buf[0] = p.PacketID
	binary.LittleEndian.PutUint32(buf[1:5], p.ItemUnitID)
	binary.LittleEndian.PutUint32(buf[5:9], p.CubeUnitID)
	binary.LittleEndian.PutUint32(buf[9:13], p.Unknown1)
	binary.LittleEndian.PutUint16(buf[13:15], p.Unknown2)
	binary.LittleEndian.PutUint16(buf[15:17], p.Unknown3)
	binary.LittleEndian.PutUint16(buf[17:19], p.ToPosX)
	binary.LittleEndian.PutUint16(buf[19:21], p.ToPosY)
	return buf
}
