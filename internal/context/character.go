package context

import (
	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/skill"
	"local/internal/svc/internal/gamelib/data/stat"
	"local/internal/svc/internal/game"
)

type Character interface {
	CheckKeyBindings() []skill.ID
	BuffSkills() []skill.ID
	PreCTABuffSkills() []skill.ID
	KillCountess() error
	KillAndariel() error
	KillSummoner() error
	KillDuriel() error
	KillMephisto() error
	KillPindle() error
	KillNihlathak() error
	KillCouncil() error
	KillDiablo() error
	KillIzual() error
	KillBaal() error
	KillUberIzual() error
	KillUberDuriel() error
	KillLilith() error
	KillUberMephisto() error
	KillUberDiablo() error
	KillUberBaal() error
	KillMonsterSequence(
		monsterSelector func(d game.Data) (data.UnitID, bool),
		skipOnImmunities []stat.Resist,
	) error
	ShouldIgnoreMonster(m data.Monster) bool
}
type StatAllocation struct {
	Stat   stat.ID
	Points int
}

type LevelingCharacter interface {
	Character
	// StatPoints Stats will be assigned in the order they are returned by this function.
	StatPoints() []StatAllocation
	SkillPoints() []skill.ID
	SkillsToBind() (skill.ID, []skill.ID)
	ShouldResetSkills() bool
	GetAdditionalRunewords() []string
	KillAncients() error
	InitialCharacterConfigSetup()
	AdjustCharacterConfig()
}
