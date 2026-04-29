package memory

import (
	"bytes"
	"encoding/binary"
	"regexp"
	"strings"

	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/ntapi"
)

var tooltipRenderPriceSnippet = regexp.MustCompile(`[\x20-\x7e\n]{0,512}(?:Sell value|Cost|Price):?\s*\d[\d,.]*[\x20-\x7e\n]{0,160}`)
var tooltipRenderMarkers = [][]byte{
	[]byte("Sell value"),
	[]byte("Cost"),
	[]byte("Price"),
}

func NewPanel(panelPtr uintptr, panelParent string, depth int, gd *GameReader) *data.Panel { //itemUnitPtr = uintptr(gd.Process.ReadUInt(itemUnitPtr+0x158, Uint64))
	panel := &data.Panel{
		PanelPtr:      panelPtr,
		PanelName:     gd.Process.ReadStringFromMemory(uintptr(gd.Process.ReadUInt(panelPtr+0x08, Uint64)), 0),
		PanelEnabled:  gd.Process.ReadUInt(panelPtr+0x50, Uint8) != 0,
		PanelVisible:  gd.Process.ReadUInt(panelPtr+0x51, Uint8) != 0,
		PtrChild:      uintptr(gd.Process.ReadUInt(panelPtr+0x58, Uint64)),
		NumChildren:   int(gd.Process.ReadUInt(panelPtr+0x60, Uint8)),
		ExtraText:     gd.Process.ReadStringFromMemory(panelPtr+0xA0, 0),
		ExtraText2:    gd.Process.ReadStringFromMemory(uintptr(gd.Process.ReadUInt(panelPtr+0x290, Uint64)), 0),
		ExtraText3:    gd.Process.ReadStringFromMemory(uintptr(gd.Process.ReadUInt(panelPtr+0x88, Uint64)), 0),
		PanelParent:   panelParent,
		PanelChildren: make(map[string]data.Panel),
		Depth:         depth,
	}
	if panel.NumChildren > 0 && panel.NumChildren < 50 {
		readPanel(panel.PtrChild, panel.NumChildren, &panel.PanelChildren, panel.PanelName, depth+1, gd)
	}
	return panel
}

func GetText(p data.Panel) string {
	text1 := cleanString(p.ExtraText)
	text2 := cleanString(p.ExtraText2)
	text3 := cleanString(p.ExtraText3)

	if text3 != "" && isASCII(text3) {
		return text3
	}
	if text2 != "" && isASCII(text2) {
		return text2
	}
	if text1 != "" && isASCII(text1) {
		return text1
	}
	return ""
}

// VisiblePanelTexts returns readable text from every visible panel in traversal
// order. Tooltip text is assembled by D2R after hover, so callers can use this
// as the live UI source instead of recalculating values from item txt data.
func (gd *GameReader) VisiblePanelTexts() []string {
	base := gd.Process.moduleBaseAddressPtr + gd.offset.PanelManagerContainerOffset
	panelStructPtr := uintptr(gd.Process.ReadUInt(base, Uint64))
	panelPtr := uintptr(gd.Process.ReadUInt(panelStructPtr+0x58, Uint64))
	numChildren := int(gd.Process.ReadUInt(panelStructPtr+0x60, Uint8))

	out := make([]string, 0, 64)
	seen := make(map[string]struct{}, 64)
	collectVisiblePanelTexts(panelPtr, numChildren, &out, seen, gd)
	return out
}

// TooltipRenderTexts scans render/cache pages referenced by visible panel child
// arrays. D2R keeps item tooltip strings here even when TooltipsPanel has no
// children and the normal panel text reader returns nothing.
func (gd *GameReader) TooltipRenderTexts() []string {
	return gd.tooltipRenderTexts(true)
}

// TooltipRenderTextsFast is the hot-path variant for vendor price resolution.
// It only scans pages anchored by visible panel child arrays. The broader
// item-unit cluster scan in TooltipRenderTexts is useful for diagnostics, but
// it is too slow to run immediately before every vendor packet.
func (gd *GameReader) TooltipRenderTextsFast() []string {
	return gd.tooltipRenderTexts(false)
}

// TooltipRenderTextsNearItemUnit scans readable heap pages near one item-unit
// allocation. Live D2R stores active tooltip layout strings in a sibling VAD,
// not always near visible panel child arrays.
func (gd *GameReader) TooltipRenderTextsNearItemUnit(unitPtr uintptr) []string {
	if gd == nil || gd.Process == nil || gd.Process.handler == 0 {
		return nil
	}
	itemAllocs := make(map[uintptr]struct{}, 1)
	if unitPtr != 0 {
		if info, err := ntapi.QueryVirtualMemory(gd.Process.handler, unitPtr); err == nil && info.AllocationBase != 0 {
			itemAllocs[info.AllocationBase] = struct{}{}
		}
	}
	if len(itemAllocs) == 0 {
		return nil
	}

	const scanSize uintptr = 0x4000000
	const maxReadBytes uintptr = 0x6000000
	const chunk uintptr = 0x100000

	out := make([]string, 0, 16)
	seenText := make(map[string]struct{}, 32)
	seenRegion := make(map[uintptr]struct{}, 32)
	var readBytes uintptr
	for alloc := range itemAllocs {
		end := alloc + scanSize
		for addr := alloc; addr < end && readBytes < maxReadBytes; {
			info, err := ntapi.QueryVirtualMemory(gd.Process.handler, addr)
			if err != nil || info.RegionSize == 0 {
				addr += 0x1000
				continue
			}
			next := info.BaseAddress + info.RegionSize
			if info.State != ntapi.MEM_COMMIT || !readableTooltipProtect(info.Protect) {
				addr = next
				continue
			}
			if _, ok := seenRegion[info.BaseAddress]; ok {
				addr = next
				continue
			}
			seenRegion[info.BaseAddress] = struct{}{}
			for off := uintptr(0); off < info.RegionSize && readBytes < maxReadBytes; off += chunk {
				readLen := chunk
				if off+readLen > info.RegionSize {
					readLen = info.RegionSize - off
				}
				buf := gd.Process.ReadBytesFromMemory(info.BaseAddress+off, uint(readLen))
				readBytes += readLen
				if len(buf) == 0 {
					continue
				}
				for _, text := range tooltipTextsFromRawBuffer(buf) {
					if _, ok := seenText[text]; ok {
						continue
					}
					seenText[text] = struct{}{}
					out = append(out, text)
					if len(out) >= 64 {
						return out
					}
				}
			}
			addr = next
		}
	}
	return out
}

// TooltipRenderTextsNearItemUnits scans readable heap pages near all current
// item-unit allocations. Keep this for diagnostics; the hot vendor path should
// prefer TooltipRenderTextsNearItemUnit for the target item.
func (gd *GameReader) TooltipRenderTextsNearItemUnits() []string {
	if gd == nil || gd.Process == nil || gd.Process.handler == 0 {
		return nil
	}
	itemAllocs := collectItemUnitAllocations(gd)
	if len(itemAllocs) == 0 {
		return nil
	}

	const scanSize uintptr = 0x4000000
	const maxReadBytes uintptr = 0x6000000
	const chunk uintptr = 0x100000

	out := make([]string, 0, 16)
	seenText := make(map[string]struct{}, 32)
	seenRegion := make(map[uintptr]struct{}, 32)
	var readBytes uintptr
	for alloc := range itemAllocs {
		end := alloc + scanSize
		for addr := alloc; addr < end && readBytes < maxReadBytes; {
			info, err := ntapi.QueryVirtualMemory(gd.Process.handler, addr)
			if err != nil || info.RegionSize == 0 {
				addr += 0x1000
				continue
			}
			next := info.BaseAddress + info.RegionSize
			if info.State != ntapi.MEM_COMMIT || !readableTooltipProtect(info.Protect) {
				addr = next
				continue
			}
			if _, ok := seenRegion[info.BaseAddress]; ok {
				addr = next
				continue
			}
			seenRegion[info.BaseAddress] = struct{}{}
			for off := uintptr(0); off < info.RegionSize && readBytes < maxReadBytes; off += chunk {
				readLen := chunk
				if off+readLen > info.RegionSize {
					readLen = info.RegionSize - off
				}
				buf := gd.Process.ReadBytesFromMemory(info.BaseAddress+off, uint(readLen))
				readBytes += readLen
				if len(buf) == 0 {
					continue
				}
				for _, text := range tooltipTextsFromRawBuffer(buf) {
					if _, ok := seenText[text]; ok {
						continue
					}
					seenText[text] = struct{}{}
					out = append(out, text)
					if len(out) >= 64 {
						return out
					}
				}
			}
			addr = next
		}
	}
	return out
}

func (gd *GameReader) tooltipRenderTexts(includeItemClusters bool) []string {
	base := gd.Process.moduleBaseAddressPtr + gd.offset.PanelManagerContainerOffset
	panelStructPtr := uintptr(gd.Process.ReadUInt(base, Uint64))
	panelPtr := uintptr(gd.Process.ReadUInt(panelStructPtr+0x58, Uint64))
	numChildren := int(gd.Process.ReadUInt(panelStructPtr+0x60, Uint8))

	childPages := make(map[uintptr]struct{}, 64)
	collectPanelChildPages(panelPtr, numChildren, childPages, gd)

	scanPages := make(map[uintptr]struct{}, 256)
	for page := range childPages {
		for _, delta := range []int{-0x2000, -0x1000, 0, 0x1000, 0x2000} {
			if addr := pageFromDelta(page, delta); addr != 0 {
				scanPages[addr] = struct{}{}
			}
		}
	}
	if includeItemClusters {
		for anchor := range collectItemUnitClusters(gd) {
			for delta := -0x400000; delta <= 0x200000; delta += 0x1000 {
				if addr := pageFromDelta(anchor, delta); addr != 0 {
					scanPages[addr] = struct{}{}
				}
			}
		}
	}

	out := make([]string, 0, 16)
	seen := make(map[string]struct{}, 16)
	for page := range scanPages {
		buf := gd.Process.ReadBytesFromMemory(page, 0x1000)
		if len(buf) == 0 {
			continue
		}
		for _, match := range tooltipRenderPriceSnippet.FindAll(buf, -1) {
			text := cleanTooltipRenderSnippet(match)
			if text == "" {
				continue
			}
			if _, ok := seen[text]; ok {
				continue
			}
			seen[text] = struct{}{}
			out = append(out, text)
		}
		for _, pageText := range utf16LEPrintablePages(buf) {
			for _, match := range tooltipRenderPriceSnippet.FindAllString(pageText, -1) {
				text := cleanTooltipRenderText(match)
				if text == "" {
					continue
				}
				if _, ok := seen[text]; ok {
					continue
				}
				seen[text] = struct{}{}
				out = append(out, text)
			}
		}
	}
	return out
}

func readableTooltipProtect(protect uint32) bool {
	if protect&ntapi.PAGE_GUARD != 0 || protect&ntapi.PAGE_NOACCESS != 0 {
		return false
	}
	return protect&(ntapi.PAGE_READONLY|ntapi.PAGE_READWRITE) != 0
}

func collectItemUnitAllocations(gd *GameReader) map[uintptr]struct{} {
	allocs := make(map[uintptr]struct{}, 4)
	if gd == nil || gd.Process == nil || gd.Process.handler == 0 {
		return allocs
	}
	baseAddr := gd.Process.moduleBaseAddressPtr + gd.offset.UnitTable + (4 * 1024)
	table := gd.Process.ReadBytesFromMemory(baseAddr, 128*8)
	if len(table) < 128*8 {
		return allocs
	}
	seenUnits := make(map[uintptr]struct{}, 256)
	for slot := 0; slot < 128; slot++ {
		unitPtr := uintptr(binary.LittleEndian.Uint64(table[slot*8 : slot*8+8]))
		for hops := 0; unitPtr != 0 && hops < 512; hops++ {
			if _, ok := seenUnits[unitPtr]; ok {
				break
			}
			seenUnits[unitPtr] = struct{}{}
			if info, err := ntapi.QueryVirtualMemory(gd.Process.handler, unitPtr); err == nil && info.AllocationBase != 0 {
				allocs[info.AllocationBase] = struct{}{}
			}
			next := gd.Process.ReadBytesFromMemory(unitPtr+0x158, 8)
			if len(next) != 8 {
				break
			}
			unitPtr = uintptr(binary.LittleEndian.Uint64(next))
		}
	}
	return allocs
}

func tooltipTextsFromRawBuffer(buf []byte) []string {
	out := make([]string, 0, 4)
	seen := make(map[string]struct{}, 4)
	for _, marker := range tooltipRenderMarkers {
		searchAt := 0
		for searchAt < len(buf) {
			idx := bytes.Index(buf[searchAt:], marker)
			if idx < 0 {
				break
			}
			abs := searchAt + idx
			start := abs - 512
			if start < 0 {
				start = 0
			}
			end := abs + 192
			if end > len(buf) {
				end = len(buf)
			}
			text := cleanTooltipRenderLooseASCII(buf[start:end])
			if text != "" {
				if _, ok := seen[text]; !ok {
					seen[text] = struct{}{}
					out = append(out, text)
				}
			}
			searchAt = abs + len(marker)
		}
	}
	return out
}

func pageFromDelta(page uintptr, delta int) uintptr {
	addr := int64(page) + int64(delta)
	if addr <= 0 {
		return 0
	}
	return uintptr(addr) & ^uintptr(0xfff)
}

func collectItemUnitClusters(gd *GameReader) map[uintptr]struct{} {
	clusters := make(map[uintptr]struct{}, 16)
	baseAddr := gd.Process.moduleBaseAddressPtr + gd.offset.UnitTable + (4 * 1024)
	table := gd.Process.ReadBytesFromMemory(baseAddr, 128*8)
	if len(table) < 128*8 {
		return clusters
	}
	seenUnits := make(map[uintptr]struct{}, 256)
	for slot := 0; slot < 128; slot++ {
		unitPtr := uintptr(binary.LittleEndian.Uint64(table[slot*8 : slot*8+8]))
		for hops := 0; unitPtr != 0 && hops < 512; hops++ {
			if _, ok := seenUnits[unitPtr]; ok {
				break
			}
			seenUnits[unitPtr] = struct{}{}
			clusters[unitPtr&^uintptr(0xfffff)] = struct{}{}
			next := gd.Process.ReadBytesFromMemory(unitPtr+0x158, 8)
			if len(next) != 8 {
				break
			}
			unitPtr = uintptr(binary.LittleEndian.Uint64(next))
		}
	}
	return clusters
}

func collectPanelChildPages(panelPtr uintptr, numChildren int, pages map[uintptr]struct{}, gd *GameReader) {
	if numChildren <= 0 || numChildren >= 80 {
		return
	}
	for i := 0; i < numChildren; i++ {
		panelStructPtr := uintptr(gd.Process.ReadUInt(uintptr(uint64(panelPtr)+uint64(i*8)), Uint64))
		if panelStructPtr == 0 {
			continue
		}
		childPtr := uintptr(gd.Process.ReadUInt(panelStructPtr+0x58, Uint64))
		childCount := int(gd.Process.ReadUInt(panelStructPtr+0x60, Uint8))
		if childPtr != 0 {
			pages[childPtr&^uintptr(0xfff)] = struct{}{}
		}
		if childCount > 0 && childCount < 50 {
			collectPanelChildPages(childPtr, childCount, pages, gd)
		}
	}
}

func cleanTooltipRenderSnippet(raw []byte) string {
	raw = bytes.Trim(raw, "\x00")
	return cleanTooltipRenderText(string(raw))
}

func cleanTooltipRenderText(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	cleaned := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		line = strings.TrimLeft(line, "0123456789")
		line = strings.TrimSpace(line)
		if line != "" {
			cleaned = append(cleaned, line)
		}
	}
	return strings.Join(cleaned, "\n")
}

func cleanTooltipRenderLooseASCII(raw []byte) string {
	var b strings.Builder
	lastWasNL := true
	for _, c := range raw {
		if c == '\r' || c == '\n' || c == '\t' {
			if !lastWasNL {
				b.WriteByte('\n')
				lastWasNL = true
			}
			continue
		}
		if c >= 0x20 && c <= 0x7e {
			b.WriteByte(c)
			lastWasNL = false
			continue
		}
		if !lastWasNL {
			b.WriteByte('\n')
			lastWasNL = true
		}
	}
	return cleanTooltipRenderText(b.String())
}

func utf16LEPrintablePages(buf []byte) []string {
	out := make([]string, 0, 2)
	for align := 0; align < 2; align++ {
		var b strings.Builder
		lastWasNL := true
		for i := align; i+1 < len(buf); i += 2 {
			lo := buf[i]
			hi := buf[i+1]
			if hi == 0 && (lo == '\n' || lo == '\r' || lo == '\t' || (lo >= 0x20 && lo <= 0x7e)) {
				if lo == '\r' || lo == '\t' {
					lo = '\n'
				}
				if lo == '\n' {
					if !lastWasNL {
						b.WriteByte('\n')
						lastWasNL = true
					}
					continue
				}
				b.WriteByte(lo)
				lastWasNL = false
				continue
			}
			if !lastWasNL {
				b.WriteByte('\n')
				lastWasNL = true
			}
		}
		text := strings.TrimSpace(b.String())
		if len(text) >= 16 {
			out = append(out, text)
		}
	}
	return out
}

func collectVisiblePanelTexts(panelPtr uintptr, numChildren int, out *[]string, seen map[string]struct{}, gd *GameReader) {
	if numChildren <= 0 || numChildren >= 50 {
		return
	}
	for i := 0; i < numChildren; i++ {
		panelStructPtr := uintptr(gd.Process.ReadUInt(uintptr(uint64(panelPtr)+uint64(i*8)), Uint64))
		if panelStructPtr == 0 {
			continue
		}
		p := data.Panel{
			PanelEnabled: gd.Process.ReadUInt(panelStructPtr+0x50, Uint8) != 0,
			PanelVisible: gd.Process.ReadUInt(panelStructPtr+0x51, Uint8) != 0,
			PtrChild:     uintptr(gd.Process.ReadUInt(panelStructPtr+0x58, Uint64)),
			NumChildren:  int(gd.Process.ReadUInt(panelStructPtr+0x60, Uint8)),
			ExtraText:    gd.Process.ReadStringFromMemory(panelStructPtr+0xA0, 0),
			ExtraText2:   gd.Process.ReadStringFromMemory(uintptr(gd.Process.ReadUInt(panelStructPtr+0x290, Uint64)), 0),
			ExtraText3:   gd.Process.ReadStringFromMemory(uintptr(gd.Process.ReadUInt(panelStructPtr+0x88, Uint64)), 0),
		}
		if p.PanelVisible && p.PanelEnabled {
			if text := strings.TrimSpace(GetText(p)); text != "" {
				if _, ok := seen[text]; !ok {
					seen[text] = struct{}{}
					*out = append(*out, text)
				}
			}
			collectVisiblePanelTexts(p.PtrChild, p.NumChildren, out, seen, gd)
		}
	}
}

func cleanString(input string) string {
	return input // Replace newlines and carriage returns as needed
}

func isASCII(s string) bool {
	for _, r := range s {
		if r < 32 || r > 126 {
			return false
		}
	}
	return true
}

// ReadAllPanels reads all panels from the game memory
func (gd *GameReader) ReadAllPanels() map[string]data.Panel {
	base := gd.Process.moduleBaseAddressPtr + gd.offset.PanelManagerContainerOffset
	panelStructPtr := uintptr(gd.Process.ReadUInt(base, Uint64))
	panelPtr := uintptr(gd.Process.ReadUInt(panelStructPtr+0x58, Uint64))
	numChildren := int(gd.Process.ReadUInt(panelStructPtr+0x60, Uint8))

	panels := make(map[string]data.Panel)
	depth := 0
	// recursively read all panels, starting with the Root panel
	readPanel(panelPtr, numChildren, &panels, "Root", depth, gd)
	return panels
}

func readPanel(panelPtr uintptr, numChildren int, panels *map[string]data.Panel, panelParent string, depth int, gd *GameReader) {
	for i := 0; i < numChildren; i++ {
		panelStructPtr := uintptr(gd.Process.ReadUInt(uintptr(uint64(panelPtr)+uint64(i*8)), Uint64))
		thisPanel := NewPanel(panelStructPtr, panelParent, depth, gd)
		(*panels)[thisPanel.PanelName] = *thisPanel
	}
}
