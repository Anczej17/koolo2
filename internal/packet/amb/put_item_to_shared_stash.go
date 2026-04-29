package amb

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// PutItemToSharedStash represents packet 0x55 (D2GS_INV_MOVE_ITEM_STASH)
// Size: 29 bytes (NO PADDING - exact size)
// Purpose: Move items between shared stash tabs (tabs 2-4) and inventory
//
// Different from packet 0x54 which is for personal stash/cube/inventory
//
// StashTabID field (CRITICAL):
//
//	This is the PHANTOM UNIT ID from the shared stash linked list structure:
//	- Retrieved from data.Inventory.StashTabUnitIDs array
//	- [0] = PlayerUnit.ID (used for personal stash, aka tab 1)
//	- [1-N] = Phantom unit IDs for shared stash tabs 2..N
//
// IsStashPage field (DIRECTION CONTROL):
//
//	0 = Moving FROM inventory TO shared stash (FromInvPage=0, ToInvPage=4)
//	1 = Moving FROM shared stash TO inventory (FromInvPage=4, ToInvPage=0)
//
// Container Types:
//
//	0x55 uses the normal stash page value (4) for shared stash too; StashTabID
//	selects the shared tab owner. Live capture on 2026-04-26 confirmed page 5
//	is ignored for this packet.
//
// 🚨 CRITICAL: ToPosX/ToPosY MUST be calculated using getFreeSlotForItem!
// Setting to 0,0 does NOT auto-place - it tries to place at position 0,0!
type PutItemToSharedStash struct {
	PacketID    byte
	ItemUnitId  uint32
	StashTabID  uint32 // Phantom unit ID from StashTabUnitIDs array
	FromInvPage uint32 // Source inventory page (0=inventory, 4=stash)
	FromPosX    uint16 // X position in source
	FromPosY    uint16 // Y position in source
	ToInvPage   uint32 // Destination page (0=inventory, 4=stash)
	ToPosX      uint16 // X position in destination (MUST calculate free slot!)
	ToPosY      uint16 // Y position in destination (MUST calculate free slot!)
	IsStashPage uint32 // Boolean: 1 if FromInvPage==4 (stash), 0 otherwise
}

// NewPutItemToSharedStash creates packet 0x55 for moving items TO shared stash FROM inventory
// IsStashPage = 0 (moving FROM inventory TO shared stash)
func NewPutItemToSharedStash(item data.Item, stashTabID uint32, toPosX, toPosY uint16) *PutItemToSharedStash {
	return &PutItemToSharedStash{
		PacketID:    0x55,
		ItemUnitId:  uint32(item.UnitID),
		StashTabID:  stashTabID,
		FromInvPage: uint32(ContainerInventory), // 0 = Inventory
		FromPosX:    uint16(item.Position.X),
		FromPosY:    uint16(item.Position.Y),
		ToInvPage:   uint32(ContainerStash), // 4 = stash page; StashTabID selects shared tab
		ToPosX:      toPosX,
		ToPosY:      toPosY,
		IsStashPage: 0, // 0 = FROM inventory TO shared stash
	}
}

// NewTakeItemFromSharedStash creates packet 0x55 for moving items FROM shared stash TO inventory
// IsStashPage = 1 (moving FROM shared stash TO inventory)
func NewTakeItemFromSharedStash(item data.Item, stashTabID uint32, toPosX, toPosY uint16) *PutItemToSharedStash {
	return &PutItemToSharedStash{
		PacketID:    0x55,
		ItemUnitId:  uint32(item.UnitID),
		StashTabID:  stashTabID,
		FromInvPage: uint32(ContainerStash), // 4 = stash page; StashTabID selects shared tab
		FromPosX:    uint16(item.Position.X),
		FromPosY:    uint16(item.Position.Y),
		ToInvPage:   uint32(ContainerInventory), // 0 = Inventory
		ToPosX:      toPosX,
		ToPosY:      toPosY,
		IsStashPage: 1, // 1 = FROM shared stash TO inventory
	}
}

// GetPayload converts the PutItemToSharedStash struct to bytes (29 bytes)
// Per D2GS_INV_MOVE_ITEM_STASH packet structure:
// [0]      PacketID (1 byte) = 0x55
// [1-4]    ItemUnitId (4 bytes)
// [5-8]    StashTabID (4 bytes) - Phantom unit ID from StashTabUnitIDs array
// [9-12]   FromInvPage (4 bytes) - Source inventory page (0=inventory, 4=shared stash via StashTabID)
// [13-14]  FromPosX (2 bytes)
// [15-16]  FromPosY (2 bytes)
// [17-20]  ToInvPage (4 bytes) - Destination inventory page (0=inventory, 4=shared stash via StashTabID)
// [21-22]  ToPosX (2 bytes)
// [23-24]  ToPosY (2 bytes)
// [25-28]  IsStashPage (4 bytes) - Boolean: 1 if FromInvPage==5 (shared stash), 0 otherwise
func (p *PutItemToSharedStash) GetPayload() []byte {
	buf := make([]byte, 29)
	buf[0] = p.PacketID
	binary.LittleEndian.PutUint32(buf[1:5], p.ItemUnitId)
	binary.LittleEndian.PutUint32(buf[5:9], p.StashTabID)
	binary.LittleEndian.PutUint32(buf[9:13], p.FromInvPage)
	binary.LittleEndian.PutUint16(buf[13:15], p.FromPosX)
	binary.LittleEndian.PutUint16(buf[15:17], p.FromPosY)
	binary.LittleEndian.PutUint32(buf[17:21], p.ToInvPage)
	binary.LittleEndian.PutUint16(buf[21:23], p.ToPosX)
	binary.LittleEndian.PutUint16(buf[23:25], p.ToPosY)
	binary.LittleEndian.PutUint32(buf[25:29], p.IsStashPage)
	return buf
}
