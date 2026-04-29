package amb

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// PullItemFromBody represents packet 0x1C (PullItemFromBody)
// Removes item from equipped body slot to cursor
type PullItemFromBody struct {
	PacketID     byte
	ItemGUID     uint32
	BodyLocation uint32
}

// NewPullItemFromBody creates packet 0x1C for removing equipped item to cursor
// bodyLocation should be the body slot (0=head, 1=neck, 2=torso, 3=rhand, 4=lhand, etc.)
func NewPullItemFromBody(item data.Item, bodyLocation uint32) *PullItemFromBody {
	return &PullItemFromBody{
		PacketID:     0x1C,
		ItemGUID:     uint32(item.UnitID),
		BodyLocation: bodyLocation,
	}
}

// GetPayload converts the PullItemFromBody struct to bytes (9 bytes)
func (p *PullItemFromBody) GetPayload() []byte {
	buf := make([]byte, 9)
	buf[0] = p.PacketID
	binary.LittleEndian.PutUint32(buf[1:5], p.ItemGUID)
	binary.LittleEndian.PutUint32(buf[5:9], p.BodyLocation)
	return buf
}
