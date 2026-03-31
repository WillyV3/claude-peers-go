# Setup Guide

## Prerequisites

- Go 1.25+ (as declared in `go.mod`; uses `strings.SplitSeq` added in 1.24 and other 1.22+ features)
- NATS server (optional, for real-time event streaming)

## Single-Machine Setup (Simplest)

If all your Claude Code sessions run on one machine:

```bash
# Build
go install github.com/WillyV3/claude-peers-go@latest

# Initialize as broker (generates keypair + root token)
claude-peers init broker

# Add to Claude Code
# In ~/.claude/settings.json:
{
  "mcpServers": {
    "claude-peers": {
      "command": "claude-peers",
      "args": ["server"]
    }
  }
}
```

The MCP server auto-starts the broker when it detects a local broker URL. No manual broker startup needed for single-machine use.

## Multi-Machine Setup

### Broker Machine (always-on server)

```bash
claude-peers init broker
claude-peers broker  # or run as a systemd service
```

This generates:
- `~/.config/claude-peers/identity.pem` -- broker private key
- `~/.config/claude-peers/identity.pub` -- broker public key
- `~/.config/claude-peers/root.pub` -- root public key (distribute to clients)
- `~/.config/claude-peers/root-token.jwt` -- root token (keep secret)
- `~/.config/claude-peers/config.json` -- broker config

### Client Machines

```bash
# Initialize as client, pointing to broker
claude-peers init client http://<broker-ip>:7899

# Copy root.pub from broker to verify its identity
scp broker:~/.config/claude-peers/root.pub ~/.config/claude-peers/root.pub

# On the BROKER, issue a token for this client
claude-peers issue-token /path/to/client-identity.pub peer-session

# Save the issued token on the client
claude-peers save-token <jwt-from-broker>
```

### Token Refresh

Tokens expire after 24 hours by default. The MCP server auto-refreshes tokens when they near expiry. You can also manually refresh:

```bash
claude-peers refresh-token
```

### NATS (Optional)

For real-time event streaming instead of HTTP polling:

1. Install and run a NATS server with JetStream enabled
2. Configure NATS URL in config or via environment:
   ```bash
   export CLAUDE_PEERS_NATS=nats://your-nats-server:4222
   ```
3. For per-machine auth, generate NKeys:
   ```bash
   claude-peers generate-nkey
   # Add the public key to your NATS server config
   ```

### Running as a systemd Service

```ini
[Unit]
Description=claude-peers broker
After=network.target

[Service]
ExecStart=/path/to/claude-peers broker
Restart=always
User=youruser

[Install]
WantedBy=multi-user.target
```

## Configuration Reference

Config file: `~/.config/claude-peers/config.json`

```json
{
  "role": "client",
  "broker_url": "http://127.0.0.1:7899",
  "listen": "127.0.0.1:7899",
  "machine_name": "my-machine",
  "db_path": "/home/user/.claude-peers.db",
  "stale_timeout": 300,
  "nats_url": "",
  "nats_token": "",
  "nats_nkey_seed": "",
  "llm_base_url": "http://127.0.0.1:4000/v1",
  "llm_model": "claude-haiku",
  "llm_api_key": ""
}
```

All fields can be overridden via environment variables. See README.md for the full list.

## Auto-Summary

claude-peers can auto-generate a summary of what each session is working on using an LLM. Configure an OpenAI-compatible endpoint:

```bash
export CLAUDE_PEERS_LLM_URL=http://your-llm:4000/v1
export CLAUDE_PEERS_LLM_API_KEY=your-key
```

The summary model defaults to `claude-haiku` and can be overridden with `CLAUDE_PEERS_LLM_MODEL`.

## Troubleshooting

**Broker not reachable**: Check that the broker URL is correct and the broker is running. Use `claude-peers status` to test connectivity.

**Token errors**: Run `claude-peers refresh-token` to get a fresh token. If that fails, re-issue from the broker with `claude-peers issue-token`.

**NATS connection failures**: Non-fatal. The system falls back to HTTP polling automatically. Check NATS server status and authentication config.

**Stale peers**: Peers are automatically cleaned after `stale_timeout` seconds (default 300s) without a heartbeat. The MCP server sends heartbeats every 15 seconds.
