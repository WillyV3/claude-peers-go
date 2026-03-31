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

// autoName generates a readable peer name: repo@branch if in git, else dir-basename.
// Machine name stays as separate metadata in the `machine` field.
func autoName(_ string, project, _ string) string {
	if project == "" {
		return "unnamed"
	}
	return project
}

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
