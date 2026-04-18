package memory

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data/mode"

	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/area"
	"local/internal/svc/internal/gamelib/data/game"
	"local/internal/svc/internal/gamelib/data/skill"
	"local/internal/svc/internal/gamelib/data/state"
)

func (gd *GameReader) GetRawPlayerUnits() RawPlayerUnits {
	rawPlayerUnits := make(RawPlayerUnits, 0)
	hover := gd.HoveredData()

	// Phase 3 — when ROP_READ=batch is wired, prefetch the whole UnitTable
	// (128 × u64 = 1 KB) in one CMD_ROP_READ_BATCH entry instead of 128
	// per-slot NtReadVirtualMemory calls. Falls back transparently to the
	// per-slot RPM read when the batch path isn't available.
	var unitTableBuf []byte
	if bufs, err := gd.Process.BatchReadBytes([]BatchReadEntry{{
		Src: gd.Process.moduleBaseAddressPtr + gd.offset.UnitTable,
		Len: 128 * 8,
	}}); err == nil && len(bufs) == 1 {
		unitTableBuf = bufs[0]
	}

	// Cache expansion-char LoD flag per call — it's invariant across the
	// 128-slot sweep. Saves 2 reads per non-null player on the hot path.
	expCharPtr := uintptr(gd.reader.ReadUInt(gd.moduleBaseAddressPtr+gd.offset.Expansion, Uint64))
	expChar := gd.reader.ReadUInt(expCharPtr+0x5C, Uint16)
	isMainOffset := uintptr(0x30)
	if expChar >= uint(game.CharLoD) {
		isMainOffset = 0x70
	}

	for i := 0; i < 128; i++ {
		var playerUnit uintptr
		if unitTableBuf != nil {
			playerUnit = uintptr(binary.LittleEndian.Uint64(unitTableBuf[i*8:]))
		} else {
			unitOffset := gd.offset.UnitTable + uintptr(i*8)
			playerUnitAddr := gd.Process.moduleBaseAddressPtr + unitOffset
			playerUnit = uintptr(gd.reader.ReadUInt(playerUnitAddr, Uint64))
		}
		for playerUnit > 0 {
			// Batch the per-playerUnit fixed-offset reads. 7 fields collapsed
			// into a single CMD_ROP_READ_BATCH dispatch when ROP_READ=batch.
			// Byte field layout mirrors the list below so parsing stays in
			// lock-step with it.
			var (
				unitID         uint32
				inventoryAddr  uintptr
				pathAddress    uintptr
				playerNameAddr uintptr
				statsListExPtr uintptr
				playerMode     mode.PlayerMode
				isCorpse       uint
				nextPlayer     uintptr
			)
			batched := false
			if bufs, err := gd.Process.BatchReadBytes([]BatchReadEntry{
				{Src: playerUnit + 0x08, Len: 4},   // 0 unitID u32
				{Src: playerUnit + 0x90, Len: 8},   // 1 inventory ptr
				{Src: playerUnit + 0x38, Len: 8},   // 2 path ptr
				{Src: playerUnit + 0x10, Len: 8},   // 3 unitData ptr (name)
				{Src: playerUnit + 0x1AE, Len: 1},  // 4 isCorpse u8
				{Src: playerUnit + 0x88, Len: 8},   // 5 statsListEx ptr
				{Src: playerUnit + 0x0C, Len: 4},   // 6 playerMode u32
				{Src: playerUnit + 0x158, Len: 8},  // 7 next
			}); err == nil && len(bufs) == 8 {
				unitID = binary.LittleEndian.Uint32(bufs[0])
				inventoryAddr = uintptr(binary.LittleEndian.Uint64(bufs[1]))
				pathAddress = uintptr(binary.LittleEndian.Uint64(bufs[2]))
				playerNameAddr = uintptr(binary.LittleEndian.Uint64(bufs[3]))
				isCorpse = uint(bufs[4][0])
				statsListExPtr = uintptr(binary.LittleEndian.Uint64(bufs[5]))
				playerMode = mode.PlayerMode(binary.LittleEndian.Uint32(bufs[6]))
				nextPlayer = uintptr(binary.LittleEndian.Uint64(bufs[7]))
				batched = true
			}
			if !batched {
				unitID = uint32(gd.reader.ReadUInt(playerUnit+0x08, Uint32))
				inventoryAddr = uintptr(gd.reader.ReadUInt(playerUnit+0x90, Uint64))
				pathAddress = uintptr(gd.reader.ReadUInt(playerUnit+0x38, Uint64))
				playerNameAddr = uintptr(gd.reader.ReadUInt(playerUnit+0x10, Uint64))
				isCorpse = gd.reader.ReadUInt(playerUnit+0x1AE, Uint8)
				statsListExPtr = uintptr(gd.reader.ReadUInt(playerUnit+0x88, Uint64))
				playerMode = mode.PlayerMode(gd.reader.ReadUInt(playerUnit+0x0c, Uint32))
				nextPlayer = uintptr(gd.reader.ReadUInt(playerUnit+0x158, Uint64))
			}

			// Path-chain dereferences remain sequential (each read depends on
			// the previous) so batching helps no further here.
			room1Ptr := uintptr(gd.reader.ReadUInt(pathAddress+0x20, Uint64))
			room2Ptr := uintptr(gd.reader.ReadUInt(room1Ptr+0x18, Uint64))
			levelPtr := uintptr(gd.reader.ReadUInt(room2Ptr+0x90, Uint64))
			levelNo := gd.reader.ReadUInt(levelPtr+0x1F8, Uint32)
			xPos := gd.reader.ReadUInt(pathAddress+0x02, Uint16)
			yPos := gd.reader.ReadUInt(pathAddress+0x06, Uint16)
			name := gd.reader.ReadStringFromMemory(playerNameAddr, 0)

			isMainPlayer := gd.reader.ReadUInt(inventoryAddr+isMainOffset, Uint16)

			baseStats := gd.getStatsList(statsListExPtr + 0x30)
			stats := gd.getStatsList(statsListExPtr + 0xA8)
			states := gd.GetStates(statsListExPtr)

			rawPlayerUnits = append(rawPlayerUnits, RawPlayerUnit{
				UnitID:       data.UnitID(unitID),
				Address:      playerUnit,
				Name:         name,
				IsMainPlayer: isMainPlayer > 0,
				IsCorpse:     isCorpse == 1 && inventoryAddr > 0 && xPos > 0 && yPos > 0,
				Area:         area.ID(levelNo),
				Position: data.Position{
					X: int(xPos),
					Y: int(yPos),
				},
				IsHovered: hover.IsHovered && hover.UnitID == data.UnitID(unitID) && hover.UnitType == 0,
				States:    states,
				Stats:     stats,
				BaseStats: baseStats,
				Mode:      playerMode,
			})
			playerUnit = nextPlayer
		}
	}

	return rawPlayerUnits
}

func (gd *GameReader) GetPlayerUnit(mainPlayerUnit RawPlayerUnit) data.PlayerUnit {
	// Skills
	skillListPtr := uintptr(gd.reader.ReadUInt(mainPlayerUnit.Address+0x100, Uint64))
	skills := gd.getSkills(skillListPtr)

	leftSkillPtr := gd.reader.ReadUInt(skillListPtr+0x08, Uint64)
	leftSkillTxtPtr := uintptr(gd.reader.ReadUInt(uintptr(leftSkillPtr), Uint64))
	leftSkillId := uintptr(gd.reader.ReadUInt(leftSkillTxtPtr, Uint16))

	rightSkillPtr := gd.reader.ReadUInt(skillListPtr+0x10, Uint64)
	rightSkillTxtPtr := uintptr(gd.reader.ReadUInt(uintptr(rightSkillPtr), Uint64))
	rightSkillId := uintptr(gd.reader.ReadUInt(rightSkillTxtPtr, Uint16))

	// Class
	class := data.Class(gd.reader.ReadUInt(mainPlayerUnit.Address+0x17C, Uint32))

	availableWPs := gd.decodeWaypointMasks()

	d := data.PlayerUnit{
		Address:            mainPlayerUnit.Address,
		Name:               mainPlayerUnit.Name,
		ID:                 mainPlayerUnit.UnitID,
		Area:               mainPlayerUnit.Area,
		Position:           mainPlayerUnit.Position,
		Stats:              mainPlayerUnit.Stats,
		BaseStats:          mainPlayerUnit.BaseStats,
		Skills:             skills,
		States:             mainPlayerUnit.States,
		Class:              class,
		LeftSkill:          skill.ID(leftSkillId),
		RightSkill:         skill.ID(rightSkillId),
		AvailableWaypoints: availableWPs,
		Mode:               mainPlayerUnit.Mode,
	}

	return d
}

// WaypointTableData returns the waypoint table struct and the data buffer it points to
func (gd *GameReader) WaypointTableData() (structAddr uintptr, structBuf []byte, dataAddr uintptr, dataBuf []byte) {
	var structSize = uint(0x100)
	var dataSize = uint(0x200)

	ptrAddr := gd.moduleBaseAddressPtr + gd.offset.WaypointTableOffset
	structAddr = uintptr(gd.reader.ReadUInt(ptrAddr, Uint64))
	structBuf = gd.reader.ReadBytesFromMemory(structAddr, structSize)
	if len(structBuf) >= 0x18 {
		dataAddr = uintptr(binary.LittleEndian.Uint64(structBuf[0x10:]))
		if dataAddr != 0 {
			dataBuf = gd.reader.ReadBytesFromMemory(dataAddr, dataSize)
		}
	}
	return
}

// decodeWaypointMasks reads the global waypoint bitfield from the waypoint table.
// It returns nil if the table cannot be read.
func (gd *GameReader) decodeWaypointMasks() []area.ID {
	waypointOrder := []area.ID{
		// Act 1
		area.RogueEncampment, area.ColdPlains, area.StonyField, area.DarkWood, area.BlackMarsh,
		area.OuterCloister, area.JailLevel1, area.InnerCloister, area.CatacombsLevel2,
		// Act 2
		area.LutGholein, area.SewersLevel2Act2, area.DryHills, area.HallsOfTheDeadLevel2, area.FarOasis,
		area.LostCity, area.PalaceCellarLevel1, area.ArcaneSanctuary, area.CanyonOfTheMagi,
		// Act 3
		area.KurastDocks, area.SpiderForest, area.GreatMarsh, area.FlayerJungle, area.LowerKurast,
		area.KurastBazaar, area.UpperKurast, area.Travincal, area.DuranceOfHateLevel2,
		// Act 4
		area.ThePandemoniumFortress, area.CityOfTheDamned, area.RiverOfFlame,
		// Act 5
		area.Harrogath, area.FrigidHighlands, area.ArreatPlateau, area.CrystallinePassage, area.GlacialTrail,
		area.HallsOfPain, area.FrozenTundra, area.TheAncientsWay, area.TheWorldStoneKeepLevel2,
	}
	_, structBuf, _, _ := gd.WaypointTableData()
	if len(structBuf) < 4 {
		return nil
	}
	// First two bytes are the 0x0201 header; the next five bytes hold the bitfield (per classic D2 layout).
	if structBuf[0] != 0x02 || structBuf[1] != 0x01 {
		return nil
	}

	var bits uint64
	for i := 0; i < 5 && 2+i < len(structBuf); i++ {
		bits |= uint64(structBuf[2+i]) << (8 * i)
	}

	if bits == 0 {
		return nil
	}

	out := make([]area.ID, 0, len(waypointOrder))
	for idx, wpArea := range waypointOrder {
		if bits&(1<<uint(idx)) != 0 {
			out = append(out, wpArea)
		}
	}
	return out
}

func (gd *GameReader) getSkills(skillListPtr uintptr) map[skill.ID]skill.Points {
	skills := make(map[skill.ID]skill.Points)

	skillPtr := uintptr(gd.reader.ReadUInt(skillListPtr, Uint64))

	for skillPtr != 0 {
		skillTxtPtr := uintptr(gd.reader.ReadUInt(skillPtr, Uint64))
		skillTxt := uintptr(gd.reader.ReadUInt(skillTxtPtr, Uint16))
		lvl := gd.reader.ReadUInt(skillPtr+0x40, Uint16)
		qty := gd.reader.ReadUInt(skillPtr+0x48, Uint16)
		charges := gd.reader.ReadUInt(skillPtr+0x50, Uint16)

		shouldSetSkill := true
		existingSkill, exists := skills[skill.ID(skillTxt)]
		if exists {
			if existingSkill.Charges == 0 && charges > 0 {
				shouldSetSkill = false
			}
		}

		if shouldSetSkill {
			skills[skill.ID(skillTxt)] = skill.Points{
				Level:    lvl,
				Quantity: qty,
				Charges:  charges,
			}
		}

		skillPtr = uintptr(gd.reader.ReadUInt(skillPtr+0x08, Uint64))
	}

	return skills
}

func (gd *GameReader) GetStates(statsListExPtr uintptr) state.States {
	var states state.States

	// Batch path — 8 fixed-offset u32 reads at statsListEx+0xAF0 stride 4.
	// One CMD_ROP_READ_BATCH round-trip replaces 8 individual RPM calls when
	// ROP_READ=batch is wired. Falls back to sequential reads otherwise.
	entries := make([]BatchReadEntry, 8)
	for i := 0; i < 8; i++ {
		entries[i] = BatchReadEntry{
			Src: statsListExPtr + 0xAF0 + uintptr(i*4),
			Len: 4,
		}
	}
	if bufs, err := gd.Process.BatchReadBytes(entries); err == nil && len(bufs) == 8 {
		for i, buf := range bufs {
			stateByte := uint(binary.LittleEndian.Uint32(buf))
			offset := (32 * i) - 1
			states = append(states, calculateStates(stateByte, uint(offset))...)
		}
		return states
	}

	for i := 0; i < 8; i++ {
		offset := i * 4
		stateByte := gd.reader.ReadUInt(statsListExPtr+0xAF0+uintptr(offset), Uint32)

		offset = (32 * i) - 1
		states = append(states, calculateStates(stateByte, uint(offset))...)
	}

	return states
}

func calculateStates(stateFlag uint, offset uint) []state.State {
	var states []state.State
	if 0x00000001&stateFlag != 0 {
		states = append(states, state.State(1+offset))
	}
	if 0x00000002&stateFlag != 0 {
		states = append(states, state.State(2+offset))
	}
	if 0x00000004&stateFlag != 0 {
		states = append(states, state.State(3+offset))
	}
	if 0x00000008&stateFlag != 0 {
		states = append(states, state.State(4+offset))
	}
	if 0x00000010&stateFlag != 0 {
		states = append(states, state.State(5+offset))
	}
	if 0x00000020&stateFlag != 0 {
		states = append(states, state.State(6+offset))
	}
	if 0x00000040&stateFlag != 0 {
		states = append(states, state.State(7+offset))
	}
	if 0x00000080&stateFlag != 0 {
		states = append(states, state.State(8+offset))
	}
	if 0x00000100&stateFlag != 0 {
		states = append(states, state.State(9+offset))
	}
	if 0x00000200&stateFlag != 0 {
		states = append(states, state.State(10+offset))
	}
	if 0x00000400&stateFlag != 0 {
		states = append(states, state.State(11+offset))
	}
	if 0x00000800&stateFlag != 0 {
		states = append(states, state.State(12+offset))
	}
	if 0x00001000&stateFlag != 0 {
		states = append(states, state.State(13+offset))
	}
	if 0x00002000&stateFlag != 0 {
		states = append(states, state.State(14+offset))
	}
	if 0x00004000&stateFlag != 0 {
		states = append(states, state.State(15+offset))
	}
	if 0x00008000&stateFlag != 0 {
		states = append(states, state.State(16+offset))
	}
	if 0x00010000&stateFlag != 0 {
		states = append(states, state.State(17+offset))
	}
	if 0x00020000&stateFlag != 0 {
		states = append(states, state.State(18+offset))
	}
	if 0x00040000&stateFlag != 0 {
		states = append(states, state.State(19+offset))
	}
	if 0x00080000&stateFlag != 0 {
		states = append(states, state.State(20+offset))
	}
	if 0x00100000&stateFlag != 0 {
		states = append(states, state.State(21+offset))
	}
	if 0x00200000&stateFlag != 0 {
		states = append(states, state.State(22+offset))
	}
	if 0x00400000&stateFlag != 0 {
		states = append(states, state.State(23+offset))
	}
	if 0x00800000&stateFlag != 0 {
		states = append(states, state.State(24+offset))
	}
	if 0x01000000&stateFlag != 0 {
		states = append(states, state.State(25+offset))
	}
	if 0x02000000&stateFlag != 0 {
		states = append(states, state.State(26+offset))
	}
	if 0x04000000&stateFlag != 0 {
		states = append(states, state.State(27+offset))
	}
	if 0x08000000&stateFlag != 0 {
		states = append(states, state.State(28+offset))
	}
	if 0x10000000&stateFlag != 0 {
		states = append(states, state.State(29+offset))
	}
	if 0x20000000&stateFlag != 0 {
		states = append(states, state.State(30+offset))
	}
	if 0x40000000&stateFlag != 0 {
		states = append(states, state.State(31+offset))
	}
	if 0x80000000&stateFlag != 0 {
		states = append(states, state.State(32+offset))
	}

	return states
}
