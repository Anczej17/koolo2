package character

import (
	"log/slog"
	"math"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/npc"
	"github.com/hectorgimenez/d2go/pkg/data/skill"
	"github.com/hectorgimenez/d2go/pkg/data/stat"
	"github.com/hectorgimenez/d2go/pkg/data/state"
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

	StaticMinDistance    = 13
	StaticMaxDistance    = 22
	NovaMaxAttacksLoop  = 10
	StaticFieldThreshold  = 67 // Cast Static Field if monster HP is above this percentage
	HeraldStaticThreshold = 55 // Cast Static Field on Heralds until HP is below this percentage (Static can't go below ~50% in Hell)

	// Herald-specific constants
	HeraldDangerDistance = 4 // If Herald closer than this → break burst & reposition
	HeraldSafeDistance   = 7 // Reposition target distance (7 so with variance bot lands 7-9)
	// Pack construction radius (tiles) around a seed/anchor.
	NovaPackRadius = 15

	// Aggressive positioning cooldown (was 650ms, reduced for faster pack transitions)
	aggressiveRepositionCooldown = 300 * time.Millisecond
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

// countOpenCorners checks center + 4 cardinal neighbors for walkability.
// Returns 0..5 (reduced from 3x3=9 in old countOpenTiles — 44% fewer IsWalkable calls).
func countOpenCorners(pos data.Position, isWalkable func(data.Position) bool) int {
	open := 0
	if isWalkable(pos) {
		open++
	}
	if isWalkable(data.Position{X: pos.X - 1, Y: pos.Y}) {
		open++
	}
	if isWalkable(data.Position{X: pos.X + 1, Y: pos.Y}) {
		open++
	}
	if isWalkable(data.Position{X: pos.X, Y: pos.Y - 1}) {
		open++
	}
	if isWalkable(data.Position{X: pos.X, Y: pos.Y + 1}) {
		open++
	}
	return open
}

// findSafePositionFromHerald finds a reachable position at HeraldSafeDistance (8) from Herald.
// Scans full ring of tiles at target distance, sorted by proximity to current player direction
// (so bot retreats "backwards" rather than sideways). Falls back to closer rings if needed.
func (s NovaSorceress) findSafePositionFromHerald(heraldPos data.Position, playerPos data.Position) (data.Position, bool) {
	ctx := context.Get()
	isWalkable := ctx.Data.AreaData.IsWalkable

	// Direction vector from Herald to player (retreat direction)
	retreatDX := float64(playerPos.X - heraldPos.X)
	retreatDY := float64(playerPos.Y - heraldPos.Y)
	retreatLen := math.Sqrt(retreatDX*retreatDX + retreatDY*retreatDY)
	if retreatLen > 0 {
		retreatDX /= retreatLen
		retreatDY /= retreatLen
	}

	// Search rings from safe distance down, prioritize 8 > 7 > 6 > 5
	for radius := HeraldSafeDistance; radius >= HeraldDangerDistance+1; radius-- {
		type candidate struct {
			pos   data.Position
			score float64 // higher = more aligned with retreat direction
		}
		var candidates []candidate

		// Scan full ring (all tiles at this Chebyshev distance from Herald)
		for dx := -radius; dx <= radius; dx++ {
			for dy := -radius; dy <= radius; dy++ {
				// Only tiles at exactly this Chebyshev distance (ring, not filled circle)
				adx, ady := dx, dy
				if adx < 0 {
					adx = -adx
				}
				if ady < 0 {
					ady = -ady
				}
				maxD := adx
				if ady > maxD {
					maxD = ady
				}
				if maxD != radius {
					continue
				}

				p := data.Position{X: heraldPos.X + dx, Y: heraldPos.Y + dy}
				if !isWalkable(p) {
					continue
				}

				// Must be reachable by pathfinder
				_, _, found := ctx.PathFinder.GetPath(p)
				if !found {
					continue
				}

				// Score: dot product with retreat direction (prefer "behind" player)
				ndx := float64(dx) / float64(radius)
				ndy := float64(dy) / float64(radius)
				dot := ndx*retreatDX + ndy*retreatDY

				candidates = append(candidates, candidate{pos: p, score: dot})
			}
		}

		if len(candidates) == 0 {
			continue
		}

		// Pick candidate most aligned with retreat direction
		best := candidates[0]
		for _, c := range candidates[1:] {
			if c.score > best.score {
				best = c
			}
		}
		return best.pos, true
	}

	return data.Position{}, false
}

// repositionFromHerald moves player to HeraldSafeDistance (8) from the given Herald.
// Returns true if repositioned, false if no valid position found.
func (s NovaSorceress) repositionFromHerald(herald *data.Monster) bool {
	ctx := context.Get()
	playerPos := ctx.Data.PlayerUnit.Position

	// Simple approach: teleport AWAY from Herald using BeyondPosition
	// This extends the line Herald→Player by HeraldSafeDistance tiles
	dest := ctx.PathFinder.BeyondPosition(herald.Position, playerPos, HeraldSafeDistance)

	if err := step.MoveTo(dest); err != nil {
		// Fallback: try the full ring search
		if targetPos, found := s.findSafePositionFromHerald(herald.Position, playerPos); found {
			if err := step.MoveTo(targetPos); err != nil {
				s.Logger.Warn("Herald reposition failed", slog.String("error", err.Error()))
				return false
			}
			return true
		}
		s.Logger.Warn("Herald reposition failed", slog.String("error", err.Error()))
		return false
	}
	return true
}

// findClosestHeraldFrom returns the closest living Herald and its distance, or nil.
// Accepts a pre-cached enemies slice to avoid redundant ctx.Data.Monsters.Enemies() calls.
func findClosestHeraldFrom(enemies []data.Monster, playerPos data.Position) (*data.Monster, int) {
	var closest *data.Monster
	closestDist := 999

	for _, enemy := range enemies {
		if enemy.Stats[stat.Life] <= 0 {
			continue
		}
		if !isHerald(enemy) {
			continue
		}
		dist := gridDistance(playerPos, enemy.Position)
		if dist < closestDist {
			closestDist = dist
			closest = &enemy
		}
	}
	return closest, closestDist
}

func desiredHitsForPack(packSize int) int {
	switch {
	case packSize >= 10:
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
// Uses grid bucketing for O(n) density estimation instead of O(n²) pairwise checks.
func pickDenseSeed(playerPos, targetPos data.Position, enemies []data.Monster) (seed data.Position, ok bool) {
	const focusRadius = 22
	const bucketShift = 3 // 8-tile buckets (1<<3 = 8)

	focusR2 := focusRadius * focusRadius

	type entry struct {
		pos    data.Position
		bx, by int
	}

	focused := make([]entry, 0, len(enemies))
	buckets := make(map[int64]int, 64)

	bkey := func(bx, by int) int64 { return (int64(bx) << 32) ^ int64(by) }

	for _, m := range enemies {
		if m.Stats[stat.Life] <= 0 {
			continue
		}
		if squaredDistance(m.Position, targetPos) > focusR2 {
			continue
		}
		bx := m.Position.X >> bucketShift
		by := m.Position.Y >> bucketShift
		buckets[bkey(bx, by)]++
		focused = append(focused, entry{pos: m.Position, bx: bx, by: by})
	}

	if len(focused) == 0 {
		// Fallback: closest alive to target
		bestD := 1 << 30
		bestPos := data.Position{}
		found := false
		for _, m := range enemies {
			if m.Stats[stat.Life] <= 0 {
				continue
			}
			d := gridDistance(m.Position, targetPos)
			if d < bestD {
				bestD = d
				bestPos = m.Position
				found = true
			}
		}
		return bestPos, found
	}

	// For each focused monster, sum density in 3x3 bucket neighborhood
	bestIdx := 0
	bestDensity := -1
	bestTie := 1 << 30

	for i, e := range focused {
		density := 0
		for dx := -1; dx <= 1; dx++ {
			for dy := -1; dy <= 1; dy++ {
				density += buckets[bkey(e.bx+dx, e.by+dy)]
			}
		}

		tie := gridDistance(e.pos, targetPos)*10 + gridDistance(e.pos, playerPos)
		if density > bestDensity || (density == bestDensity && tie < bestTie) {
			bestDensity = density
			bestIdx = i
			bestTie = tie
		}
	}

	return focused[bestIdx].pos, true
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
//
// Optimizations vs original:
//   - Centroid fast-path: if pack centroid already gives enough hits, skip full grid scan (~90% of cases)
//   - Coarser grid: step 2 instead of 1 cuts candidates by ~75%
//   - countOpenCorners: 5 checks instead of 9
//   - Accepts pre-cached enemies slice (no redundant Monsters.Enemies() call)
func (s NovaSorceress) evalAggressiveNovaPosition(target data.Monster, enemies []data.Monster) novaPosEval {
	ctx := context.Get()
	playerPos := ctx.Data.PlayerUnit.Position
	evalStart := time.Now()

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

	cent := centroidOf(pack)
	isWalkable := ctx.Data.AreaData.IsWalkable
	need := desiredHitsForPack(packSize)

	// ── FAST PATH: try centroid first ──
	// If centroid is walkable and gives enough hits, skip the expensive grid scan.
	// This handles ~90% of normal packs where monsters cluster around center.
	if isWalkable(cent) {
		centHits := countHitsAt(cent, pack)
		if centHits >= need {
			s.Logger.Debug("Nova eval: centroid fast path",
				slog.Int("pack", packSize),
				slog.Int("centHits", centHits),
				slog.Int("curHits", currentHits),
				slog.Duration("eval", time.Since(evalStart)))
			return novaPosEval{
				ok:          true,
				bestPos:     cent,
				bestHits:    centHits,
				currentHits: currentHits,
				packSize:    packSize,
				engKey:      key,
				anchorPos:   anchor,
			}
		}
	}

	// ── FULL SCAN (fallback for spread-out or wall-blocked packs) ──
	// Coarser grid: step 2 cuts candidates by ~75% while still finding good positions.
	anchorRadius := 7
	searchRadiusFromPlayer := 16
	if packSize >= 10 {
		anchorRadius = 9
		searchRadiusFromPlayer = 20
	} else if packSize >= 7 {
		anchorRadius = 8
		searchRadiusFromPlayer = 18
	}

	seen := make(map[int64]struct{}, 256)
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

	candidates := make([]data.Position, 0, 256)

	// 1) Ring around anchor (main) — step 2 for coarser sampling.
	for x := anchor.X - anchorRadius; x <= anchor.X+anchorRadius; x += 2 {
		for y := anchor.Y - anchorRadius; y <= anchor.Y+anchorRadius; y += 2 {
			p := data.Position{X: x, Y: y}
			if gridDistance(anchor, p) > anchorRadius {
				continue
			}
			if gridDistance(playerPos, p) > searchRadiusFromPlayer {
				continue
			}
			add(p, &candidates)
		}
	}

	// 2) Small ring around centroid — step 2.
	centRadius := 5
	if packSize >= 10 {
		centRadius = 7
	}
	for x := cent.X - centRadius; x <= cent.X+centRadius; x += 2 {
		for y := cent.Y - centRadius; y <= cent.Y+centRadius; y += 2 {
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

	// Always include exact centroid + anchor (might be on odd coordinates skipped by step 2).
	add(cent, &candidates)
	add(anchor, &candidates)

	// 3) Local around player (micro adjustment) — step 2.
	for x := playerPos.X - 3; x <= playerPos.X+3; x += 2 {
		for y := playerPos.Y - 3; y <= playerPos.Y+3; y += 2 {
			add(data.Position{X: x, Y: y}, &candidates)
		}
	}

	if len(candidates) == 0 {
		return novaPosEval{}
	}

	// Hard rule: do NOT move away from elite anchor.
	currentAnchorDist := gridDistance(playerPos, anchor)
	maxAllowedAnchorDist := currentAnchorDist + 1
	if packSize < 10 {
		maxAllowedAnchorDist = currentAnchorDist + 2
	}

	// Scoring tuned for "fast clear":
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
		open := float64(countOpenCorners(p, isWalkable))

		score := float64(hits)*hitsW -
			dAnchor*anchorW -
			dp*moveW -
			dCent*centroidW +
			open*openW

		// Slight penalty if standing on top of monsters (micro bump issues).
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
			bestPos = p
			bestHits = hits
		}
	}

	s.Logger.Debug("Nova eval: grid scan fallback",
		slog.Int("pack", packSize),
		slog.Int("candidates", len(candidates)),
		slog.Int("bestHits", bestHits),
		slog.Int("curHits", currentHits),
		slog.Duration("eval", time.Since(evalStart)))

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

	// Safety timeout: prevent infinite loop when monster is unreachable
	killSequenceStart := time.Now()
	lastDamageTime := time.Now()
	var lastMonsterHP int

	for {
		ctx.PauseIfNotPriority()

		// Safety: if no damage dealt for 15 seconds, give up on this target
		if time.Since(lastDamageTime) > 15*time.Second {
			s.Logger.Warn("KillMonsterSequence: no damage for 15s, skipping target",
				slog.Duration("totalTime", time.Since(killSequenceStart)))
			return nil
		}

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

		// Track damage progress for safety timeout
		currentHP := monster.Stats[stat.Life]
		if lastMonsterHP == 0 || currentHP < lastMonsterHP {
			lastDamageTime = time.Now()
		}
		lastMonsterHP = currentHP

		// ── OPT 1: Cache enemies once per loop iteration ──
		// All functions below reuse this slice instead of calling ctx.Data.Monsters.Enemies() again.
		enemies := ctx.Data.Monsters.Enemies()
		playerPos := ctx.Data.PlayerUnit.Position

		// Herald lookup uses cached enemies — O(n) once instead of separate O(n) scan
		cachedHerald, cachedHeraldDist := findClosestHeraldFrom(enemies, playerPos)

		// HERALD PRIORITY: If Herald is alive and within Nova range, target it instead
		// of whatever monsterSelector picked. This prevents chasing minions near Herald
		// (which causes ping-pong).
		if cachedHerald != nil && cachedHeraldDist <= NovaMaxDistance {
			monster = *cachedHerald
		}

		// HERALD SAFETY: if Herald is dangerously close (< 4 tiles), reposition to 8 immediately
		if cachedHerald != nil && cachedHeraldDist < HeraldDangerDistance {
			s.Logger.Info("Herald too close, repositioning to safe distance",
				slog.Int("currentDist", cachedHeraldDist))
			s.repositionFromHerald(cachedHerald)
			continue
		}

		// Aggressive Nova positioning:
		// One decisive reposition per pack, anchored to elite center (when available),
		// then "nova bum bum" without dancing.
		// SKIP when any Herald is nearby — prevents pulling bot towards Herald
		if cachedHerald != nil {
			s.Logger.Debug("Herald detected, skipping aggressive positioning",
				slog.Int("heraldDist", cachedHeraldDist),
				slog.Bool("isTarget", isHerald(monster)))
		}
		if ctx.CharacterCfg.Character.NovaSorceress.AggressiveNovaPositioning && !isHerald(monster) && cachedHerald == nil {
			ev := s.evalAggressiveNovaPosition(monster, enemies) // OPT: pass cached enemies

			if ev.engKey != 0 && ev.engKey != lastEngKey {
				lastEngKey = ev.engKey
				repositionCount = 0
				attackedThisEngagement = false
				lastRepositionAt = time.Time{}
			}

			// cachedHerald == nil means no Herald on screen — no need to check pack
			if ev.ok && !attackedThisEngagement {
				need := desiredHitsForPack(ev.packSize)
				maxRep := maxRepositionsForPack(ev.packSize)

				if need > 0 && repositionCount < maxRep && ev.currentHits < need {
					// OPT 3: Cooldown reduced from 650ms to 300ms for faster pack transitions.
					if lastRepositionAt.IsZero() || time.Since(lastRepositionAt) > aggressiveRepositionCooldown {
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
						dist := gridDistance(playerPos, ev.bestPos)
						if dist >= 18 && ev.bestHits < need {
							worthIt = false
						}

						// Don't bother if position is basically the same.
						if dist == 0 {
							worthIt = false
						}

						if worthIt {
							s.Logger.Info("Nova aggressive reposition",
								slog.Int("pack", ev.packSize),
								slog.Int("curHits", ev.currentHits),
								slog.Int("bestHits", ev.bestHits),
								slog.Int("tpDist", dist))
							if err := step.MoveTo(ev.bestPos); err != nil {
								s.Logger.Debug("Aggressive Nova reposition failed", slog.String("error", err.Error()))
								repositionCount++
							} else {
								lastRepositionAt = time.Now()
								repositionCount++
								// Refresh playerPos after teleport — stale value would
								// break hasNearbyEnemy check below (SkipRangeCheck decision).
								playerPos = ctx.Data.PlayerUnit.Position
							}
						}
					}
				}
			}
		}

		// Static Field first if needed.
		// For Heralds: spam Static until HP < 55% (ignore staticFieldCast flag)
		// For normal elites: cast once if HP > 67%
		isHeraldMonster := isHerald(monster)
		shouldCastStatic := s.shouldCastStaticField(monster)

		if shouldCastStatic && (isHeraldMonster || !staticFieldCast) {
			// For Heralds: minDistance=0 (cast from anywhere), keep default maxDistance.
			// Static has 40+ tile range — no need to close in.
			staticMin := StaticMinDistance
			staticMax := StaticMaxDistance
			staticCasts := 1
			if isHeraldMonster {
				staticMin = 0
				staticCasts = 4
			}
			staticOpts := []step.AttackOption{
				step.RangedDistance(staticMin, staticMax),
			}

			if err := step.SecondaryAttack(skill.StaticField, monster.UnitID, staticCasts, staticOpts...); err == nil {
				staticFieldCast = true
				attackedThisEngagement = true

				if !isHeraldMonster {
					continue
				}

				// Herald: re-read monster data and check HP after Static batch.
				if freshMonster, ok := s.Data.Monsters.FindByID(monster.UnitID); ok && s.shouldCastStaticField(freshMonster) {
					continue // HP still above 55%, need more Static
				}
				// HP ≤ 55% — Static phase done, fall through to Nova burst
				s.Logger.Info("Herald Static phase complete, switching to Nova burst")
			}
		}

		// Choose Nova distance based on config (aggressive / normal).
		novaMin := NovaMinDistance
		novaMax := NovaMaxDistance
		if ctx.CharacterCfg.Character.NovaSorceress.AggressiveNovaPositioning {
			novaMin = NovaAggroMinDistance
			novaMax = NovaAggroMaxDistance
		}

		// Herald: bypass ensureEnemyIsInRange (it has a BeyondPosition overshoot bug
		// that catapults the bot into melee range). We manage distance ourselves.
		if isHeraldMonster {
			novaMin = 0

			// If too far for Nova to hit (>8 tiles), approach Herald but stop at safe distance.
			heraldDist := gridDistance(playerPos, monster.Position)
			if heraldDist > NovaSpellRadius {
				if err := step.MoveTo(monster.Position, step.WithDistanceToFinish(HeraldSafeDistance)); err != nil {
					s.Logger.Debug("Herald approach failed", slog.String("error", err.Error()))
				}
				// Re-check distance after approach — skip Nova if still out of range
				heraldDist = gridDistance(ctx.Data.PlayerUnit.Position, monster.Position)
				if heraldDist > NovaSpellRadius {
					continue // Try again next iteration
				}
			}

			// Use actual Nova radius as max distance — never cast from beyond hit range
			novaMax = NovaSpellRadius
		}

		novaOpts := []step.AttackOption{
			step.RangedDistance(novaMin, novaMax),
		}

		// Aggressive mode: skip ensureEnemyIsInRange inside burstAttack so the bot
		// stays at the pack-center position chosen by evalAggressiveNovaPosition
		// instead of chasing individual targets to the pack edge.
		// Only skip when there ARE enemies in Nova range — otherwise let normal
		// movement kick in to reach remaining stragglers after the center is cleared.
		if ctx.CharacterCfg.Character.NovaSorceress.AggressiveNovaPositioning && !isHeraldMonster {
			hasNearbyEnemy := false
			for _, m := range enemies { // OPT: use cached enemies
				if m.Stats[stat.Life] > 0 && gridDistance(playerPos, m.Position) <= NovaSpellRadius {
					hasNearbyEnemy = true
					break
				}
			}
			if hasNearbyEnemy {
				novaOpts = append(novaOpts, step.SkipRangeCheck())
			}
		}

		// OPT 5: Herald abort — hybrid approach:
		// Fast path: O(1) FindByID for the known Herald (every burst tick).
		// Full scan: O(n) scan for ANY Herald, throttled to every 500ms
		// (catches new Heralds that enter range during burst, e.g. after teleport).
		if cachedHerald != nil {
			heraldID := cachedHerald.UnitID
			lastFullScan := time.Now()
			novaOpts = append(novaOpts, step.AbortWhen(func() bool {
				pPos := ctx.Data.PlayerUnit.Position

				// Fast path: check known Herald by ID (O(1))
				if h, ok := ctx.Data.Monsters.FindByID(heraldID); ok && h.Stats[stat.Life] > 0 {
					if gridDistance(pPos, h.Position) < HeraldDangerDistance {
						return true
					}
				}

				// Full scan: check for ANY Herald, throttled to every 500ms
				if time.Since(lastFullScan) > 500*time.Millisecond {
					lastFullScan = time.Now()
					for _, e := range ctx.Data.Monsters.Enemies() {
						if e.Stats[stat.Life] > 0 && isHerald(e) && gridDistance(pPos, e.Position) < HeraldDangerDistance {
							return true
						}
					}
				}

				return false
			}))
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

// isHerald identifies the Herald BOSS using stat.HeraldTier (stat 367).
// Both boss and minions get stat 367, but Herald boss is always Unique,
// while Herald minions are white (MonsterTypeNone).
// Fallback: state.Herald + Unique/SuperUnique for older memory readers.
func isHerald(m data.Monster) bool {
	tier := m.Stats[stat.HeraldTier]
	if tier > 0 {
		// stat 367 present — only the Unique/SuperUnique is the boss
		return m.Type == data.MonsterTypeUnique || m.Type == data.MonsterTypeSuperUnique
	}
	// Fallback: state-based detection (memory reader doesn't populate stat 367)
	if m.States.HasState(state.Herald) &&
		(m.Type == data.MonsterTypeUnique || m.Type == data.MonsterTypeSuperUnique) {
		return true
	}
	return false
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

	// If fewer than threshold normals around it, treat as leftover.
	minNormals := ctx.CharacterCfg.Character.NovaSorceress.AggressiveSkipMinNormals
	if minNormals <= 0 {
		minNormals = 3 // default
	}
	return normalCount < minNormals
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
