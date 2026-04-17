package item

type Description struct {
	ID                int
	Name              string
	Code              string
	NormalCode        string // Normal
	UberCode          string // Exceptional
	UltraCode         string // Elite
	InventoryWidth    int
	InventoryHeight   int
	MinDefense        int
	MaxDefense        int
	MinDamage         int
	MaxDamage         int
	TwoHandMinDamage  int
	TwoHandMaxDamage  int
	MinMissileDamage  int
	MaxMissileDamage  int
	Speed             int // for weapons speed is the attack speed modifier,for armor its the movement penalty
	StrengthBonus     int
	DexterityBonus    int
	RequiredStrength  int
	RequiredDexterity int
	Durability        int
	RequiredLevel     int
	MaxSockets        int
	Type              string
	BaseCost          int // base vendor cost from txt (sell = BaseCost/4 for normal items)
}

// SellPrice returns approximate vendor sell price.
func (d Description) SellPrice(quality Quality) int {
	if d.BaseCost == 0 {
		return 1
	}
	m := 1
	switch quality {
	case QualityMagic:
		m = 2
	case QualityRare:
		m = 3
	case QualitySet:
		m = 4
	case QualityUnique:
		m = 5
	}
	p := d.BaseCost * m / 4
	if p < 1 {
		return 1
	}
	return p
}

func (d Description) Tier() Tier {
	if d.Code == d.UltraCode {
		return TierElite
	}

	if d.Code == d.UberCode {
		return TierExceptional
	}

	return TierNormal
}

func (d Description) GetType() Type {
	return ItemTypes[d.Type]
}
