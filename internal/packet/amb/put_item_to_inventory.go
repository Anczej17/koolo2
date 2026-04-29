package amb

import (
	"encoding/binary"
)

// PutItemToInventory represents packet 0x18 (PutItemToInventory)
// Places an item from cursor to a container (inventory, equipped, cube, stash)
// Item must be on cursor first (use packet 0x19 to pick up)
type PutItemToInventory struct {
	PacketID    byte
	ItemID      uint32
	PosX        uint32 // X position in grid (0 for equipped items)
	PosY        uint32 // Y position in grid (0 for equipped items)
	InventoryID uint32 // Target container (see InventoryType constants)
}

// InventoryType constants for PutItemToInventory
const (
	InventoryTypeInventory = 0 // INVPAGE_INVENTORY
	InventoryTypeEquipped  = 1 // INVPAGE_EQUIP - equipped items
	InventoryTypeCube      = 3 // INVPAGE_CUBE
	InventoryTypeStash     = 4 // INVPAGE_STASH
)

// NewPutItemToInventory creates packet 0x18 to place item from cursor
// Item must already be on cursor (picked up with packet 0x19)
// For equipped items: use InventoryTypeEquipped with posX=0, posY=0
// For inventory/cube/stash: use appropriate positions
func NewPutItemToInventory(itemID uint32, posX uint32, posY uint32, inventoryID uint32) *PutItemToInventory {
	return &PutItemToInventory{
		PacketID:    0x18,
		ItemID:      itemID,
		PosX:        posX,
		PosY:        posY,
		InventoryID: inventoryID,
	}
}

// GetPayload converts the PutItemToInventory struct to bytes (17 bytes)
func (p *PutItemToInventory) GetPayload() []byte {
	buf := make([]byte, 17)
	buf[0] = p.PacketID
	binary.LittleEndian.PutUint32(buf[1:5], p.ItemID)
	binary.LittleEndian.PutUint32(buf[5:9], p.PosX)
	binary.LittleEndian.PutUint32(buf[9:13], p.PosY)
	binary.LittleEndian.PutUint32(buf[13:17], p.InventoryID)
	return buf
}
