package livetrace

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// Tracer streams per-event JSON lines to a log file. Meant to be tail -F'd
// in real time. One global instance per bot process; access via Get().
type Tracer struct {
	mu      sync.Mutex
	file    *os.File
	enabled atomic.Bool

	tracePackets    atomic.Bool
	traceClicks     atomic.Bool
	traceActions    atomic.Bool
	traceStateDiffs atomic.Bool
}

var (
	global     *Tracer
	globalOnce sync.Once
)

// Get returns the singleton tracer. Always returns a valid pointer; if
// Init has not run, emit calls are no-ops.
func Get() *Tracer {
	globalOnce.Do(func() { global = &Tracer{} })
	return global
}

// Init opens the trace file and enables sub-flags. Safe to call twice
// (second call closes the previous file and opens a new one).
func Init(path string, tracePackets, traceClicks, traceActions, traceStateDiffs bool) error {
	t := Get()
	t.mu.Lock()

	if t.file != nil {
		_ = t.file.Close()
		t.file = nil
	}

	if path == "" {
		path = filepath.Join("logs", "livetrace.log")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.mu.Unlock()
		return fmt.Errorf("livetrace: mkdir %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.mu.Unlock()
		return fmt.Errorf("livetrace: open %s: %w", path, err)
	}
	t.file = f
	t.enabled.Store(true)
	t.tracePackets.Store(tracePackets)
	t.traceClicks.Store(traceClicks)
	t.traceActions.Store(traceActions)
	t.traceStateDiffs.Store(traceStateDiffs)
	t.mu.Unlock()

	// Session banner — writeLine takes its own lock.
	t.writeLine(map[string]any{
		"ts":    time.Now().Format(time.RFC3339Nano),
		"kind":  "session",
		"pid":   os.Getpid(),
		"flags": map[string]bool{"packets": tracePackets, "clicks": traceClicks, "actions": traceActions, "stateDiffs": traceStateDiffs},
	})
	return nil
}

// IsEnabled returns true if any tracing is active.
func (t *Tracer) IsEnabled() bool { return t != nil && t.enabled.Load() }

// writeLine serialises a map to JSON line. Lock held by caller OR atomic flush.
func (t *Tracer) writeLine(m map[string]any) {
	if t == nil || t.file == nil {
		return
	}
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	t.mu.Lock()
	_, _ = t.file.Write(append(b, '\n'))
	_ = t.file.Sync()
	t.mu.Unlock()
}

// Packet records a SendPacket/SendUIPacket/SendDualPacket event.
// path = "ui" | "dual" | "game" | "game_gt" | ...
// errStr empty string means success.
func (t *Tracer) Packet(path string, opcode byte, payload []byte, errStr string, elapsedUs int64) {
	if !t.IsEnabled() || !t.tracePackets.Load() {
		return
	}
	head := payload
	if len(head) > 32 {
		head = head[:32]
	}
	t.writeLine(map[string]any{
		"ts":        time.Now().Format(time.RFC3339Nano),
		"kind":      "pkt",
		"path":      path,
		"op":        fmt.Sprintf("0x%02X", opcode),
		"len":       len(payload),
		"hex":       hex.EncodeToString(head),
		"err":       errStr,
		"elapsedUs": elapsedUs,
	})
}

// Click records a HID click / modifier click / press key event.
func (t *Tracer) Click(btn string, x, y int, modifier string) {
	if !t.IsEnabled() || !t.traceClicks.Load() {
		return
	}
	t.writeLine(map[string]any{
		"ts":       time.Now().Format(time.RFC3339Nano),
		"kind":     "click",
		"btn":      btn,
		"x":        x,
		"y":        y,
		"modifier": modifier,
	})
}

// Key records a HID key press.
func (t *Tracer) Key(vk byte, modifier string) {
	if !t.IsEnabled() || !t.traceClicks.Load() {
		return
	}
	t.writeLine(map[string]any{
		"ts":       time.Now().Format(time.RFC3339Nano),
		"kind":     "key",
		"vk":       fmt.Sprintf("0x%02X", vk),
		"modifier": modifier,
	})
}

// Action records a bot-side action boundary (start / result / warning).
// stage = "begin" | "ok" | "fail" | "note". Details is free-form.
func (t *Tracer) Action(name, stage string, details map[string]any) {
	if !t.IsEnabled() || !t.traceActions.Load() {
		return
	}
	m := map[string]any{
		"ts":    time.Now().Format(time.RFC3339Nano),
		"kind":  "act",
		"name":  name,
		"stage": stage,
	}
	for k, v := range details {
		m[k] = v
	}
	t.writeLine(m)
}

// StateDiff records a state snapshot delta (one field changed at a time).
func (t *Tracer) StateDiff(field string, from, to any) {
	if !t.IsEnabled() || !t.traceStateDiffs.Load() {
		return
	}
	t.writeLine(map[string]any{
		"ts":    time.Now().Format(time.RFC3339Nano),
		"kind":  "state",
		"field": field,
		"from":  from,
		"to":    to,
	})
}
