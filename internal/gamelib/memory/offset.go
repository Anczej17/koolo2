package memory

type Offset struct {
	GameData                    uintptr
	UnitTable                   uintptr
	UI                          uintptr
	Hover                       uintptr
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

func calculateOffsets(_ *Process) Offset {
	// D2R patch 2026-04-02 — offsets from scanutil + computed delta (-0x2F78)
	unitTableOffset := uintptr(0x1EA73D0)
	uiOffsetPtr := uintptr(0x1EB70CA)                 // computed: old - 0x2F78
	hoverOffset := uintptr(0x1DFB080)
	expOffset := uintptr(0x1DFA4E8)
	rosterOffset := uintptr(0x1EBD6E8)
	panelManagerContainerOffset := uintptr(0x1E11E40)
	WidgetStatesOffset := uintptr(0x1EDF700)           // computed: old - 0x2F78
	WaypointTableOffset := uintptr(0x1D59440)
	fpsOffset := uintptr(0x1D59414)
	keyBindingsOffset := uintptr(0x19D25B4)
	keyBindingsSkillsOffset := uintptr(0x1DFB190)
	questInfoOffset := uintptr(0x1EC7388)              // behavior verified by kolega
	tzOffset := uintptr(0x25B1B80)                     // behavior verified by kolega
	tzOfflineOffset := uintptr(0x25B2300)              // offline TZ array
	pingOffset := uintptr(0x1DFA4E8)
	legacyGfxOffset := uintptr(0x1EC3FC6)
	charDataOffset := uintptr(0x1DFE638)               // behavior verified by kolega
	selectedCharNameOffset := uintptr(0x1D50215)       // verified
	lastGameNameOffset := uintptr(0x25FA4E0)           // verified
	lastGamePasswordOffset := uintptr(0x25FA538)       // verified

	return Offset{
		UnitTable:                   unitTableOffset,
		UI:                          uiOffsetPtr,
		Hover:                       hoverOffset,
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
