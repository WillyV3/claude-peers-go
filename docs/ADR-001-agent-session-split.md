# ADR-001: Agent/Session split

**Status:** Accepted
**Date:** 2026-04-08

## Context

The broker conflates two concepts under a single `peers` table:

- **Agent** — a stable role a human addresses (`caretaker`, `jim`). Should survive process restarts.
- **Session** — one running `claude` process. Rotates every launch.

This produced six concrete failures in production:

1. **Name collisions.** Two sessions in the same project both `autoName` to the same string. `sendMessage("caretaker")` resolves to `most-recent(last_seen)` — a race.
2. **"Same CWD gets weird."** Not actually about CWD. Two concurrent sessions in the same project have identical auto-names → routing is nondeterministic.
3. **Dead PID cleanup.** `DELETE FROM peers WHERE pid = ? AND machine = ?` at `broker_core.go:225` — PID is the claude-peers subprocess, new every launch, so this line never matches. Dead code masquerading as safety.
4. **TTY fallback hole.** `getTTY()` shells out to `ps -o tty=`. Headless `claude -p` runs return `?` → no TTY → no stale cleanup → ghost peers accumulate.
5. **Push is admittedly unreliable.** `mcpInstructions` literally say *"CALL check_messages ON EVERY USER PROMPT"*. The `notifications/claude/channel` push has no ACK — if MCP stdio stalls, messages are silently lost.
6. **No stable session identity.** Identity is `(machine, tty, autoname)` — all three derived, none declared. Any stable-handle feature is bolted on top of a rotating base.

## Decision

Split Agent and Session into two tables. Agent identity is **declared, not derived**. Messaging routes through agent names, not session IDs.

### Data model

```
sessions                             messages
───────────────                      ───────────────
session_id    TEXT PK                id             INTEGER PK
agent_name    TEXT (nullable)        to_agent       TEXT NOT NULL
machine       TEXT                   from_session   TEXT NOT NULL
cwd           TEXT                   from_agent     TEXT
git_root      TEXT                   text           TEXT
tty           TEXT                   sent_at        TEXT
project       TEXT                   delivered_at   TEXT (null = undelivered)
branch        TEXT                   ack_at         TEXT (null = not acked)
summary       TEXT                   ack_session    TEXT
started_at    TEXT                   attempts       INTEGER DEFAULT 0
last_seen     TEXT
```

No separate `agents` table. An agent "exists" iff at least one current-or-historical session declared it. That's enough — we don't need agent metadata outside of what the current session provides.

### Rules

**R1. Agents are declared, not derived.**
Session registers with `agent_name` from, in order: `--as <name>` flag → `CLAUDE_PEERS_AGENT` env var → `.claude-peers-agent` file in cwd. If none provided, session is **ephemeral** — it exists (appears in `list_sessions`, can send messages, can be seen) but `agent_name` is NULL and it cannot be messaged by name.

No fallback to autoName. No dir-basename guess. No `machine:tty` construction. Identity is explicit or absent.

**R2. Agent names are globally unique while held.**
Second session trying to register with an agent name that's already held by a live session gets a **hard 409**:
```
agent 'caretaker' already held by session a4f2b1c3
  machine: host-alpha
  cwd:     /home/user/projects/edge-gateway
  started: 2026-04-08T14:32:11Z

kill that session or pick a different agent name.
```
No silent `-2` suffix. Fail fast.

**R3. Messages route to agent name, queue if offline.**
`send_message("caretaker", ...)` inserts with `to_agent = "caretaker"`. Broker finds active session holding that agent and pushes. If no active session holds the agent, message queues in the `messages` table with `delivered_at = NULL`. When a session next registers with `agent_name = "caretaker"`, undelivered messages drain.

Messages addressed to ephemeral sessions use `to_session` instead — session ID directly, no agent. If the session is gone, message is dropped (ephemeral sessions can't have queues).

**R4. Push requires ACK.**
Broker sends `notifications/claude/channel` with a `message_id` field. Client MUST send `notifications/claude/channel/ack` with that `message_id` back. Broker marks `delivered_at` and `ack_at` only after ACK. Retries once after 2s if no ACK. After 2 failed attempts, message remains in queue for the next poll.

This kills the "call check_messages on every prompt" crutch in the MCP instructions.

**R5. Clean unregister frees the name immediately. Dirty death frees within 60s.**
Graceful shutdown calls `/unregister` via `defer`. Crash or force-kill: `cleanStaleSessions` sweeps anything with `last_seen < now - 60s`. When a session is removed, its agent name becomes available immediately.

**R6. Delete dead code.**
- `autoName` in `fleet_git.go` — deleted. Auto-name was the problem.
- Dead PID cleanup in `register` — deleted.
- TTY disambiguation logic — deleted. TTY still recorded for debugging but no longer used for identity.
- "CALL check_messages ON EVERY USER PROMPT" rule in `mcpInstructions` — deleted. Push is reliable now.

### What "two claude sessions in same cwd" means now

- Neither specifies identity → both are ephemeral sessions with auto-generated session IDs like `session-a4f2`, both appear in `list_sessions`, neither addressable by agent name. Intentional.
- Both tried `--as work` → second one fails with a clear 409 telling the user exactly where the first one is running. User decides.
- One says `--as left`, other `--as right` → both addressable, both stable, restart-proof. Messages survive crashes.

## Test harness

`arch_test.go` — 10 scenarios, each runs against a real broker with an in-memory SQLite. Build before the rewrite to prove the current broker fails; run after to prove the rewrite succeeds.

| # | Scenario | Asserts |
|---|---|---|
| T1 | Two named agents exchange messages | Both sides receive, ACK, broker marks delivered |
| T2 | Message to offline agent → queued → delivered on reconnect | Queue drains when same agent_name re-registers |
| T3 | Name collision: second register with held name | Returns 409 with clear error containing first session's location |
| T4 | Ephemeral session (no --as) cannot be messaged by name | `send_message("<never-declared>")` returns 404 |
| T5 | Dirty death: session crashes, stale sweep runs, name freed | New session claims within staleTimeout + 1s |
| T6 | Push ACK timeout | Broker retries once after 2s, then falls back to poll |
| T7 | Queue TTL | Messages older than 24h dead-lettered from the queue |
| T8 | Concurrent send to same agent | All delivered in send order, no dupes |
| T9 | Graceful unregister frees name immediately | New session with same agent name registers successfully in the same second |
| T10 | Restart continuity: agent "jim" sends, dies, reconnects | Queued messages for "jim" drain on reconnect |

## Consequences

**Positive:**
- All six failures above are eliminated by construction, not by patches.
- Identity is something users can reason about — either you named yourself or you didn't.
- Tests exist as the specification.

**Negative:**
- `--as` is now required for any meaningful multi-session messaging. Scripts that previously relied on auto-naming will break. The error message is explicit, but it's still a behavior change.
- Existing peers DB cannot be migrated cleanly — sessions don't have agent names. Solution: drop the old `peers` table on startup of new code, let everything re-register.
- MCP protocol gains an ACK message type. Clients that don't implement ACK will fall through to poll-based delivery (still works, just slower).

**Neutral:**
- UCAN auth untouched.
- NATS fleet event bus untouched.
- Dashboard / CLI surface mostly unchanged (lists of sessions instead of lists of peers, same shape).

## Not in scope

- Fancy routing (topic subscriptions, broadcast, group messaging). YAGNI.
- LLM-generated agent names. Deterministic declaration is simpler and more predictable.
- Multi-session-per-agent (same agent name held by multiple sessions). Forbidden by R2. Can be added later if needed; adding it is easier than removing it.
