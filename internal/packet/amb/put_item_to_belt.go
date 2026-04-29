package amb

import (
	"encoding/binary"
)

// PutItemToBelt moves an item from inventory/stash to belt using QuickMove
// Packet 0x53, size 21 bytes
type PutItemToBelt struct {
	ItemID      uint32
	Unknown     uint32
	FromInvPage uint32
	FromPosX    uint16
	FromPosY    uint16
	ToPosX      uint16
	ToPosY      uint16
}

func NewPutItemToBelt(itemID uint32, fromInvPage uint32, fromX, fromY, toX, toY uint16) *PutItemToBelt {
	return &PutItemToBelt{
		ItemID:      itemID,
		Unknown:     0,
		FromInvPage: fromInvPage,
		FromPosX:    fromX,
		FromPosY:    fromY,
		ToPosX:      toX,
		ToPosY:      toY,
	}
}

func (p *PutItemToBelt) GetPayload() []byte {
	payload := make([]byte, 21)

	// Opcode
	payload[0] = 0x53

	binary.LittleEndian.PutUint32(payload[1:5], p.ItemID)
	binary.LittleEndian.PutUint32(payload[5:9], p.Unknown)
	binary.LittleEndian.PutUint32(payload[9:13], p.FromInvPage)
	binary.LittleEndian.PutUint16(payload[13:15], p.FromPosX)
	binary.LittleEndian.PutUint16(payload[15:17], p.FromPosY)
	binary.LittleEndian.PutUint16(payload[17:19], p.ToPosX)
	binary.LittleEndian.PutUint16(payload[19:21], p.ToPosY)

	return payload
}
