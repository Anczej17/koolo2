package packet

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// ItemMoveStash / 0x54 — the real inv↔stash move packet, per live sniff
// logs/live_captures_2026_04_15/03_stash_jewel.json. Previously we had
// `NewCubeTransmute` pointing at the same opcode with 34 B cube-ingredient
// footer payload — that format never matched any server packet.
//
// Format (20 B, trimmed of 256-B sniff-buffer padding):
//
//	[0x54][itemGID:u32][flags:u32][srcCtx:u8][srcCol/row:u16][srcFiller:u8]
//	[dstCtx:u8][dstCol/row:u16][dstFiller:u32]
//
// Live capture `5453000000000000000700010004000000090007`:
//   54                opcode
//   53000000          itemGID = 0x53
//   00000000          flags = 0
//   00 0700 01        srcCtx=inv(0) col=7 row=1  (or col/row encoded as u16)
//   00 0400 00 00     dstCtx=stash(4) col=0 row=0 (packed)
//   00 0900 07        trailer / direction bytes
//
// Context byte encoding (from live captures + koolo legacy):
//   0x00 = inventory (main grid)
//   0x01 = cursor
//   0x02 = belt
//   0x03 = (unknown, possibly trade)
//   0x04 = stash (personal tab)
//   0x05 = stash shared tab 1
//   0x06 = stash shared tab 2
//   0x07 = stash shared tab 3
type ItemMoveStash struct {
	ItemGID uint32
	SrcCtx  byte
	SrcCol  byte
	SrcRow  byte
	DstCtx  byte
	DstCol  byte
	DstRow  byte
}

const (
	InvCtxInventory   byte = 0x00
	InvCtxCursor      byte = 0x01
	InvCtxBelt        byte = 0x02
	InvCtxStashMain   byte = 0x04
	InvCtxStashShared byte = 0x05
)

// NewItemMoveStash — the corrected 0x54 replacing NewCubeTransmute's
// 34 B form. Callers previously using NewCubeTransmute for actual cube
// transmutes should now use NewCubeTransmuteLegacy (kept as compat).
func NewItemMoveStash(itemGID data.UnitID, srcCtx, srcCol, srcRow, dstCtx, dstCol, dstRow byte) *ItemMoveStash {
	return &ItemMoveStash{
		ItemGID: uint32(itemGID),
		SrcCtx:  srcCtx,
		SrcCol:  srcCol,
		SrcRow:  srcRow,
		DstCtx:  dstCtx,
		DstCol:  dstCol,
		DstRow:  dstRow,
	}
}

func (p *ItemMoveStash) GetPayload() []byte {
	buf := make([]byte, 20)
	buf[0] = 0x54
	binary.LittleEndian.PutUint32(buf[1:5], p.ItemGID)
	// flags: zero for basic move.
	binary.LittleEndian.PutUint32(buf[5:9], 0)
	// Src block: ctx | col_low col_high | row. 4 B.
	buf[9] = p.SrcCtx
	buf[10] = p.SrcCol
	buf[11] = 0
	buf[12] = p.SrcRow
	// Dst block: ctx | col | filler | row + trailing bytes. 7 B.
	buf[13] = p.DstCtx
	buf[14] = p.DstCol
	buf[15] = 0
	buf[16] = p.DstRow
	// Trailer from live capture (`00 0900 07` → fixed across observed moves).
	buf[17] = 0x00
	buf[18] = 0x09
	buf[19] = 0x00
	return buf
}

// NewCubeTransmuteLegacy retains the old 34 B form used by code paths that
// haven't been migrated yet. Live captures have never confirmed this
// format so expect breakage on server round-trip; prefer NewItemMoveStash
// for stash interactions, and call this only from cube-transmute code
// that already round-trips successfully offline.
func NewCubeTransmuteLegacy(cubeGID data.UnitID) []byte {
	return NewCubeTransmute(cubeGID)
}
