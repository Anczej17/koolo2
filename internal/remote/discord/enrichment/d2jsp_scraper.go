package enrichment

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// D2JSPPrice holds price info for an item.
type D2JSPPrice struct {
	ItemName   string
	MinPrice   int
	MaxPrice   int
	AvgPrice   int
	Currency   string // "fg" (forum gold)
	Source     string
	NumResults int
}

// D2JSPScraper scrapes d2jsp forum for fg prices via FlareSolverr.
type D2JSPScraper struct {
	realm   string
	cookies []FlareCookie // session cookies from Chrome (injected into FlareSolverr)
	flare   *FlareSolverr
	logger  *slog.Logger

	// Rate limiter — d2jsp has delays between actions
	searchMu     sync.Mutex
	lastSearchAt time.Time
}

// NewD2JSPScraper creates a d2jsp price scraper for the given realm.
// cookieStr is a raw cookie header from Chrome DevTools ("name1=val1; name2=val2").
func NewD2JSPScraper(realm string, flare *FlareSolverr, logger *slog.Logger, cookieStr string) *D2JSPScraper {
	if realm == "" {
		realm = "SC NL"
	}
	cookies := ParseCookieString(cookieStr, ".d2jsp.org")
	if len(cookies) > 0 {
		logger.Info("d2jsp: loaded session cookies from config", slog.Int("count", len(cookies)))
	} else if cookieStr != "" {
		logger.Warn("d2jsp: cookie string provided but no valid cookies parsed")
	}
	return &D2JSPScraper{
		realm:   realm,
		cookies: cookies,
		flare:   flare,
		logger:  logger,
	}
}

// GetPrice tries to scrape live d2jsp prices via FlareSolverr + cookies, falls back to static database.
func (s *D2JSPScraper) GetPrice(ctx context.Context, itemName string) (*D2JSPPrice, error) {
	if s.flare != nil && len(s.cookies) > 0 {
		price, err := s.scrapeLive(ctx, itemName)
		if err == nil && price != nil {
			s.logger.Info("d2jsp live price found", slog.String("item", itemName), slog.Int("avg", price.AvgPrice))
			return price, nil
		}
		if err != nil {
			s.logger.Warn("d2jsp live scrape failed, using static DB", slog.String("item", itemName), slog.Any("error", err))
		}
	}

	return s.getStaticPrice(itemName)
}

// scrapeLive scrapes d2jsp search using a FlareSolverr persistent session.
// Flow: (1) create session, (2) visit d2jsp with cookies to auth + bypass CF,
// (3) wait for rate limit, (4) GET search with retry on flood.
func (s *D2JSPScraper) scrapeLive(ctx context.Context, itemName string) (*D2JSPPrice, error) {
	// Rate limit: one search at a time, minimum 20s between searches.
	// The mutex ensures only one goroutine talks to d2jsp at a time.
	s.searchMu.Lock()
	defer s.searchMu.Unlock()

	sinceLastSearch := time.Since(s.lastSearchAt)
	if sinceLastSearch < 20*time.Second {
		waitTime := 20*time.Second - sinceLastSearch
		s.logger.Info("d2jsp: rate limiter — waiting", slog.Duration("wait", waitTime))
		time.Sleep(waitTime)
	}

	// Direct search via GET with cookies — no session needed.
	// FlareSolverr sessions cause flood detection because the persistent browser
	// accumulates page load history. Sessionless requests use a fresh browser each time.
	forumID := s.getForumID()
	searchURL := fmt.Sprintf("https://forums.d2jsp.org/search.php?c=7&f=%d&t=0&stext=%s",
		forumID, url.QueryEscape(itemName))

	var html string
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		s.logger.Info("d2jsp: searching", slog.String("item", itemName), slog.Int("attempt", attempt+1))

		html, err = s.flare.GetPageWithCookies(ctx, searchURL, s.cookies)
		if err != nil {
			return nil, fmt.Errorf("d2jsp search failed: %w", err)
		}

		htmlLower := strings.ToLower(html)

		// Check for search flood — retry after delay
		if strings.Contains(htmlLower, "search flood") {
			s.logger.Warn("d2jsp: search flood detected, waiting 25s before retry", slog.Int("attempt", attempt+1))
			time.Sleep(25 * time.Second)
			continue
		}

		// Check if auth was lost (cookies expired)
		if !strings.Contains(htmlLower, "log out") && !strings.Contains(htmlLower, "logout") {
			s.logger.Warn("d2jsp: not logged in — cookies may have expired")
			return nil, fmt.Errorf("d2jsp cookies expired — update d2jspCookie in config from Chrome DevTools")
		}

		break
	}

	s.lastSearchAt = time.Now()
	s.logger.Info("d2jsp: got search response", slog.String("item", itemName), slog.Int("htmlLen", len(html)))
	_ = os.WriteFile("logs/d2jsp_search_debug.html", []byte(html), 0644)

	// Final flood check after all retries
	if strings.Contains(strings.ToLower(html), "search flood") {
		return nil, fmt.Errorf("d2jsp search flood — rate limited after 3 retries")
	}

	// Verify response looks like search results (not an error page)
	htmlLower := strings.ToLower(html)
	if !strings.Contains(htmlLower, "search") && len(html) < 2000 {
		s.logger.Warn("d2jsp: suspicious search response — too short",
			slog.Int("htmlLen", len(html)),
			slog.String("sample", truncate(html, 500)))
		return nil, fmt.Errorf("d2jsp: unexpected search response (len=%d)", len(html))
	}

	return s.parseFGPrices(html, itemName)
}

// fgPattern matches common fg price patterns in trade posts.
// Handles: "30 fg", "30fg", "30</span> fg", "30 FG", "80 Fgs" etc.
var fgPattern = regexp.MustCompile(`(?i)(\d+)\s*(?:<[^>]*>\s*)*fgs?\b`)

func (s *D2JSPScraper) parseFGPrices(html string, itemName string) (*D2JSPPrice, error) {
	matches := fgPattern.FindAllStringSubmatch(html, 50)
	if len(matches) == 0 {
		s.logger.Warn("d2jsp: no fg prices found in HTML",
			slog.String("item", itemName),
			slog.Int("htmlLen", len(html)),
			slog.String("htmlSample", truncate(html, 2000)),
		)
		return nil, fmt.Errorf("no fg prices found in search results")
	}
	s.logger.Info("d2jsp: found fg matches", slog.String("item", itemName), slog.Int("matchCount", len(matches)))

	var prices []int
	for _, m := range matches {
		price, err := strconv.Atoi(m[1])
		if err != nil || price <= 0 || price > 100000 {
			continue
		}
		prices = append(prices, price)
	}

	if len(prices) == 0 {
		return nil, fmt.Errorf("no valid fg prices found")
	}

	prices = removeOutliers(prices)

	minP, maxP, sum := prices[0], prices[0], 0
	for _, p := range prices {
		if p < minP {
			minP = p
		}
		if p > maxP {
			maxP = p
		}
		sum += p
	}
	avgP := sum / len(prices)

	return &D2JSPPrice{
		ItemName:   itemName,
		MinPrice:   minP,
		MaxPrice:   maxP,
		AvgPrice:   avgP,
		Currency:   "fg",
		Source:     "d2jsp live",
		NumResults: len(prices),
	}, nil
}


func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// removeOutliers filters prices more than 5x or less than 0.2x the median.
func removeOutliers(prices []int) []int {
	if len(prices) < 3 {
		return prices
	}

	sorted := make([]int, len(prices))
	copy(sorted, prices)
	for i := range sorted {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j] < sorted[i] {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	median := sorted[len(sorted)/2]
	if median == 0 {
		return prices
	}

	var filtered []int
	for _, p := range prices {
		ratio := float64(p) / float64(median)
		if ratio >= 0.2 && ratio <= 5.0 {
			filtered = append(filtered, p)
		}
	}

	if len(filtered) == 0 {
		return prices
	}
	return filtered
}

func (s *D2JSPScraper) getForumID() int {
	realm := strings.ToLower(s.realm)
	switch {
	case strings.Contains(realm, "hc") && strings.Contains(realm, "ladder"):
		return 272
	case strings.Contains(realm, "hc"):
		return 269
	case strings.Contains(realm, "ladder"):
		return 271
	default:
		return 268 // SC NL (RotW)
	}
}

func (s *D2JSPScraper) getStaticPrice(itemName string) (*D2JSPPrice, error) {
	key := strings.ToLower(strings.TrimSpace(itemName))

	entry, ok := d2jspPriceDB[key]
	if !ok {
		return nil, fmt.Errorf("no price data for %s", itemName)
	}

	multiplier := 1.0
	realm := strings.ToLower(s.realm)
	switch {
	case strings.Contains(realm, "hc") && strings.Contains(realm, "ladder"):
		multiplier = 2.5
	case strings.Contains(realm, "hc"):
		multiplier = 2.0
	case strings.Contains(realm, "ladder"):
		multiplier = 1.5
	}

	return &D2JSPPrice{
		ItemName: itemName,
		MinPrice: int(float64(entry.min) * multiplier),
		MaxPrice: int(float64(entry.max) * multiplier),
		AvgPrice: int(float64(entry.avg) * multiplier),
		Currency: "fg",
		Source:   fmt.Sprintf("static DB (%s)", s.realm),
	}, nil
}

// FormatPrice returns a formatted price string for Discord display.
func FormatPrice(p *D2JSPPrice) string {
	if p == nil {
		return ""
	}
	if p.MinPrice == p.MaxPrice {
		return fmt.Sprintf("~%d fg", p.AvgPrice)
	}
	return fmt.Sprintf("%d–%d fg (avg: %d fg)", p.MinPrice, p.MaxPrice, p.AvgPrice)
}

type priceEntry struct {
	min, max, avg int
}

// d2jspPriceDB fallback prices for when live scraping fails.
var d2jspPriceDB = map[string]priceEntry{
	"harlequin crest":          {300, 600, 400},
	"griffon's eye":            {3000, 15000, 7000},
	"nightwing's veil":         {500, 3000, 1200},
	"crown of ages":            {1500, 8000, 3500},
	"andariel's visage":        {200, 800, 400},
	"giant skull":              {100, 500, 250},
	"kira's guardian":          {50, 300, 120},
	"skin of the vipermagi":    {30, 200, 80},
	"vampire gaze":             {50, 200, 100},
	"tyrael's might":           {15000, 40000, 25000},
	"tal rasha's guardianship": {300, 1500, 600},
	"the gladiator's bane":     {20, 50, 30},
	"skullder's ire":           {50, 150, 80},
	"ormus' robes":             {100, 2000, 500},
	"arkaine's valor":          {100, 500, 200},
	"leviathan":                {30, 100, 50},
	"shaftstop":                {20, 50, 30},
	"duriel's shell":           {10, 30, 15},
	"guardian angel":           {10, 30, 15},
	"toothrow":                 {5, 15, 10},
	"atma's wail":              {10, 30, 15},
	"iron pelt":                {5, 15, 10},
	"templar's might":          {50, 200, 100},
	"steel carapace":           {30, 100, 50},
	"arachnid mesh":            {500, 1200, 700},
	"verdungo's hearty cord":   {50, 300, 120},
	"thundergod's vigor":       {20, 60, 35},
	"string of ears":           {30, 150, 60},
	"nosferatu's coil":         {20, 50, 30},
	"razortail":                {5, 15, 10},
	"goldwrap":                 {10, 30, 15},
	"war traveler":             {200, 1500, 500},
	"sandstorm trek":           {30, 100, 50},
	"marrowwalk":               {20, 80, 40},
	"shadow dancer":            {100, 400, 200},
	"gore rider":               {50, 120, 70},
	"waterwalk":                {5, 15, 10},
	"silkweave":                {5, 15, 10},
	"infernostride":            {5, 15, 10},
	"chance guards":            {30, 200, 80},
	"magefist":                 {20, 50, 30},
	"trang-oul's claws":        {10, 30, 15},
	"dracul's grasp":           {200, 800, 400},
	"laying of hands":          {10, 30, 15},
	"soul drainer":             {20, 80, 40},
	"steelrend":                {200, 2000, 600},
	"frostburn":                {5, 15, 10},
	"ghoulhide":                {5, 10, 7},
	"lava gout":                {5, 10, 7},
	"stormshield":              {100, 300, 150},
	"herald of zakarum":        {100, 300, 150},
	"homunculus":               {30, 80, 50},
	"lidless wall":             {10, 30, 15},
	"head hunter's glory":      {20, 60, 35},
	"medusa's gaze":            {30, 100, 50},
	"mara's kaleidoscope":      {300, 2000, 800},
	"the cat's eye":            {20, 50, 30},
	"highlord's wrath":         {50, 120, 70},
	"metalgrid":                {100, 400, 200},
	"seraph's hymn":            {30, 150, 60},
	"the rising sun":           {10, 30, 15},
	"atma's scarab":            {5, 15, 10},
	"saracen's chance":         {10, 30, 15},
	"the eye of etlich":        {5, 15, 10},
	"crescent moon":            {10, 30, 15},
	"nokozan relic":            {5, 10, 7},
	"stone of jordan":          {500, 800, 600},
	"bul-kathos' wedding band": {100, 400, 200},
	"raven frost":              {15, 50, 25},
	"wisp projector":           {200, 1500, 500},
	"nature's peace":           {20, 60, 35},
	"dwarf star":               {10, 30, 15},
	"carrion wind":             {10, 30, 15},
	"manald heal":              {5, 10, 7},
	"nagelring":                {5, 30, 15},
	"the oculus":               {30, 80, 50},
	"death's fathom":           {3000, 20000, 8000},
	"eschuta's temper":         {100, 1500, 400},
	"death's web":              {2000, 10000, 5000},
	"titan's revenge":          {30, 200, 80},
	"thunderstroke":            {20, 80, 40},
	"windforce":                {200, 500, 300},
	"the grandfather":          {300, 800, 500},
	"doombringer":              {100, 300, 180},
	"lightsabre":               {30, 100, 50},
	"azurewrath":               {100, 500, 250},
	"stormlash":                {100, 400, 200},
	"baranar's star":           {20, 60, 35},
	"schaefer's hammer":        {100, 400, 200},
	"steel pillar":             {50, 200, 100},
	"tomb reaver":              {200, 2000, 700},
	"bonehew":                  {10, 30, 15},
	"reaper's toll":            {30, 150, 60},
	"ethereal edge":            {10, 30, 15},
	"messerschmidt's reaver":   {20, 60, 35},
	"hellslayer":               {10, 30, 15},
	"stone crusher":            {50, 200, 100},
	"jade talon":               {20, 80, 40},
	"shadow killer":            {10, 40, 20},
	"bartuc's cut-throat":      {10, 30, 15},
	"wizardspike":              {20, 50, 30},
	"astreon's iron ward":      {50, 200, 100},
	"horizon's tornado":        {30, 100, 50},
	"cranebeak":                {50, 300, 120},
	"infinity":                 {4000, 8000, 5500},
	"enigma":                   {3000, 6000, 4000},
	"chains of honor":          {1500, 3000, 2000},
	"grief":                    {2000, 4000, 2800},
	"fortitude":                {800, 1500, 1000},
	"heart of the oak":         {200, 500, 300},
	"call to arms":             {500, 2000, 800},
	"ber rune":                 {2000, 2500, 2200},
	"jah rune":                 {1800, 2200, 2000},
	"sur rune":                 {900, 1100, 1000},
	"lo rune":                  {800, 1000, 900},
	"ohm rune":                 {400, 500, 450},
	"vex rune":                 {350, 450, 400},
	"gul rune":                 {150, 200, 175},
	"ist rune":                 {100, 150, 125},
	"mal rune":                 {50, 80, 60},
	"um rune":                  {30, 50, 40},
	"pul rune":                 {15, 25, 20},
	"tal rasha's adjudication": {50, 120, 70},
	"tal rasha's lidless eye":  {20, 50, 30},
	"guillaume's face":         {10, 30, 15},
	"ik maul":                  {20, 80, 40},
	"annihilus":                {500, 3000, 1200},
	"hellfire torch":           {200, 3000, 800},
	"gheed's fortune":          {50, 400, 150},

	// --- ROTW / DLC Uniques ---
	"cold rupture":         {2000, 8000, 4000},
	"flame rift":           {2000, 8000, 4000},
	"crack of the heavens": {2000, 8000, 4000},
	"rotting fissure":      {1500, 6000, 3000},
	"bone break":           {1500, 6000, 3000},
	"black cleft":          {1500, 6000, 3000},
	"ars al'diablolos":     {500, 3000, 1200},
	"ars tor'baalos":       {300, 2000, 800},
	"ars dul'mephistos":    {200, 1500, 600},
	"measured wrath":       {50, 300, 120},
	"dreadfang":            {100, 500, 200},
	"wraithstep":           {100, 400, 180},
	"bloodpact shard":      {50, 300, 120},
	"sling":                {30, 150, 60},
	"opalvein":             {30, 150, 60},
	"entropy locket":       {50, 200, 100},
	"gheed's wager":        {100, 500, 200},
	"defender's bile":      {200, 1000, 400},

	// --- Set Items ---
	"tal rasha's fine spun cloth":   {5, 15, 10},
	"tal rasha's horadric crest":    {10, 30, 15},
	"immortal king's soul cage":     {50, 150, 80},
	"immortal king's stone crusher": {20, 60, 35},
	"immortal king's will":          {10, 30, 15},
	"immortal king's detail":        {10, 30, 15},
	"immortal king's forge":         {10, 20, 12},
	"immortal king's pillar":        {10, 20, 12},
	"griswold's honor":              {10, 30, 15},
	"griswold's heart":              {10, 30, 15},
	"griswold's redemption":         {20, 80, 40},
	"griswold's valor":              {10, 20, 12},
	"natalya's mark":                {20, 60, 35},
	"natalya's shadow":              {30, 100, 50},
	"natalya's totem":               {10, 30, 15},
	"natalya's soul":                {10, 30, 15},
	"trang-oul's guise":             {10, 20, 12},
	"trang-oul's scales":            {10, 30, 15},
	"trang-oul's wing":              {10, 20, 12},
	"trang-oul's girth":             {5, 15, 10},
	"aldur's rhythm":                {10, 30, 15},
	"aldur's stony gaze":            {10, 20, 12},
	"aldur's deception":             {10, 30, 15},
	"bul-kathos' sacred charge":     {30, 100, 50},
	"bul-kathos' tribal guardian":   {30, 100, 50},
	"mavina's caster":               {10, 20, 12},
	"mavina's embrace":              {10, 20, 12},
	"mavina's true sight":           {10, 20, 12},
	"mavina's icy clutch":           {5, 15, 10},
	"mavina's tenet":                {5, 15, 10},
}
