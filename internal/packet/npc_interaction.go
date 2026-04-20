package packet

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// Entity types for interact packet 0x13.
const (
	EntityTypePlayer   uint32 = 0
	EntityTypeNPC      uint32 = 1
	EntityTypeObject   uint32 = 2
	EntityTypeMissile  uint32 = 3
	EntityTypeItem     uint32 = 4
	EntityTypeEntrance uint32 = 5
)

// NewEntityInteract creates an entity interact packet (0x13).
// [0x13][EntityType:u32 LE][EntityGID:u32 LE] — 9 bytes.
// Used for object interaction (stash, WP, chests), etc.
// NOTE: For NPC trade interaction, 0x13 is IGNORED by server.
// Use NewNPCEntityAction (0x4D) + NewNPCChatInit (0x2F) instead.
func NewEntityInteract(entityType uint32, entityGID data.UnitID) []byte {
	buf := make([]byte, 9)
	buf[0] = 0x13
	binary.LittleEndian.PutUint32(buf[1:], entityType)
	binary.LittleEndian.PutUint32(buf[5:], uint32(entityGID))
	return buf
}

// NewNPCInteract creates an NPC interact packet.
// Shorthand for NewEntityInteract(EntityTypeNPC, npcGID).
// WARNING: Server ignores 0x13 for NPC trade. Use NewNPCEntityAction + NewNPCChatInit.
func NewNPCInteract(npcGID data.UnitID) []byte {
	return NewEntityInteract(EntityTypeNPC, npcGID)
}

// NewObjectInteract creates an object interact packet (stash, WP, chests, shrines).
// Shorthand for NewEntityInteract(EntityTypeObject, objectGID).
func NewObjectInteract(objectGID data.UnitID) []byte {
	return NewEntityInteract(EntityTypeObject, objectGID)
}

// NewNPCEntityAction creates the PreInteract packet (0x4D, 29 bytes).
//
// AUTHORITATIVE live capture 02_npc_chat_clean.json buf=0 entry#0 tick=40781765:
//
//	4d 0e000000 d6000000 0000 04 01000000 0e000000 b411 0112 01 b511 fb11
//
// Layout:
//
//	[4D]
//	[npcGID:u32]       bytes 1-4   — target NPC
//	[playerGID:u32]    bytes 5-8   — initiating player
//	[0x0000:u16]       bytes 9-10  — padding, observed zero
//	[0x04:u8]          byte  11    — const flag (interaction class?)
//	[0x00000001:u32]   bytes 12-15 — const, observed 1
//	[npcGID:u32]       bytes 16-19 — repeated npcGID (entity type tag?)
//	[playerX:u16]      bytes 20-21 — player position x
//	[playerY:u16]      bytes 22-23 — player position y
//	[0x01:u8]          byte  24    — const unit-type (=1 PlayerUnit)
//	[npcX:u16]         bytes 25-26 — npc position x
//	[npcY:u16]         bytes 27-28 — npc position y
//
// Prior 5B truncated form (per "Discord plaintext 2026-04-15") is contradicted
// by buf=1 mirror capture showing full 29B context — server silently dropped
// 5B and bot was forced into HID fallback. CAPTURE_AUDIT_2026_04_19 revert.
func NewNPCEntityAction(npcGID, playerGID data.UnitID, playerX, playerY, npcX, npcY uint16) []byte {
	buf := make([]byte, 29)
	buf[0] = OpEntityActionResult // 0x4D
	binary.LittleEndian.PutUint32(buf[1:5], uint32(npcGID))
	binary.LittleEndian.PutUint32(buf[5:9], uint32(playerGID))
	// buf[9:11] zero padding u16
	buf[11] = 0x04
	binary.LittleEndian.PutUint32(buf[12:16], 0x00000001)
	binary.LittleEndian.PutUint32(buf[16:20], uint32(npcGID))
	binary.LittleEndian.PutUint16(buf[20:22], playerX)
	binary.LittleEndian.PutUint16(buf[22:24], playerY)
	buf[24] = 0x01
	binary.LittleEndian.PutUint16(buf[25:27], npcX)
	binary.LittleEndian.PutUint16(buf[27:29], npcY)
	return buf
}
