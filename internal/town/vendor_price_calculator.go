package town

import (
	"math"

	"local/internal/svc/internal/context"
	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/item"
)

const (
	vendorSellNumerator    = 512
	vendorPriceDenominator = 1024
)

type vendorComputedPrice struct {
	Price      uint32
	BaseCost   int64
	TotalMult  int64
	TotalAdd   int64
	Multiplier int
	Source     string
}

func vendorComputedSellPrice(ctx *context.Status, i data.Item) (vendorComputedPrice, bool) {
	if ctx == nil || !ctx.Data.OpenMenus.NPCShop {
		return vendorComputedPrice{}, false
	}
	base := int64(i.Desc().BaseCost)
	if liveBase, ok := vendorGameBaseCost(i); ok {
		base = int64(liveBase)
	}
	if base <= 0 {
		if generatedBase, ok := vendorItemBaseCostByCode[i.Desc().Code]; ok {
			base = int64(generatedBase)
		}
	}
	if base <= 0 {
		return vendorComputedPrice{}, false
	}

	rawBase := base
	if i.Type().Throwable {
		qty := i.StackedQuantity
		if qty <= 0 {
			qty = vendorItemMaxStackByCode[i.Desc().Code]
		}
		if qty > 0 {
			base *= int64(qty)
		}
	}

	var mult, add int64
	source := "base"
	switch i.Quality {
	case item.QualityUnique:
		mod, ok := vendorUniqueCostMods[int(i.UniqueSetID)]
		if !ok {
			return vendorComputedPrice{}, false
		}
		mult += int64(mod.Mult)
		add += int64(mod.Add)
		source = "unique"
	case item.QualitySet:
		mod, ok := vendorSetCostMods[int(i.UniqueSetID)]
		if !ok {
			return vendorComputedPrice{}, false
		}
		mult += int64(mod.Mult)
		add += int64(mod.Add)
		source = "set"
	case item.QualityMagic, item.QualityRare, item.QualityCrafted, item.QualitySuperior, item.QualityNormal:
		statMult, statAdd := vendorStatCost(i)
		mult += statMult
		add += statAdd
		source = "magic_stats"
	case item.QualityLowQuality:
		base /= 2
		source = "low_quality"
	default:
		return vendorComputedPrice{}, false
	}

	modBase := base
	if i.Type().Throwable && rawBase > 0 {
		modBase = rawBase
	}
	cost := base + (modBase*mult)/vendorPriceDenominator + add
	if i.Ethereal {
		cost = (cost * 3) / 8
	}
	if cost < 1 {
		cost = 1
	}
	price := (cost * vendorSellNumerator) / vendorPriceDenominator
	if price < 1 {
		price = 1
	}
	if price > math.MaxUint32 {
		price = math.MaxUint32
	}
	return vendorComputedPrice{
		Price:      uint32(price),
		BaseCost:   base,
		TotalMult:  mult,
		TotalAdd:   add,
		Multiplier: vendorSellNumerator,
		Source:     source,
	}, true
}

func vendorStatCost(i data.Item) (int64, int64) {
	seen := make(map[[2]int]struct{}, len(i.Stats)+len(i.BaseStats))
	var mult, add int64
	for _, st := range i.Stats {
		key := [2]int{int(st.ID), st.Layer}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		mod, ok := vendorItemStatCostMods[int(st.ID)]
		if !ok || st.Value == 0 {
			continue
		}
		mult += int64(st.Value) * int64(mod.Mult)
		add += int64(mod.Add)
	}
	for _, st := range i.BaseStats {
		key := [2]int{int(st.ID), st.Layer}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		mod, ok := vendorItemStatCostMods[int(st.ID)]
		if !ok || st.Value == 0 {
			continue
		}
		mult += int64(st.Value) * int64(mod.Mult)
		add += int64(mod.Add)
	}
	return mult, add
}

func vendorAffixCost(i data.Item) (int64, int64) {
	var mult, add int64
	addMod := func(mod vendorPriceCostMod) {
		mult += int64(mod.Mult)
		add += int64(mod.Add)
	}
	for _, id := range i.Affixes.Magic.Prefixes {
		if id == 0 {
			continue
		}
		if mod, ok := vendorMagicPrefixCostMods[int(id)]; ok {
			addMod(mod)
		}
	}
	for _, id := range i.Affixes.Magic.Suffixes {
		if id == 0 {
			continue
		}
		if mod, ok := vendorMagicSuffixCostMods[int(id)]; ok {
			addMod(mod)
		}
	}
	return mult, add
}
