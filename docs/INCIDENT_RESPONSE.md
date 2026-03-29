# Incident Response Playbook

This is the 3am runbook. When an alert fires, come here, find the scenario, follow the steps.

## Quick Reference

| Alert Type | Severity | Auto-Response | Human Action Required |
|---|---|---|---|
| SSH Brute Force | Tier 2 (Contain) | Forensic capture, IP block (1h auto-expire), email | Review forensics, check if IP is known, extend block if needed |
| Credential File Tampering | Tier 3 (Approval) | Forensic capture, email | Rotate all credentials on affected machine, audit access logs |
| Binary Tamper | Tier 2 (Contain) | Forensic capture with file hash, quarantine, email | Verify binary integrity, redeploy if needed, audit other machines |
| Rogue Systemd Service | Tier 1 (Auto) | Capture unit file, email | Review unit file, check if legitimate, remove if not |
| Lateral Movement | Tier 3 (Approval) | Forensic capture on all affected machines, email | Full fleet audit, credential rotation, network review |
| Generic Quarantine | Tier 1 (Auto) | Email notification | Investigate root cause, unquarantine when resolved |

## How You Get Notified

**Email:** The response daemon sends emails via `gws gmail +send` to vansicklewilly@gmail.com (configurable via `RESPONSE_EMAIL_TO` env var). Subject line format: `[fleet-security] SEVERITY on MACHINE: incident_type`. Throttled to 1 email per machine per 15 minutes.

**Gridwatch Dashboard:** The fleet health dashboard at `http://100.109.211.128:8888` shows real-time machine health scores. Machines turn yellow (degraded, score 5-9) or red (quarantined, score 10+). Run `claude-peers gridwatch` locally to view.

**NATS Events:** Security events flow through `fleet.security.>` subjects on the NATS server. The wazuh-bridge publishes Wazuh alerts, the security-watch correlates them, and the response-daemon acts on them.

**Broker API:** Check machine health directly:
```bash
curl -H "Authorization: Bearer $(cat ~/.config/claude-peers/token.jwt)" \
  http://100.109.211.128:7899/machine-health
```

## Scenario 1: SSH Brute Force

### What Happened
Someone (or a bot) is hammering SSH with failed login attempts. Wazuh rule 5763 fires after 8+ failures in a window. The wazuh-bridge publishes the event, security-watch may escalate if the source IP appears on multiple machines, and the response daemon classifies it as `brute_force` (Tier 2).

### Auto-Response Taken
1. Forensic snapshot captured (processes, listeners, logins, SSH logs, temp files, services)
2. Source IP blocked via `iptables -A INPUT -s <IP> -j DROP` (auto-expires after 1 hour)
3. Email sent with full incident details

### Investigate
1. Check the email for the source IP and event details.
2. SSH to the affected machine and review the forensic snapshot:
   ```bash
   ls ~/.config/claude-peers/forensics/
   cat ~/.config/claude-peers/forensics/<machine>-<timestamp>.json | jq .
   ```
3. Check if the IP is a known scanner:
   ```bash
   whois <source-ip>
   ```
4. Review auth logs for context around the failures:
   ```bash
   ssh <machine> "journalctl -u sshd --since '2 hours ago' | grep <source-ip>"
   ```
5. Check if the attacker succeeded at any point:
   ```bash
   ssh <machine> "last -20"
   ssh <machine> "who"
   ```

### Recover
1. If the IP block auto-expired and attacks resume, re-block permanently:
   ```bash
   ssh <machine> "sudo iptables -A INPUT -s <source-ip> -j DROP"
   ```
2. If the attacker got in, treat this as a full compromise -- jump to the General Recovery Checklist below.
3. If this was noise (scanner bots), no further action needed. The IP block expires in 1 hour.

### Prevent Recurrence
- Ensure `fail2ban` is running on all fleet machines
- Consider restricting SSH to Tailscale IPs only: `iptables -A INPUT -p tcp --dport 22 ! -i tailscale0 -j DROP`
- Audit that all machines use key-only auth (no password auth)

## Scenario 2: Credential File Tampering

### What Happened
Wazuh FIM detected changes to credential files in `~/.config/claude-peers/` (identity.pem, token.jwt, private keys). This is Wazuh level 12, which maps to critical severity (+10 score), triggering quarantine. The response daemon classifies it as `credential_theft` (Tier 3 -- requires human approval).

### Auto-Response Taken
1. Forensic snapshot captured
2. Machine quarantined (broker rejects requests from this machine)
3. Email sent with "ACTION REQUIRED" severity
4. Status set to `approval_pending`

### Investigate
1. SSH to the affected machine immediately:
   ```bash
   ssh <machine>
   ```
2. Check who touched the credential files:
   ```bash
   stat ~/.config/claude-peers/identity.pem
   stat ~/.config/claude-peers/token.jwt
   ls -la ~/.config/claude-peers/
   ```
3. Check recent processes and logins:
   ```bash
   last -20
   who
   ps auxf
   ```
4. Review the forensic snapshot for anything suspicious (unknown processes, unexpected listeners, unfamiliar temp files).
5. Check if the token was used:
   ```bash
   # On ubuntu-homelab (broker), check broker logs
   ssh ubuntu-homelab "journalctl -u claude-peers-broker --since '1 hour ago' | grep <machine>"
   ```

### Recover
1. Rotate credentials on the affected machine:
   ```bash
   ssh <machine>
   cd ~/.config/claude-peers
   # Back up old keys for forensic comparison
   cp identity.pem identity.pem.compromised
   cp token.jwt token.jwt.compromised
   ```
2. Generate new keypair:
   ```bash
   claude-peers init client http://100.109.211.128:7899
   ```
3. Issue new token from the broker:
   ```bash
   # On ubuntu-homelab
   claude-peers issue-token /path/to/<machine>-identity.pub peer-session
   ```
4. Save the new token on the affected machine:
   ```bash
   ssh <machine> "claude-peers save-token <new-jwt>"
   ```
5. Unquarantine:
   ```bash
   claude-peers unquarantine <machine>
   ```
6. Verify the machine can communicate with the broker:
   ```bash
   ssh <machine> "claude-peers status"
   ```

### Prevent Recurrence
- Audit file permissions on all machines: `~/.config/claude-peers/` should be 700, private keys should be 600
- Review who has SSH access to fleet machines
- Consider adding file integrity monitoring to additional sensitive paths

## Scenario 3: Binary Tamper

### What Happened
Wazuh FIM detected modification to a file in `/usr/local/bin/` matching `claude-peers*`. This triggers custom rule 100101 at level 13, which maps to critical severity and quarantine. The response daemon classifies it as `binary_tamper` (Tier 2).

### Auto-Response Taken
1. Forensic snapshot captured with file hash of the tampered binary
2. Machine quarantined
3. Email sent with incident details including the file hash

### Investigate
1. Check the forensic snapshot for the file hash:
   ```bash
   cat ~/.config/claude-peers/forensics/<machine>-<timestamp>.json | jq .FileHash
   ```
2. Compare against the known good hash:
   ```bash
   # On a trusted machine
   md5sum /usr/local/bin/claude-peers
   ```
3. SSH to the affected machine and check the binary:
   ```bash
   ssh <machine> "ls -la /usr/local/bin/claude-peers*"
   ssh <machine> "file /usr/local/bin/claude-peers"
   ssh <machine> "md5sum /usr/local/bin/claude-peers"
   ```
4. Check how it was modified:
   ```bash
   ssh <machine> "journalctl --since '2 hours ago' | grep -i 'claude-peers'"
   ssh <machine> "last -20"
   ```

### Recover
1. If the binary was legitimately updated (you did a deploy), just unquarantine:
   ```bash
   claude-peers unquarantine <machine>
   ```
2. If tampering is confirmed, redeploy the binary from a trusted source:
   ```bash
   # Build fresh on a trusted machine
   cd ~/projects/claude-peers && go build -o claude-peers .
   scp claude-peers <machine>:/usr/local/bin/claude-peers
   ```
3. Unquarantine after verified:
   ```bash
   claude-peers unquarantine <machine>
   ```
4. If compromise is suspected, follow the General Recovery Checklist.

### Prevent Recurrence
- Deploy binaries through a controlled pipeline, not ad-hoc
- Consider immutable binary deployments (read-only /usr/local/bin)
- Add Wazuh FIM to all binary paths across the fleet

## Scenario 4: Rogue Systemd Service

### What Happened
Wazuh FIM detected a new or modified systemd unit file. Custom rule 100130 fires at level 9 (warning severity, +1 score). The response daemon classifies it as `rogue_service` (Tier 1).

### Auto-Response Taken
1. Unit file content captured and stored in the incident record
2. Email sent with the unit file contents

### Investigate
1. Read the email -- the unit file content is included inline.
2. SSH to the machine and examine the unit file:
   ```bash
   ssh <machine> "cat <unit-file-path>"
   ssh <machine> "systemctl status <service-name>"
   ssh <machine> "systemctl is-enabled <service-name>"
   ```
3. Check if it was installed by a known package or process:
   ```bash
   ssh <machine> "journalctl --since '1 hour ago' | grep systemd"
   ```

### Recover
1. If the service is legitimate (you or a package manager installed it), no action needed.
2. If suspicious, disable and remove:
   ```bash
   ssh <machine> "systemctl --user stop <service-name> && systemctl --user disable <service-name>"
   ssh <machine> "rm <unit-file-path>"
   ssh <machine> "systemctl --user daemon-reload"
   ```
3. If the machine score has accumulated to degraded/quarantined from multiple warnings:
   ```bash
   claude-peers unquarantine <machine>
   ```

### Prevent Recurrence
- Audit all user-level systemd services periodically
- Monitor `~/.config/systemd/user/` across all fleet machines
- Restrict write access to system-level unit directories

## Scenario 5: Lateral Movement

### What Happened
The security-watch correlator detected a pattern: auth failures from an IP on one machine followed by successful auth from the same IP on another machine. This suggests an attacker compromised credentials on machine A and used them to access machine B. Classified as `lateral_movement` (Tier 3).

### Auto-Response Taken
1. Forensic snapshots captured on ALL affected machines
2. Email sent with "ACTION REQUIRED" severity
3. Status set to `approval_pending`

### Investigate
1. This is the most serious scenario. Drop everything.
2. Identify the source IP from the email.
3. Check all machines for activity from that IP:
   ```bash
   for machine in omarchy raspdeck macbook1 willyv4 thinkbook; do
     echo "=== $machine ==="
     ssh $machine "last -20 | grep <source-ip>" 2>/dev/null
     ssh $machine "journalctl -u sshd --since '4 hours ago' 2>/dev/null | grep <source-ip> | tail -5"
   done
   ```
4. On each affected machine, review forensics:
   ```bash
   ls ~/.config/claude-peers/forensics/ | grep <machine>
   ```
5. Check for unauthorized SSH keys:
   ```bash
   ssh <machine> "cat ~/.ssh/authorized_keys"
   ```
6. Check for persistence mechanisms:
   ```bash
   ssh <machine> "crontab -l"
   ssh <machine> "ls ~/.config/systemd/user/"
   ssh <machine> "ls /etc/systemd/system/ | grep -v '^$'"
   ```

### Recover
1. Block the source IP on ALL fleet machines:
   ```bash
   for machine in omarchy ubuntu-homelab raspdeck macbook1 willyv4 thinkbook; do
     ssh $machine "sudo iptables -A INPUT -s <source-ip> -j DROP" 2>/dev/null
   done
   ```
2. Rotate SSH keys on all affected machines.
3. Rotate claude-peers credentials on all affected machines (see Scenario 2 recovery steps).
4. Remove any unauthorized SSH keys from `~/.ssh/authorized_keys`.
5. Remove any persistence mechanisms found.
6. Unquarantine machines one by one after verification:
   ```bash
   claude-peers unquarantine <machine>
   ```

### Prevent Recurrence
- Restrict SSH to Tailscale network only on all machines
- Implement SSH key pinning (specific keys per machine)
- Enable Wazuh active response for automatic IP blocking on all agents
- Consider network segmentation within Tailscale ACLs

## Scenario 6: Generic Quarantine

### What Happened
A machine's health score exceeded 10 through accumulated security events that did not match a specific incident pattern. The broker quarantined the machine, rejecting its API requests.

### Auto-Response Taken
1. Email notification sent

### Investigate
1. Check the machine's health details:
   ```bash
   curl -s -H "Authorization: Bearer $(cat ~/.config/claude-peers/token.jwt)" \
     http://100.109.211.128:7899/machine-health | jq '.<machine>'
   ```
2. Review the events list in the health response to understand what accumulated.
3. SSH to the machine and investigate each event.

### Recover
1. Address the root cause of each contributing event.
2. Unquarantine:
   ```bash
   claude-peers unquarantine <machine>
   ```

## General Recovery Checklist

Use this when a machine is confirmed or suspected compromised.

- [ ] Capture forensic snapshot if not already done: check `~/.config/claude-peers/forensics/`
- [ ] Block attacker IP on all fleet machines
- [ ] Rotate SSH keys on the affected machine
- [ ] Rotate claude-peers credentials (keypair + token) on the affected machine
- [ ] Check `~/.ssh/authorized_keys` for unauthorized entries
- [ ] Check crontab for unauthorized jobs
- [ ] Check systemd user services for unauthorized units
- [ ] Review `/tmp` and `/var/tmp` for suspicious files
- [ ] Check running processes for anything unexpected
- [ ] Check listening ports for unauthorized services
- [ ] Verify binary integrity: `md5sum /usr/local/bin/claude-peers`
- [ ] Review Wazuh alerts for the past 24 hours on this machine
- [ ] If broker (ubuntu-homelab) was compromised: rotate NATS tokens, rotate all fleet tokens
- [ ] Unquarantine only after all checks pass
- [ ] Monitor the machine closely for 24 hours after recovery

## Emergency Contacts

| Who | Role | Reach |
|---|---|---|
| Willy | Fleet owner, all access | vansicklewilly@gmail.com |
| Corn | HFL partner, backup | Via HFL channels |

## Testing the Detection and Response Chain

Run the simulation harness to validate the full pipeline is working:

```bash
# Dry run first (no SSH commands executed)
claude-peers sim-attack brute-force --target=raspdeck --dry-run

# Live test on raspdeck (safe, low-priority machine)
claude-peers sim-attack brute-force --target=raspdeck

# Run all scenarios
claude-peers sim-attack --all --target=raspdeck

# Test lateral movement (requires two targets)
claude-peers sim-attack lateral-movement --target=raspdeck,willyv4

# Available scenarios
claude-peers sim-attack brute-force --target=<machine>
claude-peers sim-attack credential-theft --target=<machine>
claude-peers sim-attack binary-tamper --target=<machine>
claude-peers sim-attack rogue-service --target=<machine>
claude-peers sim-attack lateral-movement --target=<machine1>,<machine2>
```

The sim-attack command uses RFC 5737 documentation IPs (203.0.113.x) for simulated source addresses. It injects fake log entries via `logger`, then polls the broker's `/machine-health` endpoint to verify detection. Each scenario cleans up after itself (removes IP blocks, unquarantines machines, deletes test files).

Default target is `raspdeck`. The command will refuse to target `ubuntu-homelab` (the broker) without explicit confirmation.
