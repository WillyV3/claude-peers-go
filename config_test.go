package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	c := defaultConfig()
	if c.Role != "client" {
		t.Fatalf("expected role client, got %s", c.Role)
	}
	if c.BrokerURL != "http://127.0.0.1:7899" {
		t.Fatalf("expected default broker URL, got %s", c.BrokerURL)
	}
	if c.StaleTimeout != 300 {
		t.Fatalf("expected stale timeout 300, got %d", c.StaleTimeout)
	}
	if c.MachineName == "" {
		t.Fatal("expected machine name from hostname")
	}
}

func TestLoadConfigFromFile(t *testing.T) {
	dir := t.TempDir()
	configFile := filepath.Join(dir, "config.json")
	os.WriteFile(configFile, []byte(`{"role":"broker","broker_url":"http://10.0.0.1:7899","machine_name":"test-host"}`), 0644)

	t.Setenv("CLAUDE_PEERS_CONFIG", configFile)
	c := loadConfig()
	if c.Role != "broker" {
		t.Fatalf("expected role broker, got %s", c.Role)
	}
	if c.BrokerURL != "http://10.0.0.1:7899" {
		t.Fatalf("expected custom broker URL, got %s", c.BrokerURL)
	}
}

func TestEnvOverridesConfig(t *testing.T) {
	dir := t.TempDir()
	configFile := filepath.Join(dir, "config.json")
	os.WriteFile(configFile, []byte(`{"broker_url":"http://from-file:7899"}`), 0644)

	t.Setenv("CLAUDE_PEERS_CONFIG", configFile)
	t.Setenv("CLAUDE_PEERS_BROKER_URL", "http://from-env:7899")
	c := loadConfig()
	if c.BrokerURL != "http://from-env:7899" {
		t.Fatalf("env should override config file, got %s", c.BrokerURL)
	}
}

func TestInvalidConfigFallsToDefaults(t *testing.T) {
	dir := t.TempDir()
	configFile := filepath.Join(dir, "config.json")
	os.WriteFile(configFile, []byte(`{invalid json`), 0644)

	t.Setenv("CLAUDE_PEERS_CONFIG", configFile)
	c := loadConfig()
	if c.Role != "client" {
		t.Fatalf("expected default role on invalid config, got %s", c.Role)
	}
}

func TestGenerateSecret(t *testing.T) {
	s := generateSecret()
	if len(s) < 10 {
		t.Fatalf("secret too short: %s", s)
	}
	if s[:3] != "cp-" {
		t.Fatalf("secret should start with cp-, got %s", s)
	}
	s2 := generateSecret()
	if s == s2 {
		t.Fatal("two generated secrets should differ")
	}
}

func TestSecretEnvOverride(t *testing.T) {
	t.Setenv("CLAUDE_PEERS_CONFIG", "/nonexistent")
	t.Setenv("CLAUDE_PEERS_SECRET", "my-secret")
	c := loadConfig()
	if c.Secret != "my-secret" {
		t.Fatalf("expected secret from env, got %s", c.Secret)
	}
}

func TestLegacyPortEnvVar(t *testing.T) {
	t.Setenv("CLAUDE_PEERS_CONFIG", "/nonexistent")
	t.Setenv("CLAUDE_PEERS_PORT", "9999")
	c := loadConfig()
	if c.Listen != "127.0.0.1:9999" {
		t.Fatalf("expected listen on port 9999, got %s", c.Listen)
	}
}
