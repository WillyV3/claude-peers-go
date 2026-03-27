package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseOutput(t *testing.T) {
	m := Machine{ID: "test", OS: "arch", Specs: "4C/16GB", IP: "10.0.0.1"}
	output := `CPU:42.5
MEM:8589934592 17179869184
DISK:107374182400 214748364800
PROC:claude,12.3
PROC:firefox,8.7
up 3 days, 2 hours`

	s := parseOutput(m, output)

	if s.Status != "online" {
		t.Errorf("status = %s, want online", s.Status)
	}
	if s.CPU != 42.5 {
		t.Errorf("cpu = %f, want 42.5", s.CPU)
	}
	if s.MemPct != 50.0 {
		t.Errorf("mem_pct = %f, want 50.0", s.MemPct)
	}
	if s.DiskPct != 50.0 {
		t.Errorf("disk_pct = %f, want 50.0", s.DiskPct)
	}
	if len(s.Processes) != 2 {
		t.Fatalf("processes = %d, want 2", len(s.Processes))
	}
	if s.Processes[0].Name != "claude" || s.Processes[0].MemPct != 12.3 {
		t.Errorf("proc[0] = %v, want claude/12.3", s.Processes[0])
	}
	if s.Uptime != "up 3 days, 2 hours" {
		t.Errorf("uptime = %q", s.Uptime)
	}
}

func TestParseOutputCPUCap(t *testing.T) {
	m := Machine{ID: "test", OS: "macos"}
	s := parseOutput(m, "CPU:150.0\n")
	if s.CPU != 100.0 {
		t.Errorf("cpu = %f, want 100.0 (capped)", s.CPU)
	}
}

func TestParseOutputEmpty(t *testing.T) {
	m := Machine{ID: "test", OS: "arch"}
	s := parseOutput(m, "")
	if s.Status != "online" {
		t.Errorf("status = %s", s.Status)
	}
	if s.CPU != 0 {
		t.Errorf("cpu = %f", s.CPU)
	}
}

func TestParseOutputLongProcName(t *testing.T) {
	m := Machine{ID: "test", OS: "arch"}
	s := parseOutput(m, "PROC:this-is-a-very-long-process-name-that-exceeds-twenty,5.5\n")
	if len(s.Processes) != 1 {
		t.Fatal("expected 1 process")
	}
	if len(s.Processes[0].Name) > 20 {
		t.Errorf("name length = %d, want <= 20", len(s.Processes[0].Name))
	}
}

func TestShellQuote(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"hello", "'hello'"},
		{"it's", "'it'\\''s'"},
		{"a b c", "'a b c'"},
	}
	for _, tc := range cases {
		got := shellQuote(tc.in)
		if got != tc.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestGridwatchHandlers(t *testing.T) {
	gw := &Gridwatch{
		config: GridwatchConfig{Port: 0},
		stats:  make(map[string]*MachineStats),
	}
	gw.stats["test"] = &MachineStats{ID: "test", Status: "online", CPU: 50}

	// Test /api/stats
	rec := httptest.NewRecorder()
	gw.handleStats(rec, httptest.NewRequest("GET", "/api/stats", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("stats: status = %d", rec.Code)
	}
	var statsResp map[string]any
	json.Unmarshal(rec.Body.Bytes(), &statsResp)
	machines, _ := statsResp["machines"].(map[string]any)
	if machines["test"] == nil {
		t.Error("stats: missing test machine")
	}

	// Test /api/peers (empty)
	rec = httptest.NewRecorder()
	gw.handlePeers(rec, httptest.NewRequest("GET", "/api/peers", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("peers: status = %d", rec.Code)
	}
	var peersResp map[string]any
	json.Unmarshal(rec.Body.Bytes(), &peersResp)
	if peersResp["peers"] == nil {
		t.Error("peers: missing peers key")
	}

	// Test /api/llm (empty)
	rec = httptest.NewRecorder()
	gw.handleLLM(rec, httptest.NewRequest("GET", "/api/llm", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("llm: status = %d", rec.Code)
	}

	// Test /api/nats
	rec = httptest.NewRecorder()
	gw.handleNATS(rec, httptest.NewRequest("GET", "/api/nats", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("nats: status = %d", rec.Code)
	}

	// Test /api/willyv4 (empty)
	rec = httptest.NewRecorder()
	gw.handleWillyv4(rec, httptest.NewRequest("GET", "/api/willyv4", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("willyv4: status = %d", rec.Code)
	}
	if rec.Body.String() != "{}" {
		t.Errorf("willyv4: expected empty object, got %s", rec.Body.String())
	}
}

func TestLoadGridwatchConfigMissing(t *testing.T) {
	// With no config file and no env override, should return defaults.
	t.Setenv("GRIDWATCH_CONFIG", "/tmp/nonexistent-gridwatch-config.json")
	gwc := loadGridwatchConfig()
	if gwc.Port != 8888 {
		t.Errorf("default port = %d, want 8888", gwc.Port)
	}
}

func TestHandleNATSMonitor(t *testing.T) {
	gw := &Gridwatch{
		config: GridwatchConfig{Port: 0},
		stats:  make(map[string]*MachineStats),
	}

	// Empty cache returns valid JSON default.
	rec := httptest.NewRecorder()
	gw.handleNATSMonitor(rec, httptest.NewRequest("GET", "/api/nats-stats", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("nats-stats empty: status = %d", rec.Code)
	}
	var emptyResp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &emptyResp); err != nil {
		t.Fatalf("nats-stats empty: invalid JSON: %v", err)
	}

	// Populate cache and verify round-trip.
	d := NATSMonitorData{
		Timestamp: "2026-01-01T00:00:00Z",
		Server:    NATSServerInfo{Version: "2.10.0", NumStreams: 3, NumMessages: 9999},
	}
	raw, _ := json.Marshal(d)
	gw.natsMonMu.Lock()
	gw.natsMonCache = raw
	gw.natsMonMu.Unlock()

	rec = httptest.NewRecorder()
	gw.handleNATSMonitor(rec, httptest.NewRequest("GET", "/api/nats-stats", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("nats-stats: status = %d", rec.Code)
	}
	var result NATSMonitorData
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("nats-stats: invalid JSON: %v", err)
	}
	if result.Server.Version != "2.10.0" {
		t.Errorf("nats-stats: version = %q, want 2.10.0", result.Server.Version)
	}
	if result.Server.NumStreams != 3 {
		t.Errorf("nats-stats: num_streams = %d, want 3", result.Server.NumStreams)
	}
	if result.Server.NumMessages != 9999 {
		t.Errorf("nats-stats: num_messages = %d, want 9999", result.Server.NumMessages)
	}
}
