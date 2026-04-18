package memory

import (
	"errors"
	"fmt"
	"log"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/area"
	"local/internal/svc/internal/gamelib/data/skill"
	"local/internal/svc/internal/gamelib/data/stat"
	"local/internal/svc/internal/presenter"
)

type GameReader struct {
	offset Offset
	*Process

	// reader is the source of truth for all D2R memory reads. Defaults to
	// the embedded *Process (RPM path). Phase A of P1-GID attaches a
	// SnapshotReader via AttachSnapshot so reads come from the rmod-populated
	// SHM snapshot (in-process, zero syscall), eliminating app.exe → D2R RPM.
	reader Reader

	monstersLastUpdate  time.Time
	inventoryLastUpdate time.Time
	objectsLastUpdate   time.Time

	cachedMonsters  data.Monsters
	cachedInventory data.Inventory
	cachedObjects   []data.Object

	// Per-tick widget-state cache. GetWidgetState walks an FNV-hash chain
	// in D2R via 11 RPM reads per call; GetActiveWeaponSlot alone fires it
	// every tick, and a few other paths (cursor item check, inventory UI
	// toggle) add more — audited at ~20-30 RPM/tick total just for widget
	// lookups. Caching per tick cuts that to 1 (first lookup) + hits.
	widgetStateCache   map[uint64]int
	widgetStateCacheAt time.Time
}

type MercOption struct {
	Index   int
	Name    string
	Skill   skill.Skill
	Level   int
	Life    int
	Defense int
	Cost    int
}

type CharacterFlags struct {
	Hardcore    bool
	HasEverDied bool
	Expansion   bool
	Ladder      bool
}

var WidgetStateFlags = map[string]uint64{
	"WeaponSwap": 0xF2D7CF8E9CC08212,
}

var firstNumberText = regexp.MustCompile(`\d[\d,]*`)

func NewGameReader(process *Process) *GameReader {
	return &GameReader{
		offset:              calculateOffsets(process),
		Process:             process,
		reader:              process, // default reader = RPM path; AttachSnapshot switches it.
		monstersLastUpdate:  time.Time{},
		inventoryLastUpdate: time.Time{},
		objectsLastUpdate:   time.Time{},
	}
}

// AttachSnapshot swaps the memory-read source from RPM (*Process) to the
// in-process SHM snapshot written by rmod. Must be called AFTER the
// SnapshotReader has observed its first tick (use WaitForFirstTick).
// Per P1_GID_PLAN.md the caller hard-fails if SR isn't ready — no silent
// fallback to RPM.
func (gd *GameReader) AttachSnapshot(sr *SnapshotReader) {
	gd.reader = sr
}

// SnapshotInitOffsets returns the three D2R virtual addresses rmod needs to
// walk the player-unit pointer chain. Helper for wiring presenter.SnapshotInit
// from bot integration code without exporting the Offset struct or module base.
func (gd *GameReader) SnapshotInitOffsets() (unitTableVA, expansionVA, waypointVA uint64) {
	base := uint64(gd.moduleBaseAddressPtr)
	return base + uint64(gd.offset.UnitTable),
		base + uint64(gd.offset.Expansion),
		base + uint64(gd.offset.WaypointTableOffset)
}

// SnapshotStaticEntries returns the fixed-address D2R regions rmod should mirror
// every tick (Phase B2). Bot calls presenter.WriteSnapshotStatics(entries) BEFORE
// presenter.SnapshotInit so rmod sees them on first dispatch. Regular entries
// cover the simple "ReadBytes/ReadUInt at fixed VA" dispatches in GetData();
// Role-tagged entries trigger chain walking inside rmod (Phase B2b) so reads
// past the first deref (Ping struct, QuestInfo flags buffer, TerrorZones
// zones array) also land in snapshot coverage.
func (gd *GameReader) SnapshotStaticEntries() []presenter.SnapshotStatic {
	base := uint64(gd.moduleBaseAddressPtr)
	return []presenter.SnapshotStatic{
		// HoveredData: 12 B at moduleBase + offset.Hover
		{VA: base + uint64(gd.offset.Hover), Len: 12},
		// OpenMenus/IsIngame/HasMerc share the UI buffer at uiBase = moduleBase + offset.UI - 0xA,
		// length 0x16D covers all accessed indices including the +0xA map-shown byte.
		{VA: base + uint64(gd.offset.UI) - 0xA, Len: 0x16D},
		// LegacyGraphics: 1 B (Uint8) — pad to 8 B for safe alignment.
		{VA: base + uint64(gd.offset.LegacyGraphics), Len: 8},
		// FPS: Uint32 read — pad to 8 B.
		{VA: base + uint64(gd.offset.FPS), Len: 8},
		// Selected character name + last game name/password — UTF-16 strings,
		// 0x80 B buffer covers typical names.
		{VA: base + uint64(gd.offset.SelectedCharName), Len: 0x80},
		{VA: base + uint64(gd.offset.LastGameName), Len: 0x80},
		{VA: base + uint64(gd.offset.LastGamePassword), Len: 0x80},
		// B2b: KeyBindings — two pure-static 0x500 blobs.
		{VA: base + uint64(gd.offset.KeyBindingsOffset), Len: 0x500},
		{VA: base + uint64(gd.offset.KeyBindingsSkillsOffset), Len: 0x500},
		// B2b: Ping — 8 B ptr slot; rmod walker derefs and mirrors 40 B at target (Go reads +0x24 as Uint32).
		{VA: base + uint64(gd.offset.Ping), Len: 8, Role: presenter.SnapshotStaticRolePingChain},
		// B2b: QuestInfo — 8 B ptr slot; rmod walker dereferences twice and mirrors 82 B at innermost.
		{VA: base + uint64(gd.offset.QuestInfo), Len: 8, Role: presenter.SnapshotStaticRoleQuestChain},
		// B2b: TerrorZones — 16 B head (ptr @0, count @8); rmod walker mirrors count*4 B zones array.
		{VA: base + uint64(gd.offset.TZ), Len: 16, Role: presenter.SnapshotStaticRoleTZChain},
		// B3: Roster — 8 B ptr slot; rmod walker mirrors party struct head + linked list via +0x148.
		{VA: base + uint64(gd.offset.RosterOffset), Len: 8, Role: presenter.SnapshotStaticRoleRosterChain},
	}
}

// ReaderSource returns a short identifier of which reader is currently
// serving memory reads ("rpm" or "snapshot"). Used for debug/log purposes.
func (gd *GameReader) ReaderSource() string {
	if _, ok := gd.reader.(*SnapshotReader); ok {
		return "snapshot"
	}
	return "rpm"
}

func (gd *GameReader) GetData() data.Data {
	if gd.offset.UnitTable == 0 {
		gd.offset = calculateOffsets(gd.Process)
	}

	// Layer 3: flush per-tick chunk cache so field reads load fresh data.
	// No-op when STEALTH_READ is off (cache never populated).
	gd.Process.FlushChunkCache()
	gd.resetWidgetStateCache()
	// Trace ring: bump per-tick group-id so /debug/read-trace shows
	// reads grouped by the GetData call that issued them.
	if gd.Process != nil && gd.Process.readTrace != nil {
		gd.Process.readTrace.BumpTick()
	}

	// Prerequisites (order-dependent — consumed by downstream dispatches).
	rawPlayerUnits := gd.GetRawPlayerUnits()
	mainPlayerUnit := rawPlayerUnits.GetMainPlayer()
	pu := gd.GetPlayerUnit(mainPlayerUnit)
	hover := gd.HoveredData()
	now := time.Now()

	// Layer 1: Independent dispatch block. Shuffled per-tick when
	// STEALTH_READ=1 so Warden-style fixed-sequence fingerprinting on our
	// RPM trace is broken. When STEALTH_READ is unset, the natural
	// [0..N-1] order is used — matches upstream koolo exactly.
	var (
		monsters                   data.Monsters
		inventory                  data.Inventory
		objects                    []data.Object
		corpses                    data.Monsters
		corpseUnit                 RawPlayerUnit
		roster                     data.Roster
		openMenus                  data.OpenMenus
		entrances                  data.Entrances
		terrorZones                []area.ID
		keyBindings                data.KeyBindings
		hasMerc                    bool
		activeSlot                 int
		legacyGfx                  bool
		isIngame                   bool
		lastGameName, lastGamePass string
		fps, ping                  int
		gameQuestsBytes            []byte
	)

	dispatches := []func(){
		func() {
			if now.Sub(gd.monstersLastUpdate) > JitterDuration(200*time.Millisecond, 0.10) {
				monsters = gd.Monsters(pu.Position, hover)
				gd.cachedMonsters = monsters
				gd.monstersLastUpdate = now
			} else {
				monsters = gd.cachedMonsters
			}
		},
		func() {
			if now.Sub(gd.inventoryLastUpdate) > JitterDuration(500*time.Millisecond, 0.10) ||
				(hover.IsHovered && hover.UnitType == 4) {
				inventory = gd.Inventory(rawPlayerUnits, hover)
				gd.cachedInventory = inventory
				gd.inventoryLastUpdate = now
			} else {
				inventory = gd.cachedInventory
			}
		},
		func() {
			if now.Sub(gd.objectsLastUpdate) > JitterDuration(200*time.Millisecond, 0.10) {
				objects = gd.Objects(pu.Position, hover)
				gd.cachedObjects = objects
				gd.objectsLastUpdate = now
			} else {
				objects = gd.cachedObjects
			}
		},
		func() { corpseUnit = rawPlayerUnits.GetCorpse() },
		func() { corpses = gd.Corpses(pu.Position, hover) },
		func() { roster = gd.getRoster(rawPlayerUnits) },
		func() { openMenus = gd.OpenMenus() },
		func() {
			questDataPtr := uintptr(gd.reader.ReadUInt(gd.moduleBaseAddressPtr+gd.offset.QuestInfo, Uint64))
			flagsBufferPtr := uintptr(gd.reader.ReadUInt(questDataPtr, Uint64))
			gameQuestsBytes = gd.reader.ReadBytesFromMemory(flagsBufferPtr, 82)
		},
		func() { entrances = gd.Entrances(pu.Position, hover) },
		func() { terrorZones = gd.TerrorZones() },
		func() { keyBindings = gd.GetKeyBindings() },
		func() { hasMerc = gd.HasMerc() },
		func() { activeSlot = gd.GetActiveWeaponSlot() },
		func() { legacyGfx = gd.LegacyGraphics() },
		func() { isIngame = gd.IsIngame() },
		func() {
			lastGameName = gd.LastGameName()
			lastGamePass = gd.LastGamePass()
			fps = gd.FPS()
			ping = gd.Ping()
		},
	}

	// Parallel dispatches were attempted but several dispatch bodies
	// share GameReader state (cached*, *LastUpdate, widgetStateCache)
	// that isn't goroutine-safe. Race corruption produced hangs and
	// stale snapshots. Serial order preserved — pump-level coalescing
	// inside *each* dispatch's own reads is where the win lives now.
	order := dispatchOrder(len(dispatches))
	for _, i := range order {
		dispatches[i]()
	}

	return data.Data{
		Corpse: data.Corpse{
			Found:     corpseUnit.Address != 0,
			IsHovered: corpseUnit.IsHovered,
			Position:  corpseUnit.Position,
			States:    corpseUnit.States,
		},
		Game: data.OnlineGame{
			LastGameName:     lastGameName,
			LastGamePassword: lastGamePass,
			FPS:              fps,
			Ping:             ping,
		},
		Monsters:         monsters,
		Corpses:          corpses,
		PlayerUnit:       pu,
		Inventory:        inventory,
		Objects:          objects,
		Entrances:        entrances,
		OpenMenus:        openMenus,
		Roster:           roster,
		HoverData:        hover,
		TerrorZones:      terrorZones,
		Quests:           gd.getQuests(gameQuestsBytes),
		KeyBindings:      keyBindings,
		LegacyGraphics:   legacyGfx,
		IsIngame:         isIngame,
		HasMerc:          hasMerc,
		ActiveWeaponSlot: activeSlot,
	}
}

// dispatchOrder returns the order in which GetData's independent subsystem
// dispatches execute. When STEALTH_READ=1 the order is a fresh per-tick
// permutation (Layer 1); otherwise natural [0..n-1] matching upstream.
func dispatchOrder(n int) []int {
	if !StealthEnabled() {
		out := make([]int, n)
		for i := range out {
			out[i] = i
		}
		return out
	}
	return ShufflePermutation(n)
}

func (gd *GameReader) GetInventory() data.Inventory {
	rawPlayerUnits := gd.GetRawPlayerUnits()
	hover := gd.HoveredData()
	return gd.Inventory(rawPlayerUnits, hover)
}

func (gd *GameReader) InGame() bool {
	player := gd.GetRawPlayerUnits().GetMainPlayer()

	return player.UnitID > 0 && player.Position.X > 0 && player.Position.Y > 0 && player.Area > 0
}

func (gd *GameReader) OpenMenus() data.OpenMenus {
	uiBase := gd.Process.moduleBaseAddressPtr + gd.offset.UI - 0xA

	buffer := gd.reader.ReadBytesFromMemory(uiBase, 0x16D)

	isMapShown := gd.reader.ReadUInt(gd.Process.moduleBaseAddressPtr+gd.offset.UI, Uint8)

	return data.OpenMenus{
		Inventory:      buffer[0x01] != 0,
		LoadingScreen:  buffer[0x168] != 0,
		NPCInteract:    buffer[0x08] != 0,
		NPCShop:        buffer[0x0B] != 0,
		Stash:          buffer[0x18] != 0,
		Waypoint:       buffer[0x13] != 0,
		MapShown:       isMapShown != 0,
		SkillTree:      buffer[0x04] != 0,
		NewSkills:      buffer[0x07] != 0,
		NewStats:       buffer[0x06] != 0,
		Character:      buffer[0x02] != 0,
		QuitMenu:       buffer[0x09] != 0,
		Cube:           buffer[0x19] != 0,
		SkillSelect:    buffer[0x03] != 0,
		Anvil:          buffer[0x0D] != 0,
		MercInventory:  buffer[0x1E] != 0,
		BeltRows:       buffer[0x1A] != 0,
		QuestLog:       buffer[0xE] != 0,
		PortraitsShown: buffer[0x1D] != 0,
		ChatOpen:       buffer[0x05] != 0,
		Cinematic:      buffer[0x11] != 0,
	}
}

func (gd *GameReader) HoveredData() data.HoverData {
	hoverAddressPtr := gd.Process.moduleBaseAddressPtr + gd.offset.Hover
	hoverBuffer := gd.reader.ReadBytesFromMemory(hoverAddressPtr, 12)
	isUnitHovered := ReadUIntFromBuffer(hoverBuffer, 0, Uint16)
	if isUnitHovered > 0 {
		hoveredType := ReadUIntFromBuffer(hoverBuffer, 0x04, Uint32)
		hoveredUnitID := ReadUIntFromBuffer(hoverBuffer, 0x08, Uint32)

		return data.HoverData{
			IsHovered: true,
			UnitID:    data.UnitID(hoveredUnitID),
			UnitType:  int(hoveredType),
		}
	}

	return data.HoverData{}
}

func (gd *GameReader) getStatsList(statListPtr uintptr) stat.Stats {
	// Kept sequential. Both reads are 1-entry batches which would still
	// pay ~10 ms Present latency each — plain RPM is microseconds. Only
	// worth batching here if we can bundle across multiple callers.
	statsListBuffer := gd.reader.ReadBytesFromMemory(statListPtr, 0x10)
	statList := ReadUIntFromBuffer(statsListBuffer, 0, Uint64)
	statCount := ReadUIntFromBuffer(statsListBuffer, 0x08, Uint64)
	if statCount == 0 {
		return []stat.Data{}
	}

	var stats = make([]stat.Data, 0)

	statBuffer := gd.reader.ReadBytesFromMemory(uintptr(statList), statCount*10)
	for i := 0; i < int(statCount); i++ {
		offset := uint(i * 8)

		statLayer := ReadUIntFromBuffer(statBuffer, offset, Uint16)
		statEnum := ReadUIntFromBuffer(statBuffer, offset+0x2, Uint16)
		statValue := ReadIntFromBuffer(statBuffer, offset+0x4, Uint32)

		value := statValue
		switch stat.ID(statEnum) {
		case stat.Life,
			stat.MaxLife,
			stat.Mana,
			stat.MaxMana,
			stat.Stamina,
			stat.MaxStamina:
			value = statValue >> 8
		case stat.ColdLength,
			stat.PoisonLength:
			value = statValue / 25
		case stat.DeadlyStrikePerLevel:
			value = int(float64(statValue) / .8)
		case stat.HitCausesMonsterToFlee:
			value = int(float64(statValue) / 1.28)
		case stat.AttackRatingUndeadPerLevel:
			value = statValue / 2
		case stat.MagicFindPerLevel,
			stat.ExtraGoldPerLevel,
			stat.DamageDemonPerLevel,
			stat.DamageUndeadPerLevel,
			stat.DefensePerLevel,
			stat.MaxDamagePerLevel,
			stat.MaxDamagePercentPerLevel,
			stat.StrengthPerLevel,
			stat.DexterityPerLevel,
			stat.VitalityPerLevel,
			stat.ThornsPerLevel:
			value = int(math.Max(float64(statValue/8), 1))
		case stat.LifePerLevel,
			stat.ManaPerLevel:
			value = int(math.Max(float64(statValue/2048), 1))
		case stat.ReplenishDurability, stat.ReplenishQuantity:
			if statValue > 0 {
				value = int(math.Max(float64(2/statValue), 1))
			}
		case stat.RegenStaminaPerLevel:
			value = int(statValue) * 10

		case stat.LevelRequirePercent:
			value = int(statValue) * -1
		case stat.AttackRatingPerLevel:
			value = int(math.Max(float64(statValue), 15))
		}

		stats = append(stats, stat.Data{
			ID:    stat.ID(statEnum),
			Value: value,
			Layer: int(statLayer),
		})
	}

	return stats
}

// GetPanel returns a Panel object from the specified path (starting from the root panel)
func (gd *GameReader) GetPanel(panelPath ...string) data.Panel {
	if len(panelPath) == 0 {
		return data.Panel{}
	}

	// Get all panels
	allPanels := gd.ReadAllPanels()

	// Start with the first panel in the path
	firstPanelName := panelPath[0]
	currentPanel, exists := allPanels[firstPanelName]
	if !exists {
		// Panel not found at top level
		return data.Panel{}
	}

	// Traverse the path from left to right
	for i := 1; i < len(panelPath); i++ {
		childName := panelPath[i]
		nextPanel, exists := currentPanel.PanelChildren[childName]
		if !exists {
			return data.Panel{}
		}
		currentPanel = nextPanel
	}

	return currentPanel
}

func (gd *GameReader) InCharacterSelectionScreen() bool {
	panel := gd.GetPanel("CharacterSelectPanel")
	return panel.PanelName != "" && panel.PanelEnabled && panel.PanelVisible
}

func (gd *GameReader) GetSelectedCharacterName() string {
	return gd.reader.ReadStringFromMemory(gd.Process.moduleBaseAddressPtr+gd.offset.SelectedCharName, 0)
}

func (gd *GameReader) GetExpChar() uint {
	expCharPtr := uintptr(gd.Process.ReadUInt(gd.moduleBaseAddressPtr+gd.offset.Expansion, Uint64))
	expChar := gd.Process.ReadUInt(expCharPtr+0x5C, Uint16)
	return expChar
}

func (gd *GameReader) LegacyGraphics() bool {
	return gd.reader.ReadUInt(gd.Process.moduleBaseAddressPtr+gd.offset.LegacyGraphics, Uint8) != 0
}

func (gd *GameReader) IsOnline() bool {
	panel := gd.GetPanel("MainMenuPanel", "SecondaryContextButton")
	return panel.PanelName != "" && panel.PanelEnabled && panel.PanelVisible
}

func (gd *GameReader) IsIngame() bool {
	return gd.reader.ReadUInt(gd.Process.moduleBaseAddressPtr+gd.offset.UI-0xA, 1) == 1
}

func (gd *GameReader) IsInLobby() bool {
	panel := gd.GetPanel("LobbyBackgroundPanel")
	return panel.PanelName != "" && panel.PanelEnabled && panel.PanelVisible
}

func (gd *GameReader) IsInCharacterSelectionScreen() bool {
	panel := gd.GetPanel("CharacterSelectPanel")
	return panel.PanelName != "" && panel.PanelEnabled && panel.PanelVisible
}

func (gd *GameReader) IsInCharacterCreationScreen() bool {
	panel := gd.GetPanel("CharacterCreatePanel")
	return panel.PanelName != "" && panel.PanelEnabled && panel.PanelVisible
}

func (gd *GameReader) GetCharacterList() []string {
	containerPanel := gd.GetPanel("CharacterSelectPanel", "Background", "CharacterList", "View", "Container")
	if containerPanel.PanelName == "" || containerPanel.NumChildren == 0 {
		return []string{}
	}

	// Get the character names that are in the container children [ListView 0,1,2 (0 indexed)] -> children -> Name -> Extra Text 3
	characterNames := make([]string, containerPanel.NumChildren)
	for i := 0; i < containerPanel.NumChildren; i++ {
		characterNames[i] = containerPanel.PanelChildren[fmt.Sprintf("ListItem%d", i)].PanelChildren["Name"].ExtraText3
	}

	return characterNames
}

// GetMercList returns the list of mercenaries available for hire in the Hire Menu.
func (gd *GameReader) GetMercList() []MercOption {
	panel := gd.GetPanel("HireMenuPanel", "ListContainer", "View", "Container")

	if panel.PanelName == "" || panel.NumChildren == 0 {
		return []MercOption{}
	}

	mercOptions := make([]MercOption, panel.NumChildren)

	for i := 0; i < panel.NumChildren; i++ {
		row := panel.PanelChildren[fmt.Sprintf("ListItem%d", i)]
		merc := row.PanelChildren["TextBox"].ExtraText3

		var name, skillName string
		var level, life, def, cost int

		n, err := fmt.Sscanf(merc, "%s - Lvl: %d  Life: %d  Def: %d  Cost: %d\n", &name, &level, &life, &def, &cost)
		if err != nil || n < 5 {
			// HD (non-legacy)
			option := MercOption{
				Index: i,
				Name:  GetText(row.PanelChildren["HireName"]),
			}
			if option.Name == "" {
				option.Name = GetText(row.PanelChildren["Name"])
			}
			option.Level, _ = strconv.Atoi(firstNumberText.FindString(GetText(row.PanelChildren["HireLevel"])))
			option.Cost, _ = strconv.Atoi(GetText(row.PanelChildren["CostContainer"].PanelChildren["HireCost"]))

			if sk, ok := gd.mercSkillFromPanel(row.PanelChildren["Skill1"]); ok {
				option.Skill = sk
			}

			mercOptions[i] = option
			continue
		}

		lines := strings.Split(merc, "\n")
		skillName = strings.TrimSpace(lines[1])
		sk := skill.Skill{}

		for _, s := range skill.Skills {
			if s.Name == skillName {
				sk = s
				break
			}
		}

		if sk.Name == "" {
			log.Printf("Unknown merc skill: %s", skillName)
			continue
		}

		mercOptions[i] = MercOption{
			Index:   i,
			Name:    name,
			Skill:   sk,
			Level:   level,
			Life:    life,
			Defense: def,
			Cost:    cost,
		}
	}

	return mercOptions
}

func (gd *GameReader) mercSkillFromPanel(p data.Panel) (skill.Skill, bool) {
	if p.PanelPtr == 0 {
		return skill.Skill{}, false
	}
	id := skill.ID(gd.ReadUInt(p.PanelPtr+0x960, Uint32))
	if id <= 0 {
		return skill.Skill{}, false
	}
	sk, ok := skill.Skills[id]
	return sk, ok
}

// IsBlocking checks if there's a blocking popup or loading screen present
func (gd *GameReader) IsBlocking() bool {
	panel := gd.GetPanel("BlockingPanel")
	panel2 := gd.GetPanel("DismissableModal")

	return (panel.PanelName != "" && panel.PanelEnabled && panel.PanelVisible) ||
		(panel2.PanelName != "" && panel2.PanelEnabled && panel2.PanelVisible)
}

// IsDismissableModalPresent checks if there's a error popup present
func (gd *GameReader) IsDismissableModalPresent() (bool, string) {
	panel := gd.GetPanel("DismissableModal")

	if panel.PanelName == "" {
		return false, ""
	}

	modalText := panel.PanelChildren["Frame"].PanelChildren["Prompt"].ExtraText3
	return (panel.PanelName != "" && panel.PanelEnabled && panel.PanelVisible), modalText
}

func (gd *GameReader) LastGameName() string {
	return gd.reader.ReadStringFromMemory(gd.moduleBaseAddressPtr+gd.offset.LastGameName, 0)
}

func (gd *GameReader) LastGamePass() string {
	return gd.reader.ReadStringFromMemory(gd.moduleBaseAddressPtr+gd.offset.LastGamePassword, 0)
}

func (gd *GameReader) FPS() int {
	return int(gd.reader.ReadUInt(gd.moduleBaseAddressPtr+gd.offset.FPS, Uint32))
}

func (gd *GameReader) Ping() int {
	ptrToStructPtr := gd.moduleBaseAddressPtr + gd.offset.Ping
	structPtrAddr := gd.reader.ReadUInt(ptrToStructPtr, Uint64)
	return int(gd.reader.ReadUInt(uintptr(structPtrAddr+36), Uint32))
}

func (gd *GameReader) HasMerc() bool {
	return gd.reader.ReadUInt(gd.Process.moduleBaseAddressPtr+gd.offset.UI+0x8, Uint8) != 0
}

// GetWidgetState reference : https://github.com/ResurrectedTrader/ResurrectedTrade/blob/f121ec02dd3fbe1c574f713e5a0c2db92ccca821/ResurrectedTrade.AgentBase/Capture.cs#L618
//
// Per-tick cache: the hash-chain walk costs ~11 RPM calls and the audit
// flagged this as the single hottest forced-RPM path (20-30/tick when
// multiple callers repeat the same lookup). `GetData` seeds the cache by
// calling `resetWidgetStateCache` at the start of each tick; subsequent
// GetWidgetState calls return from cache on flag match.
func (gd *GameReader) GetWidgetState(stateFlag uint64) (int, error) {
	if v, ok := gd.widgetStateCache[stateFlag]; ok {
		return v, nil
	}
	// Get widget states pointer
	stateFlags := uint64(gd.Process.ReadUInt(gd.moduleBaseAddressPtr+gd.offset.WidgetStatesOffset, Uint64))
	if stateFlags == 0 {
		return 0, nil
	}

	v2 := uint64(gd.Process.ReadUInt(uintptr(stateFlags)+8, Uint64))
	if v2 == 0 {
		return 0, nil
	}

	flag := stateFlag
	v4 := uint64(0xC4CEB9FE1A85EC53) * ((uint64(0xFF51AFD7ED558CCD) * (flag ^ (flag >> 33))) ^ ((uint64(0xFF51AFD7ED558CCD) * (flag ^ (flag >> 33))) >> 33))
	v5 := (uint64(gd.Process.ReadUInt(uintptr(stateFlags), Uint64)) - 1) & (v4 ^ (v4 >> 33))
	v6 := uint64(gd.Process.ReadUInt(uintptr(v2)+uintptr(8*v5), Uint64))

	i := uintptr(v2) + uintptr(8*v5)

	for ; v6 != 0; v6 = uint64(gd.Process.ReadUInt(uintptr(v6), Uint64)) {
		if flag == uint64(gd.Process.ReadUInt(uintptr(v6)+8, Uint64)) {
			break
		}
		i = uintptr(v6)
	}

	ir := uint64(gd.Process.ReadUInt(i, Uint64))
	if ir != 0 {
		ptr1 := uint64(gd.Process.ReadUInt(uintptr(ir)+16, Uint64))
		ptr2 := uint64(gd.Process.ReadUInt(uintptr(ptr1)+16, Uint64))
		result := int(gd.Process.ReadUInt(uintptr(ptr2), Uint8))
		if gd.widgetStateCache != nil {
			gd.widgetStateCache[stateFlag] = result
		}
		return result, nil
	}

	if gd.widgetStateCache != nil {
		gd.widgetStateCache[stateFlag] = 0
	}
	return 0, nil
}

// resetWidgetStateCache is called at the start of GetData each tick so
// widget lookups reflect current state while still sharing a hash walk
// within the tick. Zero cost when the map is empty (most ticks).
func (gd *GameReader) resetWidgetStateCache() {
	if gd.widgetStateCache == nil {
		gd.widgetStateCache = make(map[uint64]int, 8)
		return
	}
	for k := range gd.widgetStateCache {
		delete(gd.widgetStateCache, k)
	}
}

func (gd *GameReader) GetActiveWeaponSlot() int {
	state, err := gd.GetWidgetState(WidgetStateFlags["WeaponSwap"])
	if err != nil {
		return 0 // Default to primary weapons on error
	}
	return state
}

func (gd *GameReader) GetCharacterFlags(characterName string) (CharacterFlags, error) {
	const (
		charDataHeaderSize = 16
		charNameOffset     = 0x010
		charFlagsOffset    = 0x122
		maxCharCount       = 47

		flagHardcore  = 0x04
		flagDead      = 0x08
		flagExpansion = 0x20
		flagLadder    = 0x40
	)

	charDataPtr := gd.moduleBaseAddressPtr + gd.offset.CharData
	if charDataPtr == 0 {
		return CharacterFlags{}, errors.New("character data pointer is invalid")
	}

	headerBuffer := gd.Process.ReadBytesFromMemory(charDataPtr, charDataHeaderSize)
	if len(headerBuffer) < charDataHeaderSize {
		return CharacterFlags{}, errors.New("failed to read character data header")
	}

	charArrayPtr := uintptr(ReadUIntFromBuffer(headerBuffer, 0x00, Uint64))
	charCount := int(ReadIntFromBuffer(headerBuffer, 0x08, Uint64))

	if charArrayPtr == 0 || charCount <= 0 || charCount > maxCharCount {
		return CharacterFlags{}, fmt.Errorf("invalid character metadata: arrayPtr=%v, count=%d", charArrayPtr, charCount)
	}

	charPointerArray := gd.Process.ReadBytesFromMemory(charArrayPtr, uint(charCount*8))

	for i := 0; i < charCount; i++ {
		charStructPtr := uintptr(ReadUIntFromBuffer(charPointerArray, uint(i*8), Uint64))
		if charStructPtr == 0 {
			continue
		}

		charName := gd.Process.ReadStringFromMemory(charStructPtr+charNameOffset, 0)

		if charName == characterName {
			fieldValue := uint16(gd.Process.ReadUInt(charStructPtr+charFlagsOffset, Uint16))

			flags := CharacterFlags{
				Hardcore:    (fieldValue & flagHardcore) != 0,
				HasEverDied: (fieldValue & flagDead) != 0,
				Expansion:   (fieldValue & flagExpansion) != 0,
				Ladder:      (fieldValue & flagLadder) != 0,
			}

			return flags, nil
		}
	}

	return CharacterFlags{}, fmt.Errorf("character not found: %s", characterName)
}
