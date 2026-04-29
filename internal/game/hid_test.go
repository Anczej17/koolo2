package game

import "testing"

func TestHIDGuardBlocksOnlyWhenDisabled(t *testing.T) {
	hid := NewHID(nil, nil)
	if hid.IsDisabled() {
		t.Fatal("new HID should start enabled")
	}
	if !hid.guard("test") {
		t.Fatal("guard blocked while HID was enabled")
	}

	hid.Disable("unit test")
	if !hid.IsDisabled() {
		t.Fatal("Disable did not mark HID disabled")
	}
	if hid.guard("test") {
		t.Fatal("guard allowed action while HID was disabled")
	}
	if got := hid.BlockedCount(); got != 1 {
		t.Fatalf("blocked count = %d, want 1", got)
	}

	hid.Enable("unit test")
	if hid.IsDisabled() {
		t.Fatal("Enable did not mark HID enabled")
	}
	if !hid.guard("test") {
		t.Fatal("guard blocked after HID was re-enabled")
	}
}
