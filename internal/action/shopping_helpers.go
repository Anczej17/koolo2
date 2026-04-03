package action

import (
	"local/internal/svc/internal/gamelib/data/area"
	"local/internal/svc/internal/gamelib/data/npc"
)

// Vendor → town area
var VendorLocationMap = map[npc.ID]area.ID{
	npc.Akara:   area.RogueEncampment,
	npc.Charsi:  area.RogueEncampment,
	npc.Gheed:   area.RogueEncampment,
	npc.Fara:    area.LutGholein,
	npc.Drognan: area.LutGholein,
	npc.Elzix:   area.LutGholein,
	npc.Ormus:   area.KurastDocks,
	npc.Halbu:   area.ThePandemoniumFortress,
	npc.Malah:   area.Harrogath,
	npc.Larzuk:  area.Harrogath,
	npc.Drehya:  area.Harrogath, // Anya in backend
}
