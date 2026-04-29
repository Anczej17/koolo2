package memory

import (
	"bytes"
	"encoding/binary"
	"strings"

	"local/internal/svc/internal/ntapi"
)

const (
	miscTxtRecordSize     = 0x1C0
	miscTxtInvFileOffset  = 0x30
	miscTxtAltCodeOffset  = 0x90
	miscTxtBaseCostOffset = 0xF8
)

// ItemBaseCostFromGameData returns BaseCost from D2R's live, expanded
// misc.txt records. This is intentionally separate from generated item data:
// the generated table has stale/missing costs for several consumables.
func (gd *GameReader) ItemBaseCostFromGameData(code string) (int, bool) {
	code = normalizeItemCode(code)
	if gd == nil || gd.Process == nil || code == "" {
		return 0, false
	}

	gd.itemTxtCostMu.Lock()
	defer gd.itemTxtCostMu.Unlock()

	if !gd.itemTxtCostScanned {
		gd.itemTxtBaseCosts = gd.scanMiscTxtBaseCosts()
		gd.itemTxtCostScanned = true
	}

	v, ok := gd.itemTxtBaseCosts[code]
	return v, ok
}

func (gd *GameReader) scanMiscTxtBaseCosts() map[string]int {
	out := make(map[string]int, 256)
	if gd == nil || gd.Process == nil || gd.Process.handler == 0 {
		return out
	}

	const maxUserVA = uintptr(0x0000800000000000)
	const chunkSize = uintptr(0x100000)
	const overlap = uintptr(miscTxtRecordSize)

	for addr := uintptr(0x10000); addr < maxUserVA; {
		info, err := ntapi.QueryVirtualMemory(gd.Process.handler, addr)
		if err != nil || info.RegionSize == 0 {
			addr += 0x10000
			continue
		}

		next := info.BaseAddress + info.RegionSize
		if next <= addr {
			addr += 0x10000
			continue
		}

		if info.State != ntapi.MEM_COMMIT ||
			info.Protect == ntapi.PAGE_NOACCESS ||
			(info.Protect&ntapi.PAGE_GUARD) != 0 {
			addr = next
			continue
		}

		regionEnd := info.BaseAddress + info.RegionSize
		for cur := info.BaseAddress; cur < regionEnd; cur += chunkSize {
			readLen := chunkSize + overlap
			if cur+readLen > regionEnd {
				readLen = regionEnd - cur
			}
			if readLen < miscTxtRecordSize {
				continue
			}
			buf := gd.Process.ReadBytesViaKernel32(cur, uint(readLen))
			if len(buf) < miscTxtRecordSize {
				continue
			}
			scanMiscTxtChunk(out, cur, buf)
		}
		addr = next
	}

	return out
}

func scanMiscTxtChunk(out map[string]int, _ uintptr, buf []byte) {
	pos := 0
	for {
		idx := bytes.Index(buf[pos:], []byte("inv"))
		if idx < 0 {
			return
		}
		idx += pos
		pos = idx + 1

		base := idx - miscTxtInvFileOffset
		if base < 0 || base+miscTxtBaseCostOffset+4 > len(buf) {
			continue
		}
		code := normalizeItemCode(string(buf[base : base+4]))
		if code == "" {
			continue
		}
		if !sameItemCode(buf[base+miscTxtAltCodeOffset:base+miscTxtAltCodeOffset+4], code) {
			continue
		}
		cost := int(binary.LittleEndian.Uint32(buf[base+miscTxtBaseCostOffset : base+miscTxtBaseCostOffset+4]))
		if cost <= 0 || cost > 10000000 {
			continue
		}
		out[code] = cost
	}
}

func sameItemCode(raw []byte, code string) bool {
	return normalizeItemCode(string(raw)) == code
}

func normalizeItemCode(code string) string {
	code = strings.TrimRight(code, "\x00 ")
	code = strings.TrimSpace(code)
	return strings.ToLower(code)
}
