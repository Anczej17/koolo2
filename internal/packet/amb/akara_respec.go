package amb

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// AkaraRespec represents packet 0x39 (AkaraRespec)
// Requests respec from Akara NPC
type AkaraRespec struct {
	PacketID byte
	AkaraID  uint32
}

// NewAkaraRespec creates packet 0x39 for requesting respec from Akara
func NewAkaraRespec(akara data.Monster) *AkaraRespec {
	return &AkaraRespec{
		PacketID: 0x39,
		AkaraID:  uint32(akara.UnitID),
	}
}

// GetPayload converts the AkaraRespec struct to bytes (5 bytes)
func (p *AkaraRespec) GetPayload() []byte {
	buf := make([]byte, 5)
	buf[0] = p.PacketID
	binary.LittleEndian.PutUint32(buf[1:5], p.AkaraID)
	return buf
}
