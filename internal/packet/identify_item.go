package packet

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// NewIdentifyTomeOnItem builds a "identify item with tome" packet (0x26).
//
// D2R format (34 bytes) from ground truth (logs/sec_id_tome.log):
//
//	26 00 [tomeGID:u32] [00ff] [04000000] [00000000] [0207] [ffffffff] [footer:12B]
//
// Footer: 00 00 ff ff ff ff 00 00 ff ff ff ff
//
// This replaces the legacy 0x27 (9-byte) format from D2 LOD. The 0x27
// format crashes D2R or is silently rejected. Must use SendUIPacket.
func NewIdentifyTomeOnItem(tomeGID data.UnitID) []byte {
	buf := make([]byte, 34)
	buf[0] = 0x26
	buf[1] = 0x00
	binary.LittleEndian.PutUint32(buf[2:6], uint32(tomeGID))
	binary.LittleEndian.PutUint16(buf[6:8], 0xFF00) // observed constant
	binary.LittleEndian.PutUint32(buf[8:12], 0x00000004)
	// buf[12:16] zero
	binary.LittleEndian.PutUint16(buf[16:18], 0x0207) // IDENTIFY sub-action
	binary.LittleEndian.PutUint32(buf[18:22], 0xFFFFFFFF)
	copy(buf[22:34], []byte{0x00, 0x00, 0xff, 0xff, 0xff, 0xff, 0x00, 0x00, 0xff, 0xff, 0xff, 0xff})
	return buf
}
