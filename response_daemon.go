package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ResponseDaemon subscribes to fleet security events and executes
// automated incident response actions based on severity tiers.
type ResponseDaemon struct {
	pub             *NATSPublisher
	mu              sync.RWMutex
	incidents       map[string]*Incident // incident ID -> incident
	ipBlocks        map[string]time.Time // "machine:ip" -> unblock time
	emailThrottle   map[string]time.Time // machine -> last email time
	sshTargets      map[string]string    // machine -> ssh target
	machineOS       map[string]string    // machine -> "linux" or "macos"
	emailTo         string
	forensicDir     string
	ipBlockDuration time.Duration
	dryRun          bool
}

func runResponseDaemon(ctx context.Context) error {
	log.Printf("[response] starting incident response daemon")

	gwc := loadGridwatchConfig()

	// Build SSH target and OS maps from gridwatch config
	sshTargets := map[string]string{
		"ubuntu-homelab": "100.109.211.128",
		"omarchy":        "100.85.150.110",
		"raspdeck":       "raspdeck",
		"thinkbook":      "thinkbook-omarchy",
		"macbook1":       "williamvansickleiii@100.67.104.73",
		"willyv4":        "100.95.2.57",
	}
	machineOS := make(map[string]string)

	for _, m := range gwc.Machines {
		if m.Host != "" {
			sshTargets[m.ID] = m.Host
		}
		if m.OS == "macos" {
			machineOS[m.ID] = "macos"
		} else {
			machineOS[m.ID] = "linux"
		}
	}

	emailTo := os.Getenv("RESPONSE_EMAIL_TO")
	if emailTo == "" {
		emailTo = "vansicklewilly@gmail.com"
	}

	home, _ := os.UserHomeDir()
	forensicDir := os.Getenv("RESPONSE_FORENSIC_DIR")
	if forensicDir == "" {
		forensicDir = filepath.Join(home, ".config", "claude-peers", "forensics")
	}

	if err := os.MkdirAll(forensicDir, 0755); err != nil {
		log.Printf("[response] warning: could not create forensic dir: %v", err)
	}

	dryRun := os.Getenv("RESPONSE_DRY_RUN") == "true" || os.Getenv("RESPONSE_DRY_RUN") == "1"

	pub := newNATSPublisher()
	if pub == nil {
		return errStr("response-daemon requires NATS connection")
	}
	defer pub.close()

	rd := &ResponseDaemon{
		pub:             pub,
		incidents:       make(map[string]*Incident),
		ipBlocks:        make(map[string]time.Time),
		emailThrottle:   make(map[string]time.Time),
		sshTargets:      sshTargets,
		machineOS:       machineOS,
		emailTo:         emailTo,
		forensicDir:     forensicDir,
		ipBlockDuration: 1 * time.Hour,
		dryRun:          dryRun,
	}

	nc, err := subscribeFleet("response-daemon", func(event FleetEvent) {
		rd.processEvent(event)
	})
	if err != nil {
		return err
	}
	defer nc.Close()

	log.Printf("[response] subscribed to fleet events (email=%s, forensics=%s, dry_run=%v)", emailTo, forensicDir, dryRun)

	// Start IP block expiry goroutine
	go rd.expireIPBlocks(ctx)

	<-ctx.Done()
	log.Printf("[response] shutting down")
	return nil
}

func (rd *ResponseDaemon) processEvent(event FleetEvent) {
	// Only process security-related events
	if !strings.HasPrefix(event.Type, "security") && !strings.Contains(event.Type, "quarantine") {
		if event.Data == "" {
			return
		}
	}

	var secEvent SecurityEvent

	// Try parsing the data field as SecurityEvent
	if event.Data != "" {
		if err := json.Unmarshal([]byte(event.Data), &secEvent); err == nil && secEvent.Machine != "" {
			goto parsed
		}
	}

	// Fallback: construct from FleetEvent fields
	if event.Machine != "" {
		secEvent = SecurityEvent{
			Type:        event.Type,
			Machine:     event.Machine,
			Description: event.Summary,
			Timestamp:   event.Timestamp,
		}
		goto parsed
	}

	return

parsed:
	if secEvent.Machine == "" {
		return
	}

	// Skip info/warning severity -- no incident needed
	if secEvent.Severity == "info" || secEvent.Severity == "warning" {
		return
	}

	// Classify incident type
	incType := rd.classifyIncident(event, secEvent)
	if incType == "" {
		return
	}

	log.Printf("[response] incident classified: type=%s machine=%s rule=%s severity=%s",
		incType, secEvent.Machine, secEvent.RuleID, secEvent.Severity)

	// Deduplicate: find existing incident for same machine+type within 30 min
	inc := rd.findOrCreateIncident(incType, secEvent)

	rd.respondToIncident(inc)
}

func (rd *ResponseDaemon) classifyIncident(event FleetEvent, secEvent SecurityEvent) IncidentType {
	// Quarantine events from security-watch escalations
	if strings.Contains(event.Type, "quarantine") {
		data := strings.ToLower(event.Data + " " + event.Summary + " " + secEvent.Description)
		if strings.Contains(data, "brute") {
			return IncidentBruteForce
		}
		if strings.Contains(data, "credential") {
			return IncidentCredentialTheft
		}
		if strings.Contains(data, "distributed") {
			return IncidentLateralMovement
		}
		return IncidentQuarantine
	}

	// Binary tamper: custom rule 100101 at level 13+
	if secEvent.RuleID == "100101" && secEvent.Level >= 13 {
		return IncidentBinaryTamper
	}

	// Rogue service: custom rule 100130
	if secEvent.RuleID == "100130" {
		return IncidentRogueService
	}

	// Generic quarantine/critical severity
	if secEvent.Severity == "quarantine" || secEvent.Severity == "critical" {
		return IncidentQuarantine
	}

	return ""
}

func (rd *ResponseDaemon) findOrCreateIncident(incType IncidentType, secEvent SecurityEvent) *Incident {
	rd.mu.Lock()
	defer rd.mu.Unlock()

	cutoff := time.Now().Add(-30 * time.Minute)

	// Look for existing incident of same type on same machine within window
	for _, inc := range rd.incidents {
		if inc.Type != incType || inc.Status == "resolved" {
			continue
		}
		if inc.CreatedAt.Before(cutoff) {
			continue
		}
		for _, m := range inc.Machines {
			if m == secEvent.Machine {
				inc.Events = append(inc.Events, secEvent)
				return inc
			}
		}
	}

	// Create new incident
	id := fmt.Sprintf("inc-%s-%s-%d", secEvent.Machine, incType, time.Now().UnixMilli())
	inc := &Incident{
		ID:        id,
		Type:      incType,
		Tier:      incidentTier(incType),
		Machines:  []string{secEvent.Machine},
		Events:    []SecurityEvent{secEvent},
		Status:    "active",
		CreatedAt: time.Now(),
	}

	rd.incidents[id] = inc
	return inc
}

func (rd *ResponseDaemon) respondToIncident(inc *Incident) {
	machine := ""
	if len(inc.Machines) > 0 {
		machine = inc.Machines[0]
	}

	switch inc.Type {
	case IncidentBruteForce:
		// Tier 2: forensics + IP block + email
		snap, err := rd.captureForensics(machine, "")
		if err != nil {
			log.Printf("[response] forensics capture failed on %s: %v", machine, err)
		} else {
			inc.Actions = append(inc.Actions, ResponseAction{
				Type:    "forensic_capture",
				Machine: machine,
				Detail:  "forensic snapshot captured",
				Status:  "complete",
			})
			_ = snap
		}

		// Block source IP if available and not Tailscale
		lastEvent := inc.Events[len(inc.Events)-1]
		if lastEvent.SourceIP != "" {
			if err := rd.executeIPBlock(machine, lastEvent.SourceIP); err != nil {
				log.Printf("[response] IP block failed: %v", err)
				inc.Actions = append(inc.Actions, ResponseAction{
					Type:    "ip_block",
					Machine: machine,
					Detail:  fmt.Sprintf("block %s: %v", lastEvent.SourceIP, err),
					Status:  "failed",
				})
			} else {
				now := time.Now()
				expires := now.Add(rd.ipBlockDuration)
				inc.Actions = append(inc.Actions, ResponseAction{
					Type:       "ip_block",
					Machine:    machine,
					Detail:     "blocked " + lastEvent.SourceIP,
					Status:     "complete",
					ExecutedAt: &now,
					ExpiresAt:  &expires,
				})
			}
		}

		rd.sendIncidentEmail(inc)
		inc.Status = "contained"
		log.Printf("[response] brute_force on %s: IP %s blocked, forensics captured", machine, lastEvent.SourceIP)

	case IncidentBinaryTamper:
		// Tier 2: forensics with file hash + email
		filePath := ""
		if len(inc.Events) > 0 {
			filePath = inc.Events[len(inc.Events)-1].FilePath
		}
		_, err := rd.captureForensics(machine, filePath)
		if err != nil {
			log.Printf("[response] forensics capture failed on %s: %v", machine, err)
		} else {
			inc.Actions = append(inc.Actions, ResponseAction{
				Type:    "forensic_capture",
				Machine: machine,
				Detail:  "forensic snapshot with file hash captured",
				Status:  "complete",
			})
		}

		rd.sendIncidentEmail(inc)
		inc.Status = "contained"
		log.Printf("[response] binary_tamper on %s: quarantined, forensics captured", machine)

	case IncidentRogueService:
		// Tier 1: capture unit file + email
		if len(inc.Events) > 0 {
			lastEvent := inc.Events[len(inc.Events)-1]
			if lastEvent.FilePath != "" {
				content, err := rd.captureUnitFile(machine, lastEvent.FilePath)
				if err != nil {
					log.Printf("[response] unit file capture failed on %s: %v", machine, err)
				} else {
					inc.Actions = append(inc.Actions, ResponseAction{
						Type:    "forensic_capture",
						Machine: machine,
						Detail:  "unit file captured: " + lastEvent.FilePath,
						Status:  "complete",
						Result:  content,
					})
				}
			}
		}

		rd.sendIncidentEmail(inc)
		log.Printf("[response] rogue_service on %s: unit file captured", machine)

	case IncidentCredentialTheft:
		// Tier 3: forensics + email with rotation notice
		_, err := rd.captureForensics(machine, "")
		if err != nil {
			log.Printf("[response] forensics capture failed on %s: %v", machine, err)
		} else {
			inc.Actions = append(inc.Actions, ResponseAction{
				Type:    "forensic_capture",
				Machine: machine,
				Detail:  "forensic snapshot captured",
				Status:  "complete",
			})
		}

		rd.sendIncidentEmail(inc)
		inc.Status = "approval_pending"
		log.Printf("[response] credential_theft on %s: REQUIRES CREDENTIAL ROTATION", machine)

	case IncidentLateralMovement:
		// Tier 3: forensics on ALL affected machines + email
		for _, m := range inc.Machines {
			_, err := rd.captureForensics(m, "")
			if err != nil {
				log.Printf("[response] forensics capture failed on %s: %v", m, err)
			} else {
				inc.Actions = append(inc.Actions, ResponseAction{
					Type:    "forensic_capture",
					Machine: m,
					Detail:  "forensic snapshot captured",
					Status:  "complete",
				})
			}
		}

		rd.sendIncidentEmail(inc)
		inc.Status = "approval_pending"
		log.Printf("[response] lateral_movement across %s: REQUIRES FULL FLEET AUDIT", strings.Join(inc.Machines, ", "))

	case IncidentQuarantine:
		// Tier 1: email
		rd.sendIncidentEmail(inc)
		reason := ""
		if len(inc.Events) > 0 {
			reason = inc.Events[len(inc.Events)-1].Description
		}
		log.Printf("[response] quarantine on %s: %s", machine, reason)
	}
}

// expireIPBlocks periodically checks for expired IP blocks and removes them.
func (rd *ResponseDaemon) expireIPBlocks(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rd.mu.RLock()
			var expired []string
			for key, unblockAt := range rd.ipBlocks {
				if time.Now().After(unblockAt) {
					expired = append(expired, key)
				}
			}
			rd.mu.RUnlock()

			for _, key := range expired {
				parts := strings.SplitN(key, ":", 2)
				if len(parts) == 2 {
					if err := rd.removeIPBlock(parts[0], parts[1]); err != nil {
						log.Printf("[response] failed to expire IP block %s: %v", key, err)
					} else {
						log.Printf("[response] expired IP block: %s", key)
					}
				}
			}
		}
	}
}
