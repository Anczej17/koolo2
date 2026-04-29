package memory

import (
	"bytes"
	"encoding/binary"
	"time"

	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/ntapi"
)

const (
	vendorShopRootMagic   uint32  = 0x01020304
	vendorShopGridW       int     = 10
	vendorShopGridH       int     = 10
	vendorShopScanBack    uintptr = 0x02000000
	vendorShopScanForward uintptr = 0x01000000
	vendorShopScanChunk   uintptr = 0x100000
)

// NativeVendorSellSlot resolves the 0x33 TargetTab/ToPos from D2R's active
// shopRoot grids. It is RPM-only; it does not call D2R code or touch UI state.
func (gd *GameReader) NativeVendorSellSlot(i data.Item, vendorItems []data.Item, candidateTabs []byte) (uint16, uint16, byte, bool) {
	if gd == nil || gd.Process == nil || gd.Process.handler == 0 || len(candidateTabs) == 0 {
		return 0, 0, 0, false
	}
	w, h := i.Desc().InventoryWidth, i.Desc().InventoryHeight
	if w <= 0 || h <= 0 || w > vendorShopGridW || h > vendorShopGridH {
		return 0, 0, 0, false
	}

	root, ok := gd.findActiveVendorShopRoot(vendorItems)
	if !ok {
		return 0, 0, 0, false
	}
	pages := gd.readVendorShopTabGrids(root)
	if len(pages) == 0 {
		return 0, 0, 0, false
	}
	for _, tab := range candidateTabs {
		grid, ok := pages[tab]
		if !ok {
			continue
		}
		if x, y, ok := nativeVendorGridFirstFit(grid, w, h); ok {
			return uint16(x), uint16(y), tab, true
		}
	}
	return 0, 0, 0, false
}

type nativeVendorGrid struct {
	occupied [vendorShopGridH][vendorShopGridW]bool
}

func (gd *GameReader) findActiveVendorShopRoot(vendorItems []data.Item) (uintptr, bool) {
	vendorPtrs := make(map[uintptr]struct{}, len(vendorItems))
	var anchors []uintptr
	cacheKey := uintptr(len(vendorItems))
	for _, it := range vendorItems {
		if it.UnitPtr == 0 {
			continue
		}
		vendorPtrs[it.UnitPtr] = struct{}{}
		anchors = append(anchors, it.UnitPtr)
		cacheKey ^= it.UnitPtr + 0x9e3779b97f4a7c15 + (cacheKey << 6) + (cacheKey >> 2)
	}
	if len(vendorPtrs) == 0 {
		return 0, false
	}
	if root, ok := gd.cachedVendorShopRoot(cacheKey, vendorPtrs); ok {
		return root, true
	}

	bestRoot := uintptr(0)
	bestScore := 0
	seenRegion := make(map[uintptr]struct{}, 64)
	for _, anchor := range anchors {
		info, err := ntapi.QueryVirtualMemory(gd.Process.handler, anchor)
		if err != nil || info.AllocationBase == 0 {
			continue
		}
		start := info.AllocationBase
		if start > vendorShopScanBack {
			start -= vendorShopScanBack
		}
		end := info.AllocationBase + vendorShopScanForward
		if end < info.AllocationBase {
			end = info.AllocationBase
		}
		for addr := start; addr < end; {
			region, err := ntapi.QueryVirtualMemory(gd.Process.handler, addr)
			if err != nil || region.RegionSize == 0 {
				addr += 0x10000
				continue
			}
			next := region.BaseAddress + region.RegionSize
			if next <= addr {
				addr += 0x10000
				continue
			}
			if region.State != ntapi.MEM_COMMIT ||
				region.Protect == ntapi.PAGE_NOACCESS ||
				(region.Protect&ntapi.PAGE_GUARD) != 0 {
				addr = next
				continue
			}
			if _, ok := seenRegion[region.BaseAddress]; ok {
				addr = next
				continue
			}
			seenRegion[region.BaseAddress] = struct{}{}
			root, score := gd.scanVendorShopRootRegion(region.BaseAddress, region.RegionSize, vendorPtrs)
			if score > bestScore {
				bestRoot = root
				bestScore = score
			}
			addr = next
		}
	}
	if bestRoot == 0 || bestScore < 8 {
		return 0, false
	}
	gd.storeVendorShopRoot(cacheKey, bestRoot)
	return bestRoot, true
}

func (gd *GameReader) cachedVendorShopRoot(cacheKey uintptr, vendorPtrs map[uintptr]struct{}) (uintptr, bool) {
	gd.vendorShopRootMu.Lock()
	root := gd.vendorShopRootPtr
	key := gd.vendorShopRootKey
	seen := gd.vendorShopRootSeen
	gd.vendorShopRootMu.Unlock()

	if root == 0 || key != cacheKey || time.Since(seen) > 30*time.Second {
		return 0, false
	}
	if uint32(gd.Process.ReadUInt(root+0x00, Uint32)) != vendorShopRootMagic {
		return 0, false
	}
	first := uintptr(gd.Process.ReadUInt(root+0x10, Uint64))
	grids := uintptr(gd.Process.ReadUInt(root+0x20, Uint64))
	count := gd.Process.ReadUInt(root+0x28, Uint64)
	if !looksLikeD2RPtr(first) || !looksLikeD2RPtr(grids) || count == 0 || count > 32 {
		return 0, false
	}
	if gd.scoreVendorShopRoot(first, vendorPtrs) < 8 {
		return 0, false
	}
	return root, true
}

func (gd *GameReader) storeVendorShopRoot(cacheKey, root uintptr) {
	gd.vendorShopRootMu.Lock()
	defer gd.vendorShopRootMu.Unlock()
	gd.vendorShopRootKey = cacheKey
	gd.vendorShopRootPtr = root
	gd.vendorShopRootSeen = time.Now()
}

func (gd *GameReader) scanVendorShopRootRegion(base, size uintptr, vendorPtrs map[uintptr]struct{}) (uintptr, int) {
	bestRoot := uintptr(0)
	bestScore := 0
	for off := uintptr(0); off < size; off += vendorShopScanChunk {
		readLen := vendorShopScanChunk
		if off+readLen > size {
			readLen = size - off
		}
		if readLen < 0x30 {
			continue
		}
		buf := gd.Process.ReadBytesViaKernel32(base+off, uint(readLen))
		if len(buf) < 0x30 {
			continue
		}
		pos := 0
		needle := []byte{0x04, 0x03, 0x02, 0x01}
		for {
			idx := bytes.Index(buf[pos:], needle)
			if idx < 0 {
				break
			}
			idx += pos
			root := base + off + uintptr(idx)
			if idx+0x30 <= len(buf) {
				first := uintptr(binary.LittleEndian.Uint64(buf[idx+0x10 : idx+0x18]))
				grids := uintptr(binary.LittleEndian.Uint64(buf[idx+0x20 : idx+0x28]))
				count := binary.LittleEndian.Uint64(buf[idx+0x28 : idx+0x30])
				if first != 0 && grids != 0 && count > 0 && count <= 32 {
					score := gd.scoreVendorShopRoot(first, vendorPtrs)
					if score > bestScore {
						bestScore = score
						bestRoot = root
					}
				}
			}
			pos = idx + 1
		}
	}
	return bestRoot, bestScore
}

func (gd *GameReader) scoreVendorShopRoot(first uintptr, vendorPtrs map[uintptr]struct{}) int {
	score := 0
	seen := make(map[uintptr]struct{}, 64)
	for ptr := first; ptr != 0 && len(seen) < 128; {
		if _, ok := seen[ptr]; ok {
			break
		}
		seen[ptr] = struct{}{}
		if uint32(gd.Process.ReadUInt(ptr+0x00, Uint32)) != 4 {
			break
		}
		if _, ok := vendorPtrs[ptr]; ok {
			score += 10
		} else {
			score++
		}
		unitData := uintptr(gd.Process.ReadUInt(ptr+0x10, Uint64))
		if !looksLikeD2RPtr(unitData) {
			break
		}
		ptr = uintptr(gd.Process.ReadUInt(unitData+0xB0, Uint64))
	}
	return score
}

func (gd *GameReader) readVendorShopTabGrids(root uintptr) map[byte]nativeVendorGrid {
	out := make(map[byte]nativeVendorGrid, 4)
	gridBase := uintptr(gd.Process.ReadUInt(root+0x20, Uint64))
	gridCount := int(gd.Process.ReadUInt(root+0x28, Uint64))
	if !looksLikeD2RPtr(gridBase) || gridCount < 3 || gridCount > 32 {
		return out
	}
	for nativePage := 2; nativePage < gridCount; nativePage++ {
		gridStruct := gridBase + uintptr(nativePage)*0x20
		w := int(gd.Process.ReadUInt(gridStruct+0x10, Uint8))
		h := int(gd.Process.ReadUInt(gridStruct+0x11, Uint8))
		cells := uintptr(gd.Process.ReadUInt(gridStruct+0x18, Uint64))
		if w != vendorShopGridW || h != vendorShopGridH || !looksLikeD2RPtr(cells) {
			continue
		}
		var grid nativeVendorGrid
		cellBuf := gd.Process.ReadBytesFromMemory(cells, uint(w*h*8))
		if len(cellBuf) < w*h*8 {
			continue
		}
		for idx := 0; idx < w*h; idx++ {
			ptr := uintptr(binary.LittleEndian.Uint64(cellBuf[idx*8 : idx*8+8]))
			if ptr != 0 {
				grid.occupied[idx/w][idx%w] = true
			}
		}
		out[byte(nativePage-2)] = grid
	}
	return out
}

func nativeVendorGridFirstFit(grid nativeVendorGrid, w, h int) (int, int, bool) {
	if h == 1 {
		for x := vendorShopGridW - w; x >= 0; x-- {
			for y := 0; y <= vendorShopGridH-h; y++ {
				if nativeVendorRectFree(grid, x, y, w, h) {
					return x, y, true
				}
			}
		}
		return 0, 0, false
	}
	for x := 0; x <= vendorShopGridW-w; x++ {
		for y := 0; y <= vendorShopGridH-h; y++ {
			if nativeVendorRectFree(grid, x, y, w, h) {
				return x, y, true
			}
		}
	}
	return 0, 0, false
}

func nativeVendorRectFree(grid nativeVendorGrid, x, y, w, h int) bool {
	for dy := 0; dy < h; dy++ {
		for dx := 0; dx < w; dx++ {
			if grid.occupied[y+dy][x+dx] {
				return false
			}
		}
	}
	return true
}

func looksLikeD2RPtr(ptr uintptr) bool {
	return ptr >= 0x10000000000 && ptr < 0x0000800000000000
}
