package packet

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// NewRepairAll creates a repair-all packet (0x35, 16 bytes).
//
// AUTHORITATIVE plaintext capture 2026-04-15 from Discord colleague:
//
//	35 [subcmd:u24 = 0x000004] [npcGID:u32 LE] [cost:u32 LE] [FFFFFFFF]
//
// Example (Akara repair, NPC GID=2, cost=0x115B=4443): `35 04 00 00 02 00 00 00 5B 11 00 00 FF FF FF FF`
//
// Byte layout:
//   +0  u8   0x35 opcode
//   +1  u8   0x04 subcmd (repair-all)
//   +2  u16  0x0000 pad
//   +4  u32  npcGID (repairer NPC, NOT player)
//   +8  u32  cost in gold
//   +12 u32  0xFFFFFFFF sentinel
//
// Prior 18-byte format referenced an older D2R version and included wrong
// fields (playerGID, 0x00020804 constant, trailing 0x12). Current format
// matches what D2R actually sends on the wire.
//
func NewRepairAll(npcGID data.UnitID, repairCost uint32) []byte {
	buf := make([]byte, 16)
	buf[0] = OpRepairAll
	buf[1] = 0x04 // subcmd: repair-all
	// bytes 2,3 pad (0x00 0x00)
	binary.LittleEndian.PutUint32(buf[4:8], uint32(npcGID))
	// bytes 8-11 must be the live repair-all cost from the vendor UI.
	binary.LittleEndian.PutUint32(buf[8:12], repairCost)
	binary.LittleEndian.PutUint32(buf[12:16], 0xFFFFFFFF)
	return buf
}
