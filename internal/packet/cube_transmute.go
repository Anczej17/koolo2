package packet

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// NewCubeTransmute creates a Horadric Cube transmute packet (0x54).
//
// D2R format (34 bytes) from ground truth (logs/sec_cube.log):
//
//	54 [cubeGID:u32] [00000000] [05] [00] [02] [00] [03000000] [footer:17B]
//
// Footer: 02 00 02 00 ff 00 00 ff ff ff ff 00 00 ff ff ff ff
//
// D2R changed cube transmute from 0x20 (D2 LOD) to 0x54. The ingredient
// list is NOT sent — the server knows what is inside the cube. Only the
// cube container GID is needed.
//
// After 0x54, D2R also sends a 0x19 follow-up packet (~590ms later) to
// pick up the result. That is handled separately.
func NewCubeTransmute(cubeGID data.UnitID) []byte {
	buf := make([]byte, 34)
	buf[0] = 0x54
	binary.LittleEndian.PutUint32(buf[1:5], uint32(cubeGID))
	// buf[5:9] zero
	buf[9] = 0x05
	buf[10] = 0x00
	buf[11] = 0x02
	buf[12] = 0x00
	binary.LittleEndian.PutUint32(buf[13:17], 0x00000003)
	copy(buf[17:34], []byte{0x02, 0x00, 0x02, 0x00, 0xff, 0x00, 0x00, 0xff, 0xff, 0xff, 0xff, 0x00, 0x00, 0xff, 0xff, 0xff, 0xff})
	return buf
}

// NewCubeCommit creates the cube transmute commit/ack packet (0x19).
//
// D2R sends this ~590ms after 0x54 to finalize the transmute and pick up
// the result. Same 34-byte structure, same footer.
//
// Ground truth (logs/sec_cube.log):
//
//	19 [cubeGID:u32] [02000000] [02000000] [03000000] [footer:17B]
func NewCubeCommit(cubeGID data.UnitID) []byte {
	buf := make([]byte, 34)
	buf[0] = 0x19
	binary.LittleEndian.PutUint32(buf[1:5], uint32(cubeGID))
	binary.LittleEndian.PutUint32(buf[5:9], 0x00000002)
	binary.LittleEndian.PutUint32(buf[9:13], 0x00000002)
	binary.LittleEndian.PutUint32(buf[13:17], 0x00000003)
	copy(buf[17:34], []byte{0x02, 0x00, 0x02, 0x00, 0xff, 0x00, 0x00, 0xff, 0xff, 0xff, 0xff, 0x00, 0x00, 0xff, 0xff, 0xff, 0xff})
	return buf
}
