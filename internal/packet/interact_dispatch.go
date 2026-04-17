package packet

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// 0x32 is a generic "entity → entity" interact dispatcher. The semantic
// (use potion / NPC buy / gamble buy / ...) is determined by the unit_type
// byte and the trailing flag bytes.
//
// Common header:
//
//	[0x32][SourceGID:u32 LE][TargetGID:u32 LE][UnitType:u32 LE][flags...]
//
// Concrete sub-formats observed live (logs/sec_potion.log, sec_npc_buy.log,
// sec_gamble.log) — see internal/packet/SPEC.md for detailed table.

// NewUsePotion sends a "drink belt potion" packet (0x32 sub-form, type=0x4E).
//
// Sniffed (23 bytes):
//
//	32 e8030000 83010000 4e000000 0800 0300 0100 [slot]00 0302
//
// playerGID and itemGID are the consumer and the consumed potion. Belt slot
// is the 0..3 belt position; encoded as a u16 (slot byte + 0x00 high byte).
func NewUsePotion(playerGID, itemGID data.UnitID, beltSlot byte) []byte {
	buf := make([]byte, 23)
	buf[0] = OpInteractDispatch
	binary.LittleEndian.PutUint32(buf[1:], uint32(playerGID))
	binary.LittleEndian.PutUint32(buf[5:], uint32(itemGID))
	binary.LittleEndian.PutUint32(buf[9:], UnitTypeBeltPotion)
	// Trailing structure: three u16 flags, slot:u16, sub-action:u16.
	buf[13] = 0x08
	buf[14] = 0x00
	buf[15] = 0x03
	buf[16] = 0x00
	buf[17] = 0x01
	buf[18] = 0x00
	buf[19] = beltSlot
	buf[20] = 0x00
	buf[21] = 0x03
	buf[22] = 0x02
	return buf
}

// NewNPCBuy builds the 22-byte 0x32 "buy from merchant" packet.
//
// AUTHORITATIVE live capture 2026-04-14 (bufpoll, both buf0 and buf1):
//
//	32 [price:u32 LE] [itemGID:u32 LE] [npcGID:u32 LE] [09 00 00 00] \
//	   [slot:u16 LE] [seq:u16 LE] [term:u8] [00]
//
// Example (Jewel bought back from Charsi GID=8 for 1466 gold, slot=5, seq=2):
//
//	32 ba050000 3e000000 08000000 09000000 0500 0200 03 00
//
// Layout identical to 0x33 sell — only opcode differs. Updated 2026-04-15
// to 24 bytes matching Discord plaintext capture (cursor-buy mode).
// Slot/seq/term retained for API compat; IGNORED.
func NewNPCBuy(price uint32, itemGID, npcGID data.UnitID, slot, seq uint16, term byte) []byte {
	_ = slot
	_ = seq
	_ = term
	buf := make([]byte, 24)
	buf[0] = OpInteractDispatch
	binary.LittleEndian.PutUint32(buf[1:5], price)
	binary.LittleEndian.PutUint32(buf[5:9], uint32(itemGID))
	binary.LittleEndian.PutUint32(buf[9:13], uint32(npcGID))
	// bytes 13..23 = 11 zero bytes (Discord-captured layout)
	return buf
}

// NewGambleBuy sends a "gamble item" packet (0x32 sub-form, type=0x06).
//
// Sniffed (23 bytes):
//
//	32 18f60000 7d020000 06000000 0900 0100 0500 [slot]00 0001
//
// gambleSlot is the column index in the gamble window of the item being bought.
// Slot is encoded as a u16 (slot byte + 0x00 high byte).
func NewGambleBuy(playerGID, gambleItemGID data.UnitID, gambleSlot byte) []byte {
	buf := make([]byte, 23)
	buf[0] = OpInteractDispatch
	binary.LittleEndian.PutUint32(buf[1:], uint32(playerGID))
	binary.LittleEndian.PutUint32(buf[5:], uint32(gambleItemGID))
	binary.LittleEndian.PutUint32(buf[9:], UnitTypeGambledItem)
	buf[13] = 0x09
	buf[14] = 0x00
	buf[15] = 0x01
	buf[16] = 0x00
	buf[17] = 0x05
	buf[18] = 0x00
	buf[19] = gambleSlot
	buf[20] = 0x00
	buf[21] = 0x00
	buf[22] = 0x01
	return buf
}
