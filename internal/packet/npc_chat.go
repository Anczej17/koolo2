package packet

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// NewNPCChatInit creates an NPC chat init packet (0x2F, 5 bytes).
//
// AUTHORITATIVE plaintext capture 2026-04-15 from Discord colleague
// (post-decrypt outgoing buffer, 100% accurate — "nothing is encrypted when
// you read the right buffer offsets"):
//
//	2F [npcGID:u32 LE]
//
// Example (Akara NPC GID=8): `2f 08 00 00 00`
//
// Prior 13-byte format with coordinates was a misread of adjacent buffer
// residue — sending 13 bytes caused server to reject and crash D2R's
// server-reply handler. 5 bytes matches D2R's internal send_fn output
// byte-for-byte.
//
// Parameters `playerX`, `playerY` are retained in the signature for
// API stability (many callers pass them); they are ignored.
func NewNPCChatInit(entityGID data.UnitID, playerX, playerY uint16) []byte {
	_ = playerX
	_ = playerY
	buf := make([]byte, 5)
	buf[0] = OpNPCChatInit
	binary.LittleEndian.PutUint32(buf[1:], uint32(entityGID))
	return buf
}

// NewNPCChatTerminate creates an NPC chat close packet (0x30, 5 bytes).
//
// AUTHORITATIVE live capture 2026-04-14 (bufpoll, both buf0 and buf1):
//
//	30 [npcGID:u32 LE]
//
// Dual buffer — dispatch via SendDualPacket. Previous 13-byte layout with
// NPC coords was a misread of buffer residue.
func NewNPCChatTerminate(npcGID data.UnitID) []byte {
	buf := make([]byte, 5)
	buf[0] = OpNPCChatTerminate
	binary.LittleEndian.PutUint32(buf[1:], uint32(npcGID))
	return buf
}
