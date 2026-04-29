package amb

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// QuickItemMove represents packet 0x54 (QuickItemMove)
// Size: 21 bytes (NO PADDING - exact size)
// Purpose: Move item between inventory locations (inventory, stash, cube, belt)
//
// Inventory Pages:
//
//	0 = Inventory (INVPAGE_INVENTORY)
//	1 = Equip (INVPAGE_EQUIP)
//	2 = Trade (INVPAGE_TRADE)
//	3 = Cube (INVPAGE_CUBE)
//	4 = Stash (INVPAGE_STASH)
//	5 = Belt (INVPAGE_BELT)
//
// 🚨 CRITICAL: ToPosX/ToPosY MUST be calculated using getFreeSlotForItem!
// Setting to 0,0 does NOT auto-place - it tries to place at position 0,0!
// If that slot is occupied, the packet will fail silently.
type QuickItemMove struct {
	PacketID    byte
	ItemUnitId  uint32
	FromInvPage uint32
	FromPosX    uint16
	FromPosY    uint16
	ToInvPage   uint32
	ToPosX      uint16 // MUST calculate free slot - NOT auto-placement!
	ToPosY      uint16 // MUST calculate free slot - NOT auto-placement!
}

// NewQuickItemMove creates packet 0x54 (QuickItemMove)
// 🚨 WARNING: This uses 0,0 for destination which only works if that slot is free!
// For reliable item movement, use NewQuickItemMoveWithPosition instead.
func NewQuickItemMove(item data.Item, fromPage, toPage ContainerType) *QuickItemMove {
	return &QuickItemMove{
		PacketID:    0x54,
		ItemUnitId:  uint32(item.UnitID),
		FromInvPage: uint32(fromPage),
		FromPosX:    uint16(item.Position.X),
		FromPosY:    uint16(item.Position.Y),
		ToInvPage:   uint32(toPage),
		ToPosX:      0, // ⚠️ Only works if slot 0,0 is free!
		ToPosY:      0, // ⚠️ Only works if slot 0,0 is free!
	}
}

// NewQuickItemMoveWithPosition creates packet 0x54 with explicit destination position
// Use this with a calculated free slot position for reliable item movement
func NewQuickItemMoveWithPosition(item data.Item, fromPage, toPage ContainerType, toPosX, toPosY uint16) *QuickItemMove {
	return &QuickItemMove{
		PacketID:    0x54,
		ItemUnitId:  uint32(item.UnitID),
		FromInvPage: uint32(fromPage),
		FromPosX:    uint16(item.Position.X),
		FromPosY:    uint16(item.Position.Y),
		ToInvPage:   uint32(toPage),
		ToPosX:      toPosX,
		ToPosY:      toPosY,
	}
}

// GetPayload converts the QuickItemMove struct to bytes
func (p *QuickItemMove) GetPayload() []byte {
	buf := make([]byte, 21)
	buf[0] = byte(p.PacketID)
	binary.LittleEndian.PutUint32(buf[1:5], p.ItemUnitId)
	binary.LittleEndian.PutUint32(buf[5:9], p.FromInvPage)
	binary.LittleEndian.PutUint16(buf[9:11], p.FromPosX)
	binary.LittleEndian.PutUint16(buf[11:13], p.FromPosY)
	binary.LittleEndian.PutUint32(buf[13:17], p.ToInvPage)
	binary.LittleEndian.PutUint16(buf[17:19], p.ToPosX)
	binary.LittleEndian.PutUint16(buf[19:21], p.ToPosY)
	return buf
}
