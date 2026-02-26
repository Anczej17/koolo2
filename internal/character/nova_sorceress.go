package character

import (
	"log/slog"
	"math"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/npc"
	"github.com/hectorgimenez/d2go/pkg/data/skill"
	"github.com/hectorgimenez/d2go/pkg/data/stat"
	"github.com/hectorgimenez/koolo/internal/action/step"
	"github.com/hectorgimenez/koolo/internal/context"
	"github.com/hectorgimenez/koolo/internal/game"
)

const (
	// Default / safe Nova spacing
	NovaMinDistance = 6
	NovaMaxDistance = 9

	// Aggressive Nova spacing:
	// We do NOT want the engine to "step away" from the elite/pack just to satisfy distance.
	// If your step.RangedDistance has issues with min=0, set it to 1.
	NovaAggroMinDistance = 0
	NovaAggroMaxDistance = 8

	// Real Nova hit radius (tiles) used for scoring and leftover ignore.
	NovaSpellRadius = 8

	StaticMinDistance       = 13
	StaticMaxDistance       = 22
	NovaMaxAttacksLoop      = 10
	StaticFieldThreshold    = 67 // Cast Static Field if monster HP is above this percentage
	HeraldStaticThreshold   = 51 // Cast Static Field on Heralds until HP is below this percentage (like hell bosses)

	// Herald-specific constants (safe distance from dangerous DLC bosses)
	HeraldMinDistance     = 5  // Minimum safe distance from Herald (within 8-tile Nova range)
	HeraldTargetDistance  = 7  // Target distance when repositioning away from Herald

	// Pack construction radius (tiles) around a seed/anchor.
	NovaPackRadius = 15
)

// -------------------------------------------------------------------------------------
// Helpers
// -------------------------------------------------------------------------------------

type NovaSorceress struct {
	BaseCharacter
}

// gridDistance returns Chebyshev distance on the tile grid (max of |dx|,|dy|).
func gridDistance(a, b data.Position) int {
	dx := a.X - b.X
	if dx < 0 {
		dx = -dx
	}
	dy := a.Y - b.Y
	if dy < 0 {
		dy = -dy
	}
	if dx > dy {
		return dx
	}
	return dy
}

// squaredDistance returns Euclidean distance squared (dx*dx + dy*dy).
func squaredDistance(a, b data.Position) int {
	dx := a.X - b.X
	dy := a.Y - b.Y
	return dx*dx + dy*dy
}

// packKey creates a stable "engagement key" based on an anchor position (quantized).
// This makes positioning stable even if target ID changes inside the same pack.
func packKey(pos data.Position) int64 {
	qx := int64(pos.X >> 3) // 8-tile buckets
	qy := int64(pos.Y >> 3)
	return (qx << 32) ^ qy
}

// countHitsAt counts how many monsters are within NovaSpellRadius from `pos`.
func countHitsAt(pos data.Position, pack []data.Monster) int {
	r2 := NovaSpellRadius * NovaSpellRadius
	hits := 0
	for _, m := range pack {
		if m.Stats[stat.Life] <= 0 {
			continue
		}
		if squaredDistance(pos, m.Position) <= r2 {
			hits++
		}
	}
	return hits
}

// countOpenTiles returns how many tiles in a (2r+1)x(2r+1) square are walkable.
// Cheap "wall/corridor penalty": low openness = likely awkward near walls.
func countOpenTiles(pos data.Position, r int, isWalkable func(data.Position) bool) int {
	open := 0
	for x := pos.X - r; x <= pos.X+r; x++ {
		for y := pos.Y - r; y <= pos.Y+r; y++ {
			if isWalkable(data.Position{X: x, Y: y}) {
				open++
			}
		}
	}
	return open
}

// hasLineOfSight checks if there's a clear line of sight between two positions
func hasLineOfSight(from, to data.Position, isWalkable func(data.Position) bool) bool {
	dx := to.X - from.X
	dy := to.Y - from.Y

	// Get the maximum of absolute dx and dy to determine number of steps
	steps := dx
	if dy > dx {
		steps = dy
	}
	if -dx > steps {
		steps = -dx
	}
	if -dy > steps {
		steps = -dy
	}

	if steps == 0 {
		return true
	}

	// Check each point along the line
	for i := 0; i <= steps; i++ {
		x := from.X + (dx * i / steps)
		y := from.Y + (dy * i / steps)
		if !isWalkable(data.Position{X: x, Y: y}) {
			return false
		}
	}

	return true
}

// findSafePositionFromHerald finds a walkable position at safe distance from Herald with LoS
func (s NovaSorceress) findSafePositionFromHerald(heraldPos data.Position, playerPos data.Position) (data.Position, bool) {
	ctx := context.Get()
	isWalkable := ctx.Data.AreaData.IsWalkable

	// Try straight-line first (existing approach)
	dist := gridDistance(playerPos, heraldPos)
	if dist > 0 {
		dx := playerPos.X - heraldPos.X
		dy := playerPos.Y - heraldPos.Y
		length := float64(dist)

		targetX := heraldPos.X + int(float64(dx)/length*float64(HeraldTargetDistance))
		targetY := heraldPos.Y + int(float64(dy)/length*float64(HeraldTargetDistance))
		targetPos := data.Position{X: targetX, Y: targetY}

		if isWalkable(targetPos) && hasLineOfSight(targetPos, heraldPos, isWalkable) {
			return targetPos, true
		}
	}

	// Fallback: search for walkable tiles in 5-7 tile range with LoS
	// Start from target distance (7), work down to minimum (5)
	for radius := HeraldTargetDistance; radius >= HeraldMinDistance; radius-- {
		// Try 16 angles around Herald (every 22.5 degrees)
		for angle := 0; angle < 16; angle++ {
			rad := float64(angle) * 0.39269908169872414 // 2*pi/16 radians
			dx := int(float64(radius) * math.Cos(rad))
			dy := int(float64(radius) * math.Sin(rad))

			candidate := data.Position{
				X: heraldPos.X + dx,
				Y: heraldPos.Y + dy,
			}

			if isWalkable(candidate) && hasLineOfSight(candidate, heraldPos, isWalkable) {
				return candidate, true
			}
		}
	}

	return data.Position{}, false
}

// ensureHeraldDistance enforces 4-7 tile distance from any nearby Herald
// Returns true if distance is OK and bot can attack
// Returns false if repositioning happened (caller should skip attack this iteration)
// ONLY applies when Heralds are present - no impact on normal combat
func (s NovaSorceress) ensureHeraldDistance() bool {
	ctx := context.Get()
	playerPos := ctx.Data.PlayerUnit.Position

	// Find closest Herald
	var closestHerald *data.Monster
	closestDist := 999

	for _, enemy := range ctx.Data.Monsters.Enemies() {
		if enemy.Stats[stat.Life] <= 0 {
			continue
		}
		if !isHerald(enemy) {
			continue
		}

		dist := gridDistance(playerPos, enemy.Position)
		if dist < closestDist {
			closestDist = dist
			closestHerald = &enemy
		}
	}

	// No Herald nearby - distance check not needed
	if closestHerald == nil {
		return true
	}

	// Enforce 4-7 tile distance for Herald safety
	// < 4: too close (danger)
	// > 7: too far (out of Nova range, need to move closer)
	const minSafeDistance = 4
	const maxSafeDistance = 7
	const targetDistance = 5 // Reposition to middle of safe range

	if closestDist >= minSafeDistance && closestDist <= maxSafeDistance {
		// Distance OK, can attack
		return true
	}

	// Distance out of safe range - must reposition
	s.Logger.Info("Herald distance out of range, repositioning before attack",
		slog.String("herald", string(closestHerald.Name)),
		slog.Int("currentDistance", closestDist),
		slog.Int("targetDistance", targetDistance))

	// Calculate position at target distance from Herald
	if targetPos, found := s.findSafePositionFromHerald(closestHerald.Position, playerPos); found {
		if err := step.MoveTo(targetPos); err != nil {
			s.Logger.Warn("Failed to reposition for Herald distance", slog.String("error", err.Error()))
		} else {
			s.Logger.Debug("Repositioned to maintain Herald safe distance")
			time.Sleep(100 * time.Millisecond) // Brief pause after reposition
		}
	} else {
		s.Logger.Warn("No safe position found for Herald distance enforcement")
	}

	// Repositioning happened - skip attack this iteration
	return false
}

func desiredHitsForPack(packSize int) int {
	// User thresholds:
	// big 10+, medium 7+, small 3+
	switch {
	case packSize >= 10:
		if packSize < 10 {
			return packSize
		}
		return 10
	case packSize >= 7:
		return 7
	case packSize >= 3:
		return 3
	default:
		return 0
	}
}

func maxRepositionsForPack(packSize int) int {
	// Fast clear: no dancing.
	// big/medium: 1 decisive reposition, small: allow 2.
	if packSize >= 7 {
		return 1
	}
	return 2
}

// -------------------------------------------------------------------------------------
// Pack selection + Elite Anchor
// -------------------------------------------------------------------------------------

// pickDenseSeed chooses a dense monster near the current target.
// We don't want the entire screen; we want the current "engagement area".
func pickDenseSeed(playerPos, targetPos data.Position, enemies []data.Monster) (seed data.Position, ok bool) {
	alive := make([]data.Monster, 0, len(enemies))
	for _, m := range enemies {
		if m.Stats[stat.Life] > 0 {
			alive = append(alive, m)
		}
	}
	if len(alive) == 0 {
		return data.Position{}, false
	}

	// Focus only on monsters not too far from target to avoid mixing two packs.
	const focusRadius = 22
	const densityRadius = 8

	focusR2 := focusRadius * focusRadius
	densityR2 := densityRadius * densityRadius

	bestIdx := -1
	bestNeighbors := -1
	bestTie := 1 << 30

	for i := range alive {
		c := alive[i]
		if squaredDistance(c.Position, targetPos) > focusR2 {
			continue
		}

		neighbors := 0
		for j := range alive {
			if i == j {
				continue
			}
			if squaredDistance(c.Position, alive[j].Position) <= densityR2 {
				neighbors++
			}
		}

		// Tie-break: closer to target, then closer to player (stable).
		tie := gridDistance(c.Position, targetPos)*10 + gridDistance(c.Position, playerPos)
		if neighbors > bestNeighbors || (neighbors == bestNeighbors && tie < bestTie) {
			bestNeighbors = neighbors
			bestIdx = i
			bestTie = tie
		}
	}

	// Fallback: if nothing was inside focus (rare), seed = closest alive to target.
	if bestIdx < 0 {
		bestIdx = 0
		bestD := 1 << 30
		for i := range alive {
			d := gridDistance(alive[i].Position, targetPos)
			if d < bestD {
				bestD = d
				bestIdx = i
			}
		}
	}

	return alive[bestIdx].Position, true
}

// buildPack builds a pack around a seed (NovaPackRadius).
func buildPack(seed data.Position, enemies []data.Monster) []data.Monster {
	pack := make([]data.Monster, 0, len(enemies))
	r2 := NovaPackRadius * NovaPackRadius
	for _, m := range enemies {
		if m.Stats[stat.Life] <= 0 {
			continue
		}
		if squaredDistance(seed, m.Position) <= r2 {
			pack = append(pack, m)
		}
	}
	return pack
}

// packHasHerald checks if any monster in the pack is a Herald
func packHasHerald(pack []data.Monster) bool {
	for _, m := range pack {
		if m.Stats[stat.Life] <= 0 {
			continue
		}
		if isHerald(m) {
			return true
		}
	}
	return false
}

func centroidOf(pack []data.Monster) data.Position {
	if len(pack) == 0 {
		return data.Position{}
	}
	sumX, sumY := 0, 0
	n := 0
	for _, m := range pack {
		if m.Stats[stat.Life] <= 0 {
			continue
		}
		sumX += m.Position.X
		sumY += m.Position.Y
		n++
	}
	if n == 0 {
		return data.Position{}
	}
	return data.Position{X: sumX / n, Y: sumY / n}
}

// chooseAnchorForPack:
// - For big packs (>=10): anchor on an elite/champion inside the pack if possible
// - Otherwise anchor on target if it's elite
// - Otherwise anchor on the densest seed
func chooseAnchorForPack(target data.Monster, pack []data.Monster, seed data.Position) data.Position {
	packSize := len(pack)
	cent := centroidOf(pack)

	// If big pack, elite anchor is king.
	if packSize >= 10 {
		var bestElite *data.Monster
		bestTie := 1 << 30

		for i := range pack {
			m := pack[i]
			if m.Stats[stat.Life] <= 0 {
				continue
			}
			if !m.IsElite() {
				continue
			}

			// Prefer elite closer to centroid (more "center of pack")
			tie := gridDistance(m.Position, cent)
			if bestElite == nil || tie < bestTie {
				bestElite = &m
				bestTie = tie
			}
		}

		if bestElite != nil {
			return bestElite.Position
		}
	}

	// If current target is elite, that's a good anchor in most real scenarios.
	if target.IsElite() {
		return target.Position
	}

	// Otherwise: if any elite exists in pack, anchor to it.
	for i := range pack {
		m := pack[i]
		if m.Stats[stat.Life] <= 0 {
			continue
		}
		if m.IsElite() {
			return m.Position
		}
	}

	// Fallback to seed.
	return seed
}

// -------------------------------------------------------------------------------------
// Positioning (Aggressive Nova)
// -------------------------------------------------------------------------------------

type novaPosEval struct {
	ok          bool
	bestPos     data.Position
	bestHits    int
	currentHits int
	packSize    int
	engKey      int64
	anchorPos   data.Position
}

// evalAggressiveNovaPosition finds one best "entry" position for the current pack.
// Core goal: maximize Nova hits WITHOUT moving away from the elite anchor.
func (s NovaSorceress) evalAggressiveNovaPosition(target data.Monster) novaPosEval {
	ctx := context.Get()
	playerPos := ctx.Data.PlayerUnit.Position
	enemies := ctx.Data.Monsters.Enemies()
	if len(enemies) == 0 {
		return novaPosEval{}
	}

	seed, ok := pickDenseSeed(playerPos, target.Position, enemies)
	if !ok {
		return novaPosEval{}
	}

	pack := buildPack(seed, enemies)
	if len(pack) == 0 {
		return novaPosEval{}
	}

	anchor := chooseAnchorForPack(target, pack, seed)
	key := packKey(anchor)

	packSize := len(pack)
	currentHits := countHitsAt(playerPos, pack)

	// If pack is tiny, no need to reposition here.
	if packSize < 3 {
		return novaPosEval{
			ok:          false,
			packSize:    packSize,
			currentHits: currentHits,
			engKey:      key,
			anchorPos:   anchor,
		}
	}

	// Candidate generation: mostly around anchor (elite center), some around centroid.
	cent := centroidOf(pack)

	// Radii based on pack size (big packs allow slightly larger search).
	anchorRadius := 7
	searchRadiusFromPlayer := 16
	if packSize >= 10 {
		anchorRadius = 9
		searchRadiusFromPlayer = 20
	} else if packSize >= 7 {
		anchorRadius = 8
		searchRadiusFromPlayer = 18
	}

	isWalkable := ctx.Data.AreaData.IsWalkable
	seen := make(map[int64]struct{}, 1024)
	add := func(p data.Position, out *[]data.Position) {
		if !isWalkable(p) {
			return
		}
		k := (int64(p.X) << 32) ^ int64(p.Y)
		if _, exists := seen[k]; exists {
			return
		}
		seen[k] = struct{}{}
		*out = append(*out, p)
	}

	candidates := make([]data.Position, 0, 1024)

	// 1) Ring around anchor (main).
	for x := anchor.X - anchorRadius; x <= anchor.X+anchorRadius; x++ {
		for y := anchor.Y - anchorRadius; y <= anchor.Y+anchorRadius; y++ {
			p := data.Position{X: x, Y: y}
			if gridDistance(anchor, p) > anchorRadius {
				continue
			}
			// Keep search local from current position (avoid long pointless teleports).
			if gridDistance(playerPos, p) > searchRadiusFromPlayer {
				continue
			}
			add(p, &candidates)
		}
	}

	// 2) Small ring around centroid (helps in weird layouts), but still near player.
	centRadius := 5
	if packSize >= 10 {
		centRadius = 7
	}
	for x := cent.X - centRadius; x <= cent.X+centRadius; x++ {
		for y := cent.Y - centRadius; y <= cent.Y+centRadius; y++ {
			p := data.Position{X: x, Y: y}
			if gridDistance(cent, p) > centRadius {
				continue
			}
			if gridDistance(playerPos, p) > searchRadiusFromPlayer {
				continue
			}
			add(p, &candidates)
		}
	}

	// 3) Local around player (micro adjustment).
	for x := playerPos.X - 3; x <= playerPos.X+3; x++ {
		for y := playerPos.Y - 3; y <= playerPos.Y+3; y++ {
			add(data.Position{X: x, Y: y}, &candidates)
		}
	}

	if len(candidates) == 0 {
		return novaPosEval{}
	}

	// Hard rule: do NOT move away from elite anchor.
	// We allow tiny sideways drift, but never "escape the pack".
	currentAnchorDist := gridDistance(playerPos, anchor)
	maxAllowedAnchorDist := currentAnchorDist + 1
	if packSize < 10 {
		// For non-big packs allow slightly more lateral movement.
		maxAllowedAnchorDist = currentAnchorDist + 2
	}

	// Scoring tuned for "fast clear":
	// - Hits dominate
	// - Strong attraction to anchor (elite center)
	// - Openness to avoid walls
	// - Light movement penalty
	hitsW := 16.0
	anchorW := 0.95
	openW := 0.55
	moveW := 0.55
	centroidW := 0.08

	if packSize >= 10 {
		hitsW = 18.0
		anchorW = 1.10
		openW = 0.70
		moveW = 0.50
		centroidW = 0.10
	} else if packSize >= 7 {
		hitsW = 17.0
		anchorW = 1.00
		openW = 0.60
		moveW = 0.52
		centroidW = 0.09
	}

	bestPos := playerPos
	bestHits := currentHits
	bestScore := -1e18

	for _, p := range candidates {
		// Hard guard: don't increase distance from anchor beyond a tiny slack.
		da := gridDistance(p, anchor)
		if da > maxAllowedAnchorDist {
			continue
		}

		hits := countHitsAt(p, pack)
		if hits == 0 {
			continue
		}

		dp := float64(gridDistance(playerPos, p))
		dAnchor := float64(da)
		dCent := float64(gridDistance(p, cent))
		open := float64(countOpenTiles(p, 1, isWalkable)) // 0..9

		// Score: maximize hits, stay near elite anchor, avoid walls, avoid long teleports.
		score := float64(hits)*hitsW -
			dAnchor*anchorW -
			dp*moveW -
			dCent*centroidW +
			open*openW

		// Slight penalty if standing on top of monsters (micro bump issues).
		// Approx: if we are within 1 tile of any monster, subtract a bit.
		for _, m := range pack {
			if m.Stats[stat.Life] <= 0 {
				continue
			}
			if gridDistance(p, m.Position) <= 1 {
				score -= 1.2
				break
			}
		}

		if score > bestScore {
			bestScore = score
			bestScore = score
			bestPos = p
			bestHits = hits
		}
	}

	// If we did not find anything better than current, we still return eval (for stats),
	// but "ok" is true so caller may decide based on hits/gain.
	return novaPosEval{
		ok:          true,
		bestPos:     bestPos,
		bestHits:    bestHits,
		currentHits: currentHits,
		packSize:    packSize,
		engKey:      key,
		anchorPos:   anchor,
	}
}

// -------------------------------------------------------------------------------------
// Character interface
// -------------------------------------------------------------------------------------

func (s NovaSorceress) CheckKeyBindings() []skill.ID {
	requiredKeybindings := []skill.ID{
		skill.Nova,
		skill.Teleport,
		skill.TomeOfTownPortal,
		skill.StaticField,
	}

	missingKeybindings := make([]skill.ID, 0)
	for _, cskill := range requiredKeybindings {
		if _, found := s.Data.KeyBindings.KeyBindingForSkill(cskill); !found {
			missingKeybindings = append(missingKeybindings, cskill)
		}
	}

	armorSkills := []skill.ID{skill.FrozenArmor, skill.ShiverArmor, skill.ChillingArmor}
	hasArmor := false
	for _, armor := range armorSkills {
		if _, found := s.Data.KeyBindings.KeyBindingForSkill(armor); found {
			hasArmor = true
			break
		}
	}
	if !hasArmor {
		missingKeybindings = append(missingKeybindings, skill.FrozenArmor)
	}

	if len(missingKeybindings) > 0 {
		s.Logger.Debug("There are missing required key bindings.", slog.Any("Bindings", missingKeybindings))
	}

	return missingKeybindings
}

func (s NovaSorceress) KillMonsterSequence(
	monsterSelector func(d game.Data) (data.UnitID, bool),
	skipOnImmunities []stat.Resist,
) error {
	ctx := context.Get()

	completedAttackLoops := 0
	staticFieldCast := false

	// Pack-based engagement state
	var lastEngKey int64 = 0
	repositionCount := 0
	attackedThisEngagement := false
	lastRepositionAt := time.Time{}

	for {
		ctx.PauseIfNotPriority()

		id, found := monsterSelector(*s.Data)
		if !found {
			return nil
		}

		if !s.preBattleChecks(id, skipOnImmunities) {
			return nil
		}

		monster, found := s.Data.Monsters.FindByID(id)
		if !found || monster.Stats[stat.Life] <= 0 {
			return nil
		}

		// HERALD HANDLING: Keep safe distance from ANY nearby Heralds
		// Check ALL enemies for Heralds, not just current target
		// This prevents deaths when targeting a normal but Herald is nearby
		var closestHerald *data.Monster
		closestHeraldDist := 999
		for _, enemy := range ctx.Data.Monsters.Enemies() {
			if enemy.Stats[stat.Life] <= 0 {
				continue
			}
			if !isHerald(enemy) {
				continue
			}
			dist := gridDistance(ctx.Data.PlayerUnit.Position, enemy.Position)
			if dist < closestHeraldDist {
				closestHeraldDist = dist
				closestHerald = &enemy
			}
		}

		// If any Herald is too close (< 5 tiles), FORCE TARGET SWITCH to Herald
		// This ensures Herald is targeted first for Static Field (single-target spell)
		// Nova is AoE so target doesn't matter, but Static MUST hit Herald
		if closestHerald != nil && closestHeraldDist < HeraldMinDistance {
			s.Logger.Info("Herald too close - breaking current kill sequence to target Herald",
				slog.String("herald", string(closestHerald.Name)),
				slog.Int("currentDistance", closestHeraldDist),
				slog.String("currentTarget", string(monster.Name)))

			// Find safe position with LoS check and fallback to walkable tile search
			if targetPos, found := s.findSafePositionFromHerald(closestHerald.Position, ctx.Data.PlayerUnit.Position); found {
				if err := step.MoveTo(targetPos); err != nil {
					s.Logger.Warn("Failed to reposition away from Herald", slog.String("error", err.Error()))
				} else {
					s.Logger.Info("Successfully repositioned away from Herald")
					time.Sleep(200 * time.Millisecond)
				}
			} else {
				s.Logger.Warn("No safe position found from Herald, maintaining current position")
			}

			// BREAK kill sequence - return to clearRoom which will re-select target
			// Herald has top priority in SortEnemiesByPriority, so it will be selected next
			return nil
		}

		// Aggressive Nova positioning:
		// One decisive reposition per pack, anchored to elite center (when available),
		// then "nova bum bum" without dancing.
		// SKIP FOR HERALDS: If target is Herald OR pack contains Herald, skip aggressive positioning
		if ctx.CharacterCfg.Character.NovaSorceress.AggressiveNovaPositioning && !isHerald(monster) {
			ev := s.evalAggressiveNovaPosition(monster)

			// CRITICAL: Check if pack contains Herald - if yes, skip aggressive positioning entirely
			// This prevents jumping into the middle of a pack that has a Herald
			hasHeraldInPack := false
			if ev.ok && ev.packSize > 0 {
				// Build the pack to check for Heralds
				seed, seedOk := pickDenseSeed(ctx.Data.PlayerUnit.Position, monster.Position, ctx.Data.Monsters.Enemies())
				if seedOk {
					pack := buildPack(seed, ctx.Data.Monsters.Enemies())
					hasHeraldInPack = packHasHerald(pack)
					if hasHeraldInPack {
						s.Logger.Info("Pack contains Herald, skipping aggressive positioning for safety")
					}
				}
			}

			if ev.engKey != 0 && ev.engKey != lastEngKey {
				lastEngKey = ev.engKey
				repositionCount = 0
				attackedThisEngagement = false
				lastRepositionAt = time.Time{}
			}

			// Only do aggressive positioning if pack does NOT contain Herald
			if ev.ok && !attackedThisEngagement && !hasHeraldInPack {
				need := desiredHitsForPack(ev.packSize)
				maxRep := maxRepositionsForPack(ev.packSize)

				if need > 0 && repositionCount < maxRep && ev.currentHits < need {
					// Cooldown prevents wall-fail spam.
					if lastRepositionAt.IsZero() || time.Since(lastRepositionAt) > 650*time.Millisecond {
						gain := ev.bestHits - ev.currentHits

						// Big packs: demand meaningful improvement.
						worthIt := false
						if ev.bestHits > ev.currentHits {
							if ev.bestHits >= need {
								worthIt = true
							} else {
								if ev.packSize >= 10 {
									worthIt = gain >= 2
								} else {
									worthIt = gain >= 1
								}
							}
						}

						// Do not waste time on long teleports unless it reaches desired hits.
						dist := gridDistance(ctx.Data.PlayerUnit.Position, ev.bestPos)
						if dist >= 18 && ev.bestHits < need {
							worthIt = false
						}

						// Don't bother if position is basically the same.
						if dist == 0 {
							worthIt = false
						}

						if worthIt {
							if err := step.MoveTo(ev.bestPos); err != nil {
								s.Logger.Debug("Aggressive Nova reposition failed", slog.String("error", err.Error()))
								repositionCount++
							} else {
								lastRepositionAt = time.Now()
								repositionCount++
							}
						}
					}
				}
			}
		}

		// HERALD DISTANCE ENFORCEMENT: Check before Static Field
		// Ensures 4-7 tile distance maintained during combat
		// Prevents teleporting too close during Static Field cast
		if !s.ensureHeraldDistance() {
			continue // Repositioned for Herald safety, skip attack this iteration
		}

		// Static Field first if needed.
		// For Heralds: spam Static until HP < 51% (ignore staticFieldCast flag)
		// For normal elites: cast once if HP > 67%
		isHeraldMonster := isHerald(monster)
		shouldCastStatic := s.shouldCastStaticField(monster)

		if shouldCastStatic && (isHeraldMonster || !staticFieldCast) {
			staticOpts := []step.AttackOption{
				step.RangedDistance(StaticMinDistance, StaticMaxDistance),
			}

			if err := step.SecondaryAttack(skill.StaticField, monster.UnitID, 1, staticOpts...); err == nil {
				staticFieldCast = true
				attackedThisEngagement = true
				continue
			}
		}

		// HERALD DISTANCE ENFORCEMENT: Check before Nova
		// Ensures 4-7 tile distance maintained during combat
		// Prevents Nova spam when too close or too far from Herald
		if !s.ensureHeraldDistance() {
			continue // Repositioned for Herald safety, skip attack this iteration
		}

		// Choose Nova distance based on config (aggressive / normal).
		novaMin := NovaMinDistance
		novaMax := NovaMaxDistance
		if ctx.CharacterCfg.Character.NovaSorceress.AggressiveNovaPositioning {
			novaMin = NovaAggroMinDistance
			novaMax = NovaAggroMaxDistance
		}

		novaOpts := []step.AttackOption{
			step.RangedDistance(novaMin, novaMax),
		}

		if err := step.SecondaryAttack(skill.Nova, monster.UnitID, 1, novaOpts...); err == nil {
			completedAttackLoops++
			attackedThisEngagement = true
		}

		if completedAttackLoops >= NovaMaxAttacksLoop {
			completedAttackLoops = 0
			staticFieldCast = false
		}
	}
}

// isHerald detects dangerous DLC bosses (Heralds) using state count heuristic
// Heralds have 5+ states, regular elites have 3-4 states
func isHerald(m data.Monster) bool {
	return len(m.States) >= 5
}

func (s NovaSorceress) shouldCastStaticField(monster data.Monster) bool {
	maxLife := float64(monster.Stats[stat.MaxLife])
	if maxLife == 0 {
		return false
	}
	hpPercentage := (float64(monster.Stats[stat.Life]) / maxLife) * 100

	// HERALDS: Spam Static Field until HP < 51% (like hell bosses)
	if isHerald(monster) {
		return hpPercentage > float64(HeraldStaticThreshold)
	}

	// NORMAL/ELITES: Cast Static Field once if HP > 67%
	return hpPercentage > StaticFieldThreshold
}

func (s NovaSorceress) killBossWithStatic(bossID npc.ID, monsterType data.MonsterType) error {
	ctx := context.Get()

	for {
		ctx.PauseIfNotPriority()

		boss, found := s.Data.Monsters.FindOne(bossID, monsterType)
		if !found || boss.Stats[stat.Life] <= 0 {
			return nil
		}

		bossHPPercent := (float64(boss.Stats[stat.Life]) / float64(boss.Stats[stat.MaxLife])) * 100
		thresholdFloat := float64(ctx.CharacterCfg.Character.NovaSorceress.BossStaticThreshold)

		// Cast Static Field until boss HP is below threshold.
		if bossHPPercent > thresholdFloat {
			staticOpts := []step.AttackOption{
				step.Distance(StaticMinDistance, StaticMaxDistance),
			}

			err := step.SecondaryAttack(skill.StaticField, boss.UnitID, 1, staticOpts...)
			if err != nil {
				s.Logger.Warn("Failed to cast Static Field", slog.String("error", err.Error()))
			}

			continue
		}

		// Switch to Nova once boss HP is low enough.
		return s.KillMonsterSequence(func(d game.Data) (data.UnitID, bool) {
			return boss.UnitID, true
		}, nil)
	}
}

func (s NovaSorceress) killMonsterByName(id npc.ID, monsterType data.MonsterType, skipOnImmunities []stat.Resist) error {
	return s.KillMonsterSequence(func(d game.Data) (data.UnitID, bool) {
		if m, found := d.Monsters.FindOne(id, monsterType); found {
			return m.UnitID, true
		}
		return 0, false
	}, skipOnImmunities)
}

func (s NovaSorceress) BuffSkills() []skill.ID {
	skillsList := make([]skill.ID, 0)

	if _, found := s.Data.KeyBindings.KeyBindingForSkill(skill.EnergyShield); found {
		skillsList = append(skillsList, skill.EnergyShield)
	}

	if _, found := s.Data.KeyBindings.KeyBindingForSkill(skill.ThunderStorm); found {
		skillsList = append(skillsList, skill.ThunderStorm)
	}

	// Add one of the armor skills.
	for _, armor := range []skill.ID{skill.ChillingArmor, skill.ShiverArmor, skill.FrozenArmor} {
		if _, found := s.Data.KeyBindings.KeyBindingForSkill(armor); found {
			skillsList = append(skillsList, armor)
			break
		}
	}

	return skillsList
}

func (s NovaSorceress) PreCTABuffSkills() []skill.ID { return []skill.ID{} }

// ShouldIgnoreMonster skips tiny leftover packs in aggressive mode (<3 normals nearby).
func (s NovaSorceress) ShouldIgnoreMonster(m data.Monster) bool {
	ctx := context.Get()

	// If aggressive Nova is not enabled, never ignore.
	if !ctx.CharacterCfg.Character.NovaSorceress.AggressiveNovaPositioning {
		return false
	}

	// Never ignore elites / bosses / important monsters.
	if m.IsElite() {
		return false
	}

	// Dead or invalid monsters do not matter here.
	if m.Stats[stat.Life] <= 0 || m.Stats[stat.MaxLife] <= 0 {
		return false
	}

	// Count how many normal (non-elite) monsters are within Nova radius around this monster.
	radius := NovaSpellRadius
	normalCount := 0

	for _, other := range ctx.Data.Monsters.Enemies() {
		if other.Stats[stat.Life] <= 0 || other.Stats[stat.MaxLife] <= 0 {
			continue
		}
		if other.IsElite() {
			continue
		}
		if gridDistance(m.Position, other.Position) <= radius {
			normalCount++
		}
	}

	// If fewer than 3 normals around it, treat as leftover.
	return normalCount < 3
}

func (s NovaSorceress) KillAndariel() error {
	return s.killBossWithStatic(npc.Andariel, data.MonsterTypeUnique)
}

func (s NovaSorceress) KillDuriel() error {
	return s.killBossWithStatic(npc.Duriel, data.MonsterTypeUnique)
}

func (s NovaSorceress) KillMephisto() error {
	return s.killBossWithStatic(npc.Mephisto, data.MonsterTypeUnique)
}

func (s NovaSorceress) KillDiablo() error {
	timeout := time.Second * 20
	startTime := time.Now()
	diabloFound := false

	for {
		if time.Since(startTime) > timeout && !diabloFound {
			s.Logger.Error("Diablo was not found, timeout reached")
			return nil
		}

		diablo, found := s.Data.Monsters.FindOne(npc.Diablo, data.MonsterTypeUnique)
		if !found || diablo.Stats[stat.Life] <= 0 {
			if diabloFound {
				return nil
			}

			time.Sleep(200 * time.Millisecond)
			continue
		}

		diabloFound = true
		s.Logger.Info("Diablo detected, attacking")
		return s.killBossWithStatic(npc.Diablo, data.MonsterTypeUnique)
	}
}

func (s NovaSorceress) KillBaal() error {
	return s.killBossWithStatic(npc.BaalCrab, data.MonsterTypeUnique)
}

func (s NovaSorceress) KillCountess() error {
	return s.killMonsterByName(npc.DarkStalker, data.MonsterTypeSuperUnique, nil)
}

func (s NovaSorceress) KillSummoner() error {
	return s.killMonsterByName(npc.Summoner, data.MonsterTypeUnique, nil)
}

func (s NovaSorceress) KillIzual() error {
	return s.killBossWithStatic(npc.Izual, data.MonsterTypeUnique)
}

func (s NovaSorceress) KillCouncil() error {
	return s.KillMonsterSequence(func(d game.Data) (data.UnitID, bool) {
		for _, m := range d.Monsters.Enemies() {
			if m.Name == npc.CouncilMember || m.Name == npc.CouncilMember2 || m.Name == npc.CouncilMember3 {
				return m.UnitID, true
			}
		}
		return 0, false
	}, nil)
}

func (s NovaSorceress) KillPindle() error {
	return s.killMonsterByName(
		npc.DefiledWarrior,
		data.MonsterTypeSuperUnique,
		s.CharacterCfg.Game.Pindleskin.SkipOnImmunities,
	)
}

func (s NovaSorceress) KillNihlathak() error {
	return s.killMonsterByName(npc.Nihlathak, data.MonsterTypeSuperUnique, nil)
}
