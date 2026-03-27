package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseNATSVarz(t *testing.T) {
	varzJSON := `{
		"version": "2.10.0",
		"uptime": "1d2h3m",
		"connections": 5,
		"total_connections": 42,
		"in_msgs": 1000,
		"out_msgs": 2000,
		"in_bytes": 102400,
		"out_bytes": 204800,
		"slow_consumers": 0,
		"mem": 16777216
	}`

	var varz struct {
		Version       string `json:"version"`
		Uptime        string `json:"uptime"`
		Connections   int    `json:"connections"`
		TotalConns    int    `json:"total_connections"`
		InMsgs        int64  `json:"in_msgs"`
		OutMsgs       int64  `json:"out_msgs"`
		InBytes       int64  `json:"in_bytes"`
		OutBytes      int64  `json:"out_bytes"`
		SlowConsumers int    `json:"slow_consumers"`
		Mem           int64  `json:"mem"`
	}
	if err := json.Unmarshal([]byte(varzJSON), &varz); err != nil {
		t.Fatalf("unmarshal varz: %v", err)
	}

	if varz.Version != "2.10.0" {
		t.Errorf("version = %q, want 2.10.0", varz.Version)
	}
	if varz.Connections != 5 {
		t.Errorf("connections = %d, want 5", varz.Connections)
	}
	if varz.TotalConns != 42 {
		t.Errorf("total_connections = %d, want 42", varz.TotalConns)
	}
	if varz.InMsgs != 1000 {
		t.Errorf("in_msgs = %d, want 1000", varz.InMsgs)
	}
	if varz.OutMsgs != 2000 {
		t.Errorf("out_msgs = %d, want 2000", varz.OutMsgs)
	}
	if varz.Mem != 16777216 {
		t.Errorf("mem = %d, want 16777216", varz.Mem)
	}
}

func TestParseNATSConnz(t *testing.T) {
	connzJSON := `{
		"num_connections": 2,
		"connections": [
			{
				"name": "client-1",
				"ip": "10.0.0.1",
				"lang": "go",
				"in_msgs": 100,
				"out_msgs": 200,
				"num_subscriptions": 3,
				"pending_bytes": 0
			},
			{
				"name": "client-2",
				"ip": "10.0.0.2",
				"lang": "python",
				"in_msgs": 50,
				"out_msgs": 75,
				"num_subscriptions": 1,
				"pending_bytes": 512
			}
		]
	}`

	var connz struct {
		Connections []struct {
			Name    string `json:"name"`
			IP      string `json:"ip"`
			Lang    string `json:"lang"`
			InMsgs  int64  `json:"in_msgs"`
			OutMsgs int64  `json:"out_msgs"`
			Subs    int    `json:"num_subscriptions"`
			Pending int    `json:"pending_bytes"`
		} `json:"connections"`
	}
	if err := json.Unmarshal([]byte(connzJSON), &connz); err != nil {
		t.Fatalf("unmarshal connz: %v", err)
	}

	if len(connz.Connections) != 2 {
		t.Fatalf("connections count = %d, want 2", len(connz.Connections))
	}
	c0 := connz.Connections[0]
	if c0.Name != "client-1" {
		t.Errorf("conn[0].name = %q, want client-1", c0.Name)
	}
	if c0.Lang != "go" {
		t.Errorf("conn[0].lang = %q, want go", c0.Lang)
	}
	if c0.InMsgs != 100 {
		t.Errorf("conn[0].in_msgs = %d, want 100", c0.InMsgs)
	}
	if c0.Subs != 3 {
		t.Errorf("conn[0].subs = %d, want 3", c0.Subs)
	}

	c1 := connz.Connections[1]
	if c1.Pending != 512 {
		t.Errorf("conn[1].pending = %d, want 512", c1.Pending)
	}
}

func TestNATSMonitorHandler(t *testing.T) {
	gw := &Gridwatch{
		config: GridwatchConfig{Port: 0},
		stats:  make(map[string]*MachineStats),
	}

	// Test with no cache (nil) — should return default JSON.
	rec := httptest.NewRecorder()
	gw.handleNATSMonitor(rec, httptest.NewRequest("GET", "/api/nats-stats", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if rec.Header().Get("Content-Type") != "application/json" {
		t.Errorf("content-type = %q, want application/json", rec.Header().Get("Content-Type"))
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}

	// Test with populated cache.
	sample := NATSMonitorData{
		Timestamp: "2026-01-01T00:00:00Z",
		Server: NATSServerInfo{
			Version:     "2.10.0",
			Connections: 3,
			InMsgs:      500,
		},
		Connections: []NATSConnInfo{
			{Name: "test-conn", IP: "192.168.1.1", Lang: "go", InMsgs: 10, OutMsgs: 20, Subs: 2},
		},
		Streams: []NATSStreamInfo{
			{Name: "FLEET", Messages: 1000, Bytes: 512000, ConsumerCount: 2},
		},
	}
	raw, _ := json.Marshal(sample)
	gw.natsMonMu.Lock()
	gw.natsMonCache = raw
	gw.natsMonMu.Unlock()

	rec = httptest.NewRecorder()
	gw.handleNATSMonitor(rec, httptest.NewRequest("GET", "/api/nats-stats", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}

	var result NATSMonitorData
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("response unmarshal: %v", err)
	}
	if result.Server.Version != "2.10.0" {
		t.Errorf("server.version = %q, want 2.10.0", result.Server.Version)
	}
	if result.Server.Connections != 3 {
		t.Errorf("server.connections = %d, want 3", result.Server.Connections)
	}
	if len(result.Connections) != 1 {
		t.Fatalf("connections count = %d, want 1", len(result.Connections))
	}
	if result.Connections[0].Name != "test-conn" {
		t.Errorf("connections[0].name = %q, want test-conn", result.Connections[0].Name)
	}
	if len(result.Streams) != 1 {
		t.Fatalf("streams count = %d, want 1", len(result.Streams))
	}
	if result.Streams[0].Name != "FLEET" {
		t.Errorf("streams[0].name = %q, want FLEET", result.Streams[0].Name)
	}
	if result.Streams[0].Messages != 1000 {
		t.Errorf("streams[0].messages = %d, want 1000", result.Streams[0].Messages)
	}
}
