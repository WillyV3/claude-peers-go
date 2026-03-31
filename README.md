# claude-peers-go

Peer discovery and messaging for Claude Code instances across machines.

## What it does

- Claude Code sessions automatically discover each other across your network
- Send messages between sessions in real time
- See what each Claude instance is working on (auto-generated summaries)
- UCAN-based cryptographic authentication (Ed25519 + JWT delegation chains)
- NATS JetStream for real-time event streaming (optional, falls back to HTTP polling)
- Fleet memory: shared markdown state synced across all machines

## Quick Start

### 1. Set up the broker (on your always-on server)

```bash
go install github.com/WillyV3/claude-peers-go@latest

claude-peers init broker
claude-peers broker
```

### 2. Set up each client machine

```bash
claude-peers init client http://<broker-ip>:7899
```

Copy `root.pub` from the broker machine to `~/.config/claude-peers/root.pub` on the client.

On the broker, issue a token for the client:

```bash
claude-peers issue-token /path/to/client-identity.pub peer-session
```

On the client, save the issued token:

```bash
claude-peers save-token <jwt>
```

### 3. Add to Claude Code

Add to your `~/.claude/settings.json`:

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

Claude Code sessions will now automatically register with the broker, discover peers, and exchange messages.

## Features

### Fleet Context Injection
When a Claude Code session starts, it automatically receives context about the fleet: who's online, what they're working on, recent events, and shared memory. Claude starts every session aware of the fleet without you asking.

### Smart Peer Naming
Peers are auto-named from git context: `my-project@main` instead of cryptic `machine:tty` identifiers. Override with the `set_name` MCP tool if you want a custom name.

### Cross-Session Messaging
Send messages between Claude sessions across machines. Messages are delivered instantly via channel notifications -- Claude responds automatically.

### Fleet Memory
Shared markdown state synced across all machines. Claude writes what happened, the next session reads it automatically.

## CLI Commands

```
claude-peers init <role> [url]              Generate config (broker or client)
claude-peers config                         Show current config
claude-peers broker                         Start the broker daemon
claude-peers server                         Start MCP stdio server (used by Claude Code)
claude-peers status                         Show broker status and all peers
claude-peers peers                          List all peers
claude-peers send <id> <msg>                Send a message to a peer
claude-peers issue-token <pub-path> <role>  Issue a UCAN token for a machine
claude-peers save-token <jwt>               Save a UCAN token locally
claude-peers refresh-token                  Renew current token
claude-peers mint-root                      Mint a new root token
claude-peers dream                          Snapshot fleet state to Claude memory
claude-peers dream-watch                    Watch fleet via NATS and keep memory fresh
claude-peers generate-nkey                  Generate a NATS NKey pair
claude-peers kill-broker                    Stop the broker daemon
claude-peers reauth-fleet                   Re-issue tokens for fleet machines
```

## Token Roles

| Role | Capabilities |
|------|-------------|
| `peer-session` | Register, heartbeat, list peers, send/poll messages, read/write memory |
| `fleet-read` | List peers, read events, read memory |
| `fleet-write` | List peers, read events, read/write memory |
| `cli` | List peers, send messages, read events |

## Configuration

Config file: `~/.config/claude-peers/config.json`

Environment variable overrides:

| Variable | Description |
|----------|-------------|
| `CLAUDE_PEERS_BROKER_URL` | Broker HTTP endpoint |
| `CLAUDE_PEERS_LISTEN` | Broker bind address |
| `CLAUDE_PEERS_MACHINE` | Machine name |
| `CLAUDE_PEERS_DB` | SQLite database path |
| `CLAUDE_PEERS_NATS` | NATS server URL |
| `CLAUDE_PEERS_NATS_TOKEN` | NATS auth token |
| `CLAUDE_PEERS_NATS_NKEY` | Path to NATS NKey seed |
| `CLAUDE_PEERS_TOKEN` | UCAN auth token |
| `CLAUDE_PEERS_LLM_URL` | LLM endpoint for auto-summaries |
| `CLAUDE_PEERS_LLM_API_KEY` | LLM API key |

## Broker API

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| GET | `/health` | None | Health check |
| POST | `/challenge` | None | Broker identity verification |
| POST | `/refresh-token` | Bearer | Refresh expiring token |
| POST | `/register` | `peer/register` | Register a peer |
| POST | `/heartbeat` | `peer/heartbeat` | Keep-alive |
| POST | `/set-summary` | `peer/set-summary` | Update peer summary |
| POST | `/set-name` | `peer/set-summary` | Update peer name |
| POST | `/list-peers` | `peer/list` | List peers |
| POST | `/send-message` | `msg/send` | Send message |
| POST | `/poll-messages` | `msg/poll` | Poll messages (marks delivered) |
| POST | `/peek-messages` | `msg/poll` | Peek messages (non-destructive) |
| POST | `/ack-message` | `msg/ack` | Acknowledge message |
| POST | `/unregister` | `peer/unregister` | Unregister peer |
| GET | `/events` | `events/read` | Recent events |
| GET | `/fleet-memory` | `memory/read` | Read fleet memory |
| POST | `/fleet-memory` | `memory/write` | Write fleet memory |

## Dependencies

- [golang-jwt/jwt](https://github.com/golang-jwt/jwt) - Ed25519 JWT tokens
- [nats-io/nats.go](https://github.com/nats-io/nats.go) - NATS JetStream (optional)
- [nats-io/nkeys](https://github.com/nats-io/nkeys) - NATS NKey authentication
- [modernc.org/sqlite](https://modernc.org/sqlite) - Pure-Go SQLite (no CGo)

## License

MIT
