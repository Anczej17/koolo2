package packet

import "encoding/binary"

// GoldTransfer — packet 0x27. Client → server when moving gold between the
// player's inventory and stash/cube. Observed in logs/live_captures_2026_04_15
// during stash-gold automation sessions.
//
// Format (17 B):
//   [0x27][action:u32][balance:u32][amount:u32][trailer:u32]
//
// - action (u32 LE): 0x00000001 = deposit (inv→stash), 0x00000002 = withdraw (stash→inv).
// - balance: sender's resulting inventory gold AFTER the transfer
//   (server double-checks against its own view; mismatches lead to
//    immediate disconnect, so compute from Data.PlayerUnit).
// - amount: the number of gold pieces being moved, always positive.
// - trailer: live capture showed 0x5B44FFFF each time; likely a session
//   nonce or unit GID suffix. Reproducing the exact bytes is what
//   passed the server's sanity check.
type GoldTransfer struct {
	Action  uint32
	Balance uint32
	Amount  uint32
	Trailer uint32
}

const (
	GoldTransferDeposit  uint32 = 1
	GoldTransferWithdraw uint32 = 2
)

// NewGoldTransfer composes the 17 B payload. Trailer defaults to the
// live-captured 0x5B44FFFF — if the server rejects with disconnect after
// rollout, swap to a computed value derived from PlayerUnit GID.
func NewGoldTransfer(action, balance, amount uint32) *GoldTransfer {
	return &GoldTransfer{
		Action:  action,
		Balance: balance,
		Amount:  amount,
		Trailer: 0x5B44FFFF,
	}
}

func (p *GoldTransfer) GetPayload() []byte {
	buf := make([]byte, 17)
	buf[0] = 0x27
	binary.LittleEndian.PutUint32(buf[1:5], p.Action)
	binary.LittleEndian.PutUint32(buf[5:9], p.Balance)
	binary.LittleEndian.PutUint32(buf[9:13], p.Amount)
	binary.LittleEndian.PutUint32(buf[13:17], p.Trailer)
	return buf
}
