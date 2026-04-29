package amb

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// NPCBuy represents packet 0x32 (NPCBuy)
// Purchases item from NPC vendor
type NPCBuy struct {
	PacketID        byte
	ItemPrice       uint32
	ItemUnitID      uint32
	NPCUnitID       uint32
	ItemPosX        uint16
	ItemPosY        uint16
	ToPosX          uint16
	ToPosY          uint16
	SourceTab       byte
	TargetLocation  byte
	TransactionMode byte
}

// NewNPCBuy creates packet 0x32 for buying from NPC
func NewNPCBuy(item data.Item, npc data.Monster, itemPrice uint32, toPosX, toPosY uint16, sourceTab, targetLocation, transactionMode byte) *NPCBuy {
	return &NPCBuy{
		PacketID:        0x32,
		ItemPrice:       itemPrice,
		ItemUnitID:      uint32(item.UnitID),
		NPCUnitID:       uint32(npc.UnitID),
		ItemPosX:        uint16(item.Position.X),
		ItemPosY:        uint16(item.Position.Y),
		ToPosX:          toPosX,
		ToPosY:          toPosY,
		SourceTab:       sourceTab,
		TargetLocation:  targetLocation,
		TransactionMode: transactionMode,
	}
}

// GetPayload converts the NPCBuy struct to bytes (24 bytes)
func (p *NPCBuy) GetPayload() []byte {
	buf := make([]byte, 24)
	buf[0] = p.PacketID
	binary.LittleEndian.PutUint32(buf[1:5], p.ItemPrice)
	binary.LittleEndian.PutUint32(buf[5:9], p.ItemUnitID)
	binary.LittleEndian.PutUint32(buf[9:13], p.NPCUnitID)
	binary.LittleEndian.PutUint16(buf[13:15], p.ItemPosX)
	binary.LittleEndian.PutUint16(buf[15:17], p.ItemPosY)
	binary.LittleEndian.PutUint16(buf[17:19], p.ToPosX)
	binary.LittleEndian.PutUint16(buf[19:21], p.ToPosY)
	buf[21] = p.SourceTab
	buf[22] = p.TargetLocation
	buf[23] = p.TransactionMode
	return buf
}
