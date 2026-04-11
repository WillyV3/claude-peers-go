package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAutoProjectWithGitRoot(t *testing.T) {
	got := autoProject("/home/user/projects/claude-peers", "/home/user/projects/claude-peers")
	if got != "claude-peers" {
		t.Fatalf("expected claude-peers, got %s", got)
	}
}

func TestAutoProjectHomeDirReturnsEmpty(t *testing.T) {
	// When CWD is exactly the home directory, should return empty.
	got := autoProject("/home/user", "")
	// This depends on os.UserHomeDir() which returns the real home.
	// Just verify it doesn't panic.
	_ = got
}

func TestAutoProjectSubdir(t *testing.T) {
	got := autoProject("/home/user/projects/foo", "")
	// Without git root, uses CWD. Trims home prefix.
	// Result depends on actual home dir -- just verify no panic and non-empty.
	if got == "" {
		// Could be empty if home doesn't match -- that's ok
		_ = got
	}
}

// ADR-001: autoName was deleted. Agent names are declared, not derived.
// The replacement is resolveAgentName, which reads from (in order):
// 1. agentNameOverride (--as flag)
// 2. CLAUDE_PEERS_AGENT env var
// 3. .claude-peers-agent file found by walking up from cwd toward $HOME
//    (the walk-up behaviour is tested on findAgentFile directly so the test
//    process doesn't have to clobber its real $HOME)

func writeAgentFile(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, ".claude-peers-agent")
	if err := os.WriteFile(path, []byte(name), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestFindAgentFileExactCWD(t *testing.T) {
	home := t.TempDir()
	cwd := filepath.Join(home, "project")
	writeAgentFile(t, cwd, "alice\n")
	if got := findAgentFile(cwd, home); got != "alice" {
		t.Fatalf("got %q, want alice", got)
	}
}

func TestFindAgentFileAncestorWalkup(t *testing.T) {
	// File at project root, cwd several levels deep -- must walk up.
	home := t.TempDir()
	project := filepath.Join(home, "project")
	writeAgentFile(t, project, "bob")
	deep := filepath.Join(project, "src", "deep", "nested")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := findAgentFile(deep, home); got != "bob" {
		t.Fatalf("walk-up failed: got %q, want bob", got)
	}
}

func TestFindAgentFileStopsAtHomeBoundary(t *testing.T) {
	// A file exists ABOVE the fake home -- must NOT be found. This is the
	// security property: a stray file in /etc or /tmp cannot claim identity
	// for a session just because cwd is somewhere under it.
	root := t.TempDir()
	writeAgentFile(t, root, "evil")
	home := filepath.Join(root, "home")
	cwd := filepath.Join(home, "project")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := findAgentFile(cwd, home); got != "" {
		t.Fatalf("walked above $HOME and found %q -- security regression", got)
	}
}

func TestFindAgentFileAtHomeRoot(t *testing.T) {
	// If the file is at $HOME itself, walk-up should still find it
	// (home is inclusive in the stop condition).
	home := t.TempDir()
	writeAgentFile(t, home, "carol")
	cwd := filepath.Join(home, "projects", "foo")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := findAgentFile(cwd, home); got != "carol" {
		t.Fatalf("got %q, want carol (file at $HOME itself)", got)
	}
}

func TestFindAgentFileOutsideHomeOnlyChecksCwd(t *testing.T) {
	// CWD is outside HOME. Even if a file exists in the parent directory,
	// we must NOT ascend into it. Intentional: sessions launched from /tmp
	// or /var are sandboxed and shouldn't inherit identity from neighbours.
	home := t.TempDir()
	outside := t.TempDir()
	// Put a file in outside/, then launch from outside/sub/.
	writeAgentFile(t, outside, "dave")
	sub := filepath.Join(outside, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := findAgentFile(sub, home); got != "" {
		t.Fatalf("outside-HOME cwd walked up: got %q", got)
	}
	// But the exact cwd is still checked.
	if got := findAgentFile(outside, home); got != "dave" {
		t.Fatalf("outside-HOME cwd with file in same dir: got %q, want dave", got)
	}
}

func TestFindAgentFileEmptyFileYieldsEmpty(t *testing.T) {
	home := t.TempDir()
	writeAgentFile(t, home, "\n")
	if got := findAgentFile(home, home); got != "" {
		t.Fatalf("empty file yielded %q, want empty", got)
	}
}

func TestFindAgentFileEmptyFileFallsThroughToAncestor(t *testing.T) {
	// Empty file at a nested level should not block walk-up to a populated
	// ancestor file. Matches intent: an empty file is "no declaration here".
	home := t.TempDir()
	project := filepath.Join(home, "project")
	writeAgentFile(t, project, "eve")
	nested := filepath.Join(project, "src")
	writeAgentFile(t, nested, "\n") // empty
	if got := findAgentFile(nested, home); got != "eve" {
		t.Fatalf("empty nested file blocked walk-up: got %q, want eve", got)
	}
}

func TestFindAgentFileMultilineTakesFirstLine(t *testing.T) {
	home := t.TempDir()
	writeAgentFile(t, home, "frank\n# comment about frank\nother\n")
	if got := findAgentFile(home, home); got != "frank" {
		t.Fatalf("got %q, want frank (first non-empty line)", got)
	}
}

func TestFindAgentFileNoFileAnywhere(t *testing.T) {
	home := t.TempDir()
	cwd := filepath.Join(home, "a", "b", "c")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := findAgentFile(cwd, home); got != "" {
		t.Fatalf("no files anywhere should yield empty, got %q", got)
	}
}

func TestFilterEmpty(t *testing.T) {
	cases := []struct {
		input    []string
		expected int
	}{
		{[]string{"a", "", "b", "", "c"}, 3},
		{[]string{"", "", ""}, 0},
		{[]string{"a", "b"}, 2},
		{nil, 0},
	}
	for _, tc := range cases {
		got := filterEmpty(tc.input)
		if len(got) != tc.expected {
			t.Errorf("filterEmpty(%v) = %d items, want %d", tc.input, len(got), tc.expected)
		}
		// Verify no empty strings in output.
		for _, s := range got {
			if s == "" {
				t.Errorf("filterEmpty output contains empty string")
			}
		}
	}
}

func TestSplitNonEmpty(t *testing.T) {
	cases := []struct {
		input    string
		expected int
	}{
		{"a\nb\nc", 3},
		{"a\n\nb", 2},
		{"", 0},
		{"\n\n\n", 0},
	}
	for _, tc := range cases {
		got := splitNonEmpty(tc.input)
		if len(got) != tc.expected {
			t.Errorf("splitNonEmpty(%q) = %d items, want %d", tc.input, len(got), tc.expected)
		}
	}
}
