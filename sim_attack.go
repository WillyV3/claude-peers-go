package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

var simSSHTargets = map[string]string{
	"omarchy":        "100.85.150.110",
	"ubuntu-homelab": "100.109.211.128",
	"raspdeck":       "raspdeck",
	"thinkbook":      "thinkbook-omarchy",
	"macbook1":       "williamvansickleiii@100.67.104.73",
	"willyv4":        "100.95.2.57",
}

type simConfig struct {
	scenario string
	targets  []string
	dryRun   bool
}

func runSimAttack(args []string) error {
	sc, err := parseSimArgs(args)
	if err != nil {
		return err
	}

	// Safety check: refuse to target ubuntu-homelab without confirmation
	for _, t := range sc.targets {
		if t == "ubuntu-homelab" {
			if !simConfirm("ubuntu-homelab is the broker. Are you sure you want to target it? (yes/no): ") {
				return fmt.Errorf("aborted: refused to target ubuntu-homelab")
			}
		}
	}

	if sc.scenario == "--all" {
		return simRunAll(sc)
	}

	switch sc.scenario {
	case "brute-force":
		return simBruteForce(sc.targets[0], sc.dryRun)
	case "credential-theft":
		return simCredentialTheft(sc.targets[0], sc.dryRun)
	case "binary-tamper":
		return simBinaryTamper(sc.targets[0], sc.dryRun)
	case "rogue-service":
		return simRogueService(sc.targets[0], sc.dryRun)
	case "lateral-movement":
		if len(sc.targets) < 2 {
			return fmt.Errorf("lateral-movement requires two targets: --target=machine1,machine2")
		}
		return simLateralMovement(sc.targets[0], sc.targets[1], sc.dryRun)
	default:
		return fmt.Errorf("unknown scenario: %s\nAvailable: brute-force, credential-theft, binary-tamper, rogue-service, lateral-movement, --all", sc.scenario)
	}
}

func parseSimArgs(args []string) (*simConfig, error) {
	sc := &simConfig{
		targets: []string{"raspdeck"},
	}

	var positional []string
	for _, arg := range args {
		switch {
		case strings.HasPrefix(arg, "--target="):
			sc.targets = strings.Split(strings.TrimPrefix(arg, "--target="), ",")
		case arg == "--dry-run":
			sc.dryRun = true
		case strings.HasPrefix(arg, "-"):
			return nil, fmt.Errorf("unknown flag: %s", arg)
		default:
			positional = append(positional, arg)
		}
	}

	if len(positional) < 1 {
		return nil, fmt.Errorf("usage: claude-peers sim-attack <scenario> [--target=machine] [--dry-run]\nScenarios: brute-force, credential-theft, binary-tamper, rogue-service, lateral-movement, --all")
	}
	sc.scenario = positional[0]

	// Validate targets
	for _, t := range sc.targets {
		if _, ok := simSSHTargets[t]; !ok {
			return nil, fmt.Errorf("unknown target machine: %s\nKnown: omarchy, ubuntu-homelab, raspdeck, thinkbook, macbook1, willyv4", t)
		}
	}

	return sc, nil
}

func simConfirm(prompt string) bool {
	fmt.Print(prompt)
	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() {
		return strings.TrimSpace(strings.ToLower(scanner.Text())) == "yes"
	}
	return false
}

// simSSH runs a command on a remote machine via SSH.
func simSSH(target, command string) (string, error) {
	sshTarget, ok := simSSHTargets[target]
	if !ok {
		return "", fmt.Errorf("unknown machine: %s", target)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ssh",
		"-o", "ConnectTimeout=5",
		"-o", "StrictHostKeyChecking=no",
		"-o", "BatchMode=yes",
		sshTarget, command)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("ssh %s: %w: %s", target, err, string(out))
	}
	return string(out), nil
}

// simWaitForHealth polls the broker /machine-health endpoint until the check function returns true.
func simWaitForHealth(machine string, check func(h *MachineHealth) bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		health := simFetchHealth()
		if h, ok := health[machine]; ok && check(h) {
			return true
		}
		time.Sleep(5 * time.Second)
	}
	return false
}

// simFetchHealth fetches the /machine-health endpoint from the broker.
func simFetchHealth() map[string]*MachineHealth {
	client := http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest("GET", cfg.BrokerURL+"/machine-health", nil)
	if err != nil {
		return nil
	}
	if authToken != "" {
		req.Header.Set("Authorization", "Bearer "+authToken)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil
	}

	var result map[string]*MachineHealth
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil
	}
	return result
}

// simUnquarantine sends a POST /unquarantine request to the broker.
func simUnquarantine(machine string) error {
	payload := map[string]string{"machine": machine}
	data, _ := json.Marshal(payload)

	client := http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest("POST", cfg.BrokerURL+"/unquarantine", strings.NewReader(string(data)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if authToken != "" {
		req.Header.Set("Authorization", "Bearer "+authToken)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("unquarantine %s: %w", machine, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unquarantine %s: %d: %s", machine, resp.StatusCode, string(b))
	}
	return nil
}

func simPrintResult(name string, pass bool, detail string) {
	if pass {
		fmt.Printf("  PASS: %s -- %s\n", name, detail)
	} else {
		fmt.Printf("  FAIL: %s -- %s\n", name, detail)
	}
}

// --- Scenarios ---

func simBruteForce(target string, dryRun bool) error {
	fmt.Printf("=== SIM: SSH Brute Force on %s ===\n", target)

	// Step 1: Write fake auth failure log entries
	fmt.Println("  Injecting 6 fake auth failure log entries...")
	for i := 0; i < 6; i++ {
		cmd := fmt.Sprintf(`logger -p auth.warning "sshd[9999]: Failed password for invalid user testattacker from 203.0.113.99 port 22 ssh2"`)
		if dryRun {
			fmt.Printf("  [DRY-RUN] SSH %s: %s\n", target, cmd)
		} else {
			if _, err := simSSH(target, cmd); err != nil {
				log.Printf("  Warning: log injection failed: %v", err)
			}
		}
	}

	// Step 2: Wait for detection
	pass := false
	if dryRun {
		fmt.Println("  [DRY-RUN] SKIP: would wait up to 120s for detection")
		fmt.Println("  [DRY-RUN] SKIP: verification")
	} else {
		fmt.Println("  Waiting for detection (up to 120s)...")
		pass = simWaitForHealth(target, func(h *MachineHealth) bool {
			return h.Score > 0
		}, 120*time.Second)
		if pass {
			health := simFetchHealth()
			if h, ok := health[target]; ok {
				fmt.Printf("  Detected: score=%d status=%s last_event=%s\n", h.Score, h.Status, h.LastEventDesc)
			}
		}
	}

	// Step 3: Cleanup
	fmt.Println("  Cleaning up...")
	cleanupCmd := `logger -p auth.info "sim-attack: brute-force cleanup"`
	ipCleanup := `sudo iptables -D INPUT -s 203.0.113.99 -j DROP 2>/dev/null || true`
	if dryRun {
		fmt.Printf("  [DRY-RUN] SSH %s: %s\n", target, cleanupCmd)
		fmt.Printf("  [DRY-RUN] SSH %s: %s\n", target, ipCleanup)
	} else {
		simSSH(target, cleanupCmd)
		simSSH(target, ipCleanup)
	}

	if !dryRun {
		simPrintResult("brute-force", pass, fmt.Sprintf("target=%s", target))
	} else {
		fmt.Println("  [DRY-RUN] Result: SKIP")
	}
	return nil
}

func simCredentialTheft(target string, dryRun bool) error {
	fmt.Printf("=== SIM: Credential Theft on %s ===\n", target)

	// Step 1: Touch credential files
	cmd := `touch ~/.config/claude-peers/identity.pem && touch ~/.config/claude-peers/token.jwt`
	fmt.Println("  Creating fake credential files...")
	if dryRun {
		fmt.Printf("  [DRY-RUN] SSH %s: %s\n", target, cmd)
	} else {
		if _, err := simSSH(target, cmd); err != nil {
			log.Printf("  Warning: credential file creation failed: %v", err)
		}
	}

	// Step 2: Wait for FIM detection
	pass := false
	if dryRun {
		fmt.Println("  [DRY-RUN] SKIP: would wait up to 180s for FIM detection")
	} else {
		fmt.Println("  Waiting for FIM detection (up to 180s)...")
		pass = simWaitForHealth(target, func(h *MachineHealth) bool {
			return h.Score >= 10 || h.Status == "quarantined"
		}, 180*time.Second)
		if pass {
			health := simFetchHealth()
			if h, ok := health[target]; ok {
				fmt.Printf("  Detected: score=%d status=%s last_event=%s\n", h.Score, h.Status, h.LastEventDesc)
			}
		}
	}

	// Step 3: Cleanup -- unquarantine
	fmt.Println("  Cleaning up...")
	if dryRun {
		fmt.Printf("  [DRY-RUN] Would unquarantine %s\n", target)
	} else {
		if err := simUnquarantine(target); err != nil {
			log.Printf("  Warning: unquarantine failed: %v", err)
		} else {
			fmt.Printf("  Unquarantined %s\n", target)
		}
	}

	if !dryRun {
		simPrintResult("credential-theft", pass, fmt.Sprintf("target=%s", target))
	} else {
		fmt.Println("  [DRY-RUN] Result: SKIP")
	}
	return nil
}

func simBinaryTamper(target string, dryRun bool) error {
	fmt.Printf("=== SIM: Binary Tamper on %s ===\n", target)

	// Step 1: Create a fake binary (NOT the real one)
	cmd := `sudo touch /usr/local/bin/claude-peers-sim-test`
	fmt.Println("  Creating fake tampered binary...")
	if dryRun {
		fmt.Printf("  [DRY-RUN] SSH %s: %s\n", target, cmd)
	} else {
		if _, err := simSSH(target, cmd); err != nil {
			log.Printf("  Warning: fake binary creation failed: %v", err)
		}
	}

	// Step 2: Wait for FIM detection (rule 100101, level 13 -> quarantine)
	pass := false
	if dryRun {
		fmt.Println("  [DRY-RUN] SKIP: would wait up to 180s for FIM detection")
	} else {
		fmt.Println("  Waiting for FIM detection (up to 180s)...")
		pass = simWaitForHealth(target, func(h *MachineHealth) bool {
			return h.Status == "quarantined"
		}, 180*time.Second)
		if pass {
			health := simFetchHealth()
			if h, ok := health[target]; ok {
				fmt.Printf("  Detected: score=%d status=%s last_event=%s\n", h.Score, h.Status, h.LastEventDesc)
			}
		}
	}

	// Step 3: Cleanup
	fmt.Println("  Cleaning up...")
	cleanupCmd := `sudo rm -f /usr/local/bin/claude-peers-sim-test`
	if dryRun {
		fmt.Printf("  [DRY-RUN] SSH %s: %s\n", target, cleanupCmd)
		fmt.Printf("  [DRY-RUN] Would unquarantine %s\n", target)
	} else {
		simSSH(target, cleanupCmd)
		if err := simUnquarantine(target); err != nil {
			log.Printf("  Warning: unquarantine failed: %v", err)
		} else {
			fmt.Printf("  Unquarantined %s\n", target)
		}
	}

	if !dryRun {
		simPrintResult("binary-tamper", pass, fmt.Sprintf("target=%s", target))
	} else {
		fmt.Println("  [DRY-RUN] Result: SKIP")
	}
	return nil
}

func simRogueService(target string, dryRun bool) error {
	fmt.Printf("=== SIM: Rogue Systemd Service on %s ===\n", target)

	// Step 1: Create a fake unit file
	cmd := `mkdir -p ~/.config/systemd/user && printf '[Unit]\nDescription=Sim Rogue\n[Service]\nExecStart=/bin/true\n' > ~/.config/systemd/user/sim-rogue.service`
	fmt.Println("  Creating fake rogue service unit...")
	if dryRun {
		fmt.Printf("  [DRY-RUN] SSH %s: %s\n", target, cmd)
	} else {
		if _, err := simSSH(target, cmd); err != nil {
			log.Printf("  Warning: unit file creation failed: %v", err)
		}
	}

	// Step 2: Wait for FIM detection (rule 100130, level 9 -> warning -> score > 0)
	pass := false
	if dryRun {
		fmt.Println("  [DRY-RUN] SKIP: would wait up to 120s for FIM detection")
	} else {
		fmt.Println("  Waiting for FIM detection (up to 120s)...")
		pass = simWaitForHealth(target, func(h *MachineHealth) bool {
			return h.Score > 0
		}, 120*time.Second)
		if pass {
			health := simFetchHealth()
			if h, ok := health[target]; ok {
				fmt.Printf("  Detected: score=%d status=%s last_event=%s\n", h.Score, h.Status, h.LastEventDesc)
			}
		}
	}

	// Step 3: Cleanup
	fmt.Println("  Cleaning up...")
	cleanupCmd := `rm -f ~/.config/systemd/user/sim-rogue.service`
	if dryRun {
		fmt.Printf("  [DRY-RUN] SSH %s: %s\n", target, cleanupCmd)
	} else {
		simSSH(target, cleanupCmd)
	}

	if !dryRun {
		simPrintResult("rogue-service", pass, fmt.Sprintf("target=%s", target))
	} else {
		fmt.Println("  [DRY-RUN] Result: SKIP")
	}
	return nil
}

func simLateralMovement(target1, target2 string, dryRun bool) error {
	fmt.Printf("=== SIM: Lateral Movement: %s -> %s ===\n", target1, target2)

	sourceIP := "203.0.113.50"

	// Step 1: Generate auth failures on target1
	fmt.Printf("  Injecting 5 auth failures on %s from %s...\n", target1, sourceIP)
	for i := 0; i < 5; i++ {
		cmd := fmt.Sprintf(`logger -p auth.warning "sshd[9999]: Failed password for invalid user lateral from %s port 22 ssh2"`, sourceIP)
		if dryRun {
			fmt.Printf("  [DRY-RUN] SSH %s: %s\n", target1, cmd)
		} else {
			if _, err := simSSH(target1, cmd); err != nil {
				log.Printf("  Warning: log injection on %s failed: %v", target1, err)
			}
		}
	}

	// Step 2: Generate auth success on target2 from same IP
	fmt.Printf("  Injecting auth success on %s from %s...\n", target2, sourceIP)
	successCmd := fmt.Sprintf(`logger -p auth.info "sshd[9998]: Accepted publickey for root from %s port 22 ssh2: RSA SHA256:simulated"`, sourceIP)
	if dryRun {
		fmt.Printf("  [DRY-RUN] SSH %s: %s\n", target2, successCmd)
	} else {
		if _, err := simSSH(target2, successCmd); err != nil {
			log.Printf("  Warning: log injection on %s failed: %v", target2, err)
		}
	}

	// Step 3: Wait for correlation detection
	pass := false
	if dryRun {
		fmt.Println("  [DRY-RUN] SKIP: would wait up to 180s for correlation detection")
	} else {
		fmt.Println("  Waiting for correlation detection (up to 180s)...")
		pass = simWaitForHealth(target1, func(h *MachineHealth) bool {
			return h.Score > 0
		}, 180*time.Second)
		// Also check target2
		if pass {
			pass2 := simWaitForHealth(target2, func(h *MachineHealth) bool {
				return h.Score > 0
			}, 30*time.Second)
			if pass2 {
				fmt.Printf("  Detected on both machines\n")
			} else {
				fmt.Printf("  Detected on %s only (correlation may not have propagated to %s)\n", target1, target2)
			}
		}
		health := simFetchHealth()
		for _, t := range []string{target1, target2} {
			if h, ok := health[t]; ok {
				fmt.Printf("  %s: score=%d status=%s\n", t, h.Score, h.Status)
			}
		}
	}

	// Step 4: Cleanup
	fmt.Println("  Cleaning up...")
	if dryRun {
		fmt.Printf("  [DRY-RUN] Would unquarantine %s and %s\n", target1, target2)
	} else {
		for _, t := range []string{target1, target2} {
			ipCleanup := fmt.Sprintf(`sudo iptables -D INPUT -s %s -j DROP 2>/dev/null || true`, sourceIP)
			simSSH(t, ipCleanup)
			if err := simUnquarantine(t); err != nil {
				log.Printf("  Warning: unquarantine %s failed: %v", t, err)
			} else {
				fmt.Printf("  Unquarantined %s\n", t)
			}
		}
	}

	if !dryRun {
		simPrintResult("lateral-movement", pass, fmt.Sprintf("targets=%s,%s", target1, target2))
	} else {
		fmt.Println("  [DRY-RUN] Result: SKIP")
	}
	return nil
}

func simRunAll(sc *simConfig) error {
	target := sc.targets[0]
	dryRun := sc.dryRun

	scenarios := []struct {
		name string
		fn   func(string, bool) error
	}{
		{"brute-force", simBruteForce},
		{"credential-theft", simCredentialTheft},
		{"binary-tamper", simBinaryTamper},
		{"rogue-service", simRogueService},
	}

	fmt.Printf("=== Running all scenarios on %s ===\n\n", target)

	results := make([]string, 0, len(scenarios))
	for _, s := range scenarios {
		if err := s.fn(target, dryRun); err != nil {
			results = append(results, fmt.Sprintf("  %s: ERROR (%v)", s.name, err))
		} else {
			results = append(results, fmt.Sprintf("  %s: completed", s.name))
		}
		fmt.Println()
	}

	fmt.Println("=== Summary ===")
	fmt.Println("  (lateral-movement skipped -- requires two targets)")
	for _, r := range results {
		fmt.Println(r)
	}
	return nil
}
