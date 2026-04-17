package packet

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// NewCainIdentifyItem creates a per-item identify packet (0x5C).
//
// LIVE CAPTURE 2026-04-10 (buf1, last_diff=16 = 17 bytes):
//
//	5c [itemGID:u32] [FFFFFFFF] [00000000] [page:u8] [00] [idx:u16] [00] [08]
//
// This is sent once per unidentified item. page/idx track position in
// the identify-all sequence. The trailing 0x08 is a constant marker.
//
// For simple "identify all" use, page=0 idx=0 works (server identifies all).
func NewCainIdentifyItem(itemGID data.UnitID, page uint8, idx uint16) []byte {
	buf := make([]byte, 17)
	buf[0] = OpCainIdentifyItem
	binary.LittleEndian.PutUint32(buf[1:5], uint32(itemGID))
	binary.LittleEndian.PutUint32(buf[5:9], 0xFFFFFFFF)
	// buf[9:13] = zero
	buf[13] = page
	// buf[14] = 0
	binary.LittleEndian.PutUint16(buf[15:17], idx)
	return buf
}

// NewCainIdentifyAll creates a simple identify-all packet (0x5C, 17 bytes).
// Convenience wrapper — sends with page=0, idx=0.
func NewCainIdentifyAll(cainGID data.UnitID) []byte {
	return NewCainIdentifyItem(cainGID, 0, 0)
}
