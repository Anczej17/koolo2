// Code generated locally from kolbot-d2data JSON for vendor price calculation. DO NOT EDIT.
package town

type vendorPriceCostMod struct {
	Mult int
	Add  int
}

var vendorItemStatCostMods = map[int]vendorPriceCostMod{
	0: {Mult: 55, Add: 125}, // strength
	1: {Mult: 55, Add: 100}, // energy
	2: {Mult: 55, Add: 125}, // dexterity
	3: {Mult: 55, Add: 100}, // vitality
	7: {Mult: 20, Add: 56}, // maxhp
	9: {Mult: 20, Add: 81}, // maxmana
	11: {Mult: 20, Add: 75}, // maxstamina
	16: {Mult: 20, Add: 47}, // item_armor_percent
	17: {Mult: 20, Add: 45}, // item_maxdamage_percent
	18: {Mult: 20, Add: 45}, // item_mindamage_percent
	19: {Mult: 10, Add: 15}, // tohit
	20: {Mult: 204, Add: 89}, // toblock
	21: {Mult: 25, Add: 122}, // mindamage
	22: {Mult: 16, Add: 94}, // maxdamage
	23: {Mult: 15, Add: 97}, // secondary_mindamage
	24: {Mult: 11, Add: 85}, // secondary_maxdamage
	25: {Mult: 40, Add: 45}, // damagepercent
	31: {Mult: 10, Add: 17}, // armorclass
	32: {Mult: 5, Add: 11}, // armorclass_vs_missile
	33: {Mult: 7, Add: 13}, // armorclass_vs_hth
	34: {Mult: 200, Add: 188}, // normal_damage_reduction
	35: {Mult: 340, Add: 397}, // magic_damage_reduction
	36: {Mult: 68, Add: 152}, // damageresist
	37: {Mult: 68, Add: 164}, // magicresist
	38: {Mult: 409, Add: 1091}, // maxmagicresist
	39: {Mult: 20, Add: 43}, // fireresist
	40: {Mult: 256, Add: 584}, // maxfireresist
	41: {Mult: 20, Add: 43}, // lightresist
	42: {Mult: 256, Add: 584}, // maxlightresist
	43: {Mult: 20, Add: 43}, // coldresist
	44: {Mult: 256, Add: 584}, // maxcoldresist
	45: {Mult: 20, Add: 43}, // poisonresist
	46: {Mult: 256, Add: 526}, // maxpoisonresist
	48: {Mult: 10, Add: 11}, // firemindam
	49: {Mult: 10, Add: 19}, // firemaxdam
	50: {Mult: 10, Add: 12}, // lightmindam
	51: {Mult: 10, Add: 17}, // lightmaxdam
	52: {Mult: 20, Add: 196}, // magicmindam
	53: {Mult: 20, Add: 183}, // magicmaxdam
	54: {Mult: 512, Add: 451}, // coldmindam
	55: {Mult: 340, Add: 128}, // coldmaxdam
	56: {Mult: 4, Add: 77}, // coldlength
	57: {Mult: 28, Add: 12}, // poisonmindam
	58: {Mult: 34, Add: 11}, // poisonmaxdam
	59: {Mult: 4, Add: 0}, // poisonlength
	60: {Mult: 341, Add: 1044}, // lifedrainmindam
	62: {Mult: 341, Add: 1179}, // manadrainmindam
	73: {Mult: 4, Add: 9}, // maxdurability
	74: {Mult: 410, Add: 451}, // hpregen
	75: {Mult: 10, Add: 117}, // item_maxdurability_percent
	76: {Mult: 204, Add: 32093}, // item_maxhp_percent
	77: {Mult: 204, Add: 56452}, // item_maxmana_percent
	78: {Mult: 128, Add: 112}, // item_attackertakesdamage
	79: {Mult: 34, Add: 187}, // item_goldbonus
	80: {Mult: 102, Add: 577}, // item_magicbonus
	81: {Mult: 0, Add: 105}, // item_knockback
	83: {Mult: 1560, Add: 49523}, // item_addclassskills
	85: {Mult: 519, Add: 36015}, // item_addexperience
	86: {Mult: 101, Add: 30}, // item_healafterkill
	87: {Mult: 203, Add: 18957}, // item_reducedprices
	89: {Mult: 51, Add: 15}, // item_lightradius
	90: {Mult: 0, Add: 155}, // item_lightcolor
	91: {Mult: -34, Add: 26}, // item_req_percent
	93: {Mult: 156, Add: 1042}, // item_fasterattackrate
	96: {Mult: 156, Add: 4083}, // item_fastermovevelocity
	97: {Mult: 327, Add: 181}, // item_nonclassskill
	98: {Mult: 64, Add: 415}, // state
	99: {Mult: 72, Add: 1065}, // item_fastergethitrate
	102: {Mult: 72, Add: 1484}, // item_fasterblockrate
	105: {Mult: 156, Add: 3876}, // item_fastercastrate
	107: {Mult: 256, Add: 181}, // item_singleskill
	108: {Mult: 0, Add: 1987}, // item_restinpeace
	109: {Mult: 33, Add: 159}, // curse_resistance
	110: {Mult: 10, Add: 27}, // item_poisonlengthresist
	111: {Mult: 100, Add: 94}, // item_normaldamage
	112: {Mult: 10, Add: 55}, // item_howl
	113: {Mult: 1024, Add: 332}, // item_stupidity
	114: {Mult: 20, Add: 43}, // item_damagetomana
	115: {Mult: 1024, Add: 1088}, // item_ignoretargetac
	116: {Mult: 20, Add: 67}, // item_fractionaltargetac
	117: {Mult: 50, Add: 48}, // item_preventheal
	118: {Mult: 988, Add: 5096}, // item_halffreezeduration
	119: {Mult: 40, Add: 981}, // item_tohit_percent
	120: {Mult: -20, Add: 24}, // item_damagetargetac
	121: {Mult: 12, Add: 19}, // item_demondamage_percent
	122: {Mult: 12, Add: 13}, // item_undeaddamage_percent
	123: {Mult: 7, Add: 15}, // item_demon_tohit
	124: {Mult: 7, Add: 11}, // item_undead_tohit
	125: {Mult: 1024, Add: 82}, // item_throwable
	126: {Mult: 1024, Add: 76}, // item_elemskill
	127: {Mult: 4096, Add: 15123}, // item_allskills
	128: {Mult: 102, Add: 4}, // item_attackertakeslightdamage
	134: {Mult: 12, Add: 666}, // item_freeze
	135: {Mult: 10, Add: 23}, // item_openwounds
	136: {Mult: 40, Add: 98}, // item_crushingblow
	137: {Mult: 51, Add: 77}, // item_kickdamage
	138: {Mult: 102, Add: 17}, // item_manaafterkill
	139: {Mult: 102, Add: 18}, // item_healafterdemonkill
	140: {Mult: 10, Add: 15}, // item_extrablood
	141: {Mult: 25, Add: 31}, // item_deadlystrike
	142: {Mult: 102, Add: 5486}, // item_absorbfire_percent
	143: {Mult: 204, Add: 1739}, // item_absorbfire
	144: {Mult: 102, Add: 5486}, // item_absorblight_percent
	145: {Mult: 204, Add: 1739}, // item_absorblight
	146: {Mult: 102, Add: 5486}, // item_absorbmagic_percent
	147: {Mult: 204, Add: 1739}, // item_absorbmagic
	148: {Mult: 102, Add: 5486}, // item_absorbcold_percent
	149: {Mult: 204, Add: 1739}, // item_absorbcold
	150: {Mult: 40, Add: 101}, // item_slow
	153: {Mult: 2048, Add: 15011}, // item_cannotbefrozen
	154: {Mult: 20, Add: 102}, // item_staminadrainpct
	156: {Mult: 2048, Add: 1924}, // item_pierce
	157: {Mult: 1024, Add: 511}, // item_magicarrow
	158: {Mult: 1536, Add: 492}, // item_explosivearrow
	159: {Mult: 128, Add: 76}, // item_throw_mindamage
	160: {Mult: 128, Add: 88}, // item_throw_maxdamage
	179: {Mult: 14, Add: 19}, // attack_vs_montype
	180: {Mult: 17, Add: 27}, // damage_vs_montype
	187: {Mult: 513, Add: 1432}, // item_pierce_cold_immunity
	188: {Mult: 768, Add: 11042}, // item_addskill_tab
	189: {Mult: 513, Add: 1432}, // item_pierce_fire_immunity
	190: {Mult: 513, Add: 1432}, // item_pierce_light_immunity
	191: {Mult: 513, Add: 1432}, // item_pierce_poison_immunity
	192: {Mult: 513, Add: 1432}, // item_pierce_damage_immunity
	193: {Mult: 513, Add: 1432}, // item_pierce_magic_immunity
	194: {Mult: 170, Add: 38}, // item_numsockets
	195: {Mult: 256, Add: 190}, // item_skillonattack
	196: {Mult: 19, Add: 85}, // item_skillonkill
	197: {Mult: 9, Add: 11}, // item_skillondeath
	198: {Mult: 256, Add: 190}, // item_skillonhit
	199: {Mult: 6, Add: 7}, // item_skillonlevelup
	200: {Mult: 256, Add: 106}, // item_charge_noconsume
	201: {Mult: 256, Add: 190}, // item_skillongethit
	204: {Mult: 256, Add: 401}, // item_charged_skill
	205: {Mult: 256, Add: 106}, // item_noconsume
	214: {Mult: 42, Add: 43}, // item_armor_perlevel
	215: {Mult: 100, Add: 87}, // item_armorpercent_perlevel
	216: {Mult: 64, Add: 92}, // item_hp_perlevel
	217: {Mult: 128, Add: 90}, // item_mana_perlevel
	218: {Mult: 204, Add: 54}, // item_maxdamage_perlevel
	219: {Mult: 100, Add: 86}, // item_maxdamage_percent_perlevel
	220: {Mult: 128, Add: 132}, // item_strength_perlevel
	221: {Mult: 128, Add: 132}, // item_dexterity_perlevel
	222: {Mult: 128, Add: 105}, // item_energy_perlevel
	223: {Mult: 128, Add: 105}, // item_vitality_perlevel
	224: {Mult: 20, Add: 53}, // item_tohit_perlevel
	225: {Mult: 256, Add: 10}, // item_tohitpercent_perlevel
	226: {Mult: 340, Add: 1058}, // item_cold_damagemax_perlevel
	227: {Mult: 128, Add: 49}, // item_fire_damagemax_perlevel
	228: {Mult: 128, Add: 49}, // item_ltng_damagemax_perlevel
	229: {Mult: 128, Add: 49}, // item_pois_damagemax_perlevel
	230: {Mult: 128, Add: 101}, // item_resist_cold_perlevel
	231: {Mult: 128, Add: 101}, // item_resist_fire_perlevel
	232: {Mult: 128, Add: 101}, // item_resist_ltng_perlevel
	233: {Mult: 128, Add: 101}, // item_resist_pois_perlevel
	234: {Mult: 340, Add: 207}, // item_absorb_cold_perlevel
	235: {Mult: 340, Add: 207}, // item_absorb_fire_perlevel
	236: {Mult: 340, Add: 207}, // item_absorb_ltng_perlevel
	237: {Mult: 340, Add: 207}, // item_absorb_pois_perlevel
	238: {Mult: 256, Add: 55}, // item_thorns_perlevel
	239: {Mult: 256, Add: 42}, // item_find_gold_perlevel
	240: {Mult: 1024, Add: 814}, // item_find_magic_perlevel
	241: {Mult: 256, Add: 79}, // item_regenstamina_perlevel
	242: {Mult: 64, Add: 104}, // item_stamina_perlevel
	243: {Mult: 10, Add: 56}, // item_damage_demon_perlevel
	244: {Mult: 10, Add: 91}, // item_damage_undead_perlevel
	245: {Mult: 10, Add: 55}, // item_tohit_demon_perlevel
	246: {Mult: 10, Add: 12}, // item_tohit_undead_perlevel
	247: {Mult: 1024, Add: 213}, // item_crushingblow_perlevel
	248: {Mult: 128, Add: 181}, // item_openwounds_perlevel
	249: {Mult: 128, Add: 104}, // item_kick_damage_perlevel
	250: {Mult: 512, Add: 118}, // item_deadlystrike_perlevel
	252: {Mult: 256, Add: 106}, // item_replenish_durability
	253: {Mult: 256, Add: 106}, // item_replenish_quantity
	254: {Mult: 10, Add: 99}, // item_extra_stack
	305: {Mult: 513, Add: 1432}, // item_pierce_cold
	306: {Mult: 497, Add: 1240}, // item_pierce_fire
	307: {Mult: 481, Add: 1187}, // item_pierce_ltng
	308: {Mult: 506, Add: 1322}, // item_pierce_pois
	329: {Mult: 415, Add: 1117}, // passive_fire_mastery
	330: {Mult: 408, Add: 1054}, // passive_ltng_mastery
	331: {Mult: 379, Add: 1295}, // passive_cold_mastery
	332: {Mult: 394, Add: 978}, // passive_pois_mastery
	333: {Mult: 2578, Add: 0}, // passive_fire_pierce
	334: {Mult: 2493, Add: 0}, // passive_ltng_pierce
	335: {Mult: 1984, Add: 0}, // passive_cold_pierce
	336: {Mult: 2345, Add: 0}, // passive_pois_pierce
	357: {Mult: 431, Add: 1211}, // passive_mag_mastery
	358: {Mult: 2812, Add: 0}, // passive_mag_pierce
	365: {Mult: 128, Add: 49}, // item_magic_damagemax_perlevel
	366: {Mult: 2812, Add: 0}, // passive_dmg_pierce
}

var vendorUniqueCostMods = map[int]vendorPriceCostMod{
	0: {Mult: 5, Add: 5000}, // The Gnasher
	1: {Mult: 5, Add: 5000}, // Deathspade
	2: {Mult: 5, Add: 5000}, // Bladebone
	3: {Mult: 5, Add: 5000}, // Mindrend
	4: {Mult: 5, Add: 5000}, // Rakescar
	5: {Mult: 5, Add: 5000}, // Fechmars Axe
	6: {Mult: 5, Add: 5000}, // Goreshovel
	7: {Mult: 5, Add: 5000}, // The Chieftan
	8: {Mult: 5, Add: 5000}, // Brainhew
	9: {Mult: 5, Add: 5000}, // The Humongous
	10: {Mult: 5, Add: 5000}, // Iros Torch
	11: {Mult: 5, Add: 5000}, // Maelstromwrath
	12: {Mult: 5, Add: 5000}, // Gravenspine
	13: {Mult: 5, Add: 5000}, // Umes Lament
	14: {Mult: 5, Add: 5000}, // Felloak
	15: {Mult: 5, Add: 5000}, // Knell Striker
	16: {Mult: 5, Add: 5000}, // Rusthandle
	17: {Mult: 5, Add: 5000}, // Stormeye
	18: {Mult: 5, Add: 5000}, // Stoutnail
	19: {Mult: 5, Add: 5000}, // Crushflange
	20: {Mult: 5, Add: 5000}, // Bloodrise
	21: {Mult: 5, Add: 5000}, // The Generals Tan Do Li Ga
	22: {Mult: 5, Add: 5000}, // Ironstone
	23: {Mult: 5, Add: 5000}, // Bonesob
	24: {Mult: 5, Add: 5000}, // Steeldriver
	25: {Mult: 5, Add: 5000}, // Rixots Keen
	26: {Mult: 5, Add: 5000}, // Blood Crescent
	27: {Mult: 5, Add: 5000}, // Krintizs Skewer
	28: {Mult: 5, Add: 5000}, // Gleamscythe
	29: {Mult: 5, Add: 5000}, // Azurewrath
	30: {Mult: 5, Add: 5000}, // Griswolds Edge
	31: {Mult: 5, Add: 5000}, // Hellplague
	32: {Mult: 5, Add: 5000}, // Culwens Point
	33: {Mult: 5, Add: 5000}, // Shadowfang
	34: {Mult: 5, Add: 5000}, // Soulflay
	35: {Mult: 5, Add: 5000}, // Kinemils Awl
	36: {Mult: 5, Add: 5000}, // Blacktongue
	37: {Mult: 5, Add: 5000}, // Ripsaw
	38: {Mult: 5, Add: 5000}, // The Patriarch
	39: {Mult: 5, Add: 5000}, // Gull
	40: {Mult: 5, Add: 5000}, // The Diggler
	41: {Mult: 5, Add: 5000}, // The Jade Tan Do
	42: {Mult: 5, Add: 5000}, // Irices Shard
	43: {Mult: 5, Add: 5000}, // The Dragon Chang
	44: {Mult: 5, Add: 5000}, // Razortine
	45: {Mult: 5, Add: 5000}, // Bloodthief
	46: {Mult: 5, Add: 5000}, // Lance of Yaggai
	47: {Mult: 5, Add: 5000}, // The Tannr Gorerod
	48: {Mult: 5, Add: 5000}, // Dimoaks Hew
	49: {Mult: 5, Add: 5000}, // Steelgoad
	50: {Mult: 5, Add: 5000}, // Soul Harvest
	51: {Mult: 5, Add: 5000}, // The Battlebranch
	52: {Mult: 5, Add: 5000}, // Woestave
	53: {Mult: 5, Add: 5000}, // The Grim Reaper
	54: {Mult: 5, Add: 5000}, // Bane Ash
	55: {Mult: 5, Add: 5000}, // Serpent Lord
	56: {Mult: 5, Add: 5000}, // Lazarus Spire
	57: {Mult: 5, Add: 5000}, // The Salamander
	58: {Mult: 5, Add: 5000}, // The Iron Jang Bong
	59: {Mult: 5, Add: 5000}, // Pluckeye
	60: {Mult: 5, Add: 5000}, // Witherstring
	61: {Mult: 5, Add: 5000}, // Rimeraven
	62: {Mult: 5, Add: 5000}, // Piercerib
	63: {Mult: 5, Add: 5000}, // Pullspite
	64: {Mult: 5, Add: 5000}, // Wizendraw
	65: {Mult: 5, Add: 5000}, // Hellclap
	66: {Mult: 5, Add: 5000}, // Blastbark
	67: {Mult: 5, Add: 5000}, // Leadcrow
	68: {Mult: 5, Add: 5000}, // Ichorsting
	69: {Mult: 5, Add: 5000}, // Hellcast
	70: {Mult: 5, Add: 5000}, // Doomspittle
	71: {Mult: 5, Add: 5000}, // War Bonnet
	72: {Mult: 5, Add: 5000}, // Tarnhelm
	73: {Mult: 5, Add: 5000}, // Coif of Glory
	74: {Mult: 5, Add: 5000}, // Duskdeep
	75: {Mult: 5, Add: 5000}, // Wormskull
	76: {Mult: 5, Add: 5000}, // Howltusk
	77: {Mult: 5, Add: 5000}, // Undead Crown
	78: {Mult: 5, Add: 5000}, // The Face of Horror
	79: {Mult: 5, Add: 5000}, // Greyform
	80: {Mult: 5, Add: 5000}, // Blinkbats Form
	81: {Mult: 5, Add: 5000}, // The Centurion
	82: {Mult: 5, Add: 5000}, // Twitchthroe
	83: {Mult: 5, Add: 5000}, // Darkglow
	84: {Mult: 5, Add: 5000}, // Hawkmail
	85: {Mult: 5, Add: 5000}, // Sparking Mail
	86: {Mult: 5, Add: 5000}, // Venomsward
	87: {Mult: 5, Add: 5000}, // Iceblink
	88: {Mult: 5, Add: 5000}, // Boneflesh
	89: {Mult: 5, Add: 5000}, // Rockfleece
	90: {Mult: 5, Add: 5000}, // Rattlecage
	91: {Mult: 5, Add: 5000}, // Goldskin
	92: {Mult: 5, Add: 5000}, // Victors Silk
	93: {Mult: 5, Add: 5000}, // Heavenly Garb
	94: {Mult: 5, Add: 5000}, // Pelta Lunata
	95: {Mult: 5, Add: 5000}, // Umbral Disk
	96: {Mult: 5, Add: 5000}, // Stormguild
	97: {Mult: 5, Add: 5000}, // Wall of the Eyeless
	98: {Mult: 5, Add: 5000}, // Swordback Hold
	99: {Mult: 5, Add: 5000}, // Steelclash
	100: {Mult: 5, Add: 5000}, // Bverrit Keep
	101: {Mult: 5, Add: 5000}, // The Ward
	102: {Mult: 5, Add: 5000}, // The Hand of Broc
	103: {Mult: 5, Add: 5000}, // Bloodfist
	104: {Mult: 5, Add: 5000}, // Chance Guards
	105: {Mult: 5, Add: 5000}, // Magefist
	106: {Mult: 5, Add: 5000}, // Frostburn
	107: {Mult: 5, Add: 5000}, // Hotspur
	108: {Mult: 5, Add: 5000}, // Gorefoot
	109: {Mult: 5, Add: 5000}, // Treads of Cthon
	110: {Mult: 5, Add: 5000}, // Goblin Toe
	111: {Mult: 5, Add: 5000}, // Tearhaunch
	112: {Mult: 5, Add: 5000}, // Lenyms Cord
	113: {Mult: 5, Add: 5000}, // Snakecord
	114: {Mult: 5, Add: 5000}, // Nightsmoke
	115: {Mult: 5, Add: 5000}, // Goldwrap
	116: {Mult: 5, Add: 5000}, // Bladebuckle
	117: {Mult: 5, Add: 5000}, // Nokozan Relic
	118: {Mult: 5, Add: 5000}, // The Eye of Etlich
	119: {Mult: 5, Add: 5000}, // The Mahim-Oak Curio
	120: {Mult: 5, Add: 5000}, // Nagelring
	121: {Mult: 5, Add: 5000}, // Manald Heal
	122: {Mult: 5, Add: 5000}, // The Stone of Jordan
	123: {Mult: 5, Add: 5000}, // Amulet of the Viper
	124: {Mult: 5, Add: 5000}, // Staff of Kings
	125: {Mult: 5, Add: 5000}, // Horadric Staff
	126: {Mult: 5, Add: 5000}, // Hell Forge Hammer
	127: {Mult: 5, Add: 5000}, // KhalimFlail
	128: {Mult: 5, Add: 5000}, // SuperKhalimFlail
	129: {Mult: 5, Add: 5000}, // Coldkill
	130: {Mult: 5, Add: 5000}, // Butcher's Pupil
	131: {Mult: 5, Add: 5000}, // Islestrike
	132: {Mult: 5, Add: 5000}, // Pompe's Wrath
	133: {Mult: 5, Add: 5000}, // Guardian Naga
	134: {Mult: 5, Add: 5000}, // Warlord's Trust
	135: {Mult: 5, Add: 5000}, // Spellsteel
	136: {Mult: 5, Add: 5000}, // Stormrider
	137: {Mult: 5, Add: 5000}, // Boneslayer Blade
	138: {Mult: 5, Add: 5000}, // The Minataur
	139: {Mult: 5, Add: 5000}, // Suicide Branch
	140: {Mult: 5, Add: 5000}, // Carin Shard
	141: {Mult: 5, Add: 5000}, // Arm of King Leoric
	142: {Mult: 5, Add: 5000}, // Blackhand Key
	143: {Mult: 5, Add: 5000}, // Dark Clan Crusher
	144: {Mult: 5, Add: 5000}, // Zakarum's Hand
	145: {Mult: 5, Add: 5000}, // The Fetid Sprinkler
	146: {Mult: 5, Add: 5000}, // Hand of Blessed Light
	147: {Mult: 5, Add: 5000}, // Fleshrender
	148: {Mult: 5, Add: 5000}, // Sureshrill Frost
	149: {Mult: 5, Add: 5000}, // Moonfall
	150: {Mult: 5, Add: 5000}, // Baezil's Vortex
	151: {Mult: 5, Add: 5000}, // Earthshaker
	152: {Mult: 5, Add: 5000}, // Bloodtree Stump
	153: {Mult: 5, Add: 5000}, // The Gavel of Pain
	154: {Mult: 5, Add: 5000}, // Bloodletter
	155: {Mult: 5, Add: 5000}, // Coldsteel Eye
	156: {Mult: 5, Add: 5000}, // Hexfire
	157: {Mult: 5, Add: 5000}, // Blade of Ali Baba
	158: {Mult: 5, Add: 5000}, // Ginther's Rift
	159: {Mult: 5, Add: 5000}, // Headstriker
	160: {Mult: 5, Add: 5000}, // Plague Bearer
	161: {Mult: 5, Add: 5000}, // The Atlantian
	162: {Mult: 5, Add: 5000}, // Crainte Vomir
	163: {Mult: 5, Add: 5000}, // Bing Sz Wang
	164: {Mult: 5, Add: 5000}, // The Vile Husk
	165: {Mult: 5, Add: 5000}, // Cloudcrack
	166: {Mult: 5, Add: 5000}, // Todesfaelle Flamme
	167: {Mult: 5, Add: 5000}, // Swordguard
	168: {Mult: 5, Add: 5000}, // Spineripper
	169: {Mult: 5, Add: 5000}, // Heart Carver
	170: {Mult: 5, Add: 5000}, // Blackbog's Sharp
	171: {Mult: 5, Add: 5000}, // Stormspike
	172: {Mult: 5, Add: 5000}, // The Impaler
	173: {Mult: 5, Add: 5000}, // Kelpie Snare
	174: {Mult: 5, Add: 5000}, // Soulfeast Tine
	175: {Mult: 5, Add: 5000}, // Hone Sundan
	176: {Mult: 5, Add: 5000}, // Spire of Honor
	177: {Mult: 5, Add: 5000}, // The Meat Scraper
	178: {Mult: 5, Add: 5000}, // Blackleach Blade
	179: {Mult: 5, Add: 5000}, // Athena's Wrath
	180: {Mult: 5, Add: 5000}, // Pierre Tombale Couant
	181: {Mult: 5, Add: 5000}, // Husoldal Evo
	182: {Mult: 5, Add: 5000}, // Grim's Burning Dead
	183: {Mult: 5, Add: 5000}, // Razorswitch
	184: {Mult: 5, Add: 5000}, // Ribcracker
	185: {Mult: 5, Add: 5000}, // Chromatic Ire
	186: {Mult: 5, Add: 5000}, // Warpspear
	187: {Mult: 5, Add: 5000}, // Skullcollector
	188: {Mult: 5, Add: 5000}, // Skystrike
	189: {Mult: 5, Add: 5000}, // Riphook
	190: {Mult: 5, Add: 5000}, // Kuko Shakaku
	191: {Mult: 5, Add: 5000}, // Endlesshail
	192: {Mult: 5, Add: 5000}, // Whichwild String
	193: {Mult: 5, Add: 5000}, // Cliffkiller
	194: {Mult: 5, Add: 5000}, // Magewrath
	195: {Mult: 5, Add: 5000}, // Godstrike Arch
	196: {Mult: 5, Add: 5000}, // Langer Briser
	197: {Mult: 5, Add: 5000}, // Pus Spiter
	198: {Mult: 5, Add: 5000}, // Buriza-Do Kyanon
	199: {Mult: 5, Add: 5000}, // Demon Machine
	201: {Mult: 3, Add: 5000}, // Peasent Crown
	202: {Mult: 3, Add: 5000}, // Rockstopper
	203: {Mult: 3, Add: 5000}, // Stealskull
	204: {Mult: 3, Add: 5000}, // Darksight Helm
	205: {Mult: 3, Add: 5000}, // Valkiry Wing
	206: {Mult: 3, Add: 5000}, // Crown of Thieves
	207: {Mult: 3, Add: 5000}, // Blackhorn's Face
	208: {Mult: 3, Add: 5000}, // Vampiregaze
	209: {Mult: 3, Add: 5000}, // The Spirit Shroud
	210: {Mult: 3, Add: 5000}, // Skin of the Vipermagi
	211: {Mult: 3, Add: 5000}, // Skin of the Flayerd One
	212: {Mult: 3, Add: 5000}, // Ironpelt
	213: {Mult: 3, Add: 5000}, // Spiritforge
	214: {Mult: 3, Add: 5000}, // Crow Caw
	215: {Mult: 3, Add: 5000}, // Shaftstop
	216: {Mult: 3, Add: 5000}, // Duriel's Shell
	217: {Mult: 3, Add: 5000}, // Skullder's Ire
	218: {Mult: 3, Add: 5000}, // Guardian Angel
	219: {Mult: 3, Add: 5000}, // Toothrow
	220: {Mult: 3, Add: 5000}, // Atma's Wail
	221: {Mult: 3, Add: 5000}, // Black Hades
	222: {Mult: 3, Add: 5000}, // Corpsemourn
	223: {Mult: 3, Add: 5000}, // Que-Hegan's Wisdon
	224: {Mult: 3, Add: 5000}, // Visceratuant
	225: {Mult: 3, Add: 5000}, // Mosers Blessed Circle
	226: {Mult: 3, Add: 5000}, // Stormchaser
	227: {Mult: 3, Add: 5000}, // Tiamat's Rebuke
	228: {Mult: 3, Add: 5000}, // Kerke's Sanctuary
	229: {Mult: 3, Add: 5000}, // Radimant's Sphere
	230: {Mult: 3, Add: 5000}, // Lidless Wall
	231: {Mult: 3, Add: 5000}, // Lance Guard
	232: {Mult: 5, Add: 5000}, // Venom Grip
	233: {Mult: 5, Add: 5000}, // Gravepalm
	234: {Mult: 5, Add: 5000}, // Ghoulhide
	235: {Mult: 5, Add: 5000}, // Lavagout
	236: {Mult: 5, Add: 5000}, // Hellmouth
	237: {Mult: 5, Add: 5000}, // Infernostride
	238: {Mult: 5, Add: 5000}, // Waterwalk
	239: {Mult: 5, Add: 5000}, // Silkweave
	240: {Mult: 5, Add: 5000}, // Wartraveler
	241: {Mult: 5, Add: 5000}, // Gorerider
	242: {Mult: 5, Add: 5000}, // String of Ears
	243: {Mult: 5, Add: 5000}, // Razortail
	244: {Mult: 5, Add: 5000}, // Gloomstrap
	245: {Mult: 5, Add: 5000}, // Snowclash
	246: {Mult: 5, Add: 5000}, // Thudergod's Vigor
	248: {Mult: 3, Add: 5000}, // Harlequin Crest
	249: {Mult: 3, Add: 5000}, // Veil of Steel
	250: {Mult: 3, Add: 5000}, // The Gladiator's Bane
	251: {Mult: 3, Add: 5000}, // Arkaine's Valor
	252: {Mult: 3, Add: 5000}, // Blackoak Shield
	253: {Mult: 3, Add: 5000}, // Stormshield
	254: {Mult: 5, Add: 5000}, // Hellslayer
	255: {Mult: 5, Add: 5000}, // Messerschmidt's Reaver
	256: {Mult: 5, Add: 5000}, // Baranar's Star
	257: {Mult: 5, Add: 5000}, // Schaefer's Hammer
	258: {Mult: 5, Add: 5000}, // The Cranium Basher
	259: {Mult: 5, Add: 5000}, // Lightsabre
	260: {Mult: 5, Add: 5000}, // Doombringer
	261: {Mult: 5, Add: 5000}, // The Grandfather
	262: {Mult: 5, Add: 5000}, // Wizardspike
	263: {Mult: 5, Add: 5000}, // Constricting Ring
	264: {Mult: 5, Add: 5000}, // Stormspire
	265: {Mult: 5, Add: 5000}, // Eaglehorn
	266: {Mult: 5, Add: 5000}, // Windforce
	268: {Mult: 5, Add: 5000}, // Bul Katho's Wedding Band
	269: {Mult: 5, Add: 5000}, // The Cat's Eye
	270: {Mult: 5, Add: 5000}, // The Rising Sun
	271: {Mult: 5, Add: 5000}, // Crescent Moon
	272: {Mult: 5, Add: 5000}, // Mara's Kaleidoscope
	273: {Mult: 5, Add: 5000}, // Atma's Scarab
	274: {Mult: 5, Add: 5000}, // Dwarf Star
	275: {Mult: 5, Add: 5000}, // Raven Frost
	276: {Mult: 5, Add: 5000}, // Highlord's Wrath
	277: {Mult: 5, Add: 5000}, // Saracen's Chance
	279: {Mult: 5, Add: 5000}, // Arreat's Face
	280: {Mult: 5, Add: 5000}, // Homunculus
	281: {Mult: 5, Add: 5000}, // Titan's Revenge
	282: {Mult: 5, Add: 5000}, // Lycander's Aim
	283: {Mult: 5, Add: 5000}, // Lycander's Flank
	284: {Mult: 5, Add: 5000}, // The Oculus
	285: {Mult: 5, Add: 5000}, // Herald of Zakarum
	286: {Mult: 5, Add: 5000}, // Cutthroat1
	287: {Mult: 5, Add: 5000}, // Jalal's Mane
	288: {Mult: 5, Add: 5000}, // The Scalper
	289: {Mult: 5, Add: 5000}, // Bloodmoon
	290: {Mult: 5, Add: 5000}, // Djinnslayer
	291: {Mult: 5, Add: 5000}, // Deathbit
	292: {Mult: 5, Add: 5000}, // Warshrike
	293: {Mult: 5, Add: 5000}, // Gutsiphon
	294: {Mult: 5, Add: 5000}, // Razoredge
	295: {Mult: 5, Add: 5000}, // Gore Ripper
	296: {Mult: 5, Add: 5000}, // Demonlimb
	297: {Mult: 5, Add: 5000}, // Steelshade
	298: {Mult: 5, Add: 5000}, // Tomb Reaver
	299: {Mult: 5, Add: 5000}, // Deaths's Web
	300: {Mult: 5, Add: 5000}, // Nature's Peace
	301: {Mult: 5, Add: 5000}, // Azurewrath
	302: {Mult: 5, Add: 5000}, // Seraph's Hymn
	303: {Mult: 5, Add: 5000}, // Zakarum's Salvation
	304: {Mult: 5, Add: 5000}, // Fleshripper
	305: {Mult: 5, Add: 5000}, // Odium
	306: {Mult: 5, Add: 5000}, // Horizon's Tornado
	307: {Mult: 5, Add: 5000}, // Stone Crusher
	308: {Mult: 5, Add: 5000}, // Jadetalon
	309: {Mult: 5, Add: 5000}, // Shadowdancer
	310: {Mult: 5, Add: 5000}, // Cerebus
	311: {Mult: 5, Add: 5000}, // Tyrael's Might
	312: {Mult: 5, Add: 5000}, // Souldrain
	313: {Mult: 5, Add: 5000}, // Runemaster
	314: {Mult: 5, Add: 5000}, // Deathcleaver
	315: {Mult: 5, Add: 5000}, // Executioner's Justice
	316: {Mult: 5, Add: 5000}, // Stoneraven
	317: {Mult: 5, Add: 5000}, // Leviathan
	318: {Mult: 5, Add: 5000}, // Larzuk's Champion
	319: {Mult: 5, Add: 5000}, // Wisp
	320: {Mult: 5, Add: 5000}, // Gargoyle's Bite
	321: {Mult: 5, Add: 5000}, // Lacerator
	322: {Mult: 5, Add: 5000}, // Mang Song's Lesson
	323: {Mult: 5, Add: 5000}, // Viperfork
	324: {Mult: 5, Add: 5000}, // Ethereal Edge
	325: {Mult: 5, Add: 5000}, // Demonhorn's Edge
	326: {Mult: 5, Add: 5000}, // The Reaper's Toll
	327: {Mult: 5, Add: 5000}, // Spiritkeeper
	328: {Mult: 5, Add: 5000}, // Hellrack
	329: {Mult: 5, Add: 5000}, // Alma Negra
	330: {Mult: 5, Add: 5000}, // Darkforge Spawn
	331: {Mult: 5, Add: 5000}, // Widowmaker
	332: {Mult: 5, Add: 5000}, // Bloodraven's Charge
	333: {Mult: 5, Add: 5000}, // Ghostflame
	334: {Mult: 5, Add: 5000}, // Shadowkiller
	335: {Mult: 5, Add: 5000}, // Gimmershred
	336: {Mult: 5, Add: 5000}, // Griffon's Eye
	337: {Mult: 5, Add: 5000}, // Windhammer
	338: {Mult: 5, Add: 5000}, // Thunderstroke
	339: {Mult: 5, Add: 5000}, // Giantmaimer
	340: {Mult: 5, Add: 5000}, // Demon's Arch
	341: {Mult: 3, Add: 5000}, // Boneflame
	342: {Mult: 3, Add: 5000}, // Steelpillar
	343: {Mult: 3, Add: 5000}, // Nightwing's Veil
	344: {Mult: 3, Add: 5000}, // Crown of Ages
	345: {Mult: 3, Add: 5000}, // Andariel's Visage
	346: {Mult: 5, Add: 5000}, // Darkfear
	347: {Mult: 3, Add: 5000}, // Dragonscale
	348: {Mult: 3, Add: 5000}, // Steel Carapice
	349: {Mult: 3, Add: 5000}, // Medusa's Gaze
	350: {Mult: 3, Add: 5000}, // Ravenlore
	351: {Mult: 3, Add: 5000}, // Boneshade
	352: {Mult: 3, Add: 5000}, // Nethercrow
	353: {Mult: 3, Add: 5000}, // Flamebellow
	354: {Mult: 3, Add: 5000}, // Fathom
	355: {Mult: 3, Add: 5000}, // Wolfhowl
	356: {Mult: 3, Add: 5000}, // Spirit Ward
	357: {Mult: 3, Add: 5000}, // Kira's Guardian
	358: {Mult: 3, Add: 5000}, // Ormus' Robes
	359: {Mult: 3, Add: 5000}, // Gheed's Fortune
	360: {Mult: 3, Add: 5000}, // Stormlash
	361: {Mult: 3, Add: 5000}, // Halaberd's Reign
	362: {Mult: 3, Add: 5000}, // Warriv's Warder
	363: {Mult: 5, Add: 5000}, // Spike Thorn
	364: {Mult: 5, Add: 5000}, // Dracul's Grasp
	365: {Mult: 5, Add: 5000}, // Frostwind
	366: {Mult: 5, Add: 5000}, // Templar's Might
	367: {Mult: 5, Add: 5000}, // Eschuta's temper
	368: {Mult: 5, Add: 5000}, // Firelizard's Talons
	369: {Mult: 5, Add: 5000}, // Sandstorm Trek
	370: {Mult: 5, Add: 5000}, // Marrowwalk
	371: {Mult: 5, Add: 5000}, // Heaven's Light
	373: {Mult: 5, Add: 5000}, // Arachnid Mesh
	374: {Mult: 5, Add: 5000}, // Nosferatu's Coil
	375: {Mult: 5, Add: 5000}, // Metalgrid
	376: {Mult: 5, Add: 5000}, // Verdugo's Hearty Cord
	377: {Mult: 5, Add: 5000}, // Sigurd's Staunch
	378: {Mult: 3, Add: 5000}, // Carrion Wind
	379: {Mult: 3, Add: 5000}, // Giantskull
	380: {Mult: 3, Add: 5000}, // Ironward
	381: {Mult: 3, Add: 5000}, // Annihilus
	382: {Mult: 3, Add: 5000}, // Arioc's Needle
	383: {Mult: 3, Add: 5000}, // Cranebeak
	384: {Mult: 3, Add: 5000}, // Nord's Tenderizer
	385: {Mult: 3, Add: 5000}, // Earthshifter
	386: {Mult: 3, Add: 5000}, // Wraithflight
	387: {Mult: 3, Add: 5000}, // Bonehew
	388: {Mult: 3, Add: 5000}, // Ondal's Wisdom
	389: {Mult: 3, Add: 5000}, // The Reedeemer
	390: {Mult: 3, Add: 5000}, // Headhunter's Glory
	391: {Mult: 3, Add: 5000}, // Steelrend
	392: {Mult: 3, Add: 5000}, // Rainbow Facet
	393: {Mult: 3, Add: 5000}, // Rainbow Facet
	394: {Mult: 3, Add: 5000}, // Rainbow Facet
	395: {Mult: 3, Add: 5000}, // Rainbow Facet
	396: {Mult: 3, Add: 5000}, // Rainbow Facet
	397: {Mult: 3, Add: 5000}, // Rainbow Facet
	398: {Mult: 3, Add: 5000}, // Rainbow Facet
	399: {Mult: 3, Add: 5000}, // Rainbow Facet
	400: {Mult: 3, Add: 5000}, // Hellfire Torch
	401: {Mult: 3, Add: 5000}, // Cold Rupture
	402: {Mult: 3, Add: 5000}, // Flame Rift
	403: {Mult: 3, Add: 5000}, // Crack of the Heavens
	404: {Mult: 3, Add: 5000}, // Rotting Fissure
	405: {Mult: 3, Add: 5000}, // Bone Break
	406: {Mult: 3, Add: 5000}, // Black Cleft
}

var vendorSetCostMods = map[int]vendorPriceCostMod{
	69: {Mult: 5, Add: 5000}, // Aldur's Advance
	67: {Mult: 5, Add: 5000}, // Aldur's Deception
	68: {Mult: 5, Add: 5000}, // Aldur's Gauntlet
	66: {Mult: 5, Add: 5000}, // Aldur's Stony Gaze
	52: {Mult: 5, Add: 2500}, // Angelic Halo
	51: {Mult: 5, Add: 2500}, // Angelic Mantle
	50: {Mult: 5, Add: 2500}, // Angelic Sickle
	53: {Mult: 5, Add: 2500}, // Angelic Wings
	59: {Mult: 5, Add: 2500}, // Arcanna's Deathwand
	61: {Mult: 5, Add: 2500}, // Arcanna's Flesh
	60: {Mult: 5, Add: 2500}, // Arcanna's Head
	58: {Mult: 5, Add: 2500}, // Arcanna's Sign
	56: {Mult: 5, Add: 2500}, // Arctic Binding
	55: {Mult: 5, Add: 2500}, // Arctic Furs
	54: {Mult: 5, Add: 2500}, // Arctic Horn
	57: {Mult: 5, Add: 2500}, // Arctic Mitts
	46: {Mult: 5, Add: 2500}, // Berserker's Hatchet
	45: {Mult: 5, Add: 2500}, // Berserker's Hauberk
	44: {Mult: 5, Add: 2500}, // Berserker's Headgear
	115: {Mult: 5, Add: 5000}, // Bul-Kathos' Sacred Charge
	116: {Mult: 5, Add: 5000}, // Bul-Kathos' Tribal Guardian
	26: {Mult: 5, Add: 2500}, // Cathan's Mesh
	25: {Mult: 5, Add: 2500}, // Cathan's Rule
	29: {Mult: 5, Add: 2500}, // Cathan's Seal
	28: {Mult: 5, Add: 2500}, // Cathan's Sigil
	27: {Mult: 5, Add: 2500}, // Cathan's Visage
	2: {Mult: 5, Add: 2500}, // Civerb's Cudgel
	1: {Mult: 5, Add: 2500}, // Civerb's Icon
	0: {Mult: 5, Add: 2500}, // Civerb's Ward
	7: {Mult: 5, Add: 2500}, // Cleglaw's Claw
	8: {Mult: 5, Add: 2500}, // Cleglaw's Pincers
	6: {Mult: 5, Add: 2500}, // Cleglaw's Tooth
	118: {Mult: 5, Add: 5000}, // Cow King's Hide
	119: {Mult: 5, Add: 5000}, // Cow King's Hoofs
	117: {Mult: 5, Add: 5000}, // Cow King's Horns
	99: {Mult: 5, Add: 5000}, // Credendum
	100: {Mult: 5, Add: 5000}, // Dangoon's Teaching
	48: {Mult: 5, Add: 2500}, // Death's Guard
	47: {Mult: 5, Add: 2500}, // Death's Hand
	49: {Mult: 5, Add: 2500}, // Death's Touch
	82: {Mult: 5, Add: 5000}, // Griswold's Heart
	84: {Mult: 5, Add: 5000}, // Griswold's Honor
	81: {Mult: 5, Add: 5000}, // Griswold's Valor
	83: {Mult: 5, Add: 5000}, // Griswolds's Redemption
	104: {Mult: 5, Add: 5000}, // Guillaume's Face
	102: {Mult: 5, Add: 5000}, // Haemosu's Adament
	101: {Mult: 5, Add: 5000}, // Heaven's Taebaek
	4: {Mult: 5, Add: 2500}, // Hsarus' Iron Fist
	3: {Mult: 5, Add: 2500}, // Hsarus' Iron Heel
	5: {Mult: 5, Add: 2500}, // Hsarus' Iron Stay
	111: {Mult: 5, Add: 5000}, // Hwanin's Justice
	109: {Mult: 5, Add: 5000}, // Hwanin's Refuge
	110: {Mult: 5, Add: 5000}, // Hwanin's Seal
	108: {Mult: 5, Add: 5000}, // Hwanin's Splendor
	72: {Mult: 5, Add: 5000}, // Immortal King's Detail
	73: {Mult: 5, Add: 5000}, // Immortal King's Forge
	74: {Mult: 5, Add: 5000}, // Immortal King's Pillar
	71: {Mult: 5, Add: 5000}, // Immortal King's Soul Cage
	75: {Mult: 5, Add: 5000}, // Immortal King's Stone Crusher
	70: {Mult: 5, Add: 5000}, // Immortal King's Will
	41: {Mult: 5, Add: 2500}, // Infernal Cranium
	43: {Mult: 5, Add: 2500}, // Infernal Sign
	42: {Mult: 5, Add: 2500}, // Infernal Torch
	11: {Mult: 5, Add: 2500}, // Iratha's Coil
	9: {Mult: 5, Add: 2500}, // Iratha's Collar
	12: {Mult: 5, Add: 2500}, // Iratha's Cord
	10: {Mult: 5, Add: 2500}, // Iratha's Cuff
	15: {Mult: 5, Add: 2500}, // Isenhart's Case
	16: {Mult: 5, Add: 2500}, // Isenhart's Horns
	13: {Mult: 5, Add: 2500}, // Isenhart's Lightbrand
	14: {Mult: 5, Add: 2500}, // Isenhart's Parry
	96: {Mult: 5, Add: 5000}, // Laying of Hands
	94: {Mult: 5, Add: 5000}, // M'avina's Caster
	91: {Mult: 5, Add: 5000}, // M'avina's Embrace
	92: {Mult: 5, Add: 5000}, // M'avina's Icy Clutch
	93: {Mult: 5, Add: 5000}, // M'avina's Tenet
	90: {Mult: 5, Add: 5000}, // M'avina's True Sight
	106: {Mult: 5, Add: 5000}, // Magnus' Skin
	123: {Mult: 5, Add: 5000}, // McAuley's Paragon
	124: {Mult: 5, Add: 5000}, // McAuley's Riprap
	126: {Mult: 5, Add: 5000}, // McAuley's Superstition
	125: {Mult: 5, Add: 5000}, // McAuley's Taboo
	23: {Mult: 5, Add: 2500}, // Milabrega's Diadem
	21: {Mult: 5, Add: 2500}, // Milabrega's Orb
	24: {Mult: 5, Add: 2500}, // Milabrega's Robe
	22: {Mult: 5, Add: 2500}, // Milabrega's Rod
	122: {Mult: 5, Add: 5000}, // Naj's Circlet
	121: {Mult: 5, Add: 5000}, // Naj's Light Plate
	120: {Mult: 5, Add: 5000}, // Naj's Puzzler
	63: {Mult: 5, Add: 5000}, // Natalya's Mark
	64: {Mult: 5, Add: 5000}, // Natalya's Shadow
	65: {Mult: 5, Add: 5000}, // Natalya's Soul
	62: {Mult: 5, Add: 5000}, // Natalya's Totem
	103: {Mult: 5, Add: 5000}, // Ondal's Almighty
	97: {Mult: 5, Add: 5000}, // Rite of Passage
	112: {Mult: 5, Add: 5000}, // Sazabi's Cobalt Redeemer
	113: {Mult: 5, Add: 5000}, // Sazabi's Ghost Liberator
	114: {Mult: 5, Add: 5000}, // Sazabi's Mental Sheath
	35: {Mult: 5, Add: 2500}, // Sigon's Gage
	40: {Mult: 5, Add: 2500}, // Sigon's Guard
	38: {Mult: 5, Add: 2500}, // Sigon's Sabot
	37: {Mult: 5, Add: 2500}, // Sigon's Shelter
	36: {Mult: 5, Add: 2500}, // Sigon's Visor
	39: {Mult: 5, Add: 2500}, // Sigon's Wrap
	98: {Mult: 5, Add: 5000}, // Spiritual Custodian
	77: {Mult: 5, Add: 5000}, // Tal Rasha's Adjudication
	76: {Mult: 5, Add: 5000}, // Tal Rasha's Fire-Spun Cloth
	80: {Mult: 5, Add: 5000}, // Tal Rasha's Horadric Crest
	79: {Mult: 5, Add: 5000}, // Tal Rasha's Howling Wind
	78: {Mult: 5, Add: 5000}, // Tal Rasha's Lidless Eye
	30: {Mult: 5, Add: 2500}, // Tancred's Crowbill
	32: {Mult: 5, Add: 2500}, // Tancred's Hobnails
	34: {Mult: 5, Add: 2500}, // Tancred's Skull
	31: {Mult: 5, Add: 2500}, // Tancred's Spine
	33: {Mult: 5, Add: 2500}, // Tancred's Weird
	95: {Mult: 5, Add: 5000}, // Telling of Beads
	88: {Mult: 5, Add: 5000}, // Trang-Oul's Claws
	89: {Mult: 5, Add: 5000}, // Trang-Oul's Girth
	85: {Mult: 5, Add: 5000}, // Trang-Oul's Guise
	86: {Mult: 5, Add: 5000}, // Trang-Oul's Scales
	87: {Mult: 5, Add: 5000}, // Trang-Oul's Wing
	19: {Mult: 5, Add: 2500}, // Vidala's Ambush
	17: {Mult: 5, Add: 2500}, // Vidala's Barb
	18: {Mult: 5, Add: 2500}, // Vidala's Fetlock
	20: {Mult: 5, Add: 2500}, // Vidala's Snare
	131: {Mult: 5, Add: 5000}, // Warlord's Authority
	127: {Mult: 5, Add: 5000}, // Warlord's Conquest
	130: {Mult: 5, Add: 5000}, // Warlord's Crushers
	128: {Mult: 5, Add: 5000}, // Warlord's Lust
	129: {Mult: 5, Add: 5000}, // Warlord's Mantle
	107: {Mult: 5, Add: 5000}, // Wihtstan's Guard
	105: {Mult: 5, Add: 5000}, // Wilhelm's Pride
}

var vendorMagicPrefixCostMods = map[int]vendorPriceCostMod{
	1064: {Mult: 1280, Add: 500}, // Shimmering
	1065: {Mult: 1280, Add: 1500}, // Shimmering
	1066: {Mult: 1280, Add: 5000}, // Shimmering
	1067: {Mult: 1280, Add: 1500}, // Shimmering
	1068: {Mult: 1280, Add: 5000}, // Shimmering
	1069: {Mult: 1280, Add: 10000}, // Shimmering
	1070: {Mult: 1280, Add: 500}, // Shimmering
	1071: {Mult: 1280, Add: 2000}, // Rainbow
	1072: {Mult: 1280, Add: 4000}, // Scintillating
	1073: {Mult: 1280, Add: 8000}, // Prismatic
	1074: {Mult: 1280, Add: 16000}, // Chromatic
	1075: {Mult: 1280, Add: 3000}, // Shimmering
	1076: {Mult: 1280, Add: 6000}, // Rainbow
	1077: {Mult: 1280, Add: 12000}, // Scintillating
	1078: {Mult: 1280, Add: 24000}, // Prismatic
	1079: {Mult: 1280, Add: 32000}, // Chromatic
	1080: {Mult: 1280, Add: 4000}, // Shimmering
	1081: {Mult: 1280, Add: 8000}, // Rainbow
	1082: {Mult: 1280, Add: 16000}, // Scintillating
	1083: {Mult: 1280, Add: 3000}, // Shimmering
	1084: {Mult: 1280, Add: 8000}, // Scintillating
}

var vendorMagicSuffixCostMods = map[int]vendorPriceCostMod{
}

var vendorItemBaseCostByCode = map[string]int{
	"0sc": 100,
	"2ax": 873,
	"2hs": 644,
	"6bs": 7986,
	"6cb": 17743,
	"6cs": 9048,
	"6hb": 21350,
	"6hx": 26065,
	"6l7": 18105,
	"6lb": 25610,
	"6ls": 5367,
	"6lw": 19375,
	"6lx": 19512,
	"6mx": 22305,
	"6rx": 25851,
	"6s7": 24605,
	"6sb": 21091,
	"6ss": 8154,
	"6sw": 15042,
	"6ws": 10001,
	"72a": 13496,
	"72h": 23789,
	"7ar": 16205,
	"7ax": 15781,
	"7b7": 19939,
	"7b8": 251,
	"7ba": 18956,
	"7bk": 248,
	"7bl": 16076,
	"7br": 14658,
	"7bs": 19044,
	"7bt": 16377,
	"7bw": 9208,
	"7cl": 1488,
	"7cm": 14391,
	"7cr": 25680,
	"7cs": 15537,
	"7dg": 12429,
	"7di": 17988,
	"7fb": 24800,
	"7fc": 18479,
	"7fl": 19532,
	"7ga": 19245,
	"7gd": 22259,
	"7gi": 18431,
	"7gl": 241,
	"7gm": 17821,
	"7gs": 16331,
	"7gw": 7687,
	"7h7": 21554,
	"7ha": 14033,
	"7ja": 211,
	"7kr": 13729,
	"7la": 17430,
	"7ls": 14306,
	"7lw": 16035,
	"7m7": 12156,
	"7ma": 14765,
	"7mp": 16108,
	"7mt": 15674,
	"7o7": 9871,
	"7p7": 16070,
	"7pa": 16509,
	"7pi": 220,
	"7qr": 18002,
	"7qs": 23501,
	"7s7": 232,
	"7s8": 11086,
	"7sb": 22785,
	"7sc": 20199,
	"7sm": 16103,
	"7sp": 6793,
	"7sr": 15900,
	"7ss": 15680,
	"7st": 17248,
	"7ta": 230,
	"7tk": 215,
	"7tr": 17347,
	"7ts": 199,
	"7tw": 17396,
	"7vo": 12822,
	"7wa": 15957,
	"7wb": 16996,
	"7wc": 17692,
	"7wd": 20187,
	"7wh": 14388,
	"7wn": 9412,
	"7ws": 22870,
	"7xf": 15784,
	"7yw": 8786,
	"8bs": 3969,
	"8cb": 2481,
	"8cs": 2598,
	"8hb": 1350,
	"8hx": 12588,
	"8l8": 4935,
	"8lb": 1770,
	"8ls": 1689,
	"8lw": 11025,
	"8lx": 2709,
	"8mx": 7335,
	"8rx": 8517,
	"8s8": 3435,
	"8sb": 600,
	"8ss": 804,
	"8sw": 7800,
	"8ws": 6255,
	"92a": 2919,
	"92h": 2232,
	"9ar": 2334,
	"9ax": 1509,
	"9b7": 1206,
	"9b8": 110,
	"9b9": 6213,
	"9ba": 2718,
	"9bk": 100,
	"9bl": 5592,
	"9br": 3786,
	"9bs": 3387,
	"9bt": 4086,
	"9bw": 4188,
	"9cl": 396,
	"9cm": 4086,
	"9cr": 6681,
	"9cs": 2964,
	"9dg": 480,
	"9di": 1896,
	"9fb": 7500,
	"9fc": 2349,
	"9fl": 4536,
	"9ga": 6048,
	"9gd": 10653,
	"9gi": 7530,
	"9gl": 145,
	"9gm": 9945,
	"9gs": 4677,
	"9gw": 6594,
	"9h9": 8418,
	"9ha": 810,
	"9ja": 120,
	"9kr": 3981,
	"9la": 1362,
	"9ls": 4860,
	"9lw": 5061,
	"9m9": 5310,
	"9ma": 1689,
	"9mp": 4563,
	"9mt": 2832,
	"9p9": 6369,
	"9pa": 5403,
	"9pi": 125,
	"9qr": 14223,
	"9qs": 3627,
	"9s8": 2844,
	"9s9": 135,
	"9sb": 1698,
	"9sc": 1350,
	"9sm": 1122,
	"9sp": 975,
	"9sr": 1200,
	"9ss": 516,
	"9st": 5316,
	"9ta": 90,
	"9tk": 80,
	"9tr": 2349,
	"9ts": 150,
	"9tw": 9291,
	"9vo": 2133,
	"9wa": 6669,
	"9wb": 4203,
	"9wc": 10464,
	"9wd": 6291,
	"9wh": 6543,
	"9wn": 915,
	"9ws": 7269,
	"9xf": 4203,
	"9yw": 2535,
	"aar": 73864,
	"am1": 2500,
	"am2": 3575,
	"am3": 1672,
	"am4": 2023,
	"am5": 48,
	"am6": 7800,
	"am7": 11025,
	"am8": 5316,
	"am9": 6369,
	"ama": 444,
	"amb": 23700,
	"amc": 33375,
	"amd": 16248,
	"ame": 19407,
	"amf": 302,
	"amu": 2400,
	"aqv": 256,
	"axe": 403,
	"axf": 466,
	"ba1": 320,
	"ba2": 750,
	"ba3": 2750,
	"ba4": 3420,
	"ba5": 5785,
	"ba6": 16603,
	"ba7": 21378,
	"ba8": 30063,
	"ba9": 36398,
	"baa": 46633,
	"bab": 60650,
	"bac": 68211,
	"bad": 78881,
	"bae": 88278,
	"baf": 97844,
	"bal": 35,
	"bar": 302,
	"bax": 806,
	"bbb": 100,
	"bet": 10000,
	"bey": 80,
	"bhm": 6323,
	"bkd": 12000,
	"bkf": 15,
	"bks": 100,
	"bld": 1764,
	"bpl": 100,
	"bps": 100,
	"brn": 1162,
	"brs": 10078,
	"brz": 60,
	"bsd": 1029,
	"bsh": 3175,
	"bst": 1223,
	"bsw": 1971,
	"btl": 847,
	"btx": 1262,
	"buc": 68,
	"bwn": 1296,
	"cap": 64,
	"cbw": 727,
	"ceh": 10000,
	"ces": 514,
	"chn": 9360,
	"ci0": 12000,
	"ci1": 23000,
	"ci2": 35000,
	"ci3": 58000,
	"clb": 32,
	"clm": 1262,
	"clw": 683,
	"cm1": 2000,
	"cm2": 1000,
	"cm3": 600,
	"cqv": 256,
	"crn": 8345,
	"crs": 2127,
	"cst": 766,
	"d33": 666,
	"dgr": 60,
	"dhn": 80,
	"dir": 532,
	"dr1": 364,
	"dr2": 664,
	"dr3": 2832,
	"dr4": 3332,
	"dr5": 5670,
	"dr6": 16300,
	"dr7": 18564,
	"dr8": 27768,
	"dr9": 29518,
	"dra": 38127,
	"drb": 61755,
	"drc": 68617,
	"drd": 79730,
	"dre": 86575,
	"drf": 92143,
	"elx": 20,
	"eyz": 45,
	"fed": 10000,
	"fhl": 3095,
	"fla": 1412,
	"flb": 2400,
	"flc": 683,
	"fld": 23841,
	"flg": 98,
	"fng": 80,
	"ful": 47192,
	"g33": 666,
	"g34": 100,
	"gax": 1916,
	"gcb": 500,
	"gcg": 500,
	"gcr": 500,
	"gcv": 500,
	"gcw": 500,
	"gcy": 500,
	"gfb": 1500,
	"gfg": 1500,
	"gfr": 1500,
	"gfv": 1500,
	"gfw": 1500,
	"gfy": 1500,
	"ghm": 6177,
	"gis": 1459,
	"gix": 2410,
	"glb": 15000,
	"glg": 15000,
	"glr": 15000,
	"glv": 36,
	"glw": 15000,
	"gly": 15000,
	"gma": 3215,
	"gpb": 30000,
	"gpg": 30000,
	"gpl": 40,
	"gpm": 120,
	"gpr": 30000,
	"gps": 200,
	"gpv": 30000,
	"gpw": 30000,
	"gpy": 30000,
	"gsb": 5000,
	"gsc": 1109,
	"gsd": 3451,
	"gsg": 5000,
	"gsr": 5000,
	"gsv": 5000,
	"gsw": 5000,
	"gsy": 5000,
	"gth": 34646,
	"gts": 8000,
	"gwn": 2098,
	"gzv": 15000,
	"hal": 2706,
	"hax": 170,
	"hbl": 2068,
	"hbt": 2954,
	"hbw": 350,
	"hdm": 600,
	"hfh": 600,
	"hgl": 2964,
	"hla": 1060,
	"hlm": 1558,
	"hp1": 30,
	"hp2": 75,
	"hp3": 125,
	"hp4": 250,
	"hp5": 500,
	"hpf": 150,
	"hpo": 30,
	"hrb": 75,
	"hrn": 48,
	"hrt": 60,
	"hst": 1223,
	"hxb": 4096,
	"ibk": 200,
	"ice": 10000,
	"isc": 80,
	"j34": 100,
	"jav": 5,
	"jaw": 75,
	"jew": 1000,
	"key": 45,
	"kit": 2129,
	"kri": 1227,
	"ktr": 72,
	"lax": 354,
	"lbb": 1545,
	"lbl": 64,
	"lbt": 80,
	"lbw": 490,
	"lea": 481,
	"leg": 20,
	"lgl": 80,
	"lrg": 1214,
	"lsd": 1520,
	"lst": 463,
	"ltp": 28327,
	"luv": 99999,
	"lwb": 3575,
	"lxb": 803,
	"mac": 463,
	"mau": 1670,
	"mbl": 495,
	"mbr": 80,
	"mbt": 854,
	"mgl": 859,
	"mp1": 60,
	"mp2": 150,
	"mp3": 300,
	"mp4": 500,
	"mp5": 1000,
	"mpf": 150,
	"mpi": 1421,
	"mpo": 30,
	"msf": 1223,
	"msk": 2857,
	"mst": 844,
	"mxb": 2345,
	"ne1": 128,
	"ne2": 418,
	"ne3": 1070,
	"ne4": 2164,
	"ne5": 3475,
	"ne6": 11590,
	"ne7": 14839,
	"ne8": 18169,
	"ne9": 22750,
	"nea": 27993,
	"neb": 56131,
	"ned": 64122,
	"nee": 80462,
	"nef": 86238,
	"neg": 64437,
	"ob1": 168,
	"ob2": 463,
	"ob3": 766,
	"ob4": 1223,
	"ob5": 1985,
	"ob6": 804,
	"ob7": 1689,
	"ob8": 2598,
	"ob9": 3969,
	"oba": 6255,
	"obb": 21051,
	"obc": 19367,
	"obd": 22094,
	"obe": 25207,
	"obf": 20065,
	"opl": 24,
	"opm": 80,
	"ops": 150,
	"pa1": 384,
	"pa2": 982,
	"pa3": 2816,
	"pa4": 5158,
	"pa5": 6935,
	"pa6": 32500,
	"pa7": 40941,
	"pa8": 52947,
	"pa9": 64807,
	"paa": 75374,
	"pab": 72618,
	"pac": 85659,
	"pad": 97676,
	"pae": 120042,
	"paf": 139860,
	"pax": 1701,
	"pik": 2023,
	"pil": 18,
	"pk1": 99999,
	"pk2": 99999,
	"pk3": 99999,
	"plt": 22335,
	"qbr": 60,
	"qey": 45,
	"qf1": 1412,
	"qf2": 1412,
	"qhr": 60,
	"qll": 32,
	"qui": 140,
	"r01": 560,
	"r02": 560,
	"r03": 1260,
	"r04": 1260,
	"r05": 2240,
	"r06": 2240,
	"r07": 3500,
	"r08": 5040,
	"r09": 6860,
	"r10": 8960,
	"r11": 11340,
	"r12": 14000,
	"r13": 16940,
	"r14": 20160,
	"r15": 1715,
	"r16": 27440,
	"r17": 31500,
	"r18": 35840,
	"r19": 40460,
	"r20": 45360,
	"r21": 50540,
	"r22": 56000,
	"r23": 61740,
	"r24": 67760,
	"r25": 74060,
	"r26": 80640,
	"r27": 87500,
	"r28": 94640,
	"r29": 102060,
	"r30": 109760,
	"r31": 117740,
	"r32": 126000,
	"r33": 134540,
	"rin": 1800,
	"rng": 4428,
	"rpl": 100,
	"rps": 100,
	"rvl": 1500,
	"rvs": 400,
	"rxb": 2739,
	"sbb": 1045,
	"sbr": 466,
	"sbw": 100,
	"scl": 6508,
	"scm": 274,
	"scp": 350,
	"scy": 848,
	"scz": 40,
	"skc": 1000,
	"skf": 3000,
	"skl": 30000,
	"skp": 441,
	"skr": 1029,
	"sku": 10000,
	"skz": 100000,
	"sml": 410,
	"sol": 100,
	"spc": 225,
	"spe": 85,
	"spk": 1890,
	"spl": 15489,
	"spr": 300,
	"spt": 1672,
	"ssd": 72,
	"ssp": 24,
	"sst": 168,
	"std": 2000,
	"stu": 2385,
	"swb": 2500,
	"tal": 63,
	"tax": 11,
	"tbk": 250,
	"tbl": 963,
	"tbt": 1630,
	"tch": 50,
	"tes": 10000,
	"tgl": 1635,
	"tkf": 6,
	"toa": 99999,
	"tow": 4249,
	"tri": 683,
	"tsc": 100,
	"tsp": 48,
	"uap": 56307,
	"uar": 373558,
	"ucl": 262166,
	"uea": 226927,
	"uh9": 87274,
	"uhb": 45459,
	"uhc": 45051,
	"uhg": 45481,
	"uhl": 57806,
	"uhm": 87168,
	"uhn": 227700,
	"uit": 81896,
	"ukp": 62739,
	"ula": 232573,
	"ulb": 28315,
	"ulc": 28842,
	"uld": 308015,
	"ulg": 26920,
	"ulm": 69940,
	"ult": 300130,
	"umb": 36456,
	"umc": 37134,
	"umg": 34964,
	"uml": 57189,
	"ung": 254435,
	"uow": 97701,
	"upk": 85443,
	"upl": 285351,
	"urg": 65914,
	"urn": 94770,
	"urs": 272206,
	"ush": 101842,
	"usk": 76578,
	"utb": 41658,
	"utc": 41183,
	"utg": 39119,
	"uth": 323094,
	"utp": 317526,
	"uts": 109635,
	"utu": 243050,
	"uuc": 48303,
	"uui": 218357,
	"uul": 336644,
	"uvb": 32271,
	"uvc": 32656,
	"uvg": 30862,
	"vbl": 192,
	"vbt": 334,
	"vgl": 352,
	"vip": 400,
	"vou": 611,
	"vps": 25,
	"wax": 2123,
	"whm": 2081,
	"wms": 25,
	"wnd": 205,
	"wrb": 220,
	"wsc": 3388,
	"wsd": 1997,
	"wsp": 2323,
	"wst": 1985,
	"xap": 13560,
	"xar": 225120,
	"xcl": 64242,
	"xea": 34960,
	"xh9": 37683,
	"xhb": 20885,
	"xhg": 20900,
	"xhl": 29083,
	"xhm": 37846,
	"xhn": 75440,
	"xit": 23094,
	"xkp": 17210,
	"xla": 39090,
	"xlb": 9230,
	"xld": 111674,
	"xlg": 8480,
	"xlm": 23075,
	"xlt": 111395,
	"xmb": 14103,
	"xmg": 14111,
	"xml": 15451,
	"xng": 56600,
	"xow": 29550,
	"xpk": 23155,
	"xpl": 93404,
	"xrg": 19537,
	"xrn": 42458,
	"xrs": 74719,
	"xsh": 39143,
	"xsk": 27190,
	"xtb": 16905,
	"xtg": 16913,
	"xth": 137817,
	"xtp": 118407,
	"xts": 39220,
	"xtu": 47582,
	"xuc": 12562,
	"xui": 30552,
	"xul": 162672,
	"xvb": 11393,
	"xvg": 11420,
	"xyz": 10000,
	"yps": 40,
	"ywn": 745,
	"zhb": 20350,
	"zlb": 9304,
	"zmb": 13143,
	"ztb": 15713,
	"zvb": 10700,
}

var vendorItemMaxStackByCode = map[string]int{
	"7b8": 270,
	"7bk": 300,
	"7gl": 120,
	"7ja": 150,
	"7pi": 140,
	"7s7": 120,
	"7ta": 270,
	"7tk": 300,
	"7ts": 120,
	"9b8": 200,
	"9bk": 240,
	"9gl": 30,
	"9ja": 90,
	"9pi": 75,
	"9s9": 60,
	"9ta": 200,
	"9tk": 240,
	"9ts": 120,
	"am5": 120,
	"ama": 120,
	"amf": 120,
	"aqv": 500,
	"bal": 200,
	"bkf": 240,
	"bpl": 10,
	"bps": 10,
	"cqv": 500,
	"gld": 5000,
	"glv": 60,
	"gpl": 25,
	"gpm": 25,
	"gps": 25,
	"ibk": 20,
	"jav": 90,
	"key": 12,
	"opl": 25,
	"opm": 25,
	"ops": 25,
	"pil": 75,
	"rpl": 10,
	"rps": 10,
	"ssp": 60,
	"tax": 200,
	"tbk": 20,
	"tch": 10,
	"tkf": 240,
	"tsp": 120,
}

var vendorItemTypeCostFormula = map[string]int{
}

