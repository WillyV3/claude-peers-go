package main

import (
	"bytes"
	"cmp"
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
	// Trim home dir prefix for readability.
	home, _ := os.UserHomeDir()
	rel := cwd
	if home != "" && strings.HasPrefix(cwd, home) {
		rel = strings.TrimPrefix(cwd, home+"/")
	}
	// Use first two path segments max.
	parts := strings.SplitN(rel, "/", 3)
	if len(parts) > 2 {
		return parts[0] + "/" + parts[1]
	}
	return rel
}

// autoName generates a readable peer name from machine + project.
func autoName(machine, project string) string {
	if project == "" {
		return machine
	}
	return machine + "/" + project
}

func generateSummary(cwd, root, branch string, files []string) string {
	// Try LiteLLM on the broker machine first, then fall back to Anthropic API.
	apiKey := cmp.Or(
		os.Getenv("OPENAI_API_KEY"),
		os.Getenv("LITELLM_API_KEY"),
		os.Getenv("ANTHROPIC_AUTH_TOKEN"),
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

	model := cmp.Or(os.Getenv("CLAUDE_PEERS_SUMMARY_MODEL"), "claude-haiku")
	body, _ := json.Marshal(map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": "One sentence: what is this developer doing right now? Be specific. No preamble."},
			{"role": "user", "content": strings.Join(parts, "\n")},
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
