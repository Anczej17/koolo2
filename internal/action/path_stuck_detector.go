package action

import (
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/koolo/internal/pather"
	"github.com/hectorgimenez/koolo/internal/utils"
)

const (
	PathStuckTimeout = 5 * time.Second
	BlacklistRadius  = 5
)

type BlacklistedPoint struct {
	Position data.Position
	Radius   int
}

type PathStuckDetector struct {
	lastPosition      data.Position
	lastArea          area.ID
	stuckSince        time.Time
	blacklistedPoints []BlacklistedPoint
	enabled           bool
}

func NewPathStuckDetector() *PathStuckDetector {
	return &PathStuckDetector{
		enabled: true,
	}
}

func (psd *PathStuckDetector) Update(currentPos data.Position, currentArea area.ID) bool {
	if !psd.enabled {
		return false
	}

	if psd.lastArea != currentArea {
		psd.Reset()
		psd.lastArea = currentArea
		psd.lastPosition = currentPos
		return false
	}

	if currentPos.X == psd.lastPosition.X && currentPos.Y == psd.lastPosition.Y {
		if psd.stuckSince.IsZero() {
			psd.stuckSince = time.Now()
		} else if time.Since(psd.stuckSince) >= PathStuckTimeout+
			(time.Duration(utils.GetCurrentPing())*time.Millisecond) {
			return true
		}
	} else {
		psd.stuckSince = time.Time{}
		psd.lastPosition = currentPos
	}

	return false
}

func (psd *PathStuckDetector) OnStuckDetected(currentPos data.Position, nextPathStep data.Position) {
	psd.blacklistedPoints = append(psd.blacklistedPoints, BlacklistedPoint{
		Position: currentPos,
		Radius:   BlacklistRadius,
	})

	if nextPathStep.X != currentPos.X || nextPathStep.Y != currentPos.Y {
		psd.blacklistedPoints = append(psd.blacklistedPoints, BlacklistedPoint{
			Position: nextPathStep,
			Radius:   BlacklistRadius,
		})
	}

	psd.stuckSince = time.Time{}
}

func (psd *PathStuckDetector) Reset() {
	psd.stuckSince = time.Time{}
	psd.blacklistedPoints = nil
	psd.lastPosition = data.Position{}
}

func (psd *PathStuckDetector) IsPointBlacklisted(pos data.Position) bool {
	for _, bp := range psd.blacklistedPoints {
		if pather.DistanceFromPoint(pos, bp.Position) <= bp.Radius {
			return true
		}
	}
	return false
}

func (psd *PathStuckDetector) GetBlacklistedPoints() []BlacklistedPoint {
	result := make([]BlacklistedPoint, len(psd.blacklistedPoints))
	copy(result, psd.blacklistedPoints)
	return result
}

func (psd *PathStuckDetector) HasBlacklistedPoints() bool {
	return len(psd.blacklistedPoints) > 0
}

func (psd *PathStuckDetector) Enable() {
	psd.enabled = true
}

func (psd *PathStuckDetector) Disable() {
	psd.enabled = false
}

func (psd *PathStuckDetector) IsEnabled() bool {
	return psd.enabled
}
