package game

import (
	"log"
	"runtime"
	"sync/atomic"
)

type HID struct {
	gr *MemoryReader
	gi *MemoryInjector

	disabled       atomic.Bool
	disabledReason atomic.Value
	blockedCount   atomic.Uint64
}

func NewHID(gr *MemoryReader, gi *MemoryInjector) *HID {
	hid := &HID{
		gr: gr,
		gi: gi,
	}
	hid.disabledReason.Store("")
	return hid
}

func (hid *HID) Disable(reason string) {
	if hid == nil {
		return
	}
	if reason == "" {
		reason = "disabled"
	}
	hid.disabledReason.Store(reason)
	hid.blockedCount.Store(0)
	if !hid.disabled.Swap(true) {
		log.Printf("[HID] disabled: %s", reason)
	}
}

func (hid *HID) Enable(reason string) {
	if hid == nil {
		return
	}
	if hid.disabled.Swap(false) {
		log.Printf("[HID] enabled: %s blockedWhileDisabled=%d", reason, hid.blockedCount.Load())
	}
}

func (hid *HID) IsDisabled() bool {
	if hid == nil {
		return false
	}
	return hid.disabled.Load()
}

func (hid *HID) BlockedCount() uint64 {
	if hid == nil {
		return 0
	}
	return hid.blockedCount.Load()
}

func (hid *HID) guard(action string) bool {
	if hid == nil {
		log.Printf("[HID] blocked action=%s reason=nil-hid", action)
		return false
	}
	if !hid.disabled.Load() {
		return true
	}
	reason, _ := hid.disabledReason.Load().(string)
	n := hid.blockedCount.Add(1)
	_, file, line, ok := runtime.Caller(2)
	if ok {
		log.Printf("[HID] blocked action=%s caller=%s:%d reason=%q blockedWhileDisabled=%d", action, file, line, reason, n)
	} else {
		log.Printf("[HID] blocked action=%s reason=%q blockedWhileDisabled=%d", action, reason, n)
	}
	return false
}
