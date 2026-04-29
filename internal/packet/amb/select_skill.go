package amb

import (
	"encoding/binary"

	"local/internal/svc/internal/gamelib/data/skill"
)

type SelectSkillHand byte

const (
	RightHand SelectSkillHand = 0x00
	LeftHand  SelectSkillHand = 0x80
)

type SelectSkill struct {
	PacketID      byte
	SkillID       uint16
	NULL          byte // Documentation specifies NULL byte at offset 3
	Hand          byte // Changed from uint16 to byte per documentation
	ChargedItemID uint32
}

// NewSelectSkill creates a new SelectSkill packet
func NewSelectSkill(skillID skill.ID, hand SelectSkillHand) *SelectSkill {
	return &SelectSkill{
		PacketID:      0x3C, // 60 in decimal
		SkillID:       uint16(skillID),
		NULL:          0x00,
		Hand:          byte(hand),
		ChargedItemID: 0xFFFFFFFF, // uint.MaxValue
	}
}

// NewSelectSkillWithItem creates a new SelectSkill packet for a charged item skill
func NewSelectSkillWithItem(skillID skill.ID, hand SelectSkillHand, chargedItemID uint32) *SelectSkill {
	return &SelectSkill{
		PacketID:      0x3C,
		SkillID:       uint16(skillID),
		NULL:          0x00,
		Hand:          byte(hand),
		ChargedItemID: chargedItemID,
	}
}

func (p *SelectSkill) GetPayload() []byte {
	buf := make([]byte, 9)
	buf[0] = p.PacketID
	binary.LittleEndian.PutUint16(buf[1:3], p.SkillID)
	buf[3] = p.NULL
	buf[4] = p.Hand
	binary.LittleEndian.PutUint32(buf[5:9], p.ChargedItemID)
	return buf
}
