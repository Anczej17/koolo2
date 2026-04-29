package memory

import (
	"crypto/rand"
	"encoding/binary"
	mrand "math/rand"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// stealth.go — Stealth RPM Layer utilities.
//
// Central policy + primitives for the Stealth RPM Layer described in
// docs/archive/STEALTH_RPM_PLAN.md. All layers gate on stealthEnabled() so the code
// falls back to original (upstream-parity) behavior when STEALTH_READ
// is unset or != "1".

// stealthOnce memoises env flag read on first access.
var (
	stealthOnce  sync.Once
	stealthFlag  atomic.Bool
	stealthTrace atomic.Bool
)

func initStealthFlags() {
	stealthOnce.Do(func() {
		// Stealth RPM defaults OFF — historically the stealth layer's chaff
		// reader + chunk-cache NtQueryVirtualMemory races D2R's early-attach
		// page layout and trips Arxan SEH → app.exe SIGSEGV before game entry.
		// Live-reproduced 2026-04-21 17:07: direct .exe click (no env) =
		// segfault at MemoryInjector.Load; adding STEALTH_READ=0 (or this new
		// default) = clean boot to char-select and beyond.
		//
		// Opt-in via STEALTH_READ=1 once the chaff-vs-init race is fixed
		// (see project_stealth_rpm_layer_2026_04_15 + feedback-loop work).
		stealthFlag.Store(os.Getenv("STEALTH_READ") == "1")
		stealthTrace.Store(os.Getenv("STEALTH_TRACE") == "1")
	})
}

// StealthEnabled returns true only when STEALTH_READ=1 was set at startup.
// Current default is OFF for stability during early attach; future work can
// flip this back to opt-out once the startup race is resolved and live-verified.
func StealthEnabled() bool {
	initStealthFlags()
	return stealthFlag.Load()
}

// StealthTraceEnabled returns true if STEALTH_TRACE=1. Diagnostic only —
// enables RPM-trace recording for before/after pattern comparison.
func StealthTraceEnabled() bool {
	initStealthFlags()
	return stealthTrace.Load()
}

// ----------------------------------------------------------------------
// RNG — cryptographic fallback to math/rand (non-blocking, fast).
// ----------------------------------------------------------------------

// cryptRand64 returns a random uint64 from crypto/rand. On extreme rare
// failure falls back to math/rand seeded with time.Now().UnixNano() XOR'd
// with prior sample to avoid deterministic patterns.
func cryptRand64() uint64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err == nil {
		return binary.LittleEndian.Uint64(b[:])
	}
	return uint64(mrand.Int63())
}

// cryptRandN returns uniform int in [0, n). n must be > 0.
func cryptRandN(n int) int {
	if n <= 1 {
		return 0
	}
	return int(cryptRand64() % uint64(n))
}

// ----------------------------------------------------------------------
// Shuffle helpers
// ----------------------------------------------------------------------

// ShufflePermutation returns a random permutation of indices [0, n).
// Fisher-Yates using cryptographic RNG. Returns nil if n < 1.
func ShufflePermutation(n int) []int {
	if n < 1 {
		return nil
	}
	p := make([]int, n)
	for i := range p {
		p[i] = i
	}
	for i := n - 1; i > 0; i-- {
		j := cryptRandN(i + 1)
		p[i], p[j] = p[j], p[i]
	}
	return p
}

// ----------------------------------------------------------------------
// Jitter helpers
// ----------------------------------------------------------------------

// JitterDuration returns `base * (1 + rand in [-jitterPct, +jitterPct])`.
// jitterPct is expressed as fraction (0.15 == 15%). Result is always > 0.
// When stealth is disabled, returns base unchanged.
func JitterDuration(base time.Duration, jitterPct float64) time.Duration {
	if !StealthEnabled() || jitterPct <= 0 {
		return base
	}
	// Symmetric jitter: ±jitterPct. cryptRand64 mod 1e6 gives 0..999_999.
	// Center at 0, scale to ±jitterPct * base.
	r := int64(cryptRand64() % 2_000_001) // 0..2_000_000
	offset := r - 1_000_000               // -1_000_000..+1_000_000
	delta := time.Duration(float64(base) * jitterPct * float64(offset) / 1_000_000)
	result := base + delta
	if result < 1 {
		result = 1
	}
	return result
}

// ----------------------------------------------------------------------
// Chunk size randomization (Layer 3)
// ----------------------------------------------------------------------

// chunkSizes are valid randomized chunk read sizes. Page-aligned (0x1000)
// to reduce page-boundary mitigation cost. Range chosen so reads are big
// enough to cache-amortize but small enough to stay within typical VAD
// regions (D2R's .data section is tens of MB — never falls off for these).
var chunkSizes = []int{0x1000, 0x1400, 0x1800, 0x1C00, 0x2000}

// RandomChunkSize returns a randomized chunk size for Layer 3 BatchRead.
func RandomChunkSize() int {
	return chunkSizes[cryptRandN(len(chunkSizes))]
}

// ----------------------------------------------------------------------
// Layer 4: loop cadence jitter helpers
// ----------------------------------------------------------------------

// TickerInterval returns a duration suitable for paced game-data refresh
// loops. With stealth ON, returns `base ± 15%` jitter; stealth OFF
// returns `base` verbatim.
//
// Use as `d := memory.TickerInterval(100*time.Millisecond); <-time.After(d)`
// to replace `time.NewTicker(base).C` with a jittered pulse. Breaks fixed
// cadence fingerprints from kernel ETW/process-tracker that Warden-class
// scanners use to spot bot-paced read rhythms.
func TickerInterval(base time.Duration) time.Duration {
	return JitterDuration(base, 0.15)
}
