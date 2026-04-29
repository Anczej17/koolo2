package amb

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// ActivateItem represents packet 0x3E (ActivateItem)
// Activates an item (e.g., scrolls, books, keys)
type ActivateItem struct {
	PacketID byte
	ItemId   uint32
}

// NewActivateItem creates packet 0x3E for activating an item
func NewActivateItem(item data.Item) *ActivateItem {
	return &ActivateItem{
		PacketID: 0x3E,
		ItemId:   uint32(item.UnitID),
	}
}

// GetPayload converts the ActivateItem struct to bytes (5 bytes)
func (p *ActivateItem) GetPayload() []byte {
	buf := make([]byte, 5)
	buf[0] = p.PacketID
	binary.LittleEndian.PutUint32(buf[1:5], p.ItemId)
	return buf
}
