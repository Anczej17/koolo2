package amb

import (
	"encoding/binary"
)

// DropGold represents packet 0x47 (DropGold)
// Drops gold from inventory to ground
type DropGold struct {
	PacketID           byte
	InventoryGoldCount uint32
	DropGoldCount      uint32
}

// NewDropGold creates packet 0x47 for dropping gold on ground
func NewDropGold(inventoryGoldCount, dropGoldCount uint32) *DropGold {
	return &DropGold{
		PacketID:           0x47,
		InventoryGoldCount: inventoryGoldCount,
		DropGoldCount:      dropGoldCount,
	}
}

// GetPayload converts the DropGold struct to bytes (9 bytes)
func (p *DropGold) GetPayload() []byte {
	buf := make([]byte, 9)
	buf[0] = p.PacketID
	binary.LittleEndian.PutUint32(buf[1:5], p.InventoryGoldCount)
	binary.LittleEndian.PutUint32(buf[5:9], p.DropGoldCount)
	return buf
}
