package game

import (
	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/npc"
)

func IsActBoss(m data.Monster) bool {
	switch m.Name {
	case npc.Andariel, npc.Duriel, npc.Mephisto, npc.Diablo, npc.BaalCrab:
		return true
	}
	return false
}

func IsMonsterSealElite(monster data.Monster) bool {
	return monster.Type == data.MonsterTypeSuperUnique && (monster.Name == npc.OblivionKnight || monster.Name == npc.VenomLord || monster.Name == npc.StormCaster)
}

func IsQuestEnemy(m data.Monster) bool {
	if IsActBoss(m) {
		return true
	}
	if IsMonsterSealElite(m) {
		return true
	}
	switch m.Name {
	case npc.Summoner, npc.CouncilMember, npc.CouncilMember2, npc.CouncilMember3:
		return true
	}
	return false
}
