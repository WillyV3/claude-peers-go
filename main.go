package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"time"
)

func main() {
	log.SetFlags(0)
	initConfig()

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	switch os.Args[1] {
	case "broker":
		if err := runBroker(ctx); err != nil {
			log.Fatal(err)
		}
	case "server":
		if err := runServer(ctx); err != nil {
			log.Fatal(err)
		}
	case "init":
		cliInit(os.Args[2:])
	case "config":
		cliShowConfig()
	case "status":
		cliStatus()
	case "peers":
		cliPeers()
	case "send":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "Usage: claude-peers send <peer-id> <message>")
			os.Exit(1)
		}
		cliSend(os.Args[2], strings.Join(os.Args[3:], " "))
	case "dream":
		cliDream()
	case "dream-watch":
		cliDreamWatch()
	case "supervisor":
		cliSupervisor(ctx)
	case "gridwatch":
		if err := runGridwatch(ctx); err != nil {
			log.Fatal(err)
		}
	case "kill-broker":
		cliKillBroker()
	default:
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`claude-peers - peer discovery and messaging for Claude Code

Usage:
  claude-peers init <role> [url]   Generate config (broker or client)
  claude-peers config              Show current config
  claude-peers broker              Start the broker daemon
  claude-peers server              Start MCP stdio server (used by Claude Code)
  claude-peers status              Show broker status and all peers
  claude-peers peers               List all peers
  claude-peers send <id> <msg>     Send a message to a peer
  claude-peers dream               Snapshot fleet state to Claude memory
  claude-peers dream-watch         Watch fleet via NATS and keep memory fresh
  claude-peers supervisor          Run daemon supervisor (manages agent workflows)
  claude-peers gridwatch           Start fleet health dashboard (reads gridwatch.json)
  claude-peers kill-broker         Stop the broker daemon

Setup:
  # On the broker machine (e.g. your always-on server):
  claude-peers init broker
  claude-peers broker

  # On each client machine:
  claude-peers init client http://<broker-ip>:7899`)
}

func cliFetch(path string, body any, result any) error {
	data, _ := json.Marshal(body)
	client := http.Client{Timeout: 3 * time.Second}

	var req *http.Request
	if body != nil {
		req, _ = http.NewRequest("POST", cfg.BrokerURL+path, bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req, _ = http.NewRequest("GET", cfg.BrokerURL+path, nil)
	}
	if cfg.Secret != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Secret)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%d: %s", resp.StatusCode, string(b))
	}
	if result != nil {
		return json.NewDecoder(resp.Body).Decode(result)
	}
	return nil
}

func cliShowConfig() {
	fmt.Printf("Config: %s\n\n", configPath())
	fmt.Printf("  role:         %s\n", cfg.Role)
	fmt.Printf("  machine_name: %s\n", cfg.MachineName)
	fmt.Printf("  broker_url:   %s\n", cfg.BrokerURL)
	fmt.Printf("  listen:       %s\n", cfg.Listen)
	fmt.Printf("  db_path:      %s\n", cfg.DBPath)
	fmt.Printf("  stale_timeout: %ds\n", cfg.StaleTimeout)
}

func cliStatus() {
	var health HealthResponse
	if err := cliFetch("/health", nil, &health); err != nil {
		fmt.Printf("Broker at %s is not reachable.\n", cfg.BrokerURL)
		return
	}
	fmt.Printf("Broker: %s (%d peer(s), host: %s)\n", health.Status, health.Peers, health.Machine)
	fmt.Printf("URL: %s\n", cfg.BrokerURL)

	if health.Peers > 0 {
		var peers []Peer
		cliFetch("/list-peers", ListPeersRequest{Scope: "all"}, &peers)
		fmt.Println("\nPeers:")
		for _, p := range peers {
			fmt.Printf("  %s  [%s]  PID:%d  %s\n", p.ID, p.Machine, p.PID, p.CWD)
			if p.Summary != "" {
				fmt.Printf("         %s\n", p.Summary)
			}
			if p.TTY != "" {
				fmt.Printf("         TTY: %s\n", p.TTY)
			}
			fmt.Printf("         Last seen: %s\n", p.LastSeen)
		}
	}
}

func cliPeers() {
	var peers []Peer
	if err := cliFetch("/list-peers", ListPeersRequest{Scope: "all"}, &peers); err != nil {
		fmt.Printf("Broker at %s is not reachable.\n", cfg.BrokerURL)
		return
	}
	if len(peers) == 0 {
		fmt.Println("No peers registered.")
		return
	}
	for _, p := range peers {
		fmt.Printf("%s  [%s]  PID:%d  %s\n", p.ID, p.Machine, p.PID, p.CWD)
		if p.Summary != "" {
			fmt.Printf("  Summary: %s\n", p.Summary)
		}
	}
}

func cliSend(toID, msg string) {
	var resp SendMessageResponse
	if err := cliFetch("/send-message", SendMessageRequest{
		FromID: "cli",
		ToID:   toID,
		Text:   msg,
	}, &resp); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if resp.OK {
		fmt.Printf("Message sent to %s\n", toID)
	} else {
		fmt.Fprintf(os.Stderr, "Failed: %s\n", resp.Error)
		os.Exit(1)
	}
}

func cliKillBroker() {
	var health HealthResponse
	if err := cliFetch("/health", nil, &health); err != nil {
		fmt.Println("Broker is not running.")
		return
	}
	fmt.Printf("Broker has %d peer(s). Shutting down...\n", health.Peers)

	port := strings.TrimPrefix(cfg.Listen, "0.0.0.0:")
	if strings.Contains(port, ":") {
		parts := strings.Split(port, ":")
		port = parts[len(parts)-1]
	}
	out, err := execOutput("lsof", "-ti", ":"+port)
	if err != nil {
		fmt.Println("Could not find broker process.")
		return
	}
	for pid := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		if pid != "" {
			execOutput("kill", pid)
		}
	}
	fmt.Println("Broker stopped.")
}

func execOutput(name string, args ...string) (string, error) {
	var buf bytes.Buffer
	cmd := execCommand(name, args...)
	cmd.Stdout = &buf
	err := cmd.Run()
	return buf.String(), err
}

func execCommand(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}
