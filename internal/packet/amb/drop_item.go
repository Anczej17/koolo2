package amb

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// DropItem represents packet 0x17 (DropItem)
// Size: 5 bytes (NO PADDING - exact size)
// Purpose: Drop item from inventory/cursor to ground
type DropItem struct {
	PacketID byte
	ItemGUID uint32 // Item to drop
}

// NewDropItem creates a DropItem packet
// The item will be dropped at the player's current position
func NewDropItem(itemUnitID data.UnitID) *DropItem {
	return &DropItem{
		PacketID: 0x17,
		ItemGUID: uint32(itemUnitID),
	}
}

func (p *DropItem) GetPayload() []byte {
	buf := make([]byte, 5)
	buf[0] = byte(p.PacketID)
	binary.LittleEndian.PutUint32(buf[1:], p.ItemGUID)
	return buf
}
