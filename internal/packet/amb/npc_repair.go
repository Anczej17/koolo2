package amb

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// NPCRepair represents packet 0x35 (NPCRepair)
// Requests NPC to repair item(s)
// Mode: 0=repair single inv, 1=repair single equip, 4=repair all
type NPCRepair struct {
	PacketID    byte
	Mode        byte // 0=single inv, 1=single equip, 4=repair all
	ItemPosX    byte
	ItemPosY    byte
	NPCUnitID   uint32
	RepairCosts uint32
	ItemUnitId  uint32
}

// NewNPCRepair creates packet 0x35 for repairing single item
func NewNPCRepair(mode byte, itemPosX, itemPosY byte, npcUnitID, repairCosts, itemUnitId uint32) *NPCRepair {
	return &NPCRepair{
		PacketID:    0x35,
		Mode:        mode,
		ItemPosX:    itemPosX,
		ItemPosY:    itemPosY,
		NPCUnitID:   npcUnitID,
		RepairCosts: repairCosts,
		ItemUnitId:  itemUnitId,
	}
}

// NewNPCRepairAll creates packet 0x35 for repairing all items
// Uses Mode=4 with ItemUnitId=-1 (0xFFFFFFFF) as per protocol
func NewNPCRepairAll(npcUnitID data.UnitID, repairCosts uint32) *NPCRepair {
	return &NPCRepair{
		PacketID:    0x35,
		Mode:        0x04, // Repair all mode
		ItemPosX:    0,
		ItemPosY:    0,
		NPCUnitID:   uint32(npcUnitID),
		RepairCosts: repairCosts,
		ItemUnitId:  0xFFFFFFFF, // -1 for repair all
	}
}

// GetPayload converts the NPCRepair struct to bytes (16 bytes)
func (p *NPCRepair) GetPayload() []byte {
	buf := make([]byte, 16)
	buf[0] = p.PacketID
	buf[1] = p.Mode
	buf[2] = p.ItemPosX
	buf[3] = p.ItemPosY
	binary.LittleEndian.PutUint32(buf[4:8], p.NPCUnitID)
	binary.LittleEndian.PutUint32(buf[8:12], p.RepairCosts)
	binary.LittleEndian.PutUint32(buf[12:16], p.ItemUnitId)
	return buf
}
