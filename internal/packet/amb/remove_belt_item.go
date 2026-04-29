package amb

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// RemoveBeltItem represents packet 0x24 (RemoveBeltItem)
// Removes potion from belt - complex packet with multiple item states
type RemoveBeltItem struct {
	PacketID          byte
	ItemUnitID1       uint32
	ItemPosX1         byte
	ItemUnitID2       uint32
	ItemPosXCurrent2  byte
	ItemPosXPrevious2 byte
	ItemUnitID3       uint32
	ItemPosXCurrent3  byte
	ItemPosXPrevious3 byte
	ItemUnitID4       uint32
	ItemPosXCurrent4  byte
	ItemPosXPrevious4 byte
}

// RemoveBeltItemEntry is a small helper describing an item unit and its X position
// used to build RemoveBeltItem packets from higher-level callers.
type RemoveBeltItemEntry struct {
	UnitID data.UnitID
	PosX   byte
}

// NewRemoveBeltItemFromSlice builds a RemoveBeltItem packet from an ordered slice
// of up to 4 entries. The expected ordering matches the Lua constructor: the
// first entry is the removed (bottom) item, subsequent entries are the items
// above it in the column (closest above first). Missing entries are filled
// with sentinel values (0xFFFFFFFF for unit id and 0x00 for pos bytes).
func NewRemoveBeltItemFromSlice(items []RemoveBeltItemEntry) *RemoveBeltItem {
	// default sentinel values
	const sentinelID uint32 = 0xFFFFFFFF

	var id1, id2, id3, id4 uint32 = sentinelID, sentinelID, sentinelID, sentinelID
	var x1, cur2, prev2, cur3, prev3, cur4, prev4 byte

	if len(items) >= 1 {
		id1 = uint32(items[0].UnitID)
		x1 = items[0].PosX
	}

	if len(items) >= 2 {
		id2 = uint32(items[1].UnitID)
		cur2 = items[1].PosX
		prev2 = x1
	}

	if len(items) >= 3 {
		id3 = uint32(items[2].UnitID)
		cur3 = items[2].PosX
		// previous for item3 is the PosX of item2 (if present)
		prev3 = cur2
	}

	if len(items) >= 4 {
		id4 = uint32(items[3].UnitID)
		cur4 = items[3].PosX
		// previous for item4 is the PosX of item3 (if present)
		prev4 = cur3
	}

	return NewRemoveBeltItem(data.UnitID(id1), x1, data.UnitID(id2), cur2, prev2, data.UnitID(id3), cur3, prev3, data.UnitID(id4), cur4, prev4)
}

// NewRemoveBeltItem creates packet 0x24 for removing belt item
func NewRemoveBeltItem(itemUnitID1 data.UnitID, itemPosX1 byte, itemUnitID2 data.UnitID, posXCur2, posXPrev2 byte, itemUnitID3 data.UnitID, posXCur3, posXPrev3 byte, itemUnitID4 data.UnitID, posXCur4, posXPrev4 byte) *RemoveBeltItem {
	return &RemoveBeltItem{
		PacketID:          0x24,
		ItemUnitID1:       uint32(itemUnitID1),
		ItemPosX1:         itemPosX1,
		ItemUnitID2:       uint32(itemUnitID2),
		ItemPosXCurrent2:  posXCur2,
		ItemPosXPrevious2: posXPrev2,
		ItemUnitID3:       uint32(itemUnitID3),
		ItemPosXCurrent3:  posXCur3,
		ItemPosXPrevious3: posXPrev3,
		ItemUnitID4:       uint32(itemUnitID4),
		ItemPosXCurrent4:  posXCur4,
		ItemPosXPrevious4: posXPrev4,
	}
}

// GetPayload converts the RemoveBeltItem struct to bytes (24 bytes)
func (p *RemoveBeltItem) GetPayload() []byte {
	buf := make([]byte, 24)
	buf[0] = p.PacketID
	binary.LittleEndian.PutUint32(buf[1:5], p.ItemUnitID1)
	buf[5] = p.ItemPosX1
	binary.LittleEndian.PutUint32(buf[6:10], p.ItemUnitID2)
	buf[10] = p.ItemPosXCurrent2
	buf[11] = p.ItemPosXPrevious2
	binary.LittleEndian.PutUint32(buf[12:16], p.ItemUnitID3)
	buf[16] = p.ItemPosXCurrent3
	buf[17] = p.ItemPosXPrevious3
	binary.LittleEndian.PutUint32(buf[18:22], p.ItemUnitID4)
	buf[22] = p.ItemPosXCurrent4
	buf[23] = p.ItemPosXPrevious4
	return buf
}
