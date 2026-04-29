package memory

import (
	"log"
)

type Offset struct {
	GameData                    uintptr
	UnitTable                   uintptr
	UI                          uintptr
	Hover                       uintptr
	MouseXY                     uintptr
	Expansion                   uintptr
	RosterOffset                uintptr
	PanelManagerContainerOffset uintptr
	WidgetStatesOffset          uintptr
	WaypointTableOffset         uintptr
	FPS                         uintptr
	KeyBindingsOffset           uintptr
	KeyBindingsSkillsOffset     uintptr
	QuestInfo                   uintptr
	TZ                          uintptr
	TZOffline                   uintptr
	Quests                      uintptr
	Ping                        uintptr
	LegacyGraphics              uintptr
	CharData                    uintptr
	SelectedCharName            uintptr
	LastGameName                uintptr
	LastGamePassword            uintptr
}

func calculateOffsets(p *Process) Offset {
	// Per-build baked offsets path (mirrors GID's precomputed Rdata-blob
	// strategy). On a known D2R build we skip the legacy hardcoded values
	// and use the table in baked_offsets.go — eliminates any "constant at
	// known offset" signature from our binary for that build.
	//
	// Logs the hash on every startup so a new D2R release can be added to
	// bakedOffsets after one offset_resolver run.
	if p != nil && p.pid != 0 {
		if baked, hash, ok := LookupBakedOffsets(p.pid); ok {
			log.Printf("offsets: using baked table for D2R build hash=%s", hash[:16])
			return baked
		} else if hash != "" {
			log.Printf("offsets: D2R build hash=%s NOT in baked table — using fallback hardcoded values; add this hash to bakedOffsets after offset_resolver verify", hash[:16])
		} else {
			log.Printf("offsets: D2RBuildHash failed (pid=%d) — using fallback hardcoded values", p.pid)
		}
	} else {
		log.Printf("offsets: process nil or pid 0 — using fallback hardcoded values")
	}

	// D2R patch 2026-04-02 — offsets from scanutil + computed delta (-0x2F78)
	unitTableOffset := uintptr(0x1EA73D0)
	uiOffsetPtr := uintptr(0x1EB70CA) // computed: old - 0x2F78
	hoverOffset := uintptr(0x1DFB080)
	mouseXYOffset := uintptr(0x1EC3BB8)
	expOffset := uintptr(0x1DFA4E8)
	rosterOffset := uintptr(0x1EBD6E8)
	panelManagerContainerOffset := uintptr(0x1E11E40)
	WidgetStatesOffset := uintptr(0x1EDF700) // computed: old - 0x2F78
	WaypointTableOffset := uintptr(0x1D59440)
	fpsOffset := uintptr(0x1D59414)
	keyBindingsOffset := uintptr(0x19D25B4)
	keyBindingsSkillsOffset := uintptr(0x1DFB190)
	questInfoOffset := uintptr(0x1EC3D58) // 2026-04-19 confirmed via offset_resolver (aob-primary)
	tzOffset := uintptr(0x25B1B80)        // behavior verified by kolega
	tzOfflineOffset := uintptr(0x25B2300) // offline TZ array
	pingOffset := uintptr(0x1DFA4E8)
	legacyGfxOffset := uintptr(0x1EC3FC6)
	charDataOffset := uintptr(0x1DFE678)         // 2026-04-19 confirmed via offset_resolver (aob-primary, was 0x1DFE638)
	selectedCharNameOffset := uintptr(0x1D50215) // verified
	lastGameNameOffset := uintptr(0x25FA4E0)     // verified
	lastGamePasswordOffset := uintptr(0x25FA538) // verified

	return Offset{
		UnitTable:                   unitTableOffset,
		UI:                          uiOffsetPtr,
		Hover:                       hoverOffset,
		MouseXY:                     mouseXYOffset,
		Expansion:                   expOffset,
		RosterOffset:                rosterOffset,
		PanelManagerContainerOffset: panelManagerContainerOffset,
		WidgetStatesOffset:          WidgetStatesOffset,
		WaypointTableOffset:         WaypointTableOffset,
		FPS:                         fpsOffset,
		KeyBindingsOffset:           keyBindingsOffset,
		KeyBindingsSkillsOffset:     keyBindingsSkillsOffset,
		QuestInfo:                   questInfoOffset,
		TZ:                          tzOffset,
		TZOffline:                   tzOfflineOffset,
		Ping:                        pingOffset,
		LegacyGraphics:              legacyGfxOffset,
		CharData:                    charDataOffset,
		SelectedCharName:            selectedCharNameOffset,
		LastGameName:                lastGameNameOffset,
		LastGamePassword:            lastGamePasswordOffset,
	}
}
