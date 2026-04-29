package amb

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// QuickItemDrop represents the packet for dropping an item directly from inventory/stash
// Packet structure: OUT 0x5C = QuickItemDrop (size=17)
// Format: [ItemUnitId:uint32][Unknown:uint32][FromInvPage:uint32][FromPosX:uint16][FromPosY:uint16]
type QuickItemDrop struct {
	PacketID    byte
	ItemUnitId  uint32
	Unknown     uint32
	FromInvPage uint32
	FromPosX    uint16
	FromPosY    uint16
}

// NewQuickItemDrop creates a quick item drop packet
// This drops an item directly from its current location without needing to pick it up first
func NewQuickItemDrop(item data.Item) *QuickItemDrop {
	return &QuickItemDrop{
		PacketID:    0x5C,
		ItemUnitId:  uint32(item.UnitID),
		Unknown:     0xFFFFFFFF, // Unknown field, using -1 (0xFFFFFFFF) per Auto scripts pattern
		FromInvPage: uint32(item.Location.Page),
		FromPosX:    uint16(item.Position.X),
		FromPosY:    uint16(item.Position.Y),
	}
}

// GetPayload converts the QuickItemDrop struct to bytes
func (p *QuickItemDrop) GetPayload() []byte {
	buf := make([]byte, 17)
	buf[0] = byte(p.PacketID)
	binary.LittleEndian.PutUint32(buf[1:5], p.ItemUnitId)
	binary.LittleEndian.PutUint32(buf[5:9], p.Unknown)
	binary.LittleEndian.PutUint32(buf[9:13], p.FromInvPage)
	binary.LittleEndian.PutUint16(buf[13:15], p.FromPosX)
	binary.LittleEndian.PutUint16(buf[15:17], p.FromPosY)
	return buf
}
