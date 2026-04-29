package amb

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// MoveStashGold represents packet 0x27 (MoveStashGold)
// Transfers gold between inventory and stash (both personal and shared)
//
// UnitID field:
//
//	This is the unit ID from data.Inventory.StashTabUnitIDs:
//	- [0] = PlayerUnit.ID (for personal stash, aka tab 1)
//	- [1-3] = Phantom unit IDs for shared stash tabs 2-4
//
// TransferAmount:
//   - Negative = Deposit to stash (e.g., -5000 = deposit 5000 gold)
//   - Positive = Withdraw from stash (e.g., 5000 = withdraw 5000 gold)
//
// Important: Stash must be open before sending this packet
type MoveStashGold struct {
	PacketID       byte
	UnitID         uint32 // Unit ID from StashTabUnitIDs: [0]=PlayerID, [1-3]=SharedTab IDs
	StashGold      uint32 // Current gold in stash
	InvGold        uint32 // Current gold in inventory
	TransferAmount int32  // Amount to move (negative = deposit, positive = withdraw)
}

// NewMoveStashGold creates packet 0x27 for moving gold to/from stash
// Use negative transferAmount to deposit, positive to withdraw
func NewMoveStashGold(unitID data.UnitID, stashGold, invGold uint32, transferAmount int32) *MoveStashGold {
	return &MoveStashGold{
		PacketID:       0x27,
		UnitID:         uint32(unitID),
		StashGold:      stashGold,
		InvGold:        invGold,
		TransferAmount: transferAmount,
	}
}

// NewDepositGoldToStash creates packet 0x27 for depositing gold to stash
// amount should be a positive value representing how much gold to deposit
func NewDepositGoldToStash(unitID data.UnitID, stashGold, invGold, amount uint32) *MoveStashGold {
	return &MoveStashGold{
		PacketID:       0x27,
		UnitID:         uint32(unitID),
		StashGold:      stashGold,
		InvGold:        invGold,
		TransferAmount: -int32(amount), // Negative = deposit
	}
}

// NewWithdrawGoldFromStash creates packet 0x27 for withdrawing gold from stash
// amount should be a positive value representing how much gold to withdraw
func NewWithdrawGoldFromStash(unitID data.UnitID, stashGold, invGold, amount uint32) *MoveStashGold {
	return &MoveStashGold{
		PacketID:       0x27,
		UnitID:         uint32(unitID),
		StashGold:      stashGold,
		InvGold:        invGold,
		TransferAmount: int32(amount), // Positive = withdraw
	}
}

// GetPayload converts the MoveStashGold struct to bytes (17 bytes)
func (p *MoveStashGold) GetPayload() []byte {
	buf := make([]byte, 17)
	buf[0] = p.PacketID
	binary.LittleEndian.PutUint32(buf[1:5], p.UnitID)
	binary.LittleEndian.PutUint32(buf[5:9], p.StashGold)
	binary.LittleEndian.PutUint32(buf[9:13], p.InvGold)
	binary.LittleEndian.PutUint32(buf[13:17], uint32(p.TransferAmount))
	return buf
}
