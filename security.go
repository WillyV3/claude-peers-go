package main

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
)

// MachineHealth tracks the security posture of a fleet machine.
type MachineHealth struct {
	Score         int      `json:"score"`
	Status        string   `json:"status"`
	LastEvent     string   `json:"last_event"`
	LastEventDesc string   `json:"last_event_desc"`
	DemotedAt     string   `json:"demoted_at,omitempty"`
	Events        []string `json:"events"`
}

// healthChecker is implemented by the Broker to allow the UCAN middleware
// to check machine quarantine status without importing the full Broker type.
type healthChecker interface {
	getMachineHealth(machine string) *MachineHealth
}

// getMachineHealth returns the health record for a machine, or nil if none exists.
func (b *Broker) getMachineHealth(machine string) *MachineHealth {
	b.healthMu.RLock()
	defer b.healthMu.RUnlock()
	return b.machineHealth[machine]
}

// updateMachineHealth processes a SecurityEvent and updates the machine's health score.
func (b *Broker) updateMachineHealth(event SecurityEvent) {
	b.healthMu.Lock()
	defer b.healthMu.Unlock()

	h, ok := b.machineHealth[event.Machine]
	if !ok {
		h = &MachineHealth{
			Status: "healthy",
			Events: []string{},
		}
		b.machineHealth[event.Machine] = h
	}

	switch event.Severity {
	case "warning":
		h.Score += 1
		// Warnings cap at 9 -- they can degrade but never quarantine alone.
		if h.Score > 9 {
			h.Score = 9
		}
	case "critical":
		h.Score += 10
	case "quarantine":
		h.Status = "quarantined"
		h.DemotedAt = nowISO()
	}

	// Recalculate status from score (unless explicitly quarantined above).
	if event.Severity != "quarantine" {
		switch {
		case h.Score >= 10:
			h.Status = "quarantined"
			if h.DemotedAt == "" {
				h.DemotedAt = nowISO()
			}
		case h.Score >= 5:
			h.Status = "degraded"
		default:
			h.Status = "healthy"
		}
	}

	// Ring buffer of recent event summaries (max 10).
	summary := event.Severity + ": " + event.Description
	h.Events = append(h.Events, summary)
	if len(h.Events) > 10 {
		h.Events = h.Events[len(h.Events)-10:]
	}

	h.LastEvent = event.Timestamp
	h.LastEventDesc = event.Description
}

// unquarantine resets a machine's health to clean state.
func (b *Broker) unquarantine(machine string) {
	b.healthMu.Lock()
	defer b.healthMu.Unlock()

	h, ok := b.machineHealth[machine]
	if !ok {
		return
	}
	h.Score = 0
	h.Status = "healthy"
	h.DemotedAt = ""
}

// decayHealthScores reduces health scores over time for non-quarantined machines.
func (b *Broker) decayHealthScores() {
	b.healthMu.Lock()
	defer b.healthMu.Unlock()

	for _, h := range b.machineHealth {
		if h.Status == "quarantined" {
			continue
		}
		if h.Score > 0 {
			h.Score--
		}
		// Recalculate status after decay.
		switch {
		case h.Score >= 10:
			h.Status = "quarantined"
			if h.DemotedAt == "" {
				h.DemotedAt = nowISO()
			}
		case h.Score >= 5:
			h.Status = "degraded"
		default:
			h.Status = "healthy"
		}
	}
}

// subscribeSecurityEvents listens for fleet.security.> events via NATS and updates machine health.
func (b *Broker) subscribeSecurityEvents(ctx context.Context) {
	opts := []nats.Option{
		nats.Name("claude-peers-security-monitor"),
		nats.ReconnectWait(2 * time.Second),
		nats.MaxReconnects(-1),
	}
	if cfg.NatsToken != "" {
		opts = append(opts, nats.Token(cfg.NatsToken))
	}

	nc, err := nats.Connect(natsURL(), opts...)
	if err != nil {
		log.Printf("[security] NATS connect failed (security monitoring disabled): %v", err)
		return
	}
	defer nc.Close()

	js, err := nc.JetStream()
	if err != nil {
		log.Printf("[security] JetStream init failed: %v", err)
		return
	}

	_, err = js.Subscribe("fleet.security.>", func(msg *nats.Msg) {
		var event SecurityEvent
		if err := json.Unmarshal(msg.Data, &event); err != nil {
			log.Printf("[security] parse event: %v", err)
			msg.Ack()
			return
		}

		if event.Machine == "" {
			msg.Ack()
			return
		}

		b.updateMachineHealth(event)

		severity := strings.ToUpper(event.Severity)
		log.Printf("[security] [%s] %s on %s: level=%d rule=%s %s",
			severity, event.Type, event.Machine, event.Level, event.RuleID, event.Description)

		msg.Ack()
	}, nats.Durable("security-monitor"), nats.DeliverNew())

	if err != nil {
		log.Printf("[security] subscribe fleet.security.>: %v", err)
		return
	}

	log.Printf("[security] monitoring fleet security events via NATS")
	<-ctx.Done()
}

// getHealthScore is a convenience to read a machine's current score thread-safely.
func (b *Broker) getHealthScore(machine string) int {
	b.healthMu.RLock()
	defer b.healthMu.RUnlock()
	if h, ok := b.machineHealth[machine]; ok {
		return h.Score
	}
	return 0
}

// startHealthDecay runs the periodic score decay loop.
func (b *Broker) startHealthDecay(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.decayHealthScores()
		}
	}
}
