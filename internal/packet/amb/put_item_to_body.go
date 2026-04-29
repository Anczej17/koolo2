package amb

import (
	"encoding/binary"
)

// PutItemToBody represents packet 0x1A (ItemToBody)
// Equips an item from cursor to equipment slot
// Item must be on cursor first (use packet 0x19 to pick up)
type PutItemToBody struct {
	PacketID     byte
	ItemUnitID   uint32
	BodyLocation uint32 // Equipment slot (see EquipmentSlot constants)
}

// Equipment slot constants for PutItemToBody
const (
	EquipSlotHelm         = 1
	EquipSlotAmulet       = 2
	EquipSlotArmor        = 3
	EquipSlotWeaponRight  = 4
	EquipSlotWeaponLeft   = 5
	EquipSlotRingRight    = 6
	EquipSlotRingLeft     = 7
	EquipSlotBelt         = 8
	EquipSlotBoots        = 9
	EquipSlotGloves       = 10
	EquipSlotWeaponRight2 = 11 // Weapon swap
	EquipSlotWeaponLeft2  = 12 // Weapon swap (shield/offhand)
	// Mercenary equipment slot constants
	MERC_HELM   = 13 // Mercenary helmet
	MERC_ARMOR  = 14 // Mercenary body/armor
	MERC_WEAPON = 15 // Mercenary weapon
)

// NewPutItemToBody creates packet 0x1A to equip item from cursor
// Item must already be on cursor (picked up with packet 0x19)
// If slot is occupied, previous item swaps to cursor
func NewPutItemToBody(itemUnitID uint32, bodyLocation uint32) *PutItemToBody {
	return &PutItemToBody{
		PacketID:     0x1A,
		ItemUnitID:   itemUnitID,
		BodyLocation: bodyLocation,
	}
}

// GetPayload converts the PutItemToBody struct to bytes (9 bytes)
func (p *PutItemToBody) GetPayload() []byte {
	buf := make([]byte, 9)
	buf[0] = p.PacketID
	binary.LittleEndian.PutUint32(buf[1:5], p.ItemUnitID)
	binary.LittleEndian.PutUint32(buf[5:9], p.BodyLocation)
	return buf
}
