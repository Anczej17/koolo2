package game

import (
	"encoding/hex"
	"errors"
	"testing"

	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/npc"
	"local/internal/svc/internal/gamelib/data/skill"
	"local/internal/svc/internal/packet/amb"
)

type npcActionRouteProbe struct {
	gamePackets      [][]byte
	uiPackets        [][]byte
	dualPackets      [][]byte
	ambPackets       [][]byte
	dualAPC          [][]byte
	uiDual           [][]byte
	sendFnHijack     [][]byte
	npc9BWrapper     []npc9BWrapperCall
	npcDialogActions []uint32
	npcCancels       []uint32
	clicks           int
	forceClicks      int
	postKeys         int
}

type npc9BWrapperCall struct {
	opcode uint8
	arg1   uint32
	arg2   uint32
}

func (p *npcActionRouteProbe) SendPacket(pkt []byte) error {
	p.gamePackets = append(p.gamePackets, append([]byte(nil), pkt...))
	return nil
}

func (p *npcActionRouteProbe) SendPacketAPC(pkt []byte) error {
	p.ambPackets = append(p.ambPackets, append([]byte(nil), pkt...))
	return nil
}

func (p *npcActionRouteProbe) SendUIPacket(pkt []byte) error {
	p.uiPackets = append(p.uiPackets, append([]byte(nil), pkt...))
	return nil
}

func (p *npcActionRouteProbe) SendDualPacket(pkt []byte) error {
	p.dualPackets = append(p.dualPackets, append([]byte(nil), pkt...))
	return nil
}

func (p *npcActionRouteProbe) SendDualPacketAPC(pkt []byte) error {
	p.dualAPC = append(p.dualAPC, append([]byte(nil), pkt...))
	return nil
}

func (p *npcActionRouteProbe) SendUIDualPacket(pkt []byte) error {
	p.uiDual = append(p.uiDual, append([]byte(nil), pkt...))
	return nil
}

func (p *npcActionRouteProbe) SendPacketViaSendFnHijack(pkt []byte) error {
	p.sendFnHijack = append(p.sendFnHijack, append([]byte(nil), pkt...))
	return nil
}

func (p *npcActionRouteProbe) Send9BWrapper(opcode uint8, arg1, arg2 uint32) error {
	p.npc9BWrapper = append(p.npc9BWrapper, npc9BWrapperCall{opcode: opcode, arg1: arg1, arg2: arg2})
	return nil
}

func (p *npcActionRouteProbe) CallNPCDialogOption(actionID uint32) error {
	p.npcDialogActions = append(p.npcDialogActions, actionID)
	return nil
}

func (p *npcActionRouteProbe) CallNPCCancel(npcID uint32) error {
	p.npcCancels = append(p.npcCancels, npcID)
	return nil
}

func (p *npcActionRouteProbe) ClickAt(int32, int32, byte) error {
	p.clicks++
	return nil
}
func (p *npcActionRouteProbe) ForceClick(int32, int32) error {
	p.forceClicks++
	return nil
}
func (p *npcActionRouteProbe) PostKeyInProcess(byte) error {
	p.postKeys++
	return nil
}

type gameOnlyProbe struct {
	gamePackets [][]byte
	uiPackets   [][]byte
	dualPackets [][]byte
}

func (p *gameOnlyProbe) SendPacket(pkt []byte) error {
	p.gamePackets = append(p.gamePackets, append([]byte(nil), pkt...))
	return nil
}

func (p *gameOnlyProbe) SendUIPacket(pkt []byte) error {
	p.uiPackets = append(p.uiPackets, append([]byte(nil), pkt...))
	return nil
}

func (p *gameOnlyProbe) SendDualPacket(pkt []byte) error {
	p.dualPackets = append(p.dualPackets, append([]byte(nil), pkt...))
	return nil
}

func (p *gameOnlyProbe) ClickAt(int32, int32, byte) error { return nil }
func (p *gameOnlyProbe) ForceClick(int32, int32) error    { return nil }
func (p *gameOnlyProbe) PostKeyInProcess(byte) error      { return nil }

func TestPacketSenderDisableBlocksProcessWrites(t *testing.T) {
	probe := &npcActionRouteProbe{}
	ps := NewPacketSender(probe)
	ps.Disable()

	checkDisabled := func(name string, err error) {
		t.Helper()
		if !errors.Is(err, ErrPacketSenderDisabled) {
			t.Fatalf("%s returned %v, want ErrPacketSenderDisabled", name, err)
		}
	}

	checkDisabled("SendPacket", ps.SendPacket([]byte{0x0c, 0, 0, 0, 0}))
	checkDisabled("SendAMBPacket", ps.SendAMBPacket(amb.NewNPCInit(0x0e).GetPayload()))
	checkDisabled("ClickAt", ps.ClickAt(1, 2, MouseLeft))
	checkDisabled("ForceClick", ps.ForceClick(1, 2))
	checkDisabled("PostKeyInProcess", ps.PostKeyInProcess(0x57))
	checkDisabled("NPCPrimeInteraction", ps.NPCPrimeInteraction(0x0e, 1, data.Position{}, data.Position{}))

	if len(probe.gamePackets) != 0 || len(probe.ambPackets) != 0 || len(probe.dualAPC) != 0 ||
		probe.clicks != 0 || probe.forceClicks != 0 || probe.postKeys != 0 {
		t.Fatalf("disabled sender reached process: game=%d amb=%d dualAPC=%d clicks=%d forceClicks=%d postKeys=%d",
			len(probe.gamePackets), len(probe.ambPackets), len(probe.dualAPC), probe.clicks, probe.forceClicks, probe.postKeys)
	}
}

func TestNPCActionUsesNativeDialogOptionWhenAvailable(t *testing.T) {
	probe := &npcActionRouteProbe{}
	ps := NewPacketSender(probe)

	if err := ps.NPCAction(1, 0x0e); err != nil {
		t.Fatalf("NPCAction failed: %v", err)
	}

	if len(probe.npcDialogActions) != 1 || probe.npcDialogActions[0] != 1 {
		t.Fatalf("expected native dialog action 1, got %#v", probe.npcDialogActions)
	}
	if len(probe.uiPackets) != 0 || len(probe.gamePackets) != 0 || len(probe.ambPackets) != 0 || len(probe.dualPackets) != 0 || len(probe.dualAPC) != 0 || len(probe.uiDual) != 0 || len(probe.npc9BWrapper) != 0 {
		t.Fatalf("unexpected NPCAction route: ui=%d game=%d amb=%d dual=%d dualAPC=%d uiDual=%d wrapper=%d",
			len(probe.uiPackets), len(probe.gamePackets), len(probe.ambPackets), len(probe.dualPackets), len(probe.dualAPC), len(probe.uiDual), len(probe.npc9BWrapper))
	}
}

func TestNPCTradeUsesNativeActionIDOne(t *testing.T) {
	probe := &npcActionRouteProbe{}
	ps := NewPacketSender(probe)

	if err := ps.NPCTrade(0x0e); err != nil {
		t.Fatalf("NPCTrade failed: %v", err)
	}

	if len(probe.npcDialogActions) != 1 || probe.npcDialogActions[0] != 1 {
		t.Fatalf("expected native trade action 1, got %#v", probe.npcDialogActions)
	}
}

func TestNPCIdentifyActionUsesNativeActionIDOne(t *testing.T) {
	probe := &npcActionRouteProbe{}
	ps := NewPacketSender(probe)

	if err := ps.NPCIdentifyAction(0x0e); err != nil {
		t.Fatalf("NPCIdentifyAction failed: %v", err)
	}

	if len(probe.npcDialogActions) != 1 || probe.npcDialogActions[0] != 1 {
		t.Fatalf("expected native identify action 1, got %#v", probe.npcDialogActions)
	}
}

func TestNPCServiceActionsUseAMBMenuMap(t *testing.T) {
	tests := []struct {
		name       string
		send       func(*PacketSender) error
		wantAction uint32
	}{
		{
			name: "Akara trade",
			send: func(ps *PacketSender) error {
				return ps.NPCTradeFor(uint32(npc.Akara), 0x0e)
			},
			wantAction: 1,
		},
		{
			name: "Gheed gamble",
			send: func(ps *PacketSender) error {
				return ps.NPCGambleFor(uint32(npc.Gheed), 0x0e)
			},
			wantAction: 2,
		},
		{
			name: "Cain identify",
			send: func(ps *PacketSender) error {
				return ps.NPCIdentifyActionFor(uint32(npc.DeckardCain5), 0x0e)
			},
			wantAction: 1,
		},
		{
			name: "Charsi imbue",
			send: func(ps *PacketSender) error {
				return ps.NPCImbueFor(uint32(npc.Charsi), 0x0e)
			},
			wantAction: 3,
		},
		{
			name: "Larzuk socket",
			send: func(ps *PacketSender) error {
				return ps.NPCSocketFor(uint32(npc.Larzuk), 0x0e)
			},
			wantAction: 3,
		},
		{
			name: "Anya personalize",
			send: func(ps *PacketSender) error {
				return ps.NPCPersonalizeFor(uint32(npc.Drehya), 0x0e)
			},
			wantAction: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			probe := &npcActionRouteProbe{}
			ps := NewPacketSender(probe)

			if err := tt.send(ps); err != nil {
				t.Fatalf("send failed: %v", err)
			}
			if len(probe.npcDialogActions) != 1 || probe.npcDialogActions[0] != tt.wantAction {
				t.Fatalf("expected native service action %d, got %#v", tt.wantAction, probe.npcDialogActions)
			}
			if len(probe.dualAPC) != 0 || len(probe.npc9BWrapper) != 0 {
				t.Fatalf("unexpected raw dialog route: dualAPC=%d wrapper=%#v", len(probe.dualAPC), probe.npc9BWrapper)
			}
		})
	}
}

func TestNPCServiceActionRejectsMissingAMBMenuMapping(t *testing.T) {
	probe := &npcActionRouteProbe{}
	ps := NewPacketSender(probe)

	if err := ps.NPCGambleFor(uint32(npc.Akara), 0x0e); err == nil {
		t.Fatal("expected missing AMB menu mapping error")
	}
	if len(probe.ambPackets) != 0 {
		t.Fatalf("expected no packet on missing mapping, got %d", len(probe.ambPackets))
	}
	if len(probe.dualPackets) != 0 {
		t.Fatalf("expected no dual packet on missing mapping, got %d", len(probe.dualPackets))
	}
}

func TestNPCInitUsesD2GSDualAPCAndCancelUsesNativeWhenAvailable(t *testing.T) {
	probe := &npcActionRouteProbe{}
	ps := NewPacketSender(probe)

	if err := ps.NPCInit(0x0e); err != nil {
		t.Fatalf("NPCInit failed: %v", err)
	}
	if err := ps.NPCCancel(0x0e); err != nil {
		t.Fatalf("NPCCancel failed: %v", err)
	}

	if len(probe.dualAPC) != 1 {
		t.Fatalf("expected one AMB dual-APC packet, got %d", len(probe.dualAPC))
	}
	if got := hex.EncodeToString(probe.dualAPC[0]); got != "2f0e000000" {
		t.Fatalf("unexpected NPCInit payload: %s", got)
	}
	if len(probe.npcCancels) != 1 || probe.npcCancels[0] != 0x0e {
		t.Fatalf("expected native NPC cancel call for 0x0e, got %#v", probe.npcCancels)
	}
}

func TestNPCPrimeInteractionUsesGamePreInteractAndDualRunToUnit(t *testing.T) {
	probe := &npcActionRouteProbe{}
	ps := NewPacketSender(probe)

	if err := ps.NPCPrimeInteraction(0x0e, 0xd6, data.Position{X: 4532, Y: 4597}, data.Position{X: 4533, Y: 4601}); err != nil {
		t.Fatalf("NPCPrimeInteraction failed: %v", err)
	}

	if len(probe.ambPackets) != 1 {
		t.Fatalf("expected one AMB APC pre-interact packet, got %d", len(probe.ambPackets))
	}
	if got := hex.EncodeToString(probe.ambPackets[0]); got != "4d0e000000" {
		t.Fatalf("unexpected NPCPrimeInteraction pre-interact payload: %s", got)
	}
	if len(probe.dualAPC) != 1 {
		t.Fatalf("expected one AMB dual-APC packet, got %d", len(probe.dualAPC))
	}
	got := probe.dualAPC[0]
	if len(got) != 214 {
		t.Fatalf("unexpected NPCPrimeInteraction length: got %d want 214", len(got))
	}
	if gotHex := hex.EncodeToString(got[:13]); gotHex != "04010000000e000000b411f511" {
		t.Fatalf("unexpected NPCPrimeInteraction prefix: %s", gotHex)
	}
	for i, b := range got[13:] {
		if b != 0 {
			t.Fatalf("NPCPrimeInteraction padding byte %d = 0x%02X, want 0", i+13, b)
		}
	}
	if len(probe.uiPackets) != 0 || len(probe.gamePackets) != 0 || len(probe.dualPackets) != 0 {
		t.Fatalf("unexpected NPCPrimeInteraction route: ui=%d game=%d dual=%d",
			len(probe.uiPackets), len(probe.gamePackets), len(probe.dualPackets))
	}
}

func TestNPCDialogPositionSyncUsesAMBDualRunToLocation(t *testing.T) {
	probe := &npcActionRouteProbe{}
	ps := NewPacketSender(probe)

	if err := ps.NPCDialogPositionSync(data.Position{X: 4533, Y: 4606}); err != nil {
		t.Fatalf("NPCDialogPositionSync failed: %v", err)
	}

	if len(probe.dualAPC) != 1 {
		t.Fatalf("expected one AMB dual-APC packet, got %d", len(probe.dualAPC))
	}
	got := probe.dualAPC[0]
	if len(got) != 210 {
		t.Fatalf("unexpected NPCDialogPositionSync length: got %d want 210", len(got))
	}
	if gotHex := hex.EncodeToString(got[:9]); gotHex != "03b511fe11b511fe11" {
		t.Fatalf("unexpected NPCDialogPositionSync prefix: %s", gotHex)
	}
	for i, b := range got[9:] {
		if b != 0 {
			t.Fatalf("NPCDialogPositionSync padding byte %d = 0x%02X, want 0", i+9, b)
		}
	}
	if len(probe.uiPackets) != 0 || len(probe.gamePackets) != 0 || len(probe.dualPackets) != 0 || len(probe.ambPackets) != 0 {
		t.Fatalf("unexpected NPCDialogPositionSync route: ui=%d game=%d dual=%d amb=%d",
			len(probe.uiPackets), len(probe.gamePackets), len(probe.dualPackets), len(probe.ambPackets))
	}
}

func TestSendAMBPacketUsesDualForNPCActionWithoutAPC(t *testing.T) {
	probe := &gameOnlyProbe{}
	ps := NewPacketSender(probe)

	if err := ps.NPCAction(2, 0x1234); err != nil {
		t.Fatalf("NPCAction failed: %v", err)
	}

	if len(probe.dualPackets) != 1 {
		t.Fatalf("expected one dual packet, got %d", len(probe.dualPackets))
	}
	if got := hex.EncodeToString(probe.dualPackets[0]); got != "380200000034120000" {
		t.Fatalf("unexpected fallback payload: %s", got)
	}
	if len(probe.uiPackets) != 0 || len(probe.gamePackets) != 0 {
		t.Fatalf("unexpected fallback route: ui=%d game=%d", len(probe.uiPackets), len(probe.gamePackets))
	}
}

func TestNPCPreInteractUsesFiveByte4D(t *testing.T) {
	probe := &npcActionRouteProbe{}
	ps := NewPacketSender(probe)

	if err := ps.NPCPreInteract(0x0e, 0x01, 4499, 4609, 4533, 4601); err != nil {
		t.Fatalf("NPCPreInteract failed: %v", err)
	}

	if len(probe.ambPackets) != 1 {
		t.Fatalf("expected one AMB APC packet, got %d", len(probe.ambPackets))
	}
	if got := hex.EncodeToString(probe.ambPackets[0]); got != "4d0e000000" {
		t.Fatalf("unexpected NPCPreInteract payload: %s", got)
	}
}

func TestAMBItemVendorAndSwapPacketsUseD2GSSendAPC(t *testing.T) {
	item := data.Item{UnitID: 0x53, Position: data.Position{X: 7, Y: 1}}
	merchant := data.Monster{UnitID: 0x0e}
	obj := data.Object{ID: 0x77}
	entrance := data.Entrance{ID: 0x88}

	tests := []struct {
		name        string
		send        func(*PacketSender) error
		wantOpcode  byte
		wantLen     int
		wantGame    bool
		wantDualAPC bool
		wantUIDual  bool
		wantHijack  bool
	}{
		{
			name: "SelectRightSkill",
			send: func(ps *PacketSender) error {
				return ps.SelectRightSkill(skill.BattleOrders)
			},
			wantOpcode: 0x3C,
			wantLen:    9,
		},
		{
			name: "CastRightSkillAMB",
			send: func(ps *PacketSender) error {
				return ps.CastRightSkillAMB(4488, 4652)
			},
			wantOpcode: 0x0C,
			wantLen:    5,
		},
		{
			name: "CastSkillAtLocation",
			send: func(ps *PacketSender) error {
				return ps.CastSkillAtLocation(data.Position{X: 4488, Y: 4652}, data.Position{})
			},
			wantOpcode: 0x0C,
			wantLen:    5,
		},
		{
			name: "NPCBuyAMB",
			send: func(ps *PacketSender) error {
				return ps.NPCBuyAMB(item, merchant, 0x1234, 0, 0, 0, 0, 0)
			},
			wantOpcode: 0x32,
			wantLen:    24,
		},
		{
			name: "NPCSellAMB",
			send: func(ps *PacketSender) error {
				return ps.NPCSellAMB(item, merchant, 0x1234, 0, 0, 0, 0, 0)
			},
			wantOpcode: 0x33,
			wantLen:    24,
		},
		{
			name: "NPCRepairAll",
			send: func(ps *PacketSender) error {
				return ps.NPCRepairAll(0x0e, 0x1234)
			},
			wantOpcode:  0x35,
			wantLen:     16,
			wantDualAPC: true,
		},
		{
			name: "NPCIdentifyAllAMB",
			send: func(ps *PacketSender) error {
				return ps.NPCIdentifyAll(0x02, 0x5c, 8, 8)
			},
			wantOpcode:  0x34,
			wantLen:     21,
			wantDualAPC: true,
		},
		{
			name: "QuickItemMoveAMBWithPosition",
			send: func(ps *PacketSender) error {
				return ps.QuickItemMoveAMBWithPosition(item, amb.ContainerInventory, amb.ContainerStash, 1, 1)
			},
			wantOpcode: 0x54,
			wantLen:    21,
		},
		{
			name: "PutItemToSharedStash",
			send: func(ps *PacketSender) error {
				return ps.PutItemToSharedStash(item, 2, 0, 0)
			},
			wantOpcode: 0x55,
			wantLen:    29,
		},
		{
			name: "UseCubeTransmute",
			send: func(ps *PacketSender) error {
				cube := data.Item{UnitID: 0x59}
				items := []data.Item{
					{UnitID: 0x58, Position: data.Position{X: 2, Y: 1}},
					{UnitID: 0x71, Position: data.Position{X: 2, Y: 2}},
					{UnitID: 0x62, Position: data.Position{X: 2, Y: 3}},
				}
				return ps.UseCubeTransmute(cube, items)
			},
			wantOpcode: 0x20,
			wantLen:    23,
			wantGame:   true,
		},
		{
			name: "SwapWeaponSlotsAMB",
			send: func(ps *PacketSender) error {
				return ps.SwapWeaponSlotsAMB(1, 2, 3, 4, 5, 6, 1)
			},
			wantOpcode:  0x50,
			wantLen:     30,
			wantDualAPC: true,
		},
		{
			name: "SwapWeapon",
			send: func(ps *PacketSender) error {
				return ps.SwapWeapon(1, 2, 3, 4, 5, 6, 1)
			},
			wantOpcode:  0x50,
			wantLen:     30,
			wantDualAPC: true,
		},
		{
			name: "UnitInteract",
			send: func(ps *PacketSender) error {
				return ps.UnitInteract(merchant.UnitID)
			},
			wantOpcode: 0x40,
			wantLen:    5,
		},
		{
			name: "NPCInteractEx",
			send: func(ps *PacketSender) error {
				return ps.NPCInteractEx(merchant)
			},
			wantOpcode: 0x41,
			wantLen:    13,
		},
		{
			name: "NPCInteract0x13",
			send: func(ps *PacketSender) error {
				return ps.NPCInteract0x13(merchant, 0xd6)
			},
			wantOpcode: 0x13,
			wantLen:    9,
		},
		{
			name: "SimpleObjectInteract",
			send: func(ps *PacketSender) error {
				return ps.SimpleObjectInteract(obj)
			},
			wantOpcode: 0x40,
			wantLen:    5,
		},
		{
			name: "ObjectInteractEx",
			send: func(ps *PacketSender) error {
				return ps.ObjectInteractEx(obj)
			},
			wantOpcode: 0x41,
			wantLen:    13,
		},
		{
			name: "ObjectInteract0x13",
			send: func(ps *PacketSender) error {
				return ps.ObjectInteract0x13(obj, 0xd6)
			},
			wantOpcode: 0x13,
			wantLen:    9,
		},
		{
			name: "PortalInteract",
			send: func(ps *PacketSender) error {
				return ps.PortalInteract(obj)
			},
			wantOpcode: 0x41,
			wantLen:    13,
		},
		{
			name: "EntranceInteractEx",
			send: func(ps *PacketSender) error {
				return ps.EntranceInteractEx(entrance)
			},
			wantOpcode: 0x41,
			wantLen:    13,
		},
		{
			name: "EntranceInteractAMB",
			send: func(ps *PacketSender) error {
				return ps.EntranceInteractAMB(entrance)
			},
			wantOpcode: 0x40,
			wantLen:    5,
		},
		{
			name: "TpInteractAMB",
			send: func(ps *PacketSender) error {
				return ps.TpInteractAMB(obj)
			},
			wantOpcode: 0x41,
			wantLen:    13,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			probe := &npcActionRouteProbe{}
			ps := NewPacketSender(probe)

			if err := tt.send(ps); err != nil {
				t.Fatalf("send failed: %v", err)
			}

			var got []byte
			if tt.wantGame {
				if len(probe.gamePackets) != 1 {
					t.Fatalf("expected one game packet, got %d", len(probe.gamePackets))
				}
				got = probe.gamePackets[0]
			} else if tt.wantHijack {
				if len(probe.sendFnHijack) != 1 {
					t.Fatalf("expected one AMB send_fn hijack packet, got %d", len(probe.sendFnHijack))
				}
				got = probe.sendFnHijack[0]
			} else if tt.wantUIDual {
				if len(probe.uiDual) != 1 {
					t.Fatalf("expected one AMB UI-dual packet, got %d", len(probe.uiDual))
				}
				got = probe.uiDual[0]
			} else if tt.wantDualAPC {
				if len(probe.dualAPC) != 1 {
					t.Fatalf("expected one AMB dual APC packet, got %d", len(probe.dualAPC))
				}
				got = probe.dualAPC[0]
			} else {
				if len(probe.ambPackets) != 1 {
					t.Fatalf("expected one AMB APC packet, got %d", len(probe.ambPackets))
				}
				got = probe.ambPackets[0]
			}
			if len(got) != tt.wantLen {
				t.Fatalf("unexpected payload length: got %d want %d hex=%s", len(got), tt.wantLen, hex.EncodeToString(got))
			}
			if got[0] != tt.wantOpcode {
				t.Fatalf("unexpected opcode: got 0x%02X want 0x%02X hex=%s", got[0], tt.wantOpcode, hex.EncodeToString(got))
			}
			if (!tt.wantGame && len(probe.gamePackets) != 0) || len(probe.uiPackets) != 0 {
				t.Fatalf("unexpected route: game=%d ui=%d",
					len(probe.gamePackets), len(probe.uiPackets))
			}
			if tt.wantDualAPC && len(probe.ambPackets) != 0 {
				t.Fatalf("expected no single AMB APC packets for dual opcode, got %d", len(probe.ambPackets))
			}
			if tt.wantUIDual && len(probe.ambPackets) != 0 {
				t.Fatalf("expected no single AMB APC packets for UI-dual opcode, got %d", len(probe.ambPackets))
			}
			if !tt.wantUIDual && len(probe.uiDual) != 0 {
				t.Fatalf("expected no UI-dual AMB packets for this opcode, got %d", len(probe.uiDual))
			}
			if !tt.wantHijack && len(probe.sendFnHijack) != 0 {
				t.Fatalf("expected no send_fn hijack AMB packets for this opcode, got %d", len(probe.sendFnHijack))
			}
			if !tt.wantDualAPC && len(probe.dualAPC) != 0 {
				t.Fatalf("expected no dual APC AMB packets for single opcode, got %d", len(probe.dualAPC))
			}
			if len(probe.dualPackets) != 0 {
				t.Fatalf("expected no dual AMB packets for single opcode, got %d", len(probe.dualPackets))
			}
		})
	}
}
