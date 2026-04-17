package packet

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// NewNPCDialogOption creates an NPC dialog option select packet (0x38).
//
// AUTHORITATIVE live capture 2026-04-14 (bufpoll, both buf0 and buf1, 9 bytes):
//
//	38 [option:u32 LE] [npcGID:u32 LE]
//
// option=1 is "Trade" (first menu item). Other values for repair, gamble, quest.
// Sent after the NPC dialog is already open (the dialog open itself must be
// driven via HID click — 0x2F from main thread crashes send_fn).
//
// Dual buffer: must be dispatched via SendDualPacket (mirror write + send_fn).
// The old 13-byte layout with trailing coords was residue from a mixed buffer
// read; authoritative bufpoll captures never show those 4 extra bytes.
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
