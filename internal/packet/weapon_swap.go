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
	// Trailer layout, corrected 2026-04-21 from live capture of user's
	// manual CTA pre-buff sequence in Outer Cloister:
	//   slot 0->1: ...FF FF FF FF FF FF FF FF 37 00 95 00 01
	//   slot 1->0: ...FF FF FF FF FF FF FF FF 37 00 28 00 00
	// So:
	//   [25] = 0x37 anim byte, CONSTANT for both directions
	//   [26] = 0x00 constant
	//   [27:29] = u16 session seq counter (observed 95/28/36/9b — varies;
	//             server appears not to strict-validate, 0 works)
	//   [29] = target weapon slot = fromSlot XOR 1
	// Old builder wrote [25]=0x00 for fromSlot==0 AND set [29]=fromSlot
	// (not target slot). Both wrong — server silent-drops an all-zero-anim
	// trailer, and a dir byte matching the current slot means "swap to
	// where I already am" which the server treats as no-op.
	buf[25] = 0x37
	buf[26] = 0x00
	buf[27] = 0x00
	buf[28] = 0x00
	buf[29] = fromSlot ^ 1
	return buf
}
