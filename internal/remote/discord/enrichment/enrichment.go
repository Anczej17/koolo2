package enrichment

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/item"
	"local/internal/svc/internal/remote/discord/discordembed"
)

// Service orchestrates async item enrichment (roll quality + prices).
type Service struct {
	d2jspScraper    *D2JSPScraper
	traderieScraper *TraderieScraper
	logger          *slog.Logger
}

// NewService creates an enrichment service.
func NewService(d2jsp *D2JSPScraper, traderie *TraderieScraper, logger *slog.Logger) *Service {
	return &Service{
		d2jspScraper:    d2jsp,
		traderieScraper: traderie,
		logger:          logger,
	}
}

// EnrichResult holds all enrichment data for an item.
type EnrichResult struct {
	RollReport    *RollReport
	D2JSPPrice    *D2JSPPrice
	TraderiePrice *TraderiePrice
}

// Enrich gathers all enrichment data for an item. Safe to call async.
func (s *Service) Enrich(ctx context.Context, itm data.Item, statsText string) *EnrichResult {
	result := &EnrichResult{}

	itemName := itm.IdentifiedName
	if itemName == "" {
		itemName = string(itm.Name)
	}

	// 1. Roll quality (instant, no I/O)
	if itm.Quality == item.QualityUnique && itm.IdentifiedName != "" {
		report := CalculateRollQuality(itm)
		if report.HasKnownRanges {
			result.RollReport = &report
		}
	}

	// 2. D2JSP price (network, with static DB fallback)
	if s.d2jspScraper != nil {
		priceCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
		price, err := s.d2jspScraper.GetPrice(priceCtx, itemName)
		cancel()
		if err != nil {
			s.logger.Debug("d2jsp price failed", slog.String("item", itemName), slog.Any("error", err))
		} else {
			result.D2JSPPrice = price
		}
	}

	// Apply roll quality multiplier to d2jsp price
	if result.D2JSPPrice != nil && result.RollReport != nil {
		mult := rollMultiplier(result.RollReport.OverallPercentile)
		if mult != 1.0 {
			result.D2JSPPrice.MinPrice = int(float64(result.D2JSPPrice.MinPrice) * mult)
			result.D2JSPPrice.MaxPrice = int(float64(result.D2JSPPrice.MaxPrice) * mult)
			result.D2JSPPrice.AvgPrice = int(float64(result.D2JSPPrice.AvgPrice) * mult)
		}
	}

	// 3. Traderie price (network)
	if s.traderieScraper != nil {
		trCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		price, err := s.traderieScraper.GetPrice(trCtx, itemName)
		cancel()
		if err != nil {
			s.logger.Debug("traderie price failed", slog.String("item", itemName), slog.Any("error", err))
		} else {
			result.TraderiePrice = price
		}
	}

	return result
}

// runeEmojis maps rune names to colored circle emojis representing rune tier.
var runeEmojis = map[string]string{
	"Zod": "🟡", "Cham": "🟡", "Jah": "🟡", "Ber": "🟡",
	"Sur": "🟠", "Lo": "🟠", "Ohm": "🟠",
	"Vex": "🔵", "Gul": "🔵", "Ist": "🔵",
	"Mal": "🟢", "Um": "🟢", "Pul": "🟢",
	"Lem": "⚪", "Fal": "⚪", "Ko": "⚪",
	"Lum": "⚪", "Io": "⚪", "Hel": "⚪",
	"Dol": "⚪", "Sol": "⚪",
}

// toFancyText converts a string to Unicode mathematical bold (fancy Discord text).
func toFancyText(s string) string {
	var b strings.Builder
	for _, c := range s {
		switch {
		case c >= 'A' && c <= 'Z':
			b.WriteRune(rune(0x1D400 + (c - 'A'))) // 𝐀-𝐙
		case c >= 'a' && c <= 'z':
			b.WriteRune(rune(0x1D41A + (c - 'a'))) // 𝐚-𝐳
		case c >= '0' && c <= '9':
			b.WriteRune(rune(0x1D7CE + (c - '0'))) // 𝟎-𝟗
		default:
			b.WriteRune(c)
		}
	}
	return b.String()
}

// ExtractRuneName returns the primary rune name from a Traderie price string.
// E.g. "Ist" → "Ist", "2 Vex" → "Vex", "3 Ist" → "Ist"
func ExtractRuneName(priceStr string) string {
	for _, rune := range runeNames {
		if strings.Contains(priceStr, rune) {
			return rune
		}
	}
	return ""
}

// BuildEnrichedEmbed creates a rich Discord embed with roll quality and prices.
func BuildEnrichedEmbed(baseDescription string, baseColor int, result *EnrichResult) *discordembed.Embed {
	embed := &discordembed.Embed{
		Color: baseColor,
	}

	var desc strings.Builder
	desc.WriteString(baseDescription)

	// Add roll quality section
	if result.RollReport != nil && result.RollReport.HasKnownRanges && len(result.RollReport.Rolls) > 0 {
		desc.WriteString("\n\n**📊 Roll Quality**\n")
		for _, r := range result.RollReport.Rolls {
			desc.WriteString(FormatRollLine(r))
			desc.WriteString("\n")
		}
		overallEmoji := FormatRollEmoji(result.RollReport.OverallPercentile)
		desc.WriteString(fmt.Sprintf("\n%s Overall: **%.0f%%**", overallEmoji, result.RollReport.OverallPercentile))
		if result.RollReport.PerfectRollCount > 0 {
			desc.WriteString(fmt.Sprintf(" (%d perfect)", result.RollReport.PerfectRollCount))
		}
	}

	// Add pricing section
	if result.D2JSPPrice != nil || result.TraderiePrice != nil {
		desc.WriteString("\n\n**💰 Market Prices**\n")
		if result.D2JSPPrice != nil {
			src := result.D2JSPPrice.Source
			desc.WriteString(fmt.Sprintf("D2JSP: %s", FormatPrice(result.D2JSPPrice)))
			if result.D2JSPPrice.NumResults > 0 {
				desc.WriteString(fmt.Sprintf(" (%d trades)", result.D2JSPPrice.NumResults))
			}
			if strings.Contains(src, "static") {
				desc.WriteString(" ⚠️")
			}
			desc.WriteString("\n")
		}
		if result.TraderiePrice != nil {
			runeName := ExtractRuneName(result.TraderiePrice.AvgPrice)
			if runeName != "" {
				fgValue := RuneFGValue(runeName)
				if fgValue > 0 {
					desc.WriteString(fmt.Sprintf("Traderie: %s (%d fg)\n", result.TraderiePrice.AvgPrice, fgValue))
				} else {
					desc.WriteString(fmt.Sprintf("Traderie: %s\n", result.TraderiePrice.AvgPrice))
				}
			} else {
				desc.WriteString(fmt.Sprintf("Traderie: %s\n", result.TraderiePrice.AvgPrice))
			}
		}
	}

	embed.Description = desc.String()
	return embed
}

// runeFGValues maps rune names to their approximate fg value on d2jsp.
var runeFGValues = map[string]int{
	"Zod": 800, "Cham": 400, "Jah": 2000, "Ber": 2200,
	"Sur": 1000, "Lo": 900, "Ohm": 450,
	"Vex": 400, "Gul": 175, "Ist": 125,
	"Mal": 60, "Um": 40, "Pul": 20,
	"Lem": 8, "Fal": 5, "Ko": 3,
	"Lum": 2, "Io": 1, "Hel": 1,
	"Dol": 1, "Sol": 1,
}

// RuneFGValue returns the approximate fg value for a rune name.
func RuneFGValue(runeName string) int {
	return runeFGValues[runeName]
}

// rollMultiplier returns a price multiplier based on roll quality.
// 0-80% = baseline (×1.0), 80-95% = ×1.2, 95-100% = ×1.5.
func rollMultiplier(percentile float64) float64 {
	switch {
	case percentile >= 95:
		return 1.5
	case percentile >= 80:
		return 1.2
	default:
		return 1.0
	}
}
