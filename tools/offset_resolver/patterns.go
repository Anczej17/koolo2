package main

// AOB pattern database ported from the Python offset-resolver
// (PatternRecognition_DMA/scanner.py PATTERNS dict, 2026-04-12 snapshot).
//
// Each entry is (pattern, operandOffset, description):
//   pattern       — IDA-style hex signature with "??" wildcards
//   operandOffset — byte offset within the match where a RIP-relative disp32
//                   starts. If -1, the match is the direct RVA (no RIP deref).
//   description   — human-readable instruction summary for debugging
//
// Multiple entries per offset act as primary + fallback signatures. The scanner
// tries them in order and accepts the first one that matches exactly once AND
// passes the validator (if one is registered).

type PatternDef struct {
	Pattern       string
	OperandOffset int
	Description   string
}

var Patterns = map[string][]PatternDef{
	"UnitTable": {
		{"48 63 C1 48 8D 0D ?? ?? ?? ?? 48 C1 E0 0A", 6,
			"MOVSXD RAX,ECX; LEA RCX,[rip+disp]; SHL RAX,0xA"},
		// D2R 3.0.92198 alt: paired LEA RDI/RSI loading UnitTable + ServerUnitTable.
		{"48 8D 3D ?? ?? ?? ?? 48 8D 35 ?? ?? ?? ?? 0F 1F 00 48 8B 0F 48 85 C9", 3,
			"LEA RDI,[rip+disp]; LEA RSI,[rip+disp2]; NOP; MOV RCX,[RDI]; TEST RCX (paired unit/serverUnit load)"},
	},
	"Hover": {
		{"8B C1 48 8D 0D ?? ?? ?? ?? 48 03 C0 80 3C C1", 5,
			"MOV EAX,ECX; LEA RCX,[rip+disp]; ADD RAX,RAX; CMP"},
		{"48 8D 15 ?? ?? ?? ?? 8B C1 89 1D", 3,
			"LEA RDX,[rip+disp]; MOV EAX,ECX; MOV [rip+...],EBX"},
		{"40 57 48 83 EC 20 8B C1 48 8D 0D ?? ?? ?? ?? 48 03 C0 80 3C C1 00", 11,
			"PUSH RDI; SUB RSP; MOV EAX,ECX; LEA RCX,[rip+disp]; ADD; CMP (HoverAccessor2)"},
		// D2R 3.0.92198 alt: Hover array destructor with stride=0x10 (distinguishes from sibling destructors
		// at stride 0x04/0x08/0x3F). Function prologue + LEA RDI,[rip+Hover] + ADD RDI,0x10 loop.
		{"48 89 5C 24 08 57 48 83 EC 20 48 8D 3D ?? ?? ?? ?? BB 08 00 00 00 48 8B CF E8 ?? ?? ?? ?? 48 83 C7 10", 13,
			"MOV [RSP+8],RBX; PUSH RDI; SUB RSP; LEA RDI,[rip+disp]; MOV EBX,8; MOV RCX,RDI; CALL; ADD RDI,0x10 (Hover dtor stride 0x10)"},
	},
	"Expansion": {
		{"75 3B 48 8B 05 ?? ?? ?? ?? 8B 48 14", 5,
			"JNZ +0x3B; MOV RAX,[rip+disp]; MOV ECX,[RAX+0x14]"},
	},
	"Roster": {
		{"48 8B 15 ?? ?? ?? ?? 83 F9 FF 74 ?? 48 85 D2 74 ??", 3,
			"MOV RDX,[rip+disp]; CMP ECX,-1; JZ; TEST RDX,RDX; JZ"},
		{"48 8B 0D ?? ?? ?? ?? 83 FF FF 74", 3,
			"MOV RCX,[rip+disp]; CMP EDI,-1; JZ"},
		{"48 8B 1D ?? ?? ?? ?? 3B FE", 3,
			"MOV RBX,[rip+disp]; CMP EDI,ESI"},
		{"48 8B 0D ?? ?? ?? ?? 48 85 C9 74 ?? 0F 1F 40 00 39 59 48 0F 84 ?? ?? ?? ?? 48 8B 41 78 48 85 C0", 3,
			"MOV RCX,[rip+disp]; TEST; JZ; NOP; CMP [RCX+0x48],EBX; JZ; MOV RAX,[RCX+0x78]"},
		{"48 8B 0D ?? ?? ?? ?? 48 85 C9 74 ?? 0F 1F 40 00 39 59 48", 3,
			"MOV RCX,[rip+disp]; TEST; JZ; NOP; CMP [RCX+0x48],EBX"},
	},
	"PanelMgrCont": {
		{"FF 50 08 48 8B 1D ?? ?? ?? ?? 48 8B 05", 6,
			"CALL [RAX+8]; MOV RBX,[rip+disp]; MOV RAX,..."},
	},
	"WidgetStates": {
		{"74 3D 48 8B 0D ?? ?? ?? ?? 48 89 5C 24", 5,
			"JZ +0x3D; MOV RCX,[rip+disp]; MOV [RSP+...]"},
		// D2R 3.0.92198 alt: MOV RCX,[rip+WidgetStates] followed by MOV RDX,<immediate QWORD cookie>
		// and CALL + TEST AL/R11B. The 0x12 82 C0 9C 8E CF D7 F2 cookie is build-stable anchor.
		{"48 8B 0D ?? ?? ?? ?? 48 BA 12 82 C0 9C 8E CF D7 F2 E8 ?? ?? ?? ?? 84 C0 74 0F 45", 3,
			"MOV RCX,[rip+disp]; MOV RDX,<QWORD const>; CALL; TEST AL; JZ; TEST R11B (widget lookup w/ cookie)"},
	},
	"FPS": {
		{"89 05 ?? ?? ?? ?? 8B C7 89 05", 2,
			"MOV [rip+disp],EAX; MOV EAX,EDI; MOV [rip+...]"},
	},
	"KeyBindSkills": {
		{"48 8D 3D ?? ?? ?? ?? 48 63 D9 48", 3,
			"LEA RDI,[rip+disp]; MOVSXD RBX,ECX"},
	},
	"UI": {
		{"80 3D ?? ?? ?? ?? 00 0F 85 ?? ?? ?? ?? 48 8B 03 48 8D 54 24", 2,
			"CMP byte [rip+disp],0x00; JNZ; MOV RAX,[RBX]; LEA RDX,[RSP+xx]"},
		{"0F 94 05 ?? ?? ?? ?? B9 0A 00 00 00", 3,
			"SETZ [rip+disp]; MOV ECX,0xA"},
		// D2R 3.0.92198 alt: CMP byte [rip+UI],0; JNZ long; MOV ECX,[rip+disp2]; CALL; MOV RCX,RAX.
		{"80 3D ?? ?? ?? ?? 00 0F 85 ?? ?? ?? ?? 8B 0D ?? ?? ?? ?? E8 ?? ?? ?? ?? 48 8B C8", 2,
			"CMP byte [rip+disp],0x00; JNZ long; MOV ECX,[rip+disp2]; CALL; MOV RCX,RAX (UI enable check)"},
	},
	"WaypointTable": {
		{"48 8B 1D ?? ?? ?? ?? 48 85 DB 74 ?? 0F B7 03", 3,
			"MOV RBX,[rip+disp]; TEST RBX,RBX; JZ; MOVZX EAX,word[RBX]"},
		// D2R 3.0.92198 alt: MOV RDI,[rip+WaypointTable] followed by MOV R11,R13; MOV EBX,[rip+...].
		{"48 8B 3D ?? ?? ?? ?? 4D 8B DD 8B 1D", 3,
			"MOV RDI,[rip+disp]; MOV R11,R13; MOV EBX,[rip+disp2] (wp table load)"},
	},
	"KeyBindings": {
		{"48 8D 05 ?? ?? ?? ?? 8B D7 4C 8D 05", 3,
			"LEA RAX,[rip+disp]; MOV EDX,EDI; LEA R8,..."},
		// D2R 3.0.92198 alt: LEA RAX,[rip+KeyBindings]; MOV EDX,EDI; LEA R8,[rip+skills]; NOP word;
		// CMP [RAX],BP; JZ. Primary-shape extended with JZ-16 trailing that only KeyBindings has.
		{"48 8D 05 ?? ?? ?? ?? 8B D7 4C 8D 05 ?? ?? ?? ?? 66 90 66 39 28", 3,
			"LEA RAX,[rip+disp]; MOV EDX,EDI; LEA R8,[rip+disp2]; NOP word; CMP [RAX],BP (KeyBindings binary search)"},
	},
	"QuestInfo": {
		{"80 FF 06 75 ?? 48 8B 05 ?? ?? ?? ?? 41 0F 10 00 48 8B 08 0F 11 01", 8,
			"CMP BH,6; JNZ; MOV RAX,[rip+disp]; MOVUPS; MOV RCX,[RAX]; MOVUPS (quest update)"},
		{"33 C0 0F B6 00 48 8B 05 ?? ?? ?? ?? 48 83 C4 40 5D C3", 8,
			"XOR EAX; MOVZX; MOV RAX,[rip+disp]; ADD RSP,0x40; POP RBP; RET"},
	},
	"LegacyGfx": {
		{"44 38 2D ?? ?? ?? ?? 48 8D 05", 3,
			"CMP [rip+disp],R13B; LEA RAX,..."},
	},
	"CharData": {
		{"4C 8B 3D ?? ?? ?? ?? 33 D2 4D 3B", 3,
			"MOV R15,[rip+disp]; XOR EDX,EDX; CMP R11,..."},
		{"48 8D 05 ?? ?? ?? ?? 89 93 FC 0C 00 00 48 8D 35", 3,
			"LEA RAX,[rip+disp]; MOV [RBX+0xCFC],EDX; LEA RSI,[rip+disp2]"},
	},
	"SendPacket": {
		{"E8 ?? ?? ?? ?? 0F B6 85 ?? ?? ?? ?? 48 03 F0", 1,
			"CALL rel32; MOVZX EAX,byte [RBP+xx]; ADD RSI,RAX"},
		// D2R 3.0.92198 alt: LEA RAX,[RBP-0x20]; MOV [RBP-0x40],RAX; CALL SendPacket; IMUL RCX,[RBP-0x78],0x2E8.
		// Unique preceding sequence disambiguates from 3 other CALL sites that resolve to the same target.
		{"48 8D 45 E0 48 89 45 C0 E8 ?? ?? ?? ?? 48 69 4D 88 E8 02 00 00 44 8B 44 24 60", 9,
			"LEA RAX,[RBP-0x20]; MOV [RBP-0x40],RAX; CALL SendPacket; IMUL RCX,[RBP-0x78],0x2E8; MOV R8D,[RSP+0x60]"},
	},
	"MouseXY": {
		{"8B 1D ?? ?? ?? ?? FF C8 33 FF 85 DB 79 ?? 8B DF", 2,
			"MOV EBX,[rip+disp]; DEC EAX; XOR EDI,EDI; TEST EBX,EBX; JNS; MOV EBX,EDI"},
		{"8B 1D ?? ?? ?? ?? FF C8", 2,
			"MOV EBX,[rip+disp]; DEC EAX (relaxed)"},
	},
	"PlayerUnitIndex": {
		{"8B 0D ?? ?? ?? ?? 48 8B 58 18 E8 ?? ?? ?? ?? 48 85 C0 74 ??", 2,
			"MOV ECX,[rip+disp]; MOV RBX,[RAX+0x18]; CALL; TEST RAX; JZ"},
	},
	"GameManager": {
		{"0F 84 ?? ?? ?? ?? 48 8B 05 ?? ?? ?? ?? 0F 57 C9 48 85 C0 74 ?? 0F B6 88 B9 00 00 00", 9,
			"JZ; MOV RAX,[rip+disp]; XORPS; TEST; JZ; MOVZX [+0xB9] (s_panelManager)"},
		{"0F 84 ?? ?? ?? ?? 48 8B 05 ?? ?? ?? ?? 0F 57 C9", 9,
			"JZ; MOV RAX,[rip+disp]; XORPS XMM1,XMM1 (s_panelManager relaxed)"},
		// D2R 3.0.92198 alt: MOV RAX,[rip+GameManager]; XORPS XMM1,XMM1; CMOVG R9,RCX; MOV [RSP+0x33],CL;
		// TEST RAX; JZ; MOVZX [RAX+0xB9]. The CMOVG+MOV [RSP+0x33] idiom is unique to s_panelManager.
		{"48 8B 05 ?? ?? ?? ?? 0F 57 C9 41 0F 4F C9 88 4C 24 33 48 85 C0 74 0A 44 0F B6 88 B9 00 00 00", 3,
			"MOV RAX,[rip+disp]; XORPS; CMOVG R9,RCX; MOV [RSP+0x33],CL; TEST; JZ; MOVZX [+0xB9] (s_panelManager)"},
	},
	"HpUpdateFn": {
		// operandOffset = -1 means the match start IS the RVA (no RIP deref).
		{"8B 81 E8 06 00 00 3B 81 EC 06 00 00 44 8B 81 C0 09 00 00", -1,
			"MOV EAX,[RCX+0x6E8]; CMP [RCX+0x6EC]; MOV R8D,[RCX+0x9C0]"},
		// D2R 3.0.92198 alt: prologue + new first-body instruction sequence.
		// PUSH RDI; SUB RSP,0x30; MOV EAX,[RCX+0x6E8]; MOV RDI,RCX; MOV EDX,[RCX+0xA88].
		{"40 57 48 83 EC 30 8B 81 E8 06 00 00 48 8B F9 8B 91 88 0A 00 00", -1,
			"PUSH RDI; SUB RSP,0x30; MOV EAX,[RCX+0x6E8]; MOV RDI,RCX; MOV EDX,[RCX+0xA88] (HP update fn entry)"},
	},
	"PlayerPos": {
		{"8B 05 ?? ?? ?? ?? 8D 34 80", 2,
			"MOV EAX,[rip+disp]; LEA ESI,[RAX+RAX*4] (g_PlayerPos)"},
	},
	"AutomapLayer": {
		{"48 8B 05 ?? ?? ?? ?? 41 8B F1", 3,
			"MOV RAX,[rip+disp]; MOV ESI,R9D (g_AutomapLayer)"},
	},
	"SPGame": {
		{"48 89 05 ?? ?? ?? ?? 48 85 C0 0F 84 ?? ?? ?? ?? 44 8B C7", 3,
			"MOV [rip+disp],RAX; TEST RAX; JZ; MOV R8D,EDI (g_CurrentSinglePlayerGame)"},
	},
	"CameraStuff": {
		{"66 0F 7F 05 ?? ?? ?? ?? 48 89 15 ?? ?? ?? ?? C7 05", 4,
			"MOVDQA [rip+disp],XMM0; MOV [rip+disp2],RDX; MOV dword (sgptCameraStuff)"},
	},
	"GameQuitEnum": {
		{"89 2D ?? ?? ?? ?? FF 15 ?? ?? ?? ?? 4C 8B 1D ?? ?? ?? ?? 48 8B D6 3B 05 ?? ?? ?? ?? 49 8B CB", 2,
			"MOV [rip+disp],EBP; CALL; MOV R11; MOV RDX,RSI; CMP (sgptGameQuitEnum)"},
	},
	"DataTbls": {
		{"48 8B 05 ?? ?? ?? ?? 8B 90 E8 04 00 00 85 D2 75 ?? 33 C0 C3", 3,
			"MOV RAX,[rip+disp]; MOV EDX,[RAX+0x4E8]; TEST EDX; JNZ; XOR EAX; RET (sgptDataTbls)"},
	},
	"NetworkMgr": {
		{"48 8B 05 ?? ?? ?? ?? 48 8D 0D ?? ?? ?? ?? FF 50 08 41 B9 ?? ?? ?? ?? 41 8D 56 01 45 33 C0", 3,
			"MOV RAX,[rip+vtable]; LEA RCX,[rip+this]; CALL [RAX+8]; (sgptNetworkMgr)"},
		// D2R 3.0.92198 alt: same vtable+this call shape, different caller frame:
		// MOV RAX,[rip+vtable]; LEA RCX,[rip+this]; CALL [RAX+8]; CMP [RSP+0x58],0.
		{"48 8B 05 ?? ?? ?? ?? 48 8D 0D ?? ?? ?? ?? FF 50 08 48 83 7C 24 58 00", 3,
			"MOV RAX,[rip+vtable]; LEA RCX,[rip+this]; CALL [RAX+8]; CMP [RSP+0x58],0 (sgptNetworkMgr caller2)"},
	},
	"AutomapGrid": {
		{"0F B6 0D ?? ?? ?? ?? 8B D3 E8 ?? ?? ?? ?? 48 85 C0 0F 84 ?? ?? ?? ?? F6 40 27 02", 3,
			"MOVZX ECX,byte [rip+disp]; MOV EDX,EBX; CALL; TEST RAX; JZ; TEST [RAX+0x27],0x02"},
		// D2R 3.0.92198 alt: idiomatic "check returned byte==SOMETHING" without the RAX test +0x27.
		// MOVZX ECX,byte [rip+disp]; MOV EDX,EBX; CALL; CMP AL,imm8.
		{"0F B6 0D ?? ?? ?? ?? 8B D3 E8 ?? ?? ?? ?? 3C", 3,
			"MOVZX ECX,byte [rip+disp]; MOV EDX,EBX; CALL; CMP AL,imm8 (new nGridSize check shape)"},
	},
	"ActiveSessionMgr": {
		{"0A 55 01 48 8B 05 ?? ?? ?? ?? 48 85 C0 74 ?? 48 83 78 08 00 74", 6,
			"MOV RAX,[rip+disp]; TEST RAX; JZ; CMP [RAX+8],0; JZ (s_ActiveSessionMgr)"},
	},
	"ConnectionState": {
		{"0F 57 C9 0F 2F C1 0F 97 C1 84 C9 74 ?? 48 8B 0D ?? ?? ?? ??", 16,
			"XORPS; COMISS; SETNBE; TEST; JZ; MOV RCX,[rip+disp] (g_ConnectionState)"},
		// D2R 3.0.92198 alt: primary is ambiguous (3 identical call sites, all resolving to the same RVA).
		// Extend with the specific JZ-0x2C trailing followed by CALL [rip+vtable]; MOV EAX,[RSP+0x28].
		{"0F 57 C9 0F 2F C1 0F 97 C1 84 C9 74 2C 48 8B 0D ?? ?? ?? ?? 48 8D 54 24 20 FF 15 ?? ?? ?? ?? 8B 44 24 28", 16,
			"XORPS; COMISS; SETNBE; TEST; JZ +0x2C; MOV RCX,[rip+disp]; LEA RDX,[RSP+0x20]; CALL [rip]; MOV EAX,[RSP+0x28]"},
	},
}

// OffsetMeta maps each pattern key to its d2go/Koolo Offset struct field name
// (empty string = not part of d2go Offset). Used when generating the Go
// struct literal at the end of a scan run.
var OffsetMeta = map[string]struct {
	D2GoField string // Field in memory.Offset struct
	Category  string // "d2go" or "extra"
}{
	"UnitTable":        {"UnitTable", "d2go"},
	"UI":               {"UI", "d2go"},
	"Hover":            {"Hover", "d2go"},
	"Expansion":        {"Expansion", "d2go"},
	"Roster":           {"RosterOffset", "d2go"},
	"PanelMgrCont":     {"PanelManagerContainerOffset", "d2go"},
	"WidgetStates":     {"WidgetStatesOffset", "d2go"},
	"FPS":              {"FPS", "d2go"},
	"WaypointTable":    {"WaypointTableOffset", "d2go"},
	"KeyBindings":      {"KeyBindingsOffset", "d2go"},
	"KeyBindSkills":    {"KeyBindingsSkillsOffset", "d2go"},
	"QuestInfo":        {"QuestInfo", "d2go"},
	"LegacyGfx":        {"LegacyGraphics", "d2go"},
	"CharData":         {"CharData", "d2go"},
	"SendPacket":       {"", "extra"}, // function RVA for rmod.dll CmdSendPacket
	"MouseXY":          {"", "extra"},
	"PlayerUnitIndex":  {"", "extra"},
	"GameManager":      {"", "extra"},
	"HpUpdateFn":       {"", "extra"},
	"PlayerPos":        {"", "extra"},
	"AutomapLayer":     {"", "extra"},
	"SPGame":           {"", "extra"},
	"CameraStuff":      {"", "extra"},
	"GameQuitEnum":     {"", "extra"},
	"DataTbls":         {"", "extra"},
	"NetworkMgr":       {"", "extra"},
	"AutomapGrid":      {"", "extra"},
	"ActiveSessionMgr": {"", "extra"},
	"ConnectionState":  {"", "extra"},
}

// Aliases: values that are identical to another resolved offset.
// Ping is just Expansion — same RVA, different semantic meaning.
var Aliases = map[string]string{
	"Ping": "Expansion",
}

// Deltas: values derived by adding a constant to another resolved offset.
// After the source is scanned, the target = source + delta.
var Deltas = map[string]struct {
	Source string
	Delta  int64
}{
	"FrameCount":      {"FPS", 0x4},
	"GameTick":        {"FPS", 0xC},
	"MonsterTable":    {"UnitTable", 0x400},
	"ServerUnitTable": {"UnitTable", 0x1800},
	"UIGameFlags":     {"UI", 0x11A},
}
