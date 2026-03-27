package main

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// TickerEvent is a single event in the scrolling ticker feed.
type TickerEvent struct {
	Time   string `json:"time"`
	Type   string `json:"type"`             // "svc", "docker", "sync", "daemon", "peer", "disk", "chezmoi", "nats"
	Level  string `json:"level"`            // "info", "warn", "error"
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
}

// TickerBus is a thread-safe ring buffer of ticker events.
// Optionally publishes each event to NATS for fleet-wide visibility.
type TickerBus struct {
	mu     sync.RWMutex
	events []TickerEvent
	max    int
	nats   *NATSPublisher
}

func newTickerBus(max int, np *NATSPublisher) *TickerBus {
	return &TickerBus{max: max, events: make([]TickerEvent, 0, max), nats: np}
}

// Push appends an event to the ring buffer and publishes to NATS.
func (tb *TickerBus) Push(typ, level, title, detail string) {
	now := time.Now().UTC().Format(time.RFC3339)
	tb.mu.Lock()
	tb.events = append(tb.events, TickerEvent{
		Time:   now,
		Type:   typ,
		Level:  level,
		Title:  title,
		Detail: detail,
	})
	if len(tb.events) > tb.max {
		tb.events = tb.events[len(tb.events)-tb.max:]
	}
	tb.mu.Unlock()

	// Publish to NATS so other fleet members can see gridwatch events.
	if tb.nats != nil {
		tb.nats.publish("fleet.gridwatch."+typ, FleetEvent{
			Type:    "gridwatch_" + typ,
			Machine: cfg.MachineName,
			Summary: title,
			Data:    "level=" + level + " " + detail,
		})
	}
}

// Events returns recent events (max 5 min old, min 30s old on first minute to skip seed burst), newest first.
func (tb *TickerBus) Events() []TickerEvent {
	tb.mu.RLock()
	defer tb.mu.RUnlock()

	now := time.Now().UTC()
	ttl := 5 * time.Minute

	// For the first 60 seconds of uptime, only show events from the last 30s.
	// This skips the seed burst (all services/docker/peers emitted on first poll).
	if len(tb.events) > 0 {
		if oldest, err := time.Parse(time.RFC3339, tb.events[0].Time); err == nil {
			if now.Sub(oldest) < 60*time.Second {
				ttl = 30 * time.Second
			}
		}
	}

	cutoff := now.Add(-ttl).Format(time.RFC3339)
	var out []TickerEvent
	for i := len(tb.events) - 1; i >= 0; i-- {
		if tb.events[i].Time >= cutoff {
			out = append(out, tb.events[i])
		}
	}
	return out
}

func (gw *Gridwatch) handleTicker(w http.ResponseWriter, r *http.Request) {
	data, _ := json.Marshal(gw.ticker.Events())
	jsonResponse(w, data)
}
