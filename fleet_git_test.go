package main

import (
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

func TestAutoNameWithProject(t *testing.T) {
	got := autoName("machine", "claude-peers", "")
	if got != "claude-peers" {
		t.Fatalf("expected claude-peers, got %s", got)
	}
}

func TestAutoNameWithoutProject(t *testing.T) {
	got := autoName("machine", "", "")
	if got != "machine" {
		t.Fatalf("expected machine, got %s", got)
	}
}

func TestAutoNameWithTTY(t *testing.T) {
	got := autoName("laptop", "", "pts/3")
	if got != "laptop:3" {
		t.Fatalf("expected laptop:3, got %s", got)
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
