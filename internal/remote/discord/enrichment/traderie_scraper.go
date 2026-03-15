package enrichment

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"strings"
)

// TraderiePrice holds price info from Traderie.
type TraderiePrice struct {
	ItemName    string
	AvgPrice    string // e.g. "Vex", "2 Ist", "Jah + Ber"
	NumListings int
	Source      string
}

// TraderieScraper fetches item prices from Traderie.com via FlareSolverr.
type TraderieScraper struct {
	flare  *FlareSolverr
	logger *slog.Logger
}

// NewTraderieScraper creates a Traderie price scraper.
func NewTraderieScraper(flare *FlareSolverr, logger *slog.Logger) *TraderieScraper {
	return &TraderieScraper{
		flare:  flare,
		logger: logger,
	}
}

// Rune names for price extraction (ordered by value, high→low)
var runeNames = []string{
	"Zod", "Cham", "Jah", "Ber", "Sur", "Lo", "Ohm", "Vex", "Gul", "Ist", "Mal", "Um", "Pul",
	"Lem", "Fal", "Ko", "Lum", "Io", "Hel", "Dol", "Sol",
}

// traderiePricePattern matches rune-based prices: "Vex", "1 Vex", "2x Ist", "3 Ist"
var traderiePricePattern = regexp.MustCompile(`(?i)(\d*)\s*x?\s*\b(` + strings.Join(runeNames, "|") + `)\b`)

// GetPrice fetches the current price for an item from Traderie via FlareSolverr.
func (s *TraderieScraper) GetPrice(ctx context.Context, itemName string) (*TraderiePrice, error) {
	if s.flare == nil {
		return nil, fmt.Errorf("FlareSolverr not configured")
	}

	// Step 1: Search for product (use proper URL encoding, not slugification)
	searchURL := fmt.Sprintf("https://traderie.com/diablo2resurrected/products?search=%s", url.QueryEscape(itemName))

	html, err := s.flare.GetPage(ctx, searchURL)
	if err != nil {
		return nil, fmt.Errorf("traderie search failed: %w", err)
	}

	// Step 2: Find product link
	productURL := s.findProductLink(html, itemName)
	if productURL == "" {
		sample := html
		if len(sample) > 2000 {
			sample = sample[:2000]
		}
		s.logger.Warn("traderie: no product link found in search HTML",
			slog.String("item", itemName),
			slog.Int("htmlLen", len(html)),
			slog.String("htmlSample", sample),
		)
		return nil, fmt.Errorf("no traderie product found for %s", itemName)
	}
	s.logger.Debug("traderie: found product link", slog.String("item", itemName), slog.String("url", productURL))

	// DEBUG: dump search HTML to file for inspection (temporary)
	_ = os.WriteFile("logs/traderie_search_debug.html", []byte(html), 0644)

	// Step 3: Fetch product page with listings
	html, err = s.flare.GetPage(ctx, productURL)
	if err != nil {
		return nil, fmt.Errorf("traderie product page failed: %w", err)
	}

	// DEBUG: dump product HTML to file for inspection (temporary)
	_ = os.WriteFile("logs/traderie_product_debug.html", []byte(html), 0644)

	return s.parsePrices(html, itemName)
}

func (s *TraderieScraper) findProductLink(html string, itemName string) string {
	// Try multiple link patterns — Traderie may use numeric IDs or slugs
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`href="(/diablo2resurrected/product/\d+)"`),
		regexp.MustCompile(`href="(/diablo2resurrected/product/[^"]+)"`),
		regexp.MustCompile(`"/diablo2resurrected/product/([^"]+)"`),
	}

	target := strings.ToLower(itemName)
	for _, linkPattern := range patterns {
		matches := linkPattern.FindAllStringSubmatch(html, 20)
		for _, m := range matches {
			path := m[1]
			// Ensure path starts with /
			if !strings.HasPrefix(path, "/") {
				path = "/diablo2resurrected/product/" + path
			}
			linkIdx := strings.Index(html, m[0])
			if linkIdx >= 0 {
				start := linkIdx - 300
				if start < 0 {
					start = 0
				}
				end := linkIdx + 300
				if end > len(html) {
					end = len(html)
				}
				surrounding := strings.ToLower(html[start:end])
				if strings.Contains(surrounding, target) {
					return "https://traderie.com" + path
				}
			}
		}
		// Fallback: return first match from this pattern
		if len(matches) > 0 {
			path := matches[0][1]
			if !strings.HasPrefix(path, "/") {
				path = "/diablo2resurrected/product/" + path
			}
			return "https://traderie.com" + path
		}
	}

	return ""
}

func (s *TraderieScraper) parsePrices(html string, itemName string) (*TraderiePrice, error) {
	matches := traderiePricePattern.FindAllStringSubmatch(html, 30)
	if len(matches) == 0 {
		sample := html
		if len(sample) > 2000 {
			sample = sample[:2000]
		}
		s.logger.Warn("traderie: no rune prices found in product HTML",
			slog.String("item", itemName),
			slog.Int("htmlLen", len(html)),
			slog.String("htmlSample", sample),
		)
		return nil, fmt.Errorf("no rune prices found on traderie for %s", itemName)
	}
	s.logger.Debug("traderie: found rune price matches", slog.String("item", itemName), slog.Int("matchCount", len(matches)))

	priceCounts := make(map[string]int)
	for _, m := range matches {
		qty := m[1]
		runeName := m[2]
		var priceStr string
		if qty == "" || qty == "1" {
			priceStr = runeName
		} else {
			priceStr = fmt.Sprintf("%s %s", qty, runeName)
		}
		priceCounts[priceStr]++
	}

	var bestPrice string
	bestCount := 0
	for price, count := range priceCounts {
		if count > bestCount {
			bestCount = count
			bestPrice = price
		}
	}

	return &TraderiePrice{
		ItemName:    itemName,
		AvgPrice:    bestPrice,
		NumListings: len(matches),
		Source:      "traderie.com",
	}, nil
}

// FormatTraderiePrice returns a formatted price string for Discord display.
func FormatTraderiePrice(p *TraderiePrice) string {
	if p == nil {
		return ""
	}
	return fmt.Sprintf("%s (%d listings)", p.AvgPrice, p.NumListings)
}
