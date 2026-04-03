package enrichment

import (
	"local/internal/svc/internal/gamelib/data/stat"
)

// StatRange defines the possible roll range for a single stat on a unique item.
type StatRange struct {
	StatID   stat.ID
	StatName string // human-readable label
	Min      int
	Max      int
}

// UniqueStatRanges maps unique item IdentifiedName → variable stat ranges.
// Only stats with actual roll variance are included (fixed stats omitted).
// Data sourced from Arreat Summit / d2 wiki.
var UniqueStatRanges = map[string][]StatRange{
	// --- Helms ---
	"Harlequin Crest": {
		{stat.Defense, "Defense", 98, 141},
	},
	"Griffon's Eye": {
		{stat.Defense, "Defense", 50, 60}, // base diadem
		{stat.EnemyLightningResist, "-% Enemy Lightning Res", 15, 20},
		{stat.LightningSkillDamage, "+% Lightning Damage", 10, 15},
	},
	"Nightwing's Veil": {
		{stat.Defense, "Defense", 327, 378},
		{stat.ColdSkillDamage, "+% Cold Damage", 8, 15},
		{stat.Dexterity, "Dexterity", 10, 20},
		{stat.AbsorbCold, "Cold Absorb", 5, 9},
	},
	"Crown of Ages": {
		{stat.Defense, "Defense", 349, 399},
		{stat.FireResist, "All Resistances", 20, 30},
		{stat.DamageReduced, "Damage Reduced", 10, 15},
	},
	"Andariel's Visage": {
		{stat.Defense, "Defense", 310, 387},
		{stat.Strength, "Strength", 25, 30},
		{stat.LifeSteal, "Life Steal", 8, 10},
	},
	"Giantskull": {
		{stat.Defense, "Defense", 350, 477},
		{stat.Strength, "Strength", 25, 35},
		{stat.CrushingBlow, "Crushing Blow", 10, 10},
	},
	"Kira's Guardian": {
		{stat.Defense, "Defense", 90, 170},
		{stat.FireResist, "All Resistances", 50, 70},
	},
	"Vampiregaze": {
		{stat.Defense, "Defense", 252, 303},
		{stat.LifeSteal, "Life Steal", 6, 8},
		{stat.ManaSteal, "Mana Steal", 6, 8},
		{stat.DamageReduced, "Damage Reduced", 15, 20},
	},

	"Guillaume's Face": {
		{stat.Defense, "Defense", 217, 245},
		{stat.CrushingBlow, "Crushing Blow", 35, 35},
	},

	// --- Armor ---
	"Skin of the Vipermagi": {
		{stat.Defense, "Defense", 229, 279},
		{stat.FireResist, "All Resistances", 20, 35},
		{stat.MagicDamageReduction, "Magic Damage Reduced", 9, 13},
	},
	"Chains of Honor": {}, // runeword, skip
	"Enigma":          {}, // runeword, skip
	"Tyrael's Might": {
		{stat.Defense, "Defense", 120, 150},
		{stat.Strength, "Strength", 20, 30},
		{stat.FireResist, "All Resistances", 20, 30},
	},
	// Note: Tal Rasha's armor is "Tal Rasha's Howling Wind" in gamelib (see set items section)
	"The Gladiator's Bane": {
		{stat.Defense, "Defense", 1255, 1496},
	},
	"Skullder's Ire": {
		{stat.Defense, "Defense", 634, 732},
	},
	"Ormus' Robes": {
		{stat.Defense, "Defense", 371, 487},
		{stat.LightningSkillDamage, "+% Lightning Damage", 10, 15},
		{stat.ColdSkillDamage, "+% Cold Damage", 10, 15},
		{stat.FireSkillDamage, "+% Fire Damage", 10, 15},
	},
	"Arkaine's Valor": {
		{stat.Defense, "Defense", 1286, 1577},
		{stat.AllSkills, "All Skills", 1, 2},
		{stat.Vitality, "Vitality", 0, 0},
		{stat.DamageReduced, "Damage Reduced", 10, 15},
	},
	"Leviathan": {
		{stat.Defense, "Defense", 1514, 1720},
		{stat.Strength, "Strength", 40, 50},
		{stat.DamageReduced, "Damage Reduced", 15, 25},
	},
	"Shaftstop": {
		{stat.Defense, "Defense", 599, 684},
		{stat.DamageReduced, "Damage Reduced", 30, 30},
	},
	"Duriel's Shell": {
		{stat.Defense, "Defense", 528, 650},
		{stat.Strength, "Strength", 15, 20},
		{stat.FireResist, "All Resistances", 20, 20},
		{stat.Life, "Life", 15, 15},
	},
	"Guardian Angel": {
		{stat.Defense, "Defense", 770, 1023},
		{stat.FireResist, "All Resistances", 15, 15},
	},
	"Toothrow": {
		{stat.Defense, "Defense", 556, 672},
		{stat.Strength, "Strength", 10, 10},
	},
	"Atma's Wail": {
		{stat.Defense, "Defense", 515, 579},
		{stat.Dexterity, "Dexterity", 15, 15},
		{stat.MagicFind, "MF", 20, 20},
		{stat.ReplenishLife, "Replenish Life", 10, 10},
	},
	"Templar's Might": {
		{stat.Defense, "Defense", 1622, 1837},
		{stat.Strength, "Strength", 10, 15},
		{stat.Vitality, "Vitality", 10, 15},
	},
	"Steel Carapice": {
		{stat.Defense, "Defense", 1319, 1534},
		{stat.DamageReduced, "Damage Reduced", 9, 14},
	},

	// --- Belts ---
	"Arachnid Mesh": {
		{stat.Defense, "Defense", 119, 138},
		{stat.EnhancedDamage, "Enhanced Defense", 90, 120},
	},
	"Verdungo's Hearty Cord": {
		{stat.Defense, "Defense", 261, 331},
		{stat.Vitality, "Vitality", 30, 40},
		{stat.ReplenishLife, "Replenish Life", 10, 13},
		{stat.DamageReduced, "Damage Reduced", 10, 15},
	},
	"Thudergod's Vigor": {
		{stat.Defense, "Defense", 137, 159},
		{stat.Strength, "Strength", 20, 20},
		{stat.Vitality, "Vitality", 20, 20},
		{stat.AbsorbLightning, "Lightning Absorb", 1, 1},
	},
	"String of Ears": {
		{stat.Defense, "Defense", 102, 113},
		{stat.LifeSteal, "Life Steal", 6, 8},
		{stat.DamageReduced, "Damage Reduced", 10, 15},
		{stat.MagicDamageReduction, "Magic Damage Reduced", 10, 15},
	},
	"Nosferatu's Coil": {
		{stat.Defense, "Defense", 56, 63},
		{stat.LifeSteal, "Life Steal", 5, 7},
		{stat.Strength, "Strength", 15, 15},
	},
	"Razortail": {
		{stat.Defense, "Defense", 96, 107},
		{stat.Dexterity, "Dexterity", 15, 15},
	},
	"Goldwrap": {
		{stat.Defense, "Defense", 34, 36},
		{stat.MagicFind, "MF", 30, 30},
	},

	// --- Boots ---
	"Wartraveler": {
		{stat.Defense, "Defense", 120, 139},
		{stat.MagicFind, "MF", 30, 50},
		{stat.Vitality, "Vitality", 10, 10},
		{stat.Strength, "Strength", 10, 10},
	},
	"Sandstorm Trek": {
		{stat.Defense, "Defense", 158, 178},
		{stat.Strength, "Strength", 10, 15},
		{stat.Vitality, "Vitality", 10, 15},
	},
	"Marrowwalk": {
		{stat.Defense, "Defense", 183, 204},
		{stat.Strength, "Strength", 10, 20},
		{stat.Dexterity, "Dexterity", 17, 17},
	},
	"Shadowdancer": {
		{stat.Defense, "Defense", 122, 144},
		{stat.Dexterity, "Dexterity", 15, 25},
	},
	"Gorerider": {
		{stat.Defense, "Defense", 140, 162},
	},
	"Waterwalk": {
		{stat.Defense, "Defense", 112, 124},
		{stat.Life, "Life", 45, 65},
		{stat.Dexterity, "Dexterity", 15, 15},
	},
	"Silkweave": {
		{stat.Defense, "Defense", 112, 130},
		{stat.Mana, "Mana", 5, 5},
		{stat.MagicFind, "MF", 10, 10},
	},
	"Infernostride": {
		{stat.Defense, "Defense", 94, 105},
		{stat.MagicFind, "MF", 20, 20},
	},
	"Aldur's Advance": {
		{stat.Defense, "Defense", 39, 47},
	},

	// --- Gloves ---
	"Chance Guards": {
		{stat.Defense, "Defense", 27, 28},
		{stat.MagicFind, "MF", 25, 40},
	},
	"Magefist": {
		{stat.Defense, "Defense", 24, 25},
	},
	"Trang-Oul's Claws": {
		{stat.Defense, "Defense", 67, 74},
	},
	"Dracul's Grasp": {
		{stat.Defense, "Defense", 125, 145},
		{stat.Strength, "Strength", 10, 15},
		{stat.LifeSteal, "Life Steal", 7, 10},
		{stat.Life, "Life", 5, 10},
	},
	"Laying of Hands": {
		{stat.Defense, "Defense", 79, 87},
	},
	"Souldrain": {
		{stat.Defense, "Defense", 129, 149},
		{stat.ManaSteal, "Mana Steal", 4, 7},
		{stat.LifeSteal, "Life Steal", 4, 7},
	},
	"Steelrend": {
		{stat.Defense, "Defense", 232, 281},
		{stat.EnhancedDamage, "Enhanced Damage", 30, 60},
		{stat.Strength, "Strength", 15, 25},
	},
	"Frostburn": {
		{stat.Defense, "Defense", 47, 49},
		{stat.Mana, "Enhanced Maximum Mana", 40, 40},
	},
	"Ghoulhide": {
		{stat.Defense, "Defense", 36, 40},
		{stat.LifeSteal, "Life Steal", 4, 5},
	},
	"Lavagout": {
		{stat.Defense, "Defense", 120, 142},
	},

	// --- Shields ---
	"Stormshield": {
		{stat.Defense, "Defense", 136, 519},
		{stat.Strength, "Strength", 30, 30},
	},
	"Herald of Zakarum": {
		{stat.Defense, "Defense", 422, 507},
		{stat.AllSkills, "All Skills", 2, 2},
		{stat.FireResist, "All Resistances", 50, 50},
	},
	"Homunculus": {
		{stat.Defense, "Defense", 177, 213},
		{stat.AllSkills, "All Skills", 2, 2},
		{stat.FireResist, "All Resistances", 40, 40},
	},
	"Lidless Wall": {
		{stat.Defense, "Defense", 271, 347},
		{stat.Mana, "Mana", 10, 10},
	},
	"Headhunter's Glory": {
		{stat.Defense, "Defense", 320, 420},
		{stat.FireResist, "Fire Resist", 20, 30},
	},
	"Medusa's Gaze": {
		{stat.Defense, "Defense", 405, 453},
		{stat.LifeSteal, "Life Steal", 5, 9},
	},

	// --- Amulets ---
	"Mara's Kaleidoscope": {
		{stat.AllSkills, "All Skills", 1, 2},
		{stat.FireResist, "All Resistances", 20, 30},
	},
	"The Cat's Eye":     {},
	"Highlord's Wrath":  {},
	"Tal Rasha's Adjudication": {},
	"Metalgrid": {
		{stat.Defense, "Defense", 300, 350},
		{stat.FireResist, "All Resistances", 25, 35},
	},
	"Seraph's Hymn": {
		{stat.AllSkills, "All Skills", 1, 2},
		{stat.DemonDamagePercent, "Damage to Demons", 25, 50},
		{stat.UndeadDamagePercent, "Damage to Undead", 25, 50},
	},
	"Atma's Scarab": {},
	"Saracen's Chance": {
		{stat.FireResist, "All Resistances", 15, 25},
		{stat.Strength, "Strength", 12, 12},
		{stat.Dexterity, "Dexterity", 12, 12},
		{stat.Vitality, "Vitality", 12, 12},
		{stat.Energy, "Energy", 12, 12},
	},
	"The Eye of Etlich": {
		{stat.LifeSteal, "Life Steal", 3, 7},
		{stat.Defense, "Defense vs. Missile", 10, 40},
	},
	"Crescent Moon": {
		{stat.LifeSteal, "Life Steal", 3, 6},
		{stat.ManaSteal, "Mana Steal", 11, 15},
	},
	"Nokozan Relic":     {},

	// --- Rings ---
	"The Stone of Jordan": {},
	"Bul Katho's Wedding Band": {
		{stat.Life, "Life", 3, 5},
		{stat.LifeSteal, "Life Steal", 3, 5},
	},
	"The Rising Sun": {},
	"Dwarf Star": {
		{stat.AbsorbFire, "Fire Absorb", 15, 15},
		{stat.Life, "Life", 40, 40},
		{stat.MagicDamageReduction, "Magic Damage Reduced", 12, 15},
	},
	"Carrion Wind": {
		{stat.LifeSteal, "Life Steal", 6, 9},
		{stat.Defense, "Defense vs. Missile", 15, 15},
	},
	"Manald Heal": {
		{stat.ManaSteal, "Mana Steal", 4, 7},
		{stat.ReplenishLife, "Replenish Life", 5, 8},
	},
	"Nagelring": {
		{stat.MagicFind, "MF", 15, 30},
		{stat.AttackRating, "Attack Rating", 50, 75},
	},
	"Raven Frost": {
		{stat.Dexterity, "Dexterity", 15, 20},
		{stat.AttackRating, "Attack Rating", 150, 250},
	},
	"Wisp Projector": {
		{stat.AbsorbLightning, "Lightning Absorb", 10, 20},
		{stat.MagicFind, "MF", 10, 20},
	},
	"Nature's Peace": {
		{stat.PoisonResist, "Poison Resist", 20, 30},
		{stat.DamageReduced, "Damage Reduced", 7, 11},
	},

	// --- Weapons ---
	"The Oculus": {
		{stat.Defense, "Defense", 0, 0},
		{stat.AllSkills, "All Skills", 3, 3},
		{stat.FireResist, "All Resistances", 20, 20},
		{stat.MagicFind, "MF", 50, 50},
	},
	"Death's Fathom": {
		{stat.ColdSkillDamage, "+% Cold Damage", 15, 30},
		{stat.AllSkills, "All Skills", 3, 3},
	},
	"Eschuta's temper": {
		{stat.AllSkills, "All Skills", 1, 3},
		{stat.LightningSkillDamage, "+% Lightning Damage", 10, 20},
		{stat.FireSkillDamage, "+% Fire Damage", 10, 20},
	},
	"Heart of the Oak": {}, // runeword
	"Call to Arms":      {}, // runeword
	"Deaths's Web": {
		{stat.EnemyPoisonResist, "-% Enemy Poison Res", 40, 50},
		{stat.AllSkills, "All Skills", 2, 2},
	},
	"Titan's Revenge": {
		{stat.EnhancedDamage, "Enhanced Damage", 150, 200},
		{stat.LifeSteal, "Life Steal", 5, 9},
	},
	"Thunderstroke": {
		{stat.EnhancedDamage, "Enhanced Damage", 150, 200},
		{stat.EnemyLightningResist, "-% Enemy Lightning Res", 15, 15},
	},
	"Windforce": {
		{stat.EnhancedDamage, "Enhanced Damage", 250, 250},
	},
	"The Grandfather": {
		{stat.EnhancedDamage, "Enhanced Damage", 150, 250},
		{stat.Life, "Life", 80, 80},
		{stat.AttackRating, "Attack Rating", 50, 50},
	},
	"Doombringer": {
		{stat.EnhancedDamage, "Enhanced Damage", 180, 250},
		{stat.LifeSteal, "Life Steal", 7, 7},
		{stat.Life, "Life", 20, 20},
	},
	"Lightsabre": {
		{stat.EnhancedDamage, "Enhanced Damage", 150, 200},
		{stat.AbsorbLightning, "Lightning Absorb", 1, 1},
	},
	"Azurewrath": {
		{stat.EnhancedDamage, "Enhanced Damage", 230, 270},
		{stat.AllSkills, "All Skills", 1, 1},
	},
	"Stormlash": {
		{stat.EnhancedDamage, "Enhanced Damage", 240, 300},
		{stat.CrushingBlow, "Crushing Blow", 33, 33},
		{stat.AbsorbLightning, "Lightning Absorb", 3, 9},
	},
	"Baranar's Star": {
		{stat.EnhancedDamage, "Enhanced Damage", 200, 200},
	},
	"Schaefer's Hammer": {
		{stat.EnhancedDamage, "Enhanced Damage", 100, 130},
		{stat.Life, "Life", 50, 50},
		{stat.AbsorbLightning, "Lightning Absorb", 20, 20},
	},
	"Steelpillar": {
		{stat.EnhancedDamage, "Enhanced Damage", 210, 260},
		{stat.Defense, "Enhanced Defense", 50, 80},
	},
	"Tomb Reaver": {
		{stat.EnhancedDamage, "Enhanced Damage", 200, 280},
		{stat.MagicFind, "MF", 50, 80},
		{stat.FireResist, "All Resistances", 30, 50},
		{stat.LifeSteal, "Life Steal", 10, 15},
	},
	"Bonehew": {
		{stat.EnhancedDamage, "Enhanced Damage", 270, 320},
	},
	"The Reaper's Toll": {
		{stat.EnhancedDamage, "Enhanced Damage", 190, 240},
		{stat.LifeSteal, "Life Steal", 11, 15},
	},
	"Ethereal Edge": {
		{stat.EnhancedDamage, "Enhanced Damage", 150, 180},
		{stat.AbsorbFire, "Fire Absorb", 10, 12},
	},
	"Messerschmidt's Reaver": {
		{stat.EnhancedDamage, "Enhanced Damage", 200, 300},
	},
	"Hellslayer": {
		{stat.EnhancedDamage, "Enhanced Damage", 100, 100},
		{stat.Strength, "Strength", 25, 25},
		{stat.Vitality, "Vitality", 25, 25},
	},
	"Stone Crusher": {
		{stat.EnhancedDamage, "Enhanced Damage", 280, 320},
		{stat.CrushingBlow, "Crushing Blow", 40, 40},
		{stat.TargetDefense, "-% Enemy Def per Hit", 25, 25},
	},
	"Jadetalon": {
		{stat.Defense, "Defense", 495, 580},
		{stat.ManaSteal, "Mana Steal", 10, 15},
		{stat.FireResist, "All Resistances", 40, 50},
	},
	"Shadowkiller": {
		{stat.EnhancedDamage, "Enhanced Damage", 170, 220},
		{stat.ManaSteal, "Mana Steal", 10, 15},
	},
	"Cutthroat1": {
		{stat.EnhancedDamage, "Enhanced Damage", 150, 200},
		{stat.LifeSteal, "Life Steal", 5, 9},
		{stat.Strength, "Strength", 20, 20},
		{stat.Dexterity, "Dexterity", 20, 20},
	},
	"Wizardspike": {
		{stat.Mana, "Mana", 75, 75},
		{stat.FireResist, "All Resistances", 75, 75},
	},
	"Astreon's Iron Ward": {
		{stat.EnhancedDamage, "Enhanced Damage", 240, 290},
		{stat.CrushingBlow, "Crushing Blow", 33, 33},
	},
	"Horizon's Tornado": {
		{stat.EnhancedDamage, "Enhanced Damage", 230, 280},
	},
	"Cranebeak": {
		{stat.EnhancedDamage, "Enhanced Damage", 240, 300},
		{stat.MagicFind, "MF", 20, 50},
	},

	// --- Mercenary Items ---
	"Andariel's Visage (merc)": {},
	"Ethereal Titans":          {},

	// --- Popular Set Items ---
	"Tal Rasha's Lidless Eye": {
		{stat.Defense, "Defense", 0, 0},
	},
	"Immortal King's Stone Crusher": {
		{stat.EnhancedDamage, "Enhanced Damage", 200, 250},
	},
	"Tal Rasha's Fire-Spun Cloth": {
		{stat.Defense, "Defense", 35, 40},
		{stat.Dexterity, "Dexterity", 20, 20},
	},
	"Tal Rasha's Howling Wind": {
		{stat.Defense, "Defense", 828, 939},
		{stat.MagicFind, "MF", 65, 100},
	},
	"Tal Rasha's Horadric Crest": {
		{stat.Defense, "Defense", 99, 131},
		{stat.LifeSteal, "Life Steal", 10, 10},
		{stat.ManaSteal, "Mana Steal", 10, 10},
		{stat.FireResist, "All Resistances", 15, 15},
	},
	"Immortal King's Soul Cage": {
		{stat.Defense, "Defense", 1001, 1100},
	},
	"Immortal King's Will": {
		{stat.Defense, "Defense", 160, 175},
		{stat.MagicFind, "MF", 25, 40},
	},
	"Immortal King's Detail": {
		{stat.Defense, "Defense", 89, 99},
		{stat.Strength, "Strength", 25, 25},
	},
	"Griswold's Heart": {
		{stat.Defense, "Defense", 917, 950},
		{stat.Strength, "Strength", 20, 20},
	},
	"Griswolds's Redemption": {
		{stat.EnhancedDamage, "Enhanced Damage", 200, 240},
	},
	"Griswold's Honor": {
		{stat.Defense, "Defense", 290, 333},
		{stat.FireResist, "All Resistances", 45, 45},
	},
	"Natalya's Mark": {
		{stat.EnhancedDamage, "Enhanced Damage", 200, 200},
	},
	"Natalya's Shadow": {
		{stat.Defense, "Defense", 540, 660},
	},
	"Trang-Oul's Scales": {
		{stat.Defense, "Defense", 857, 1057},
	},
	// Note: Aldur's weapon is "Aldur's Stony Gaze" in gamelib — no variable stats
	"Aldur's Deception": {
		{stat.Defense, "Defense", 1029, 1109},
	},
	"Bul-Kathos' Sacred Charge": {
		{stat.EnhancedDamage, "Enhanced Damage", 200, 200},
	},
	"Bul-Kathos' Tribal Guardian": {
		{stat.EnhancedDamage, "Enhanced Damage", 200, 200},
	},
	"M'avina's Caster": {
		{stat.EnhancedDamage, "Enhanced Damage", 188, 188},
	},

	// --- RotW DLC: Sunder Charms ---
	"Cold Rupture": {
		{stat.ColdResist, "Cold Resist Penalty", -90, -70},
	},
	"Flame Rift": {
		{stat.FireResist, "Fire Resist Penalty", -90, -70},
	},
	"Crack of the Heavens": {
		{stat.LightningResist, "Lightning Resist Penalty", -90, -70},
	},
	"Rotting Fissure": {
		{stat.PoisonResist, "Poison Resist Penalty", -90, -70},
	},
	"Bone Break": {
		{stat.DamageReduced, "Phys Resist Penalty", -20, -10},
	},
	"Black Cleft": {
		{stat.MagicDamageReduction, "Magic Resist Penalty", -65, -45},
	},

	// --- RotW DLC: Warlock Items ---
	"Ars Al'Diablolos": {
		{stat.EnhancedDefense, "Enhanced Defense", 170, 200},
		{stat.FireSkillDamage, "+% Fire Damage", 15, 25},
		{stat.FireResist, "Fire Resist", 20, 30},
	},
	"Ars Tor'Baalos": {
		{stat.EnhancedDefense, "Enhanced Defense", 120, 150},
		{stat.DamageReduced, "Damage Reduced", 5, 10},
	},
	"Ars Dul'Mephistos": {
		{stat.FasterCastRate, "Faster Cast Rate", 20, 30},
		{stat.EnhancedDamage, "Enhanced Damage", 70, 115},
		{stat.AttackRatingPercent, "Attack Rating %", 50, 70},
		{stat.EnhancedDefense, "Enhanced Defense", 140, 170},
		{stat.PassiveMagicPierce, "Pierce Magic Resist", 10, 20},
		{stat.MagicFind, "MF", 10, 25},
	},
	"Measured Wrath": {
		{stat.EnhancedDefense, "Enhanced Defense", 130, 180},
		{stat.Vitality, "Vitality", 10, 20},
		{stat.FireResist, "All Resistances", 20, 30},
	},
	"Dreadfang": {
		{stat.EnhancedDamage, "Enhanced Damage", 133, 166},
		{stat.ManaSteal, "Mana Steal", 3, 6},
	},
	"Wraithstep": {
		{stat.Defense, "Defense", 40, 60},
		{stat.Dexterity, "Dexterity", 10, 15},
		{stat.Energy, "Energy", 10, 15},
	},
	"Bloodpact Shard": {
		{stat.Life, "Life %", 10, 15},
		{stat.MagicFind, "MF", 20, 35},
	},

	// --- RotW DLC: Rings & Amulets ---
	"Sling": {
		{stat.PassiveMagicPierce, "Pierce Magic Resist", 3, 5},
		{stat.Energy, "Energy", 10, 15},
		{stat.MagicFind, "MF", 10, 20},
	},
	"Opalvein": {
		{stat.FireResist, "All Resistances", 6, 8},
		{stat.LifeAfterEachKill, "Life After Kill", 1, 3},
		{stat.ManaAfterKill, "Mana After Kill", 1, 3},
	},
	"Entropy Locket": {
		{stat.PassiveMagicMastery, "+% Magic Damage", 5, 10},
		{stat.FasterCastRate, "Faster Cast Rate", 5, 10},
		{stat.LightningResist, "Lightning Resist", 25, 40},
		{stat.MaxMana, "Mana %", 10, 15},
		{stat.MagicDamageReduction, "Magic Damage Reduced", 8, 12},
	},
	"Gheed's Wager": {
		{stat.FasterCastRate, "Faster Cast Rate", 10, 20},
		{stat.FasterHitRecovery, "Faster Hit Recovery", 10, 20},
		{stat.FasterRunWalk, "Faster Run/Walk", 10, 20},
		{stat.EnhancedDefense, "Enhanced Defense", 90, 150},
		{stat.PassiveMagicPierce, "Pierce Magic Resist", 3, 7},
		{stat.FireResist, "All Resistances", 5, 15},
		{stat.GoldFind, "Gold Find", 44, 75},
	},

	// --- RotW DLC: Unique Warlock Helm (Death Mask) ---
	"Unique Warlock Helm": {
		{stat.PierceFire, "Pierce Fire Resist", 5, 8},
		{stat.PassiveMagicPierce, "Pierce Magic Resist", 5, 8},
		{stat.EnhancedDefense, "Enhanced Defense", 150, 215},
		{stat.ManaAfterKill, "Mana After Kill", 4, 6},
	},

	// --- RotW DLC: Gemstone Jewels ---
	"Defender's Bile": {
		{stat.PoisonSkillDamage, "+% Poison Damage", 5, 10},
		{stat.PiercePoison, "Pierce Poison Resist", 5, 10},
		{stat.AddExperience, "Bonus Experience", 3, 5},
		{stat.MagicFind, "MF", 15, 35},
		{stat.GoldFind, "Gold Find", 25, 50},
	},
	"Guardian's Thunder": {
		{stat.LightningSkillDamage, "+% Lightning Damage", 5, 10},
		{stat.PierceLightning, "Pierce Lightning Resist", 5, 10},
		{stat.AddExperience, "Bonus Experience", 3, 5},
		{stat.MagicFind, "MF", 15, 35},
		{stat.GoldFind, "Gold Find", 25, 50},
	},
	"Protector's Frost": {
		{stat.ColdSkillDamage, "+% Cold Damage", 5, 10},
		{stat.PierceCold, "Pierce Cold Resist", 5, 10},
		{stat.AddExperience, "Bonus Experience", 3, 5},
		{stat.MagicFind, "MF", 15, 35},
		{stat.GoldFind, "Gold Find", 25, 50},
	},
	"Defender's Fire": {
		{stat.FireSkillDamage, "+% Fire Damage", 5, 10},
		{stat.PierceFire, "Pierce Fire Resist", 5, 10},
		{stat.AddExperience, "Bonus Experience", 3, 5},
		{stat.MagicFind, "MF", 15, 35},
		{stat.GoldFind, "Gold Find", 25, 50},
	},
	"Protector's Stone": {
		{stat.EnhancedDamage, "Enhanced Damage", 30, 50},
		{stat.PassiveDamagePierce, "Pierce Physical Resist", 5, 10},
		{stat.AddExperience, "Bonus Experience", 3, 5},
		{stat.MagicFind, "MF", 15, 35},
		{stat.GoldFind, "Gold Find", 25, 50},
	},
	"Guardian's Light": {
		{stat.PassiveMagicMastery, "+% Magic Damage", 5, 10},
		{stat.PassiveMagicPierce, "Pierce Magic Resist", 5, 10},
		{stat.AddExperience, "Bonus Experience", 3, 5},
		{stat.MagicFind, "MF", 15, 35},
		{stat.GoldFind, "Gold Find", 25, 50},
	},

	// --- RotW DLC: Set Items — McAuley's Folly ---
	"McAuley's Paragon":     {},
	"McAuley's Riprap":      {},
	"McAuley's Taboo":       {},
	"McAuley's Superstition": {},

	// --- RotW DLC: Set Items — Warlord's Glory ---
	"Warlord's Conquest": {},
	"Warlord's Lust":     {},
	"Warlord's Mantle":   {},
	"Warlord's Crushers": {},
	"Warlord's Authority": {},

	// --- RotW DLC: Set Items — Bane's Garments ---
	"Bane's Oathmaker":   {},
	"Bane's Wraithskin":  {},
	"Bane's Authority":   {},

	// --- RotW DLC: Set Items — Horazon's Splendor ---
	"Horazon's Countenance": {
		{stat.Strength, "Strength", 10, 20},
		{stat.MagicDamageReduction, "Magic Damage Reduced", 7, 10},
	},
	"Horazon's Dominion": {
		{stat.EnhancedDefense, "Enhanced Defense", 75, 100},
		{stat.MaxMana, "Mana", 75, 100},
		{stat.FireResist, "All Resistances", 15, 25},
	},
	"Horazon's Hold": {
		{stat.AttackRating, "Attack Rating", 95, 150},
		{stat.Dexterity, "Dexterity", 10, 15},
	},
	"Horazon's Legacy": {
		{stat.Strength, "Strength", 10, 15},
		{stat.Dexterity, "Dexterity", 10, 15},
	},
	"Horazon's Secrets": {
		{stat.Vitality, "Vitality", 20, 30},
		{stat.Life, "Life", 30, 50},
	},
}
