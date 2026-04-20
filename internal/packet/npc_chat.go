package packet

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// NewNPCChatInit creates an NPC chat-open packet (0x2F, 5 bytes).
//
// ROLLBACK 2026-04-20 18:44: The 13B form [opcode][npcGID][npcGID][npcPos]
// derived from buf=1 mirror capture 02_npc_chat_clean.json CRASHES D2R
// ~100-150ms after emit (correlated over two runs at 18:43:32 and 18:44:07,
// both fatal 0xC0000005 shortly after 0x30 13B landed on server). The 13B
// shape is what D2R ITSELF writes to its mirror buffer via dual_send_wrap
// when a user closes a dialog — NOT a valid externally-submitted packet.
// D2R's packet validator on a bot-emitted 13B 0x2F/0x30 trips and AVs.
//
// Back to 5B [opcode][npcGID:u32] — the original koolo shape. Server
// silently drops without opening dialog, so callers must HID-fallback for
// NPC open (step/interact_npc.go already does this after a 1.7s timeout).
//
// Extra playerX/Y params retained for API compatibility, ignored.
func NewNPCChatInit(npcGID data.UnitID, npcX, npcY uint16) []byte {
	_ = npcX
	_ = npcY
	buf := make([]byte, 5)
	buf[0] = OpNPCChatInit
	binary.LittleEndian.PutUint32(buf[1:5], uint32(npcGID))
	return buf
}

// NewNPCChatTerminate creates an NPC chat-close packet (0x30, 5 bytes).
//
// ROLLBACK 2026-04-20 18:44: same reason as 0x2F above — 13B form CRASHES
// D2R ~100ms after emit. buf=1 13B is D2R's internal mirror write, not an
// acceptable external packet. 5B is the koolo-original safe shape.
//
// The 22B post-trade variant (NewNPCChatTerminatePostTrade) lives in its
// own function and MAY still be valid for the specific sell/buy-close
// context — awaiting fresh empirical test during Phase 5b.
func NewNPCChatTerminate(npcGID data.UnitID, npcX, npcY uint16) []byte {
	_ = npcX
	_ = npcY
	buf := make([]byte, 5)
	buf[0] = OpNPCChatTerminate
	binary.LittleEndian.PutUint32(buf[1:5], uint32(npcGID))
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
