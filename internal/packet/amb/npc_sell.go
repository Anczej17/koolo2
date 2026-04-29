package amb

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// NPCSell represents packet 0x33 (NPCSell)
// Sells item to NPC vendor
type NPCSell struct {
	PacketID        byte
	ItemPrice       uint32
	ItemUnitID      uint32
	NPCUnitID       uint32
	ToPosX          uint16
	ToPosY          uint16
	ItemPosX        uint16
	ItemPosY        uint16
	TargetTab       byte
	TargetLocation  byte
	TransactionMode byte
}

// NewNPCSell creates packet 0x33 for selling to NPC
func NewNPCSell(item data.Item, npc data.Monster, itemPrice uint32, toPosX, toPosY uint16, targetTab, targetLocation, transactionMode byte) *NPCSell {
	return &NPCSell{
		PacketID:        0x33,
		ItemPrice:       itemPrice,
		ItemUnitID:      uint32(item.UnitID),
		NPCUnitID:       uint32(npc.UnitID),
		ToPosX:          toPosX,
		ToPosY:          toPosY,
		ItemPosX:        uint16(item.Position.X),
		ItemPosY:        uint16(item.Position.Y),
		TargetTab:       targetTab,
		TargetLocation:  targetLocation,
		TransactionMode: transactionMode,
	}
}

// GetPayload converts the NPCSell struct to bytes (24 bytes)
func (p *NPCSell) GetPayload() []byte {
	buf := make([]byte, 24)
	buf[0] = p.PacketID
	binary.LittleEndian.PutUint32(buf[1:5], p.ItemPrice)
	binary.LittleEndian.PutUint32(buf[5:9], p.ItemUnitID)
	binary.LittleEndian.PutUint32(buf[9:13], p.NPCUnitID)
	binary.LittleEndian.PutUint16(buf[13:15], p.ToPosX)
	binary.LittleEndian.PutUint16(buf[15:17], p.ToPosY)
	binary.LittleEndian.PutUint16(buf[17:19], p.ItemPosX)
	binary.LittleEndian.PutUint16(buf[19:21], p.ItemPosY)
	buf[21] = p.TargetTab
	buf[22] = p.TargetLocation
	buf[23] = p.TransactionMode
	return buf
}
