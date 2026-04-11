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
	"strconv"
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

	// ADR-001: agent names are declared, not derived. Read from (in order):
	//   1. --as <name> flag (set via CLI arg before runMCP)
	//   2. CLAUDE_PEERS_AGENT env var
	//   3. .claude-peers-agent file in cwd
	// If none of these are set, this session is ephemeral -- it exists on the
	// network but cannot be messaged by name.
	agentName := resolveAgentName(cwd)
	if agentName != "" {
		logMCP("Agent: %s", agentName)
	} else {
		logMCP("Agent: <ephemeral>")
	}
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

	registerReq := RegisterRequest{
		AgentName: agentName,
		PID:       os.Getpid(),
		Machine:   cfg.MachineName,
		CWD:       cwd,
		GitRoot:   root,
		TTY:       tty,
		Project:   project,
		Branch:    branch,
		Summary:   initialSummary,
	}

	var reg RegisterResponse
	if err := brokerFetch("/register", registerReq, &reg); err != nil {
		return fmt.Errorf("register: %w", err)
	}

	// T6: if the configured agent name collides with a live session, fall back
	// to ephemeral registration instead of aborting the MCP server. Pre-T6
	// behaviour was to return an error and exit -- which claude-code surfaces
	// as "MCP failed to connect" with no context, because the detailed
	// collision error lives in subprocess stderr. That's a silent failure
	// from the user's POV and the wrong trade. An ephemeral fallback keeps
	// the session visible and messageable by session ID, logs the collision
	// loudly to stderr, and lets the user resolve it at their leisure.
	//
	// We only retry once and only when we actually sent a non-empty
	// AgentName. If the broker rejects an ephemeral register, that's a real
	// failure and still bubbles up.
	if !reg.OK && agentName != "" {
		logMCP("WARNING: configured agent name %q is already held by session %s on %s (cwd: %s, since: %s). "+
			"Falling back to ephemeral registration -- this session will be visible in list_peers and "+
			"addressable by session ID, but not by the configured name. Resolve the collision by killing "+
			"the holder, picking a different name in .claude-peers-agent / CLAUDE_PEERS_AGENT / --as, or "+
			"waiting for the holder to exit (names free 60s after stale sweep).",
			agentName, reg.HeldBySession, reg.HeldByMachine, reg.HeldByCWD, reg.HeldBySince)
		agentName = ""
		registerReq.AgentName = ""
		if err := brokerFetch("/register", registerReq, &reg); err != nil {
			return fmt.Errorf("register (ephemeral fallback after %q collision): %w", registerReq.AgentName, err)
		}
	}
	if !reg.OK {
		return fmt.Errorf("register failed: %s", reg.Error)
	}
	myID := reg.ID
	if agentName != "" {
		logMCP("Registered as %s (session %s)", agentName, myID)
	} else {
		logMCP("Registered as <ephemeral> (session %s)", myID)
	}

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

// writePeerRow renders one peer entry into the list_peers MCP tool result
// using the historical layout. When isSelf is true, the header line gets
// a "← this session" suffix so the caller can spot itself at a glance.
func writePeerRow(sb *strings.Builder, p Peer, isSelf bool) {
	if p.AgentName != "" {
		fmt.Fprintf(sb, "%s (agent) on %s [session %s]", p.AgentName, p.Machine, p.ID)
	} else {
		fmt.Fprintf(sb, "session %s on %s (ephemeral -- not addressable by name)", p.ID, p.Machine)
	}
	if isSelf {
		sb.WriteString("  ← this session")
	}
	sb.WriteString("\n")
	if p.Project != "" {
		fmt.Fprintf(sb, "  Project: %s", p.Project)
		if p.Branch != "" {
			fmt.Fprintf(sb, " [%s]", p.Branch)
		}
		fmt.Fprintln(sb)
	}
	fmt.Fprintf(sb, "  CWD: %s\n", p.CWD)
	if p.Summary != "" {
		fmt.Fprintf(sb, "  Summary: %s\n", p.Summary)
	}
	fmt.Fprintf(sb, "  Last seen: %s\n\n", p.LastSeen)
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

		// No ExcludeID -- list_peers should return ALL peers including this
		// session. Pre-T5 the MCP tool hardcoded ExcludeID=myID, which made
		// the caller invisible to itself and broke "am I registered?" /
		// "what's my session id?" introspection. The caller's row is now
		// printed first with a "← this session" marker so Claude can
		// answer those questions at a glance. All other consumers of
		// /list-peers (CLI peers/status/send, internal send_message lookup)
		// already pass empty ExcludeID, so this aligns the MCP tool with
		// the rest of the codebase. The broker primitive still supports
		// ExcludeID for callers that genuinely want "everyone but me".
		listReq := ListPeersRequest{
			Scope:   args.Scope,
			CWD:     cwd,
			GitRoot: root,
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
			toolResult(id, t, "No peers registered (scope: %s). The broker has no sessions at all.", args.Scope)
			return
		}

		// Locate this session in the result so we can print it first and
		// detect the "registered but missing" diagnostic case.
		selfIndex := -1
		for i, p := range peers {
			if p.ID == myID {
				selfIndex = i
				break
			}
		}

		// Edge case: only this session is registered, no others on the network.
		if len(peers) == 1 && selfIndex == 0 {
			var sb strings.Builder
			fmt.Fprintf(&sb, "Only this session is registered (scope: %s, no other peers).\n\n", args.Scope)
			writePeerRow(&sb, peers[0], true)
			toolResult(id, t, "%s", sb.String())
			return
		}

		var sb strings.Builder
		fmt.Fprintf(&sb, "Found %d peer(s) (scope: %s):\n\n", len(peers), args.Scope)

		// Print this session first if present, so Claude sees its own
		// identity at the top of the list and can immediately answer
		// "am I registered, and as what?".
		if selfIndex >= 0 {
			writePeerRow(&sb, peers[selfIndex], true)
		}
		for i, p := range peers {
			if i == selfIndex {
				continue
			}
			writePeerRow(&sb, p, false)
		}

		// Diagnostic: if the caller is not in the registry, surface it
		// loudly. This should be impossible under normal operation -- the
		// MCP server registers on startup -- so its presence is a real
		// signal that something is broken in the registration path.
		if selfIndex < 0 {
			fmt.Fprintf(&sb, "WARNING: this session [%s] is not in the peer registry. "+
				"Registration may have failed; messages addressed to this session "+
				"will not deliver. Try restarting the claude-peers MCP server.\n",
				myID)
		}

		toolResult(id, t, "%s", sb.String())

	case "send_message":
		var args struct {
			To      string `json:"to"`
			Message string `json:"message"`
		}
		json.Unmarshal(call.Arguments, &args)

		if args.To == "" {
			toolError(id, t, "to is required (agent name or session id)")
			return
		}

		// Default to ToAgent. Only route as ToSession if the target is the
		// literal session ID of a currently-live ephemeral peer (no agent name).
		// Never silently downgrade an offline-agent send -- that would drop
		// messages the caller meant to queue for when the agent reconnects.
		var peers []Peer
		brokerFetch("/list-peers", ListPeersRequest{Scope: "all"}, &peers)
		sendReq := SendMessageRequest{FromID: myID, Text: args.Message}
		var targetEphemeralSession string
		for _, p := range peers {
			if p.ID == args.To && p.AgentName == "" {
				targetEphemeralSession = args.To
				break
			}
		}
		if targetEphemeralSession != "" {
			sendReq.ToSession = targetEphemeralSession
		} else {
			sendReq.ToAgent = args.To
		}

		var resp SendMessageResponse
		err := brokerFetch("/send-message", sendReq, &resp)
		if err != nil {
			toolError(id, t, "Error sending message: %v", err)
			return
		}
		if !resp.OK {
			toolError(id, t, "Failed to send: %s", resp.Error)
			return
		}
		if resp.Queued {
			toolResult(id, t, "Message queued for agent %q (no live session holds it right now -- will deliver on reconnect).", args.To)
		} else {
			toolResult(id, t, "Message sent to %s", args.To)
		}

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

	case "claim_agent_name":
		var args struct {
			Name string `json:"name"`
		}
		json.Unmarshal(call.Arguments, &args)
		if args.Name == "" {
			toolError(id, t, "name is required")
			return
		}
		var resp ClaimAgentResponse
		err := brokerFetch("/claim-agent", ClaimAgentRequest{SessionID: myID, AgentName: args.Name}, &resp)
		if err != nil {
			toolError(id, t, "Error claiming agent name: %v", err)
			return
		}
		if !resp.OK {
			if resp.HeldBySession != "" {
				toolError(id, t, "Cannot claim %q: %s\n  held by session %s on %s (cwd: %s)\n  started: %s",
					args.Name, resp.Error, resp.HeldBySession, resp.HeldByMachine, resp.HeldByCWD, resp.HeldBySince)
			} else {
				toolError(id, t, "Cannot claim %q: %s", args.Name, resp.Error)
			}
			return
		}
		toolResult(id, t, "Claimed agent name: %s. Other sessions can now address this session as %q across restarts (as long as this session holds the name).", args.Name, args.Name)

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
		// Look up sender info once.
		var peers []Peer
		brokerFetch("/list-peers", ListPeersRequest{Scope: "all"}, &peers)
		for _, m := range resp.Messages {
			sender := m.FromAgent
			if sender == "" {
				sender = m.FromSession
			}
			var fromMachine, fromSummary string
			for _, p := range peers {
				if p.ID == m.FromSession {
					fromMachine = p.Machine
					fromSummary = p.Summary
					break
				}
			}
			fmt.Fprintf(&sb, "From %s on %s", sender, fromMachine)
			if fromSummary != "" {
				fmt.Fprintf(&sb, " (%s)", fromSummary)
			}
			fmt.Fprintf(&sb, " at %s:\n%s\n\n---\n\n", m.SentAt, m.Text)
			// ACK so it doesn't surface again.
			brokerFetch("/ack-message", AckMessageRequest{SessionID: myID, MessageID: m.ID}, nil)
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

// pushedIDs is the set of message IDs the push loop has already written as
// channel notifications. Prevents re-pushing the same message every tick.
// The check_messages tool call is the authoritative drain path and calls
// /poll-messages which sets delivered_at + ack_at -- at that point we can
// forget the ID. Until then we keep it in-memory so we don't spam.
var pushedIDs = struct {
	sync.Mutex
	m map[int]bool
}{m: make(map[int]bool)}

// pollAndPush pushes new messages via notifications/claude/channel for any
// MCP client that honors them, but does NOT consume the message from the
// broker queue. check_messages remains the authoritative drain: it calls
// /poll-messages (marks delivered_at), returns the content, and acks.
// If push works: the model sees it early and may call check_messages to drain.
// If push doesn't work: check_messages on next prompt still drains reliably.
// Either way, messages never get silently dropped between push and tool call.
func pollAndPush(myID, cwd, root string, t *MCPTransport) {
	var resp PollMessagesResponse
	if err := brokerFetch("/peek-messages", PollMessagesRequest{ID: myID}, &resp); err != nil {
		return
	}

	if len(resp.Messages) == 0 {
		return
	}

	var peers []Peer
	brokerFetch("/list-peers", ListPeersRequest{Scope: "all"}, &peers)
	peerByID := make(map[string]Peer, len(peers))
	for _, p := range peers {
		peerByID[p.ID] = p
	}

	pushedIDs.Lock()
	defer pushedIDs.Unlock()

	for _, msg := range resp.Messages {
		if pushedIDs.m[msg.ID] {
			continue // already pushed once -- don't spam
		}
		sender := msg.FromAgent
		if sender == "" {
			sender = msg.FromSession
		}
		fromPeer := peerByID[msg.FromSession]

		// IMPORTANT: channel protocol requires meta to be map[string]string.
		// Claude Code silently drops notifications where any meta value is not
		// a string, including numeric IDs. Stringify everything.
		t.writeNotification("notifications/claude/channel", map[string]any{
			"content": msg.Text,
			"meta": map[string]string{
				"message_id":   strconv.Itoa(msg.ID),
				"from_agent":   msg.FromAgent,
				"from_session": msg.FromSession,
				"from_machine": fromPeer.Machine,
				"from_summary": fromPeer.Summary,
				"from_cwd":     fromPeer.CWD,
				"sent_at":      msg.SentAt,
			},
		})
		pushedIDs.m[msg.ID] = true
		logMCP("Pushed message from %s@%s: %.80s", sender, fromPeer.Machine, msg.Text)
	}
}
