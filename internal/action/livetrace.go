package action

import (
	"time"

	"local/internal/svc/internal/livetrace"
)

// WithTrace wraps a top-level action in begin/ok|fail livetrace events.
// Use at the boundary of a user-visible action (e.g. MoveToArea,
// InteractNPC, StashItem) — NEVER inside a retry loop. Retry loops should
// log on the outer call attempt, not per-iteration.
//
// If livetrace is not active, this is a thin wrapper with no measurable
// overhead: the Action emit returns immediately after the atomic flag
// check.
func WithTrace(name string, fn func() error) error {
	lt := livetrace.Get()
	start := time.Now()
	lt.Action(name, "begin", nil)
	err := fn()
	details := map[string]any{
		"elapsedMs": time.Since(start).Milliseconds(),
	}
	if err != nil {
		details["err"] = err.Error()
		lt.Action(name, "fail", details)
	} else {
		lt.Action(name, "ok", details)
	}
	return err
}

// deferredTrace is the defer-friendly sibling of WithTrace for functions
// that are hard to refactor into a single-body closure (long existing
// bodies with early returns using named error). Usage:
//
//	func MoveToArea(dst area.ID) (err error) {
//	    defer deferredTrace("MoveToArea:"+dst.Name(), &err)()
//	    // existing body; named return auto-propagates to the deferred
//	    // emitter.
//	}
//
// The function returned must be called (note the `()` at end). errPtr may
// be nil for void actions — then fail/ok is decided solely by a sentinel.
func deferredTrace(name string, errPtr *error) func() {
	start := time.Now()
	livetrace.Get().Action(name, "begin", nil)
	return func() {
		details := map[string]any{
			"elapsedMs": time.Since(start).Milliseconds(),
		}
		if errPtr != nil && *errPtr != nil {
			details["err"] = (*errPtr).Error()
			livetrace.Get().Action(name, "fail", details)
		} else {
			livetrace.Get().Action(name, "ok", details)
		}
	}
}
