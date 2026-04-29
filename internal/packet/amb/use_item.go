package amb

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/item"
)

// UseItem represents packet 0x18 (UseItem)
// Variable size: 8 + (4 * number of additional items)
// Purpose: Use item (potions, scrolls, cube transmute, etc.)
//
// Action Types:
//   - 32 (0x20) = Normal use (potions, scrolls, keys, tomes)
//   - 136 (0x88) = Cube transmute
//
// Inventory Pages (ItemInvPage):
//   - 0 = Inventory (INVPAGE_INVENTORY)
//   - 1 = Equip (INVPAGE_EQUIP)
//   - 2 = Trade (INVPAGE_TRADE)
//   - 3 = Cube (INVPAGE_CUBE)
//   - 4 = Stash (INVPAGE_STASH)
//   - 5 = Belt (INVPAGE_BELT)
//
// For cube transmute, ItemCount = number of items in cube - 1
// ItemIds contains the UnitIDs of all items involved in the recipe
type UseItem struct {
	PacketID    byte
	ItemUnitId  uint32   // Primary item being used (cube for transmute)
	ItemInvPage byte     // Inventory page the item is in
	ActionType  byte     // 32=normal use, 136=cube transmute
	ItemCount   byte     // Number of additional items - 1 (for cube recipes)
	ItemIds     []uint32 // Additional item IDs (for cube recipes)
}

// ActionType constants
const (
	UseItemActionNormal    byte = 32  // 0x20 - Normal use (potion, scroll, key)
	UseItemActionTransmute byte = 136 // 0x88 - Cube transmute
)

// NewUseItem creates packet 0x18 for using a single item (potion, scroll, key)
func NewUseItem(item data.Item, invPage ContainerType) *UseItem {
	return &UseItem{
		PacketID:    0x18,
		ItemUnitId:  uint32(item.UnitID),
		ItemInvPage: byte(invPage),
		ActionType:  UseItemActionNormal,
		ItemCount:   0,
		ItemIds:     nil,
	}
}

// NewUseItemFromBelt creates packet 0x18 for using a belt item (potion)
func NewUseItemFromBelt(item data.Item) *UseItem {
	return &UseItem{
		PacketID:    0x18,
		ItemUnitId:  uint32(item.UnitID),
		ItemInvPage: byte(ContainerBelt), // 5 = Belt
		ActionType:  UseItemActionNormal,
		ItemCount:   0,
		ItemIds:     nil,
	}
}

// GetPayload converts the UseItem struct to bytes (variable size)
// Size: 8 bytes base + 4 bytes per additional item
func (p *UseItem) GetPayload() []byte {
	size := 8 + (len(p.ItemIds) * 4)
	buf := make([]byte, size)

	buf[0] = p.PacketID
	binary.LittleEndian.PutUint32(buf[1:5], p.ItemUnitId)
	buf[5] = p.ItemInvPage
	buf[6] = p.ActionType
	buf[7] = p.ItemCount

	offset := 8
	for _, itemId := range p.ItemIds {
		binary.LittleEndian.PutUint32(buf[offset:offset+4], itemId)
		offset += 4
	}

	return buf
}

// CubeTransmute represents the live D2R transmute packet captured 2026-04-26.
// It is not packet 0x18; 0x18 remains the cursor/container item-move path.
//
// Layout:
//
//	[0x20][cubeGID:u32][cubeInvPage:u8][0x88][itemCountMinusOne:u8]
//		repeated [itemGID:u32][packedCubePos:u8]
//
// packedCubePos stores x in the low nibble and y in the high nibble.
type CubeTransmute struct {
	PacketID    byte
	CubeUnitID  uint32
	CubeInvPage byte
	ActionType  byte
	ItemCount   byte
	Items       []CubeTransmuteItem
}

type CubeTransmuteItem struct {
	ItemUnitID uint32
	PackedPos  byte
}

// NewUseCubeTransmute creates packet 0x20 for cube transmutation.
// The function name is kept so existing call sites use the corrected builder.
func NewUseCubeTransmute(cubeItem data.Item, itemsInCube []data.Item) *CubeTransmute {
	items := make([]CubeTransmuteItem, 0, len(itemsInCube))
	for _, cubeItem := range itemsInCube {
		items = append(items, CubeTransmuteItem{
			ItemUnitID: uint32(cubeItem.UnitID),
			PackedPos:  byte(((cubeItem.Position.Y & 0x0F) << 4) | (cubeItem.Position.X & 0x0F)),
		})
	}

	itemCount := byte(0)
	if len(itemsInCube) > 0 {
		itemCount = byte(len(itemsInCube) - 1)
	}

	return &CubeTransmute{
		PacketID:    0x20,
		CubeUnitID:  uint32(cubeItem.UnitID),
		CubeInvPage: cubeInventoryPage(cubeItem),
		ActionType:  UseItemActionTransmute,
		ItemCount:   itemCount,
		Items:       items,
	}
}

func cubeInventoryPage(cubeItem data.Item) byte {
	switch cubeItem.Location.LocationType {
	case item.LocationInventory:
		return byte(ContainerInventory)
	case item.LocationCube:
		return byte(ContainerCube)
	case item.LocationStash, item.LocationSharedStash, item.LocationGemsTab, item.LocationMaterialsTab, item.LocationRunesTab:
		return byte(ContainerStash)
	default:
		return byte(ContainerInventory)
	}
}

func (p *CubeTransmute) GetPayload() []byte {
	buf := make([]byte, 8+(len(p.Items)*5))
	buf[0] = p.PacketID
	binary.LittleEndian.PutUint32(buf[1:5], p.CubeUnitID)
	buf[5] = p.CubeInvPage
	buf[6] = p.ActionType
	buf[7] = p.ItemCount

	offset := 8
	for _, itm := range p.Items {
		binary.LittleEndian.PutUint32(buf[offset:offset+4], itm.ItemUnitID)
		buf[offset+4] = itm.PackedPos
		offset += 5
	}

	return buf
}
