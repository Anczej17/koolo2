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
// Full-packet bot mandate 2026-04-19: prefer packet path. Emergency HID
// fallback only when PacketSender is nil (rmod presenter not yet attached
// during early game start) — without it, callers like MoveCharacter end up
// teleporting with the wrong right-skill (Blizzard sorc on WP click → casts
// Blizzard at WP coords instead of Teleport → bot stuck).
func SelectRightSkill(skillID skill.ID) error {
	ctx := context.Get()

	if ctx.Data.PlayerUnit.RightSkill == skillID {
		return nil
	}

	if ctx.PacketSender == nil {
		ctx.Logger.Warn(fmt.Sprintf("PacketSender nil during SelectRightSkill(%v) - falling back to HID keybinding", skillID))
		return selectSkillViaHIDIfAvailable(skillID)
	}

	if err := ctx.PacketSender.SelectRightSkill(skillID); err != nil {
		ctx.Logger.Warn(fmt.Sprintf("packet 0x3C SelectRightSkill(%v) failed: %s - falling back to HID keybinding", skillID, err.Error()))
		return selectSkillViaHIDIfAvailable(skillID)
	}
	utils.Sleep(200)
	return nil
}

// SelectLeftSkill selects a skill for the left mouse button via packet 0x3C.
// Same fallback policy as SelectRightSkill.
func SelectLeftSkill(skillID skill.ID) error {
	ctx := context.Get()

	if ctx.Data.PlayerUnit.LeftSkill == skillID {
		return nil
	}

	if ctx.PacketSender == nil {
		ctx.Logger.Warn(fmt.Sprintf("PacketSender nil during SelectLeftSkill(%v) - falling back to HID keybinding", skillID))
		return selectSkillViaHIDIfAvailable(skillID)
	}

	if err := ctx.PacketSender.SelectLeftSkill(skillID); err != nil {
		ctx.Logger.Warn(fmt.Sprintf("packet 0x3C SelectLeftSkill(%v) failed: %s - falling back to HID keybinding", skillID, err.Error()))
		return selectSkillViaHIDIfAvailable(skillID)
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
// Falls through to SelectRightSkill which handles fallback policy.
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
// Falls through to SelectLeftSkill which handles fallback policy.
func SelectLeftSkillByKeyBinding(kb data.KeyBinding) error {
	ctx := context.Get()

	for skillID, binding := range ctx.Data.KeyBindings.Skills {
		if binding.Key1[0] == kb.Key1[0] {
			return SelectLeftSkill(skill.ID(skillID))
		}
	}

	return fmt.Errorf("no skill bound to keybinding %v", kb)
}

// selectSkillViaHIDIfAvailable presses the keybinding for skillID. Used only
// when PacketSender is unavailable (early-game pre-rmod-attach). Returns nil
// silently if the skill isn't bound to a key — caller continues with whatever
// skill is currently selected.
func selectSkillViaHIDIfAvailable(skillID skill.ID) error {
	ctx := context.Get()

	kb, found := ctx.Data.KeyBindings.KeyBindingForSkill(skillID)
	if !found {
		ctx.Logger.Warn(fmt.Sprintf("HID fallback: skill %v has no keybinding, leaving current skill selected", skillID))
		return nil
	}

	ctx.HID.PressKeyBinding(kb)
	utils.Sleep(50)
	return nil
}
