package amb

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data"
)

// NPCIdentifyItems represents packet 0x34 (NPCIdentifyItems).
// Requests Cain to identify items in the player's inventory.
type NPCIdentifyItems struct {
	PacketID   byte
	NPCUnitID  uint32
	Unknown1   uint32
	CubeUnitID uint32
	Unknown2   uint32
	CubePosX   uint16
	CubePosY   uint16
}

// NewNPCIdentifyItems creates packet 0x34 for identifying items
func NewNPCIdentifyItems(npcUnitID, cubeUnitID data.UnitID, cubePosX, cubePosY uint16) *NPCIdentifyItems {
	cubeValue := uint32(cubeUnitID)
	unknown2 := uint32(0x00000000)
	if cubeUnitID == 0 {
		// Live Cain Identify capture 2026-04-26: no cube in inventory is
		// encoded as CubeUnitID=0xFFFFFFFF and Unknown2=0x000000FF.
		cubeValue = 0xFFFFFFFF
		unknown2 = 0x000000FF
	}

	return &NPCIdentifyItems{
		PacketID:   0x34,
		NPCUnitID:  uint32(npcUnitID),
		Unknown1:   0x00000000,
		CubeUnitID: cubeValue,
		Unknown2:   unknown2,
		CubePosX:   cubePosX,
		CubePosY:   cubePosY,
	}
}

// GetPayload converts the NPCIdentifyItems struct to bytes (21 bytes)
func (p *NPCIdentifyItems) GetPayload() []byte {
	buf := make([]byte, 21)
	buf[0] = p.PacketID
	binary.LittleEndian.PutUint32(buf[1:5], p.NPCUnitID)
	binary.LittleEndian.PutUint32(buf[5:9], p.Unknown1)
	binary.LittleEndian.PutUint32(buf[9:13], p.CubeUnitID)
	binary.LittleEndian.PutUint32(buf[13:17], p.Unknown2)
	binary.LittleEndian.PutUint16(buf[17:19], p.CubePosX)
	binary.LittleEndian.PutUint16(buf[19:21], p.CubePosY)
	return buf
}
