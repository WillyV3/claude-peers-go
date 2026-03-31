package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	crypto_rand "crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	pollInterval      = 1 * time.Second
	heartbeatInterval = 15 * time.Second
)

var authToken string

func loadAuthToken() string {
	if v := os.Getenv("CLAUDE_PEERS_TOKEN"); v != "" {
		return v
	}
	t, err := LoadToken(configDir())
	if err != nil {
		return ""
	}
	return t
}

// brokerFetch delegates to cliFetch -- one implementation for all broker HTTP calls.
func brokerFetch(path string, body any, result any) error {
	return cliFetch(path, body, result)
}

func isBrokerAlive() bool {
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(cfg.BrokerURL + "/health") // /health is always public
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

// isLocalBroker returns true if the broker URL points to localhost.
// We only auto-start the broker when it's local.
func isLocalBroker() bool {
	url := cfg.BrokerURL
	return strings.Contains(url, "127.0.0.1") || strings.Contains(url, "localhost")
}

func ensureBroker() error {
	if isBrokerAlive() {
		logMCP("Broker available at %s", cfg.BrokerURL)
		return nil
	}

	if !isLocalBroker() {
		return fmt.Errorf("remote broker at %s is not reachable", cfg.BrokerURL)
	}

	logMCP("Starting local broker daemon...")
	exe, err := os.Executable()
	if err != nil {
		return err
	}

	cmd := exec.Command(exe, "broker")
	cmd.Stdout = nil
	cmd.Stderr = os.Stderr
	cmd.Stdin = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start broker: %w", err)
	}
	go cmd.Wait()

	for range 30 {
		time.Sleep(200 * time.Millisecond)
		if isBrokerAlive() {
			logMCP("Broker started")
			return nil
		}
	}
	return fmt.Errorf("broker failed to start after 6s")
}

func verifyBrokerIdentity() error {
	rootPubPath := filepath.Join(configDir(), rootPubKeyFile)
	rootPub, err := LoadPublicKey(rootPubPath)
	if err != nil {
		logMCP("Warning: no root.pub found, skipping broker verification")
		return nil // soft-fail if no root.pub
	}

	// Generate random nonce
	nonceBytes := make([]byte, 32)
	if _, err := crypto_rand.Read(nonceBytes); err != nil {
		return fmt.Errorf("generate nonce: %w", err)
	}
	nonce := base64.RawURLEncoding.EncodeToString(nonceBytes)

	// Send challenge to broker
	body, _ := json.Marshal(ChallengeRequest{Nonce: nonce})
	client := &http.Client{Timeout: 5 * time.Second}
	req, _ := http.NewRequest("POST", cfg.BrokerURL+"/challenge", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		logMCP("Warning: broker challenge failed: %v", err)
		return nil // soft-fail on network error
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		logMCP("Warning: broker challenge returned %d", resp.StatusCode)
		return nil
	}

	var challenge ChallengeResponse
	if err := json.NewDecoder(resp.Body).Decode(&challenge); err != nil {
		return fmt.Errorf("decode challenge response: %w", err)
	}

	// Verify nonce matches
	if challenge.Nonce != nonce {
		return fmt.Errorf("broker returned wrong nonce")
	}

	// Decode signature
	sig, err := base64.RawURLEncoding.DecodeString(challenge.Signature)
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}

	// Verify signature using root public key
	if !ed25519.Verify(rootPub, []byte(nonce), sig) {
		return fmt.Errorf("BROKER IDENTITY VERIFICATION FAILED: signature does not match root.pub")
	}

	logMCP("Broker identity verified (challenge-response OK)")
	return nil
}

func runServer(ctx context.Context) error {
	if err := ensureBroker(); err != nil {
		return err
	}

	if err := verifyBrokerIdentity(); err != nil {
		// Soft-fail for now -- log warning but continue
		logMCP("WARNING: %v", err)
	}

	authToken = loadAuthToken()
	if authToken == "" {
		logMCP("WARNING: no auth token found -- broker requests will be unauthenticated")
	}

	cwd, _ := os.Getwd()
	root := gitRoot(cwd)
	tty := getTTY()
	branch := gitBranch(cwd)
	project := autoProject(cwd, root)

	name := autoName(cfg.MachineName, project, tty)
	if branch != "" && project != "" {
		name = project + "@" + branch
	}

	logMCP("Name: %s", name)
	logMCP("CWD: %s", cwd)
	logMCP("Broker: %s", cfg.BrokerURL)

	// Generate LLM summary in background (non-blocking).
	files := recentFiles(cwd, 10)
	summaryCh := make(chan string, 1)
	go func() {
		summaryCh <- generateSummary(cwd, root, branch, files)
	}()

	var initialSummary string
	select {
	case s := <-summaryCh:
		initialSummary = s
	case <-time.After(5 * time.Second):
		logMCP("Auto-summary timed out")
	}

	var reg RegisterResponse
	if err := brokerFetch("/register", RegisterRequest{
		PID:     os.Getpid(),
		Machine: cfg.MachineName,
		CWD:     cwd,
		GitRoot: root,
		TTY:     tty,
		Name:    name,
		Project: project,
		Branch:  branch,
		Summary: initialSummary,
	}, &reg); err != nil {
		return fmt.Errorf("register: %w", err)
	}
	myID := reg.ID
	logMCP("Registered as %s (%s)", name, myID)

	// Fetch fleet memory from broker and write locally.
	go syncFleetMemory()

	// Apply late summary if initial timed out.
	if initialSummary == "" {
		go func() {
			if s := <-summaryCh; s != "" {
				brokerFetch("/set-summary", SetSummaryRequest{ID: myID, Summary: s}, nil)
				logMCP("Summary: %s", s)
			}
		}()
	}

	// Refresh summary periodically (every 5 min) so it stays current.
	go func() {
		for {
			time.Sleep(5 * time.Minute)
			newBranch := gitBranch(cwd)
			newFiles := recentFiles(cwd, 10)
			if s := generateSummary(cwd, root, newBranch, newFiles); s != "" {
				brokerFetch("/set-summary", SetSummaryRequest{ID: myID, Summary: s}, nil)
			}
		}
	}()

	t := newMCPTransport()

	defer func() {
		brokerFetch("/unregister", UnregisterRequest{ID: myID}, nil)
		logMCP("Unregistered from broker")
	}()

	var wg sync.WaitGroup
	pollCtx, pollCancel := context.WithCancel(ctx)
	defer pollCancel()

	wg.Go(func() {
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-pollCtx.Done():
				return
			case <-ticker.C:
				pollAndPush(myID, cwd, root, t)
			}
		}
	})

	wg.Go(func() {
		ticker := time.NewTicker(heartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-pollCtx.Done():
				return
			case <-ticker.C:
				brokerFetch("/heartbeat", HeartbeatRequest{ID: myID}, nil)
			}
		}
	})

	for {
		req, err := t.readRequest()
		if err != nil {
			if err == io.EOF {
				break
			}
			logMCP("Read error: %v", err)
			break
		}

		switch req.Method {
		case "initialize":
			handleInitialize(req.ID, t)
		case "notifications/initialized":
			// Client ack
		case "tools/list":
			handleToolsList(req.ID, t)
		case "tools/call":
			handleToolCall(req.ID, req.Params, myID, cwd, root, t)
		default:
			if req.ID != nil {
				t.respondError(req.ID, -32601, fmt.Sprintf("Method not found: %s", req.Method))
			}
		}
	}

	pollCancel()
	wg.Wait()
	return nil
}

func syncFleetMemory() {
	client := http.Client{Timeout: 5 * time.Second}
	req, _ := http.NewRequest("GET", cfg.BrokerURL+"/fleet-memory", nil)
	if authToken != "" {
		req.Header.Set("Authorization", "Bearer "+authToken)
	}
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		return
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil || len(data) == 0 {
		return
	}

	memDir := claudeMemoryDir()
	os.MkdirAll(memDir, 0755)
	path := filepath.Join(memDir, "fleet-activity.md")
	os.WriteFile(path, data, 0644)
	updateMemoryIndex(memDir)
	logMCP("Fleet memory synced to %s (%d bytes)", path, len(data))
}

func handleToolCall(id any, params json.RawMessage, myID, cwd, root string, t *MCPTransport) {
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	json.Unmarshal(params, &call)

	switch call.Name {
	case "list_peers":
		var args struct {
			Scope string `json:"scope"`
		}
		json.Unmarshal(call.Arguments, &args)

		listReq := ListPeersRequest{
			Scope:     args.Scope,
			CWD:       cwd,
			GitRoot:   root,
			ExcludeID: myID,
		}
		if args.Scope == "machine" {
			listReq.Machine = cfg.MachineName
		}

		var peers []Peer
		err := brokerFetch("/list-peers", listReq, &peers)
		if err != nil {
			toolError(id, t, "Error listing peers: %v", err)
			return
		}

		if len(peers) == 0 {
			toolResult(id, t, "No other Claude Code instances found (scope: %s).", args.Scope)
			return
		}

		var sb strings.Builder
		fmt.Fprintf(&sb, "Found %d peer(s) (scope: %s):\n\n", len(peers), args.Scope)
		for _, p := range peers {
			displayName := p.Name
			if displayName == "" {
				displayName = p.Machine
			}
			fmt.Fprintf(&sb, "%s on %s (id: %s)\n", displayName, p.Machine, p.ID)
			if p.Project != "" {
				fmt.Fprintf(&sb, "  Project: %s", p.Project)
				if p.Branch != "" {
					fmt.Fprintf(&sb, " [%s]", p.Branch)
				}
				fmt.Fprintln(&sb)
			}
			fmt.Fprintf(&sb, "  CWD: %s\n", p.CWD)
			if p.Summary != "" {
				fmt.Fprintf(&sb, "  Summary: %s\n", p.Summary)
			}
			fmt.Fprintf(&sb, "  Last seen: %s\n\n", p.LastSeen)
		}
		toolResult(id, t, "%s", sb.String())

	case "send_message":
		var args struct {
			ToID    string `json:"to_id"`
			Message string `json:"message"`
		}
		json.Unmarshal(call.Arguments, &args)

		if args.ToID == "" {
			toolError(id, t, "to_id is required")
			return
		}

		var resp SendMessageResponse
		err := brokerFetch("/send-message", SendMessageRequest{
			FromID: myID,
			ToID:   args.ToID,
			Text:   args.Message,
		}, &resp)
		if err != nil {
			toolError(id, t, "Error sending message: %v", err)
			return
		}
		if !resp.OK {
			toolError(id, t, "Failed to send: %s", resp.Error)
			return
		}
		toolResult(id, t, "Message sent to peer %s", args.ToID)

	case "set_summary":
		var args struct {
			Summary string `json:"summary"`
		}
		json.Unmarshal(call.Arguments, &args)

		err := brokerFetch("/set-summary", SetSummaryRequest{
			ID:      myID,
			Summary: args.Summary,
		}, nil)
		if err != nil {
			toolError(id, t, "Error setting summary: %v", err)
			return
		}
		toolResult(id, t, "Summary updated: %q", args.Summary)

	case "set_name":
		var args struct {
			Name string `json:"name"`
		}
		json.Unmarshal(call.Arguments, &args)

		err := brokerFetch("/set-name", SetNameRequest{
			ID:   myID,
			Name: args.Name,
		}, nil)
		if err != nil {
			toolError(id, t, "Error setting name: %v", err)
			return
		}
		toolResult(id, t, "Name updated: %q", args.Name)

	case "check_messages":
		var resp PollMessagesResponse
		err := brokerFetch("/poll-messages", PollMessagesRequest{ID: myID}, &resp)
		if err != nil {
			toolError(id, t, "Error checking messages: %v", err)
			return
		}
		if len(resp.Messages) == 0 {
			toolResult(id, t, "No new messages.")
			return
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "%d new message(s):\n\n", len(resp.Messages))
		for _, m := range resp.Messages {
			// Look up sender info
			fromMachine, fromSummary := "", ""
			var peers []Peer
			if err := brokerFetch("/list-peers", ListPeersRequest{Scope: "all"}, &peers); err == nil {
				for _, p := range peers {
					if p.ID == m.FromID {
						fromMachine = p.Machine
						fromSummary = p.Summary
						break
					}
				}
			}
			fmt.Fprintf(&sb, "From %s on %s", m.FromID, fromMachine)
			if fromSummary != "" {
				fmt.Fprintf(&sb, " (%s)", fromSummary)
			}
			fmt.Fprintf(&sb, " at %s:\n%s\n\n---\n\n", m.SentAt, m.Text)
		}
		toolResult(id, t, "%s", sb.String())

	default:
		t.respondError(id, -32601, fmt.Sprintf("Unknown tool: %s", call.Name))
	}
}

func toolResult(id any, t *MCPTransport, format string, args ...any) {
	text := fmt.Sprintf(format, args...)
	t.respond(id, map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": text},
		},
	})
}

func toolError(id any, t *MCPTransport, format string, args ...any) {
	text := fmt.Sprintf(format, args...)
	t.respond(id, map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": text},
		},
		"isError": true,
	})
}

func pollAndPush(myID, cwd, root string, t *MCPTransport) {
	var resp PollMessagesResponse
	if err := brokerFetch("/poll-messages", PollMessagesRequest{ID: myID}, &resp); err != nil {
		return
	}

	for _, msg := range resp.Messages {
		fromSummary, fromCwd, fromMachine := "", "", ""
		var peers []Peer
		if err := brokerFetch("/list-peers", ListPeersRequest{Scope: "all"}, &peers); err == nil {
			for _, p := range peers {
				if p.ID == msg.FromID {
					fromSummary = p.Summary
					fromCwd = p.CWD
					fromMachine = p.Machine
					break
				}
			}
		}

		t.writeNotification("notifications/claude/channel", map[string]any{
			"content": msg.Text,
			"meta": map[string]any{
				"from_id":      msg.FromID,
				"from_machine": fromMachine,
				"from_summary": fromSummary,
				"from_cwd":     fromCwd,
				"sent_at":      msg.SentAt,
			},
		})

		logMCP("Pushed message from %s@%s: %.80s", msg.FromID, fromMachine, msg.Text)
	}
}
