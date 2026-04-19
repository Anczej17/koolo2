package step

import (
	"fmt"

	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/skill"
	"local/internal/svc/internal/context"
	"local/internal/svc/internal/game"
	"local/internal/svc/internal/utils"
)

// SelectRightSkill selects a skill for the right mouse button via packet 0x3C.
// Full-packet bot: no HID fallback per user mandate 2026-04-19.
func SelectRightSkill(skillID skill.ID) error {
	ctx := context.Get()

	if ctx.Data.PlayerUnit.RightSkill == skillID {
		return nil
	}

	if ctx.PacketSender == nil {
		return fmt.Errorf("PacketSender unavailable, cannot select right skill %v", skillID)
	}

	if err := ctx.PacketSender.SelectRightSkill(skillID); err != nil {
		return fmt.Errorf("failed to select right skill %v: %w", skillID, err)
	}
	utils.Sleep(200)
	return nil
}

// SelectLeftSkill selects a skill for the left mouse button via packet 0x3C.
// Full-packet bot: no HID fallback per user mandate 2026-04-19.
func SelectLeftSkill(skillID skill.ID) error {
	ctx := context.Get()

	if ctx.Data.PlayerUnit.LeftSkill == skillID {
		return nil
	}

	if ctx.PacketSender == nil {
		return fmt.Errorf("PacketSender unavailable, cannot select left skill %v", skillID)
	}

	if err := ctx.PacketSender.SelectLeftSkill(skillID); err != nil {
		return fmt.Errorf("failed to select left skill %v: %w", skillID, err)
	}
	utils.Sleep(200)
	return nil
}

// SelectSkill selects a skill and returns the mouse button it was assigned to.
// Prefer left when both buttons are available, but return whichever is already selected.
func SelectSkill(skillID skill.ID) (game.MouseButton, bool) {
	ctx := context.Get()

	if ctx.Data.PlayerUnit.LeftSkill == skillID {
		return game.LeftButton, true
	}
	if ctx.Data.PlayerUnit.RightSkill == skillID {
		return game.RightButton, true
	}

	skillDesc, found := skill.Skills[skillID]
	if !found {
		return game.LeftButton, false
	}

	selectAndCheck := func(useLeft bool) (game.MouseButton, bool) {
		if useLeft {
			_ = SelectLeftSkill(skillID)
		} else {
			_ = SelectRightSkill(skillID)
		}
		ctx.RefreshGameData()
		if ctx.Data.PlayerUnit.LeftSkill == skillID {
			return game.LeftButton, true
		}
		if ctx.Data.PlayerUnit.RightSkill == skillID {
			return game.RightButton, true
		}
		return game.LeftButton, false
	}

	if skillDesc.LeftSkill {
		if button, ok := selectAndCheck(true); ok {
			return button, true
		}
	}
	if skillDesc.RightSkill {
		if button, ok := selectAndCheck(false); ok {
			return button, true
		}
	}

	return game.LeftButton, false
}

// SelectRightSkillByKeyBinding selects a skill using its keybinding directly via packet 0x3C.
// Full-packet bot: no HID fallback per user mandate 2026-04-19.
func SelectRightSkillByKeyBinding(kb data.KeyBinding) error {
	ctx := context.Get()

	for skillID, binding := range ctx.Data.KeyBindings.Skills {
		if binding.Key1[0] == kb.Key1[0] {
			return SelectRightSkill(skill.ID(skillID))
		}
	}

	return fmt.Errorf("no skill bound to keybinding %v", kb)
}

// SelectLeftSkillByKeyBinding selects a skill using its keybinding directly via packet 0x3C.
// Full-packet bot: no HID fallback per user mandate 2026-04-19.
func SelectLeftSkillByKeyBinding(kb data.KeyBinding) error {
	ctx := context.Get()

	for skillID, binding := range ctx.Data.KeyBindings.Skills {
		if binding.Key1[0] == kb.Key1[0] {
			return SelectLeftSkill(skill.ID(skillID))
		}
	}

	return fmt.Errorf("no skill bound to keybinding %v", kb)
}
