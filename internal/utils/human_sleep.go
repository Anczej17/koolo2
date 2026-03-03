package utils

import (
	"math/rand"
	"time"
)

// Jitter returns a jittered duration: base ± pct%.
func Jitter(ms int, pct int) time.Duration {
	if ms <= 0 {
		return 0
	}
	factor := float64(pct) / 100.0
	minMs := int(float64(ms) * (1.0 - factor))
	maxMs := int(float64(ms) * (1.0 + factor))
	if minMs < 1 {
		minMs = 1
	}
	if maxMs < minMs {
		maxMs = minMs
	}
	return time.Duration(RandRng(minMs, maxMs)) * time.Millisecond
}

// CombatSleep sleeps for ms ± 15%. Used for attacks and skill casting.
func CombatSleep(ms int) {
	time.Sleep(Jitter(ms, 15))
}

// HumanSleep sleeps for ms ± 30%, with a 15% chance of an extra 10-50ms micro-pause.
// Used for NPC interactions, portals, and general interactions.
func HumanSleep(ms int) {
	d := Jitter(ms, 30)
	// 15% chance of a micro-pause
	if rand.Intn(100) < 15 {
		d += time.Duration(RandRng(10, 50)) * time.Millisecond
	}
	time.Sleep(d)
}

// TownSleep sleeps for ms ± 30%, with a 15% chance of a micro-pause (10-50ms)
// and an 8% chance of a longer pause (500-2000ms).
// Used for gambling, stash, repair, and other town activities.
func TownSleep(ms int) {
	d := Jitter(ms, 30)
	// 15% chance of a micro-pause
	if rand.Intn(100) < 15 {
		d += time.Duration(RandRng(10, 50)) * time.Millisecond
	}
	// 8% chance of a longer "distraction" pause
	if rand.Intn(100) < 8 {
		d += time.Duration(RandRng(500, 2000)) * time.Millisecond
	}
	time.Sleep(d)
}
