package packet

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// NewNPCChatInit creates an NPC chat-open packet (0x2F, 13 bytes).
//
// AUTHORITATIVE live capture (logs/live_captures_2026_04_15/02_npc_chat_clean.json
// buf=1 mirror entry#1 tick=40902296):
//
//	2f 0e000000 0e000000 b511fe11
//
// Layout: [2F][npcGID:u32][npcGID:u32][npcX:u16][npcY:u16]
//
// The second u32 appears identical to the first in this capture (both 0x0E
// = Akara). Field-2 semantic is uncertain — could be a repeated npcGID,
// could be entity-type marker, could be playerGID that happened to equal
// Akara's GID. Writing field-2 = npcGID matches observed bytes. Fresh
// capture during Phase 5 with different player/npc GIDs will disambiguate.
//
// Prior 5B "Discord plaintext" form was per-memory CORRECTED 2026-04-15 but
// contradicts buf=1 sniffer bytes — the server accepts 5B via local dispatch
// but silently drops without a dialog state change. CAPTURE_AUDIT_2026_04_19
// revert.
//
// Trade-mode variant extends to 18B with a 5-byte playerPos block in the
// middle — not emitted here; use the 13B form for initial chat open.
func NewNPCChatInit(npcGID data.UnitID, npcX, npcY uint16) []byte {
	buf := make([]byte, 13)
	buf[0] = OpNPCChatInit
	binary.LittleEndian.PutUint32(buf[1:5], uint32(npcGID))
	binary.LittleEndian.PutUint32(buf[5:9], uint32(npcGID))
	binary.LittleEndian.PutUint16(buf[9:11], npcX)
	binary.LittleEndian.PutUint16(buf[11:13], npcY)
	return buf
}

// NewNPCChatTerminate creates an NPC chat-close packet (0x30, 13 bytes).
//
// AUTHORITATIVE live capture 02_npc_chat_clean.json buf=1 entry#3 tick=40903328:
//
//	30 0e000000 0e000000 b511fe11
//
// Layout mirrors 0x2F: [30][npcGID:u32][npcGID:u32][npcX:u16][npcY:u16].
//
// Note: a distinct 22B post-trade variant exists (see NewNPCChatTerminatePostTrade)
// emitted when closing after a sell/buy with an item-move trailer.
func NewNPCChatTerminate(npcGID data.UnitID, npcX, npcY uint16) []byte {
	buf := make([]byte, 13)
	buf[0] = OpNPCChatTerminate
	binary.LittleEndian.PutUint32(buf[1:5], uint32(npcGID))
	binary.LittleEndian.PutUint32(buf[5:9], uint32(npcGID))
	binary.LittleEndian.PutUint16(buf[9:11], npcX)
	binary.LittleEndian.PutUint16(buf[11:13], npcY)
	return buf
}

// NewNPCChatTerminatePostTrade creates the 22-byte post-trade close variant.
//
// AUTHORITATIVE live capture 08_npc_with_trade.json buf=1 entry#54 tick=40788671:
//
//	30 0e000000 07000000 01000000 00000000 0700 0100 03
//
// Layout: [30][npcGID:u32][marker=07:u32][flag=01:u32][pad:u32][slot:u16][seq:u16][term:u8]
//
// Shape matches 0x18/0x19 item-move trailers — 0x30 in post-trade context
// carries the last-touched item slot rather than chat coordinates.
func NewNPCChatTerminatePostTrade(npcGID data.UnitID, slot, seq uint16, term byte) []byte {
	buf := make([]byte, 22)
	buf[0] = OpNPCChatTerminate
	binary.LittleEndian.PutUint32(buf[1:5], uint32(npcGID))
	binary.LittleEndian.PutUint32(buf[5:9], 0x00000007) // marker
	binary.LittleEndian.PutUint32(buf[9:13], 0x00000001) // flag
	// buf[13:17] zero padding
	binary.LittleEndian.PutUint16(buf[17:19], slot)
	binary.LittleEndian.PutUint16(buf[19:21], seq)
	buf[21] = term
	return buf
}
