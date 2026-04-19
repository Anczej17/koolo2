package packet

import "encoding/binary"

// 0x4B / 0x43 — TP destination select + travel confirm. Updated 2026-04-18
// to the 13 B wire format from logs/live_captures_2026_04_15/05_waypoint_travel.json
// buf1 mirror (which reveals the actual transmitted bytes including the
// FFFFFFFF trailer). Previous 5 B / 9 B builders came from buf0 "pre-wrap"
// observations and never matched the server's expected shape.
//
// 0x4B wire format (13 B):
//   [0x4B][dest:u32][action:u32][trailer:u32=0xFFFFFFFF]
//   action observed = 0x00000002 across all TP selects (waypoint + party TP).
//
// 0x43 wire format (13 B):
//   [0x43][act:u32=0x00000001][arg:u32=0x00000001][trailer:u32=0x00000000]
//   All fields constant in observed captures — fixed "travel confirm" marker.
//
// Trailer corrected 2026-04-19 from 0xFFFFFFFF → 0x00000000 per
// CAPTURE_AUDIT_2026_04_19 sec 4.9 — live buf=1 mirror in
// 05_waypoint_travel.json#9 shows zeros, not FFs. Functional impact likely
// nil (server probably tolerates either) but matches wire byte-for-byte.

type TpDestinationSelect struct {
	Destination uint32
	Action      uint32
}

func NewTpDestinationSelect(dest byte) *TpDestinationSelect {
	return &TpDestinationSelect{
		Destination: uint32(dest),
		Action:      0x00000002,
	}
}

func (p *TpDestinationSelect) GetPayload() []byte {
	buf := make([]byte, 13)
	buf[0] = 0x4B
	binary.LittleEndian.PutUint32(buf[1:5], p.Destination)
	binary.LittleEndian.PutUint32(buf[5:9], p.Action)
	binary.LittleEndian.PutUint32(buf[9:13], 0xFFFFFFFF)
	return buf
}

type TpConfirmTravel struct{}

func NewTpConfirmTravel() *TpConfirmTravel {
	return &TpConfirmTravel{}
}

func (p *TpConfirmTravel) GetPayload() []byte {
	buf := make([]byte, 13)
	buf[0] = 0x43
	binary.LittleEndian.PutUint32(buf[1:5], 0x00000001)
	binary.LittleEndian.PutUint32(buf[5:9], 0x00000001)
	binary.LittleEndian.PutUint32(buf[9:13], 0x00000000)
	return buf
}
