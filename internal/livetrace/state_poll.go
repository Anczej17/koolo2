package livetrace

import (
	"context"
	"fmt"
	"time"
)

// Snapshot is a flat key/value map of state fields observed at one tick.
// StatePoller diffs successive snapshots and emits one StateDiff event
// per changed key. Keys should be stable strings ("area", "hp_percent",
// "open_stash") because log consumers may filter by them.
type Snapshot map[string]any

// StartStatePoll launches a background goroutine that calls snapFn at
// the given interval, compares with the previous snapshot, and emits a
// StateDiff event per changed field. Cancel ctx to stop the loop.
//
// interval is clamped to >= 200ms so the poll cannot dominate D2R's
// thread scheduling. snapFn should read from an in-process cache (e.g.
// `ctx.Data` after RefreshGameData) — NEVER issue a new RPM per call:
// the poll must not widen the memory-read stealth surface.
func (t *Tracer) StartStatePoll(ctx context.Context, interval time.Duration, snapFn func() Snapshot) {
	if interval < 200*time.Millisecond {
		interval = 500 * time.Millisecond
	}
	go t.runStatePoll(ctx, interval, snapFn)
}

func (t *Tracer) runStatePoll(ctx context.Context, interval time.Duration, snapFn func() Snapshot) {
	var prev Snapshot
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !t.TraceStateDiffsEnabled() {
				continue
			}
			cur := snapFn()
			if cur == nil {
				continue
			}
			if prev == nil {
				prev = cur
				continue
			}
			// Emit per-field delta for any changed key.
			for k, v := range cur {
				if pv, ok := prev[k]; !ok || !equalValues(pv, v) {
					t.StateDiff(k, pv, v)
				}
			}
			// Disappeared keys — emit with to=nil so the viewer sees it.
			for k, pv := range prev {
				if _, ok := cur[k]; !ok {
					t.StateDiff(k, pv, nil)
				}
			}
			prev = cur
		}
	}
}

// equalValues compares two snapshot values without allocating reflect
// machinery. Covers the common types we embed (bool/int/int32/uint32/
// string). Falls back to fmt.Sprintf for anything exotic — acceptable
// because the poll runs at 2 Hz by default.
func equalValues(a, b any) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	switch av := a.(type) {
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	case int:
		bv, ok := b.(int)
		return ok && av == bv
	case int32:
		bv, ok := b.(int32)
		return ok && av == bv
	case uint32:
		bv, ok := b.(uint32)
		return ok && av == bv
	case int64:
		bv, ok := b.(int64)
		return ok && av == bv
	case uint64:
		bv, ok := b.(uint64)
		return ok && av == bv
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	default:
		return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
	}
}
