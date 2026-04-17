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
// Pass 0 for empty hand slots.
//
// Live captures show trailing bytes vary per direction:
//   slot0→1: ...FF 00 00 36 00 00   (anim=0x00, tick=0x36, dir=0x00)
//   slot1→0: ...FF 37 00 95 00 01   (anim=0x37, tick=0x95, dir=0x01)
//
// 28 bytes CRASHES send_fn APC (buffer underread). 30 bytes = no crash.
// Server appears to accept zeros for trailing fields.
//
// D2 LOD used opcode 0x60 (1 byte, no args) — that CRASHES D2R.
func NewWeaponSwap(fromLeftGID, fromRightGID, toLeftGID, toRightGID data.UnitID) []byte {
	buf := make([]byte, 30)
	buf[0] = 0x50
	binary.LittleEndian.PutUint32(buf[1:5], uint32(fromLeftGID))
	binary.LittleEndian.PutUint32(buf[5:9], uint32(fromRightGID))
	binary.LittleEndian.PutUint32(buf[9:13], uint32(toLeftGID))
	binary.LittleEndian.PutUint32(buf[13:17], uint32(toRightGID))
	// 8-byte sentinel
	binary.LittleEndian.PutUint64(buf[17:25], 0xFFFFFFFFFFFFFFFF)
	// Trailing 5 bytes: anim state, padding, tick counter, padding, direction.
	// Zeros are accepted by server (verified: 30B no-crash via send_fn APC).
	// buf[25:30] left as zero
	return buf
}
