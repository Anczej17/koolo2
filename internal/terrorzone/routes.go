package terrorzones

import "github.com/hectorgimenez/d2go/pkg/data/area"

// StepKind defines what to do in a route step.
type StepKind int

const (
	StepMove  StepKind = iota // Move to area, do NOT clear
	StepClear                 // Clear area (after moving there)
	StepTown                  // Return to town (for disconnected segments)
	StepWP                    // Use waypoint to area (mid-route waypoint jump)
)

// Step represents one action in a route.
type Step struct {
	Kind StepKind
	Area area.ID
}

// Route is an ordered list of Steps.
type Route []Step

// DSL helpers:
func Clear(a area.ID) Step {
	return Step{Kind: StepClear, Area: a}
}

func Move(a area.ID) Step {
	return Step{Kind: StepMove, Area: a}
}

func Town() Step {
	return Step{Kind: StepTown}
}

func WP(a area.ID) Step {
	return Step{Kind: StepWP, Area: a}
}

// Routes is the central definition of multi-area TZ runs.
//
// Key   = "primary" terrorized area (ctx.Data.TerrorZones[0])
// Value = one or more routes (alternatives) for that TZ event.
//
// IMPORTANT: Navigation must be physically valid!
//   - MoveToArea only works between ADJACENT/CONNECTED areas
//   - To leave a dungeon: walk back level by level to the surface
//   - To reach a disconnected area: Town() then WP() to a new waypoint
//   - First step is always WP (handled by terror_zone.go)
//
// Updated for Reign of the Warlock patch (2025).
var Routes = map[area.ID][]Route{

	// ==================== Act 1 ====================
	// Blood Moor removed from TZ rotation in RotW patch.

	// Cold Plains / Cave
	// WP->ColdPlains, clear, enter Cave L1, clear, enter Cave L2, clear
	area.ColdPlains: {{
		Clear(area.ColdPlains),
		Clear(area.CaveLevel1), Clear(area.CaveLevel2),
	}},

	// Burial Grounds / Crypt / Mausoleum
	// WP->ColdPlains, walk to BurialGrounds, clear, enter Crypt, clear,
	// walk back to BurialGrounds, enter Mausoleum, clear
	area.BurialGrounds: {{
		Move(area.ColdPlains),
		Clear(area.BurialGrounds),
		Clear(area.Crypt),
		Move(area.BurialGrounds), // walk back from Crypt
		Clear(area.Mausoleum),
	}},

	// Stony Field / Tristram  (merged in RotW)
	// WP->StonyField, clear, then Tristram is via portal (special case in terror_zone.go)
	// For now just clear StonyField - Tristram needs portal interaction
	area.StonyField: {{
		Clear(area.StonyField),
	}},

	// Dark Wood / Underground Passage
	// WP->DarkWood, clear, walk to UndergroundPassage L1, clear, enter L2, clear
	area.DarkWood: {{
		Clear(area.DarkWood),
		Clear(area.UndergroundPassageLevel1), Clear(area.UndergroundPassageLevel2),
	}},

	// Black Marsh / The Hole / Forgotten Tower (merged in RotW)
	// WP->BlackMarsh, clear, enter Hole L1, clear, enter Hole L2, clear,
	// walk back Hole L1, walk back BlackMarsh, walk to ForgottenTower, clear,
	// enter TowerCellar L1-L5, clear each
	area.BlackMarsh: {{
		Clear(area.BlackMarsh),
		Clear(area.HoleLevel1), Clear(area.HoleLevel2),
		Move(area.HoleLevel1), Move(area.BlackMarsh), // walk back out of Hole
		Clear(area.ForgottenTower),
		Clear(area.TowerCellarLevel1), Clear(area.TowerCellarLevel2),
		Clear(area.TowerCellarLevel3), Clear(area.TowerCellarLevel4),
		Clear(area.TowerCellarLevel5),
	}},

	// Jail / Barracks
	// WP->OuterCloister, walk to Barracks, clear, walk to Jail L1-L3, clear each
	area.Barracks: {{
		Move(area.OuterCloister),
		Clear(area.Barracks),
		Clear(area.JailLevel1), Clear(area.JailLevel2), Clear(area.JailLevel3),
	}},

	// Cathedral / Catacombs
	// WP->InnerCloister, walk to Cathedral, clear, enter Catacombs L1-L4, clear each
	area.Cathedral: {{
		Move(area.InnerCloister),
		Clear(area.Cathedral),
		Clear(area.CatacombsLevel1), Clear(area.CatacombsLevel2),
		Clear(area.CatacombsLevel3), Clear(area.CatacombsLevel4),
	}},

	// Pit, Cows -> terror_zone.go special cases

	// ==================== Act 2 ====================

	// Sewers
	// WP->LutGholein (town), enter Sewers L1, clear, enter L2, clear, enter L3, clear
	area.SewersLevel1Act2: {{
		Move(area.LutGholein),
		Clear(area.SewersLevel1Act2), Clear(area.SewersLevel2Act2), Clear(area.SewersLevel3Act2),
	}},

	// Rocky Waste / Stony Tomb
	// WP->DryHills, walk to RockyWaste, clear, enter StonyTomb L1, clear, enter L2, clear
	area.RockyWaste: {{
		Move(area.DryHills),
		Clear(area.RockyWaste),
		Clear(area.StonyTombLevel1), Clear(area.StonyTombLevel2),
	}},

	// Dry Hills / Halls of the Dead
	// WP->DryHills, clear, enter HallsOfDead L1-L3, clear each
	area.DryHills: {{
		Clear(area.DryHills),
		Clear(area.HallsOfTheDeadLevel1), Clear(area.HallsOfTheDeadLevel2), Clear(area.HallsOfTheDeadLevel3),
	}},

	// Far Oasis / Maggot Lair (expanded in RotW)
	// WP->FarOasis, clear, enter MaggotLair L1-L3, clear each
	area.FarOasis: {{
		Clear(area.FarOasis),
		Clear(area.MaggotLairLevel1), Clear(area.MaggotLairLevel2), Clear(area.MaggotLairLevel3),
	}},

	// Lost City / Valley of Snakes / Claw Viper Temple / Ancient Tunnels (merged in RotW)
	// WP->LostCity, clear, enter AncientTunnels, clear,
	// walk back to LostCity, walk to ValleyOfSnakes, clear,
	// enter ClawViperTemple L1, clear, enter L2, clear
	area.LostCity: {{
		Clear(area.LostCity),
		Clear(area.AncientTunnels),
		Move(area.LostCity), // walk back from AncientTunnels
		Clear(area.ValleyOfSnakes),
		Clear(area.ClawViperTempleLevel1), Clear(area.ClawViperTempleLevel2),
	}},

	// Tal Rasha's Tombs, Arcane Sanctuary -> terror_zone.go special cases

	// ==================== Act 3 ====================

	// Spider Forest / Spider Cavern / Arachnid Lair (expanded in RotW)
	// WP->SpiderForest, clear, enter SpiderCavern, clear,
	// walk back to SpiderForest, enter SpiderCave (Arachnid Lair), clear
	area.SpiderForest: {{
		Clear(area.SpiderForest),
		Clear(area.SpiderCavern),
		Move(area.SpiderForest), // walk back from SpiderCavern
		Clear(area.SpiderCave),
	}},

	// Great Marsh
	area.GreatMarsh: {{Clear(area.GreatMarsh)}},

	// Flayer Jungle / Flayer Dungeon / Swampy Pit (expanded in RotW)
	// WP->FlayerJungle, clear, enter FlayerDungeon L1-L3, clear each,
	// walk back L2, L1, FlayerJungle, enter SwampyPit L1-L3, clear each
	area.FlayerJungle: {{
		Clear(area.FlayerJungle),
		Clear(area.FlayerDungeonLevel1), Clear(area.FlayerDungeonLevel2), Clear(area.FlayerDungeonLevel3),
		Move(area.FlayerDungeonLevel2), Move(area.FlayerDungeonLevel1), Move(area.FlayerJungle), // walk back out
		Clear(area.SwampyPitLevel1), Clear(area.SwampyPitLevel2), Clear(area.SwampyPitLevel3),
	}},

	// Kurast Bazaar / Lower Kurast / Upper Kurast / Temples (expanded in RotW)
	// WP->KurastBazaar, clear, enter RuinedTemple, clear, walk back,
	// enter DisusedFane, clear, walk back,
	// walk to LowerKurast, clear, walk to UpperKurast, clear,
	// enter ForgottenTemple, clear, walk back,
	// enter ForgottenReliquary, clear, walk back,
	// enter DisusedReliquary, clear, walk back,
	// enter RuinedFane, clear
	area.KurastBazaar: {{
		Clear(area.KurastBazaar),
		Clear(area.RuinedTemple), Move(area.KurastBazaar),
		Clear(area.DisusedFane), Move(area.KurastBazaar),
		Clear(area.LowerKurast),
		Clear(area.UpperKurast),
		Clear(area.ForgottenTemple), Move(area.UpperKurast),
		Clear(area.ForgottenReliquary), Move(area.UpperKurast),
		Clear(area.DisusedReliquary), Move(area.UpperKurast),
		Clear(area.RuinedFane),
	}},

	// Travincal, Durance of Hate -> terror_zone.go special cases

	// ==================== Act 4 ====================

	// Outer Steppes / Plains of Despair
	// WP->PandemoniumFortress (town), walk to OuterSteppes, clear, walk to PlainsOfDespair, clear
	area.OuterSteppes: {{
		Move(area.ThePandemoniumFortress),
		Clear(area.OuterSteppes), Clear(area.PlainsOfDespair),
	}},

	// River of Flame / City of the Damned
	// WP->CityOfTheDamned... wait, CityOfTheDamned has WP? Let's use RiverOfFlame WP
	// WP->RiverOfFlame, clear, then we need to walk back to CityOfTheDamned
	// Actually RiverOfFlame connects to CityOfTheDamned, so:
	// WP->CityOfTheDamned, clear, walk to RiverOfFlame, clear
	area.CityOfTheDamned: {{
		Clear(area.CityOfTheDamned),
		Clear(area.RiverOfFlame),
	}},

	// Chaos Sanctuary -> terror_zone.go special case

	// ==================== Act 5 ====================

	// Bloody Foothills / Frigid Highlands / Abaddon
	// WP->Harrogath (town), walk to BloodyFoothills, clear,
	// walk to FrigidHighlands, clear, enter Abaddon, clear
	area.BloodyFoothills: {{
		Move(area.Harrogath),
		Clear(area.BloodyFoothills), Clear(area.FrigidHighlands), Clear(area.Abaddon),
	}},

	// Glacial Trail / Drifter Cavern
	// WP->GlacialTrail, clear, enter DrifterCavern, clear
	area.GlacialTrail: {{
		Clear(area.GlacialTrail), Clear(area.DrifterCavern),
	}},

	// Crystalline Passage / Frozen River
	// WP->CrystallinePassage, clear, enter FrozenRiver, clear
	area.CrystallinePassage: {{
		Clear(area.CrystallinePassage), Clear(area.FrozenRiver),
	}},

	// Frozen Tundra / Infernal Pit (NEW in RotW)
	// WP->FrozenTundra, clear, enter InfernalPit, clear
	area.FrozenTundra: {{
		Clear(area.FrozenTundra), Clear(area.InfernalPit),
	}},

	// Arreat Plateau / Pit of Acheron
	// WP->ArreatPlateau, clear, enter PitOfAcheron, clear
	area.ArreatPlateau: {{
		Clear(area.ArreatPlateau), Clear(area.PitOfAcheron),
	}},

	// Ancient's Way / Icy Cellar
	// WP->TheAncientsWay, clear, enter IcyCellar, clear
	area.TheAncientsWay: {{
		Clear(area.TheAncientsWay), Clear(area.IcyCellar),
	}},

	// Nihlathak's Temple / Temple Halls
	// WP->NihlathaksTemple, clear, enter HallsOfAnguish, clear,
	// enter HallsOfPain, clear, enter HallsOfVaught, clear
	area.NihlathaksTemple: {{
		Clear(area.NihlathaksTemple),
		Clear(area.HallsOfAnguish), Clear(area.HallsOfPain), Clear(area.HallsOfVaught),
	}},

	// Worldstone Keep / Baal -> terror_zone.go special case
}

// RoutesFor returns all routes for a given primary TZ area.
func RoutesFor(first area.ID) []Route {
	if rs, ok := Routes[first]; ok {
		return rs
	}
	return nil
}
