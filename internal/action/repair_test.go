package action

import "testing"

func TestParseRepairAllCostText(t *testing.T) {
	tests := []struct {
		name string
		text string
		want uint32
		ok   bool
	}{
		{name: "plain", text: "Repair all equipment: 144854", want: 144854, ok: true},
		{name: "with comma", text: "Repair all equipment: 144,854", want: 144854, ok: true},
		{name: "with dot", text: "Repair all equipment: 144.854", want: 144854, ok: true},
		{name: "zero is valid", text: "Repair all equipment: 0", want: 0, ok: true},
		{name: "not repair label", text: "Gold: 144854", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseRepairAllCostText(tt.text)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("parseRepairAllCostText(%q) = (%d, %v), want (%d, %v)", tt.text, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestFirstRepairCostNumber(t *testing.T) {
	got, ok := firstRepairCostNumber("Repair all equipment: 144.854")
	if !ok || got != 144854 {
		t.Fatalf("firstRepairCostNumber got (%d, %v), want (144854, true)", got, ok)
	}
}
