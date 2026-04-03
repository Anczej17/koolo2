package memory

import (
	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/area"
	"local/internal/svc/internal/gamelib/data/mode"
	"local/internal/svc/internal/gamelib/data/stat"
	"local/internal/svc/internal/gamelib/data/state"
)

type RawPlayerUnit struct {
	data.UnitID
	Address      uintptr
	Name         string
	IsMainPlayer bool
	IsCorpse     bool
	Area         area.ID
	Position     data.Position
	IsHovered    bool
	States       state.States
	Stats        stat.Stats
	BaseStats    stat.Stats
	Mode         mode.PlayerMode
}

type RawPlayerUnits []RawPlayerUnit

func (pu RawPlayerUnits) GetMainPlayer() RawPlayerUnit {
	for _, p := range pu {
		if p.IsMainPlayer {
			return p
		}
	}
	return RawPlayerUnit{}
}

func (pu RawPlayerUnits) GetCorpse() RawPlayerUnit {
	for _, p := range pu {
		if p.IsCorpse {
			return p
		}
	}
	return RawPlayerUnit{}
}
