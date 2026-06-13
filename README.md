# claude-peers-go

> **Lineage:** extracted and rewritten from [sontara-lattice](https://github.com/Human-Frontier-Labs-Inc/sontara-lattice), which prototyped the peer discovery pattern alongside UCAN auth, Wazuh EDR, and fleet orchestration. This repo is the clean Go rewrite of just the peer layer — broker + MCP stdio server + UCAN token auth. It runs in production on a multi-machine Tailscale fleet coordinating 8+ specialized Claude Code agents.

Peer discovery and messaging between Claude Code sessions.

Run multiple Claude Code instances on the same machine? They can now see each other, send messages, and share context -- automatically.

## Security model

claude-peers is designed for deployments where the broker and peers share a private network (Tailscale, WireGuard, VPN, or a single machine). The security guarantees rely on two layers:

**1. Transport encryption — provided by your network layer.**

The broker itself speaks plain HTTP. It is meant to be reached over an encrypted network underlay:

- **Tailscale / WireGuard (recommended):** every byte between any two tailnet nodes is wrapped in WireGuard (Curve25519 key exchange, ChaCha20-Poly1305 AEAD, mutually authenticated by node keys). Stronger than typical TLS because both endpoints are cryptographically identified, not just the server.
- **Single-machine:** broker binds to `127.0.0.1` by default. Loopback traffic doesn't leave the host.
- **Cloudflare Tunnel / reverse proxy (caddy/nginx):** TLS terminates at the proxy edge. The cloudflared-to-broker hop stays on localhost.

**Do not expose the broker directly to the public internet without one of these.** No TLS = no transport privacy in that case.

For single-binary public deployments without a reverse proxy, the broker has built-in Let's Encrypt support via [autocert](https://pkg.go.dev/golang.org/x/crypto/acme/autocert) (TLS-ALPN-01 challenge, no port 80 needed):

```bash
# DNS: broker.example.com -> this machine's public IP
# Firewall: port 443 reachable
claude-peers broker --tls-domain broker.example.com --tls-email you@example.com
```

The cert is issued on first request, cached in `~/.config/claude-peers/autocert/`, and auto-renewed before expiry. `HostPolicy` is locked to your configured domains so the broker won't burn rate limits issuing certs for unauthorized hostnames.

**2. Authentication and authorization — UCAN tokens.**

Every request to the broker carries an Ed25519-signed JWT ([UCAN](https://ucan.xyz/), see [Auth](#auth) below). The broker validates the signature chain back to a root key generated at `init`. Tokens are capability-scoped, time-limited, and persisted across broker restarts (T12) so delegation chains survive process restarts without re-authentication.

**What the broker does NOT secure:**

- Token JWTs are signed but not encrypted; the network layer provides confidentiality. If you skip Tailscale/TLS, an on-path attacker on the same network can read tokens.
- The broker trusts every peer that presents a valid token. There's no per-peer rate limit on messages within the token's capabilities (yet — see [#11](https://github.com/WillyV3/claude-peers-go/issues)).
- No protection against malicious peers spamming the broker once they have a token. Token issuance is the trust boundary; only issue tokens to peers you control.

If you're deploying claude-peers, the practical answer is: **put it behind Tailscale or a reverse proxy.** Both are zero-cost and well-trodden patterns for self-hosted Go services.

## What it does

- Claude Code sessions automatically discover each other
- Send messages between sessions in real time
- See what each Claude is working on (auto-generated summaries)
- New sessions start with full context: who's active, what they're doing, recent events
- Shared memory: Claude writes what happened, the next session reads it

## Quick Start

```bash
go install github.com/WillyV3/claude-peers-go@latest

# Initialize and start the broker (runs in background)
claude-peers init broker
claude-peers broker &
```

Add to your `~/.claude.json` (or `~/.claude/settings.json`):

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

That's it. Open multiple Claude Code sessions -- they'll discover each other automatically.

## How it works

A lightweight broker process runs on your machine. Each Claude Code session connects to it via an MCP server. The broker tracks who's active, routes messages, and stores shared memory.

```
Claude Code (session 1) ──┐
Claude Code (session 2) ──┼── broker (localhost:7899) ── SQLite
Claude Code (session 3) ──┘
```

Each session gets:
- **Auto-naming** from git context: `my-project@main` instead of `session-47`
- **Context injection** at start: knows who's online and what they're doing
- **Message delivery** via channel notifications -- Claude responds automatically
- **Shared memory** that persists between sessions

## Features

### Context Injection
When a session starts, it receives a snapshot of the current state: active peers, recent events, shared memory. Claude starts every session aware of what's happening without you asking.

### Smart Naming
Peers are auto-named from context. In a git repo: `my-project@main`. In a plain directory: `projects/my-app`. In your home directory: `laptop:3` (machine + terminal). Override anytime with the `set_name` MCP tool. The auto-summary (generated by haiku) provides the real description of what each session is doing.

### Messaging
Send messages between Claude sessions. Messages are delivered instantly via channel notifications. Useful for coordinating work across multiple sessions.

### Shared Memory
Markdown-based shared state. One session writes context, the next session reads it. Survives session restarts.

## Multi-Machine Setup (Advanced)

For running across multiple machines (e.g., laptop + server):

```bash
# On the broker machine
claude-peers init broker
claude-peers broker

# On each remote machine
claude-peers init client http://<broker-ip>:7899
```

Then issue tokens for each machine:

```bash
# On the broker
claude-peers issue-token /path/to/remote-identity.pub peer-session

# Long-lived token for always-on peers (voice devices, daemons) —
# 30 days instead of the 24h default, kills the rotation treadmill:
claude-peers issue-token --ttl 30d /path/to/remote-identity.pub peer-session

# On the remote machine
claude-peers save-token <jwt>
```

Optionally add [NATS JetStream](https://nats.io/) for real-time event streaming across machines.

## CLI Reference

```
claude-peers init broker                   Set up broker (generates keys + token)
claude-peers init client <broker-url>      Connect to a remote broker
claude-peers broker [--tls-domain D]       Start the broker (add --tls-domain for autocert TLS)
claude-peers server                        Start MCP server (used by Claude Code)
claude-peers status                        Show broker status and peers
claude-peers peers                         List all peers
claude-peers send <id> <msg>               Send a message to a peer
claude-peers dream                         Snapshot state to shared memory
claude-peers dream-watch                   Watch events and keep memory fresh
claude-peers kill-broker                   Stop the broker
```

### Token Management

```
claude-peers issue-token [--ttl D] <pub> <role>   Issue a token (default 24h)
claude-peers save-token <jwt>                     Save a token locally
claude-peers refresh-token                        Renew an expiring token
claude-peers mint-root                            Mint a new root token
```

`--ttl` accepts Go durations (`24h`, `72h30m`) or day shorthand (`30d`). Max
is `365d` (the root token's own lifetime). The default is taken from
`default_child_ttl` in `config.json` or `CLAUDE_PEERS_DEFAULT_TTL`, and falls
back to `24h`. Set it to `30d` on always-on brokers to make `refresh-token`
and freshly-issued tokens all land at 30 days instead of 24 hours — this is
what kills the rotation treadmill that would otherwise brick voice peers
every day.

## Configuration

Config: `~/.config/claude-peers/config.json`

| Variable | Description |
|----------|-------------|
| `CLAUDE_PEERS_BROKER_URL` | Broker endpoint (default: `http://127.0.0.1:7899`) |
| `CLAUDE_PEERS_MACHINE` | Machine name |
| `CLAUDE_PEERS_NATS` | NATS server URL (optional) |
| `CLAUDE_PEERS_LLM_URL` | LLM endpoint for auto-summaries (optional) |
| `CLAUDE_PEERS_DEFAULT_TTL` | Default child-token TTL (e.g. `30d`, default `24h`) |
| `CLAUDE_PEERS_TLS_DOMAIN` | Comma-separated hostnames for Let's Encrypt (enables TLS) |
| `CLAUDE_PEERS_TLS_EMAIL` | Account email for ACME renewal notices |
| `CLAUDE_PEERS_TLS_CACHE_DIR` | Where autocert persists certs (default `~/.config/claude-peers/autocert`) |
| `CLAUDE_PEERS_TLS_ACME_STAGING` | `1`/`true` to use Let's Encrypt staging for testing |

## Auth

Uses [UCAN](https://ucan.xyz/) (User Controlled Authorization Networks) -- Ed25519 key pairs with JWT delegation chains. The broker generates a root key at init. Tokens are scoped by capability:

| Role | Can do |
|------|--------|
| `peer-session` | Register, message, read/write memory |
| `fleet-read` | Read-only: list peers, events, memory |
| `fleet-write` | Read/write memory, list peers, read events |
| `cli` | List peers, send messages, read events |

## Dependencies

- [golang-jwt/jwt](https://github.com/golang-jwt/jwt) -- Ed25519 JWT
- [modernc.org/sqlite](https://modernc.org/sqlite) -- Pure-Go SQLite
- [nats-io/nats.go](https://github.com/nats-io/nats.go) -- NATS JetStream (optional)

## License

MIT
