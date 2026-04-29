package amb

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// SwapWeaponSlots represents packet 0x50 (SwapWeaponSlots)
// Swaps between weapon sets (W key)
type SwapWeaponSlots struct {
	PacketID           byte
	LeftHandUnitId     uint32
	RightHandUnitId    uint32
	AltLeftHandUnitId  uint32
	AltRightHandUnitId uint32
	Unknown1           uint32
	Unknown2           uint32
	LeftSkillId        uint16
	RightSkillId       uint16
	SwapSlotId         byte
}

// NewSwapWeaponSlots creates packet 0x50 for swapping weapon sets
func NewSwapWeaponSlots(leftHandUnitId, rightHandUnitId, altLeftHandUnitId, altRightHandUnitId data.UnitID, leftSkillId, rightSkillId uint16, swapSlotId byte) *SwapWeaponSlots {
	return &SwapWeaponSlots{
		PacketID:           0x50,
		LeftHandUnitId:     uint32(leftHandUnitId),
		RightHandUnitId:    uint32(rightHandUnitId),
		AltLeftHandUnitId:  uint32(altLeftHandUnitId),
		AltRightHandUnitId: uint32(altRightHandUnitId),
		Unknown1:           0x00000000,
		Unknown2:           0x00000000,
		LeftSkillId:        leftSkillId,
		RightSkillId:       rightSkillId,
		SwapSlotId:         swapSlotId,
	}
}

// GetPayload converts the SwapWeaponSlots struct to bytes (30 bytes)
func (p *SwapWeaponSlots) GetPayload() []byte {
	buf := make([]byte, 30)
	buf[0] = p.PacketID
	binary.LittleEndian.PutUint32(buf[1:5], p.LeftHandUnitId)
	binary.LittleEndian.PutUint32(buf[5:9], p.RightHandUnitId)
	binary.LittleEndian.PutUint32(buf[9:13], p.AltLeftHandUnitId)
	binary.LittleEndian.PutUint32(buf[13:17], p.AltRightHandUnitId)
	binary.LittleEndian.PutUint32(buf[17:21], p.Unknown1)
	binary.LittleEndian.PutUint32(buf[21:25], p.Unknown2)
	binary.LittleEndian.PutUint16(buf[25:27], p.LeftSkillId)
	binary.LittleEndian.PutUint16(buf[27:29], p.RightSkillId)
	buf[29] = p.SwapSlotId
	return buf
}
