package memory

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ReadTraceEntry captures a single ReadBytesFromMemory call. Used by the
// `/debug/read-trace` endpoint + `/debug/read-trace-stats` to triage what
// the bot is reading, where, how it was served, and whether a ROP / RPM
// fallback fired. Added per Bartek's request 2026-04-18 after ROP_READ=1
// insta-crashed D2R — we couldn't see which read was the first fault.
//
// Kept intentionally small so the ring is cache-friendly; 64 bytes per
// entry, default ring size 512 = 32 KB per supervisor.
type ReadTraceEntry struct {
	Tick     uint64    // bump once per GetData tick — groups reads visually
	Timestamp time.Time
	Address  uintptr
	Size     uint32
	Source   ReadSource
	Result   ReadResult
	LatencyNs int64
}

type ReadSource uint8

const (
	ReadSourceNone  ReadSource = iota
	ReadSourceRPM              // stealth RPM via ntapi
	ReadSourceRPMKernel32      // legacy kernel32 RPM
	ReadSourceROP              // CMD_ROP_READ via presenter
	ReadSourceChunkCache       // L3 chunk cache hit
	ReadSourceSnapshot         // walker SHM snapshot
)

type ReadResult uint8

const (
	ReadResultOK        ReadResult = iota
	ReadResultFallback             // ROP failed, fell back to RPM
	ReadResultError                // RPM returned error
	ReadResultPartial              // short read
)

func (s ReadSource) String() string {
	switch s {
	case ReadSourceRPM:
		return "rpm"
	case ReadSourceRPMKernel32:
		return "rpm32"
	case ReadSourceROP:
		return "rop"
	case ReadSourceChunkCache:
		return "cache"
	case ReadSourceSnapshot:
		return "snap"
	}
	return "?"
}

func (r ReadResult) String() string {
	switch r {
	case ReadResultOK:
		return "ok"
	case ReadResultFallback:
		return "fallback"
	case ReadResultError:
		return "err"
	case ReadResultPartial:
		return "partial"
	}
	return "?"
}

type ReadTrace struct {
	mu      sync.Mutex
	ring    []ReadTraceEntry
	head    int
	tick    atomic.Uint64
	enabled atomic.Bool
	// Per-source counters. Cheap global tally so `/debug/read-trace-stats`
	// can report "ROP:1423 RPM:42 fallback:3" without walking the ring.
	counts [8]atomic.Uint64
}

func NewReadTrace(size int) *ReadTrace {
	if size <= 0 {
		size = 512
	}
	return &ReadTrace{ring: make([]ReadTraceEntry, size)}
}

// Enable starts / stops tracing. Off by default so production latency
// isn't paying the mutex + time.Now() cost.
func (t *ReadTrace) Enable(on bool) { t.enabled.Store(on) }
func (t *ReadTrace) Enabled() bool  { return t.enabled.Load() }

// BumpTick marks the start of a new per-tick group. Called by GetData.
func (t *ReadTrace) BumpTick() { t.tick.Add(1) }

// Record appends a new entry. O(1) amortised, lock-protected.
func (t *ReadTrace) Record(addr uintptr, size uint32, src ReadSource, res ReadResult, latency time.Duration) {
	if !t.enabled.Load() {
		return
	}
	t.counts[src].Add(1)
	if res == ReadResultFallback {
		t.counts[7].Add(1)
	}
	t.mu.Lock()
	t.ring[t.head] = ReadTraceEntry{
		Tick:      t.tick.Load(),
		Timestamp: time.Now(),
		Address:   addr,
		Size:      size,
		Source:    src,
		Result:    res,
		LatencyNs: latency.Nanoseconds(),
	}
	t.head = (t.head + 1) % len(t.ring)
	t.mu.Unlock()
}

// Dump returns the ring in chronological order (oldest first).
func (t *ReadTrace) Dump() []ReadTraceEntry {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]ReadTraceEntry, 0, len(t.ring))
	// Walk from head (oldest) forward.
	for i := 0; i < len(t.ring); i++ {
		e := t.ring[(t.head+i)%len(t.ring)]
		if e.Timestamp.IsZero() {
			continue
		}
		out = append(out, e)
	}
	return out
}

// Stats returns a compact human-readable counter summary.
func (t *ReadTrace) Stats() string {
	var b strings.Builder
	labels := []string{"none", "rpm", "rpm32", "rop", "cache", "snap", "?", "fallback"}
	for i := ReadSource(0); i < 8; i++ {
		c := t.counts[i].Load()
		if c == 0 {
			continue
		}
		fmt.Fprintf(&b, "%s=%d ", labels[i], c)
	}
	if b.Len() == 0 {
		return "(no reads)"
	}
	return strings.TrimSpace(b.String())
}
