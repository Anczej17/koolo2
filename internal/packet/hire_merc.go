package packet

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// NewHireMerc creates a "hire / revive mercenary" packet (0x52).
//
// Sniffed (logs/sec_merc.log):
//
//	52 50000000 ff030000 50c30000 00000050 000000 9f139c13
//
// Layout (best interpretation):
//
//	[0x52][UnitType:u32 LE][MercGID:u32 LE][Cost:u32 LE][Padding:u32 LE][Coords?:u32 LE]
//
// 0x50 = 80 = unit_type observed (likely "hireling slot"). Cost = gold price.
// Trailing 4 bytes look like player coordinates (0x9F13/0x9C13 = ~5023/5020).
//
// Status: SNIFFED, NOT LIVE-TESTED. Single capture during merc revive at NPC.
func NewHireMerc(mercGID data.UnitID, cost uint32, playerX, playerY uint16) []byte {
	buf := make([]byte, 21)
	buf[0] = OpHireMerc
	binary.LittleEndian.PutUint32(buf[1:], 0x50) // unit_type = 0x50
	binary.LittleEndian.PutUint32(buf[5:], uint32(mercGID))
	binary.LittleEndian.PutUint32(buf[9:], cost)
	binary.LittleEndian.PutUint32(buf[13:], 0) // padding
	binary.LittleEndian.PutUint16(buf[17:], playerX)
	binary.LittleEndian.PutUint16(buf[19:], playerY)
	return buf
}
