package packet

import "encoding/binary"

// MoveToEntity represents packet 0x04 — the client's intent to walk/run
// toward a specific entity (NPC, object, monster).
//
// Format from logs/live_captures_2026_04_15 (18 B fixed layout):
//   [0x04][action:u32][gid:u32][srcUnitType:u8][srcGidLow16:u16][srcGidHigh16:u16]
//   [dstUnitType:u8][dstGidLow16:u16][dstGidHigh16:u16]
//
// Action (u32 LE):
//   0x00000001 = walk to entity (default UI click)
//   0x00000002 = run to entity (shift-click)
//
// Captures showed 0x04 always immediately precedes 0x2F/0x41 (NPC chat /
// object interact), so this builder is the prerequisite step for all
// entity-based interactions. Previously the bot fell back to HID mouse
// click which left a measurable input signal.
type MoveToEntity struct {
	Action      uint32
	TargetGID   uint32
	SrcUnitType byte
	SrcGIDLow   uint16
	SrcGIDHigh  uint16
	DstUnitType byte
	DstGIDLow   uint16
	DstGIDHigh  uint16
}

const (
	MoveToEntityActionWalk uint32 = 1
	MoveToEntityActionRun  uint32 = 2
)

// NewMoveToEntity builds a 0x04 packet. `targetGID` is the entity's GID
// (network ID), not the local unit ID. `unitType` is koolo's UnitType
// byte: 0=player, 1=NPC/monster, 2=object, 4=item.
func NewMoveToEntity(action uint32, targetGID uint32, unitType byte) *MoveToEntity {
	// Split GID into two u16 for the src+dst fields. Live captures showed
	// both src and dst carry the same split of the target GID — not a
	// source/destination pair as the name might suggest.
	low := uint16(targetGID & 0xFFFF)
	high := uint16(targetGID >> 16)
	return &MoveToEntity{
		Action:      action,
		TargetGID:   targetGID,
		SrcUnitType: unitType,
		SrcGIDLow:   low,
		SrcGIDHigh:  high,
		DstUnitType: unitType,
		DstGIDLow:   low,
		DstGIDHigh:  high,
	}
}

func (p *MoveToEntity) GetPayload() []byte {
	buf := make([]byte, 18)
	buf[0] = 0x04
	binary.LittleEndian.PutUint32(buf[1:5], p.Action)
	binary.LittleEndian.PutUint32(buf[5:9], p.TargetGID)
	buf[9] = p.SrcUnitType
	binary.LittleEndian.PutUint16(buf[10:12], p.SrcGIDLow)
	binary.LittleEndian.PutUint16(buf[12:14], p.SrcGIDHigh)
	buf[14] = p.DstUnitType
	binary.LittleEndian.PutUint16(buf[15:17], p.DstGIDLow)
	// buf[17] is dstGidHigh high byte — set to high >> 8 to match live captures
	buf[17] = byte(p.DstGIDHigh >> 8)
	return buf
}
