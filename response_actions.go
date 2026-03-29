package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// sshExec runs a command on a remote machine via SSH.
func (rd *ResponseDaemon) sshExec(machine, command string) (string, error) {
	target, ok := rd.sshTargets[machine]
	if !ok {
		return "", fmt.Errorf("unknown machine: %s", machine)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ssh",
		"-o", "ConnectTimeout=5",
		"-o", "StrictHostKeyChecking=no",
		"-o", "BatchMode=yes",
		target, command)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("ssh %s: %w: %s", machine, err, string(out))
	}
	return string(out), nil
}

// captureForensics collects a forensic snapshot from a machine.
func (rd *ResponseDaemon) captureForensics(machine string, extraFile string) (*ForensicSnapshot, error) {
	if rd.dryRun {
		log.Printf("[response] DRY-RUN: would capture forensics on %s", machine)
		return &ForensicSnapshot{Machine: machine, CapturedAt: time.Now()}, nil
	}

	osType := rd.machineOS[machine]
	isMAC := osType == "macos"

	snap := &ForensicSnapshot{
		Machine:    machine,
		CapturedAt: time.Now(),
	}

	// Processes
	if isMAC {
		snap.Processes, _ = rd.sshExec(machine, "ps aux")
	} else {
		snap.Processes, _ = rd.sshExec(machine, "ps auxf")
	}

	// Listeners
	if isMAC {
		snap.Listeners, _ = rd.sshExec(machine, "lsof -iTCP -sTCP:LISTEN -n -P")
	} else {
		snap.Listeners, _ = rd.sshExec(machine, "ss -tlnp")
	}

	// Recent logins
	snap.RecentLogins, _ = rd.sshExec(machine, "last -20")

	// Current users
	snap.CurrentUsers, _ = rd.sshExec(machine, "who")

	// SSH logs
	if isMAC {
		snap.SSHLogs, _ = rd.sshExec(machine, `log show --predicate 'process == "sshd"' --last 1h --style compact 2>/dev/null | tail -30`)
	} else {
		snap.SSHLogs, _ = rd.sshExec(machine, `journalctl -u sshd --since "1 hour ago" --no-pager 2>/dev/null | tail -30`)
	}

	// Temp files
	if isMAC {
		snap.TempFiles, _ = rd.sshExec(machine, "find /tmp -mmin -60 -type f 2>/dev/null | head -20")
	} else {
		snap.TempFiles, _ = rd.sshExec(machine, "find /tmp /var/tmp -mmin -60 -type f 2>/dev/null | head -20")
	}

	// Services
	if isMAC {
		snap.Services, _ = rd.sshExec(machine, "launchctl list | head -30")
	} else {
		snap.Services, _ = rd.sshExec(machine, "systemctl list-units --type=service --state=running --no-pager | head -30")
	}

	// Extra file hash
	if extraFile != "" {
		if isMAC {
			snap.FileHash, _ = rd.sshExec(machine, "md5 "+extraFile)
		} else {
			snap.FileHash, _ = rd.sshExec(machine, "md5sum "+extraFile)
		}
	}

	// Save snapshot to disk
	if err := os.MkdirAll(rd.forensicDir, 0755); err != nil {
		log.Printf("[response] failed to create forensic dir: %v", err)
	} else {
		filename := fmt.Sprintf("%s-%s.json", machine, time.Now().Format("20060102-150405"))
		data, err := json.MarshalIndent(snap, "", "  ")
		if err == nil {
			path := filepath.Join(rd.forensicDir, filename)
			if err := os.WriteFile(path, data, 0644); err != nil {
				log.Printf("[response] failed to write forensic snapshot: %v", err)
			} else {
				log.Printf("[response] forensic snapshot saved: %s", path)
			}
		}
	}

	return snap, nil
}

// executeIPBlock blocks a source IP on a machine via iptables.
func (rd *ResponseDaemon) executeIPBlock(machine, sourceIP string) error {
	if strings.HasPrefix(sourceIP, "100.") {
		return fmt.Errorf("refusing to block Tailscale IP %s", sourceIP)
	}

	if rd.dryRun {
		log.Printf("[response] DRY-RUN: would block IP %s on %s", sourceIP, machine)
		return nil
	}

	_, err := rd.sshExec(machine, fmt.Sprintf("sudo iptables -A INPUT -s %s -j DROP 2>/dev/null || true", sourceIP))
	if err != nil {
		return fmt.Errorf("ip block on %s: %w", machine, err)
	}

	rd.mu.Lock()
	rd.ipBlocks[machine+":"+sourceIP] = time.Now().Add(rd.ipBlockDuration)
	rd.mu.Unlock()

	log.Printf("[response] IP %s blocked on %s (expires in %s)", sourceIP, machine, rd.ipBlockDuration)
	return nil
}

// removeIPBlock removes an IP block from a machine.
func (rd *ResponseDaemon) removeIPBlock(machine, sourceIP string) error {
	if rd.dryRun {
		log.Printf("[response] DRY-RUN: would unblock IP %s on %s", sourceIP, machine)
		return nil
	}

	_, err := rd.sshExec(machine, fmt.Sprintf("sudo iptables -D INPUT -s %s -j DROP 2>/dev/null || true", sourceIP))
	if err != nil {
		return fmt.Errorf("ip unblock on %s: %w", machine, err)
	}

	rd.mu.Lock()
	delete(rd.ipBlocks, machine+":"+sourceIP)
	rd.mu.Unlock()

	log.Printf("[response] IP %s unblocked on %s", sourceIP, machine)
	return nil
}

// sendIncidentEmail sends an incident notification via gws CLI.
func (rd *ResponseDaemon) sendIncidentEmail(incident *Incident) error {
	if rd.dryRun {
		log.Printf("[response] DRY-RUN: would send email for %s incident on %s", incident.Type, strings.Join(incident.Machines, ", "))
		return nil
	}

	// Throttle: max 1 email per machine per 15 min
	machine := ""
	if len(incident.Machines) > 0 {
		machine = incident.Machines[0]
	}
	if machine != "" {
		rd.mu.Lock()
		if lastSent, ok := rd.emailThrottle[machine]; ok {
			if time.Since(lastSent) < 15*time.Minute {
				rd.mu.Unlock()
				log.Printf("[response] email throttled for %s (last sent %s ago)", machine, time.Since(lastSent).Round(time.Second))
				return nil
			}
		}
		rd.emailThrottle[machine] = time.Now()
		rd.mu.Unlock()
	}

	severity := "ALERT"
	switch incident.Tier {
	case TierContain:
		severity = "CONTAIN"
	case TierApproval:
		severity = "ACTION REQUIRED"
	}

	subject := fmt.Sprintf("[fleet-security] %s on %s: %s", severity, strings.Join(incident.Machines, ", "), string(incident.Type))

	var body strings.Builder
	body.WriteString(fmt.Sprintf("Incident: %s\n", incident.Type))
	body.WriteString(fmt.Sprintf("Tier: %d\n", incident.Tier))
	body.WriteString(fmt.Sprintf("Status: %s\n", incident.Status))
	body.WriteString(fmt.Sprintf("Machines: %s\n", strings.Join(incident.Machines, ", ")))
	body.WriteString(fmt.Sprintf("Created: %s\n\n", incident.CreatedAt.Format(time.RFC3339)))

	if len(incident.Events) > 0 {
		body.WriteString("Events:\n")
		for _, e := range incident.Events {
			body.WriteString(fmt.Sprintf("  - [%s] %s: %s (rule=%s, severity=%s)\n", e.Timestamp, e.Machine, e.Description, e.RuleID, e.Severity))
			if e.SourceIP != "" {
				body.WriteString(fmt.Sprintf("    Source IP: %s\n", e.SourceIP))
			}
			if e.FilePath != "" {
				body.WriteString(fmt.Sprintf("    File: %s\n", e.FilePath))
			}
		}
	}

	if len(incident.Actions) > 0 {
		body.WriteString("\nActions taken:\n")
		for _, a := range incident.Actions {
			body.WriteString(fmt.Sprintf("  - %s on %s: %s (%s)\n", a.Type, a.Machine, a.Detail, a.Status))
		}
	}

	cmd := exec.Command("resend-email",
		"-m", body.String(),
		rd.emailTo,
		subject)
	if err := cmd.Start(); err != nil {
		log.Printf("[response] email send failed: %v", err)
		return err
	}
	go func() {
		if err := cmd.Wait(); err != nil {
			log.Printf("[response] email command failed: %v", err)
		}
	}()
	log.Printf("[response] email sent: %s", subject)
	return nil
}

// captureUnitFile reads a unit file from a remote machine.
func (rd *ResponseDaemon) captureUnitFile(machine, filePath string) (string, error) {
	if rd.dryRun {
		log.Printf("[response] DRY-RUN: would capture unit file %s on %s", filePath, machine)
		return "", nil
	}
	return rd.sshExec(machine, "cat "+filePath)
}
