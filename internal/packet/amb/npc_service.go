package amb

import (
	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/npc"
)

type NPCService byte

const (
	NPCServiceTrade NPCService = iota + 1
	NPCServiceRepair
	NPCServiceGamble
	NPCServiceIdentify
	NPCServiceImbue
	NPCServiceSocket
	NPCServicePersonalize
)

var npcServiceActions = map[uint32]map[NPCService]NPCActionType{
	uint32(npc.Gheed):        {NPCServiceTrade: 0x01, NPCServiceGamble: 0x02},
	uint32(npc.Akara):        {NPCServiceTrade: 0x01},
	uint32(npc.Charsi):       {NPCServiceTrade: 0x01, NPCServiceRepair: 0x02, NPCServiceImbue: 0x03},
	uint32(npc.DeckardCain):  {NPCServiceIdentify: 0x01},
	uint32(npc.DeckardCain2): {NPCServiceIdentify: 0x01},
	uint32(npc.DeckardCain3): {NPCServiceIdentify: 0x01},
	uint32(npc.DeckardCain4): {NPCServiceIdentify: 0x01},
	uint32(npc.DeckardCain5): {NPCServiceIdentify: 0x01},
	uint32(npc.DeckardCain6): {NPCServiceIdentify: 0x01},
	uint32(npc.Drognan):      {NPCServiceTrade: 0x01},
	uint32(npc.Fara):         {NPCServiceTrade: 0x01, NPCServiceRepair: 0x02},
	uint32(npc.Elzix):        {NPCServiceTrade: 0x01, NPCServiceGamble: 0x02},
	uint32(npc.Lysander):     {NPCServiceTrade: 0x01},
	uint32(npc.Greiz):        {NPCServiceTrade: 0x01},
	uint32(npc.Hratli):       {NPCServiceTrade: 0x01, NPCServiceRepair: 0x02},
	uint32(npc.Alkor):        {NPCServiceTrade: 0x01, NPCServiceGamble: 0x02},
	uint32(npc.Ormus):        {NPCServiceTrade: 0x01},
	uint32(npc.Asheara):      {NPCServiceTrade: 0x01},
	uint32(npc.Jamella):      {NPCServiceTrade: 0x01, NPCServiceGamble: 0x02},
	uint32(npc.Halbu):        {NPCServiceTrade: 0x01, NPCServiceRepair: 0x02},
	uint32(npc.Larzuk):       {NPCServiceTrade: 0x01, NPCServiceRepair: 0x02, NPCServiceSocket: 0x03},
	uint32(npc.Drehya):       {NPCServiceTrade: 0x01, NPCServiceGamble: 0x02, NPCServicePersonalize: 0x03},
	uint32(npc.Malah):        {NPCServiceTrade: 0x01},
	uint32(npc.Kashya):       {NPCServiceTrade: 0x01},
	uint32(npc.QualKehk):     {NPCServiceTrade: 0x01},
}

func LookupNPCServiceAction(npcClassID uint32, service NPCService) (NPCActionType, bool) {
	services, ok := npcServiceActions[npcClassID]
	if !ok {
		return 0, false
	}
	action, ok := services[service]
	return action, ok
}

func NewNPCServiceAction(npcClassID uint32, service NPCService, npcUnitID data.UnitID) (*NPCAction, bool) {
	action, ok := LookupNPCServiceAction(npcClassID, service)
	if !ok {
		return nil, false
	}
	return NewNPCAction(action, npcUnitID), true
}
