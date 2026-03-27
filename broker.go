package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

func generatePeerID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}

type Broker struct {
	db          *sql.DB
	nats        *NATSPublisher
	fleetMemory string
	mu          sync.RWMutex
}

func newBroker() (*Broker, error) {
	dbPath := cfg.DBPath
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(wal)&_pragma=busy_timeout(3000)")
	if err != nil {
		return nil, err
	}

	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS peers (
			id TEXT PRIMARY KEY,
			pid INTEGER NOT NULL,
			machine TEXT NOT NULL DEFAULT '',
			cwd TEXT NOT NULL,
			git_root TEXT,
			tty TEXT,
			summary TEXT NOT NULL DEFAULT '',
			registered_at TEXT NOT NULL,
			last_seen TEXT NOT NULL
		)`,
		// Migration: add machine column if missing (existing databases).
		`ALTER TABLE peers ADD COLUMN machine TEXT NOT NULL DEFAULT ''`,
		`CREATE TABLE IF NOT EXISTS messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			from_id TEXT NOT NULL,
			to_id TEXT NOT NULL,
			text TEXT NOT NULL,
			sent_at TEXT NOT NULL,
			delivered INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			type TEXT NOT NULL,
			peer_id TEXT,
			machine TEXT,
			data TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS kv (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
	} {
		db.Exec(stmt) // Ignore errors from ALTER (column already exists).
	}

	b := &Broker{db: db, nats: newNATSPublisher()}

	// Restore fleet memory from SQLite if available
	var mem sql.NullString
	db.QueryRow("SELECT value FROM kv WHERE key = 'fleet_memory'").Scan(&mem)
	if mem.Valid {
		b.fleetMemory = mem.String
	}

	// Periodic WAL checkpoint + stale cleanup (skip first run)
	staleTimeout := time.Duration(cfg.StaleTimeout) * time.Second
	go func() {
		time.Sleep(staleTimeout)
		for {
			b.cleanStalePeers()
			db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
			db.Exec("DELETE FROM messages WHERE delivered = 1 AND sent_at < ?",
				time.Now().UTC().Add(-1*time.Hour).Format(time.RFC3339))
			db.Exec("DELETE FROM messages WHERE delivered = 0 AND sent_at < ?",
				time.Now().UTC().Add(-1*time.Hour).Format(time.RFC3339))
			time.Sleep(30 * time.Second)
		}
	}()

	return b, nil
}

func (b *Broker) emitEvent(eventType, peerID, machine, data string) {
	b.db.Exec(
		"INSERT INTO events (type, peer_id, machine, data, created_at) VALUES (?, ?, ?, ?, ?)",
		eventType, peerID, machine, data, nowISO(),
	)
}

func (b *Broker) recentEvents(limit int) []Event {
	rows, err := b.db.Query(
		"SELECT id, type, peer_id, machine, data, created_at FROM events ORDER BY id DESC LIMIT ?",
		limit,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var e Event
		var peerID, machine sql.NullString
		rows.Scan(&e.ID, &e.Type, &peerID, &machine, &e.Data, &e.CreatedAt)
		e.PeerID = peerID.String
		e.Machine = machine.String
		events = append(events, e)
	}
	if events == nil {
		events = []Event{}
	}
	return events
}

// cleanStalePeers removes peers that haven't sent a heartbeat within the timeout.
// Works for both local and remote peers -- no PID checks needed.
func (b *Broker) cleanStalePeers() {
	timeout := cfg.StaleTimeout
	if timeout <= 0 {
		timeout = 300
	}
	cutoff := time.Now().UTC().Add(-time.Duration(timeout) * time.Second).Format(time.RFC3339)
	b.db.Exec("DELETE FROM peers WHERE last_seen < ?", cutoff)
	eventCutoff := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
	b.db.Exec("DELETE FROM events WHERE created_at < ?", eventCutoff)
}

func (b *Broker) register(req RegisterRequest) RegisterResponse {
	id := generatePeerID()
	now := nowISO()

	// Remove existing registration for same PID+machine combo.
	b.db.Exec("DELETE FROM peers WHERE pid = ? AND machine = ?", req.PID, req.Machine)

	b.db.Exec(
		"INSERT INTO peers (id, pid, machine, cwd, git_root, tty, summary, registered_at, last_seen) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		id, req.PID, req.Machine, req.CWD, req.GitRoot, req.TTY, req.Summary, now, now,
	)
	b.emitEvent("peer_joined", id, req.Machine, req.Summary)
	b.nats.publish("fleet.peer.joined", FleetEvent{
		Type: "peer_joined", PeerID: id, Machine: req.Machine,
		Summary: req.Summary, CWD: req.CWD,
	})
	return RegisterResponse{ID: id}
}

func (b *Broker) heartbeat(req HeartbeatRequest) {
	b.db.Exec("UPDATE peers SET last_seen = ? WHERE id = ?", nowISO(), req.ID)
}

func (b *Broker) setSummary(req SetSummaryRequest) {
	b.db.Exec("UPDATE peers SET summary = ? WHERE id = ?", req.Summary, req.ID)
	b.emitEvent("summary_changed", req.ID, "", req.Summary)
	b.nats.publish("fleet.summary", FleetEvent{
		Type: "summary_changed", PeerID: req.ID, Summary: req.Summary,
	})
}

func (b *Broker) listPeers(req ListPeersRequest) []Peer {
	var query string
	var args []any

	switch req.Scope {
	case "directory":
		query = "SELECT id, pid, machine, cwd, git_root, tty, summary, registered_at, last_seen FROM peers WHERE cwd = ?"
		args = []any{req.CWD}
	case "repo":
		if req.GitRoot != "" {
			query = "SELECT id, pid, machine, cwd, git_root, tty, summary, registered_at, last_seen FROM peers WHERE git_root = ?"
			args = []any{req.GitRoot}
		} else {
			query = "SELECT id, pid, machine, cwd, git_root, tty, summary, registered_at, last_seen FROM peers WHERE cwd = ?"
			args = []any{req.CWD}
		}
	case "machine":
		if req.Machine != "" {
			query = "SELECT id, pid, machine, cwd, git_root, tty, summary, registered_at, last_seen FROM peers WHERE machine = ?"
			args = []any{req.Machine}
		} else {
			query = "SELECT id, pid, machine, cwd, git_root, tty, summary, registered_at, last_seen FROM peers"
		}
	default: // "all" or empty = everything
		query = "SELECT id, pid, machine, cwd, git_root, tty, summary, registered_at, last_seen FROM peers"
	}

	rows, err := b.db.Query(query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var peers []Peer
	for rows.Next() {
		var p Peer
		var gitRoot, tty sql.NullString
		rows.Scan(&p.ID, &p.PID, &p.Machine, &p.CWD, &gitRoot, &tty, &p.Summary, &p.RegisteredAt, &p.LastSeen)
		p.GitRoot = gitRoot.String
		p.TTY = tty.String

		if req.ExcludeID != "" && p.ID == req.ExcludeID {
			continue
		}
		peers = append(peers, p)
	}
	return peers
}

func (b *Broker) sendMessage(req SendMessageRequest) SendMessageResponse {
	var exists bool
	b.db.QueryRow("SELECT EXISTS(SELECT 1 FROM peers WHERE id = ?)", req.ToID).Scan(&exists)
	if !exists {
		return SendMessageResponse{OK: false, Error: fmt.Sprintf("Peer %s not found", req.ToID)}
	}
	b.db.Exec(
		"INSERT INTO messages (from_id, to_id, text, sent_at, delivered) VALUES (?, ?, ?, ?, 0)",
		req.FromID, req.ToID, req.Text, nowISO(),
	)
	b.emitEvent("message_sent", req.FromID, "", req.ToID)
	b.nats.publish("fleet.message", FleetEvent{
		Type: "message_sent", PeerID: req.FromID, Data: req.ToID,
	})
	return SendMessageResponse{OK: true}
}

func (b *Broker) pollMessages(req PollMessagesRequest) PollMessagesResponse {
	rows, err := b.db.Query(
		"SELECT id, from_id, to_id, text, sent_at FROM messages WHERE to_id = ? AND delivered = 0 ORDER BY sent_at ASC",
		req.ID,
	)
	if err != nil {
		return PollMessagesResponse{Messages: []Message{}}
	}
	defer rows.Close()

	var msgs []Message
	for rows.Next() {
		var m Message
		rows.Scan(&m.ID, &m.FromID, &m.ToID, &m.Text, &m.SentAt)
		msgs = append(msgs, m)
	}

	for _, m := range msgs {
		b.db.Exec("UPDATE messages SET delivered = 1 WHERE id = ?", m.ID)
	}

	if msgs == nil {
		msgs = []Message{}
	}
	return PollMessagesResponse{Messages: msgs}
}

// peekMessages returns undelivered messages without marking them delivered.
// Used by the background poll loop -- messages stay available for check_messages.
func (b *Broker) peekMessages(req PollMessagesRequest) PollMessagesResponse {
	rows, err := b.db.Query(
		"SELECT id, from_id, to_id, text, sent_at FROM messages WHERE to_id = ? AND delivered = 0 ORDER BY sent_at ASC",
		req.ID,
	)
	if err != nil {
		return PollMessagesResponse{Messages: []Message{}}
	}
	defer rows.Close()

	var msgs []Message
	for rows.Next() {
		var m Message
		rows.Scan(&m.ID, &m.FromID, &m.ToID, &m.Text, &m.SentAt)
		msgs = append(msgs, m)
	}
	if msgs == nil {
		msgs = []Message{}
	}
	return PollMessagesResponse{Messages: msgs}
}

func (b *Broker) ackMessage(messageID int) {
	b.db.Exec("UPDATE messages SET delivered = 1 WHERE id = ?", messageID)
}

func (b *Broker) unregister(req UnregisterRequest) {
	b.emitEvent("peer_left", req.ID, "", "")
	b.nats.publish("fleet.peer.left", FleetEvent{
		Type: "peer_left", PeerID: req.ID,
	})
	b.db.Exec("DELETE FROM peers WHERE id = ?", req.ID)
	b.db.Exec("DELETE FROM messages WHERE to_id = ? AND delivered = 0", req.ID)
}

func (b *Broker) setFleetMemory(content string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fleetMemory = content
	b.db.Exec("INSERT OR REPLACE INTO kv (key, value) VALUES ('fleet_memory', ?)", content)
}

func (b *Broker) getFleetMemory() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.fleetMemory
}

func (b *Broker) peerCount() int {
	var count int
	b.db.QueryRow("SELECT COUNT(*) FROM peers").Scan(&count)
	return count
}

func decodeBody[T any](r *http.Request) (T, error) {
	var v T
	err := json.NewDecoder(r.Body).Decode(&v)
	return v, err
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// authMiddleware rejects requests without a valid Bearer token.
// If cfg.Secret is empty, auth is disabled (all requests pass).
func authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cfg.Secret == "" {
			next.ServeHTTP(w, r)
			return
		}
		// Health endpoint is always public (for monitoring)
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}
		auth := r.Header.Get("Authorization")
		if auth != "Bearer "+cfg.Secret {
			http.Error(w, "unauthorized", 401)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func runBroker(ctx context.Context) error {
	b, err := newBroker()
	if err != nil {
		return fmt.Errorf("init broker: %w", err)
	}
	defer b.db.Close()

	// Stale cleanup + WAL checkpoint handled by goroutine in newBroker()

	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, HealthResponse{Status: "ok", Peers: b.peerCount(), Machine: cfg.MachineName})
	})

	mux.HandleFunc("POST /register", func(w http.ResponseWriter, r *http.Request) {
		req, err := decodeBody[RegisterRequest](r)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, b.register(req))
	})

	mux.HandleFunc("POST /heartbeat", func(w http.ResponseWriter, r *http.Request) {
		req, err := decodeBody[HeartbeatRequest](r)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		b.heartbeat(req)
		writeJSON(w, map[string]bool{"ok": true})
	})

	mux.HandleFunc("POST /set-summary", func(w http.ResponseWriter, r *http.Request) {
		req, err := decodeBody[SetSummaryRequest](r)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		b.setSummary(req)
		writeJSON(w, map[string]bool{"ok": true})
	})

	mux.HandleFunc("POST /list-peers", func(w http.ResponseWriter, r *http.Request) {
		req, err := decodeBody[ListPeersRequest](r)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, b.listPeers(req))
	})

	mux.HandleFunc("POST /send-message", func(w http.ResponseWriter, r *http.Request) {
		req, err := decodeBody[SendMessageRequest](r)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, b.sendMessage(req))
	})

	mux.HandleFunc("POST /poll-messages", func(w http.ResponseWriter, r *http.Request) {
		req, err := decodeBody[PollMessagesRequest](r)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, b.pollMessages(req))
	})

	mux.HandleFunc("POST /peek-messages", func(w http.ResponseWriter, r *http.Request) {
		req, err := decodeBody[PollMessagesRequest](r)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, b.peekMessages(req))
	})

	mux.HandleFunc("POST /ack-message", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			MessageID int `json:"message_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		b.ackMessage(req.MessageID)
		writeJSON(w, map[string]bool{"ok": true})
	})

	mux.HandleFunc("POST /unregister", func(w http.ResponseWriter, r *http.Request) {
		req, err := decodeBody[UnregisterRequest](r)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		b.unregister(req)
		writeJSON(w, map[string]bool{"ok": true})
	})

	mux.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) {
		limit := 50
		if v := r.URL.Query().Get("limit"); v != "" {
			fmt.Sscanf(v, "%d", &limit)
		}
		writeJSON(w, b.recentEvents(limit))
	})

	mux.HandleFunc("GET /fleet-memory", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/markdown")
		w.Write([]byte(b.getFleetMemory()))
	})

	mux.HandleFunc("POST /fleet-memory", func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		b.setFleetMemory(string(data))
		writeJSON(w, map[string]bool{"ok": true})
	})

	addr := cfg.Listen
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	srv := &http.Server{Handler: authMiddleware(mux)}

	log.Printf("[claude-peers broker] listening on %s (db: %s, machine: %s)", addr, cfg.DBPath, cfg.MachineName)

	context.AfterFunc(ctx, func() {
		srv.Shutdown(context.Background())
	})

	if err := srv.Serve(ln); err != http.ErrServerClosed {
		return err
	}

	return nil
}
