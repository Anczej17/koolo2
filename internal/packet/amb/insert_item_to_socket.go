package amb

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// InsertItemToSocket represents packet 0x28 (InsertItemToSocket)
// Inserts gem/rune/jewel into socketed item
type InsertItemToSocket struct {
	PacketID       byte
	ItemGUID       uint32
	TargetItemGUID uint32
	Unknown1       uint16 // Always 0x0000
	Unknown2       uint16 // Always 0x0000
	InventoryId    uint32
	ToPosX         uint16
	ToPosY         uint16
}

// NewInsertItemToSocket creates packet 0x28 for socketing items
func NewInsertItemToSocket(socketable data.Item, targetItem data.Item, inventoryId uint32) *InsertItemToSocket {
	return &InsertItemToSocket{
		PacketID:       0x28,
		ItemGUID:       uint32(socketable.UnitID),
		TargetItemGUID: uint32(targetItem.UnitID),
		Unknown1:       0x0000,
		Unknown2:       0x0000,
		InventoryId:    inventoryId,
		ToPosX:         uint16(targetItem.Position.X),
		ToPosY:         uint16(targetItem.Position.Y),
	}
}

// GetPayload converts the InsertItemToSocket struct to bytes (21 bytes)
func (p *InsertItemToSocket) GetPayload() []byte {
	buf := make([]byte, 21)
	buf[0] = p.PacketID
	binary.LittleEndian.PutUint32(buf[1:5], p.ItemGUID)
	binary.LittleEndian.PutUint32(buf[5:9], p.TargetItemGUID)
	binary.LittleEndian.PutUint16(buf[9:11], p.Unknown1)
	binary.LittleEndian.PutUint16(buf[11:13], p.Unknown2)
	binary.LittleEndian.PutUint32(buf[13:17], p.InventoryId)
	binary.LittleEndian.PutUint16(buf[17:19], p.ToPosX)
	binary.LittleEndian.PutUint16(buf[19:21], p.ToPosY)
	return buf
}
