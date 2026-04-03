package town

import (
	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/area"
	"local/internal/svc/internal/gamelib/data/npc"
	"local/internal/svc/internal/context"
	"local/internal/svc/internal/game"
)

type Town interface {
	RefillNPC() npc.ID
	HealNPC() npc.ID
	RepairNPC() npc.ID
	MercContractorNPC() npc.ID
	GamblingNPC() npc.ID
	IdentifyNPC() npc.ID
	TPWaitingArea(d game.Data) data.Position
	TownArea() area.ID
}

func GetTownByArea(a area.ID) Town {
	switch a.Act() {
	case 1:
		return A1{}
	case 2:
		return A2{}
	case 3:
		return A3{}
	case 4:
		return A4{}
	}

	return A5{}
}

func IsPositionInTown(position data.Position) bool {
	ctx := context.Get()
	actTown := GetTownByArea(ctx.Data.PlayerUnit.Area)
	return ctx.Data.Areas[actTown.TownArea().Area().ID].IsInside(position)
}
