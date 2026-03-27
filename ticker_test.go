package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTickerBusPushAndEvents(t *testing.T) {
	tb := newTickerBus(10, nil)

	tb.Push("svc", "info", "nginx up", "")
	tb.Push("docker", "warn", "redis restarted", "count: 3")
	tb.Push("disk", "error", "omarchy disk 92%", "")
	tb.Push("peer", "info", "macbook1 joined", "")
	tb.Push("daemon", "info", "fleet-scout complete", "2.3s")

	events := tb.Events()
	if len(events) != 5 {
		t.Fatalf("got %d events, want 5", len(events))
	}

	// Events return newest first.
	if events[0].Type != "daemon" || events[0].Title != "fleet-scout complete" {
		t.Errorf("event[0] (newest) = %+v", events[0])
	}
	if events[0].Detail != "2.3s" {
		t.Errorf("event[0].Detail = %q, want 2.3s", events[0].Detail)
	}
	if events[4].Type != "svc" || events[4].Title != "nginx up" {
		t.Errorf("event[4] (oldest) = %+v", events[4])
	}

	for _, e := range events {
		if e.Time == "" {
			t.Error("event has empty time")
		}
	}
}

func TestTickerBusRingBuffer(t *testing.T) {
	tb := newTickerBus(3, nil)

	tb.Push("a", "info", "first", "")
	tb.Push("b", "info", "second", "")
	tb.Push("c", "info", "third", "")
	tb.Push("d", "info", "fourth", "")
	tb.Push("e", "info", "fifth", "")

	events := tb.Events()
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}

	// Newest first: fifth, fourth, third.
	if events[0].Title != "fifth" {
		t.Errorf("newest event = %q, want fifth", events[0].Title)
	}
	if events[2].Title != "third" {
		t.Errorf("oldest event = %q, want third", events[2].Title)
	}
}

func TestTickerBusEmpty(t *testing.T) {
	tb := newTickerBus(10, nil)
	events := tb.Events()
	if len(events) != 0 {
		t.Fatalf("got %d events from empty bus", len(events))
	}
}

func TestTickerBusEventsCopied(t *testing.T) {
	tb := newTickerBus(10, nil)
	tb.Push("svc", "info", "test", "")

	events := tb.Events()
	events[0].Title = "mutated"

	original := tb.Events()
	if original[0].Title != "test" {
		t.Error("Events() did not return a copy")
	}
}

func TestTickerHandler(t *testing.T) {
	gw := &Gridwatch{
		config: GridwatchConfig{Port: 0},
		stats:  make(map[string]*MachineStats),
		ticker: newTickerBus(100, nil),
	}

	gw.ticker.Push("svc", "info", "nginx up", "healthy")
	gw.ticker.Push("disk", "error", "omarchy disk 91%", "")

	rec := httptest.NewRecorder()
	gw.handleTicker(rec, httptest.NewRequest("GET", "/api/ticker", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d", rec.Code)
	}

	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("content-type = %q", ct)
	}

	var events []TickerEvent
	if err := json.Unmarshal(rec.Body.Bytes(), &events); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	// Newest first.
	if events[0].Title != "omarchy disk 91%" {
		t.Errorf("events[0].Title = %q, want 'omarchy disk 91%%'", events[0].Title)
	}
}

func TestTickerHandlerEmpty(t *testing.T) {
	gw := &Gridwatch{
		config: GridwatchConfig{Port: 0},
		stats:  make(map[string]*MachineStats),
		ticker: newTickerBus(100, nil),
	}

	rec := httptest.NewRecorder()
	gw.handleTicker(rec, httptest.NewRequest("GET", "/api/ticker", nil))

	var events []TickerEvent
	if err := json.Unmarshal(rec.Body.Bytes(), &events); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected empty array, got %d events", len(events))
	}
}
