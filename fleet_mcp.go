package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
)

// JSON-RPC 2.0 types -- minimal, no SDK needed.

type jsonrpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonrpcResponse struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      any            `json:"id"`
	Result  any            `json:"result,omitempty"`
	Error   *jsonrpcError  `json:"error,omitempty"`
}

type jsonrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type jsonrpcNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// MCPTransport reads JSON-RPC from stdin, writes to stdout.
// Thread-safe writes via mutex.
type MCPTransport struct {
	scanner *bufio.Scanner
	writer  io.Writer
	mu      sync.Mutex
}

func newMCPTransport() *MCPTransport {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB buffer
	return &MCPTransport{
		scanner: scanner,
		writer:  os.Stdout,
	}
}

func (t *MCPTransport) readRequest() (jsonrpcRequest, error) {
	if !t.scanner.Scan() {
		if err := t.scanner.Err(); err != nil {
			return jsonrpcRequest{}, err
		}
		return jsonrpcRequest{}, io.EOF
	}
	var req jsonrpcRequest
	err := json.Unmarshal(t.scanner.Bytes(), &req)
	return req, err
}

func (t *MCPTransport) writeResponse(resp jsonrpcResponse) {
	t.mu.Lock()
	defer t.mu.Unlock()
	data, _ := json.Marshal(resp)
	fmt.Fprintf(t.writer, "%s\n", data)
}

func (t *MCPTransport) writeNotification(method string, params any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	notif := jsonrpcNotification{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
	data, _ := json.Marshal(notif)
	fmt.Fprintf(t.writer, "%s\n", data)
}

func (t *MCPTransport) respond(id any, result any) {
	t.writeResponse(jsonrpcResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func (t *MCPTransport) respondError(id any, code int, msg string) {
	t.writeResponse(jsonrpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &jsonrpcError{Code: code, Message: msg},
	})
}

// MCP protocol constants
const (
	mcpProtocolVersion = "2025-03-26"
	serverName         = "claude-peers"
	serverVersion      = "1.0.0"
)

// Tool schema definitions for MCP
var mcpTools = []map[string]any{
	{
		"name":        "list_peers",
		"description": "List other Claude Code instances across the network. Returns their ID, machine, working directory, git repo, and summary.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"scope": map[string]any{
					"type":        "string",
					"enum":        []string{"all", "machine", "directory", "repo"},
					"description": `Scope of peer discovery. "all" = every peer on every machine. "machine" = only this machine. "directory" = same working directory. "repo" = same git repository.`,
				},
			},
			"required": []string{"scope"},
		},
	},
	{
		"name":        "send_message",
		"description": "Send a message to another Claude Code instance by peer ID. The message will be pushed into their session immediately via channel notification.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"to_id": map[string]any{
					"type":        "string",
					"description": "The peer ID of the target Claude Code instance (from list_peers)",
				},
				"message": map[string]any{
					"type":        "string",
					"description": "The message to send",
				},
			},
			"required": []string{"to_id", "message"},
		},
	},
	{
		"name":        "set_summary",
		"description": "Set a brief summary (1-2 sentences) of what you are currently working on. This is visible to other Claude Code instances when they list peers.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"summary": map[string]any{
					"type":        "string",
					"description": "A 1-2 sentence summary of your current work",
				},
			},
			"required": []string{"summary"},
		},
	},
	{
		"name":        "set_name",
		"description": "Set a custom display name for this Claude Code session. Overrides the auto-generated git-context name (e.g. 'my-project@main').",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "A short display name for this session (e.g. 'api-refactor', 'debug-session')",
				},
			},
			"required": []string{"name"},
		},
	},
	{
		"name":        "check_messages",
		"description": "Manually check for new messages from other Claude Code instances. Messages are normally pushed automatically via channel notifications, but you can use this as a fallback.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{},
		},
	},
}

var mcpInstructions = `You are connected to the claude-peers network. Other Claude Code instances across multiple machines can see you and send you messages.

IMPORTANT RULES:
1. When you START a new conversation, call check_messages to see if anyone sent you something.
2. When the user gives you a new prompt, call check_messages FIRST before doing anything else.
3. If there are messages, tell the user who sent what, and reply using send_message.
4. When you start, call set_summary to describe what you're working on.

If you receive a <channel source="claude-peers" ...> notification, respond to it immediately using send_message.

Available tools:
- list_peers: Discover other Claude Code instances (scope: all/machine/directory/repo)
- send_message: Send a message to another instance by ID
- set_summary: Set a 1-2 sentence summary of what you're working on (visible to other peers)
- set_name: Override the auto-generated session name with a custom display name
- check_messages: Check for new messages from other Claude Code instances -- CALL THIS ON EVERY USER PROMPT`

func handleInitialize(id any, t *MCPTransport) {
	// Build dynamic instructions with fleet context injection.
	instructions := mcpInstructions + buildFleetContext()

	t.respond(id, map[string]any{
		"protocolVersion": mcpProtocolVersion,
		"capabilities": map[string]any{
			"experimental": map[string]any{
				"claude/channel": map[string]any{},
			},
			"tools": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    serverName,
			"version": serverVersion,
		},
		"instructions": instructions,
	})
}

// buildFleetContext fetches active peers, recent events, and fleet memory
// from the broker and returns a context string to inject into Claude's session.
// Runs at session start -- gives Claude immediate awareness of the fleet.
func buildFleetContext() string {
	var ctx string

	// Active peers
	var peers []Peer
	if err := cliFetch("/list-peers", ListPeersRequest{Scope: "all"}, &peers); err == nil && len(peers) > 0 {
		ctx += "\n\n--- FLEET CONTEXT (injected at session start) ---"
		ctx += fmt.Sprintf("\n%d active Claude session(s) on the network:", len(peers))
		for _, p := range peers {
			line := fmt.Sprintf("\n- %s on %s", p.Name, p.Machine)
			if p.Summary != "" {
				line += fmt.Sprintf(" -- %s", p.Summary)
			}
			ctx += line
		}
	}

	// Recent events (last 5)
	var events []Event
	if err := cliFetch("/events?limit=5", nil, &events); err == nil && len(events) > 0 {
		ctx += "\n\nRecent fleet events:"
		for _, e := range events {
			ctx += fmt.Sprintf("\n- [%s] %s %s", e.Type, e.PeerID, e.Data)
		}
	}

	// Fleet memory snippet (first 500 chars)
	client := &http.Client{Timeout: 3 * time.Second}
	req, _ := http.NewRequest("GET", cfg.BrokerURL+"/fleet-memory", nil)
	if authToken != "" {
		req.Header.Set("Authorization", "Bearer "+authToken)
	}
	if resp, err := client.Do(req); err == nil && resp.StatusCode == 200 {
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		mem := string(body)
		if len(mem) > 500 {
			mem = mem[:500] + "..."
		}
		if len(mem) > 10 {
			ctx += "\n\nFleet memory:\n" + mem
		}
	}

	return ctx
}

func handleToolsList(id any, t *MCPTransport) {
	t.respond(id, map[string]any{"tools": mcpTools})
}

func logMCP(msg string, args ...any) {
	log.Printf("[claude-peers] "+msg, args...)
}
