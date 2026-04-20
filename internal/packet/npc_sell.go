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

// NewNPCSellItem builds the 22-byte 0x33 "sell to merchant" packet.
//
// AUTHORITATIVE live capture capture_buysell.log bufpoll v2 sample [53/54]:
//
//	33 [price:u32] [itemGID:u32] [npcGID:u32] [09 00 08 00] [slot:u16] [seq:u16] [term:u8]
//
// last_diff=21 proves packet length = 22B. Bytes 22-33 visible in hex window are
// RESIDUE from previous 0x26 identify (34B) — not part of 0x33. Writing those
// residue bytes (test30 34B form) CRASHES D2R 0xC0000005 because D2R reads past
// opcode-expected length.
//
// Example (Charsi price=0x1D4C, itemGID=0x38, npcGID=0x0E, slot=5, seq=2, term=0x03):
//
//	33 4c1d0000 38000000 0e000000 09000800 0500 0200 03
func NewNPCSellItem(sellPrice uint32, itemGID, npcGID data.UnitID, slot, seq uint16, term byte) []byte {
	buf := make([]byte, 22)
	buf[0] = OpNPCSellItem
	binary.LittleEndian.PutUint32(buf[1:5], sellPrice)
	binary.LittleEndian.PutUint32(buf[5:9], uint32(itemGID))
	binary.LittleEndian.PutUint32(buf[9:13], uint32(npcGID))
	buf[13] = 0x09
	buf[14] = 0x00
	buf[15] = 0x08
	buf[16] = 0x00
	binary.LittleEndian.PutUint16(buf[17:19], slot)
	binary.LittleEndian.PutUint16(buf[19:21], seq)
	buf[21] = term
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
