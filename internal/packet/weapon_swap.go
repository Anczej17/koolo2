package packet

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// NewWeaponSwap builds packet 0x50 (SwapWeaponSlots) per AMB's authoritative
// layout. 30 bytes, fixed.
//
//	[0]       0x50
//	[1..5]    slot-0 left-hand GID  (primary main-hand)
//	[5..9]    slot-0 right-hand GID (primary off-hand)
//	[9..13]   slot-1 left-hand GID  (alt main-hand)
//	[13..17]  slot-1 right-hand GID (alt off-hand)
//	[17..25]  zeros (u64 — AMB calls them Unknown1/Unknown2)
//	[25..27]  currently-active LeftSkillId (u16 LE) from PlayerUnit.LeftSkill
//	[27..29]  currently-active RightSkillId (u16 LE) from PlayerUnit.RightSkill
//	[29]      TARGET slot id — the slot to swap TO (active_slot ^ 1)
//
// Supersedes 2026-04-21 builder that wrote FF*8 at [17..25] and a hand-picked
// "anim byte 0x37" at [25]. Per live sniff + AMB docs, [17..25] is eight
// zeroes and [25..29] encodes the player's currently-selected skills + the
// destination slot — never a constant anim. The earlier 0x37 almost certainly
// worked-sometimes only because 0x0037 happens to be a valid skill id the
// server tolerated, not because the server parsed it as an animation byte.
//
// Slot ordering note: regardless of which slot is currently active, the
// builder expects slot-0 weapons at [1..9] and slot-1 weapons at [9..17].
// The server tracks the two sets by physical slot index, not by "source" /
// "destination" role — callers pass fixed slots, builder passes the TARGET
// slot at [29] to tell the server which set becomes active.
func NewWeaponSwap(
	slot0Left, slot0Right, slot1Left, slot1Right data.UnitID,
	leftSkillID, rightSkillID uint16,
	targetSlot byte,
) []byte {
	buf := make([]byte, 30)
	buf[0] = 0x50
	binary.LittleEndian.PutUint32(buf[1:5], uint32(slot0Left))
	binary.LittleEndian.PutUint32(buf[5:9], uint32(slot0Right))
	binary.LittleEndian.PutUint32(buf[9:13], uint32(slot1Left))
	binary.LittleEndian.PutUint32(buf[13:17], uint32(slot1Right))
	// [17..25] stay zero (make default).
	binary.LittleEndian.PutUint16(buf[25:27], leftSkillID)
	binary.LittleEndian.PutUint16(buf[27:29], rightSkillID)
	buf[29] = targetSlot
	return buf
}
