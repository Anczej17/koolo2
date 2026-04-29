package amb

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// PullItemFromSharedStash represents packet 0x46 (PullItemFromSharedStash)
// Removes item from shared stash to cursor
type PullItemFromSharedStash struct {
	PacketID byte
	ItemId   uint32
	OwnerId  uint32
	FromPosX uint16
	FromPosY uint16
}

// NewPullItemFromSharedStash creates packet 0x46 for pulling item from shared stash
// ownerId should be the stash tab unit ID (from StashTabUnitIDs array)
func NewPullItemFromSharedStash(item data.Item, stashTabOwnerID uint32) *PullItemFromSharedStash {
	return &PullItemFromSharedStash{
		PacketID: 0x46,
		ItemId:   uint32(item.UnitID),
		OwnerId:  stashTabOwnerID,
		FromPosX: uint16(item.Position.X),
		FromPosY: uint16(item.Position.Y),
	}
}

// GetPayload converts the PullItemFromSharedStash struct to bytes (13 bytes)
// Per PacketOffsetsGo.txt:
// [0]      PacketID (1 byte) = 0x46
// [1-4]    ItemId (4 bytes)
// [5-8]    OwnerId (4 bytes) - stash tab unit ID
// [9-10]   FromPosX (2 bytes)
// [11-12]  FromPosY (2 bytes)
func (p *PullItemFromSharedStash) GetPayload() []byte {
	buf := make([]byte, 13)
	buf[0] = p.PacketID
	binary.LittleEndian.PutUint32(buf[1:5], p.ItemId)
	binary.LittleEndian.PutUint32(buf[5:9], p.OwnerId)
	binary.LittleEndian.PutUint16(buf[9:11], p.FromPosX)
	binary.LittleEndian.PutUint16(buf[11:13], p.FromPosY)
	return buf
}
