package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

//go:embed gridwatch-ui
var gridwatchUI embed.FS

// GridwatchConfig is loaded from gridwatch.json.
type GridwatchConfig struct {
	Port           int       `json:"port"`
	Machines       []Machine `json:"machines"`
	LLMURL         string    `json:"llm_url"`
	NATSURL        string    `json:"nats_url"`
	NATSMonitorURL string    `json:"nats_monitor_url"`
}

// Machine definition for SSH stat collection.
type Machine struct {
	ID    string `json:"id"`
	Host  string `json:"host"`
	OS    string `json:"os"`
	Specs string `json:"specs"`
	IP    string `json:"ip"`
}

// MachineStats is the collected data for one machine.
type MachineStats struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	OS        string    `json:"os"`
	Specs     string    `json:"specs"`
	IP        string    `json:"ip"`
	Name      string    `json:"name"`
	CPU       float64   `json:"cpu"`
	MemUsed   int64     `json:"mem_used,omitempty"`
	MemTotal  int64     `json:"mem_total,omitempty"`
	MemPct    float64   `json:"mem_pct"`
	DiskUsed  int64     `json:"disk_used,omitempty"`
	DiskTotal int64     `json:"disk_total,omitempty"`
	DiskPct   float64   `json:"disk_pct"`
	Processes []Process `json:"processes"`
	Uptime    string    `json:"uptime"`
}

type Process struct {
	Name   string  `json:"name"`
	MemPct float64 `json:"mem_pct"`
}

// Gridwatch server state.
type Gridwatch struct {
	config GridwatchConfig

	mu        sync.RWMutex
	stats     map[string]*MachineStats
	statsTime string

	peersMu    sync.RWMutex
	peersCache json.RawMessage
	eventsJSON json.RawMessage

	llmMu    sync.RWMutex
	llmCache json.RawMessage

	natsMu     sync.RWMutex
	natsConn   bool
	natsEvents []json.RawMessage
	daemonRuns []json.RawMessage
	willyv4    json.RawMessage

	svcMu    sync.RWMutex
	svcCache json.RawMessage

	natsMonMu    sync.RWMutex
	natsMonCache json.RawMessage

	ticker *TickerBus

	// Previous state for ticker change detection.
	prevStatuses    map[string]string // machine ID -> "online"/"offline"/"timeout"
	statusCount     map[string]int    // machine ID -> consecutive same-status count
	prevDiskAlerts  map[string]bool   // machine ID -> already alerted
}

const linuxCmd = `head -1 /proc/stat > /tmp/.gw_cpu1 2>/dev/null
sleep 0.5
head -1 /proc/stat > /tmp/.gw_cpu2 2>/dev/null
cpu=$(awk 'NR==1{split($0,a)} NR==2{split($0,b); idle_d=b[5]-a[5]; tot=0; for(i=2;i<=NF;i++) tot+=b[i]-a[i]; printf "%.1f", tot>0?(1-idle_d/tot)*100:0}' /tmp/.gw_cpu1 /tmp/.gw_cpu2 2>/dev/null || echo "0")
echo "CPU:$cpu"
free -b 2>/dev/null | awk '/Mem:/{printf "MEM:%d %d\n",$3,$2}'
df --block-size=1 / 2>/dev/null | awk 'NR==2{printf "DISK:%d %d\n",$3,$2}'
ps aux --sort=-%mem 2>/dev/null | awk 'NR>1&&NR<=6{n=$11;gsub(/.*\//,"",n);gsub(/[\[\]]/,"",n);printf "PROC:%s,%.1f\n",n,$4}'
uptime -p 2>/dev/null || uptime | sed 's/.*up /up /' | sed 's/,.*load.*//'`

const macosCmd = `ncpu=$(sysctl -n hw.ncpu 2>/dev/null || echo 4)
cpu_sum=$(ps -A -o %cpu 2>/dev/null | awk '{s+=$1} END {printf "%.1f",s}')
echo "CPU:$(echo "$cpu_sum $ncpu" | awk '{v=$1/$2; if(v>100)v=100; printf "%.1f",v}')"
_ps=$(sysctl -n hw.pagesize 2>/dev/null || echo 16384)
_mt=$(sysctl -n hw.memsize 2>/dev/null || echo 0)
_mu=$(vm_stat 2>/dev/null | awk -v ps="$_ps" '/Pages active:/{gsub(/\./,"",$NF);a=$NF} /Pages wired down:/{gsub(/\./,"",$NF);w=$NF} /Pages occupied by compressor:/{gsub(/\./,"",$NF);c=$NF} END{print (a+w+c)*ps}')
echo "MEM:$_mu $_mt"
df -k / 2>/dev/null | awk 'NR==2{printf "DISK:%d %d\n",$3*1024,($3+$4)*1024}'
ps aux -m 2>/dev/null | awk 'NR>1&&NR<=6{n=$11;gsub(/.*\//,"",n);gsub(/[\[\]]/,"",n);printf "PROC:%s,%.1f\n",n,$4}'
uptime | sed 's/.*up /up /' | sed 's/,.*load.*//' | sed 's/,.*user.*//'`

func loadGridwatchConfig() GridwatchConfig {
	gwc := GridwatchConfig{Port: 8888}

	// Check for config file next to claude-peers config.
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, ".config", "claude-peers", "gridwatch.json")
	if p := os.Getenv("GRIDWATCH_CONFIG"); p != "" {
		path = p
	}

	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("[gridwatch] no config at %s, using defaults", path)
		return gwc
	}

	if err := json.Unmarshal(data, &gwc); err != nil {
		log.Printf("[gridwatch] bad config: %v", err)
	}

	// Env overrides.
	if p := os.Getenv("GRIDWATCH_PORT"); p != "" {
		if v, err := strconv.Atoi(p); err == nil {
			gwc.Port = v
		}
	}
	return gwc
}

func runGridwatch(ctx context.Context) error {
	gwc := loadGridwatchConfig()

	if len(gwc.Machines) == 0 {
		return fmt.Errorf("no machines configured -- create %s with a \"machines\" array", filepath.Join("~/.config/claude-peers", "gridwatch.json"))
	}

	// Connect to NATS for publishing ticker events fleet-wide.
	var np *NATSPublisher
	if gwc.NATSURL != "" || cfg.BrokerURL != "" {
		np = newNATSPublisher()
	}

	gw := &Gridwatch{
		config:         gwc,
		stats:          make(map[string]*MachineStats),
		ticker:         newTickerBus(100, np),
		prevStatuses:    make(map[string]string),
		statusCount:     make(map[string]int),
		prevDiskAlerts:  make(map[string]bool),
	}

	go gw.collectStatsLoop(ctx)
	go gw.collectPeersLoop(ctx)
	go gw.runServiceMonitor(ctx)
	if gwc.LLMURL != "" {
		go gw.collectLLMLoop(ctx)
	}
	if natsURL := gwc.NATSURL; natsURL != "" {
		go gw.subscribeNATS(ctx)
	} else if cfg.BrokerURL != "" {
		gwc.NATSURL = os.Getenv("NATS_URL")
		if gwc.NATSURL != "" {
			go gw.subscribeNATS(ctx)
		}
	}
	if gwc.NATSMonitorURL != "" {
		go gw.collectNATSMonitor(ctx)
	}

	uiFS, err := fs.Sub(gridwatchUI, "gridwatch-ui")
	if err != nil {
		return fmt.Errorf("embed fs: %w", err)
	}
	fileServer := http.FileServer(http.FS(uiFS))

	mux := http.NewServeMux()
	mux.HandleFunc("/api/stats", gw.handleStats)
	mux.HandleFunc("/api/peers", gw.handlePeers)
	mux.HandleFunc("/api/llm", gw.handleLLM)
	mux.HandleFunc("/api/nats", gw.handleNATS)
	mux.HandleFunc("/api/daemons", gw.handleDaemons)
	mux.HandleFunc("/api/willyv4", gw.handleWillyv4)
	mux.HandleFunc("/api/services", gw.handleServices)
	mux.HandleFunc("/api/nats-stats", gw.handleNATSMonitor)
	mux.HandleFunc("/api/ticker", gw.handleTicker)
	mux.Handle("/", fileServer)

	srv := &http.Server{Addr: fmt.Sprintf("0.0.0.0:%d", gwc.Port), Handler: mux}

	go func() {
		<-ctx.Done()
		srv.Close()
	}()

	log.Printf("\033[32m[GRIDWATCH]\033[0m http://localhost:%d (%d machines)", gwc.Port, len(gwc.Machines))
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}

// --- SSH stat collection ---

func (gw *Gridwatch) collectStatsLoop(ctx context.Context) {
	for {
		var wg sync.WaitGroup
		results := make(chan *MachineStats, len(gw.config.Machines))

		for _, m := range gw.config.Machines {
			wg.Add(1)
			go func(m Machine) {
				defer wg.Done()
				results <- gw.collectOne(ctx, m)
			}(m)
		}

		go func() { wg.Wait(); close(results) }()

		stats := make(map[string]*MachineStats)
		for s := range results {
			stats[s.ID] = s
		}

		gw.mu.Lock()
		gw.stats = stats
		gw.statsTime = time.Now().UTC().Format(time.RFC3339)
		gw.mu.Unlock()

		gw.emitStatsTickerEvents(stats)

		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func (gw *Gridwatch) collectOne(ctx context.Context, m Machine) *MachineStats {
	offline := &MachineStats{ID: m.ID, Status: "offline", OS: m.OS, Specs: m.Specs, IP: m.IP, Name: m.ID}

	cmd := linuxCmd
	if m.OS == "macos" {
		cmd = macosCmd
	}

	tctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	var c *exec.Cmd
	if m.Host == "" {
		c = exec.CommandContext(tctx, "bash", "-c", cmd)
	} else {
		c = exec.CommandContext(tctx, "ssh",
			"-o", "ConnectTimeout=4",
			"-o", "StrictHostKeyChecking=no",
			"-o", "BatchMode=yes",
			m.Host, "bash -c "+shellQuote(cmd))
	}

	out, err := c.Output()
	if err != nil {
		if tctx.Err() == context.DeadlineExceeded {
			offline.Status = "timeout"
		}
		return offline
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return offline
	}

	return parseOutput(m, string(out))
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func parseOutput(m Machine, output string) *MachineStats {
	s := &MachineStats{
		ID:     m.ID,
		Status: "online",
		OS:     m.OS,
		Specs:  m.Specs,
		IP:     m.IP,
		Name:   m.ID,
	}

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "CPU:"):
			if v, err := strconv.ParseFloat(line[4:], 64); err == nil {
				s.CPU = math.Min(v, 100)
			}
		case strings.HasPrefix(line, "MEM:"):
			parts := strings.Fields(line[4:])
			if len(parts) == 2 {
				used, _ := strconv.ParseInt(parts[0], 10, 64)
				total, _ := strconv.ParseInt(parts[1], 10, 64)
				s.MemUsed = used
				s.MemTotal = total
				if total > 0 {
					s.MemPct = math.Round(float64(used)/float64(total)*1000) / 10
				}
			}
		case strings.HasPrefix(line, "DISK:"):
			parts := strings.Fields(line[5:])
			if len(parts) == 2 {
				used, _ := strconv.ParseInt(parts[0], 10, 64)
				total, _ := strconv.ParseInt(parts[1], 10, 64)
				s.DiskUsed = used
				s.DiskTotal = total
				if total > 0 {
					s.DiskPct = math.Round(float64(used)/float64(total)*1000) / 10
				}
			}
		case strings.HasPrefix(line, "PROC:"):
			entry := line[5:]
			if idx := strings.LastIndex(entry, ","); idx >= 0 {
				name := entry[:idx]
				if len(name) > 20 {
					name = name[:20]
				}
				if pct, err := strconv.ParseFloat(entry[idx+1:], 64); err == nil {
					s.Processes = append(s.Processes, Process{Name: name, MemPct: pct})
				}
			}
		case strings.HasPrefix(line, "up ") || strings.Contains(line, "day") || strings.Contains(line, "hour") || strings.Contains(line, "min"):
			s.Uptime = line
		}
	}
	return s
}

// --- Broker peer/event collection ---

func (gw *Gridwatch) collectPeersLoop(ctx context.Context) {
	for {
		gw.fetchPeers()
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func (gw *Gridwatch) fetchPeers() {
	client := &http.Client{Timeout: 3 * time.Second}

	body, _ := json.Marshal(map[string]string{"scope": "all", "cwd": "/"})
	req, _ := http.NewRequest("POST", cfg.BrokerURL+"/list-peers", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if cfg.Secret != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Secret)
	}
	if resp, err := client.Do(req); err == nil {
		if data, err := io.ReadAll(resp.Body); err == nil && resp.StatusCode == 200 {
			gw.peersMu.Lock()
			gw.peersCache = data
			gw.peersMu.Unlock()
		}
		resp.Body.Close()
	}

	req2, _ := http.NewRequest("GET", cfg.BrokerURL+"/events?limit=20", nil)
	if cfg.Secret != "" {
		req2.Header.Set("Authorization", "Bearer "+cfg.Secret)
	}
	if resp, err := client.Do(req2); err == nil {
		if data, err := io.ReadAll(resp.Body); err == nil && resp.StatusCode == 200 {
			gw.peersMu.Lock()
			gw.eventsJSON = data
			gw.peersMu.Unlock()
		}
		resp.Body.Close()
	}
}

// --- LLM health collection ---

func (gw *Gridwatch) collectLLMLoop(ctx context.Context) {
	for {
		gw.fetchLLM()
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}

func (gw *Gridwatch) fetchLLM() {
	client := &http.Client{Timeout: 3 * time.Second}
	llmBase := gw.config.LLMURL
	result := map[string]any{"status": "offline"}

	if resp, err := client.Get(llmBase + "/health"); err == nil {
		defer resp.Body.Close()
		var h map[string]any
		json.NewDecoder(resp.Body).Decode(&h)
		if s, ok := h["status"].(string); ok && s == "ok" {
			result["status"] = "online"
			result["health"] = s
		}
	}

	if resp, err := client.Get(llmBase + "/props"); err == nil {
		defer resp.Body.Close()
		var p map[string]any
		json.NewDecoder(resp.Body).Decode(&p)
		if m, ok := p["model_alias"].(string); ok {
			result["model"] = m
		}
		if ts, ok := p["total_slots"].(float64); ok {
			result["total_slots"] = int(ts)
		}
	}

	if resp, err := client.Get(llmBase + "/slots"); err == nil {
		defer resp.Body.Close()
		var slots []map[string]any
		json.NewDecoder(resp.Body).Decode(&slots)
		var out []map[string]any
		for _, s := range slots {
			slot := map[string]any{
				"id":         s["id"],
				"processing": s["is_processing"],
			}
			if nt, ok := s["next_token"].([]any); ok && len(nt) > 0 {
				if tok, ok := nt[0].(map[string]any); ok {
					slot["decoded"] = tok["n_decoded"]
					slot["remaining"] = tok["n_remain"]
				}
			}
			out = append(out, slot)
		}
		result["slots"] = out
	}

	if resp, err := client.Get(llmBase + "/metrics"); err == nil {
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		metrics := map[string]float64{}
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
				continue
			}
			parts := strings.Fields(line)
			if len(parts) == 2 {
				key := strings.Replace(parts[0], "llamacpp:", "", 1)
				if v, err := strconv.ParseFloat(parts[1], 64); err == nil {
					metrics[key] = v
				}
			}
		}
		result["metrics"] = metrics
	}

	data, _ := json.Marshal(result)
	gw.llmMu.Lock()
	gw.llmCache = data
	gw.llmMu.Unlock()
}

// --- NATS subscription ---

func (gw *Gridwatch) subscribeNATS(ctx context.Context) {
	natsURL := gw.config.NATSURL
	opts := []nats.Option{nats.Timeout(5 * time.Second)}
	if cfg.NatsToken != "" {
		opts = append(opts, nats.Token(cfg.NatsToken))
	}

	nc, err := nats.Connect(natsURL, opts...)
	if err != nil {
		log.Printf("[gridwatch] nats connect failed: %v", err)
		return
	}
	defer nc.Close()

	js, err := jetstream.New(nc)
	if err != nil {
		log.Printf("[gridwatch] jetstream init failed: %v", err)
		return
	}

	consumer, err := js.CreateOrUpdateConsumer(ctx, "FLEET", jetstream.ConsumerConfig{
		Durable:       "gridwatch-go",
		FilterSubject: "fleet.>",
		DeliverPolicy: jetstream.DeliverLastPolicy,
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	if err != nil {
		log.Printf("[gridwatch] jetstream consumer failed: %v", err)
		return
	}

	iter, err := consumer.Messages()
	if err != nil {
		log.Printf("[gridwatch] jetstream messages failed: %v", err)
		return
	}
	defer iter.Stop()

	gw.natsMu.Lock()
	gw.natsConn = true
	gw.natsMu.Unlock()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		msg, err := iter.Next()
		if err != nil {
			continue
		}

		var event map[string]any
		if json.Unmarshal(msg.Data(), &event) != nil {
			msg.Ack()
			continue
		}

		gw.natsMu.Lock()

		if _, ok := event["battery"]; ok {
			gw.willyv4 = msg.Data()
		} else if _, ok := event["power"]; ok {
			gw.willyv4 = msg.Data()
		} else if _, ok := event["attention"]; ok {
			gw.willyv4 = msg.Data()
		} else if _, ok := event["type"]; ok {
			gw.natsEvents = append([]json.RawMessage{msg.Data()}, gw.natsEvents...)
			if len(gw.natsEvents) > 50 {
				gw.natsEvents = gw.natsEvents[:50]
			}

			t, _ := event["type"].(string)
			if strings.HasPrefix(t, "daemon_") {
				gw.daemonRuns = append([]json.RawMessage{msg.Data()}, gw.daemonRuns...)
				if len(gw.daemonRuns) > 20 {
					gw.daemonRuns = gw.daemonRuns[:20]
				}
			}

			// Only emit to ticker if event is recent (not a historical replay).
			if ts, ok := event["timestamp"].(string); ok {
				if pt, err := time.Parse(time.RFC3339, ts); err == nil && time.Since(pt) < 60*time.Second {
					gw.emitNATSTickerEvent(event, t)
				}
			}
		}

		gw.natsMu.Unlock()
		msg.Ack()
	}
}

// --- HTTP handlers ---

func jsonResponse(w http.ResponseWriter, data []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(data)
}

func (gw *Gridwatch) handleStats(w http.ResponseWriter, r *http.Request) {
	gw.mu.RLock()
	defer gw.mu.RUnlock()
	data, _ := json.Marshal(map[string]any{
		"timestamp": gw.statsTime,
		"machines":  gw.stats,
	})
	jsonResponse(w, data)
}

func (gw *Gridwatch) handlePeers(w http.ResponseWriter, r *http.Request) {
	gw.peersMu.RLock()
	peers := gw.peersCache
	events := gw.eventsJSON
	gw.peersMu.RUnlock()

	if peers == nil {
		peers = []byte("[]")
	}
	if events == nil {
		events = []byte("[]")
	}

	out := []byte(`{"peers":`)
	out = append(out, peers...)
	out = append(out, `,"events":`...)
	out = append(out, events...)
	out = append(out, '}')
	jsonResponse(w, out)
}

func (gw *Gridwatch) handleLLM(w http.ResponseWriter, r *http.Request) {
	gw.llmMu.RLock()
	data := gw.llmCache
	gw.llmMu.RUnlock()
	if data == nil {
		data = []byte(`{"status":"unknown"}`)
	}
	jsonResponse(w, data)
}

func (gw *Gridwatch) handleNATS(w http.ResponseWriter, r *http.Request) {
	gw.natsMu.RLock()
	result := map[string]any{
		"connected":   gw.natsConn,
		"events":      gw.natsEvents,
		"daemon_runs": gw.daemonRuns,
	}
	gw.natsMu.RUnlock()
	data, _ := json.Marshal(result)
	jsonResponse(w, data)
}

func (gw *Gridwatch) handleDaemons(w http.ResponseWriter, r *http.Request) {
	gw.natsMu.RLock()
	runs := gw.daemonRuns
	gw.natsMu.RUnlock()
	data, _ := json.Marshal(map[string]any{"runs": runs})
	jsonResponse(w, data)
}

func (gw *Gridwatch) handleWillyv4(w http.ResponseWriter, r *http.Request) {
	gw.natsMu.RLock()
	data := gw.willyv4
	gw.natsMu.RUnlock()
	if data == nil {
		data = []byte("{}")
	}
	jsonResponse(w, data)
}

func (gw *Gridwatch) handleServices(w http.ResponseWriter, r *http.Request) {
	gw.svcMu.RLock()
	data := gw.svcCache
	gw.svcMu.RUnlock()
	if data == nil {
		data = []byte("{}")
	}
	jsonResponse(w, data)
}

// emitNATSTickerEvent pushes a ticker event for NATS fleet events.
func (gw *Gridwatch) emitNATSTickerEvent(event map[string]any, typ string) {
	peerID, _ := event["peer_id"].(string)
	machine, _ := event["machine"].(string)
	data, _ := event["data"].(string)

	// Parse trigger and duration from data field ("trigger=nats:summary_changed duration=30s").
	trigger := ""
	duration := ""
	for _, part := range strings.Fields(data) {
		if strings.HasPrefix(part, "trigger=") {
			trigger = strings.TrimPrefix(part, "trigger=")
		}
		if strings.HasPrefix(part, "duration=") {
			duration = strings.TrimPrefix(part, "duration=")
		}
	}

	switch typ {
	case "daemon_complete":
		detail := duration
		if trigger != "" {
			detail += " via " + trigger
		}
		gw.ticker.Push("daemon", "info", peerID+" done "+duration, detail)
	case "daemon_failed":
		gw.ticker.Push("daemon", "error", peerID+" FAILED", trigger)
	case "peer_joined":
		gw.ticker.Push("peer", "info", machine+" joined", "")
	case "peer_left":
		gw.ticker.Push("peer", "warn", machine+" left", "")
	}
}

// emitStatsTickerEvents pushes ticker events when machine status changes.
// Debounces: requires 2 consecutive polls with same status before emitting (prevents flapping).
func (gw *Gridwatch) emitStatsTickerEvents(stats map[string]*MachineStats) {
	firstRun := len(gw.prevStatuses) == 0

	for id, s := range stats {
		prev := gw.prevStatuses[id]

		if prev == s.Status {
			gw.statusCount[id]++
		} else {
			gw.statusCount[id] = 1
		}

		// Only emit after status is stable for 2+ consecutive polls (debounce flapping).
		stable := gw.statusCount[id] >= 2

		if firstRun {
			// Seed: report initial state.
			if s.Status == "online" {
				gw.ticker.Push("peer", "info", id+" online", s.Specs)
			} else {
				gw.ticker.Push("peer", "warn", id+" "+s.Status, "")
			}
		} else if prev != s.Status && stable {
			if s.Status == "offline" || s.Status == "timeout" {
				gw.ticker.Push("peer", "warn", id+" went "+s.Status, "")
			} else if s.Status == "online" {
				gw.ticker.Push("peer", "info", id+" back online", "")
			}
		}
		gw.prevStatuses[id] = s.Status

		if s.DiskPct >= 85 {
			if !gw.prevDiskAlerts[id] {
				gw.ticker.Push("disk", "error", id+" disk "+strconv.FormatFloat(s.DiskPct, 'f', 1, 64)+"%", "")
				gw.prevDiskAlerts[id] = true
			}
		} else {
			if gw.prevDiskAlerts[id] {
				gw.ticker.Push("disk", "info", id+" disk recovered "+strconv.FormatFloat(s.DiskPct, 'f', 1, 64)+"%", "")
			}
			gw.prevDiskAlerts[id] = false
		}
	}
}
