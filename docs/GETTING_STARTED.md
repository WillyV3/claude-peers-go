# Getting Started with Sontara Lattice

This guide walks you through setting up a Sontara Lattice fleet from scratch. By the end, you'll have Claude Code sessions talking to each other across machines with capability-scoped trust, autonomous daemons running background tasks, and a real-time dashboard showing everything.

## Prerequisites

- 2+ machines on a private network (Tailscale recommended)
- Go 1.25+ (for building from source)
- Claude Code installed on each machine
- NATS Server with JetStream enabled on one machine
- Docker on the broker machine (for Wazuh EDR)

## Step 1: Build the binary

```bash
git clone https://github.com/Human-Frontier-Labs-Inc/sontara-lattice.git
cd sontara-lattice
go build -o claude-peers .
```

Cross-compile for other architectures:
```bash
GOOS=linux GOARCH=arm64 go build -o claude-peers-arm64 .   # Pi 5, Apple Silicon Linux
GOOS=linux GOARCH=arm GOARM=7 go build -o claude-peers-armv7 .  # Pi Zero 2W
GOOS=darwin GOARCH=arm64 go build -o claude-peers-darwin .  # macOS Apple Silicon
```

Copy the binary to `~/.local/bin/claude-peers` on each machine.

## Step 2: Initialize the broker

Pick one always-on machine as your broker. This is the trust anchor.

```bash
claude-peers init broker
```

This generates:
- `~/.config/claude-peers/identity.pem` -- Ed25519 private key (root of trust)
- `~/.config/claude-peers/identity.pub` -- public key
- `~/.config/claude-peers/root.pub` -- root public key (distribute to all machines)
- `~/.config/claude-peers/token.jwt` -- self-signed root UCAN token
- `~/.config/claude-peers/config.json` -- broker config

Edit `config.json` to bind to your network interface:
```json
{
  "role": "broker",
  "broker_url": "http://<your-tailscale-ip>:7899",
  "listen": "<your-tailscale-ip>:7899"
}
```

Start the broker:
```bash
claude-peers broker
```

## Step 3: Set up NATS

Install and start NATS with JetStream:
```bash
# Install NATS server
# See: https://docs.nats.io/running-a-nats-service/introduction/installation

# Start with JetStream enabled
nats-server --jetstream
```

If you want token auth on NATS, add to the NATS config:
```
authorization {
  token: "your-nats-token"
}
```

Then add `"nats_token": "your-nats-token"` to each machine's `config.json`.

## Step 4: Initialize client machines

On each client machine:

```bash
claude-peers init client http://<broker-ip>:7899
```

This generates a keypair for the machine. Now distribute trust:

1. Copy `root.pub` from the broker to `~/.config/claude-peers/root.pub` on the client
2. Copy the client's `identity.pub` back to the broker machine

## Step 5: Issue capability tokens

On the broker machine, issue tokens for each client:

```bash
# For Claude Code sessions (full peer interaction)
claude-peers issue-token /path/to/client-identity.pub peer-session > client-token.jwt

# For read-only services (gridwatch, monitoring)
claude-peers issue-token /path/to/service-identity.pub fleet-read > service-token.jwt

# For services that write fleet memory (dream-watch, supervisor)
claude-peers issue-token /path/to/service-identity.pub fleet-write > service-token.jwt
```

On each client, save the issued token:
```bash
claude-peers save-token "$(cat client-token.jwt)"
```

## Step 6: Configure Claude Code MCP

Add to your Claude Code MCP settings (per-project or global):

```json
{
  "mcpServers": {
    "claude-peers": {
      "command": "claude-peers",
      "args": ["server"]
    }
  }
}
```

Start a Claude Code session. It will automatically register with the broker, discover other sessions, and be reachable for messages.

## Step 7: Start services (systemd)

On the broker machine, create systemd user services:

```bash
# Broker
cat > ~/.config/systemd/user/sontara-broker.service << 'EOF'
[Unit]
Description=Sontara Lattice Broker
After=network-online.target

[Service]
ExecStart=%h/.local/bin/claude-peers broker
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
EOF

# Fleet memory daemon
cat > ~/.config/systemd/user/sontara-dream.service << 'EOF'
[Unit]
Description=Sontara Lattice Fleet Memory
After=network-online.target

[Service]
ExecStart=%h/.local/bin/claude-peers dream-watch
Restart=always
RestartSec=5
Environment=CLAUDE_PEERS_TOKEN=<fleet-write-jwt>

[Install]
WantedBy=default.target
EOF

# Daemon supervisor
cat > ~/.config/systemd/user/sontara-supervisor.service << 'EOF'
[Unit]
Description=Sontara Lattice Daemon Supervisor
After=network-online.target

[Service]
ExecStart=%h/.local/bin/claude-peers supervisor
Restart=always
RestartSec=5
Environment=CLAUDE_PEERS_TOKEN=<fleet-write-jwt>
Environment=LITELLM_API_KEY=<your-llm-key>

[Install]
WantedBy=default.target
EOF

systemctl --user daemon-reload
systemctl --user enable --now sontara-broker sontara-dream sontara-supervisor

# Security watch (correlates EDR events, escalates, emails alerts)
cat > ~/.config/systemd/user/sontara-security-watch.service << 'EOF'
[Unit]
Description=Sontara Lattice Security Watch
After=network-online.target

[Service]
ExecStart=%h/.local/bin/claude-peers security-watch
Restart=always
RestartSec=5
Environment=CLAUDE_PEERS_TOKEN=<fleet-write-jwt>

[Install]
WantedBy=default.target
EOF

systemctl --user enable --now sontara-security-watch
```

## Step 8: Set up Wazuh EDR (optional but recommended)

On the broker machine, deploy Wazuh Manager via Docker:

```bash
mkdir -p ~/docker/wazuh
cd ~/docker/wazuh

# Create docker-compose.yml (see docs/WAZUH_SETUP.md for full config)
docker compose up -d
```

Install Wazuh agents on each machine:
```bash
# Ubuntu/Debian
sudo WAZUH_MANAGER="<broker-ip>" apt install wazuh-agent
sudo systemctl enable --now wazuh-agent

# Arch (AUR)
yay -S wazuh-agent
```

Start the NATS bridge:
```bash
cat > ~/.config/systemd/user/sontara-wazuh-bridge.service << 'EOF'
[Unit]
Description=Sontara Lattice Wazuh Bridge
After=network-online.target docker.service

[Service]
ExecStart=%h/.local/bin/claude-peers wazuh-bridge
Restart=always
RestartSec=5
Environment=WAZUH_ALERTS_PATH=/path/to/wazuh/logs/alerts/alerts.json
Environment=CLAUDE_PEERS_TOKEN=<fleet-write-jwt>

[Install]
WantedBy=default.target
EOF

systemctl --user enable --now sontara-wazuh-bridge
```

## Step 9: Set up Gridwatch dashboard (optional)

Create `~/.config/claude-peers/gridwatch.json` with your machine definitions:

```json
{
  "port": 8888,
  "machines": [
    {"id": "machine1", "host": "100.x.x.x", "os": "arch", "specs": "N100 / 16GB", "ip": "100.x.x.x"},
    {"id": "machine2", "host": "100.x.x.x", "os": "ubuntu", "specs": "32GB", "ip": "100.x.x.x"}
  ],
  "llm_url": "http://your-llm-server:8080",
  "nats_url": "nats://100.x.x.x:4222"
}
```

```bash
claude-peers gridwatch
# Dashboard at http://localhost:8888
```

## Step 10: Create your first daemon

Create a directory under your daemons path:

```bash
mkdir -p ~/claude-peers-daemons/my-daemon
```

Create the daemon definition:

```bash
# my-daemon/daemon.json
cat > ~/claude-peers-daemons/my-daemon/daemon.json << 'EOF'
{
  "schedule": "interval:30m",
  "description": "My first autonomous daemon"
}
EOF

# my-daemon/my-daemon.agent (Agentfile DSL)
cat > ~/claude-peers-daemons/my-daemon/my-daemon.agent << 'EOF'
NAME my-daemon

GOAL check "Do something useful every 30 minutes.

Steps:
1. Check some condition
2. Take action if needed
3. Report results

Be concise."

RUN main USING check
EOF

# my-daemon/agent.toml (LLM config)
cat > ~/claude-peers-daemons/my-daemon/agent.toml << 'EOF'
[llm]
provider = "openai-compat"
model = "claude-haiku"
max_tokens = 16384
base_url = "http://your-llm-proxy:4000/v1"
api_key_env = "LITELLM_API_KEY"
EOF

# my-daemon/policy.toml (guardrails)
cat > ~/claude-peers-daemons/my-daemon/policy.toml << 'EOF'
[tools]
allow = ["bash", "read"]
deny = ["rm -rf", "shutdown"]
EOF
```

The supervisor will discover it on next restart and run it on schedule.

## Verify everything works

```bash
# Check broker status
claude-peers status

# List all peers
claude-peers peers

# Check machine health (security)
curl -H "Authorization: Bearer $(cat ~/.config/claude-peers/token.jwt)" http://<broker-ip>:7899/machine-health

# Open gridwatch
open http://<gridwatch-ip>:8888
```

## Troubleshooting

**Peer can't connect to broker**: Check `token.jwt` exists and hasn't expired (24h default). Re-issue with `issue-token`.

**401 on broker requests**: UCAN token is missing, expired, or was issued by a different root key. Verify `root.pub` matches on both machines.

**403 MISSING_CAPABILITY**: Token doesn't have the required capability for that endpoint. Check the role used when issuing.

**403 QUARANTINED**: Machine's health score triggered quarantine from Wazuh alerts. Investigate the security event, then: `claude-peers unquarantine <machine>`

**Daemon not running**: Check triage script (`triage.sh` must exit 0 for the daemon to run). Check supervisor logs: `journalctl --user -u sontara-supervisor`

**No peer summaries**: The MCP server needs `llm_base_url` and `llm_api_key` in config.json to generate auto-summaries via LLM.
