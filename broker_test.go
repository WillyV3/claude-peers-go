package main

import (
	"os"
	"path/filepath"
	"testing"
)

func testBroker(t *testing.T) *Broker {
	t.Helper()
	dir := t.TempDir()
	cfg.DBPath = filepath.Join(dir, "test.db")
	cfg.StaleTimeout = 300
	b, err := newBroker()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.db.Close() })
	return b
}

func TestRegisterAndList(t *testing.T) {
	b := testBroker(t)

	resp := b.register(RegisterRequest{
		PID: os.Getpid(), Machine: "test-machine",
		CWD: "/tmp", Summary: "testing",
	})
	if resp.ID == "" {
		t.Fatal("expected peer ID")
	}

	peers := b.listPeers(ListPeersRequest{Scope: "all"})
	if len(peers) != 1 {
		t.Fatalf("expected 1 peer, got %d", len(peers))
	}
	if peers[0].Machine != "test-machine" {
		t.Fatalf("expected machine test-machine, got %s", peers[0].Machine)
	}
	if peers[0].Summary != "testing" {
		t.Fatalf("expected summary testing, got %s", peers[0].Summary)
	}
}

func TestSendAndPollMessage(t *testing.T) {
	b := testBroker(t)

	r1 := b.register(RegisterRequest{PID: 1, Machine: "m1", CWD: "/a"})
	r2 := b.register(RegisterRequest{PID: 2, Machine: "m2", CWD: "/b"})

	resp := b.sendMessage(SendMessageRequest{FromID: r1.ID, ToID: r2.ID, Text: "hello"})
	if !resp.OK {
		t.Fatalf("send failed: %s", resp.Error)
	}

	// Peek should return without marking delivered
	peek := b.peekMessages(PollMessagesRequest{ID: r2.ID})
	if len(peek.Messages) != 1 {
		t.Fatalf("expected 1 peeked message, got %d", len(peek.Messages))
	}

	// Poll should return and mark delivered
	poll := b.pollMessages(PollMessagesRequest{ID: r2.ID})
	if len(poll.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(poll.Messages))
	}
	if poll.Messages[0].Text != "hello" {
		t.Fatalf("expected hello, got %s", poll.Messages[0].Text)
	}

	// Second poll should be empty
	poll2 := b.pollMessages(PollMessagesRequest{ID: r2.ID})
	if len(poll2.Messages) != 0 {
		t.Fatalf("expected 0 messages after poll, got %d", len(poll2.Messages))
	}
}

func TestSendToNonexistentPeer(t *testing.T) {
	b := testBroker(t)

	resp := b.sendMessage(SendMessageRequest{FromID: "a", ToID: "nonexistent", Text: "hi"})
	if resp.OK {
		t.Fatal("expected send to fail for nonexistent peer")
	}
}

func TestUnregisterCleansMessages(t *testing.T) {
	b := testBroker(t)

	r1 := b.register(RegisterRequest{PID: 1, Machine: "m1", CWD: "/a"})
	r2 := b.register(RegisterRequest{PID: 2, Machine: "m2", CWD: "/b"})

	b.sendMessage(SendMessageRequest{FromID: r1.ID, ToID: r2.ID, Text: "hello"})
	b.unregister(UnregisterRequest{ID: r2.ID})

	// Messages should be cleaned up
	poll := b.pollMessages(PollMessagesRequest{ID: r2.ID})
	if len(poll.Messages) != 0 {
		t.Fatalf("expected 0 messages after unregister, got %d", len(poll.Messages))
	}
}

func TestSetSummary(t *testing.T) {
	b := testBroker(t)

	r := b.register(RegisterRequest{PID: 1, Machine: "m1", CWD: "/a", Summary: "old"})
	b.setSummary(SetSummaryRequest{ID: r.ID, Summary: "new"})

	peers := b.listPeers(ListPeersRequest{Scope: "all"})
	if peers[0].Summary != "new" {
		t.Fatalf("expected summary new, got %s", peers[0].Summary)
	}
}

func TestListPeersByScope(t *testing.T) {
	b := testBroker(t)

	b.register(RegisterRequest{PID: 1, Machine: "m1", CWD: "/project-a", GitRoot: "/project-a"})
	b.register(RegisterRequest{PID: 2, Machine: "m1", CWD: "/project-b", GitRoot: "/project-b"})
	b.register(RegisterRequest{PID: 3, Machine: "m2", CWD: "/project-a", GitRoot: "/project-a"})

	all := b.listPeers(ListPeersRequest{Scope: "all"})
	if len(all) != 3 {
		t.Fatalf("expected 3 peers for all, got %d", len(all))
	}

	machine := b.listPeers(ListPeersRequest{Scope: "machine", Machine: "m1"})
	if len(machine) != 2 {
		t.Fatalf("expected 2 peers for machine m1, got %d", len(machine))
	}

	repo := b.listPeers(ListPeersRequest{Scope: "repo", GitRoot: "/project-a"})
	if len(repo) != 2 {
		t.Fatalf("expected 2 peers for repo /project-a, got %d", len(repo))
	}

	dir := b.listPeers(ListPeersRequest{Scope: "directory", CWD: "/project-b"})
	if len(dir) != 1 {
		t.Fatalf("expected 1 peer for dir /project-b, got %d", len(dir))
	}
}

func TestFleetMemory(t *testing.T) {
	b := testBroker(t)

	b.setFleetMemory("# Fleet Status\nAll good.")
	got := b.getFleetMemory()
	if got != "# Fleet Status\nAll good." {
		t.Fatalf("expected fleet memory content, got %q", got)
	}
}

func TestEvents(t *testing.T) {
	b := testBroker(t)

	b.register(RegisterRequest{PID: 1, Machine: "m1", CWD: "/a"})
	events := b.recentEvents(10)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != "peer_joined" {
		t.Fatalf("expected peer_joined, got %s", events[0].Type)
	}
}

func TestDuplicatePIDRegister(t *testing.T) {
	b := testBroker(t)

	b.register(RegisterRequest{PID: 1, Machine: "m1", CWD: "/a"})
	b.register(RegisterRequest{PID: 1, Machine: "m1", CWD: "/b"})

	peers := b.listPeers(ListPeersRequest{Scope: "all"})
	if len(peers) != 1 {
		t.Fatalf("expected 1 peer after re-register, got %d", len(peers))
	}
	if peers[0].CWD != "/b" {
		t.Fatalf("expected CWD /b after re-register, got %s", peers[0].CWD)
	}
}
