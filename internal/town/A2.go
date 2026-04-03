package town

import (
	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/area"
	"local/internal/svc/internal/gamelib/data/npc"
	"local/internal/svc/internal/game"
)

type A2 struct {
}

func (a A2) GamblingNPC() npc.ID {
	return npc.Elzix
}

func (a A2) HealNPC() npc.ID {
	return npc.Fara
}

func (a A2) MercContractorNPC() npc.ID {
	return npc.Greiz
}

func (a A2) RefillNPC() npc.ID {
	return npc.Drognan
}

func (a A2) RepairNPC() npc.ID {
	return npc.Fara
}

func (a A2) TPWaitingArea(_ game.Data) data.Position {
	return data.Position{
		X: 5161,
		Y: 5059,
	}
}

func (a A2) IdentifyNPC() npc.ID {
	return npc.DeckardCain2
}

func (a A2) TownArea() area.ID {
	return area.LutGholein
}
