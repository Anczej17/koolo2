package pather

import (
	"log/slog"
	"math"
	"math/rand"
	"sort"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/d2go/pkg/data/object"
	"github.com/hectorgimenez/d2go/pkg/data/skill"
	"github.com/hectorgimenez/d2go/pkg/data/stat"
	"github.com/hectorgimenez/koolo/internal/game"
	"github.com/hectorgimenez/koolo/internal/utils"
)

func (pf *PathFinder) RandomMovement() {
	midGameX := pf.gr.GameAreaSizeX / 2
	midGameY := pf.gr.GameAreaSizeY / 2
	x := midGameX + rand.Intn(midGameX) - (midGameX / 2)
	y := midGameY + rand.Intn(midGameY) - (midGameY / 2)
	pf.hid.MovePointer(x, y)
	pf.hid.PressKeyBinding(pf.data.KeyBindings.ForceMove)
	utils.Sleep(50)
}

func (pf *PathFinder) DistanceFromMe(p data.Position) int {
	return DistanceFromPoint(pf.data.PlayerUnit.Position, p)
}

// OptimizeRoomsTraverseOrder returns all rooms in greedy nearest-neighbor order (legacy).
// Prefer using RoomTraverser for dynamic room selection with real path distances.
func (pf *PathFinder) OptimizeRoomsTraverseOrder() []data.Room {
	distanceMatrix := make(map[data.Room]map[data.Room]int)

	for _, room1 := range pf.data.Rooms {
		distanceMatrix[room1] = make(map[data.Room]int)
		for _, room2 := range pf.data.Rooms {
			if room1 != room2 {
				distance := DistanceFromPoint(room1.GetCenter(), room2.GetCenter())
				distanceMatrix[room1][room2] = distance
			} else {
				distanceMatrix[room1][room2] = 0
			}
		}
	}

	currentRoom := data.Room{}
	for _, r := range pf.data.Rooms {
		if r.IsInside(pf.data.PlayerUnit.Position) {
			currentRoom = r
		}
	}

	visited := make(map[data.Room]bool)
	order := []data.Room{currentRoom}
	visited[currentRoom] = true

	for len(order) < len(pf.data.Rooms) {
		nextRoom := data.Room{}
		minDistance := math.MaxInt

		// Find the nearest unvisited room
		for _, room := range pf.data.Rooms {
			if !visited[room] && distanceMatrix[currentRoom][room] < minDistance {
				nextRoom = room
				minDistance = distanceMatrix[currentRoom][room]
			}
		}

		// Add the next room to the order of visit
		order = append(order, nextRoom)
		visited[nextRoom] = true
		currentRoom = nextRoom
	}

	return order
}

// RoomTraverser dynamically picks the next room to visit, avoiding
// bridge ping-pong by checking real A* path distance for top candidates.
type RoomTraverser struct {
	pf      *PathFinder
	visited map[data.Room]bool
	filter  []data.MonsterFilter
}

// NewRoomTraverser creates a traverser and marks the player's current room as visited.
// Optional monster filters are applied when checking if a room has monsters.
func (pf *PathFinder) NewRoomTraverser(filters ...data.MonsterFilter) *RoomTraverser {
	rt := &RoomTraverser{
		pf:      pf,
		visited: make(map[data.Room]bool),
		filter:  filters,
	}
	// Mark current room as visited
	for _, r := range pf.data.Rooms {
		if r.IsInside(pf.data.PlayerUnit.Position) {
			rt.visited[r] = true
			break
		}
	}
	return rt
}

// roomHasMonsters checks if a room has any living enemies matching the filter.
func (rt *RoomTraverser) roomHasMonsters(room data.Room) bool {
	// Filter out nil filters to avoid panic on nil function call
	var validFilters []data.MonsterFilter
	for _, f := range rt.filter {
		if f != nil {
			validFilters = append(validFilters, f)
		}
	}
	for _, m := range rt.pf.data.Monsters.Enemies(validFilters...) {
		if m.Stats[stat.Life] > 0 && room.IsInside(m.Position) {
			return true
		}
	}
	return false
}

// roomIsActivated checks if the game has loaded ANY monsters for this room (regardless of filter).
// If no monsters are loaded, the room hasn't been explored yet and shouldn't be skipped.
func (rt *RoomTraverser) roomIsActivated(room data.Room) bool {
	for _, m := range rt.pf.data.Monsters {
		if room.IsInside(m.Position) {
			return true
		}
	}
	return false
}

// roomIsVisible checks if the room center is within visibility range (~40 tiles) of the player.
func (rt *RoomTraverser) roomIsVisible(room data.Room) bool {
	return DistanceFromPoint(rt.pf.data.PlayerUnit.Position, room.GetCenter()) < 40
}

// isAdjacent checks if two rooms share an edge or corner (touching boundaries).
func isAdjacent(a, b data.Room) bool {
	// Rooms are adjacent if their bounding boxes overlap or touch (gap <= 1 tile).
	const gap = 1
	aRight := a.Position.X + a.Width + gap
	aBottom := a.Position.Y + a.Height + gap
	bRight := b.Position.X + b.Width + gap
	bBottom := b.Position.Y + b.Height + gap

	if a.Position.X-gap > bRight || b.Position.X-gap > aRight {
		return false
	}
	if a.Position.Y-gap > bBottom || b.Position.Y-gap > aBottom {
		return false
	}
	return true
}

// currentRoomOk returns the room the player is currently standing in, and whether one was found.
func (rt *RoomTraverser) currentRoomOk() (data.Room, bool) {
	for _, r := range rt.pf.data.Rooms {
		if r.IsInside(rt.pf.data.PlayerUnit.Position) {
			return r, true
		}
	}
	return data.Room{}, false
}

type roomCandidate struct {
	room     data.Room
	euclDist int
	hasEnemy bool
	adjacent bool
}

// NextRoom picks the best unvisited room to visit next. Returns room and false when done.
// Priority: adjacent rooms with monsters > nearby rooms with monsters > adjacent empty > nearest unvisited.
// For top euclidean candidates, checks real A* path length to avoid bridge ping-pong.
func (rt *RoomTraverser) NextRoom() (data.Room, bool) {
	cur, inRoom := rt.currentRoomOk()
	playerPos := rt.pf.data.PlayerUnit.Position

	// Two-tier room clearing: inner radius (no LoS check) + outer radius (with LoS).
	// Inner = AoE spell radius, rooms this close are always cleared by Nova.
	// Outer = movement + AoE range, needs LoS to avoid skipping rooms behind walls.
	// Teleporters cover much more ground due to teleport mobility.
	innerRadius := 8  // Nova spell radius — always cleared if bot is here
	outerRadius := 15 // Movement + combat range
	if rt.pf.data.CanTeleport() {
		innerRadius = 15 // Teleporters move freely, Nova covers 15 easily
		outerRadius = 25 // Teleport + AoE total coverage
	}
	for _, r := range rt.pf.data.Rooms {
		if rt.visited[r] {
			continue
		}
		dist := DistanceFromPoint(playerPos, r.GetCenter())
		if dist <= innerRadius {
			// Within AoE radius — definitely cleared, no LoS check needed
			rt.visited[r] = true
		} else if dist <= outerRadius && rt.pf.LineOfSight(playerPos, r.GetCenter()) {
			// Within extended range but needs clear LoS
			rt.visited[r] = true
		}
	}

	// Skip activated empty rooms — room is activated (game loaded monsters for it)
	// but no monsters match our filter. Safe to skip: we know what's there.
	// For teleporters, don't require visibility — they can assess rooms from further away
	// because they traverse the map faster and rooms activate at ~40 tile range anyway.
	for _, r := range rt.pf.data.Rooms {
		if rt.visited[r] || !rt.roomIsActivated(r) || rt.roomHasMonsters(r) {
			continue
		}
		if rt.pf.data.CanTeleport() || rt.roomIsVisible(r) {
			rt.visited[r] = true
		}
	}

	// Build candidate list
	var candidates []roomCandidate
	for _, r := range rt.pf.data.Rooms {
		if rt.visited[r] {
			continue
		}
		candidates = append(candidates, roomCandidate{
			room:     r,
			euclDist: DistanceFromPoint(playerPos, r.GetCenter()),
			hasEnemy: rt.roomHasMonsters(r),
			adjacent: inRoom && isAdjacent(cur, r), // only check adjacency if player is in a room
		})
	}

	if len(candidates) == 0 {
		return data.Room{}, false
	}

	// Sort: adjacent+monsters first, then monsters, then adjacent, then distance.
	sort.SliceStable(candidates, func(i, j int) bool {
		ci, cj := candidates[i], candidates[j]
		// Priority score: adjacent+enemy=3, enemy=2, adjacent=1, other=0
		scoreI := 0
		if ci.hasEnemy {
			scoreI += 2
		}
		if ci.adjacent {
			scoreI++
		}
		scoreJ := 0
		if cj.hasEnemy {
			scoreJ += 2
		}
		if cj.adjacent {
			scoreJ++
		}
		if scoreI != scoreJ {
			return scoreI > scoreJ
		}
		return ci.euclDist < cj.euclDist
	})

	// For top candidates (by priority+euclidean), check real path distance.
	// This catches bridges: euclidean=5 but real path=50 → pick the other one.
	// Check up to 5 candidates to avoid picking unreachable rooms when top 3 are walled off.
	checkCount := 5
	if checkCount > len(candidates) {
		checkCount = len(candidates)
	}

	bestIdx := -1
	bestRealDist := math.MaxInt

	for i := 0; i < checkCount; i++ {
		c := candidates[i]
		_, realDist, found := rt.pf.GetClosestWalkablePath(c.room.GetCenter())
		if !found {
			// Can't reach this room — mark as visited so we don't try it again
			rt.visited[c.room] = true
			continue
		}
		// Weight: adjacent+monsters rooms get a distance bonus (prefer them even if slightly farther)
		weight := realDist
		if c.hasEnemy && c.adjacent {
			weight = weight * 60 / 100 // 40% bonus
		} else if c.hasEnemy {
			weight = weight * 75 / 100 // 25% bonus
		} else if c.adjacent {
			weight = weight * 85 / 100 // 15% bonus
		}

		if weight < bestRealDist {
			bestRealDist = weight
			bestIdx = i
		}
	}

	// All checked candidates were unreachable — retry with remaining rooms
	if bestIdx < 0 {
		// Recurse: visited set was updated with unreachable rooms, so next call skips them
		return rt.NextRoom()
	}

	chosen := candidates[bestIdx].room
	rt.visited[chosen] = true

	slog.Debug("NextRoom selected",
		slog.Int("euclDist", candidates[bestIdx].euclDist),
		slog.Int("realDist", bestRealDist),
		slog.Bool("hasMonsters", candidates[bestIdx].hasEnemy),
		slog.Bool("adjacent", candidates[bestIdx].adjacent),
		slog.Int("remainingRooms", len(candidates)-1))

	return chosen, true
}

// MarkVisited marks a room as visited externally.
func (rt *RoomTraverser) MarkVisited(r data.Room) {
	rt.visited[r] = true
}

// SkipNearbyRooms marks all unvisited rooms within radius of a position as visited.
// Used when the bot fails to reach a room — skip the entire cluster to avoid
// spending minutes trying adjacent rooms in the same unreachable area.
func (rt *RoomTraverser) SkipNearbyRooms(pos data.Position, radius int) int {
	skipped := 0
	for _, r := range rt.pf.data.Rooms {
		if !rt.visited[r] && DistanceFromPoint(pos, r.GetCenter()) <= radius {
			rt.visited[r] = true
			skipped++
		}
	}
	return skipped
}

func (pf *PathFinder) MoveThroughPath(p Path, walkDuration time.Duration) {
	if pf.data.CanTeleport() {
		pf.moveThroughPathTeleport(p)
	} else {
		pf.moveThroughPathWalk(p, walkDuration)
	}
}

func (pf *PathFinder) moveThroughPathWalk(p Path, walkDuration time.Duration) {
	// Calculate the max distance we can walk in the given duration
	maxDistance := int(float64(25) * walkDuration.Seconds())

	// Let's try to calculate how close to the window border we can go
	screenCords := data.Position{}
	for distance, pos := range p {
		screenX, screenY := pf.gameCoordsToScreenCords(p.From().X, p.From().Y, pos.X, pos.Y)

		// We reached max distance, let's stop (if we are not teleporting)
		if !pf.data.CanTeleport() && maxDistance > 0 && distance > maxDistance {
			break
		}

		// Prevent mouse overlap the HUD
		if screenY > int(float32(pf.gr.GameAreaSizeY)/1.19) {
			break
		}

		// We are getting out of the window, let's stop
		if screenX < 0 || screenY < 0 || screenX > pf.gr.GameAreaSizeX || screenY > pf.gr.GameAreaSizeY {
			break
		}
		screenCords = data.Position{X: screenX, Y: screenY}
	}

	pf.MoveCharacter(screenCords.X, screenCords.Y)
}

func (pf *PathFinder) moveThroughPathTeleport(p Path) {
	hudBoundary := int(float32(pf.gr.GameAreaSizeY) / 1.19)
	fromX, fromY := p.From().X, p.From().Y

	for i := len(p) - 1; i >= 0; i-- {
		pos := p[i]
		screenX, screenY := pf.gameCoordsToScreenCords(fromX, fromY, pos.X, pos.Y)

		if screenY > hudBoundary {
			continue
		}

		if screenX >= 0 && screenY >= 0 && screenX <= pf.gr.GameAreaSizeX && screenY <= pf.gr.GameAreaSizeY {
			worldPos := data.Position{
				X: pos.X + pf.data.AreaOrigin.X,
				Y: pos.Y + pf.data.AreaOrigin.Y,
			}

			usePacket := pf.cfg.PacketCasting.UseForTeleport && pf.packetSender != nil

			if usePacket {
				if pf.isMouseClickTeleportZone() {
					slog.Debug("Mouse click teleport zone detected, using mouse click instead of packet",
						slog.String("area", pf.data.PlayerUnit.Area.Area().Name),
					)
					usePacket = false
				} else {
					nearBoundary := pf.isNearAreaBoundary(worldPos, 60)
					if nearBoundary {
						slog.Debug("Near area boundary detected, using mouse click instead of packet",
							slog.Int("x", worldPos.X),
							slog.Int("y", worldPos.Y),
						)
						usePacket = false
					}
				}
			}

			if usePacket {
				pf.MoveCharacter(screenX, screenY, worldPos)
			} else {
				pf.MoveCharacter(screenX, screenY)
			}
			return
		}
	}
}

func (pf *PathFinder) GetLastPathIndexOnScreen(p Path) int {
	hudBoundary := int(float32(pf.gr.GameAreaSizeY) / 1.19)
	fromX, fromY := p.From().X, p.From().Y

	for i := len(p) - 1; i >= 0; i-- {
		pos := p[i]
		screenX, screenY := pf.gameCoordsToScreenCords(fromX, fromY, pos.X, pos.Y)

		// Prevent mouse overlap the HUD
		if screenY > hudBoundary {
			continue
		}

		// Check if coordinates are within screen bounds
		if screenX >= 0 && screenY >= 0 && screenX <= pf.gr.GameAreaSizeX && screenY <= pf.gr.GameAreaSizeY {
			return i
		}
	}

	return 0
}

func (pf *PathFinder) isNearAreaBoundary(pos data.Position, threshold int) bool {
	if pf.data.AreaData.Grid == nil {
		return false
	}

	distToLeft := pos.X - pf.data.AreaData.OffsetX
	distToRight := (pf.data.AreaData.OffsetX + pf.data.AreaData.Width) - pos.X
	distToTop := pos.Y - pf.data.AreaData.OffsetY
	distToBottom := (pf.data.AreaData.OffsetY + pf.data.AreaData.Height) - pos.Y

	minDistance := distToLeft
	if distToRight < minDistance {
		minDistance = distToRight
	}
	if distToTop < minDistance {
		minDistance = distToTop
	}
	if distToBottom < minDistance {
		minDistance = distToBottom
	}

	return minDistance <= threshold
}

func (pf *PathFinder) isMouseClickTeleportZone() bool {
	currentArea := pf.data.PlayerUnit.Area
	switch currentArea {
	case area.FlayerJungle, area.LowerKurast, area.RiverOfFlame:
		return true
	}
	return false
}

func (pf *PathFinder) MoveCharacter(x, y int, gamePos ...data.Position) {
	if pf.data.CanTeleport() {
		if pf.cfg.PacketCasting.UseForTeleport && pf.packetSender != nil && len(gamePos) > 0 {
			// Ensure Teleport skill is selected on right-click if using packet skill selection
			if pf.cfg.PacketCasting.UseForSkillSelection && pf.packetSender != nil {
				if pf.data.PlayerUnit.RightSkill != skill.Teleport {
					if err := pf.packetSender.SelectRightSkill(skill.Teleport); err == nil {
						utils.Sleep(50)
					}
				}
			}

			err := pf.packetSender.Teleport(gamePos[0])
			if err != nil {
				pf.hid.Click(game.RightButton, x, y)
			} else {
				utils.Sleep(int(pf.data.PlayerCastDuration().Milliseconds()))
			}
		} else {
			pf.hid.Click(game.RightButton, x, y)
		}
	} else {
		pf.hid.MovePointer(x, y)
		pf.hid.PressKeyBinding(pf.data.KeyBindings.ForceMove)
		utils.Sleep(50)
	}
}

func (pf *PathFinder) GameCoordsToScreenCords(destinationX, destinationY int) (int, int) {
	return pf.gameCoordsToScreenCords(pf.data.PlayerUnit.Position.X, pf.data.PlayerUnit.Position.Y, destinationX, destinationY)
}

func (pf *PathFinder) gameCoordsToScreenCords(playerX, playerY, destinationX, destinationY int) (int, int) {
	// Calculate diff between current player position and destination
	diffX := destinationX - playerX
	diffY := destinationY - playerY

	// Transform cartesian movement (World) to isometric (screen)
	// Helpful documentation: https://clintbellanger.net/articles/isometric_math/
	screenX := int((float32(diffX-diffY) * 19.8) + float32(pf.gr.GameAreaSizeX/2))
	screenY := int((float32(diffX+diffY) * 9.9) + float32(pf.gr.GameAreaSizeY/2))

	return screenX, screenY
}

func IsNarrowMap(a area.ID) bool {
	switch a {
	case area.MaggotLairLevel1, area.MaggotLairLevel2, area.MaggotLairLevel3, area.ArcaneSanctuary, area.ClawViperTempleLevel2, area.RiverOfFlame, area.ChaosSanctuary:
		return true
	}

	return false
}

func DistanceFromPoint(from data.Position, to data.Position) int {
	first := math.Pow(float64(to.X-from.X), 2)
	second := math.Pow(float64(to.Y-from.Y), 2)

	return int(math.Sqrt(first + second))
}

func (pf *PathFinder) LineOfSight(origin data.Position, destination data.Position) bool {
	dx := int(math.Abs(float64(destination.X - origin.X)))
	dy := int(math.Abs(float64(destination.Y - origin.Y)))
	sx, sy := 1, 1

	if origin.X > destination.X {
		sx = -1
	}
	if origin.Y > destination.Y {
		sy = -1
	}

	err := dx - dy

	x, y := origin.X, origin.Y

	for {
		if !pf.data.AreaData.Grid.IsWalkable(data.Position{X: x, Y: y}) {
			return false
		}
		if x == destination.X && y == destination.Y {
			break
		}
		e2 := 2 * err
		if e2 > -dy {
			err -= dy
			x += sx
		}
		if e2 < dx {
			err += dx
			y += sy
		}
	}

	return true
}

func (pf *PathFinder) HasDoorBetween(origin data.Position, destination data.Position) (bool, *data.Object) {
	path, _, pathFound := pf.GetPathFrom(origin, destination)
	if !pathFound {
		if door, found := pf.GetClosestDoor(origin); found {
			return true, door
		}
		return false, nil
	}

	for _, o := range pf.data.Objects {
		if o.IsDoor() && o.Selectable && path.Intersects(*pf.data, o.Position, 4) {
			return true, &o
		}
	}

	return false, nil
}

// BeyondPosition calculates a new position that is a specified distance beyond the target position when viewed from the start position
func (pf *PathFinder) BeyondPosition(start, target data.Position, distance int) data.Position {
	// Calculate direction vector
	dx := float64(target.X - start.X)
	dy := float64(target.Y - start.Y)

	// Normalize
	length := math.Sqrt(dx*dx + dy*dy)
	if length == 0 {
		// If positions are identical, pick arbitrary direction
		dx = 1
		dy = 0
	} else {
		dx = dx / length
		dy = dy / length
	}

	// Return position extended beyond target
	return data.Position{
		X: target.X + int(dx*float64(distance)),
		Y: target.Y + int(dy*float64(distance)),
	}
}

func (pf *PathFinder) GetClosestDestructible(position data.Position) (*data.Object, bool) {
	breakableObjects := []object.Name{
		object.Barrel, object.Urn2, object.Urn3, object.Casket,
		object.Casket5, object.Casket6, object.LargeUrn1, object.LargeUrn4,
		object.LargeUrn5, object.Crate, object.HollowLog, object.Sarcophagus,
	}

	const immediateVicinity = 2.0
	var closestObject *data.Object
	minDistance := immediateVicinity

	// check for breakable objects
	for _, o := range pf.data.Objects {
		for _, breakableName := range breakableObjects {
			if o.Name == breakableName && o.Selectable {
				distanceToObj := utils.CalculateDistance(position, o.Position)
				if distanceToObj < minDistance {
					minDistance = distanceToObj
					closestObject = &o
				}
			}
		}
	}

	if closestObject != nil {
		return closestObject, true
	}

	return nil, false
}

func (pf *PathFinder) GetClosestDoor(position data.Position) (*data.Object, bool) {
	const immediateVicinity = 5.0
	var closestObject *data.Object
	minDistance := immediateVicinity

	// Then, check for doors. If a closer door is found, prioritize it.
	for _, o := range pf.data.Objects {
		if o.IsDoor() && o.Selectable {
			distanceToDoor := utils.CalculateDistance(position, o.Position)
			if distanceToDoor < immediateVicinity && distanceToDoor < minDistance {
				minDistance = distanceToDoor
				closestObject = &o
			}
		}
	}

	if closestObject != nil {
		return closestObject, true
	}

	return nil, false
}

func (pf *PathFinder) GetClosestChest(position data.Position, losCheck bool) (*data.Object, bool) {
	var closestObject *data.Object
	minDistance := 20.0

	// check for breakable objects
	for _, o := range pf.data.Objects {
		if o.Selectable {
			if !o.IsChest() && !o.IsSuperChest() {
				continue
			}

			distanceToObj := utils.CalculateDistance(position, o.Position)
			if distanceToObj < minDistance {
				if !losCheck || pf.LineOfSight(position, o.Position) {
					minDistance = distanceToObj
					closestObject = &o
				}
			}
		}
	}

	if closestObject != nil {
		return closestObject, true
	}

	return nil, false
}

func (pf *PathFinder) GetClosestSuperChest(position data.Position, losCheck bool) (*data.Object, bool) {
	var closestObject *data.Object
	minDistance := 20.0

	for _, o := range pf.data.Objects {
		if !o.Selectable {
			continue
		}

		// Rely on d2go classification for super chests.
		// NOTE: This intentionally includes racks/stands if d2go marks them as SuperChest.
		if !o.IsSuperChest() {
			continue
		}

		distanceToObj := utils.CalculateDistance(position, o.Position)
		if distanceToObj < minDistance {
			if !losCheck || pf.LineOfSight(position, o.Position) {
				minDistance = distanceToObj
				closestObject = &o
			}
		}
	}

	if closestObject != nil {
		return closestObject, true
	}

	return nil, false
}
