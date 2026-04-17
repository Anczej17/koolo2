package memory

import (
	"os"
	"sync"
	"testing"
	"time"
)

func TestStealthEnabled_Default(t *testing.T) {
	// Reset once
	stealthOnce = sync.Once{}
	os.Unsetenv("STEALTH_READ")
	if !StealthEnabled() {
		t.Fatal("expected stealth ON by default (opt-out via STEALTH_READ=0)")
	}
}

func TestStealthEnabled_Off(t *testing.T) {
	stealthOnce = sync.Once{}
	os.Setenv("STEALTH_READ", "0")
	defer os.Unsetenv("STEALTH_READ")
	if StealthEnabled() {
		t.Fatal("expected stealth OFF with env=0")
	}
}

func TestStealthEnabled_On(t *testing.T) {
	stealthOnce = sync.Once{}
	os.Setenv("STEALTH_READ", "1")
	defer os.Unsetenv("STEALTH_READ")
	if !StealthEnabled() {
		t.Fatal("expected stealth ON with env=1")
	}
}

func TestShufflePermutation_Uniqueness(t *testing.T) {
	// 8 shuffles of 7 elements — very low probability of 2 identical
	seen := map[string]bool{}
	for i := 0; i < 8; i++ {
		p := ShufflePermutation(7)
		if len(p) != 7 {
			t.Fatalf("expected len 7, got %d", len(p))
		}
		// Contains all indices 0..6
		have := make(map[int]bool)
		for _, v := range p {
			have[v] = true
		}
		for j := 0; j < 7; j++ {
			if !have[j] {
				t.Fatalf("permutation missing index %d: %v", j, p)
			}
		}
		k := ""
		for _, v := range p {
			k += string(rune('0' + v))
		}
		seen[k] = true
	}
	if len(seen) < 4 {
		t.Fatalf("expected ≥4 unique permutations of 7, got %d", len(seen))
	}
}

func TestShufflePermutation_EdgeCases(t *testing.T) {
	if ShufflePermutation(0) != nil {
		t.Fatal("expected nil for n=0")
	}
	p := ShufflePermutation(1)
	if len(p) != 1 || p[0] != 0 {
		t.Fatalf("expected [0], got %v", p)
	}
}

func TestJitterDuration_StealthOff(t *testing.T) {
	stealthOnce = sync.Once{}
	os.Setenv("STEALTH_READ", "0")
	defer os.Unsetenv("STEALTH_READ")
	base := 100 * time.Millisecond
	d := JitterDuration(base, 0.15)
	if d != base {
		t.Fatalf("stealth off: expected base %v, got %v", base, d)
	}
}

func TestJitterDuration_StealthOn(t *testing.T) {
	stealthOnce = sync.Once{}
	os.Setenv("STEALTH_READ", "1")
	defer os.Unsetenv("STEALTH_READ")
	base := 100 * time.Millisecond
	// Run many times, verify distribution
	min, max := time.Duration(1<<62), time.Duration(0)
	for i := 0; i < 500; i++ {
		d := JitterDuration(base, 0.15)
		if d < min {
			min = d
		}
		if d > max {
			max = d
		}
	}
	// Expect min < 95ms and max > 105ms (wider than trivial noise)
	if min >= 95*time.Millisecond || max <= 105*time.Millisecond {
		t.Fatalf("jitter too narrow: min=%v max=%v", min, max)
	}
	// But stay within ±20% (15% target + slack)
	if min < 80*time.Millisecond || max > 120*time.Millisecond {
		t.Fatalf("jitter too wide: min=%v max=%v", min, max)
	}
}

func TestDispatchOrder_StealthOff(t *testing.T) {
	stealthOnce = sync.Once{}
	os.Setenv("STEALTH_READ", "0")
	defer os.Unsetenv("STEALTH_READ")
	for n := 1; n <= 20; n++ {
		order := dispatchOrder(n)
		if len(order) != n {
			t.Fatalf("n=%d: expected len %d, got %d", n, n, len(order))
		}
		for i, v := range order {
			if i != v {
				t.Fatalf("n=%d stealth off: expected identity, got %v at idx %d", n, v, i)
			}
		}
	}
}

func TestDispatchOrder_StealthOn_Shuffles(t *testing.T) {
	stealthOnce = sync.Once{}
	os.Setenv("STEALTH_READ", "1")
	defer os.Unsetenv("STEALTH_READ")

	n := 16
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		order := dispatchOrder(n)
		if len(order) != n {
			t.Fatalf("expected len %d, got %d", n, len(order))
		}
		// Contains all indices
		have := make(map[int]bool, n)
		for _, v := range order {
			have[v] = true
		}
		for j := 0; j < n; j++ {
			if !have[j] {
				t.Fatalf("missing idx %d: %v", j, order)
			}
		}
		// Encode order as string key
		key := ""
		for _, v := range order {
			key += string(rune('A' + v))
		}
		seen[key] = true
	}
	if len(seen) < 15 {
		t.Fatalf("expected ≥15 distinct orders in 20 shuffles, got %d", len(seen))
	}
}

// TestChunkCache_HitAndMiss verifies L3 chunk cache semantics WITHOUT issuing
// real cross-process RPMs — we prepopulate chunks directly and assert lookup
// behavior. Full RPM path is exercised in integration tests against D2R.
func TestChunkCache_HitAndMiss(t *testing.T) {
	p := &Process{}
	stealthOnce = sync.Once{}
	os.Setenv("STEALTH_READ", "1")
	defer os.Unsetenv("STEALTH_READ")

	// Prepopulate a 4KB chunk at 0x1000 containing a known pattern.
	p.chunkCache = map[uintptr][]byte{
		0x1000: make([]byte, 0x1000),
	}
	for i := 0; i < 0x1000; i++ {
		p.chunkCache[0x1000][i] = byte(i & 0xFF)
	}

	// Hit: read 8 bytes at offset 0x10 — should match the prepopulated pattern.
	got, hit := p.chunkLookup(0x1010, 8)
	if !hit {
		t.Fatal("expected cache hit at 0x1010+8")
	}
	for i, b := range got {
		if b != byte((0x10+i)&0xFF) {
			t.Errorf("byte %d: expected 0x%02x, got 0x%02x", i, 0x10+i, b)
		}
	}

	// Hit at chunk boundary: read 4 bytes at offset 0xFFC (last 4 bytes)
	got, hit = p.chunkLookup(0x1FFC, 4)
	if !hit {
		t.Fatal("expected cache hit at chunk tail")
	}
	if len(got) != 4 {
		t.Fatalf("expected 4 bytes, got %d", len(got))
	}

	// Straddling chunk end: read 8 bytes at 0x1FFC (4 in + 4 outside) — cache
	// won't satisfy (reqEnd > chunkEnd); would need to load next chunk. Since
	// ntapi.ReadProcessMemory against p.handler=0 will fail, expect (nil, false).
	_, hit = p.chunkLookup(0x1FFC, 8)
	if hit {
		t.Fatal("expected miss on straddling read (no next chunk cached, RPM will fail)")
	}
}

func TestFlushChunkCache(t *testing.T) {
	p := &Process{}
	p.chunkCache = map[uintptr][]byte{
		0x1000: make([]byte, 0x1000),
		0x2000: make([]byte, 0x1000),
	}
	p.FlushChunkCache()
	if p.chunkCache != nil {
		t.Fatalf("expected chunkCache nil after flush, got %v keys", len(p.chunkCache))
	}
}

func TestRandomChunkSize_InRange(t *testing.T) {
	seen := map[int]bool{}
	for i := 0; i < 50; i++ {
		s := RandomChunkSize()
		if s < 0x1000 || s > 0x2000 {
			t.Fatalf("chunk size out of range: %x", s)
		}
		if s%0x400 != 0 {
			t.Fatalf("chunk size not 1K-aligned: %x", s)
		}
		seen[s] = true
	}
	// Should see >= 3 of 5 variants in 50 draws
	if len(seen) < 3 {
		t.Fatalf("expected ≥3 distinct chunk sizes, got %d: %v", len(seen), seen)
	}
}
