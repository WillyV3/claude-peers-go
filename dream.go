package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const fleetMemoryFile = "fleet-activity.md"

func cliDream() {
	log.SetFlags(0)

	peers, events := fetchFleetState()

	content := buildFleetMemory(peers, events)
	memPath := writeFleetMemory(content)

	fmt.Printf("[dream] Fleet memory updated: %s\n", memPath)
	fmt.Printf("[dream] %d peers, %d events\n", len(peers), len(events))
}

func cliDreamWatch() {
	log.SetFlags(0)
	fmt.Println("[dream] Watching fleet events via NATS...")

	var events []FleetEvent

	nc, err := subscribeFleet("dream-watch", func(event FleetEvent) {
		events = append(events, event)
		log.Printf("[dream] %s: %s %s", event.Type, event.Machine, event.Summary)

		// Consolidate every 30 events or every 5 minutes
		if len(events) >= 30 {
			consolidate(events)
			events = nil
		}
	})
	if err != nil {
		// Fall back to HTTP polling if NATS isn't available
		log.Printf("[dream] NATS unavailable (%v), falling back to HTTP polling", err)
		dreamPollLoop()
		return
	}
	defer nc.Close()

	// Periodic consolidation even if events are slow
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		if len(events) > 0 {
			consolidate(events)
			events = nil
		} else {
			// Still refresh from broker state even if no new events
			peers, evts := fetchFleetState()
			content := buildFleetMemory(peers, evts)
			writeFleetMemory(content)
		}
	}
}

func dreamPollLoop() {
	for {
		peers, events := fetchFleetState()
		content := buildFleetMemory(peers, events)
		writeFleetMemory(content)
		time.Sleep(5 * time.Minute)
	}
}

func consolidate(events []FleetEvent) {
	peers, brokerEvents := fetchFleetState()
	content := buildFleetMemory(peers, brokerEvents)
	path := writeFleetMemory(content)
	log.Printf("[dream] Consolidated %d events -> %s", len(events), path)
}

func fetchFleetState() ([]Peer, []Event) {
	var peers []Peer
	cliFetch("/list-peers", ListPeersRequest{Scope: "all", CWD: "/"}, &peers)

	var events []Event
	resp, err := fetchEventsRaw()
	if err == nil {
		events = resp
	}

	return peers, events
}

func fetchEventsRaw() ([]Event, error) {
	var events []Event
	err := cliFetch("/events?limit=100", nil, &events)
	return events, err
}

func buildFleetMemory(peers []Peer, events []Event) string {
	var sb strings.Builder

	sb.WriteString("---\n")
	sb.WriteString("name: fleet-activity\n")
	sb.WriteString("description: Live fleet activity across all machines -- updated automatically by claude-peers dream\n")
	sb.WriteString("type: project\n")
	sb.WriteString("---\n\n")

	now := time.Now().UTC().Format(time.RFC3339)
	sb.WriteString(fmt.Sprintf("Last updated: %s\n\n", now))

	// Active peers grouped by machine
	machines := map[string][]Peer{}
	for _, p := range peers {
		machines[p.Machine] = append(machines[p.Machine], p)
	}

	sb.WriteString("## Active Claude Instances\n\n")
	if len(peers) == 0 {
		sb.WriteString("No active instances.\n\n")
	} else {
		for machine, mPeers := range machines {
			sb.WriteString(fmt.Sprintf("### %s (%d sessions)\n", machine, len(mPeers)))
			for _, p := range mPeers {
				cwd := shortenPath(p.CWD)
				repo := ""
				if p.GitRoot != "" {
					parts := strings.Split(p.GitRoot, "/")
					repo = parts[len(parts)-1]
				}
				sb.WriteString(fmt.Sprintf("- **%s**", cwd))
				if repo != "" {
					sb.WriteString(fmt.Sprintf(" (repo: %s)", repo))
				}
				if p.Summary != "" {
					sb.WriteString(fmt.Sprintf(": %s", p.Summary))
				}
				sb.WriteString("\n")
			}
			sb.WriteString("\n")
		}
	}

	// Recent events
	sb.WriteString("## Recent Activity\n\n")
	if len(events) == 0 {
		sb.WriteString("No recent events.\n")
	} else {
		typeLabels := map[string]string{
			"peer_joined":     "joined",
			"peer_left":       "left",
			"summary_changed": "working on",
			"message_sent":    "messaged",
		}
		shown := 0
		for _, e := range events {
			if shown >= 20 {
				break
			}
			label := typeLabels[e.Type]
			if label == "" {
				label = e.Type
			}
			who := e.Machine
			if who == "" {
				who = e.PeerID
			}
			line := fmt.Sprintf("- %s %s", who, label)
			if e.Data != "" && e.Type == "summary_changed" {
				line += fmt.Sprintf(": %s", e.Data)
			}
			ago := timeAgoStr(e.CreatedAt)
			line += fmt.Sprintf(" (%s ago)", ago)
			sb.WriteString(line + "\n")
			shown++
		}
	}

	return sb.String()
}

func shortenPath(p string) string {
	home, _ := os.UserHomeDir()
	if strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	p = strings.Replace(p, "/home/willy/", "~/", 1)
	p = strings.Replace(p, "/Users/williamvansickleiii/", "~/", 1)
	if p == "/home/willy" || p == "/Users/williamvansickleiii" {
		return "~"
	}
	return p
}

func timeAgoStr(iso string) string {
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return "?"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func writeFleetMemory(content string) string {
	memDir := claudeMemoryDir()
	os.MkdirAll(memDir, 0755)

	path := filepath.Join(memDir, fleetMemoryFile)
	os.WriteFile(path, []byte(content), 0644)

	updateMemoryIndex(memDir)
	return path
}

func claudeMemoryDir() string {
	home, _ := os.UserHomeDir()
	// Global memory path (not project-specific)
	return filepath.Join(home, ".claude", "projects", claudeProjectKey(), "memory")
}

func claudeProjectKey() string {
	home, _ := os.UserHomeDir()
	// Convert home dir to Claude's project key format: /home/willy -> -home-willy
	key := strings.ReplaceAll(home, "/", "-")
	if key[0] == '-' {
		return key
	}
	return "-" + key
}

func updateMemoryIndex(memDir string) {
	indexPath := filepath.Join(memDir, "MEMORY.md")
	existing, _ := os.ReadFile(indexPath)

	entry := fmt.Sprintf("- [%s](%s) - Live fleet activity across all machines, auto-updated by claude-peers dream", fleetMemoryFile, fleetMemoryFile)

	if strings.Contains(string(existing), fleetMemoryFile) {
		// Already indexed
		return
	}

	content := string(existing)
	if content == "" {
		content = "# Memory Index\n\n"
	}
	content += entry + "\n"
	os.WriteFile(indexPath, []byte(content), 0644)
}

