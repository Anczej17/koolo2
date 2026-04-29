package amb

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// PickItemFromContainer represents packet 0x19 (PullItemFromInventory)
// Pulls an item FROM a container (inventory, stash, cube, etc) TO cursor
type PickItemFromContainer struct {
	PacketID  byte
	ItemID    uint32
	X         uint32
	Y         uint32
	Container ContainerType
}

// NewPickItemFromContainer creates packet 0x19 (PullItemFromInventory)
// Pulls item from the specified container at position x, y to cursor
func NewPickItemFromContainer(itemUnitID data.UnitID, x, y int, container ContainerType) *PickItemFromContainer {
	return &PickItemFromContainer{
		PacketID:  0x19, // PickItemFromContainer packet ID (25)
		ItemID:    uint32(itemUnitID),
		X:         uint32(x),
		Y:         uint32(y),
		Container: container,
	}
}

// GetPayload returns the packet payload as bytes
func (p *PickItemFromContainer) GetPayload() []byte {
	// Packet structure (D2R version):
	// [0] PacketID (1 byte)
	// [1-4] ItemID (4 bytes)
	// [5-8] X (4 bytes)
	// [9-12] Y (4 bytes)
	// [13-16] Container (4 bytes)
	buf := make([]byte, 17)

	buf[0] = p.PacketID
	binary.LittleEndian.PutUint32(buf[1:5], p.ItemID)
	binary.LittleEndian.PutUint32(buf[5:9], p.X)
	binary.LittleEndian.PutUint32(buf[9:13], p.Y)
	binary.LittleEndian.PutUint32(buf[13:17], uint32(p.Container))

	return buf
}
