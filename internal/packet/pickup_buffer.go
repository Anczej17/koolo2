package packet

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// NewPickupBufferItem creates the item-to-cursor pickup packet (0x19, 17 bytes).
//
// AUTHORITATIVE plaintext capture 2026-04-15 from Discord colleague:
//
//	19 [itemGID:u32 LE] [sourceMarker:u32 LE] [col/row:u32 LE] [pad: u32] [terminator: u8]
//
// Example captures observed:
//
//	19 17 00 00 00 02 00 00 00 00 00 00 00 00 00 00 00   (itemGID=23, src=inventory)
//	19 16 00 00 00 00 00 00 00 02 00 00 00 00 00 00 00   (itemGID=22, src=stash)
//
// The second u32 appears to be a source marker (0x02 = inventory) and the
// third u32 is the grid location (varies by container). When source = 0,
// the third u32 holds the col+row packed value. Semantics are partially
// reverse-engineered; the builder preserves observed byte layout and lets
// the caller pass raw source/position ints.
//
// Use-case: D2R pickups an inventory or stash item onto the cursor BEFORE
// a subsequent 0x33 (sell), 0x54 (stash move), or drop action. Many of our
// prior sell-path issues were caused by omitting this preliminary pickup —
// the server rejects 0x33 when cursor is empty and no slot is attached.
//
// Parameters:
//   itemGID — unit GID of the item to pick up
//   source  — source container marker (2 = inventory, 0 = stash, TBC for cube/equip)
//   gridPos — col+row packed (col = gridPos & 0xFF, row = (gridPos>>8) & 0xFF) OR 0
func NewPickupBufferItem(itemGID data.UnitID, source uint32, gridPos uint32) []byte {
	buf := make([]byte, 17)
	buf[0] = OpItemMoveFrom // 0x19 — item → cursor buffer (aka PickupBufferItem)
	binary.LittleEndian.PutUint32(buf[1:5], uint32(itemGID))
	binary.LittleEndian.PutUint32(buf[5:9], source)
	binary.LittleEndian.PutUint32(buf[9:13], gridPos)
	// bytes 13-16 are zero padding (trailing)
	return buf
}
