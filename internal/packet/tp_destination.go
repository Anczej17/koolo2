package packet

// 0x4B / 0x43 — TP destination select + travel confirm (sniffed 2026-04-15
// in capture_live.log packets [86]/[88], [92]/[94], [112]/[114], [224]/[226]
// — always paired in this order after a 0x41 TP-interact). 0x41 opens the TP
// dialog; 0x4B with a destination byte selects target area; 0x43 confirms
// and triggers map transition.
//
// 0x4B wire format: 5 bytes `4B <dest:u8> 00 00 00` (buf1 mirror = 2 B head).
//   dest byte values seen: 0x01, 0x04, 0x05, 0x1B (likely area.ID lower byte
//   or dialog row index — needs live verification with controlled TP).
//
// 0x43 wire format: 9 bytes `43 01 00 00 00 01 00 00 00` (buf1 mirror = 6 B
//   head). All fields constant in observed captures — likely a fixed "travel
//   confirm" marker, no parameters needed.

type TpDestinationSelect struct {
	PacketID    byte // 0x4B
	Destination byte
}

func NewTpDestinationSelect(dest byte) *TpDestinationSelect {
	return &TpDestinationSelect{
		PacketID:    0x4B,
		Destination: dest,
	}
}

func (p *TpDestinationSelect) GetPayload() []byte {
	return []byte{p.PacketID, p.Destination, 0, 0, 0}
}

type TpConfirmTravel struct {
	PacketID byte // 0x43
}

func NewTpConfirmTravel() *TpConfirmTravel {
	return &TpConfirmTravel{PacketID: 0x43}
}

func (p *TpConfirmTravel) GetPayload() []byte {
	return []byte{p.PacketID, 0x01, 0, 0, 0, 0x01, 0, 0, 0}
}
