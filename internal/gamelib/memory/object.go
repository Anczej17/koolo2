package memory

import (
	"encoding/binary"
	"sort"

	"local/internal/svc/internal/gamelib/data/entrance"
	"local/internal/svc/internal/gamelib/data/mode"

	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/area"
	"local/internal/svc/internal/gamelib/data/object"
	"local/internal/svc/internal/gamelib/utils"
)

func isPortal(txtFileNo int) bool {
	desc, ok := object.Desc[txtFileNo]
	return ok && desc.Name == "Portal"
}

func (gd *GameReader) Objects(playerPosition data.Position, hover data.HoverData) []data.Object {
	baseAddr := gd.Process.moduleBaseAddressPtr + gd.offset.UnitTable + (2 * 1024)
	unitTableBuffer := gd.reader.ReadBytesFromMemory(baseAddr, 128*8)

	var objects []data.Object

	for i := 0; i < 128; i++ {
		objectOffset := 8 * i
		objectUnitPtr := uintptr(ReadUIntFromBuffer(unitTableBuffer, uint(objectOffset), Uint64))

		for objectUnitPtr > 0 {
			// Batch the per-object header reads — 8 fixed-offset fields.
			// Collapses 8 individual NtRVM calls into one CMD_ROP_READ_BATCH
			// round-trip when ROP_READ=batch is active, otherwise falls
			// through to sequential reads.
			var (
				objectType   uint
				rawTxtFileNo uint
				unitID       uint
				objectMode   mode.ObjectMode
				unitDataPtr  uintptr
				pathPtr      uintptr
				shrineTextPtr uintptr
				nextUnit     uintptr
			)
			batched := false
			if bufs, err := gd.Process.BatchReadBytes([]BatchReadEntry{
				{Src: objectUnitPtr + 0x00, Len: 4}, // 0 objectType
				{Src: objectUnitPtr + 0x04, Len: 4}, // 1 rawTxtFileNo
				{Src: objectUnitPtr + 0x08, Len: 4}, // 2 unitID
				{Src: objectUnitPtr + 0x0c, Len: 4}, // 3 objectMode
				{Src: objectUnitPtr + 0x10, Len: 8}, // 4 unitDataPtr
				{Src: objectUnitPtr + 0x38, Len: 8}, // 5 pathPtr
				{Src: objectUnitPtr + 0x0A, Len: 8}, // 6 shrineTextPtr
				{Src: objectUnitPtr + 0x158, Len: 8}, // 7 next
			}); err == nil && len(bufs) == 8 {
				objectType = uint(binary.LittleEndian.Uint32(bufs[0]))
				rawTxtFileNo = uint(binary.LittleEndian.Uint32(bufs[1]))
				unitID = uint(binary.LittleEndian.Uint32(bufs[2]))
				objectMode = mode.ObjectMode(binary.LittleEndian.Uint32(bufs[3]))
				unitDataPtr = uintptr(binary.LittleEndian.Uint64(bufs[4]))
				pathPtr = uintptr(binary.LittleEndian.Uint64(bufs[5]))
				shrineTextPtr = uintptr(binary.LittleEndian.Uint64(bufs[6]))
				nextUnit = uintptr(binary.LittleEndian.Uint64(bufs[7]))
				batched = true
			}
			if !batched {
				objectType = gd.reader.ReadUInt(objectUnitPtr+0x00, Uint32)
			}

			if objectType == 2 {
				var txtFileNo uint
				if batched {
					txtFileNo = rawTxtFileNo & 0xFFFF
				} else {
					rawTxtFileNo = gd.reader.ReadUInt(objectUnitPtr+0x04, Uint32)
					txtFileNo = rawTxtFileNo & 0xFFFF
					unitID = gd.reader.ReadUInt(objectUnitPtr+0x08, Uint32)
					objectMode = mode.ObjectMode(gd.reader.ReadUInt(objectUnitPtr+0x0c, Uint32))
					unitDataPtr = uintptr(gd.reader.ReadUInt(objectUnitPtr+0x10, Uint64))
					pathPtr = uintptr(gd.reader.ReadUInt(objectUnitPtr+0x38, Uint64))
					shrineTextPtr = uintptr(gd.reader.ReadUInt(objectUnitPtr+0x0A, Uint64))
				}
				_ = rawTxtFileNo
				// Coordinates (X, Y)
				posX := gd.reader.ReadUInt(pathPtr+0x10, Uint16)
				posY := gd.reader.ReadUInt(pathPtr+0x14, Uint16)

				var shrineData object.ShrineData
				var portalData object.PortalData
				interactType := gd.reader.ReadUInt(unitDataPtr+0x08, Uint8)
				owner := gd.reader.ReadStringFromMemory(unitDataPtr+0x34, 32)

				// Handle portals
				if isPortal(int(txtFileNo)) {
					destArea := area.ID(gd.reader.ReadUInt(unitDataPtr+0x08, Uint8))
					portalData.DestArea = destArea
					// Handle Shrines
				} else {
					// shrineTextPtr already prefetched in the header batch.
					if !batched {
						shrineTextPtr = uintptr(gd.reader.ReadUInt(objectUnitPtr+0x0A, Uint64))
					}
					if shrineTextPtr > 0 {
						shrineType := gd.reader.ReadUInt(unitDataPtr+0x08, Uint8)
						shrineData = object.ShrineData{
							ShrineName: object.ShrineTypeNames[object.ShrineType(shrineType)],
							ShrineType: object.ShrineType(shrineType),
						}
					}
				}
				// Handle objects
				objects = append(objects, data.Object{
					ID:           data.UnitID(unitID),
					Name:         object.Name(int(txtFileNo)),
					IsHovered:    data.UnitID(unitID) == hover.UnitID && hover.UnitType == 2 && hover.IsHovered,
					InteractType: object.InteractType(interactType),
					Shrine:       shrineData,
					Selectable:   objectMode == mode.ObjectModeIdle,
					Position: data.Position{
						X: int(posX),
						Y: int(posY),
					},
					Owner:      owner,
					Mode:       objectMode,
					PortalData: portalData,
				})
			}
			if batched {
				objectUnitPtr = nextUnit
			} else {
				objectUnitPtr = uintptr(gd.reader.ReadUInt(objectUnitPtr+0x158, Uint64))
			}
		}
	}

	if len(objects) > 0 {
		sort.SliceStable(objects, func(i, j int) bool {
			distanceI := utils.DistanceFromPoint(playerPosition, objects[i].Position)
			distanceJ := utils.DistanceFromPoint(playerPosition, objects[j].Position)
			return distanceI < distanceJ
		})
	}

	return objects
}
func (gd *GameReader) Entrances(playerPosition data.Position, hover data.HoverData) []data.Entrance {
	baseAddr := gd.Process.moduleBaseAddressPtr + gd.offset.UnitTable + (5 * 1024)
	unitTableBuffer := gd.reader.ReadBytesFromMemory(baseAddr, 128*8)

	var entrances []data.Entrance

	for i := 0; i < 128; i++ {
		entranceOffset := 8 * i
		entranceUnitPtr := uintptr(ReadUIntFromBuffer(unitTableBuffer, uint(entranceOffset), Uint64))

		for entranceUnitPtr > 0 {
			// Batch the per-entrance header (type/txt/id/path/next).
			var (
				entranceType uint
				txtFileNo    uint
				unitID       uint
				pathPtr      uintptr
				nextUnit     uintptr
			)
			batched := false
			if bufs, err := gd.Process.BatchReadBytes([]BatchReadEntry{
				{Src: entranceUnitPtr + 0x00, Len: 4},
				{Src: entranceUnitPtr + 0x04, Len: 4},
				{Src: entranceUnitPtr + 0x08, Len: 4},
				{Src: entranceUnitPtr + 0x38, Len: 8},
				{Src: entranceUnitPtr + 0x158, Len: 8},
			}); err == nil && len(bufs) == 5 {
				entranceType = uint(binary.LittleEndian.Uint32(bufs[0]))
				txtFileNo = uint(binary.LittleEndian.Uint32(bufs[1]))
				unitID = uint(binary.LittleEndian.Uint32(bufs[2]))
				pathPtr = uintptr(binary.LittleEndian.Uint64(bufs[3]))
				nextUnit = uintptr(binary.LittleEndian.Uint64(bufs[4]))
				batched = true
			}
			if !batched {
				entranceType = gd.reader.ReadUInt(entranceUnitPtr+0x00, Uint32)
			}

			if entranceType == 5 {
				if !batched {
					txtFileNo = gd.reader.ReadUInt(entranceUnitPtr+0x04, Uint32)
					unitID = gd.reader.ReadUInt(entranceUnitPtr+0x08, Uint32)
					pathPtr = uintptr(gd.reader.ReadUInt(entranceUnitPtr+0x38, Uint64))
				}
				posX := gd.reader.ReadUInt(pathPtr+0x10, Uint16)
				posY := gd.reader.ReadUInt(pathPtr+0x14, Uint16)

				entrances = append(entrances, data.Entrance{
					ID:        data.UnitID(unitID),
					Name:      entrance.Name(txtFileNo),
					IsHovered: data.UnitID(unitID) == hover.UnitID && hover.UnitType == 5 && hover.IsHovered,
					Position: data.Position{
						X: int(posX),
						Y: int(posY),
					},
				})
			}
			if batched {
				entranceUnitPtr = nextUnit
			} else {
				entranceUnitPtr = uintptr(gd.reader.ReadUInt(entranceUnitPtr+0x158, Uint64))
			}
		}
	}

	return entrances
}
