package event

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"os"
	"sync"
	"time"

	"github.com/hectorgimenez/koolo/internal/config"
	"github.com/hectorgimenez/koolo/internal/utils"
)

// Buffered channel prevents event.Send() from blocking supervisor goroutines
// when the listener is busy processing handlers.
var events = make(chan Event, 128)

type Listener struct {
	mu            sync.RWMutex
	handlers      []Handler            // global handlers (registered once at startup)
	keyedHandlers map[string][]Handler // per-supervisor handlers (keyed by supervisor name)
	DropHandlers  map[int]Handler
	logger        *slog.Logger
}

type Handler func(ctx context.Context, e Event) error

func NewListener(logger *slog.Logger) *Listener {
	return &Listener{
		logger:        logger,
		keyedHandlers: make(map[string][]Handler),
		DropHandlers:  make(map[int]Handler),
	}
}

// Register adds a global handler that persists for the lifetime of the application.
// Use RegisterKeyed for per-supervisor handlers that need cleanup.
func (l *Listener) Register(h Handler) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.handlers = append(l.handlers, h)
}

// RegisterKeyed adds a handler associated with a key (typically supervisor name).
// Calling RegisterKeyed with the same key replaces all previous handlers for that key,
// preventing handler accumulation across supervisor restarts.
func (l *Listener) RegisterKeyed(key string, handlers ...Handler) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.keyedHandlers[key] = handlers
}

// UnregisterKeyed removes all handlers associated with the given key.
func (l *Listener) UnregisterKeyed(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.keyedHandlers, key)
}

func (l *Listener) Listen(ctx context.Context) error {
	for {
		select {
		case e := <-events:
			if _, err := os.Stat("screenshots"); os.IsNotExist(err) {
				err = os.MkdirAll("screenshots", os.ModePerm)
				if err != nil {
					l.logger.Error("error creating screenshots directory", slog.Any("error", err))
				}
			}

			if e.Image() != nil && config.Koolo.Debug.Screenshots {
				fileName := fmt.Sprintf("screenshots/error-%s.jpeg", time.Now().Format("2006-01-02 15_04_05"))
				err := utils.SaveImageJPEG(e.Image(), fileName)
				if err != nil {
					l.logger.Error("error saving screenshot", slog.Any("error", err))
				}
			}

			// Snapshot handlers under read lock to minimize lock duration
			l.mu.RLock()
			globalHandlers := make([]Handler, len(l.handlers))
			copy(globalHandlers, l.handlers)
			var keyedHandlers []Handler
			for _, hList := range l.keyedHandlers {
				keyedHandlers = append(keyedHandlers, hList...)
			}
			dropHandlers := make([]Handler, 0, len(l.DropHandlers))
			for _, h := range l.DropHandlers {
				dropHandlers = append(dropHandlers, h)
			}
			l.mu.RUnlock()

			for _, h := range globalHandlers {
				if err := h(ctx, e); err != nil && e.Message() != "" {
					l.logger.Error("error running event handler", slog.Any("error", err))
				}
			}
			for _, h := range keyedHandlers {
				if err := h(ctx, e); err != nil && e.Message() != "" {
					l.logger.Error("error running event handler", slog.Any("error", err))
				}
			}
			for _, h := range dropHandlers {
				if err := h(ctx, e); err != nil {
					l.logger.Error("error running event Drop handler", slog.Any("error", err))
				}
			}

		case <-ctx.Done():
			return nil
		}
	}
}

func (l *Listener) WaitForEvent(ctx context.Context) Event {
	evtChan := make(chan Event)
	idx := rand.Intn(math.MaxInt64)
	l.mu.Lock()
	l.DropHandlers[idx] = func(ctx context.Context, e Event) error {
		evtChan <- e
		return nil
	}
	l.mu.Unlock()
	// Clean up the handler when we're done
	defer func() {
		l.mu.Lock()
		delete(l.DropHandlers, idx)
		l.mu.Unlock()
	}()

	for {
		select {
		case e := <-evtChan:
			return e
		case <-ctx.Done():
			return nil
		}
	}
}

// Send publishes an event to all registered handlers.
// Uses a short timeout to avoid blocking supervisor goroutines indefinitely
// while still delivering events reliably (no silent drops).
func Send(e Event) {
	select {
	case events <- e:
	default:
		// Buffer full — wait up to 5s before dropping. This keeps supervisor
		// goroutines responsive while avoiding silent event loss.
		select {
		case events <- e:
		case <-time.After(5 * time.Second):
			slog.Warn("Event buffer full for 5s, dropping event", slog.String("message", e.Message()))
		}
	}
}
