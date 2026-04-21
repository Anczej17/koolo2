package game

import (
	"math/rand"
	"time"

	"local/internal/svc/internal/livetrace"

	"github.com/lxn/win"
)

// traceClickLabel converts MouseButton to a readable label for livetrace.
func traceClickLabel(btn MouseButton) string {
	switch btn {
	case LeftButton:
		return "L"
	case RightButton:
		return "R"
	default:
		return "?"
	}
}

const (
	RightButton MouseButton = win.MK_RBUTTON
	LeftButton  MouseButton = win.MK_LBUTTON

	ShiftKey ModifierKey = win.VK_SHIFT
	CtrlKey  ModifierKey = win.VK_CONTROL
)

type MouseButton uint
type ModifierKey byte

const pointerReleaseDelay = 150 * time.Millisecond

// MovePointer moves the mouse to the requested position, x and y should be the final position based on
// pixels shown in the screen. Top-left corner is 0,0
func (hid *HID) MovePointer(x, y int) {
	hid.gr.updateWindowPositionData()
	x = hid.gr.WindowLeftX + x
	y = hid.gr.WindowTopY + y

	hid.gi.CursorPos(x, y)
	lParam := calculateLparam(x, y)
	win.SendMessage(hid.gr.HWND, win.WM_NCHITTEST, 0, lParam)
	win.SendMessage(hid.gr.HWND, win.WM_SETCURSOR, 0x000105A8, 0x2010001)
	win.PostMessage(hid.gr.HWND, win.WM_MOUSEMOVE, 0, lParam)
}

// Click just does a single mouse click at current pointer position.
// In-process SendMessageW via APC was tested 2026-04-20 11:14 and CRASHED
// D2R with 0xC0000005 ~10s after character entered game — likely Arxan's
// caller-context check on APC-originated SendMessage returns. Reverted to
// cross-process win.SendMessage until a safer in-process path is designed
// (e.g. inline hook + retaddr forge in D2R .text).
func (hid *HID) Click(btn MouseButton, x, y int) {
	livetrace.Get().Click(traceClickLabel(btn), x, y, "")
	hid.MovePointer(x, y)
	lParam := calculateLparam(x, y)
	buttonDown := uint32(win.WM_LBUTTONDOWN)
	buttonUp := uint32(win.WM_LBUTTONUP)
	if btn == RightButton {
		buttonDown = win.WM_RBUTTONDOWN
		buttonUp = win.WM_RBUTTONUP
	}
	win.SendMessage(hid.gr.HWND, buttonDown, 1, lParam)
	sleepTime := rand.Intn(keyPressMaxTime-keyPressMinTime) + keyPressMinTime
	time.Sleep(time.Duration(sleepTime) * time.Millisecond)
	win.SendMessage(hid.gr.HWND, buttonUp, 1, lParam)
}

func (hid *HID) ClickWithModifier(btn MouseButton, x, y int, modifier ModifierKey) {
	modLabel := "Ctrl"
	if modifier == ShiftKey {
		modLabel = "Shift"
	}
	livetrace.Get().Click(traceClickLabel(btn), x, y, modLabel)
	hid.gi.OverrideGetKeyState(byte(modifier))
	hid.Click(btn, x, y)
	hid.gi.RestoreGetKeyState()
}

func calculateLparam(x, y int) uintptr {
	return uintptr(y<<16 | x)
}
