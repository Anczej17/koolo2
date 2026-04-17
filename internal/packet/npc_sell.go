package packet

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// Terminator bytes observed per item class (bufpoll 2026-04-14 Charsi session).
const (
	NPCSellTermEquipment  byte = 0x03 // white/magic/rare/unique equipment
	NPCSellTermConsumable byte = 0xFF // potions, scrolls, keys, tomes
)

// NewNPCSellItem builds the 24-byte 0x33 "sell to merchant" packet.
//
// CORRECTED 2026-04-15 — Discord plaintext capture shows CONSISTENT 24B
// length across multiple captures:
//
//	33 [price:u32] [itemGID:u32] [npcGID:u32] [pad: 11 bytes zeros]
//
// Examples (both 24B, first 20 bytes visible in log):
//
//	33 8A060000 17000000 08000000 00000000 0000... (price=0x68A, itemGID=0x17)
//	33 07510000 16000000 08000000 00000000 0000... (price=0x5107, itemGID=0x16)
//
// The 22-byte variant previously used (with [09000000][slot:u16][seq:u16][term:u8][00])
// matched a different D2R code path — likely "direct inventory sell" where the
// grid col/row is encoded. The 24-byte format used by the colleague's
// production packet bot is "cursor sell" — bot must first 0x19 PickupBufferItem
// the item onto the cursor, then 0x33 sells whatever is on the cursor. The
// slot/seq/term fields are not needed because item is cursor-tracked.
//
// Since the cursor-sell pattern is more robust (server looks up item by GID
// rather than trusting caller-provided grid coords), switched to 24B default.
// Slot/seq/term params retained for API compat — IGNORED.
//
// Dispatch: after 0x19 pickup, send 0x33 via SendDualPacket.
func NewNPCSellItem(sellPrice uint32, itemGID, npcGID data.UnitID, slot, seq uint16, term byte) []byte {
	_ = slot
	_ = seq
	_ = term
	buf := make([]byte, 24)
	buf[0] = OpNPCSellItem
	binary.LittleEndian.PutUint32(buf[1:5], sellPrice)
	binary.LittleEndian.PutUint32(buf[5:9], uint32(itemGID))
	binary.LittleEndian.PutUint32(buf[9:13], uint32(npcGID))
	// bytes 13..23 = 11 zero bytes (matches Discord capture)
	return buf
}

// NewNPCSellEquipment is a convenience wrapper for normal/magic/rare/unique
// equipment (terminator 0x03).
func NewNPCSellEquipment(sellPrice uint32, itemGID, npcGID data.UnitID, slot, seq uint16) []byte {
	return NewNPCSellItem(sellPrice, itemGID, npcGID, slot, seq, NPCSellTermEquipment)
}

// NewNPCSellConsumable is a convenience wrapper for potions, scrolls, keys,
// tomes (terminator 0xFF).
func NewNPCSellConsumable(sellPrice uint32, itemGID, npcGID data.UnitID, slot, seq uint16) []byte {
	return NewNPCSellItem(sellPrice, itemGID, npcGID, slot, seq, NPCSellTermConsumable)
}
