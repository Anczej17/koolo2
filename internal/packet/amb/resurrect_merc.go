package amb

import (
	"encoding/binary"
)

const (
	PacketResurrectMerc byte = 0x52 // 82 in decimal (D2R packet ID)
)

// ResurrectMerc packet structure for resurrecting mercenary
// Packet ID: 0x52 (82 in D2R)
type ResurrectMerc struct {
	PacketID byte   // 0x52
	DealerID uint32 // NPC/Dealer unit ID
	NameID   uint16 // Mercenary name ID
	Unk      uint16 // Unknown field
	Cost     uint32 // Resurrection cost
}

// GetPayload converts the ResurrectMerc packet to bytes
func (p *ResurrectMerc) GetPayload() []byte {
	buf := make([]byte, 13) // 1 + 4 + 2 + 2 + 4 = 13 bytes

	buf[0] = p.PacketID
	binary.LittleEndian.PutUint32(buf[1:], p.DealerID)
	binary.LittleEndian.PutUint16(buf[5:], p.NameID)
	binary.LittleEndian.PutUint16(buf[7:], p.Unk)
	binary.LittleEndian.PutUint32(buf[9:], p.Cost)

	return buf
}

// NewResurrectMerc creates a new ResurrectMerc packet
func NewResurrectMerc(dealerID uint32, nameID uint16, cost uint32) *ResurrectMerc {
	return &ResurrectMerc{
		PacketID: PacketResurrectMerc,
		DealerID: dealerID,
		NameID:   nameID,
		Unk:      0, // Unknown field, set to 0
		Cost:     cost,
	}
}
