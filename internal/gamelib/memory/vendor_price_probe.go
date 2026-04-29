package memory

import (
	"fmt"

	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/stat"
)

const (
	vendorDataBucketsRVA uintptr = 0x1DFDAA0
	vendorMode6ValueRVA  uintptr = 0x291940

	vendorUISelectionKeyRVA    uintptr = 0x1EAA3D4
	vendorUIInteractionListRVA uintptr = 0x1EC2B90
	vendorUIContextResolverRVA uintptr = 0x6C970
	vendorUISelectionLookupRVA uintptr = 0x72A70

	vendorItemRecordSize       uintptr = 0x1C0
	vendorItemRateAOffset      uintptr = 0x11A
	vendorItemRateBOffset      uintptr = 0x11C
	vendorItemDifficultyOffset uintptr = 0x1BD
	vendorItemDiscountFlag     uint32  = 0x400000
	vendorItemTypeAOffset      uintptr = 0x12E
	vendorItemTypeBOffset      uintptr = 0x130
)

type VendorPriceProbe struct {
	ItemGID         data.UnitID          `json:"item_gid"`
	ItemName        string               `json:"item_name"`
	ItemUnitPtr     uintptr              `json:"item_unit_ptr"`
	ItemUnitDataPtr uintptr              `json:"item_unit_data_ptr"`
	ItemTxtID       int                  `json:"item_txt_id"`
	MerchantGID     data.UnitID          `json:"merchant_gid"`
	MerchantUnitPtr uintptr              `json:"merchant_unit_ptr"`
	RecordPtr       uintptr              `json:"record_ptr"`
	RecordOffset    uintptr              `json:"record_offset"`
	RateA           uint32               `json:"rate_a"`
	RateB           uint32               `json:"rate_b"`
	Mode6Value      int64                `json:"mode6_value"`
	Mode6Merchant   int64                `json:"mode6_merchant"`
	Mode6Player     int64                `json:"mode6_player"`
	Mode6UIContext  int64                `json:"mode6_ui_context"`
	Mode6Source     string               `json:"mode6_source"`
	Stat5B          int64                `json:"stat_5b"`
	Value           int64                `json:"value"`
	Discounted      bool                 `json:"discounted"`
	FormulaComplete bool                 `json:"formula_complete"`
	NativeBlocked   bool                 `json:"native_blocked"`
	MissingPart     string               `json:"missing_part,omitempty"`
	PriceA          uint32               `json:"price_a"`
	PriceB          uint32               `json:"price_b"`
	UIContext       VendorUIContextProbe `json:"ui_context"`
	Mode6Probes     []VendorMode6Probe   `json:"mode6_probes,omitempty"`
	FunctionRVAs    struct {
		DataBuckets     string `json:"data_buckets"`
		Mode6           string `json:"mode6"`
		UISelectionKey  string `json:"ui_selection_key"`
		UIList          string `json:"ui_list"`
		UIResolver      string `json:"ui_resolver"`
		UISelectionFind string `json:"ui_selection_find"`
	} `json:"function_rvas"`
}

type VendorUIContextProbe struct {
	SelectionKey     uint32                     `json:"selection_key"`
	InteractionHead  uintptr                    `json:"interaction_head"`
	BestContextPtr   uintptr                    `json:"best_context_ptr"`
	BestContextValue int32                      `json:"best_context_value"`
	Candidates       []VendorUIContextCandidate `json:"candidates,omitempty"`
}

type VendorUIContextCandidate struct {
	EntryPtr      uintptr `json:"entry_ptr"`
	Kind          uint32  `json:"kind"`
	ID            int32   `json:"id"`
	TargetID      int32   `json:"target_id"`
	State         uint32  `json:"state"`
	NextPtr       uintptr `json:"next_ptr"`
	PointerOffset uintptr `json:"pointer_offset,omitempty"`
	Pointer       uintptr `json:"pointer,omitempty"`
	Mode6Value    int32   `json:"mode6_value,omitempty"`
}

type VendorMode6Probe struct {
	Source       string  `json:"source"`
	ContextPtr   uintptr `json:"context_ptr"`
	ItemPtr      uintptr `json:"item_ptr"`
	StatID       uint32  `json:"stat_id"`
	SrcOwner     uintptr `json:"src_owner"`
	Src          uintptr `json:"src"`
	Record       uintptr `json:"record"`
	RecordKind   uint32  `json:"record_kind"`
	RecordTypeID int32   `json:"record_type_id"`
	Difficulty   uint8   `json:"difficulty"`
	Bucket       uintptr `json:"bucket"`
	TypeCount    int32   `json:"type_count"`
	Stride       uint32  `json:"stride"`
	BitsetBase   uintptr `json:"bitset_base"`
	TypeFlagWord uint32  `json:"type_flag_word"`

	PrimaryHandled        bool    `json:"primary_handled"`
	PrimaryValue          int32   `json:"primary_value"`
	PrimaryFailure        string  `json:"primary_failure,omitempty"`
	PrimaryStatsListEx    uintptr `json:"primary_stats_list_ex,omitempty"`
	PrimaryStatsPtr       uintptr `json:"primary_stats_ptr,omitempty"`
	PrimaryStatCount      int     `json:"primary_stat_count,omitempty"`
	PrimaryMatchedStats   int     `json:"primary_matched_stats,omitempty"`
	PrimaryCandidateStats int     `json:"primary_candidate_stats,omitempty"`

	FallbackValue          int32   `json:"fallback_value"`
	FallbackFailure        string  `json:"fallback_failure,omitempty"`
	FallbackStatsListEx    uintptr `json:"fallback_stats_list_ex,omitempty"`
	FallbackStatsPtr       uintptr `json:"fallback_stats_ptr,omitempty"`
	FallbackStatCount      int     `json:"fallback_stat_count,omitempty"`
	FallbackMatchedStats   int     `json:"fallback_matched_stats,omitempty"`
	FallbackCandidateStats int     `json:"fallback_candidate_stats,omitempty"`
}

type statListDiag struct {
	StatsListEx uintptr
	StatsPtr    uintptr
	StatCount   int
	Scanned     int
	Matched     int
	Failure     string
}

func (p VendorMode6Probe) Value() int32 {
	if p.PrimaryHandled {
		return p.PrimaryValue
	}
	return p.FallbackValue
}

// VendorPriceProbeForItem mirrors the vendor price path recovered in Ghidra.
// This probe is deliberately RPM-only. Calling the D2R helpers directly through
// APC crashed live D2R during vendor testing, so the recovered mode=6 branch is
// reimplemented locally instead of invoking D2R code.
func (gd *GameReader) VendorPriceProbeForItem(i data.Item, merchantGID data.UnitID, playerGID ...data.UnitID) (VendorPriceProbe, error) {
	var out VendorPriceProbe
	out.ItemGID = i.UnitID
	out.ItemName = string(i.Name)
	out.ItemUnitPtr = i.UnitPtr
	out.ItemUnitDataPtr = i.UnitDataPtr
	out.ItemTxtID = i.ID
	out.MerchantGID = merchantGID
	out.FunctionRVAs.DataBuckets = fmt.Sprintf("0x%X", vendorDataBucketsRVA)
	out.FunctionRVAs.Mode6 = fmt.Sprintf("0x%X", vendorMode6ValueRVA)
	out.FunctionRVAs.UISelectionKey = fmt.Sprintf("0x%X", vendorUISelectionKeyRVA)
	out.FunctionRVAs.UIList = fmt.Sprintf("0x%X", vendorUIInteractionListRVA)
	out.FunctionRVAs.UIResolver = fmt.Sprintf("0x%X", vendorUIContextResolverRVA)
	out.FunctionRVAs.UISelectionFind = fmt.Sprintf("0x%X", vendorUISelectionLookupRVA)

	if gd == nil || gd.Process == nil {
		return out, fmt.Errorf("game reader/process is nil")
	}
	base := gd.Process.ModuleBaseAddress()
	if base == 0 {
		return out, fmt.Errorf("module base is zero")
	}
	if i.UnitPtr == 0 || i.UnitDataPtr == 0 {
		return out, fmt.Errorf("item pointers are empty for gid 0x%X", i.UnitID)
	}

	merchantPtr := gd.findUnitPtrByID(1, merchantGID)
	if merchantPtr == 0 {
		return out, fmt.Errorf("merchant gid 0x%X unit pointer not found", merchantGID)
	}
	out.MerchantUnitPtr = merchantPtr

	txtID := uint32(gd.Process.ReadUInt(i.UnitPtr+0x04, Uint32))
	difficulty := uint8(gd.Process.ReadUInt(i.UnitPtr+vendorItemDifficultyOffset, Uint8))
	out.ItemTxtID = int(txtID)

	recordPtr, err := gd.vendorItemRecordPtrRPM(difficulty, txtID)
	if err != nil {
		return out, err
	}
	out.RecordPtr = recordPtr
	out.RecordOffset = uintptr(txtID) * vendorItemRecordSize
	out.RateA = uint32(gd.Process.ReadUInt(out.RecordPtr+vendorItemRateAOffset, Uint16))
	out.RateB = uint32(gd.Process.ReadUInt(out.RecordPtr+vendorItemRateBOffset, Uint16))

	if st, ok := i.FindStat(stat.ID(0x5B), 0); ok {
		out.Stat5B = int64(st.Value)
	}
	merchantProbe := gd.vendorMode6ProbeRPM("merchant", merchantPtr, i.UnitPtr, 0xCB)
	out.Mode6Probes = append(out.Mode6Probes, merchantProbe)
	out.Mode6Merchant = int64(merchantProbe.Value())
	out.Mode6Value = out.Mode6Merchant
	out.Mode6Source = "merchant"
	itemProbe := gd.vendorMode6ProbeRPM("item", i.UnitPtr, i.UnitPtr, 0xCB)
	out.Mode6Probes = append(out.Mode6Probes, itemProbe)
	if itemValue := int64(itemProbe.Value()); out.Mode6Value == 0 && itemValue != 0 {
		out.Mode6Value = itemValue
		out.Mode6Source = "item"
	}
	out.UIContext = gd.vendorUIContextProbeRPM(i.UnitPtr)
	out.Mode6UIContext = int64(out.UIContext.BestContextValue)
	if out.Mode6UIContext != 0 {
		out.Mode6Value = out.Mode6UIContext
		out.Mode6Source = "ui_context"
		out.Mode6Probes = append(out.Mode6Probes, gd.vendorMode6ProbeRPM("ui_context", out.UIContext.BestContextPtr, i.UnitPtr, 0xCB))
	}
	if len(playerGID) > 0 && playerGID[0] != 0 {
		playerPtr := gd.findUnitPtrByID(0, playerGID[0])
		playerProbe := gd.vendorMode6ProbeRPM("player", playerPtr, i.UnitPtr, 0xCB)
		out.Mode6Probes = append(out.Mode6Probes, playerProbe)
		out.Mode6Player = int64(playerProbe.Value())
		if out.Mode6Value == 0 && out.Mode6Player != 0 {
			out.Mode6Value = out.Mode6Player
			out.Mode6Source = "player"
		}
	}
	out.Value = out.Mode6Value + out.Stat5B

	flags := uint32(gd.Process.ReadUInt(i.UnitDataPtr+0x18, Uint32))
	out.Discounted = flags&vendorItemDiscountFlag != 0
	out.PriceA = vendorPriceFromRate(out.Value, out.RateA, out.Discounted)
	out.PriceB = vendorPriceFromRate(out.Value, out.RateB, out.Discounted)
	out.FormulaComplete = true
	return out, nil
}

// VendorPriceProbeForItemWithContextOverride recomputes the recovered mode=6
// value with an explicit native context pointer captured from the tooltip path.
func (gd *GameReader) VendorPriceProbeForItemWithContextOverride(i data.Item, merchantGID data.UnitID, contextPtr uintptr, playerGID ...data.UnitID) (VendorPriceProbe, error) {
	out, err := gd.VendorPriceProbeForItem(i, merchantGID, playerGID...)
	if err != nil {
		return out, err
	}
	if contextPtr == 0 {
		return out, nil
	}
	probe := gd.vendorMode6ProbeRPM("context_override", contextPtr, i.UnitPtr, 0xCB)
	out.Mode6Probes = append(out.Mode6Probes, probe)
	out.Mode6Value = int64(probe.Value())
	out.Mode6Source = "context_override"
	out.Value = out.Mode6Value + out.Stat5B
	out.PriceA = vendorPriceFromRate(out.Value, out.RateA, out.Discounted)
	out.PriceB = vendorPriceFromRate(out.Value, out.RateB, out.Discounted)
	return out, nil
}

func (gd *GameReader) vendorUIContextProbeRPM(itemPtr uintptr) VendorUIContextProbe {
	var out VendorUIContextProbe
	if gd == nil || gd.Process == nil {
		return out
	}
	base := gd.Process.ModuleBaseAddress()
	if base == 0 {
		return out
	}
	out.SelectionKey = uint32(gd.Process.ReadUInt(base+vendorUISelectionKeyRVA, Uint32))
	out.InteractionHead = uintptr(gd.Process.ReadUInt(base+vendorUIInteractionListRVA, Uint64))
	if out.InteractionHead == 0 {
		return out
	}

	seen := make(map[uintptr]struct{}, 16)
	for entry := out.InteractionHead; entry != 0 && len(seen) < 16 && len(out.Candidates) < 64; entry = uintptr(gd.Process.ReadUInt(entry+0x30, Uint64)) {
		if _, ok := seen[entry]; ok {
			break
		}
		seen[entry] = struct{}{}

		baseCandidate := VendorUIContextCandidate{
			EntryPtr: entry,
			Kind:     uint32(gd.Process.ReadUInt(entry+0x04, Uint32)),
			ID:       int32(gd.Process.ReadUInt(entry+0x08, Uint32)),
			TargetID: int32(gd.Process.ReadUInt(entry+0x0C, Uint32)),
			State:    uint32(gd.Process.ReadUInt(entry+0x20, Uint32)),
			NextPtr:  uintptr(gd.Process.ReadUInt(entry+0x30, Uint64)),
		}
		if gd.vendorLooksLikeMode6Context(entry) {
			baseCandidate.Mode6Value = gd.vendorMode6ValueRPM(entry, itemPtr)
		}
		out.Candidates = append(out.Candidates, baseCandidate)
		out.recordBestUIContext(baseCandidate)

		for _, off := range []uintptr{0x00, 0x10, 0x18, 0x28, 0x38, 0x40, 0x48, 0x50, 0x58, 0x60} {
			ptr := uintptr(gd.Process.ReadUInt(entry+off, Uint64))
			if ptr == 0 || ptr == entry {
				continue
			}
			if !gd.vendorLooksLikeMode6Context(ptr) {
				continue
			}
			value := gd.vendorMode6ValueRPM(ptr, itemPtr)
			if value == 0 {
				continue
			}
			c := baseCandidate
			c.PointerOffset = off
			c.Pointer = ptr
			c.Mode6Value = value
			out.Candidates = append(out.Candidates, c)
			out.recordBestUIContext(c)
		}
	}
	return out
}

func (gd *GameReader) vendorLooksLikeMode6Context(ptr uintptr) bool {
	if ptr < 0x10000000000 || gd == nil || gd.Process == nil {
		return false
	}
	kind := uint32(gd.Process.ReadUInt(ptr, Uint32))
	if kind > 8 && kind != 0x1020304 {
		return false
	}
	statsListEx := uintptr(gd.Process.ReadUInt(ptr+0x88, Uint64))
	return statsListEx >= 0x10000000000
}

func (p *VendorUIContextProbe) recordBestUIContext(c VendorUIContextCandidate) {
	ptr := c.Pointer
	if ptr == 0 {
		ptr = c.EntryPtr
	}
	if c.Mode6Value == 0 {
		return
	}
	if p.BestContextPtr == 0 || c.Mode6Value > p.BestContextValue {
		p.BestContextPtr = ptr
		p.BestContextValue = c.Mode6Value
	}
}

func (gd *GameReader) vendorItemRecordPtrRPM(difficulty uint8, txtID uint32) (uintptr, error) {
	if difficulty < 1 || difficulty > 3 {
		return 0, fmt.Errorf("unexpected item difficulty byte %d for txtID=%d", difficulty, txtID)
	}
	base := gd.Process.ModuleBaseAddress()
	bucket := uintptr(gd.Process.ReadUInt(base+vendorDataBucketsRVA+uintptr(difficulty)*0x10, Uint64))
	if bucket == 0 {
		return 0, fmt.Errorf("item data bucket is null for difficulty=%d", difficulty)
	}
	count := uint32(gd.Process.ReadUInt(bucket+0x15A8, Uint32))
	if txtID >= count {
		return 0, fmt.Errorf("txtID=%d outside item record count=%d", txtID, count)
	}
	records := uintptr(gd.Process.ReadUInt(bucket+0x15A0, Uint64))
	if records == 0 {
		return 0, fmt.Errorf("item record base is null for difficulty=%d", difficulty)
	}
	return records + uintptr(txtID)*vendorItemRecordSize, nil
}

func (gd *GameReader) vendorMode6ValueRPM(merchantPtr, itemPtr uintptr) int32 {
	return gd.vendorMode6ProbeRPM("", merchantPtr, itemPtr, 0xCB).Value()
}

func (gd *GameReader) vendorMode6ProbeRPM(source string, contextPtr, itemPtr uintptr, statID uint32) VendorMode6Probe {
	out := VendorMode6Probe{
		Source:     source,
		ContextPtr: contextPtr,
		ItemPtr:    itemPtr,
		StatID:     statID,
	}
	if contextPtr == 0 || itemPtr == 0 || gd == nil || gd.Process == nil {
		out.PrimaryFailure = "empty_context_item_or_process"
		out.FallbackFailure = out.PrimaryFailure
		return out
	}
	value, handled, primary := gd.vendorModeValuePrimaryProbeRPM(contextPtr, itemPtr, statID)
	out.SrcOwner = primary.SrcOwner
	out.Src = primary.Src
	out.Record = primary.Record
	out.RecordKind = primary.RecordKind
	out.RecordTypeID = primary.RecordTypeID
	out.Difficulty = primary.Difficulty
	out.Bucket = primary.Bucket
	out.TypeCount = primary.TypeCount
	out.Stride = primary.Stride
	out.BitsetBase = primary.BitsetBase
	out.TypeFlagWord = primary.TypeFlagWord
	out.PrimaryHandled = handled
	out.PrimaryValue = value
	out.PrimaryFailure = primary.PrimaryFailure
	out.PrimaryStatsListEx = primary.PrimaryStatsListEx
	out.PrimaryStatsPtr = primary.PrimaryStatsPtr
	out.PrimaryStatCount = primary.PrimaryStatCount
	out.PrimaryMatchedStats = primary.PrimaryMatchedStats
	out.PrimaryCandidateStats = primary.PrimaryCandidateStats
	if handled {
		return out
	}

	best := int32(-0x80000000)
	entries, diag := gd.statListEntriesByIDDiag(contextPtr, statID, 0x20)
	out.FallbackStatsListEx = diag.StatsListEx
	out.FallbackStatsPtr = diag.StatsPtr
	out.FallbackStatCount = diag.StatCount
	out.FallbackMatchedStats = diag.Matched
	if diag.Failure != "" {
		out.FallbackFailure = diag.Failure
		return out
	}
	for _, entry := range entries {
		propEncoded := uint16(entry.key)
		propMode := propEncoded >> 14
		if propMode == 2 {
			continue
		}
		prop := uint32(propEncoded & 0x3FFF)
		if prop != 0 && !gd.vendorItemHasPropertyRPM(itemPtr, prop) {
			continue
		}
		out.FallbackCandidateStats++
		if entry.value > best {
			best = entry.value
		}
	}
	if best == int32(-0x80000000) {
		out.FallbackFailure = "fallback_no_matching_stat_entries"
		return out
	}
	out.FallbackValue = best
	return out
}

func (gd *GameReader) vendorModeValuePrimaryRPM(contextPtr, itemPtr uintptr, statID uint32) (int32, bool) {
	value, handled, _ := gd.vendorModeValuePrimaryProbeRPM(contextPtr, itemPtr, statID)
	return value, handled
}

func (gd *GameReader) vendorModeValuePrimaryProbeRPM(contextPtr, itemPtr uintptr, statID uint32) (int32, bool, VendorMode6Probe) {
	out := VendorMode6Probe{
		ContextPtr: contextPtr,
		ItemPtr:    itemPtr,
		StatID:     statID,
	}
	if contextPtr == 0 || itemPtr == 0 || gd == nil || gd.Process == nil {
		out.PrimaryFailure = "empty_context_item_or_process"
		return 0, false, out
	}
	srcOwner := uintptr(gd.Process.ReadUInt(contextPtr+0x100, Uint64))
	out.SrcOwner = srcOwner
	if srcOwner == 0 {
		out.PrimaryFailure = "context_src_owner_null"
		return 0, false, out
	}
	src := uintptr(gd.Process.ReadUInt(srcOwner+0x18, Uint64))
	out.Src = src
	if src == 0 {
		out.PrimaryFailure = "context_src_null"
		return 0, false, out
	}
	record := uintptr(gd.Process.ReadUInt(src, Uint64))
	out.Record = record
	if record == 0 {
		out.PrimaryFailure = "record_null"
		return 0, false, out
	}
	typeID := int32(int16(gd.Process.ReadUInt(record+0x38, Uint16)))
	out.RecordTypeID = typeID
	if typeID <= 0 {
		out.PrimaryFailure = "record_type_not_positive"
		return 0, false, out
	}
	out.RecordKind = uint32(gd.Process.ReadUInt(record+0x34, Uint8))
	if out.RecordKind != 2 {
		out.PrimaryFailure = "record_kind_not_2"
		return 0, false, out
	}
	difficulty := uint8(gd.Process.ReadUInt(contextPtr+vendorItemDifficultyOffset, Uint8))
	out.Difficulty = difficulty
	bucket, ok := gd.vendorDataBucket(difficulty)
	out.Bucket = bucket
	if !ok {
		out.PrimaryFailure = "data_bucket_missing"
		return 0, false, out
	}
	typeCount := int32(gd.Process.ReadUInt(bucket+0x1350, Uint32))
	out.TypeCount = typeCount
	if typeCount <= 0x30 || typeID >= typeCount {
		out.PrimaryFailure = "type_id_outside_type_count"
		return 0, false, out
	}
	stride := uint32(gd.Process.ReadUInt(bucket+0x1360, Uint32))
	bitsetBase := uintptr(gd.Process.ReadUInt(bucket+0x1368, Uint64))
	out.Stride = stride
	out.BitsetBase = bitsetBase
	if stride == 0 || bitsetBase == 0 {
		out.PrimaryFailure = "type_bitset_missing"
		return 0, false, out
	}
	flags := uint32(gd.Process.ReadUInt(bitsetBase+uintptr(uint32(typeID)*stride*4)+4, Uint32))
	out.TypeFlagWord = flags
	if flags&0x10000 == 0 {
		out.PrimaryFailure = "type_flag_0x10000_missing"
		return 0, false, out
	}

	best := int32(-0x80000000)
	entries, diag := gd.statListEntriesByIDDiag(contextPtr, statID, 0x20)
	out.PrimaryStatsListEx = diag.StatsListEx
	out.PrimaryStatsPtr = diag.StatsPtr
	out.PrimaryStatCount = diag.StatCount
	out.PrimaryMatchedStats = diag.Matched
	if diag.Failure != "" {
		out.PrimaryFailure = diag.Failure
		return 0, true, out
	}
	for _, entry := range entries {
		prop := uint32(uint16(entry.key))
		if prop != 0 && !gd.vendorItemHasPropertyRPM(itemPtr, prop) {
			continue
		}
		out.PrimaryCandidateStats++
		if entry.value > best {
			best = entry.value
		}
	}
	if best == int32(-0x80000000) {
		out.PrimaryFailure = "primary_no_matching_stat_entries"
		return 0, true, out
	}
	out.PrimaryValue = best
	return best, true, out
}

type statListEntry struct {
	key   uint32
	value int32
}

func (gd *GameReader) statListEntriesByID(unitPtr uintptr, statID uint32, limit int) []statListEntry {
	entries, _ := gd.statListEntriesByIDDiag(unitPtr, statID, limit)
	return entries
}

func (gd *GameReader) statListEntriesByIDDiag(unitPtr uintptr, statID uint32, limit int) ([]statListEntry, statListDiag) {
	var diag statListDiag
	if unitPtr == 0 || gd == nil || gd.Process == nil {
		diag.Failure = "empty_unit_or_process"
		return nil, diag
	}
	statsListEx := uintptr(gd.Process.ReadUInt(unitPtr+0x88, Uint64))
	diag.StatsListEx = statsListEx
	if statsListEx == 0 {
		diag.Failure = "stats_list_ex_null"
		return nil, diag
	}
	if int32(gd.Process.ReadUInt(statsListEx+0x1C, Uint32)) >= 0 {
		diag.Failure = "stats_list_ex_inline_or_unexpected"
		return nil, diag
	}
	statsList := statsListEx + 0x78
	statPtr := uintptr(gd.Process.ReadUInt(statsList+0x30, Uint64))
	statCount := int(gd.Process.ReadUInt(statsList+0x38, Uint64))
	diag.StatsPtr = statPtr
	diag.StatCount = statCount
	if statPtr == 0 || statCount <= 0 {
		diag.Failure = "stats_array_empty"
		return nil, diag
	}

	entries := make([]statListEntry, 0, 4)
	for idx := 0; idx < statCount && len(entries) < limit; idx++ {
		diag.Scanned++
		entryPtr := statPtr + uintptr(idx*8)
		key := uint32(gd.Process.ReadUInt(entryPtr, Uint32))
		if key>>16 != statID {
			continue
		}
		value := int32(gd.Process.ReadUInt(entryPtr+4, Uint32))
		entries = append(entries, statListEntry{key: key, value: value})
	}
	diag.Matched = len(entries)
	if len(entries) == 0 {
		diag.Failure = "stat_id_not_found"
	}
	return entries, diag
}

func (gd *GameReader) vendorItemHasPropertyRPM(itemPtr uintptr, prop uint32) bool {
	if itemPtr == 0 || gd == nil || gd.Process == nil {
		return false
	}
	difficulty := uint8(gd.Process.ReadUInt(itemPtr+vendorItemDifficultyOffset, Uint8))
	bucket, ok := gd.vendorDataBucket(difficulty)
	if !ok {
		return false
	}
	typeCount := uint32(gd.Process.ReadUInt(bucket+0x1350, Uint32))
	if prop >= typeCount {
		return false
	}
	txtID := uint32(gd.Process.ReadUInt(itemPtr+0x04, Uint32))
	record, err := gd.vendorItemRecordPtrRPM(difficulty, txtID)
	if err != nil || record == 0 {
		return false
	}
	typeA := int16(gd.Process.ReadUInt(record+vendorItemTypeAOffset, Uint16))
	if typeA < 0 {
		return false
	}
	if gd.vendorTypeHasProperty(bucket, uint32(typeA), prop) {
		return true
	}
	typeB := int16(gd.Process.ReadUInt(record+vendorItemTypeBOffset, Uint16))
	if typeB > 0 && uint32(typeB) < typeCount {
		return gd.vendorTypeHasProperty(bucket, uint32(typeB), prop)
	}
	return false
}

func (gd *GameReader) vendorDataBucket(difficulty uint8) (uintptr, bool) {
	if gd == nil || gd.Process == nil || difficulty < 1 || difficulty > 3 {
		return 0, false
	}
	base := gd.Process.ModuleBaseAddress()
	if base == 0 {
		return 0, false
	}
	bucket := uintptr(gd.Process.ReadUInt(base+vendorDataBucketsRVA+uintptr(difficulty)*0x10, Uint64))
	return bucket, bucket != 0
}

func (gd *GameReader) vendorTypeHasProperty(bucket uintptr, typeID uint32, prop uint32) bool {
	stride := uint32(gd.Process.ReadUInt(bucket+0x1360, Uint32))
	bitsetBase := uintptr(gd.Process.ReadUInt(bucket+0x1368, Uint64))
	if stride == 0 || bitsetBase == 0 {
		return false
	}
	word := uint32(gd.Process.ReadUInt(bitsetBase+uintptr((typeID*stride+(prop>>5))*4), Uint32))
	return word&(uint32(1)<<(prop&0x1F)) != 0
}

func (gd *GameReader) findUnitPtrByID(unitType int, unitID data.UnitID) uintptr {
	if gd == nil || gd.Process == nil {
		return 0
	}
	base := gd.Process.ModuleBaseAddress()
	if base == 0 {
		return 0
	}
	tableBase := base + gd.offset.UnitTable + uintptr(unitType*1024)
	table := gd.reader.ReadBytesFromMemory(tableBase, 128*8)
	for slot := 0; slot < 128; slot++ {
		ptr := uintptr(ReadUIntFromBuffer(table, uint(slot*8), Uint64))
		for ptr != 0 {
			got := data.UnitID(gd.Process.ReadUInt(ptr+0x08, Uint32))
			if got == unitID {
				return ptr
			}
			ptr = uintptr(gd.Process.ReadUInt(ptr+0x158, Uint64))
		}
	}
	return 0
}

func (gd *GameReader) FindUnitPtrByID(unitType int, unitID data.UnitID) uintptr {
	return gd.findUnitPtrByID(unitType, unitID)
}

func vendorPriceFromRate(value int64, rate uint32, discounted bool) uint32 {
	if rate == 0 {
		return 0
	}
	percent := (value * int64(rate)) / 100
	if discounted {
		percent -= 10
	}
	price := int64(rate) + percent
	if price < 0 {
		return 0
	}
	if price > int64(^uint32(0)) {
		return ^uint32(0)
	}
	return uint32(price)
}
