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

// NewNPCEntityAction creates the PreInteract packet (0x4D, 5 bytes).
//
// AUTHORITATIVE plaintext capture 2026-04-15 from Discord colleague:
//
//	4D [npcGID:u32 LE]
//
// Example (Akara NPC GID=8): `4d 08 00 00 00`
//
// Prior 24-byte layout was read from buf0 UI NetMan outgoing buffer which
// included adjacent buffer metadata (playerGID, flags, pos). The actual
// on-wire payload is just 5 bytes — D2R fills the rest in-transit from
// the UI context. Sending the 24-byte version included the right first
// 5 bytes but confused the server reply.
//
// Parameters playerGID, npcX, npcY retained for API stability — ignored.
func NewNPCEntityAction(npcGID, playerGID data.UnitID, npcX, npcY uint16) []byte {
	_ = playerGID
	_ = npcX
	_ = npcY
	buf := make([]byte, 5)
	buf[0] = OpEntityActionResult // 0x4D
	binary.LittleEndian.PutUint32(buf[1:5], uint32(npcGID))
	return buf
}
