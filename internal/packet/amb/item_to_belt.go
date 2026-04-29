package amb

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// ItemToBelt represents packet 0x23 (ItemToBelt)
// Places item into belt slot
type ItemToBelt struct {
	PacketID   byte
	ItemUnitID uint32
	BeltPosX   uint32
}

// NewItemToBelt creates packet 0x23 for placing item in belt
func NewItemToBelt(item data.Item, beltPosX uint32) *ItemToBelt {
	return &ItemToBelt{
		PacketID:   0x23,
		ItemUnitID: uint32(item.UnitID),
		BeltPosX:   beltPosX,
	}
}

// GetPayload converts the ItemToBelt struct to bytes (9 bytes)
func (p *ItemToBelt) GetPayload() []byte {
	buf := make([]byte, 9)
	buf[0] = p.PacketID
	binary.LittleEndian.PutUint32(buf[1:5], p.ItemUnitID)
	binary.LittleEndian.PutUint32(buf[5:9], p.BeltPosX)
	return buf
}
