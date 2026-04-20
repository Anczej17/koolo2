package packet

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// NewNPCDialogOption creates an NPC dialog option select packet (0x38, 9B).
//
// Sent after the NPC dialog is already open. Dispatch via SendDualPacket.
//
// Test27 2026-04-19 confirmed: 6B form [38][option:u32][npcGID_low:u8]
// (per live capture 08_npc_with_trade buf=1) CRASHES D2R with 0xC0000005
// AV — the buf=1 "6B" read was likely truncated/residue overlap, not the
// actual wire format. The 9B form [38][option:u32][npcGID:u32] is safe —
// server processes Cain Identify dialog correctly (test24 visual-confirmed).
func NewNPCDialogOption(option uint32, npcGID data.UnitID) []byte {
	buf := make([]byte, 9)
	buf[0] = OpNPCDialogResponse
	binary.LittleEndian.PutUint32(buf[1:5], option)
	binary.LittleEndian.PutUint32(buf[5:9], uint32(npcGID))
	return buf
}

// NewNPCTrade creates a "select Trade option" dialog packet (option=1).
func NewNPCTrade(npcGID data.UnitID) []byte {
	return NewNPCDialogOption(1, npcGID)
}
