package game

import (
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/area"
	"local/internal/svc/internal/gamelib/data/item"
	"local/internal/svc/internal/gamelib/data/skill"
	"local/internal/svc/internal/gamelib/data/stat"
	"local/internal/svc/internal/livetrace"
	packet "local/internal/svc/internal/packet"
	"local/internal/svc/internal/packet/amb"
)

// tracePacketSend instruments the bottom three send primitives. Called from
// SendPacket/SendUIPacket/SendDualPacket with path label.
func tracePacketSend(path string, payload []byte, fn func() error) error {
	start := time.Now()
	err := fn()
	if livetrace.Get().IsEnabled() && len(payload) > 0 {
		errStr := ""
		if err != nil {
			errStr = err.Error()
		}
		livetrace.Get().Packet(path, payload[0], payload, errStr, time.Since(start).Microseconds())
	}
	return err
}

type ProcessSender interface {
	SendPacket([]byte) error
	SendUIPacket([]byte) error
	SendDualPacket([]byte) error
	ClickAt(x, y int32, btn byte) error
	ForceClick(x, y int32) error
	PostKeyInProcess(vk byte) error
}

type classicAPCSender interface {
	SendPacketAPC([]byte) error
}

type dualAPCSender interface {
	SendDualPacketAPC([]byte) error
}

type uiDualSender interface {
	SendUIDualPacket([]byte) error
}

type sendFnHijackSender interface {
	SendPacketViaSendFnHijack([]byte) error
}

type dualWrapSender interface {
	SendPacketViaDualWrap([]byte) error
}

type npcDialogWrapperSender interface {
	Send9BWrapper(opcode uint8, arg1, arg2 uint32) error
}

type npcDialogOptionCaller interface {
	CallNPCDialogOption(actionID uint32) error
}

type npcCancelCaller interface {
	CallNPCCancel(npcID uint32) error
}

func (ps *PacketSender) sendAMBPacketAPC(packet []byte) error {
	if err := ps.ensureEnabled("sendAMBPacketAPC"); err != nil {
		return err
	}
	if sender, ok := ps.process.(classicAPCSender); ok {
		return tracePacketSend("amb-apc", packet, func() error {
			traceAMBNPCSend("amb-apc", packet)
			return sender.SendPacketAPC(packet)
		})
	}
	traceAMBNPCSend("amb-game", packet)
	return ps.SendPacket(packet)
}

func (ps *PacketSender) sendAMBDualPacketAPC(packet []byte) error {
	if err := ps.ensureEnabled("sendAMBDualPacketAPC"); err != nil {
		return err
	}
	if sender, ok := ps.process.(dualAPCSender); ok {
		return tracePacketSend("amb-dual-apc", packet, func() error {
			traceAMBNPCSend("amb-dual-apc", packet)
			return sender.SendDualPacketAPC(packet)
		})
	}
	traceAMBNPCSend("amb-dual", packet)
	if err := ps.SendDualPacket(packet); err != nil {
		return fmt.Errorf("AMB dual send failed opcode=0x%02X: %w", packet[0], err)
	}
	return nil
}

func (ps *PacketSender) sendAMBDualPacket(packet []byte) error {
	if err := ps.ensureEnabled("sendAMBDualPacket"); err != nil {
		return err
	}
	return tracePacketSend("amb-dual", packet, func() error {
		traceAMBNPCSend("amb-dual", packet)
		if err := ps.process.SendDualPacket(packet); err != nil {
			return fmt.Errorf("AMB dual send failed opcode=0x%02X: %w", packet[0], err)
		}
		return nil
	})
}

func (ps *PacketSender) sendAMBUIDualPacket(packet []byte) error {
	if err := ps.ensureEnabled("sendAMBUIDualPacket"); err != nil {
		return err
	}
	if sender, ok := ps.process.(uiDualSender); ok {
		return tracePacketSend("amb-ui-dual", packet, func() error {
			traceAMBNPCSend("amb-ui-dual", packet)
			if err := sender.SendUIDualPacket(packet); err != nil {
				return fmt.Errorf("AMB UI-dual send failed opcode=0x%02X: %w", packet[0], err)
			}
			return nil
		})
	}
	return tracePacketSend("amb-ui", packet, func() error {
		traceAMBNPCSend("amb-ui", packet)
		if err := ps.SendUIPacket(packet); err != nil {
			return fmt.Errorf("AMB UI send failed opcode=0x%02X: %w", packet[0], err)
		}
		return nil
	})
}

func (ps *PacketSender) sendAMBSendFnHijackPacket(packet []byte) error {
	if err := ps.ensureEnabled("sendAMBSendFnHijackPacket"); err != nil {
		return err
	}
	if sender, ok := ps.process.(sendFnHijackSender); ok {
		return tracePacketSend("amb-sendfn-hijack", packet, func() error {
			traceAMBNPCSend("amb-sendfn-hijack", packet)
			if err := sender.SendPacketViaSendFnHijack(packet); err != nil {
				return fmt.Errorf("AMB hijack send failed opcode=0x%02X: %w", packet[0], err)
			}
			return nil
		})
	}
	return ps.sendAMBPacketAPC(packet)
}

func (ps *PacketSender) sendNPCDialogOption(actionID uint32, npcID data.UnitID) error {
	if err := ps.ensureEnabled("sendNPCDialogOption"); err != nil {
		return err
	}
	if caller, ok := ps.process.(npcDialogOptionCaller); ok {
		if err := caller.CallNPCDialogOption(actionID); err == nil {
			return nil
		} else {
			log.Printf("[AMB] native-dialog-option failed action=%d npc=0x%X err=%v; falling back to raw 0x38", actionID, npcID, err)
		}
	}
	payload := amb.NewNPCAction(amb.NPCActionType(actionID), npcID).GetPayload()
	return ps.SendAMBPacket(payload)
}

func (ps *PacketSender) sendNPCCancel(npcID data.UnitID) error {
	if err := ps.ensureEnabled("sendNPCCancel"); err != nil {
		return err
	}
	if caller, ok := ps.process.(npcCancelCaller); ok {
		if err := caller.CallNPCCancel(uint32(npcID)); err == nil {
			return nil
		} else {
			log.Printf("[AMB] native-cancel failed npc=0x%X err=%v; falling back to raw 0x30", npcID, err)
		}
	}
	payload := amb.NewNPCCancel(npcID).GetPayload()
	return ps.SendAMBPacket(payload)
}

// Mouse button bits as consumed by D2R real_click_worker (see Phase 9 spec).
const (
	MouseLeft   byte = 1
	MouseMiddle byte = 2
	MouseRight  byte = 4
)

type PacketSender struct {
	process  ProcessSender
	disabled atomic.Bool
}

func NewPacketSender(process ProcessSender) *PacketSender {
	return &PacketSender{
		process: process,
	}
}

type PacketRouteAvailability struct {
	Game               bool `json:"game"`
	UI                 bool `json:"ui"`
	Dual               bool `json:"dual"`
	APC                bool `json:"apc"`
	DualAPC            bool `json:"dual_apc"`
	UIDual             bool `json:"ui_dual"`
	DualWrap           bool `json:"dualwrap"`
	SendFnHijack       bool `json:"send_hijack"`
	Native9BWrapper    bool `json:"native_9b_wrapper"`
	NativeDialogOption bool `json:"native_dialog_option"`
	NativeCancel       bool `json:"native_cancel"`
}

// RouteAvailability reports which send mechanisms the wrapped process exposes.
// It does not send anything; it is used to compare debug endpoint routes with
// production PacketSender wiring.
func (ps *PacketSender) RouteAvailability() PacketRouteAvailability {
	if ps == nil || ps.process == nil {
		return PacketRouteAvailability{}
	}
	_, hasAPC := ps.process.(classicAPCSender)
	_, hasDualAPC := ps.process.(dualAPCSender)
	_, hasUIDual := ps.process.(uiDualSender)
	_, hasDualWrap := ps.process.(dualWrapSender)
	_, hasSendFnHijack := ps.process.(sendFnHijackSender)
	_, has9B := ps.process.(npcDialogWrapperSender)
	_, hasDialogOption := ps.process.(npcDialogOptionCaller)
	_, hasCancel := ps.process.(npcCancelCaller)
	return PacketRouteAvailability{
		Game:               true,
		UI:                 true,
		Dual:               true,
		APC:                hasAPC,
		DualAPC:            hasDualAPC,
		UIDual:             hasUIDual,
		DualWrap:           hasDualWrap,
		SendFnHijack:       hasSendFnHijack,
		Native9BWrapper:    has9B,
		NativeDialogOption: hasDialogOption,
		NativeCancel:       hasCancel,
	}
}

var ErrPacketSenderDisabled = errors.New("packet sender disabled")

func (ps *PacketSender) Disable() {
	if ps != nil {
		ps.disabled.Store(true)
	}
}

func (ps *PacketSender) ensureEnabled(op string) error {
	if ps == nil || ps.disabled.Load() {
		return fmt.Errorf("%s: %w", op, ErrPacketSenderDisabled)
	}
	return nil
}

func (ps *PacketSender) SendPacket(packet []byte) error {
	if err := ps.ensureEnabled("SendPacket"); err != nil {
		return err
	}
	return tracePacketSend("game", packet, func() error { return ps.process.SendPacket(packet) })
}

// SendAMBPacket follows the sender AMB/d2go packet constructors were proven
// against: main-thread APC into D2GS_SendPacket. In this fork SendPacket may
// be overridden by presenter/rmod, so AMB-critical flows bypass that override
// when the process exposes SendPacketAPC.
func (ps *PacketSender) SendAMBPacket(packet []byte) error {
	if err := ps.ensureEnabled("SendAMBPacket"); err != nil {
		return err
	}
	if len(packet) > 0 {
		switch packet[0] {
		case 0x2F, 0x30, 0x38:
			return ps.sendAMBDualPacketAPC(packet)
		case 0x34:
			return ps.sendAMBDualPacketAPC(packet)
		case 0x35, 0x50:
			return ps.sendAMBDualPacketAPC(packet)
		}
	}
	return ps.sendAMBPacketAPC(packet)
}

func traceAMBNPCSend(route string, packet []byte) {
	if len(packet) == 0 {
		return
	}
	switch packet[0] {
	case 0x03, 0x04, 0x05, 0x06, 0x0C, 0x0D, 0x13, 0x18, 0x20, 0x26, 0x2F, 0x30, 0x32, 0x33, 0x34, 0x35, 0x38, 0x3C, 0x40, 0x41, 0x4D, 0x50, 0x54, 0x55:
		hexPayload := hex.EncodeToString(packet)
		if len(hexPayload) > 96 {
			hexPayload = hexPayload[:96] + "..."
		}
		log.Printf("[AMB] %s opcode=0x%02X len=%d hex=%s", route, packet[0], len(packet), hexPayload)
	}
}

// SendUIPacket sends a packet via the D2R UI NetMan path (vtable[5]).
// Required for identify/buy/sell/cube/gamble opcodes that crash when sent
// through the regular Game NetMan path.
func (ps *PacketSender) SendUIPacket(packet []byte) error {
	if err := ps.ensureEnabled("SendUIPacket"); err != nil {
		return err
	}
	return tracePacketSend("ui", packet, func() error { return ps.process.SendUIPacket(packet) })
}

// SendDualPacket sends via the mirror buffer + send_fn dual path.
// This is how D2R's internal vendor wrapper dispatches packets.
func (ps *PacketSender) SendDualPacket(packet []byte) error {
	if err := ps.ensureEnabled("SendDualPacket"); err != nil {
		return err
	}
	return tracePacketSend("dual", packet, func() error { return ps.process.SendDualPacket(packet) })
}

// ForceClick fires an in-process click with ForceMove key held.
// This is the primary non-HID movement method. D2R interprets it as
// "move to this screen position" regardless of UI elements.
func (ps *PacketSender) ForceClick(x, y int32) error {
	if err := ps.ensureEnabled("ForceClick"); err != nil {
		return err
	}
	return ps.process.ForceClick(x, y)
}

// ClickAt fires a synthetic click via the in-process Phase 9 path
// (real_click_worker → vtable[1]). Bypasses OS input queue entirely; the
// user's hardware cursor is not touched. A WalkTo on the action layer is
// just `ClickAt(x, y, MouseLeft)`.
func (ps *PacketSender) ClickAt(x, y int32, btn byte) error {
	if err := ps.ensureEnabled("ClickAt"); err != nil {
		return err
	}
	return ps.process.ClickAt(x, y, btn)
}

// PostKeyInProcess posts WM_KEYDOWN/WM_KEYUP inside D2R via the presenter path.
// It is not HID/SendInput and does not require foreground focus.
func (ps *PacketSender) PostKeyInProcess(vk byte) error {
	if err := ps.ensureEnabled("PostKeyInProcess"); err != nil {
		return err
	}
	return ps.process.PostKeyInProcess(vk)
}

func (ps *PacketSender) PickUpItem(item data.Item) error {
	err := ps.SendPacket(packet.NewPickUpItem(item).GetPayload())
	if err != nil {
		return fmt.Errorf("failed to send pick item packet: %w", err)
	}

	return nil
}

func (ps *PacketSender) Teleport(target, playerPos data.Position) error {
	payload := packet.NewTeleport(target, playerPos).GetPayload()

	if err := ps.SendPacket(payload); err != nil {
		return fmt.Errorf("failed to send teleport packet: %w", err)
	}
	return nil
}

// CastSkillAtLocation sends packet 0x0C (cast currently-equipped right skill
// at world coordinates) via the D2GS_SendPacket APC path, matching original
// koolo's PacketSender.CastSkillAtLocation flow.
//
// 2026-04-21 route change: reverted SendDualPacket → SendPacket. The dual
// path's WriteProcessMemory-to-mirror step ran in PARALLEL with D2GS
// processing and caused client-side state to silently drop the cast (buff
// icon never lit). Original koolo uses plain SendPacket + QueueUserAPC on
// D2R's main thread; the client then processes the packet in-process,
// covering both the network send and local state update.
func (ps *PacketSender) CastSkillAtLocation(target, playerPos data.Position) error {
	return ps.CastRightSkillAMB(uint16(target.X), uint16(target.Y))
}

// SelectRightSkill sends packet 0x3C to change the active right-click skill
// Use cases: Switch skills via packet instead of clicking UI (F1-F9 functionality)
// Useful for quick skill switching during combat or automation
func (ps *PacketSender) SelectRightSkill(skillID skill.ID) error {
	if err := ps.SendAMBPacket(amb.NewSelectSkill(skillID, amb.RightHand).GetPayload()); err != nil {
		return fmt.Errorf("failed to send skill selection packet: %w", err)
	}
	return nil
}

// SelectLeftSkill sends packet 0x3C to change the active left-click skill
// Use cases: Switch left-click skills via packet instead of clicking UI
// Useful for automation or quick skill switching
func (ps *PacketSender) SelectLeftSkill(skillID skill.ID) error {
	if err := ps.SendAMBPacket(amb.NewSelectSkill(skillID, amb.LeftHand).GetPayload()); err != nil {
		return fmt.Errorf("failed to send left skill selection packet: %w", err)
	}
	return nil
}

// LearnSkill sends packet 0x3B to allocate a skill point
// Use cases: Faster skill point allocation during leveling (Sorceress leveling)
// Bypasses UI interaction for instant skill learning
func (ps *PacketSender) LearnSkill(skillID skill.ID) error {
	if err := ps.SendPacket(packet.NewLearnSkill(skillID).GetPayload()); err != nil {
		return fmt.Errorf("failed to send learn skill packet: %w", err)
	}
	return nil
}

// AllocateStatPoint sends packet 0x3A to allocate a stat point
// Use cases: Faster stat point allocation during leveling (Sorceress leveling)
// Bypasses UI interaction for instant stat allocation
func (ps *PacketSender) AllocateStatPoint(statID stat.ID) error {
	if err := ps.SendPacket(packet.NewAllocateStat(statID).GetPayload()); err != nil {
		return fmt.Errorf("failed to send allocate stat packet: %w", err)
	}
	return nil
}

// Movement packets (0x01/0x02/0x03/0x04) use AMB's authoritative layout:
// 201-byte zero padding appended after the opcode-specific header. Without
// that padding the server silently rejects the move. All four go via the
// D2GS_SendPacket APC path so D2R's client processes the move locally.

// WalkToLocation sends packet 0x01 — walk to world coordinate. 210 B total.
func (ps *PacketSender) WalkToLocation(destX, destY, originX, originY uint16) error {
	if err := ps.SendAMBPacket(amb.NewWalkToLocation(destX, destY, originX, originY).GetPayload()); err != nil {
		return fmt.Errorf("failed to send walk-to-location packet: %w", err)
	}
	return nil
}

// RunToLocation sends packet 0x03 — run to world coordinate. 210 B total.
func (ps *PacketSender) RunToLocation(destX, destY, originX, originY uint16) error {
	if err := ps.SendAMBPacket(amb.NewRunToLocation(destX, destY, originX, originY).GetPayload()); err != nil {
		return fmt.Errorf("failed to send run-to-location packet: %w", err)
	}
	return nil
}

// WalkToUnit sends packet 0x02 — walk toward a unit. 214 B total.
// unitType: 0=Player, 1=Monster, 2=Object, 3=Missile, 4=Item, 5=Tile.
func (ps *PacketSender) WalkToUnit(unitType int, unitID data.UnitID, originX, originY uint16) error {
	if err := ps.SendAMBPacket(amb.NewWalkToUnit(unitType, unitID, originX, originY).GetPayload()); err != nil {
		return fmt.Errorf("failed to send walk-to-unit packet: %w", err)
	}
	return nil
}

// RunToUnit sends packet 0x04 — run toward a unit. 214 B total.
// unitType: 0=Player, 1=Monster, 2=Object, 3=Missile, 4=Item, 5=Tile.
//
// Note: our older MoveToEntity builder also uses opcode 0x04 but with an
// interaction-style 18 B layout. AMB's authoritative interpretation is
// "run to unit" with 201-byte padding — see OpRunToUnit in opcodes.go.
// Callers that need the legacy MoveToEntity behavior should be reviewed.
func (ps *PacketSender) RunToUnit(unitType int, unitID data.UnitID, originX, originY uint16) error {
	if err := ps.SendAMBPacket(amb.NewRunToUnit(unitType, unitID, originX, originY).GetPayload()); err != nil {
		return fmt.Errorf("failed to send run-to-unit packet: %w", err)
	}
	return nil
}

// NPCPrimeInteraction mirrors the AMB/native NPC pre-state immediately before
// NPCInit: 0x4D PreInteract followed by 0x04 RunToUnit(monster, npcUnitID,
// playerPos). Native captures show this pre-state before 0x2F/0x38 even when
// the player is already close to NPC. The 0x4D marker is Game-buffer only in
// the native flow; the following 0x04 prime is dual-buffered.
func (ps *PacketSender) NPCPrimeInteraction(npcID, playerID data.UnitID, playerPos, npcPos data.Position) error {
	pre := packet.NewNPCEntityAction(
		npcID,
		playerID,
		uint16(playerPos.X),
		uint16(playerPos.Y),
		uint16(npcPos.X),
		uint16(npcPos.Y),
	)
	if err := ps.sendAMBPacketAPC(pre); err != nil {
		return fmt.Errorf("NPCPrimeInteraction pre-interact send failed: %w", err)
	}
	if err := ps.sendAMBDualPacketAPC(amb.NewRunToUnit(1, npcID, uint16(playerPos.X), uint16(playerPos.Y)).GetPayload()); err != nil {
		return fmt.Errorf("NPCPrimeInteraction send failed: %w", err)
	}
	return nil
}

// NPCDialogPositionSync mirrors the native stop/sync 0x03 observed between
// NPCInit (0x2F) and NPCAction (0x38): RunToLocation(current,current). Without
// it the dialog opens, but the subsequent menu-option packet can be ignored.
func (ps *PacketSender) NPCDialogPositionSync(playerPos data.Position) error {
	if err := ps.sendAMBDualPacketAPC(amb.NewRunToLocation(
		uint16(playerPos.X),
		uint16(playerPos.Y),
		uint16(playerPos.X),
		uint16(playerPos.Y),
	).GetPayload()); err != nil {
		return fmt.Errorf("NPCDialogPositionSync send failed: %w", err)
	}
	return nil
}

// --- AMB NPC / interaction flow ------------------------------------------------
//
// Payloads follow AMB/d2go. NPCInit is sent as 0x2F; NPCAction prefers D2R's
// native dialog-option handlers because live vendor testing showed raw 0x38
// reaches the server path but does not always update local NPCShop state.

// NPCInit opens an NPC's dialog (0x2F, 5 B). Send BEFORE NPCAction.
func (ps *PacketSender) NPCInit(npcID data.UnitID) error {
	if err := ps.SendAMBPacket(amb.NewNPCInit(npcID).GetPayload()); err != nil {
		return fmt.Errorf("NPCInit send failed: %w", err)
	}
	return nil
}

// NPCAction picks a menu entry (0x38, 9 B). actionID is the MENU POSITION
// (0x01 = first option, 0x02 = second, etc.). Convenience helpers exist:
// NPCTrade / NPCGamble / NPCRepairAction / NPCIdentifyAction — see
// amb.NewNPCTrade / amb.GetNPCRepairAction etc.
func (ps *PacketSender) NPCAction(actionID uint32, npcID data.UnitID) error {
	if err := ps.sendNPCDialogOption(actionID, npcID); err != nil {
		return fmt.Errorf("NPCAction send failed: %w", err)
	}
	return nil
}

func (ps *PacketSender) NPCServiceAction(npcClassID uint32, service amb.NPCService, npcID data.UnitID) error {
	pkt, ok := amb.NewNPCServiceAction(npcClassID, service, npcID)
	if !ok {
		return fmt.Errorf("NPCServiceAction: npc class %d has no AMB service action %d", npcClassID, service)
	}
	if err := ps.sendNPCDialogOption(uint32(pkt.ActionID), npcID); err != nil {
		return fmt.Errorf("NPCServiceAction send failed: %w", err)
	}
	return nil
}

// NPCTrade selects the Trade service from an already-open NPC dialog. AMB
// represents this as NPCAction with ActionID=0x01 inside opcode 0x38.
func (ps *PacketSender) NPCTrade(npcID data.UnitID) error {
	if err := ps.sendNPCDialogOption(1, npcID); err != nil {
		return fmt.Errorf("NPCTrade send failed: %w", err)
	}
	return nil
}

func (ps *PacketSender) NPCTradeFor(npcClassID uint32, npcID data.UnitID) error {
	if err := ps.NPCServiceAction(npcClassID, amb.NPCServiceTrade, npcID); err != nil {
		return fmt.Errorf("NPCTradeFor send failed: %w", err)
	}
	return nil
}

// NPCIdentifyAction selects Cain's Identify service from his dialog. AMB uses
// the same ActionID=0x01 field inside opcode 0x38 for Cain's first option.
func (ps *PacketSender) NPCIdentifyAction(npcID data.UnitID) error {
	if err := ps.sendNPCDialogOption(1, npcID); err != nil {
		return fmt.Errorf("NPCIdentifyAction send failed: %w", err)
	}
	return nil
}

func (ps *PacketSender) NPCIdentifyActionFor(npcClassID uint32, npcID data.UnitID) error {
	if err := ps.NPCServiceAction(npcClassID, amb.NPCServiceIdentify, npcID); err != nil {
		return fmt.Errorf("NPCIdentifyActionFor send failed: %w", err)
	}
	return nil
}

// NPCCancel closes the NPC dialog (0x30, 5 B).
func (ps *PacketSender) NPCCancel(npcID data.UnitID) error {
	if err := ps.sendNPCCancel(npcID); err != nil {
		return fmt.Errorf("NPCCancel send failed: %w", err)
	}
	return nil
}

// NPCBuyAMB purchases an item from an NPC (0x32, 24 B — AMB layout,
// differs from our legacy NPCBuy which is 22 B). toPosX/Y is destination
// inventory slot, sourceTab / targetLocation / transactionMode per AMB docs.
func (ps *PacketSender) NPCBuyAMB(item data.Item, npc data.Monster, itemPrice uint32, toPosX, toPosY uint16, sourceTab, targetLocation, transactionMode byte) error {
	pkt := amb.NewNPCBuy(item, npc, itemPrice, toPosX, toPosY, sourceTab, targetLocation, transactionMode).GetPayload()
	if err := ps.SendAMBPacket(pkt); err != nil {
		return fmt.Errorf("NPCBuy send failed: %w", err)
	}
	return nil
}

// NPCSellAMB sells an item to an NPC (0x33, 24 B — AMB layout).
func (ps *PacketSender) NPCSellAMB(item data.Item, npc data.Monster, itemPrice uint32, toPosX, toPosY uint16, targetTab, targetLocation, transactionMode byte) error {
	pkt := amb.NewNPCSell(item, npc, itemPrice, toPosX, toPosY, targetTab, targetLocation, transactionMode).GetPayload()
	if err := ps.SendAMBPacket(pkt); err != nil {
		return fmt.Errorf("NPCSell send failed: %w", err)
	}
	return nil
}

func (ps *PacketSender) NPCSellAMBAt(item data.Item, npc data.Monster, itemPrice uint32, toPosX, toPosY, itemPosX, itemPosY uint16, targetTab, targetLocation, transactionMode byte) error {
	item.Position.X = int(itemPosX)
	item.Position.Y = int(itemPosY)
	pkt := amb.NewNPCSell(item, npc, itemPrice, toPosX, toPosY, targetTab, targetLocation, transactionMode).GetPayload()
	if err := ps.SendAMBPacket(pkt); err != nil {
		return fmt.Errorf("NPCSellAt send failed: %w", err)
	}
	return nil
}

func (ps *PacketSender) NPCSellDispatch(sellPrice uint32, itemGID, npcGID data.UnitID, slot, seq uint16, term byte) error {
	toPosX, toPosY := uint16(0), uint16(0)
	if term != packet.NPCSellTermConsumable {
		toPosX, toPosY = 9, seq+6
	}
	pkt := packet.NewNPCSellItem(sellPrice, itemGID, npcGID, toPosX, toPosY, slot, seq, term)
	if err := ps.SendAMBPacket(pkt); err != nil {
		return fmt.Errorf("NPCSellDispatch send failed: %w", err)
	}
	return nil
}

// NPCSellLive sells using the live-confirmed 24-byte 0x33 vendor packet.
// Live Akara tests on 2026-04-26 proved this succeeds through D2GS main-thread
// APC. UI NetMan was a stable no-op, ui-dual returned to character select, and
// dualwrap-GT restarted the client.
func (ps *PacketSender) NPCSellLive(item data.Item, npc data.Monster, itemPrice uint32, term byte) error {
	toPosX, toPosY := uint16(0), uint16(0)
	if term != packet.NPCSellTermConsumable {
		toPosX, toPosY = 9, uint16(item.Position.Y)+6
	}
	pkt := packet.NewNPCSellItem(
		itemPrice,
		item.UnitID,
		npc.UnitID,
		toPosX,
		toPosY,
		uint16(item.Position.X),
		uint16(item.Position.Y),
		term,
	)
	if err := ps.SendAMBPacket(pkt); err != nil {
		return fmt.Errorf("NPCSell live send failed: %w", err)
	}
	return nil
}

// NPCRepairAll requests blanket repair from a smith NPC (0x35, 16 B,
// mode=4, itemUnitId=0xFFFFFFFF per AMB docs).
func (ps *PacketSender) NPCRepairAll(npcUnitID data.UnitID, repairCosts uint32) error {
	if err := ps.SendAMBPacket(amb.NewNPCRepairAll(npcUnitID, repairCosts).GetPayload()); err != nil {
		return fmt.Errorf("NPCRepairAll send failed: %w", err)
	}
	return nil
}

// NPCIdentifyAll asks Cain to identify every unidentified item using the
// live-captured 21-byte 0x34 layout. Manual Cain success on 2026-04-27
// emitted this packet through dual_send_wrap/send_fn, matching dual-APC; the
// UI-dual route was a no-op or resolver failure depending on presenter DLL.
func (ps *PacketSender) NPCIdentifyAll(npcUnitID, cubeUnitID data.UnitID, cubePosX, cubePosY uint16) error {
	payload := amb.NewNPCIdentifyItems(npcUnitID, cubeUnitID, cubePosX, cubePosY).GetPayload()
	if err := ps.sendAMBDualPacketAPC(payload); err != nil {
		return fmt.Errorf("NPCIdentifyAll send failed: %w", err)
	}
	return nil
}

// CainIdentifyItem sends the legacy/sniffed per-item Cain identify packet
// (0x5C, 17 B). Keep this separate from AMB QuickItemDrop, which also uses
// opcode 0x5C with a different layout.
func (ps *PacketSender) CainIdentifyItem(itemGID data.UnitID, page uint8, idx uint16) error {
	pkt := packet.NewCainIdentifyItem(itemGID, page, idx)
	if err := ps.sendAMBUIDualPacket(pkt); err != nil {
		return fmt.Errorf("CainIdentifyItem send failed: %w", err)
	}
	return nil
}

// AkaraRespec submits a token-of-absolution respec request (0x39, 5 B).
func (ps *PacketSender) AkaraRespec(akara data.Monster) error {
	if err := ps.SendPacket(amb.NewAkaraRespec(akara).GetPayload()); err != nil {
		return fmt.Errorf("AkaraRespec send failed: %w", err)
	}
	return nil
}

// ResurrectMerc pays to revive a dead hireling (0x52, 13 B).
func (ps *PacketSender) ResurrectMerc(dealerID uint32, nameID uint16, cost uint32) error {
	if err := ps.SendPacket(amb.NewResurrectMerc(dealerID, nameID, cost).GetPayload()); err != nil {
		return fmt.Errorf("ResurrectMerc send failed: %w", err)
	}
	return nil
}

// UnitInteract triggers AMB's simple 0x40 interaction (5 B) — NPCs,
// waypoints, shrines, chests, entrances, and other non-stateful units.
func (ps *PacketSender) UnitInteract(unitID data.UnitID) error {
	if err := ps.SendAMBPacket(amb.NewUnitInteract(unitID).GetPayload()); err != nil {
		return fmt.Errorf("UnitInteract send failed: %w", err)
	}
	return nil
}

// NPCPreInteract mirrors AMB/native NPC flow's 0x4D pre-interact marker.
// The packet is intentionally the 5-byte form; the old longer capture-derived
// variant carried mirror-buffer residue and was observed crashing D2R.
func (ps *PacketSender) NPCPreInteract(npcGID, playerGID data.UnitID, playerX, playerY, npcX, npcY uint16) error {
	pkt := packet.NewNPCEntityAction(npcGID, playerGID, playerX, playerY, npcX, npcY)
	if err := ps.sendAMBPacketAPC(pkt); err != nil {
		return fmt.Errorf("NPCPreInteract send failed: %w", err)
	}
	return nil
}

// PortalInteract opens a red/blue portal (0x41, 13 B).
func (ps *PacketSender) PortalInteract(obj data.Object) error {
	if err := ps.SendAMBPacket(amb.NewPortalInteraction(obj).GetPayload()); err != nil {
		return fmt.Errorf("PortalInteract send failed: %w", err)
	}
	return nil
}

// EntranceInteractEx enters an area boundary (0x41, 13 B) — e.g. stair
// transitions, dungeon entrances.
func (ps *PacketSender) EntranceInteractEx(entrance data.Entrance) error {
	if err := ps.SendAMBPacket(amb.NewEntranceInteractionEx(entrance).GetPayload()); err != nil {
		return fmt.Errorf("EntranceInteractEx send failed: %w", err)
	}
	return nil
}

// --- AMB item management -------------------------------------------------------
//
// Inventory / belt / stash / cube / socket operations. All via the
// D2GS_SendPacket APC path so D2R's client updates widget state locally
// (same principle that unblocked weapon swap + self-cast).
//
// Opcode conflicts vs our existing builders:
//   0x54 — AMB = QuickItemMove (21 B), our CubeTransmute (20 B)
//   0x5C — AMB = QuickItemDrop (17 B), our CainIdentifyItem (variable)
// AMB wins per user ("AMB packets are definitely correct"). Our legacy
// builders stay live for backward compatibility; call the *AMB variant for
// the authoritative format.

// DropItem drops an item from cursor to the ground (0x17, 5 B).
func (ps *PacketSender) DropItem(itemUnitID data.UnitID) error {
	if err := ps.SendAMBPacket(amb.NewDropItem(itemUnitID).GetPayload()); err != nil {
		return fmt.Errorf("DropItem send failed: %w", err)
	}
	return nil
}

// DropGold drops gold from inventory to the ground (0x47, 9 B).
func (ps *PacketSender) DropGold(inventoryGoldCount, dropGoldCount uint32) error {
	if err := ps.SendAMBPacket(amb.NewDropGold(inventoryGoldCount, dropGoldCount).GetPayload()); err != nil {
		return fmt.Errorf("DropGold send failed: %w", err)
	}
	return nil
}

// PutItemToBody equips a held item to a body slot (0x1A, 9 B).
func (ps *PacketSender) PutItemToBody(itemUnitID uint32, bodyLocation uint32) error {
	if err := ps.SendAMBPacket(amb.NewPutItemToBody(itemUnitID, bodyLocation).GetPayload()); err != nil {
		return fmt.Errorf("PutItemToBody send failed: %w", err)
	}
	return nil
}

// PullItemFromBody unequips an item from a body slot (0x1C, 9 B).
func (ps *PacketSender) PullItemFromBody(item data.Item, bodyLocation uint32) error {
	if err := ps.SendAMBPacket(amb.NewPullItemFromBody(item, bodyLocation).GetPayload()); err != nil {
		return fmt.Errorf("PullItemFromBody send failed: %w", err)
	}
	return nil
}

// SwitchBodyItem atomically swaps an equipped item with another (0x21, 21 B).
func (ps *PacketSender) SwitchBodyItem(targetItem, switchItem data.Item, targetBodyLocation uint32) error {
	if err := ps.SendAMBPacket(amb.NewSwitchBodyItem(targetItem, switchItem, targetBodyLocation).GetPayload()); err != nil {
		return fmt.Errorf("SwitchBodyItem send failed: %w", err)
	}
	return nil
}

// PutItemToInventory drops a held item into inventory grid (0x18, 17 B).
func (ps *PacketSender) PutItemToInventory(itemID uint32, posX, posY, inventoryID uint32) error {
	if err := ps.SendAMBPacket(amb.NewPutItemToInventory(itemID, posX, posY, inventoryID).GetPayload()); err != nil {
		return fmt.Errorf("PutItemToInventory send failed: %w", err)
	}
	return nil
}

// PutItemToBelt places a held item into a specific belt slot (0x23 variant, 21 B).
func (ps *PacketSender) PutItemToBelt(itemID, fromInvPage uint32, fromX, fromY, toX, toY uint16) error {
	if err := ps.SendAMBPacket(amb.NewPutItemToBelt(itemID, fromInvPage, fromX, fromY, toX, toY).GetPayload()); err != nil {
		return fmt.Errorf("PutItemToBelt send failed: %w", err)
	}
	return nil
}

// ItemToBelt quick-moves an inventory item to a belt slot (0x23, 9 B).
func (ps *PacketSender) ItemToBeltSlot(item data.Item, beltPosX uint32) error {
	if err := ps.SendAMBPacket(amb.NewItemToBelt(item, beltPosX).GetPayload()); err != nil {
		return fmt.Errorf("ItemToBelt send failed: %w", err)
	}
	return nil
}

// RemoveBeltItems fires the 0x24 compound "remove multiple belt items"
// packet (24 B fixed). Pass up to 4 entries; unused slots are zero.
func (ps *PacketSender) RemoveBeltItems(entries []amb.RemoveBeltItemEntry) error {
	if err := ps.SendAMBPacket(amb.NewRemoveBeltItemFromSlice(entries).GetPayload()); err != nil {
		return fmt.Errorf("RemoveBeltItems send failed: %w", err)
	}
	return nil
}

// MoveItemToCube places an item into the Horadric Cube (0x2A, 21 B).
func (ps *PacketSender) MoveItemToCube(itemUnitID, cubeUnitID data.UnitID, toPosX, toPosY uint16) error {
	if err := ps.SendAMBPacket(amb.NewMoveItemToCube(itemUnitID, cubeUnitID, toPosX, toPosY).GetPayload()); err != nil {
		return fmt.Errorf("MoveItemToCube send failed: %w", err)
	}
	return nil
}

// InsertItemToSocket inserts a socketable (rune/jewel/gem) into a target
// item's socket (0x28, 21 B).
func (ps *PacketSender) InsertItemToSocket(socketable, targetItem data.Item, inventoryID uint32) error {
	if err := ps.SendAMBPacket(amb.NewInsertItemToSocket(socketable, targetItem, inventoryID).GetPayload()); err != nil {
		return fmt.Errorf("InsertItemToSocket send failed: %w", err)
	}
	return nil
}

// DepositGoldToStash moves gold from inventory into the shared stash (0x27, 17 B).
func (ps *PacketSender) DepositGoldToStash(unitID data.UnitID, stashGold, invGold, amount uint32) error {
	if err := ps.SendAMBPacket(amb.NewDepositGoldToStash(unitID, stashGold, invGold, amount).GetPayload()); err != nil {
		return fmt.Errorf("DepositGoldToStash send failed: %w", err)
	}
	return nil
}

// WithdrawGoldFromStash pulls gold from the shared stash into inventory (0x27, 17 B).
func (ps *PacketSender) WithdrawGoldFromStash(unitID data.UnitID, stashGold, invGold, amount uint32) error {
	if err := ps.SendAMBPacket(amb.NewWithdrawGoldFromStash(unitID, stashGold, invGold, amount).GetPayload()); err != nil {
		return fmt.Errorf("WithdrawGoldFromStash send failed: %w", err)
	}
	return nil
}

// PutItemToSharedStash moves an inventory item into the shared stash (0x55, 29 B).
func (ps *PacketSender) PutItemToSharedStash(item data.Item, stashTabID uint32, toPosX, toPosY uint16) error {
	if err := ps.SendAMBPacket(amb.NewPutItemToSharedStash(item, stashTabID, toPosX, toPosY).GetPayload()); err != nil {
		return fmt.Errorf("PutItemToSharedStash send failed: %w", err)
	}
	return nil
}

// TakeItemFromSharedStash is the mirror op (same opcode/layout, different semantics).
func (ps *PacketSender) TakeItemFromSharedStash(item data.Item, stashTabID uint32, toPosX, toPosY uint16) error {
	if err := ps.SendAMBPacket(amb.NewTakeItemFromSharedStash(item, stashTabID, toPosX, toPosY).GetPayload()); err != nil {
		return fmt.Errorf("TakeItemFromSharedStash send failed: %w", err)
	}
	return nil
}

// PullItemFromSharedStash picks an item out of the shared stash (0x46, 13 B).
func (ps *PacketSender) PullItemFromSharedStash(item data.Item, stashTabOwnerID uint32) error {
	if err := ps.SendAMBPacket(amb.NewPullItemFromSharedStash(item, stashTabOwnerID).GetPayload()); err != nil {
		return fmt.Errorf("PullItemFromSharedStash send failed: %w", err)
	}
	return nil
}

// QuickItemMoveAMB is the AMB authoritative 0x54 (21 B) — shift-click to
// bucket. Conflicts with our legacy CubeTransmute builder (also 0x54, 20 B);
// both remain available. ContainerType values live in packet/amb.
func (ps *PacketSender) QuickItemMoveAMB(item data.Item, fromPage, toPage amb.ContainerType) error {
	if err := ps.SendAMBPacket(amb.NewQuickItemMove(item, fromPage, toPage).GetPayload()); err != nil {
		return fmt.Errorf("QuickItemMove send failed: %w", err)
	}
	return nil
}

// QuickItemMoveAMBWithPosition is the positional variant.
func (ps *PacketSender) QuickItemMoveAMBWithPosition(item data.Item, fromPage, toPage amb.ContainerType, toPosX, toPosY uint16) error {
	if err := ps.SendAMBPacket(amb.NewQuickItemMoveWithPosition(item, fromPage, toPage, toPosX, toPosY).GetPayload()); err != nil {
		return fmt.Errorf("QuickItemMoveWithPosition send failed: %w", err)
	}
	return nil
}

// QuickItemDropAMB is the AMB authoritative 0x5C (17 B) — drop item to
// ground via shift+click UI. Conflicts with our legacy CainIdentifyItem
// (also 0x5C); both remain available.
func (ps *PacketSender) QuickItemDropAMB(item data.Item) error {
	if err := ps.SendAMBPacket(amb.NewQuickItemDrop(item).GetPayload()); err != nil {
		return fmt.Errorf("QuickItemDrop send failed: %w", err)
	}
	return nil
}

// PickItemFromContainer pulls an item out of a chest / container (0x19, 17 B).
func (ps *PacketSender) PickItemFromContainer(itemUnitID data.UnitID, x, y int, container amb.ContainerType) error {
	if err := ps.SendAMBPacket(amb.NewPickItemFromContainer(itemUnitID, x, y, container).GetPayload()); err != nil {
		return fmt.Errorf("PickItemFromContainer send failed: %w", err)
	}
	return nil
}

// --- AMB skills / attributes / stand-still casts / use / chat ----------------

// LeftSkillAtUnitStandStill casts the active left skill on a unit without
// moving (0x07, 9 B). unitType: 0=Player, 1=Monster, 2=Object, etc.
func (ps *PacketSender) LeftSkillAtUnitStandStill(unitType int, unitID data.UnitID) error {
	if err := ps.SendPacket(amb.NewLeftSkillAtUnitStandStill(unitType, unitID).GetPayload()); err != nil {
		return fmt.Errorf("LeftSkillAtUnitStandStill send failed: %w", err)
	}
	return nil
}

// RightSkillAtUnitStandStill casts the active right skill on a unit without
// moving (0x0E, 9 B). Ideal for self-targeted buffs (BO/BC/FrozenArmor on
// the player's own UnitID = no position shift, no missed no-op on own tile).
func (ps *PacketSender) RightSkillAtUnitStandStill(unitType int, unitID data.UnitID) error {
	if err := ps.SendPacket(amb.NewRightSkillAtUnitStandStill(unitType, unitID).GetPayload()); err != nil {
		return fmt.Errorf("RightSkillAtUnitStandStill send failed: %w", err)
	}
	return nil
}

// ActivateItem activates an equipped charged item (e.g. CTA Battle Orders
// charges, Hoto skill procs) — 0x3E, 5 B.
func (ps *PacketSender) ActivateItem(item data.Item) error {
	if err := ps.SendPacket(amb.NewActivateItem(item).GetPayload()); err != nil {
		return fmt.Errorf("ActivateItem send failed: %w", err)
	}
	return nil
}

// UseItem consumes a single inventory item (potion / scroll / tome / key).
// Variable-size (minimum 8 B). invPage selects which container — for belt
// potions use amb.ContainerBelt; for inventory items use amb.ContainerInventory.
func (ps *PacketSender) UseItem(item data.Item, invPage amb.ContainerType) error {
	if err := ps.SendPacket(amb.NewUseItem(item, invPage).GetPayload()); err != nil {
		return fmt.Errorf("UseItem send failed: %w", err)
	}
	return nil
}

// UseItemFromBelt is the shortcut for belt potions.
func (ps *PacketSender) UseItemFromBelt(item data.Item) error {
	if err := ps.SendPacket(amb.NewUseItemFromBelt(item).GetPayload()); err != nil {
		return fmt.Errorf("UseItemFromBelt send failed: %w", err)
	}
	return nil
}

// UseCubeTransmute fires the live-captured cube transmute packet (0x20,
// variable size, ActionType=0x88) through the game/D2GS path. Live test
// 2026-04-26: UI/UI-dual no-op; game path transmutes even with the cube UI
// closed as long as the cube is in the visible personal stash context.
// Packet 0x18 is a cursor/container item move and must not be used for
// transmute.
func (ps *PacketSender) UseCubeTransmute(cubeItem data.Item, itemsInCube []data.Item) error {
	if err := ps.SendPacket(amb.NewUseCubeTransmute(cubeItem, itemsInCube).GetPayload()); err != nil {
		return fmt.Errorf("UseCubeTransmute send failed: %w", err)
	}
	return nil
}

// SendChat emits an in-game chat message (0x15, variable size).
func (ps *PacketSender) SendChat(msgType amb.GameMessageType, message string) error {
	if err := ps.SendPacket(amb.NewSendMessage(msgType, message).GetPayload()); err != nil {
		return fmt.Errorf("SendChat send failed: %w", err)
	}
	return nil
}

// SendWhisper sends a private message to another player (0x15, variable size).
func (ps *PacketSender) SendWhisper(recipient, message string) error {
	if err := ps.SendPacket(amb.NewSendWhisper(recipient, message).GetPayload()); err != nil {
		return fmt.Errorf("SendWhisper send failed: %w", err)
	}
	return nil
}

// RequestPlayerUpdate asks the server to refresh its view of a player unit
// (0x43, 9 B). Useful after equipment changes to make sure the server's
// stat sheet reflects reality before downstream logic reads it.
//
// Note: 0x43 conflicts with our legacy OpTpConfirmTravel builder. AMB wins.
func (ps *PacketSender) RequestPlayerUpdate(playerUnitID data.UnitID) error {
	if err := ps.SendPacket(amb.NewRequestPlayerUpdate(playerUnitID).GetPayload()); err != nil {
		return fmt.Errorf("RequestPlayerUpdate send failed: %w", err)
	}
	return nil
}

// IncrementSkillAMB allocates a single skill point via AMB's builder.
// Mirrors our existing LearnSkill — both emit 0x3B 5 B. Kept for API parity.
func (ps *PacketSender) IncrementSkillAMB(skillID skill.ID) error {
	if err := ps.SendPacket(amb.NewIncrementSkill(skillID).GetPayload()); err != nil {
		return fmt.Errorf("IncrementSkill send failed: %w", err)
	}
	return nil
}

// IncrementAttributeAMB allocates a single stat point via AMB's builder.
// Mirrors our existing AllocateStatPoint — both emit 0x3A 5 B.
func (ps *PacketSender) IncrementAttributeAMB(statID stat.ID) error {
	if err := ps.SendPacket(amb.NewIncrementAttribute(statID).GetPayload()); err != nil {
		return fmt.Errorf("IncrementAttribute send failed: %w", err)
	}
	return nil
}

// --- Remaining AMB gap-fills ---------------------------------------------------
// The builders below close the last few AMB constructors that didn't yet have a
// direct PacketSender wrapper.

// NPCGamble opens an NPC's gamble window (convenience: 0x38 action=2).
func (ps *PacketSender) NPCGamble(npcID data.UnitID) error {
	if err := ps.SendAMBPacket(amb.NewNPCGamble(npcID).GetPayload()); err != nil {
		return fmt.Errorf("NPCGamble send failed: %w", err)
	}
	return nil
}

func (ps *PacketSender) NPCGambleFor(npcClassID uint32, npcID data.UnitID) error {
	if err := ps.NPCServiceAction(npcClassID, amb.NPCServiceGamble, npcID); err != nil {
		return fmt.Errorf("NPCGambleFor send failed: %w", err)
	}
	return nil
}

func (ps *PacketSender) NPCImbueFor(npcClassID uint32, npcID data.UnitID) error {
	if err := ps.NPCServiceAction(npcClassID, amb.NPCServiceImbue, npcID); err != nil {
		return fmt.Errorf("NPCImbueFor send failed: %w", err)
	}
	return nil
}

func (ps *PacketSender) NPCSocketFor(npcClassID uint32, npcID data.UnitID) error {
	if err := ps.NPCServiceAction(npcClassID, amb.NPCServiceSocket, npcID); err != nil {
		return fmt.Errorf("NPCSocketFor send failed: %w", err)
	}
	return nil
}

func (ps *PacketSender) NPCPersonalizeFor(npcClassID uint32, npcID data.UnitID) error {
	if err := ps.NPCServiceAction(npcClassID, amb.NPCServicePersonalize, npcID); err != nil {
		return fmt.Errorf("NPCPersonalizeFor send failed: %w", err)
	}
	return nil
}

// PickUpItemToCursor picks an item up onto the cursor rather than auto-placing
// (0x16, cursor=0 variant of NewPickUpItem).
func (ps *PacketSender) PickUpItemToCursor(it data.Item) error {
	if err := ps.SendPacket(amb.NewPickUpItemToCursor(it).GetPayload()); err != nil {
		return fmt.Errorf("PickUpItemToCursor send failed: %w", err)
	}
	return nil
}

// UseItemOnUnit uses an item (scroll/potion) on another unit (0x36, 36 B).
// Caller must provide belt slot state (otherSlots) so the packet reflects the
// current 4-slot belt snapshot — see AMB's amb.BeltSlotState.
func (ps *PacketSender) UseItemOnUnit(itemID, targetID uint32, targetUnitType, beltX, beltY, usageTarget byte, otherSlots [3]amb.BeltSlotState) error {
	if err := ps.SendPacket(amb.NewUseItemOnUnit(itemID, targetID, targetUnitType, beltX, beltY, usageTarget, otherSlots).GetPayload()); err != nil {
		return fmt.Errorf("UseItemOnUnit send failed: %w", err)
	}
	return nil
}

// SelectSkillAMB selects a skill onto the left- or right-hand slot
// (0x3C AMB 9 B). hand = amb.LeftHand (0x80) or amb.RightHand (0x00).
// Mirrors our existing SelectRightSkill/SelectLeftSkill for parity.
func (ps *PacketSender) SelectSkillAMB(skillID skill.ID, hand amb.SelectSkillHand) error {
	if err := ps.SendAMBPacket(amb.NewSelectSkill(skillID, hand).GetPayload()); err != nil {
		return fmt.Errorf("SelectSkill send failed: %w", err)
	}
	return nil
}

// SelectSkillWithItem selects a skill that draws from a charged item
// (wand, CTA charges, etc.) — 0x3C variant with the charged item's UnitID.
func (ps *PacketSender) SelectSkillWithItem(skillID skill.ID, hand amb.SelectSkillHand, chargedItemID uint32) error {
	if err := ps.SendAMBPacket(amb.NewSelectSkillWithItem(skillID, hand, chargedItemID).GetPayload()); err != nil {
		return fmt.Errorf("SelectSkillWithItem send failed: %w", err)
	}
	return nil
}

// RequestObjectUpdate asks the server to resend state for an object (0x43 variant).
func (ps *PacketSender) RequestObjectUpdate(objectID data.UnitID) error {
	if err := ps.SendAMBPacket(amb.NewRequestObjectUpdate(objectID).GetPayload()); err != nil {
		return fmt.Errorf("RequestObjectUpdate send failed: %w", err)
	}
	return nil
}

// RequestMonsterUpdate asks the server to resend state for a monster (0x43 variant).
func (ps *PacketSender) RequestMonsterUpdate(monsterID data.UnitID) error {
	if err := ps.SendAMBPacket(amb.NewRequestMonsterUpdate(monsterID).GetPayload()); err != nil {
		return fmt.Errorf("RequestMonsterUpdate send failed: %w", err)
	}
	return nil
}

// RequestUnitUpdate is the generic form accepting any unit type.
// unitType per AMB: 0=Player, 1=Monster, 2=Object, 3=Missile, 4=Item, 5=Tile.
func (ps *PacketSender) RequestUnitUpdate(unitType uint32, unitID data.UnitID) error {
	if err := ps.SendAMBPacket(amb.NewRequestUnitUpdate(unitType, unitID).GetPayload()); err != nil {
		return fmt.Errorf("RequestUnitUpdate send failed: %w", err)
	}
	return nil
}

// ObjectInteract0x13 triggers "press stuff" style interactions with world
// objects (chests, shrines, pylons) via the 0x13 + executee form (9 B).
// Our existing UnitInteract uses 0x40; this variant adds the executee field
// which some objects require.
func (ps *PacketSender) ObjectInteract0x13(obj data.Object, playerUnitID data.UnitID) error {
	if err := ps.SendAMBPacket(amb.NewObjectInteraction0x13(obj, playerUnitID).GetPayload()); err != nil {
		return fmt.Errorf("ObjectInteract0x13 send failed: %w", err)
	}
	return nil
}

// NPCInteract0x13 — 0x13 variant targeting a monster (NPC). Some NPCs
// respond to 0x13 but not 0x40.
func (ps *PacketSender) NPCInteract0x13(monster data.Monster, playerUnitID data.UnitID) error {
	if err := ps.SendAMBPacket(amb.NewNPCInteraction0x13(monster, playerUnitID).GetPayload()); err != nil {
		return fmt.Errorf("NPCInteract0x13 send failed: %w", err)
	}
	return nil
}

// SimpleObjectInteract is the 0x40 (5 B) interaction targeting a generic
// world object. Short-hand over UnitInteract that accepts a data.Object
// instead of a raw UnitID.
func (ps *PacketSender) SimpleObjectInteract(obj data.Object) error {
	if err := ps.SendAMBPacket(amb.NewSimpleObjectInteraction(obj).GetPayload()); err != nil {
		return fmt.Errorf("SimpleObjectInteract send failed: %w", err)
	}
	return nil
}

// RemoveBeltItems4 is the positional (non-slice) variant of RemoveBeltItem
// (0x24, 24 B fixed). Up to 4 slot entries with their cur/prev positions.
func (ps *PacketSender) RemoveBeltItems4(
	id1 data.UnitID, pos1 byte,
	id2 data.UnitID, posCur2, posPrev2 byte,
	id3 data.UnitID, posCur3, posPrev3 byte,
	id4 data.UnitID, posCur4, posPrev4 byte,
) error {
	pkt := amb.NewRemoveBeltItem(id1, pos1, id2, posCur2, posPrev2, id3, posCur3, posPrev3, id4, posCur4, posPrev4).GetPayload()
	if err := ps.SendPacket(pkt); err != nil {
		return fmt.Errorf("RemoveBeltItems4 send failed: %w", err)
	}
	return nil
}

// CastLeftSkillAtLocationAMB fires the left-hand skill at world coords via
// AMB's authoritative 0x05 builder (5 B). Parallel to CastSkillAtLocation
// but for the left-click slot.
func (ps *PacketSender) CastLeftSkillAtLocationAMB(x, y uint16) error {
	if err := ps.SendAMBPacket(amb.NewCastLeftSkill(x, y).GetPayload()); err != nil {
		return fmt.Errorf("CastLeftSkill send failed: %w", err)
	}
	return nil
}

// CastLeftSkillOnUnit casts the left-hand skill on a target unit (0x06, 9 B).
// unitType: 0=Player, 1=Monster, 2=Object, 3=Missile, 4=Item, 5=Tile.
func (ps *PacketSender) CastLeftSkillOnUnit(unitType int, unitID data.UnitID) error {
	if err := ps.SendAMBPacket(amb.NewCastLeftSkillOnUnit(unitType, unitID).GetPayload()); err != nil {
		return fmt.Errorf("CastLeftSkillOnUnit send failed: %w", err)
	}
	return nil
}

// MoveStashGold is the generic gold-transfer form (0x27, 17 B). transferAmount
// is signed: positive = deposit, negative = withdraw. For clarity prefer
// DepositGoldToStash / WithdrawGoldFromStash which wrap this with friendlier
// semantics.
func (ps *PacketSender) MoveStashGold(unitID data.UnitID, stashGold, invGold uint32, transferAmount int32) error {
	if err := ps.SendPacket(amb.NewMoveStashGold(unitID, stashGold, invGold, transferAmount).GetPayload()); err != nil {
		return fmt.Errorf("MoveStashGold send failed: %w", err)
	}
	return nil
}

// --- AMB parity wrappers -------------------------------------------------------
// These methods use AMB builders explicitly so AMB's authoritative byte layout
// is the send-path for every caller, not just the ones that happened to go
// through our internal packet.New* equivalents.

// CastRightSkillAMB — 0x0C 5 B via AMB (equivalent to CastSkillAtLocation
// after the Phase 2 byte-align; kept for explicit AMB parity).
func (ps *PacketSender) CastRightSkillAMB(x, y uint16) error {
	if err := ps.SendAMBPacket(amb.NewCastRightSkill(x, y).GetPayload()); err != nil {
		return fmt.Errorf("CastRightSkill send failed: %w", err)
	}
	return nil
}

// CastRightSkillOnUnitAMB — 0x0D 9 B via AMB.
func (ps *PacketSender) CastRightSkillOnUnitAMB(unitType int, unitID data.UnitID) error {
	if err := ps.SendAMBPacket(amb.NewCastRightSkillOnUnit(unitType, unitID).GetPayload()); err != nil {
		return fmt.Errorf("CastRightSkillOnUnit send failed: %w", err)
	}
	return nil
}

// EntranceInteractAMB — 0x40 5 B via AMB. Parity with InteractWithEntrance.
func (ps *PacketSender) EntranceInteractAMB(entrance data.Entrance) error {
	if err := ps.SendAMBPacket(amb.NewEntranceInteraction(entrance).GetPayload()); err != nil {
		return fmt.Errorf("EntranceInteract send failed: %w", err)
	}
	return nil
}

// TpInteractAMB — 0x41 13 B via AMB. Parity with InteractWithTp.
func (ps *PacketSender) TpInteractAMB(object data.Object) error {
	if err := ps.SendAMBPacket(amb.NewTpInteraction(object).GetPayload()); err != nil {
		return fmt.Errorf("TpInteract send failed: %w", err)
	}
	return nil
}

// WaypointInteractAMB — 0x4B via AMB. Parity with TravelWaypoint.
// destinationArea is the target area.ID.
func (ps *PacketSender) WaypointInteractAMB(destinationArea area.ID) error {
	if err := ps.SendPacket(amb.NewWaypointInteraction(destinationArea).GetPayload()); err != nil {
		return fmt.Errorf("WaypointInteract send failed: %w", err)
	}
	return nil
}

// PickUpItemAMB — 0x16 17 B via AMB. Parity with PickUpItem (same bytes per
// our 2026-04-21 comparison, but kept here for explicit AMB sourcing).
func (ps *PacketSender) PickUpItemAMB(it data.Item) error {
	if err := ps.SendPacket(amb.NewPickUpItem(it).GetPayload()); err != nil {
		return fmt.Errorf("PickUpItem send failed: %w", err)
	}
	return nil
}

// SwapWeaponSlotsAMB — 0x50 30 B via AMB's SwapWeaponSlots directly. Parity
// with our SwapWeapon / SwapWeaponFromData which already produces AMB bytes.
func (ps *PacketSender) SwapWeaponSlotsAMB(leftHand, rightHand, altLeftHand, altRightHand data.UnitID, leftSkillID, rightSkillID uint16, swapSlotID byte) error {
	pkt := amb.NewSwapWeaponSlots(leftHand, rightHand, altLeftHand, altRightHand, leftSkillID, rightSkillID, swapSlotID).GetPayload()
	if err := ps.SendAMBPacket(pkt); err != nil {
		return fmt.Errorf("SwapWeaponSlots send failed: %w", err)
	}
	return nil
}

// NPCInteractEx — 0x41 13 B via AMB (NewNPCInteraction variant). Unlike the
// 0x13 form this is the "extended" unit-interact flow used for most NPC
// dialog openings.
func (ps *PacketSender) NPCInteractEx(monster data.Monster) error {
	if err := ps.SendAMBPacket(amb.NewNPCInteraction(monster).GetPayload()); err != nil {
		return fmt.Errorf("NPCInteractEx send failed: %w", err)
	}
	return nil
}

// ObjectInteractEx — 0x41 13 B for generic interactive objects.
func (ps *PacketSender) ObjectInteractEx(obj data.Object) error {
	if err := ps.SendAMBPacket(amb.NewObjectInteraction(obj).GetPayload()); err != nil {
		return fmt.Errorf("ObjectInteractEx send failed: %w", err)
	}
	return nil
}

// NPCRepairSingle repairs one specific equipped item at a smith NPC (0x35
// mode=1 variant, 16 B). For blanket repair-all use NPCRepairAll.
func (ps *PacketSender) NPCRepairSingle(mode byte, itemPosX, itemPosY byte, npcUnitID, repairCosts, itemUnitID uint32) error {
	pkt := amb.NewNPCRepair(mode, itemPosX, itemPosY, npcUnitID, repairCosts, itemUnitID).GetPayload()
	if err := ps.SendAMBPacket(pkt); err != nil {
		return fmt.Errorf("NPCRepairSingle send failed: %w", err)
	}
	return nil
}

// SwapWeapon sends packet 0x50 (SwapWeaponSlots) via the D2GS_SendPacket APC
// path — the same path original koolo uses for ALL packets.
//
// 2026-04-21 discovery: previous SendDualPacket route (WriteProcessMemory to
// mirror + SendPacket) did not trigger client-side state update for weapon
// swap — D2R client never flipped ActiveWeaponSlot despite packet bytes
// landing cleanly. Using SendPacket alone (QueueUserAPC → stub → call
// D2GS_SendPacket(packet, len, 0) on main thread) lets D2R's own client code
// process the packet, which includes widget flip + item pointer swap + stat
// recalc. See d2go/pkg/memory/send_packet.go.
//
// Payload layout (30 B) — see packet.NewWeaponSwap for full details.
func (ps *PacketSender) SwapWeapon(
	slot0L, slot0R, slot1L, slot1R data.UnitID,
	leftSkillID, rightSkillID uint16,
	targetSlot uint8,
) error {
	if slot0L == 0 && slot0R == 0 && slot1L == 0 && slot1R == 0 {
		return fmt.Errorf("weapon swap: all GIDs are zero, cannot build packet")
	}
	pkt := amb.NewSwapWeaponSlots(slot0L, slot0R, slot1L, slot1R, leftSkillID, rightSkillID, targetSlot).GetPayload()
	if err := ps.SendAMBPacket(pkt); err != nil {
		return fmt.Errorf("failed to send weapon swap packet: %w", err)
	}
	return nil
}

// SwapWeaponFromData assembles the 0x50 payload from current game state and
// sends it. Convenience for callers that just want to toggle weapon sets —
// reads equipped items for both slots, pulls active LeftSkill/RightSkill,
// computes target slot = active ^ 1.
func (ps *PacketSender) SwapWeaponFromData(d *Data) error {
	keys := KeyBindingKeys(d.KeyBindings.SwapWeapons)
	if keys[0] != 0 && keys[0] != 255 && (keys[1] == 0 || keys[1] == 255) {
		if err := ps.PostKeyInProcess(keys[0]); err != nil {
			return fmt.Errorf("weapon swap key handler failed: %w", err)
		}
		return nil
	}

	var slot0L, slot0R, slot1L, slot1R data.UnitID
	for _, itm := range d.Inventory.ByLocation(item.LocationEquipped) {
		switch itm.Location.BodyLocation {
		case item.LocLeftArm:
			slot0L = itm.UnitID
		case item.LocRightArm:
			slot0R = itm.UnitID
		case item.LocLeftArmSecondary:
			slot1L = itm.UnitID
		case item.LocRightArmSecondary:
			slot1R = itm.UnitID
		}
	}
	return ps.SwapWeapon(
		slot0L, slot0R, slot1L, slot1R,
		uint16(d.PlayerUnit.LeftSkill), uint16(d.PlayerUnit.RightSkill),
		uint8(d.ActiveWeaponSlot)^1,
	)
}

func (ps *PacketSender) CastLeftSkillAtLocation(target, playerPos data.Position) error {
	if err := ps.SendPacket(packet.NewCastLeftSkillLocation(target, playerPos)); err != nil {
		return fmt.Errorf("failed to send left cast packet: %w", err)
	}
	return nil
}

// IdentifyItem sends an "identify with tome" packet (0x26) via the UI NetMan
// path. D2R format: 34 bytes. Takes tomeGID only — the game identifies the
// currently selected/hovered item. Caller must have inventory open with tome.
func (ps *PacketSender) IdentifyItem(tomeGID data.UnitID) error {
	if err := ps.SendUIPacket(packet.NewIdentifyTomeOnItem(tomeGID)); err != nil {
		return fmt.Errorf("failed to send identify packet: %w", err)
	}
	return nil
}

// ItemToStash moves an item from inventory to stash at (destCol, destRow).
// Uses 0x19 (OpItemMoveFrom = inv → buffer) per live capture sec_stash.log
// 2026-04-07. Server looks up the item's current location by GID; only the
// destination position is in the packet. Stash menu must be open.
func (ps *PacketSender) ItemToStash(itemGID data.UnitID, destCol, destRow uint8) error {
	// Test 2026-04-20 09:47: SendDualPacket path for 0x19 crashes D2R
	// 0xC0000005 on send_fn (similar to 0x33 34B — context-dependent
	// packets die when fed through send_fn externally). SendUIPacket
	// routes through UI NetMan vtable[5] which matches how D2R itself
	// dispatches stash moves from inside the trade/stash window.
	if err := ps.SendUIPacket(packet.NewItemToStash(itemGID, destCol, destRow)); err != nil {
		return fmt.Errorf("failed to send item-to-stash packet: %w", err)
	}
	return nil
}

// ItemFromStash moves an item from stash back into inventory at (destCol,
// destRow). Uses 0x18 (OpItemMoveTo = buffer → inv).
func (ps *PacketSender) ItemFromStash(itemGID data.UnitID, destCol, destRow uint8) error {
	if err := ps.SendDualPacket(packet.NewItemFromStash(itemGID, destCol, destRow)); err != nil {
		return fmt.Errorf("failed to send item-from-stash packet: %w", err)
	}
	return nil
}

// MoveToEntity sends a 0x04 packet — walk/run toward a specific entity.
// `action` = packet.MoveToEntityActionWalk (1) or MoveToEntityActionRun (2).
// `unitType` = 0 player, 1 NPC/monster, 2 object, 4 item.
func (ps *PacketSender) MoveToEntity(action, targetGID uint32, unitType byte) error {
	if err := ps.SendPacket(packet.NewMoveToEntity(action, targetGID, unitType).GetPayload()); err != nil {
		return fmt.Errorf("failed to send move-to-entity packet 0x04: %w", err)
	}
	return nil
}

// ItemMoveStash sends the 0x54 inv↔stash item-move packet.
// Context bytes: packet.InvCtxInventory (0), InvCtxCursor (1),
// InvCtxBelt (2), InvCtxStashMain (4), InvCtxStashShared (5).
// Caller must already have the relevant container open (stash/cube).
func (ps *PacketSender) ItemMoveStash(itemGID data.UnitID, srcCtx, srcCol, srcRow, dstCtx, dstCol, dstRow byte) error {
	return fmt.Errorf("legacy 0x54 ItemMoveStash is disabled after live crashes; use 0x19 pick + 0x18 put cursor flow")
}
