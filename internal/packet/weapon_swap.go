package packet

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// NewWeaponSwap creates a weapon swap packet (0x50).
//
// D2R format (30 bytes) from ground truth (logs/sec_swap.log, 48 captures):
//
//	50 [fromL:u32] [fromR:u32] [toL:u32] [toR:u32] [FFFFFFFFFFFFFFFF] [anim:u8] [00] [tick:u8] [00] [dir:u8]
//
// fromL/fromR = weapon GIDs of the currently active slot (being swapped FROM).
// toL/toR = weapon GIDs of the inactive slot (being swapped TO).
// Pass 0 for empty hand slots. fromSlot = 0 or 1 (the currently-active slot).
//
// Live captures (project_0x50_live_test_results 04-13):
//   slot0→1 (fromSlot=0): ...FF 00 00 36 00 00   (anim=0x00, tick=0x36, dir=0x00)
//   slot1→0 (fromSlot=1): ...FF 37 00 95 00 01   (anim=0x37, tick=0x95, dir=0x01)
//
// Live test23 04-19 confirmed: server rejects a swap with all-zero trailer
// OR with wrong-direction trailer (e.g. 37/0/0/0/0 for a 0→1 swap). The
// anim + dir bytes must match the direction; tick is a low byte of game
// state (~0x36/0x95 observed) but server tolerance is unknown — use the
// observed typical value as a best-match default.
func NewWeaponSwap(fromLeftGID, fromRightGID, toLeftGID, toRightGID data.UnitID, fromSlot uint8) []byte {
	buf := make([]byte, 30)
	buf[0] = 0x50
	binary.LittleEndian.PutUint32(buf[1:5], uint32(fromLeftGID))
	binary.LittleEndian.PutUint32(buf[5:9], uint32(fromRightGID))
	binary.LittleEndian.PutUint32(buf[9:13], uint32(toLeftGID))
	binary.LittleEndian.PutUint32(buf[13:17], uint32(toRightGID))
	binary.LittleEndian.PutUint64(buf[17:25], 0xFFFFFFFFFFFFFFFF)
	if fromSlot == 0 {
		buf[25] = 0x00
		buf[26] = 0x00
		buf[27] = 0x36
		buf[28] = 0x00
		buf[29] = 0x00
	} else {
		buf[25] = 0x37
		buf[26] = 0x00
		buf[27] = 0x95
		buf[28] = 0x00
		buf[29] = 0x01
	}
	return buf
}
