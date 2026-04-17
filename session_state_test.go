package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// T8: session state file on register. Covers directory resolution, atomic
// write shape, and the EphemeralFallback flag semantics that a SessionStart
// hook relies on to warn about T6 collision fallbacks.

func TestSessionStateDir_XDG(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/tmp/xdg-test-cache")
	got := sessionStateDir()
	want := "/tmp/xdg-test-cache/claude-peers"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSessionStateDir_FallbackToHome(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "")
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	got := sessionStateDir()
	want := filepath.Join(tmp, ".cache", "claude-peers")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestWriteSessionStateFile_RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)

	state := SessionState{
		SessionID:           "abc123",
		AgentName:           "keeper",
		ConfiguredAgentName: "keeper",
		EphemeralFallback:   false,
		CWD:                 "/home/willy/projects/claude-peers",
		Machine:             "omarchy",
		PID:                 4242,
		ParentPID:           1234,
		RegisteredAt:        "2026-04-17T00:00:00Z",
	}

	path := writeSessionStateFile(1234, state)
	if path == "" {
		t.Fatal("expected non-empty path, got empty")
	}
	if filepath.Base(path) != "current-1234.json" {
		t.Fatalf("expected filename current-1234.json, got %q", filepath.Base(path))
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var got SessionState
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got != state {
		t.Fatalf("round-trip mismatch:\n got  %+v\n want %+v", got, state)
	}
}

func TestWriteSessionStateFile_EphemeralFallback(t *testing.T) {
	// Semantic fixture: when T6 zeros agentName due to collision,
	// ConfiguredAgentName is preserved and EphemeralFallback is true.
	// The SessionStart hook reads these two fields to decide whether to
	// warn the user about the configured-but-unclaimed name.
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)

	state := SessionState{
		SessionID:           "xyz789",
		AgentName:           "", // T6 zeroed it
		ConfiguredAgentName: "jim",
		EphemeralFallback:   true,
		CWD:                 "/home/willy/hfl-projects/astrobot",
		Machine:             "omarchy",
		PID:                 5555,
		ParentPID:           6666,
		RegisteredAt:        "2026-04-17T00:00:00Z",
	}
	path := writeSessionStateFile(6666, state)
	if path == "" {
		t.Fatal("expected non-empty path")
	}

	data, _ := os.ReadFile(path)
	var got SessionState
	json.Unmarshal(data, &got)

	if got.AgentName != "" {
		t.Errorf("expected AgentName empty on fallback, got %q", got.AgentName)
	}
	if got.ConfiguredAgentName != "jim" {
		t.Errorf("expected ConfiguredAgentName=jim, got %q", got.ConfiguredAgentName)
	}
	if !got.EphemeralFallback {
		t.Error("expected EphemeralFallback=true")
	}
}

func TestWriteSessionStateFile_OverwritesExisting(t *testing.T) {
	// A restarted MCP server with the same parent PID should atomically
	// replace the prior state (claude-code keeps its PID across a session
	// reconnect, only the stdio child respawns).
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)

	first := SessionState{SessionID: "first", ParentPID: 42, RegisteredAt: "t1"}
	writeSessionStateFile(42, first)

	second := SessionState{SessionID: "second", ParentPID: 42, RegisteredAt: "t2"}
	path := writeSessionStateFile(42, second)
	if path == "" {
		t.Fatal("expected non-empty path on overwrite")
	}

	data, _ := os.ReadFile(path)
	var got SessionState
	json.Unmarshal(data, &got)
	if got.SessionID != "second" {
		t.Fatalf("expected SessionID=second after overwrite, got %q", got.SessionID)
	}

	// No .tmp leftover.
	tmpLeftover := path + ".tmp"
	if _, err := os.Stat(tmpLeftover); !os.IsNotExist(err) {
		t.Errorf(".tmp file should not exist after successful rename: %v", err)
	}
}
