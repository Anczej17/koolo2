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
//
// When presenter is installed (rmod.dll injected via Claude mode or MODE2),
// the NCHITTEST + SETCURSOR + MOUSEMOVE sequence is posted via PostMessageW
// on D2R's OWN game thread — zero cross-process input surface. Falls back
// to cross-process win.SendMessage / win.PostMessage when presenter not
// available (pre-attach / MODE2 not enabled).
func (hid *HID) MovePointer(x, y int) {
	hid.gr.updateWindowPositionData()
	x = hid.gr.WindowLeftX + x
	y = hid.gr.WindowTopY + y

	hid.gi.CursorPos(x, y)

	if hid.gi.HasInProcessMsgPath() {
		if err := hid.gi.PostMouseMoveInProcess(uintptr(hid.gr.HWND), int32(x), int32(y)); err == nil {
			return
		}
		// Fallthrough to cross-process on error.
	}

	lParam := calculateLparam(x, y)
	win.SendMessage(hid.gr.HWND, win.WM_NCHITTEST, 0, lParam)
	win.SendMessage(hid.gr.HWND, win.WM_SETCURSOR, 0x000105A8, 0x2010001)
	win.PostMessage(hid.gr.HWND, win.WM_MOUSEMOVE, 0, lParam)
}

// Click does a single mouse click at (x, y).
//
// When presenter is installed, uses PostClickInProcess — zero cross-process
// surface, PostMessageW via APC on D2R's own game thread. The full NCHITTEST
// + SETCURSOR + MOUSEMOVE + BUTTONDOWN + BUTTONUP sequence matches the
// historical cross-process path.
//
// Previous attempt at in-process SendMessageW via APC (2026-04-20) crashed
// D2R — Arxan's caller-context check on APC-originated SendMessage returns.
// PostMessageW is safe because the message is queued, not synchronously
// invoked.
//
// Falls back to cross-process win.SendMessage when presenter not available.
func (hid *HID) Click(btn MouseButton, x, y int) {
	livetrace.Get().Click(traceClickLabel(btn), x, y, "")

	if hid.gi.HasInProcessMsgPath() {
		hid.gr.updateWindowPositionData()
		screenX := int32(hid.gr.WindowLeftX + x)
		screenY := int32(hid.gr.WindowTopY + y)
		var btnByte byte
		if btn == RightButton {
			btnByte = 1
		}
		if err := hid.gi.PostClickInProcess(uintptr(hid.gr.HWND), screenX, screenY, btnByte); err == nil {
			return
		}
		// Fallthrough to cross-process on error.
	}

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
