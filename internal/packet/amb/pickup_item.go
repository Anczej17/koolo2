package amb

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// PickUpItem represents packet 0x16 (PickupItem)
// Size: 17 bytes (NO PADDING - exact size)
// Purpose: Pick up item from ground to cursor or auto-place in inventory
//
// Cursor field values:
//
//	0 = pick to cursor (for manual placement)
//	1 = auto-place in inventory (game finds free slot)
type PickUpItem struct {
	PacketID byte
	ItemGUID uint32
	X        uint32 // Item's X position on ground (world coordinates)
	Y        uint32 // Item's Y position on ground (world coordinates)
	Cursor   uint32 // 0 = to cursor, 1 = auto-place in inventory
}

// NewPickUpItem creates a PickUpItem packet that auto-places item in inventory
// This is the preferred method - game automatically finds a free slot
func NewPickUpItem(item data.Item) *PickUpItem {
	return &PickUpItem{
		PacketID: 0x16,
		ItemGUID: uint32(item.UnitID),
		X:        uint32(item.Position.X),
		Y:        uint32(item.Position.Y),
		Cursor:   1, // 1 = auto-place in inventory
	}
}

// NewPickUpItemToCursor creates a PickUpItem packet that picks up to cursor
// Use this if you need to manually place the item afterwards
func NewPickUpItemToCursor(item data.Item) *PickUpItem {
	return &PickUpItem{
		PacketID: 0x16,
		ItemGUID: uint32(item.UnitID),
		X:        uint32(item.Position.X),
		Y:        uint32(item.Position.Y),
		Cursor:   0, // 0 = to cursor
	}
}

func (p *PickUpItem) GetPayload() []byte {
	buf := make([]byte, 17)
	buf[0] = byte(p.PacketID)
	binary.LittleEndian.PutUint32(buf[1:], p.ItemGUID)
	binary.LittleEndian.PutUint32(buf[5:], p.X)
	binary.LittleEndian.PutUint32(buf[9:], p.Y)
	binary.LittleEndian.PutUint32(buf[13:], p.Cursor)
	return buf
}
