package memory

import (
	"local/internal/svc/internal/gamelib/data/area"
)

func (gd *GameReader) TerrorZones() (areas []area.ID) {
	structPtr := gd.moduleBaseAddressPtr + gd.offset.TZ
	zonesPtr := uintptr(gd.reader.ReadUInt(structPtr, Uint64))
	actualActiveZoneCount := int(gd.reader.ReadUInt(structPtr+0x8, Uint8))

	for i := 0; i < actualActiveZoneCount; i++ {
		tzArea := gd.reader.ReadUInt(zonesPtr+uintptr(i*Uint32), Uint32)
		if tzArea != 0 {
			areas = append(areas, area.ID(tzArea))
		}
	}

	return
}
