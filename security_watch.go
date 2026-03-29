package main

import (
	"context"
	"encoding/json"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// SecurityWatch subscribes to fleet.security.> events and performs
// intelligent correlation, escalation, and email alerting.
type SecurityWatch struct {
	nc            interface{ Close() }
	pub           *NATSPublisher
	mu            sync.RWMutex
	alertWindows  map[string][]SecurityEvent // machine -> recent events (30 min window)
	emailThrottle map[string]time.Time       // machine -> last email sent
}

func runSecurityWatch(ctx context.Context) error {
	log.Printf("[security-watch] starting security event correlator")

	pub := newNATSPublisher()
	if pub == nil {
		return errStr("security-watch requires NATS connection for publishing escalations")
	}
	defer pub.close()

	sw := &SecurityWatch{
		pub:           pub,
		alertWindows:  make(map[string][]SecurityEvent),
		emailThrottle: make(map[string]time.Time),
	}

	nc, err := subscribeFleet("security-watch", func(event FleetEvent) {
		sw.processEvent(event)
	})
	if err != nil {
		return err
	}
	sw.nc = nc
	defer nc.Close()

	log.Printf("[security-watch] subscribed to fleet.security.> events")

	// Periodic prune of old events
	pruneTicker := time.NewTicker(1 * time.Minute)
	defer pruneTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Printf("[security-watch] shutting down")
			return nil
		case <-pruneTicker.C:
			sw.pruneOldEvents()
		}
	}
}

func (sw *SecurityWatch) processEvent(event FleetEvent) {
	// Only process security events
	if !strings.HasPrefix(event.Type, "security.") && !strings.Contains(event.Type, "security") {
		// Try parsing Data field as SecurityEvent regardless -- the wazuh-bridge
		// publishes raw SecurityEvent JSON on fleet.security.* subjects.
		if event.Data == "" {
			return
		}
	}

	var secEvent SecurityEvent

	// First try: parse the entire FleetEvent data field as a SecurityEvent
	if event.Data != "" {
		if err := json.Unmarshal([]byte(event.Data), &secEvent); err == nil && secEvent.Machine != "" {
			goto parsed
		}
	}

	// Second try: the message itself might be the SecurityEvent (wazuh-bridge
	// publishes SecurityEvent directly, and subscribeFleet wraps it as FleetEvent)
	if event.Machine != "" {
		secEvent = SecurityEvent{
			Type:        event.Type,
			Machine:     event.Machine,
			Description: event.Summary,
			Timestamp:   event.Timestamp,
		}
		goto parsed
	}

	// Can't parse -- skip
	return

parsed:
	if secEvent.Machine == "" {
		return
	}
	if secEvent.Timestamp == "" {
		secEvent.Timestamp = nowISO()
	}

	log.Printf("[security-watch] event: type=%s severity=%s machine=%s rule=%s: %s",
		secEvent.Type, secEvent.Severity, secEvent.Machine, secEvent.RuleID, secEvent.Description)

	sw.mu.Lock()
	sw.alertWindows[secEvent.Machine] = append(sw.alertWindows[secEvent.Machine], secEvent)
	sw.mu.Unlock()

	sw.pruneOldEvents()

	// Run correlation rules
	sw.checkDistributedAttack(secEvent)
	sw.checkBruteForce(secEvent)
	sw.checkCredentialTheft(secEvent)

	// Email alert for critical+ events
	if secEvent.Severity == "critical" || secEvent.Severity == "quarantine" {
		sw.sendEmailAlert(secEvent.Machine, secEvent, "critical event detected")
	}
}

// checkDistributedAttack fires if the same rule ID appears on 3+ machines within 5 minutes.
func (sw *SecurityWatch) checkDistributedAttack(event SecurityEvent) {
	if event.RuleID == "" {
		return
	}

	cutoff := time.Now().Add(-5 * time.Minute)
	machinesHit := map[string][]SecurityEvent{}

	sw.mu.RLock()
	for machine, events := range sw.alertWindows {
		for _, e := range events {
			if e.RuleID == event.RuleID {
				ts, err := time.Parse(time.RFC3339, e.Timestamp)
				if err == nil && ts.After(cutoff) {
					machinesHit[machine] = append(machinesHit[machine], e)
				}
			}
		}
	}
	sw.mu.RUnlock()

	if len(machinesHit) >= 3 {
		machines := make([]string, 0, len(machinesHit))
		var allEvents []SecurityEvent
		for m, evts := range machinesHit {
			machines = append(machines, m)
			allEvents = append(allEvents, evts...)
		}
		reason := "distributed attack: rule " + event.RuleID + " firing on " + strings.Join(machines, ", ")
		log.Printf("[security-watch] CORRELATION: %s", reason)
		sw.escalate(event.Machine, reason, allEvents)

		// Send email for distributed attack
		sw.sendEmailAlert(event.Machine, event, reason)
	}
}

// checkBruteForce fires if 5+ auth failures from same machine within 10 minutes.
func (sw *SecurityWatch) checkBruteForce(event SecurityEvent) {
	if event.Type != "auth" {
		return
	}

	cutoff := time.Now().Add(-10 * time.Minute)
	var authFailures []SecurityEvent

	sw.mu.RLock()
	for _, e := range sw.alertWindows[event.Machine] {
		if e.Type == "auth" {
			ts, err := time.Parse(time.RFC3339, e.Timestamp)
			if err == nil && ts.After(cutoff) {
				authFailures = append(authFailures, e)
			}
		}
	}
	sw.mu.RUnlock()

	if len(authFailures) >= 5 {
		reason := "brute force: " + string(rune(len(authFailures)+'0')) + "+ auth failures on " + event.Machine + " in 10 minutes"
		reason = "brute force: " + itoa(len(authFailures)) + " auth failures on " + event.Machine + " in 10 minutes"
		log.Printf("[security-watch] CORRELATION: %s", reason)
		sw.escalate(event.Machine, reason, authFailures)
		sw.sendEmailAlert(event.Machine, event, reason)
	}
}

// checkCredentialTheft fires if a FIM event on identity.pem or token.jwt is followed
// by a peer registration from that machine within 5 minutes.
func (sw *SecurityWatch) checkCredentialTheft(event SecurityEvent) {
	cutoff := time.Now().Add(-5 * time.Minute)

	sw.mu.RLock()
	events := sw.alertWindows[event.Machine]
	sw.mu.RUnlock()

	// Look for FIM on credential files
	var fimEvents []SecurityEvent
	var peerEvents []SecurityEvent
	for _, e := range events {
		ts, err := time.Parse(time.RFC3339, e.Timestamp)
		if err != nil || ts.Before(cutoff) {
			continue
		}
		if e.Type == "fim" && (strings.Contains(e.FilePath, "identity.pem") || strings.Contains(e.FilePath, "token.jwt")) {
			fimEvents = append(fimEvents, e)
		}
		// peer.joined events come through as FleetEvent type
		if strings.Contains(e.Type, "peer") || strings.Contains(e.Description, "register") {
			peerEvents = append(peerEvents, e)
		}
	}

	if len(fimEvents) > 0 && len(peerEvents) > 0 {
		var allEvents []SecurityEvent
		allEvents = append(allEvents, fimEvents...)
		allEvents = append(allEvents, peerEvents...)
		reason := "credential theft: FIM on identity/token files + peer registration on " + event.Machine
		log.Printf("[security-watch] CORRELATION: %s", reason)
		sw.escalate(event.Machine, reason, allEvents)
		sw.sendEmailAlert(event.Machine, event, reason)
	}
}

// escalate publishes a quarantine event to NATS with correlation details.
func (sw *SecurityWatch) escalate(machine string, reason string, events []SecurityEvent) {
	escalation := map[string]interface{}{
		"machine":     machine,
		"reason":      reason,
		"event_count": len(events),
		"timestamp":   nowISO(),
	}
	data, err := json.Marshal(escalation)
	if err != nil {
		log.Printf("[security-watch] escalation marshal error: %v", err)
		return
	}

	sw.pub.publish("fleet.security.quarantine", FleetEvent{
		Type:    "security.quarantine",
		Machine: machine,
		Summary: reason,
		Data:    string(data),
	})
	log.Printf("[security-watch] escalation published: %s -> quarantine", machine)
}

// sendEmailAlert sends an email via gws CLI, throttled to 1 per machine per 15 minutes.
func (sw *SecurityWatch) sendEmailAlert(machine string, event SecurityEvent, reason string) {
	sw.mu.Lock()
	if lastSent, ok := sw.emailThrottle[machine]; ok {
		if time.Since(lastSent) < 15*time.Minute {
			sw.mu.Unlock()
			log.Printf("[security-watch] email throttled for %s (last sent %s ago)", machine, time.Since(lastSent).Round(time.Second))
			return
		}
	}
	sw.emailThrottle[machine] = time.Now()
	sw.mu.Unlock()

	subject := "[fleet-security] " + event.Severity + " on " + machine + ": " + reason
	body := "Machine: " + machine + "\n" +
		"Severity: " + event.Severity + "\n" +
		"Type: " + event.Type + "\n" +
		"Rule: " + event.RuleID + "\n" +
		"Description: " + event.Description + "\n" +
		"Reason: " + reason + "\n" +
		"Time: " + event.Timestamp + "\n"

	cmd := exec.Command("resend-email",
		"-m", body,
		"vansicklewilly@gmail.com",
		subject)
	if err := cmd.Start(); err != nil {
		log.Printf("[security-watch] email send failed: %v", err)
		return
	}
	// Don't block -- fire and forget
	go func() {
		if err := cmd.Wait(); err != nil {
			log.Printf("[security-watch] email command failed: %v", err)
		}
	}()
	log.Printf("[security-watch] email sent for %s: %s", machine, subject)
}

// pruneOldEvents removes events older than 30 minutes from all windows.
func (sw *SecurityWatch) pruneOldEvents() {
	cutoff := time.Now().Add(-30 * time.Minute)

	sw.mu.Lock()
	defer sw.mu.Unlock()

	for machine, events := range sw.alertWindows {
		kept := events[:0]
		for _, e := range events {
			ts, err := time.Parse(time.RFC3339, e.Timestamp)
			if err != nil || ts.After(cutoff) {
				kept = append(kept, e)
			}
		}
		if len(kept) == 0 {
			delete(sw.alertWindows, machine)
		} else {
			sw.alertWindows[machine] = kept
		}
	}
}

// itoa is a minimal int-to-string without importing strconv.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		digits = append([]byte{'-'}, digits...)
	}
	return string(digits)
}
