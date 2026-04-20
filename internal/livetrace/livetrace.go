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

// Tracer streams per-event JSON lines to a log file. Async buffered emit:
// call sites are non-blocking; one consumer goroutine serialises to disk.
// Rate-cap protects D2R against emit floods (a retry loop could otherwise
// dump hundreds of log lines per second and starve the render thread —
// see memory feedback_zombie_killer_logging_flood).
//
// Tail -F the file for real-time stream. No per-line file.Sync: OS flush
// cadence is fine and spares the hot path.
type Tracer struct {
	mu      sync.Mutex
	file    *os.File
	enabled atomic.Bool

	tracePackets    atomic.Bool
	traceClicks     atomic.Bool
	traceActions    atomic.Bool
	traceStateDiffs atomic.Bool

	events     chan map[string]any
	stopSignal chan struct{}
	consumerWg sync.WaitGroup

	maxEventsPerSec atomic.Int64 // 0 = unlimited
	eventsThisSec   atomic.Int64
	totalEvents     atomic.Uint64
	droppedEvents   atomic.Uint64

	// lastPacket stores a snapshot of the most recent outgoing packet for
	// crash-correlation. Crash() reads it and emits a combined event so
	// the log shows "D2R died Xms after 0x30 13B" without external join.
	lastPacket atomic.Value // holds *lastPacketInfo
	// lastAction stores the most recent action begin+name so Crash() can
	// pin the crash to a specific bot operation.
	lastAction atomic.Value // holds *lastActionInfo
}

type lastPacketInfo struct {
	ts   time.Time
	op   byte
	path string
	len  int
	hex  string // head up to 32 bytes
	err  string
}

type lastActionInfo struct {
	ts    time.Time
	name  string
	stage string
}

const (
	eventBufferCap       = 4096
	defaultMaxEventsPerS = 200
	statsBannerInterval  = 60 * time.Second
)

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

// Init opens the trace file, starts the consumer + rate-reset + stats
// goroutines, and stamps a session banner. maxEventsPerSec <= 0 means
// unlimited (not recommended in production). Safe to call twice — second
// call stops prior goroutines, closes prior file, and restarts.
func Init(path string, tracePackets, traceClicks, traceActions, traceStateDiffs bool, maxEventsPerSec int) error {
	t := Get()
	t.mu.Lock()

	// Stop prior background work, if any.
	if t.stopSignal != nil {
		close(t.stopSignal)
		t.stopSignal = nil
	}
	// Wait for prior consumer to drain before closing the file.
	t.mu.Unlock()
	t.consumerWg.Wait()
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

	if maxEventsPerSec <= 0 {
		t.maxEventsPerSec.Store(0)
	} else {
		t.maxEventsPerSec.Store(int64(maxEventsPerSec))
	}
	t.eventsThisSec.Store(0)
	t.totalEvents.Store(0)
	t.droppedEvents.Store(0)

	t.events = make(chan map[string]any, eventBufferCap)
	t.stopSignal = make(chan struct{})

	t.consumerWg.Add(1)
	go t.consumerLoop(t.events, t.stopSignal)
	go t.rateResetLoop(t.stopSignal)
	go t.statsBannerLoop(t.stopSignal)

	t.mu.Unlock()

	// Session banner via sync write so it's first line.
	t.syncWriteLine(map[string]any{
		"ts":              time.Now().Format(time.RFC3339Nano),
		"kind":            "session",
		"pid":             os.Getpid(),
		"flags":           map[string]bool{"packets": tracePackets, "clicks": traceClicks, "actions": traceActions, "stateDiffs": traceStateDiffs},
		"maxEventsPerSec": maxEventsPerSec,
		"eventBufferCap":  eventBufferCap,
	})
	return nil
}

// IsEnabled returns true if tracing was initialised and is active.
func (t *Tracer) IsEnabled() bool { return t != nil && t.enabled.Load() }

// emit queues an event to the consumer goroutine. Non-blocking — drops on
// a full channel or a rate-cap overflow. Callers must treat emit as
// best-effort; no return value.
func (t *Tracer) emit(m map[string]any) {
	if t == nil || !t.enabled.Load() {
		return
	}
	if cap := t.maxEventsPerSec.Load(); cap > 0 {
		if t.eventsThisSec.Add(1) > cap {
			t.droppedEvents.Add(1)
			return
		}
	} else {
		t.eventsThisSec.Add(1)
	}
	select {
	case t.events <- m:
		t.totalEvents.Add(1)
	default:
		// Consumer backed up; drop silently.
		t.droppedEvents.Add(1)
	}
}

// syncWriteLine writes a single JSON line directly to the file under the
// file lock. Used for session banner + periodic stats + drained-on-close
// tail. Hot-path events go through emit().
func (t *Tracer) syncWriteLine(m map[string]any) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.file == nil {
		return
	}
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	_, _ = t.file.Write(append(b, '\n'))
}

// consumerLoop drains the events channel to disk. Single writer, no
// contention with emit callers. Exits when stop is signalled and the
// channel has been drained.
func (t *Tracer) consumerLoop(ch <-chan map[string]any, stop <-chan struct{}) {
	defer t.consumerWg.Done()
	for {
		select {
		case <-stop:
			// Drain any remaining buffered events, then exit.
			for {
				select {
				case m := <-ch:
					t.syncWriteLine(m)
				default:
					return
				}
			}
		case m := <-ch:
			t.syncWriteLine(m)
		}
	}
}

// rateResetLoop resets the per-second counter every 1s so the leaky
// bucket refills.
func (t *Tracer) rateResetLoop(stop <-chan struct{}) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			t.eventsThisSec.Store(0)
		}
	}
}

// statsBannerLoop emits a session_stats event every 60s so a viewer can
// see the cumulative + dropped counts without tailing the whole file.
func (t *Tracer) statsBannerLoop(stop <-chan struct{}) {
	ticker := time.NewTicker(statsBannerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			t.syncWriteLine(map[string]any{
				"ts":        time.Now().Format(time.RFC3339Nano),
				"kind":      "session_stats",
				"total":     t.totalEvents.Load(),
				"dropped":   t.droppedEvents.Load(),
				"bufferCap": eventBufferCap,
			})
		}
	}
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
	hexStr := hex.EncodeToString(head)
	now := time.Now()
	// Record for crash correlation (always, even when pkt tracing is off
	// — cheap atomic store). Crash() reads this to build a corr event.
	t.lastPacket.Store(&lastPacketInfo{
		ts:   now,
		op:   opcode,
		path: path,
		len:  len(payload),
		hex:  hexStr,
		err:  errStr,
	})
	t.emit(map[string]any{
		"ts":        now.Format(time.RFC3339Nano),
		"kind":      "pkt",
		"path":      path,
		"op":        fmt.Sprintf("0x%02X", opcode),
		"len":       len(payload),
		"hex":       hexStr,
		"err":       errStr,
		"elapsedUs": elapsedUs,
	})
}

// Click records a HID click / modifier click.
func (t *Tracer) Click(btn string, x, y int, modifier string) {
	if !t.IsEnabled() || !t.traceClicks.Load() {
		return
	}
	t.emit(map[string]any{
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
	t.emit(map[string]any{
		"ts":       time.Now().Format(time.RFC3339Nano),
		"kind":     "key",
		"vk":       fmt.Sprintf("0x%02X", vk),
		"modifier": modifier,
	})
}

// Action records a bot-side action boundary (begin / ok / fail / note).
// Details is a free-form map merged into the event.
func (t *Tracer) Action(name, stage string, details map[string]any) {
	if !t.IsEnabled() || !t.traceActions.Load() {
		return
	}
	now := time.Now()
	// Always track last action for crash-correlation.
	t.lastAction.Store(&lastActionInfo{ts: now, name: name, stage: stage})
	m := map[string]any{
		"ts":    now.Format(time.RFC3339Nano),
		"kind":  "act",
		"name":  name,
		"stage": stage,
	}
	for k, v := range details {
		m[k] = v
	}
	t.emit(m)
}

// Crash records a fatal external event (D2R exit, supervisor panic, etc.)
// and pairs it with the most recent packet + action so the cause chain is
// visible in one line. Uses syncWriteLine so the crash event never gets
// dropped by the rate cap or async queue.
func (t *Tracer) Crash(reason string, pid uint32, exitCode uint32, extra map[string]any) {
	if t == nil || !t.enabled.Load() {
		return
	}
	now := time.Now()
	m := map[string]any{
		"ts":       now.Format(time.RFC3339Nano),
		"kind":     "crash",
		"reason":   reason,
		"pid":      pid,
		"exitCode": fmt.Sprintf("0x%X", exitCode),
	}
	if lp, ok := t.lastPacket.Load().(*lastPacketInfo); ok && lp != nil {
		m["lastPacketOp"] = fmt.Sprintf("0x%02X", lp.op)
		m["lastPacketPath"] = lp.path
		m["lastPacketLen"] = lp.len
		m["lastPacketHex"] = lp.hex
		m["msSinceLastPacket"] = now.Sub(lp.ts).Milliseconds()
	}
	if la, ok := t.lastAction.Load().(*lastActionInfo); ok && la != nil {
		m["lastActionName"] = la.name
		m["lastActionStage"] = la.stage
		m["msSinceLastAction"] = now.Sub(la.ts).Milliseconds()
	}
	for k, v := range extra {
		m[k] = v
	}
	t.syncWriteLine(m)
}

// StateDiff records a state snapshot delta (one field changed at a time).
func (t *Tracer) StateDiff(field string, from, to any) {
	if !t.IsEnabled() || !t.traceStateDiffs.Load() {
		return
	}
	t.emit(map[string]any{
		"ts":    time.Now().Format(time.RFC3339Nano),
		"kind":  "state",
		"field": field,
		"from":  from,
		"to":    to,
	})
}

// TraceStateDiffsEnabled exposes the stateDiffs sub-flag so callers can
// skip expensive snapshot work when the tracer is off.
func (t *Tracer) TraceStateDiffsEnabled() bool {
	return t != nil && t.enabled.Load() && t.traceStateDiffs.Load()
}
