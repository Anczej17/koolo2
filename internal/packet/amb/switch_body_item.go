package amb

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// SwitchBodyItem represents packet 0x21 (SwitchBodyItem)
// Swaps an item on the cursor with an equipped item
type SwitchBodyItem struct {
	PacketID           byte
	TargetItemUnitId   uint32 // currently equipped item (may be 0 if empty)
	SwitchItemUnitId   uint32 // cursor item unit id
	Unknown1           uint32 // Always 0x00
	Unknown2           uint32 // Always 0x000000FF (value 255)
	TargetItemLocation uint32
}

// NewSwitchBodyItem creates packet 0x21 for swapping equipped items
func NewSwitchBodyItem(targetItem, switchItem data.Item, targetBodyLocation uint32) *SwitchBodyItem {
	return &SwitchBodyItem{
		PacketID:           0x21,
		TargetItemUnitId:   uint32(targetItem.UnitID),
		SwitchItemUnitId:   uint32(switchItem.UnitID),
		Unknown1:           0x00000000,
		Unknown2:           0x000000FF,
		TargetItemLocation: targetBodyLocation,
	}
}

// GetPayload converts the SwitchBodyItem struct to bytes (21 bytes)
func (p *SwitchBodyItem) GetPayload() []byte {
	buf := make([]byte, 21)
	buf[0] = p.PacketID
	// Packet layout (by spec): [1-4]=CursorID, [5-8]=EquippedID
	binary.LittleEndian.PutUint32(buf[1:5], p.SwitchItemUnitId) // Cursor item first
	binary.LittleEndian.PutUint32(buf[5:9], p.TargetItemUnitId) // Then equipped item
	binary.LittleEndian.PutUint32(buf[9:13], p.Unknown1)
	binary.LittleEndian.PutUint32(buf[13:17], p.Unknown2)
	binary.LittleEndian.PutUint32(buf[17:21], p.TargetItemLocation)
	return buf
}
