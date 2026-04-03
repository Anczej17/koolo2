package run

import (
	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/area"
	"local/internal/svc/internal/gamelib/data/object"
	"local/internal/svc/internal/action"
	"local/internal/svc/internal/action/step"
	"local/internal/svc/internal/config"
	"local/internal/svc/internal/context"
)

type Rakanishu struct {
	ctx *context.Status
}

func NewRakanishu() *Rakanishu {
	return &Rakanishu{
		ctx: context.Get(),
	}
}

func (t Rakanishu) Name() string {
	return string(config.RakanishuRun)
}

func (t Rakanishu) CheckConditions(parameters *RunParameters) SequencerResult {
	if IsQuestRun(parameters) {
		return SequencerError
	}
	return SequencerOk
}

// Mostly for early leveling
func (t Rakanishu) Run(parameters *RunParameters) error {
	err := action.WayPoint(area.StonyField)
	if err != nil {
		return err
	}

	cairnStone := data.Object{}
	for _, o := range t.ctx.Data.Objects {
		if o.Name == object.CairnStoneAlpha {
			cairnStone = o
		}
	}

	// Trying to not be too close to avoid jumpimg in a monster pack
	action.MoveToCoords(cairnStone.Position, step.WithDistanceToFinish(10))

	action.ClearAreaAroundPosition(cairnStone.Position, 15, data.MonsterAnyFilter())
	action.ItemPickup(30)

	return nil
}
