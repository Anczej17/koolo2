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
// The 29B form attempted 2026-04-20 per CAPTURE_AUDIT_2026_04_19 was
// derived from a buf=0 capture, which carries 233-byte residue from prior
// packets — the actual single-packet payload cannot be read reliably from
// buf=0. Live test 14:29:58 CONFIRMED the 29B form CRASHES D2R
// (exitCode=0xc0000005) on first send via SendUIPacketViaMainThread,
// because D2R's 0x4D handler validates length == 5 and buffer-overflows
// on 29B input.
//
// Reverted to 5B [4D][npcGID:u32]. Server silently drops this form (no
// dialog opens), so callers must HID-fallback when dialog doesn't appear
// after ~1.7s — step/interact_npc.go already does this.
//
// Additional signature params retained for API stability; they will be
// used if a fresh buf=1 mirror capture in Phase 5b confirms a longer form.
func NewNPCEntityAction(npcGID, playerGID data.UnitID, playerX, playerY, npcX, npcY uint16) []byte {
	_ = playerGID
	_ = playerX
	_ = playerY
	_ = npcX
	_ = npcY
	buf := make([]byte, 5)
	buf[0] = OpEntityActionResult // 0x4D
	binary.LittleEndian.PutUint32(buf[1:5], uint32(npcGID))
	return buf
}
