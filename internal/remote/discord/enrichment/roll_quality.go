package enrichment

import (
	"fmt"
	"math"

	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/stat"
)

// RollResult holds the quality assessment for a single stat roll.
type RollResult struct {
	StatName   string
	Value      int
	Min        int
	Max        int
	Percentile float64 // 0.0–100.0
	IsPerfect  bool
}

// RollReport is the full quality assessment for an item.
type RollReport struct {
	ItemName           string
	Rolls              []RollResult
	OverallPercentile  float64 // weighted average across all variable stats
	HasKnownRanges     bool
	PerfectRollCount   int
	TotalVariableStats int
}

// CalculateRollQuality compares an item's stats against known unique ranges.
func CalculateRollQuality(item data.Item) RollReport {
	report := RollReport{
		ItemName: item.IdentifiedName,
	}

	ranges, ok := UniqueStatRanges[item.IdentifiedName]
	if !ok || len(ranges) == 0 {
		return report
	}

	report.HasKnownRanges = true

	// Build a map of item stats for quick lookup.
	// Some stats appear multiple times (layers), use highest value.
	statValues := make(map[stat.ID]int)
	for _, s := range item.Stats {
		if existing, exists := statValues[s.ID]; !exists || s.Value > existing {
			statValues[s.ID] = s.Value
		}
	}

	var totalPct float64
	var count int

	for _, sr := range ranges {
		if sr.Min == sr.Max {
			// Fixed stat, no variance — skip from percentile calculation
			continue
		}

		value, found := statValues[sr.StatID]
		if !found {
			continue
		}

		pct := calcPercentile(value, sr.Min, sr.Max)
		roll := RollResult{
			StatName:   sr.StatName,
			Value:      value,
			Min:        sr.Min,
			Max:        sr.Max,
			Percentile: pct,
			IsPerfect:  value >= sr.Max,
		}

		report.Rolls = append(report.Rolls, roll)
		report.TotalVariableStats++
		totalPct += pct
		count++

		if roll.IsPerfect {
			report.PerfectRollCount++
		}
	}

	if count > 0 {
		report.OverallPercentile = totalPct / float64(count)
	}

	return report
}

// calcPercentile returns 0–100 for where value falls in [min, max].
func calcPercentile(value, min, max int) float64 {
	if max == min {
		return 100.0
	}
	pct := float64(value-min) / float64(max-min) * 100.0
	return math.Max(0, math.Min(100, pct))
}

// FormatRollEmoji returns a visual indicator for roll quality.
func FormatRollEmoji(pct float64) string {
	switch {
	case pct >= 100:
		return "🟢 PERFECT"
	case pct >= 80:
		return "🟢"
	case pct >= 50:
		return "🟡"
	case pct >= 20:
		return "🟠"
	default:
		return "🔴"
	}
}

// FormatRollBar returns a visual progress bar for roll quality.
func FormatRollBar(pct float64) string {
	filled := int(math.Round(pct / 10))
	if filled > 10 {
		filled = 10
	}
	if filled < 0 {
		filled = 0
	}
	bar := ""
	for i := 0; i < 10; i++ {
		if i < filled {
			bar += "█"
		} else {
			bar += "░"
		}
	}
	return bar
}

// FormatRollLine returns a formatted line for a single stat roll.
func FormatRollLine(r RollResult) string {
	bar := FormatRollBar(r.Percentile)
	emoji := FormatRollEmoji(r.Percentile)
	return fmt.Sprintf("%s `%s` %s: **%d** (%d–%d) %.0f%%",
		emoji, bar, r.StatName, r.Value, r.Min, r.Max, r.Percentile)
}
