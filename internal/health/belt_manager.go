package health

import (
	"fmt"
	"log/slog"
	"strings"

	"local/internal/svc/internal/event"
	"local/internal/svc/internal/game"
	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/packet/amb"
)

type BeltManager struct {
	data         *game.Data
	hid          *game.HID
	packetSender *game.PacketSender
	logger       *slog.Logger
	supervisor   string
}

func NewBeltManager(data *game.Data, hid *game.HID, packetSender *game.PacketSender, logger *slog.Logger, supervisor string) *BeltManager {
	return &BeltManager{
		data:         data,
		hid:          hid,
		packetSender: packetSender,
		logger:       logger,
		supervisor:   supervisor,
	}
}

func (bm BeltManager) DrinkPotion(potionType data.PotionType, merc bool) bool {
	p, found := bm.data.Inventory.Belt.GetFirstPotion(potionType)
	if found {
		if bm.hid != nil && bm.hid.IsDisabled() {
			return bm.drinkPotionPacket(potionType, p, merc)
		}

		binding := bm.data.KeyBindings.UseBelt[p.X]
		if merc {
			bm.hid.PressKeyWithModifier(binding.Key1[0], game.ShiftKey)
			bm.logger.Debug(fmt.Sprintf("Using %s potion on Mercenary [Column: %d]. HP: %d", potionType, p.X+1, bm.data.MercHPPercent()))
			event.Send(event.UsedPotion(event.Text(bm.supervisor, ""), potionType, true))
			return true
		}
		bm.hid.PressKeyBinding(binding)
		bm.logger.Debug(fmt.Sprintf("Using %s potion [Column: %d]. HP: %d MP: %d", potionType, p.X+1, bm.data.PlayerUnit.HPPercent(), bm.data.PlayerUnit.MPPercent()))
		event.Send(event.UsedPotion(event.Text(bm.supervisor, ""), potionType, false))
		return true
	}

	return false
}

func (bm BeltManager) drinkPotionPacket(potionType data.PotionType, pos data.Position, merc bool) bool {
	if bm.packetSender == nil {
		bm.logger.Warn("Potion skipped: HID disabled and packet sender unavailable", slog.String("potion", string(potionType)))
		return false
	}

	potion, found := bm.beltItemAt(pos)
	if !found {
		bm.logger.Warn("Potion skipped: belt item not found at selected slot",
			slog.String("potion", string(potionType)),
			slog.Int("x", pos.X),
			slog.Int("y", pos.Y))
		return false
	}

	if merc {
		mercUnit, ok := bm.findMerc()
		if !ok {
			bm.logger.Warn("Mercenary potion skipped: mercenary unit not found",
				slog.String("potion", string(potionType)))
			return false
		}
		if err := bm.packetSender.UseItemOnUnit(
			uint32(potion.UnitID),
			uint32(mercUnit.UnitID),
			1,
			byte(pos.X),
			byte(pos.Y),
			4,
			bm.otherBeltSlots(pos),
		); err != nil {
			bm.logger.Warn("Mercenary potion packet failed", slog.String("potion", string(potionType)), slog.Any("error", err))
			return false
		}
		bm.logger.Debug(fmt.Sprintf("Using %s potion on Mercenary via packet [Column: %d]. HP: %d", potionType, pos.X+1, bm.data.MercHPPercent()))
		event.Send(event.UsedPotion(event.Text(bm.supervisor, ""), potionType, true))
		return true
	}

	if err := bm.packetSender.UseItemFromBelt(potion); err != nil {
		bm.logger.Warn("Potion packet failed", slog.String("potion", string(potionType)), slog.Any("error", err))
		return false
	}
	bm.logger.Debug(fmt.Sprintf("Using %s potion via packet [Column: %d]. HP: %d MP: %d", potionType, pos.X+1, bm.data.PlayerUnit.HPPercent(), bm.data.PlayerUnit.MPPercent()))
	event.Send(event.UsedPotion(event.Text(bm.supervisor, ""), potionType, false))
	return true
}

func (bm BeltManager) beltItemAt(pos data.Position) (data.Item, bool) {
	for _, itm := range bm.data.Inventory.Belt.Items {
		if itm.Position == pos {
			return itm, true
		}
	}
	return data.Item{}, false
}

func (bm BeltManager) findMerc() (data.Monster, bool) {
	if !bm.data.HasMerc {
		return data.Monster{}, false
	}
	for _, monster := range bm.data.Monsters {
		if monster.IsMerc() {
			return monster, true
		}
	}
	return data.Monster{}, false
}

func (bm BeltManager) otherBeltSlots(used data.Position) [3]amb.BeltSlotState {
	states := [3]amb.BeltSlotState{
		{ItemUnitID: 0xFFFFFFFF},
		{ItemUnitID: 0xFFFFFFFF},
		{ItemUnitID: 0xFFFFFFFF},
	}

	idx := 0
	for x := 0; x < 4 && idx < len(states); x++ {
		if x == used.X {
			continue
		}
		pos := data.Position{X: x, Y: used.Y}
		if itm, ok := bm.beltItemAt(pos); ok {
			states[idx] = amb.BeltSlotState{
				ItemUnitID:   uint32(itm.UnitID),
				PosXCurrent:  byte(pos.X),
				PosXPrevious: byte(pos.X),
			}
		}
		idx++
	}

	return states
}

// ShouldBuyPotions will return true if more than 25% of belt is empty (ignoring rejuv)
func (bm BeltManager) ShouldBuyPotions() bool {
	targetHealingAmount := bm.data.CharacterCfg.Inventory.BeltColumns.Total(data.HealingPotion) * bm.data.Inventory.Belt.Rows()
	targetManaAmount := bm.data.CharacterCfg.Inventory.BeltColumns.Total(data.ManaPotion) * bm.data.Inventory.Belt.Rows()
	targetRejuvAmount := bm.data.CharacterCfg.Inventory.BeltColumns.Total(data.RejuvenationPotion) * bm.data.Inventory.Belt.Rows()

	currentHealing, currentMana, currentRejuv := bm.getCurrentPotions()

	bm.logger.Debug(fmt.Sprintf(
		"Belt Stats Health: %d/%d healing, %d/%d mana, %d/%d rejuv.",
		currentHealing,
		targetHealingAmount,
		currentMana,
		targetManaAmount,
		currentRejuv,
		targetRejuvAmount,
	))

	if currentHealing < int(float32(targetHealingAmount)*0.75) || currentMana < int(float32(targetManaAmount)*0.75) {
		bm.logger.Debug("Need more pots, let's buy them.")
		return true
	}

	return false
}

func (bm BeltManager) getCurrentPotions() (int, int, int) {
	currentHealing := 0
	currentMana := 0
	currentRejuv := 0
	for _, i := range bm.data.Inventory.Belt.Items {
		if strings.Contains(string(i.Name), string(data.HealingPotion)) {
			currentHealing++
			continue
		}
		if strings.Contains(string(i.Name), string(data.ManaPotion)) {
			currentMana++
			continue
		}
		if strings.Contains(string(i.Name), string(data.RejuvenationPotion)) {
			currentRejuv++
		}
	}

	return currentHealing, currentMana, currentRejuv
}

func (bm BeltManager) GetMissingCount(potionType data.PotionType) int {
	currentHealing, currentMana, currentRejuv := bm.getCurrentPotions()

	switch potionType {
	case data.HealingPotion:
		targetAmount := bm.data.CharacterCfg.Inventory.BeltColumns.Total(data.HealingPotion) * bm.data.Inventory.Belt.Rows()
		missingPots := targetAmount - currentHealing
		if missingPots < 0 {
			return 0
		}
		return missingPots
	case data.ManaPotion:
		targetAmount := bm.data.CharacterCfg.Inventory.BeltColumns.Total(data.ManaPotion) * bm.data.Inventory.Belt.Rows()
		missingPots := targetAmount - currentMana
		if missingPots < 0 {
			return 0
		}
		return missingPots
	case data.RejuvenationPotion:
		targetAmount := bm.data.CharacterCfg.Inventory.BeltColumns.Total(data.RejuvenationPotion) * bm.data.Inventory.Belt.Rows()
		missingPots := targetAmount - currentRejuv
		if missingPots < 0 {
			return 0
		}
		return missingPots
	}

	return 0
}
