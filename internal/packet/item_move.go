package packet

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// D2R item-move opcodes (live captured 2026-04-07 → logs/sec_stash.log).
//   0x19 OpItemMoveFrom — move item FROM inventory TO a buffer (stash/cube)
//   0x18 OpItemMoveTo   — move item FROM a buffer (stash/cube) TO inventory
//
// These are NOT the same as 0x54 (OpCubeTransmute). Previous iterations of this
// file built 0x54 packets, which caused D2R to enter the Cube Transmute
// handler and fault through Arxan (~1587 AVs per send) because no cube source
// state was set up.
//
// Format (21 bytes, verified from live capture):
//   [opcode:u8]
//   [itemGID:u32 LE]
//   [marker:u32 LE]   — observed as 0x06 (inv↔stash); 0x05 in some stash→inv
//                        samples. Semantics unclear, using 0x06 as default.
//   [col:u32 LE]       — destination column (in the TARGET container)
//   [row:u32 LE]       — destination row
//   [pad:u32 LE]       — observed as 0 in all captures

// NewItemToStash builds the packet that moves an item from inventory to stash
// at (destCol, destRow). Opcode 0x19.
func NewItemToStash(itemGID data.UnitID, destCol, destRow uint8) []byte {
	return newItemMove19(uint32(itemGID), 0x06, uint32(destCol), uint32(destRow))
}

// NewItemFromStash builds the packet that moves an item from stash to inventory
// at (destCol, destRow). Opcode 0x18.
func NewItemFromStash(itemGID data.UnitID, destCol, destRow uint8) []byte {
	return newItemMove18(uint32(itemGID), 0x06, uint32(destCol), uint32(destRow))
}

func newItemMove19(itemGID, marker, col, row uint32) []byte {
	// Live capture 08_npc_with_trade.json (2026-04-15) — 22B stash-move:
	//   19 [gid:u32] [marker:u32=0x07 sell or 0x04 trade] [01000000]
	//      [00000000] [destCol:u8] 00 [destRow:u8] 00 [term=0x03]
	// The previous 21B builder with marker=0x06 + pad:u32 never matched
	// and D2R crashed on send. Marker 0x07 is the "sell/move" context; we
	// keep that as the default — caller should set 0x04 only for in-trade
	// moves (currently not wired because our trade flow uses 0x33/0x32
	// directly).
	buf := make([]byte, 22)
	buf[0] = 0x19
	binary.LittleEndian.PutUint32(buf[1:5], itemGID)
	_ = marker                                      // unused — see comment above
	binary.LittleEndian.PutUint32(buf[5:9], 0x07)   // marker (sell/move)
	binary.LittleEndian.PutUint32(buf[9:13], 0x01)  // flag=1
	binary.LittleEndian.PutUint32(buf[13:17], 0x0)  // reserved
	buf[17] = uint8(col)
	buf[18] = 0x00
	buf[19] = uint8(row)
	buf[20] = 0x00
	buf[21] = 0x03 // term
	return buf
}

func newItemMove18(itemGID, marker, col, row uint32) []byte {
	// 0x18 live-capture 2026-04-15 is 22 B, not 21 — the previous off-by-1
	// dropped the final trailer byte. With the truncated version the server
	// silently ignored the move (no disconnect, no action), which is how the
	// off-by-1 hid for so long.
	buf := make([]byte, 22)
	buf[0] = 0x18
	binary.LittleEndian.PutUint32(buf[1:5], itemGID)
	binary.LittleEndian.PutUint32(buf[5:9], marker)
	binary.LittleEndian.PutUint32(buf[9:13], col)
	binary.LittleEndian.PutUint32(buf[13:17], row)
	// buf[17:22] zero-filled (observed as sentinel).
	return buf
}
