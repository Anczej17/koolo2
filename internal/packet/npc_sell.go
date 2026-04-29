package packet

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// Terminator bytes observed per item class.
const (
	NPCSellTermEquipment  byte = 0x03 // white/magic/rare/unique equipment
	NPCSellTermConsumable byte = 0xFF // potions, scrolls, keys, tomes
)

// NewNPCSellItem builds the 24-byte 0x33 "sell to merchant" packet.
//
// Live Akara proof, 2026-04-26:
//
//	33 [price:u32] [itemGID:u32] [npcGID:u32] [toX:u16] [toY:u16] [itemX:u16] [itemY:u16] [term:u8] [00 00]
//
// This layout sells successfully through D2GS main-thread APC. UI NetMan alone
// is a no-op; ui-dual and dualwrap-GT are unsafe for vendor sell in this build.
func NewNPCSellItem(sellPrice uint32, itemGID, npcGID data.UnitID, toPosX, toPosY, itemPosX, itemPosY uint16, term byte) []byte {
	buf := make([]byte, 24)
	buf[0] = OpNPCSellItem
	binary.LittleEndian.PutUint32(buf[1:5], sellPrice)
	binary.LittleEndian.PutUint32(buf[5:9], uint32(itemGID))
	binary.LittleEndian.PutUint32(buf[9:13], uint32(npcGID))
	binary.LittleEndian.PutUint16(buf[13:15], toPosX)
	binary.LittleEndian.PutUint16(buf[15:17], toPosY)
	binary.LittleEndian.PutUint16(buf[17:19], itemPosX)
	binary.LittleEndian.PutUint16(buf[19:21], itemPosY)
	buf[21] = term
	buf[22] = 0
	buf[23] = 0
	return buf
}

// NewNPCSellEquipment is a convenience wrapper for normal/magic/rare/unique
// equipment (terminator 0x03).
func NewNPCSellEquipment(sellPrice uint32, itemGID, npcGID data.UnitID, slot, seq uint16) []byte {
	return NewNPCSellItem(sellPrice, itemGID, npcGID, 9, 8, slot, seq, NPCSellTermEquipment)
}

// NewNPCSellConsumable is a convenience wrapper for potions, scrolls, keys,
// tomes (terminator 0xFF).
func NewNPCSellConsumable(sellPrice uint32, itemGID, npcGID data.UnitID, slot, seq uint16) []byte {
	return NewNPCSellItem(sellPrice, itemGID, npcGID, 0, 0, slot, seq, NPCSellTermConsumable)
}
