package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func gitRoot(cwd string) string {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func gitBranch(cwd string) string {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func recentFiles(cwd string, limit int) []string {
	// Modified/staged files first
	cmd := exec.Command("git", "diff", "--name-only", "HEAD")
	cmd.Dir = cwd
	out, _ := cmd.Output()
	files := splitNonEmpty(strings.TrimSpace(string(out)))

	if len(files) >= limit {
		return files[:limit]
	}

	// Also recently committed files
	cmd2 := exec.Command("git", "log", "--oneline", "--name-only", "-5", "--format=")
	cmd2.Dir = cwd
	out2, _ := cmd2.Output()
	logFiles := splitNonEmpty(strings.TrimSpace(string(out2)))

	seen := make(map[string]bool, len(files))
	for _, f := range files {
		seen[f] = true
	}
	for _, f := range logFiles {
		if !seen[f] {
			files = append(files, f)
			seen[f] = true
		}
	}
	if len(files) > limit {
		return files[:limit]
	}
	return files
}

func splitNonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	var result []string
	for part := range strings.SplitSeq(s, "\n") {
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

// autoProject returns a short project name from the git root or CWD.
func autoProject(cwd, root string) string {
	if root != "" {
		return filepath.Base(root)
	}
	home, _ := os.UserHomeDir()
	// If CWD is exactly home (or home lookup failed), no meaningful project.
	if cwd == home || home == "" {
		return ""
	}
	// Trim home dir prefix for readability.
	rel := cwd
	if strings.HasPrefix(cwd, home+"/") {
		rel = strings.TrimPrefix(cwd, home+"/")
	}
	// Use the last meaningful directory name for short paths,
	// or first two segments for deeper paths.
	parts := strings.Split(strings.TrimPrefix(rel, "/"), "/")
	parts = filterEmpty(parts)
	if len(parts) == 0 {
		return ""
	}
	if len(parts) <= 2 {
		return strings.Join(parts, "/")
	}
	return parts[0] + "/" + parts[1]
}

func filterEmpty(ss []string) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// resolveAgentName returns the declared agent name for this session, or ""
// if no declaration exists. Identity is explicit (ADR-001): no fallback to
// project name, directory basename, or machine:tty. An empty return means
// this session is ephemeral and cannot be addressed by name.
//
// Resolution order:
//  1. agentNameOverride (set via --as flag, see main.go)
//  2. CLAUDE_PEERS_AGENT env var
//  3. .claude-peers-agent file found by walking up from cwd toward $HOME
//     (first non-empty first-line wins; see findAgentFile for scope rules)
func resolveAgentName(cwd string) string {
	if agentNameOverride != "" {
		return strings.TrimSpace(agentNameOverride)
	}
	if env := os.Getenv("CLAUDE_PEERS_AGENT"); env != "" {
		return strings.TrimSpace(env)
	}
	home, _ := os.UserHomeDir()
	return findAgentFile(cwd, home)
}

// findAgentFile walks up from cwd looking for a .claude-peers-agent file.
// Returns the first non-empty first-line name found, or "" if none.
//
// Scope rules:
//   - If cwd is inside home, walk up until home itself (inclusive) and stop.
//     Refusing to ascend above $HOME prevents a stray file in /etc, /tmp, or
//     any system directory from silently claiming identity for a user session.
//   - If cwd is outside home, only check cwd itself -- no walk-up. A session
//     launched from /tmp or /var is intentional and should not pick up an
//     identity file from a sibling or parent directory.
//
// Extracted from resolveAgentName so the walk-up can be exercised in tests
// without manipulating the process's real $HOME.
func findAgentFile(cwd, home string) string {
	dir, err := filepath.Abs(cwd)
	if err != nil {
		dir = cwd
	}
	absHome := ""
	if home != "" {
		if h, err := filepath.Abs(home); err == nil {
			absHome = h
		}
	}
	startedInsideHome := absHome != "" && isInside(dir, absHome)
	for {
		path := filepath.Join(dir, ".claude-peers-agent")
		if data, err := os.ReadFile(path); err == nil {
			line := strings.SplitN(strings.TrimSpace(string(data)), "\n", 2)[0]
			if name := strings.TrimSpace(line); name != "" {
				return name
			}
		}
		if !startedInsideHome {
			return ""
		}
		if dir == absHome {
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// isInside reports whether p is equal to or a descendant of root.
func isInside(p, root string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel == "." || !strings.HasPrefix(rel, "..")
}

// agentNameOverride is set by main.go from the --as CLI flag, if provided.
var agentNameOverride string

func generateSummary(cwd, root, branch string, files []string) string {
	var parts []string
	parts = append(parts, "Dir: "+cwd)
	if root != "" {
		parts = append(parts, "Repo: "+filepath.Base(root))
	}
	if branch != "" {
		parts = append(parts, "Branch: "+branch)
	}
	if len(files) > 0 {
		parts = append(parts, "Recent files: "+strings.Join(files, ", "))
	}

	prompt := "One sentence: what is this developer working on? Be specific. No preamble.\n\n" + strings.Join(parts, "\n")

	// Use the claude CLI directly -- everyone running claude-peers already has it.
	// Falls back to LLM API if claude CLI is not available.
	if summary := claudeCLISummary(prompt); summary != "" {
		return summary
	}
	return llmAPISummary(prompt)
}

func claudeCLISummary(prompt string) string {
	claudeBin, err := exec.LookPath("claude")
	if err != nil {
		return ""
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// --bare: skip hooks, LSP, plugins, CLAUDE.md -- just raw LLM call
	// --model haiku: cheapest/fastest model
	// -p: non-interactive, print and exit
	// --no-session-persistence: don't save this throwaway call
	cmd := exec.CommandContext(ctx, claudeBin,
		"-p", prompt,
		"--model", "haiku",
		"--bare",
		"--no-session-persistence",
	)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}

	result := strings.TrimSpace(string(out))
	if len(result) > 120 {
		result = result[:117] + "..."
	}
	return result
}

func llmAPISummary(prompt string) string {
	apiKey := cmp.Or(
		os.Getenv("OPENAI_API_KEY"),
		os.Getenv("ANTHROPIC_API_KEY"),
		readClaudeSettingsKey(),
		cfg.LLMAPIKey,
	)
	baseURL := cmp.Or(
		os.Getenv("LITELLM_BASE_URL"),
		os.Getenv("ANTHROPIC_BASE_URL"),
		cfg.LLMBaseURL,
	)
	if apiKey == "" || baseURL == "" {
		return ""
	}

	model := cmp.Or(os.Getenv("CLAUDE_PEERS_SUMMARY_MODEL"), cfg.LLMModel)
	body, _ := json.Marshal(map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": "One sentence: what is this developer doing right now? Be specific. No preamble."},
			{"role": "user", "content": prompt},
		},
		"max_tokens": 60,
	})

	client := http.Client{Timeout: 5 * time.Second}
	req, _ := http.NewRequest("POST", baseURL+"/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		return ""
	}
	defer resp.Body.Close()

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	if len(result.Choices) > 0 {
		return strings.TrimSpace(result.Choices[0].Message.Content)
	}
	return ""
}

// readClaudeSettingsKey reads ANTHROPIC_AUTH_TOKEN from ~/.claude/settings.json
// as a fallback when the env var isn't set (MCP servers don't inherit Claude's env).
func readClaudeSettingsKey() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		return ""
	}
	var settings struct {
		Env map[string]string `json:"env"`
	}
	if json.Unmarshal(data, &settings) != nil {
		return ""
	}
	return settings.Env["ANTHROPIC_AUTH_TOKEN"]
}

func getTTY() string {
	ppid := os.Getppid()
	cmd := exec.Command("ps", "-o", "tty=", "-p", fmt.Sprintf("%d", ppid))
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	tty := strings.TrimSpace(string(out))
	if tty == "" || tty == "?" || tty == "??" {
		return ""
	}
	return tty
}
