package amb

import (
	"encoding/binary"
)

// UseItemOnUnit uses an item (like a potion) from belt on self or mercenary
// Packet 0x26, size 36 bytes
type UseItemOnUnit struct {
	ItemUnitID     uint32
	TargetUnitID   uint32
	TargetUnitType byte // 0 = player, 1 = monster (merc)
	BeltPosX       byte
	BeltPosY       byte
	UsageTarget    byte // 2 = self, 4 = mercenary
	OtherBeltSlot  [3]BeltSlotState
}

type BeltSlotState struct {
	ItemUnitID   uint32
	PosXCurrent  byte
	PosXPrevious byte
}

func NewUseItemOnUnit(itemID uint32, targetID uint32, targetUnitType byte, beltX, beltY byte, usageTarget byte, otherSlots [3]BeltSlotState) *UseItemOnUnit {
	return &UseItemOnUnit{
		ItemUnitID:     itemID,
		TargetUnitID:   targetID,
		TargetUnitType: targetUnitType,
		BeltPosX:       beltX,
		BeltPosY:       beltY,
		UsageTarget:    usageTarget,
		OtherBeltSlot:  otherSlots,
	}
}

func (p *UseItemOnUnit) GetPayload() []byte {
	payload := make([]byte, 36)

	// Opcode
	payload[0] = 0x26

	// [Offset 1] Fix01 = 1
	payload[1] = 1
	// [Offset 2-5] UnitID (target - self or merc)
	binary.LittleEndian.PutUint32(payload[2:6], p.TargetUnitID)
	// [Offset 6] TargetUnitType (0 = player, 1 = monster/merc)
	payload[6] = p.TargetUnitType
	// [Offset 7] FixFF = 0xFF
	payload[7] = 0xFF
	// [Offset 8-9] Fix00 = 0x00 0x00
	payload[8] = 0
	payload[9] = 0

	// [Offset 10-13] ItemUnitID (the potion being used)
	binary.LittleEndian.PutUint32(payload[10:14], p.ItemUnitID)
	// [Offset 14] MercOrSelfUse (2=self, 4=merc) - but capture shows 2 for self
	payload[14] = p.UsageTarget
	// [Offset 15] FixFF = 0xFF
	payload[15] = 0xFF
	// [Offset 16] ItemPosX (belt column)
	payload[16] = p.BeltPosX
	// [Offset 17] ItemPosY (belt row)
	payload[17] = p.BeltPosY

	// [Offset 18-23] Second belt item (or 0xFFFFFFFF if unused)
	binary.LittleEndian.PutUint32(payload[18:22], p.OtherBeltSlot[0].ItemUnitID)
	payload[22] = p.OtherBeltSlot[0].PosXCurrent
	payload[23] = p.OtherBeltSlot[0].PosXPrevious

	// [Offset 24-29] Third belt item (or 0xFFFFFFFF if unused)
	binary.LittleEndian.PutUint32(payload[24:28], p.OtherBeltSlot[1].ItemUnitID)
	payload[28] = p.OtherBeltSlot[1].PosXCurrent
	payload[29] = p.OtherBeltSlot[1].PosXPrevious

	// [Offset 30-35] Fourth belt item (or 0xFFFFFFFF if unused)
	binary.LittleEndian.PutUint32(payload[30:34], p.OtherBeltSlot[2].ItemUnitID)
	payload[34] = p.OtherBeltSlot[2].PosXCurrent
	payload[35] = p.OtherBeltSlot[2].PosXPrevious

	return payload
}
