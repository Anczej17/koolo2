package terrorzones

import (
	"slices"
	"strings"

	"github.com/hectorgimenez/d2go/pkg/data/area"
)

// Tier is just a string alias for clarity.
type Tier string

const (
	TierS Tier = "S"
	TierA Tier = "A"
	TierB Tier = "B"
	TierC Tier = "C"
	TierD Tier = "D"
	TierF Tier = "F"
)

// ZoneInfo holds metadata about a terrorizable area.
// Note: Name is NOT stored here, we use area.ID.Area().Name instead.
type ZoneInfo struct {
	Act        int      // 1..5
	ExpTier    Tier     // S/A/B/C/D/F (how good for XP) (Based on "Density/Elites/Access") source www.aoeah.com and https://maxroll.gg/
	LootTier   Tier     // S/A/B/C/D/F (how good for loot) Tier (Based on "TC 87/Density/Elites/Access") source www.aoeah.com and https://maxroll.gg/
	BossPack   string   // boss packs "25-30" (for UI)
	Immunities []string // e.g. []{"c","f","l","p","ph","m"} for Cold/Fire/Lightning/Poison/physical/magic
	Group      string   // multi-level dungeon group name, e.g. "Sewers", "Tal Tombs"
}

// zones is the central metadata map.
// This replaces the tier switch in http_server.go and any scattered TZ metadata.

// Updated for Reign of the Warlock patch (2025).
var zones = map[area.ID]ZoneInfo{
	// Act 1 - Blood Moor removed from TZ rotation in RotW patch.
	area.ColdPlains:    {Act: 1, ExpTier: TierC, LootTier: TierB, BossPack: "7-9", Immunities: []string{"f", "c", "l", "p"}, Group: "Cold Plains / Cave"},
	area.BurialGrounds: {Act: 1, ExpTier: TierC, LootTier: TierD, BossPack: "8-10", Immunities: []string{"l"}, Group: "Burial Grounds / Crypt / Mausoleum"},
	area.StonyField:    {Act: 1, ExpTier: TierC, LootTier: TierC, BossPack: "7-11", Immunities: []string{"f", "c", "l", "p"}, Group: "Stony Field / Tristram"},
	area.DarkWood:      {Act: 1, ExpTier: TierB, LootTier: TierB, BossPack: "16-22", Immunities: []string{"f", "c", "l", "p"}, Group: "Dark Wood / Underground Passage"},
	area.BlackMarsh:    {Act: 1, ExpTier: TierA, LootTier: TierA, BossPack: "15-20", Immunities: []string{"f", "c", "l", "p", "ph"}, Group: "Black Marsh / The Hole / Forgotten Tower"},
	area.Barracks:      {Act: 1, ExpTier: TierA, LootTier: TierS, BossPack: "24-32", Immunities: []string{"f", "c", "p", "ph"}, Group: "Jail / Barracks"},
	area.PitLevel1:     {Act: 1, ExpTier: TierS, LootTier: TierS, BossPack: "27-35", Immunities: []string{"f", "c", "l", "ph"}, Group: "The Pit / Tamoe Highland"},
	area.Cathedral:     {Act: 1, ExpTier: TierS, LootTier: TierS, BossPack: "27-35", Immunities: []string{"f", "c", "l", "ph"}, Group: "Cathedral / Catacombs"},
	area.MooMooFarm:    {Act: 1, ExpTier: TierC, LootTier: TierS, BossPack: "6-8", Immunities: []string{}, Group: "Moo Moo Farm"},

	// Act 2
	area.SewersLevel1Act2: {Act: 2, ExpTier: TierS, LootTier: TierB, BossPack: "18-24", Immunities: []string{"f", "c", "p", "m"}, Group: "Lut Gholein Sewers"},
	area.RockyWaste:       {Act: 2, ExpTier: TierS, LootTier: TierA, BossPack: "17-23", Immunities: []string{"f", "c", "l", "p", "m"}, Group: "Rocky Waste / Stony Tomb"},
	area.DryHills:         {Act: 2, ExpTier: TierS, LootTier: TierA, BossPack: "20-27", Immunities: []string{"f", "c", "l", "p"}, Group: "Dry Hills / Halls of the Dead"},
	area.FarOasis:         {Act: 2, ExpTier: TierC, LootTier: TierD, BossPack: "7-9", Immunities: []string{"l", "p", "ph"}, Group: "Far Oasis / Maggot Lair"},
	area.LostCity:         {Act: 2, ExpTier: TierA, LootTier: TierA, BossPack: "21-28", Immunities: []string{"f", "c", "l", "p", "m"}, Group: "Lost City / Valley of Snakes / Claw Viper Temple / Ancient Tunnels"},
	area.ArcaneSanctuary:  {Act: 2, ExpTier: TierC, LootTier: TierA, BossPack: "7-9", Immunities: []string{"f", "c", "l", "p", "ph"}, Group: "Arcane Sanctuary"},
	area.TalRashasTomb1:   {Act: 2, ExpTier: TierS, LootTier: TierS, BossPack: "49-63", Immunities: []string{"f", "c", "l", "p", "m"}, Group: "Canyon of the Magi / Tal Rasha's Tombs"},

	// Act 3
	area.SpiderForest: {Act: 3, ExpTier: TierB, LootTier: TierC, BossPack: "14-20", Immunities: []string{"f", "c", "l", "p"}, Group: "Spider Forest / Spider Cavern / Arachnid Lair"},
	area.GreatMarsh:   {Act: 3, ExpTier: TierB, LootTier: TierC, BossPack: "10-15", Immunities: []string{"f", "c", "l"}, Group: "Great Marsh"},
	area.FlayerJungle: {Act: 3, ExpTier: TierS, LootTier: TierA, BossPack: "22-29", Immunities: []string{"f", "c", "l", "p", "ph", "m"}, Group: "Flayer Jungle / Flayer Dungeon / Swampy Pit"},
	area.KurastBazaar: {Act: 3, ExpTier: TierA, LootTier: TierC, BossPack: "15-17", Immunities: []string{"f", "c", "l", "p", "ph", "m"}, Group: "Kurast Bazaar / Lower Kurast / Upper Kurast / Temples"},
	area.Travincal:    {Act: 3, ExpTier: TierC, LootTier: TierS, BossPack: "6-8", Immunities: []string{"f", "c", "l", "p"}, Group: "Travincal"},
	area.DuranceOfHateLevel1: {Act: 3, ExpTier: TierC, LootTier: TierS, BossPack: "15-21", Immunities: []string{"f", "c", "l", "p"}, Group: "Durance of Hate"},

	// Act 4
	area.OuterSteppes:    {Act: 4, ExpTier: TierA, LootTier: TierC, BossPack: "16-20", Immunities: []string{"f", "c", "l", "p"}, Group: "Outer Steppes / Plains of Despair"},
	area.CityOfTheDamned: {Act: 4, ExpTier: TierA, LootTier: TierB, BossPack: "14-17", Immunities: []string{"f", "c", "l", "p"}, Group: "City of the Damned / River of Flame"},
	area.ChaosSanctuary:  {Act: 4, ExpTier: TierS, LootTier: TierS, BossPack: "6-7", Immunities: []string{"f", "c", "l"}, Group: "Chaos Sanctuary"},

	// Act 5
	area.BloodyFoothills:         {Act: 5, ExpTier: TierB, LootTier: TierA, BossPack: "19-25", Immunities: []string{"f", "c", "l", "p", "ph", "m"}, Group: "Bloody Foothills / Frigid Highlands / Abaddon"},
	area.GlacialTrail:            {Act: 5, ExpTier: TierA, LootTier: TierB, BossPack: "13-17", Immunities: []string{"f", "c", "l", "p", "ph"}, Group: "Glacial Trail / Drifter Cavern"},
	area.CrystallinePassage:      {Act: 5, ExpTier: TierA, LootTier: TierC, BossPack: "13-17", Immunities: []string{"f", "c", "l", "p", "ph", "m"}, Group: "Crystalline Passage / Frozen River"},
	area.FrozenTundra:            {Act: 5, ExpTier: TierB, LootTier: TierB, BossPack: "10-14", Immunities: []string{"c", "l", "p"}, Group: "Frozen Tundra / Infernal Pit"},
	area.ArreatPlateau:           {Act: 5, ExpTier: TierC, LootTier: TierF, BossPack: "15-19", Immunities: []string{"f", "c", "l", "p"}, Group: "Arreat Plateau / Pit of Acheron"},
	area.NihlathaksTemple:        {Act: 5, ExpTier: TierA, LootTier: TierA, BossPack: "12-14", Immunities: []string{"f", "c", "l", "p", "ph", "m"}, Group: "Nihlathak's Temple / Temple Halls"},
	area.TheAncientsWay:          {Act: 5, ExpTier: TierB, LootTier: TierB, BossPack: "6-8", Immunities: []string{"c", "l", "p", "ph"}, Group: "Ancient's Way / Icy Cellar"},
	area.TheWorldStoneKeepLevel1: {Act: 5, ExpTier: TierS, LootTier: TierS, BossPack: "22-29", Immunities: []string{"f", "c", "l", "p", "ph", "m"}, Group: "Worldstone Keep / Throne of Destruction"},
}

// -----------------------------------------------------------------------------
// Zone Metadata Helper Overview
//
// These helpers provide read-access to the central Terror Zone metadata map.
// They are intentionally minimal so all logic draws from one unified source
// (the 'zones' map defined above).
//
//  Info(id) ZoneInfo
//      Returns the full metadata record for a zone (Act, ExpTier, LootTier,
//      BossPack, Immunities, Group). If no entry exists, returns zero values.
//
//  ExpTierOf(id) string
//      Returns the EXP tier (S–F). Used for difficulty/XP-based decisions.
//      Falls back to "F" if data is missing.
//
//  LootTierOf(id) string
//      Returns the loot tier (S–F). Used for drop-quality decisions.
//      Falls back to "F" if data is missing.
//
//  Groups() []Group
//      Returns all multi-area dungeon groups, derived from each zone's
//      Group field. Used by UI grouping and logic that treats multi-layer
//      zones as a single Terror Zone. Built once and cached.
// -----------------------------------------------------------------------------

func Info(id area.ID) ZoneInfo {
	if z, ok := zones[id]; ok {
		return z
	}
	return ZoneInfo{}
}

func ExpTierOf(id area.ID) string {
	if z, ok := zones[id]; ok && z.ExpTier != "" {
		return string(z.ExpTier)
	}
	return string(TierF)
}

func LootTierOf(id area.ID) string {
	if z, ok := zones[id]; ok && z.LootTier != "" {
		return string(z.LootTier)
	}
	return string(TierF)
}

func Zones() map[area.ID]ZoneInfo {
	return zones
}

type Group struct {
	Name string
	IDs  []area.ID
}

var groupsCache []Group
var groupsBuilt bool

func Groups() []Group {
	if groupsBuilt {
		return groupsCache
	}

	groupMap := make(map[string][]area.ID)
	for id, z := range zones {
		if z.Group == "" {
			continue
		}
		groupMap[z.Group] = append(groupMap[z.Group], id)
	}

	for name, ids := range groupMap {
		slices.SortFunc(ids, func(a, b area.ID) int { return int(a) - int(b) })
		groupsCache = append(groupsCache, Group{
			Name: name,
			IDs:  ids,
		})
	}

	slices.SortFunc(groupsCache, func(a, b Group) int {
		return strings.Compare(a.Name, b.Name)
	})

	groupsBuilt = true
	return groupsCache
}
