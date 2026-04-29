package packet

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// NewCainIdentifyItem creates a per-item identify packet (0x5C).
//
// LIVE/SPEC baseline:
//
//	5c [itemGID:u32] [FFFFFFFF]
//
// This is sent once per unidentified item after Cain's identify dialog action.
// page/idx are kept in the API for callers that sequence items, but the D2R
// packet body does not carry them in this verified per-item form.
func NewCainIdentifyItem(itemGID data.UnitID, page uint8, idx uint16) []byte {
	_, _ = page, idx
	buf := make([]byte, 9)
	buf[0] = OpCainIdentifyItem
	binary.LittleEndian.PutUint32(buf[1:5], uint32(itemGID))
	binary.LittleEndian.PutUint32(buf[5:9], 0xFFFFFFFF)
	return buf
}

// NewCainIdentifyAll — 0x34 14B form per sec_id_cain.log CRASHES D2R on
// SendDualPacket (test31 2026-04-19 23:12:47 = 0xC0000005 AV). D2R client
// validates packet length pre-send; any non-expected length triggers AV.
//
// For now returns 0x5C 17B per-item path — unused caller (Cain flow goes
// via HID fallback). Real 0x34 bulk-identify path requires calling D2R's
// internal cain-identify wrapper (RVA not yet found) OR HID click on the
// "Identify All" button after 0x38 option=1 opens the identify submenu.
func NewCainIdentifyAll(cainGID data.UnitID) []byte {
	return NewCainIdentifyItem(cainGID, 0, 0)
}
