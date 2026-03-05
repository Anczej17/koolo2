package game

import (
	"math"
	"math/rand"
	"time"

	"github.com/hectorgimenez/koolo/internal/utils"
	"github.com/lxn/win"
)

const (
	RightButton MouseButton = win.MK_RBUTTON
	LeftButton  MouseButton = win.MK_LBUTTON

	ShiftKey ModifierKey = win.VK_SHIFT
	CtrlKey  ModifierKey = win.VK_CONTROL
)

type MouseButton uint
type ModifierKey byte

const pointerReleaseDelay = 150 * time.Millisecond

// MovePointer moves the mouse to (x, y) relative to the game window using a bio-realistic trajectory.
func (hid *HID) MovePointer(x, y int) {
	hid.gr.updateWindowPositionData()
	absX := hid.gr.WindowLeftX + x
	absY := hid.gr.WindowTopY + y
	minX := hid.gr.WindowLeftX
	minY := hid.gr.WindowTopY
	maxX := hid.gr.WindowLeftX + hid.gr.GameAreaSizeX
	maxY := hid.gr.WindowTopY + hid.gr.GameAreaSizeY

	absX = clampInt(absX, minX, maxX)
	absY = clampInt(absY, minY, maxY)

	if !hid.gi.CursorOverrideActive() {
		// Fallback: direct move without animation
		hid.gi.CursorPos(absX, absY)
		lParam := calculateLparam(absX, absY)
		win.SendMessage(hid.gr.HWND, win.WM_NCHITTEST, 0, lParam)
		win.SendMessage(hid.gr.HWND, win.WM_SETCURSOR, 0x000105A8, 0x2010001)
		win.PostMessage(hid.gr.HWND, win.WM_MOUSEMOVE, 0, lParam)
		return
	}

	startX, startY, ok := hid.gi.LastCursorPos()
	if !ok {
		// No prior position: jump directly on first move
		hid.gi.CursorPos(absX, absY)
		lParam := calculateLparam(absX, absY)
		win.SendMessage(hid.gr.HWND, win.WM_NCHITTEST, 0, lParam)
		win.SendMessage(hid.gr.HWND, win.WM_SETCURSOR, 0x000105A8, 0x2010001)
		win.PostMessage(hid.gr.HWND, win.WM_MOUSEMOVE, 0, lParam)
		return
	}

	// Generate SigmaDrift trajectory in absolute screen coordinates
	path := bioMotionPath(float64(startX), float64(startY), float64(absX), float64(absY), defaultSDConfig)

	// Play back intermediate points with gamma-distributed timing
	for i := 0; i+1 < len(path); i++ {
		if !hid.gi.CursorOverrideActive() {
			return
		}
		pt := path[i]
		px := clampInt(int(math.Round(pt.x)), minX, maxX)
		py := clampInt(int(math.Round(pt.y)), minY, maxY)
		hid.gi.CursorPos(px, py)
		win.PostMessage(hid.gr.HWND, win.WM_MOUSEMOVE, 0, calculateLparam(px, py))
		if dt := path[i+1].t - pt.t; dt > 0 {
			time.Sleep(time.Duration(dt) * time.Millisecond)
		}
	}

	if !hid.gi.CursorOverrideActive() {
		return
	}

	// Micro-correction: briefly re-aim near target, probability scales with distance
	dist := math.Hypot(float64(absX-startX), float64(absY-startY))
	var microCorrProb float64
	switch {
	case dist < 30:
		microCorrProb = 0.04
	case dist < 200:
		microCorrProb = 0.12
	default:
		microCorrProb = 0.25
	}
	if rand.Float64() < microCorrProb {
		mx := clampInt(absX+rand.Intn(11)-5, minX, maxX)
		my := clampInt(absY+rand.Intn(11)-5, minY, maxY)
		hid.gi.CursorPos(mx, my)
		win.PostMessage(hid.gr.HWND, win.WM_MOUSEMOVE, 0, calculateLparam(mx, my))
		time.Sleep(time.Duration(rand.Intn(40)+15) * time.Millisecond)
	}

	// Finalize at exact target
	hid.gi.CursorPos(absX, absY)
	lParam := calculateLparam(absX, absY)
	win.SendMessage(hid.gr.HWND, win.WM_NCHITTEST, 0, lParam)
	win.SendMessage(hid.gr.HWND, win.WM_SETCURSOR, 0x000105A8, 0x2010001)
	win.PostMessage(hid.gr.HWND, win.WM_MOUSEMOVE, 0, lParam)
}

// Click performs a single mouse click at (x, y) with gamma-distributed hold duration.
func (hid *HID) Click(btn MouseButton, x, y int) {
	hid.MovePointer(x, y)
	x = hid.gr.WindowLeftX + x
	y = hid.gr.WindowTopY + y

	lParam := calculateLparam(x, y)
	buttonDown := uint32(win.WM_LBUTTONDOWN)
	buttonUp := uint32(win.WM_LBUTTONUP)
	if btn == RightButton {
		buttonDown = win.WM_RBUTTONDOWN
		buttonUp = win.WM_RBUTTONUP
	}

	win.SendMessage(hid.gr.HWND, buttonDown, 1, lParam)
	sleepTime := utils.GammaDurationMs(60.0, 3.0)
	if sleepTime < time.Duration(keyPressMinTime)*time.Millisecond {
		sleepTime = time.Duration(keyPressMinTime) * time.Millisecond
	} else if sleepTime > time.Duration(keyPressMaxTime)*time.Millisecond {
		sleepTime = time.Duration(keyPressMaxTime) * time.Millisecond
	}
	time.Sleep(sleepTime)
	win.SendMessage(hid.gr.HWND, buttonUp, 1, lParam)
}

func (hid *HID) ClickWithModifier(btn MouseButton, x, y int, modifier ModifierKey) {
	hid.gi.OverrideGetKeyState(byte(modifier))
	hid.Click(btn, x, y)
	hid.gi.RestoreGetKeyState()
}

func calculateLparam(x, y int) uintptr {
	return uintptr((y&0xFFFF)<<16 | (x & 0xFFFF))
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
