package run

import (
	"slices"
	"sort"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/d2go/pkg/data/quest"
	"github.com/hectorgimenez/koolo/internal/action"
	"github.com/hectorgimenez/koolo/internal/config"
	"github.com/hectorgimenez/koolo/internal/context"
	"github.com/hectorgimenez/koolo/internal/pather"
)

type KurastTemples struct {
	ctx *context.Status
}

type kurastTempleGroup struct {
	base    area.ID
	temples []area.ID
}

func NewKurastTemples() *KurastTemples {
	return &KurastTemples{
		ctx: context.Get(),
	}
}

func (k KurastTemples) Name() string {
	return string(config.KurastTemplesRun)
}

func (k KurastTemples) CheckConditions(parameters *RunParameters) SequencerResult {
	if IsQuestRun(parameters) {
		return SequencerError
	}
	if !k.ctx.Data.Quests[quest.Act2TheSevenTombs].Completed() {
		return SequencerSkip
	}
	return SequencerOk
}

func (k KurastTemples) Run(parameters *RunParameters) error {
	k.ctx.Logger.Info("Starting Kurast Temples run")

	if err := action.WayPoint(area.LowerKurast); err != nil {
		return err
	}

	groups := []kurastTempleGroup{
		{
			base:    area.KurastBazaar,
			temples: []area.ID{area.RuinedTemple, area.DisusedFane},
		},
		{
			base:    area.UpperKurast,
			temples: []area.ID{area.ForgottenReliquary, area.ForgottenTemple},
		},
		{
			base:    area.KurastCauseway,
			temples: []area.ID{area.RuinedFane, area.DisusedReliquary},
		},
	}

	for _, group := range groups {
		if k.ctx.Data.PlayerUnit.Area != group.base {
			if err := action.MoveToArea(group.base); err != nil {
				return err
			}
		}

		for _, temple := range k.sortedTemples(group.temples) {
			if err := action.MoveToArea(temple); err != nil {
				return err
			}

			if err := action.ClearCurrentLevel(false, data.MonsterAnyFilter()); err != nil {
				return err
			}

			if err := action.MoveToArea(group.base); err != nil {
				return err
			}
		}
	}

	return nil
}

func (k KurastTemples) sortedTemples(temples []area.ID) []area.ID {
	exitPositions := make(map[area.ID]data.Position, len(k.ctx.Data.AdjacentLevels))
	for _, level := range k.ctx.Data.AdjacentLevels {
		exitPositions[level.Area] = level.Position
	}

	type templeInfo struct {
		id       area.ID
		distance int
		hasExit  bool
	}

	maxDistance := int(^uint(0) >> 1)
	infos := make([]templeInfo, 0, len(temples))
	for _, temple := range temples {
		pos, ok := exitPositions[temple]
		distance := maxDistance
		if ok {
			distance = pather.DistanceFromPoint(k.ctx.Data.PlayerUnit.Position, pos)
		}
		infos = append(infos, templeInfo{id: temple, distance: distance, hasExit: ok})
	}

	hasAnyExit := false
	for _, info := range infos {
		if info.hasExit {
			hasAnyExit = true
			break
		}
	}
	if !hasAnyExit {
		return slices.Clone(temples)
	}

	sort.SliceStable(infos, func(i, j int) bool {
		return infos[i].distance < infos[j].distance
	})

	ordered := make([]area.ID, len(infos))
	for i, info := range infos {
		ordered[i] = info.id
	}
	return ordered
}
