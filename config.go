package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config holds all runtime configuration for claude-peers.
// Loaded once at startup from file → env overrides → defaults.
type Config struct {
	// Role determines whether this instance runs a broker or connects to one.
	// "broker" = run the HTTP broker daemon locally.
	// "client" = connect to an existing broker (default).
	Role string `json:"role"`

	// BrokerURL is the HTTP endpoint of the broker.
	// Clients use this to register, send messages, etc.
	BrokerURL string `json:"broker_url"`

	// Listen is the address the broker binds to.
	// Only used when Role is "broker".
	// Use "0.0.0.0:7899" or a Tailscale IP to accept remote peers.
	Listen string `json:"listen"`

	// MachineName identifies this machine in the peer network.
	// Defaults to os.Hostname().
	MachineName string `json:"machine_name"`

	// DBPath is the SQLite database path for the broker.
	// Defaults to ~/.claude-peers.db.
	DBPath string `json:"db_path"`

	// StaleTimeout is how many seconds without a heartbeat before
	// a peer is considered stale and removed. Defaults to 300.
	StaleTimeout int `json:"stale_timeout"`

	// NatsURL is the NATS server address. Defaults to deriving from BrokerURL.
	NatsURL string `json:"nats_url"`

	// DaemonDir is the directory containing daemon definitions.
	// Defaults to ./daemons or ~/claude-peers-daemons.
	DaemonDir string `json:"daemon_dir"`

	// AgentBin is the path to the vinayprograms/agent binary.
	// Defaults to searching PATH, then ~/projects/vinay-agent/bin/agent.
	AgentBin string `json:"agent_bin"`

	// LLMBaseURL is the OpenAI-compatible LLM endpoint for daemons.
	// Defaults to http://127.0.0.1:4000/v1.
	LLMBaseURL string `json:"llm_base_url"`

	// LLMModel is the default model for daemon workflows.
	LLMModel string `json:"llm_model"`
}

// cfg is the global config, loaded once at startup.
var cfg Config

func initConfig() {
	cfg = loadConfig()
}

func loadConfig() Config {
	c := defaultConfig()

	if data, err := os.ReadFile(configPath()); err == nil {
		json.Unmarshal(data, &c)
	}

	// Env overrides take priority over config file.
	if v := os.Getenv("CLAUDE_PEERS_BROKER_URL"); v != "" {
		c.BrokerURL = v
	}
	if v := os.Getenv("CLAUDE_PEERS_LISTEN"); v != "" {
		c.Listen = v
	}
	if v := os.Getenv("CLAUDE_PEERS_MACHINE"); v != "" {
		c.MachineName = v
	}
	if v := os.Getenv("CLAUDE_PEERS_DB"); v != "" {
		c.DBPath = v
	}

	if v := os.Getenv("CLAUDE_PEERS_NATS"); v != "" {
		c.NatsURL = v
	}
	if v := os.Getenv("CLAUDE_PEERS_DAEMONS"); v != "" {
		c.DaemonDir = v
	}
	if v := os.Getenv("AGENT_BIN"); v != "" {
		c.AgentBin = v
	}
	if v := os.Getenv("CLAUDE_PEERS_LLM_URL"); v != "" {
		c.LLMBaseURL = v
	}
	if v := os.Getenv("CLAUDE_PEERS_LLM_MODEL"); v != "" {
		c.LLMModel = v
	}

	// Legacy env var
	if v := os.Getenv("CLAUDE_PEERS_PORT"); v != "" {
		c.Listen = "127.0.0.1:" + v
		if c.BrokerURL == defaultConfig().BrokerURL {
			c.BrokerURL = "http://127.0.0.1:" + v
		}
	}

	return c
}

func defaultConfig() Config {
	hostname, _ := os.Hostname()
	return Config{
		Role:         "client",
		BrokerURL:    "http://127.0.0.1:7899",
		Listen:       "127.0.0.1:7899",
		MachineName:  hostname,
		DBPath:       defaultDBPath(),
		StaleTimeout: 300,
		NatsURL:      "",
		DaemonDir:    "",
		AgentBin:     "",
		LLMBaseURL:   "http://127.0.0.1:4000/v1",
		LLMModel:     "vertex_ai/claude-sonnet-4-6",
	}
}

func defaultDBPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude-peers.db")
}

func configDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "claude-peers")
}

func configPath() string {
	if p := os.Getenv("CLAUDE_PEERS_CONFIG"); p != "" {
		return p
	}
	return filepath.Join(configDir(), "config.json")
}

// writeConfig writes a config to the standard config path.
func writeConfig(c Config) error {
	dir := configDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath(), data, 0644)
}

// cliInit generates a config file for broker or client role.
func cliInit(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, `Usage:
  claude-peers init broker                    Set up this machine as the broker
  claude-peers init client <broker-url>       Connect to a remote broker

Examples:
  claude-peers init broker
  claude-peers init client http://100.109.211.128:7899`)
		os.Exit(1)
	}

	c := defaultConfig()

	switch args[0] {
	case "broker":
		c.Role = "broker"
		c.Listen = "0.0.0.0:7899"
		c.BrokerURL = "http://127.0.0.1:7899"

	case "client":
		c.Role = "client"
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Error: client requires a broker URL")
			fmt.Fprintln(os.Stderr, "  claude-peers init client http://<broker-ip>:7899")
			os.Exit(1)
		}
		c.BrokerURL = args[1]

	default:
		fmt.Fprintf(os.Stderr, "Unknown role: %s (use 'broker' or 'client')\n", args[0])
		os.Exit(1)
	}

	if err := writeConfig(c); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing config: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Config written to %s\n\n", configPath())
	fmt.Printf("  role:         %s\n", c.Role)
	fmt.Printf("  machine_name: %s\n", c.MachineName)
	fmt.Printf("  broker_url:   %s\n", c.BrokerURL)
	if c.Role == "broker" {
		fmt.Printf("  listen:       %s\n", c.Listen)
		fmt.Printf("  db_path:      %s\n", c.DBPath)
	}
	fmt.Println()

	if c.Role == "broker" {
		fmt.Println("Start the broker with: claude-peers broker")
	} else {
		fmt.Println("The MCP server will connect to the broker automatically.")
		fmt.Println("Make sure the broker is running on the remote machine.")
	}
}
