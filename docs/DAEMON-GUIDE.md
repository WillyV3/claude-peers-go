# Daemon Guide

How to create, configure, and deploy autonomous Claude daemons on Sontara Lattice.

## What daemons are

A daemon is an autonomous Claude agent that runs on a schedule without human interaction. Each daemon has a goal, a policy, and access to tools. The supervisor manages their lifecycle -- scheduling, triage gating, invocation, error handling, and output capture.

Daemons run via the [vinayprograms/agent](https://github.com/vinayprograms/agent) binary, which provides the Agentfile DSL, LLM provider abstraction, and policy enforcement.

## Daemon directory structure

```
my-daemon/
  my-daemon.agent    # Agentfile: prompt, goals, agent definition
  daemon.json        # Schedule and metadata
  agent.toml         # LLM provider config
  policy.toml        # Tool allowlists, path restrictions, safety constraints
  triage.sh          # Optional: gate script (exit 0 = run, exit 1 = skip)
```

The supervisor discovers daemons from these paths (in order):
1. `cfg.DaemonDir` (from config.json)
2. `{repo-root}/daemons/`
3. `~/.config/claude-peers/daemons/`
4. `~/claude-peers-daemons/`

## daemon.json

```json
{
  "schedule": "interval:30m",
  "description": "What this daemon does in one line"
}
```

Schedule types:
| Format | Behavior |
|--------|----------|
| `interval:15m` | Runs every 15 minutes |
| `interval:6h` | Runs every 6 hours |
| `event:fleet.>` | Runs when any fleet NATS event arrives |
| `event:fleet.security.fim` | Runs only on file integrity events |

## Agentfile (.agent)

The Agentfile DSL defines the daemon's identity, inputs, agents, and goals:

```
NAME my-daemon

INPUT broker_url DEFAULT "http://100.109.211.128:7899"
INPUT report_to DEFAULT "you@example.com"

AGENT worker """You are a maintenance daemon. Your job is to...

Rules:
- Never delete files
- Be concise in output
- If unsure, skip and report"""

GOAL main_task """Do the thing.

Steps:
1. Check condition
2. Take action
3. Report results"""

RUN main USING main_task
```

Key elements:
- `NAME`: Daemon identifier (matches directory name)
- `INPUT`: Variables with defaults (accessible as `$var_name` in goals)
- `AGENT`: Agent personality and rules
- `GOAL`: Task definition with step-by-step instructions
- `RUN`: Entry point

## agent.toml

LLM provider configuration:

```toml
[llm]
provider = "openai-compat"
model = "claude-sonnet"
max_tokens = 16384
base_url = "http://your-llm-proxy:4000/v1"
api_key_env = "LITELLM_API_KEY"

[small_llm]
provider = "openai-compat"
model = "claude-haiku"
max_tokens = 4096
base_url = "http://your-llm-proxy:4000/v1"
api_key_env = "LITELLM_API_KEY"
```

The `small_llm` is used for summarization and triage decisions. The main `llm` handles the actual daemon work.

`max_tokens` is required by the agent binary -- it's the LLM response size limit per call, not a total limit on daemon output.

## policy.toml

Safety constraints enforced by the agent binary:

```toml
[tools]
allow = ["bash", "read", "write", "web_fetch"]
deny = ["rm -rf", "git push --force", "shutdown", "reboot"]

[policy.constraints]
max_file_deletes = 0
max_file_renames = 0
allowed_paths = ["~/projects/", "~/hfl-projects/"]
forbidden_paths = ["~/.ssh/", "~/.config/claude-peers/config.json"]
```

If you want a read-only daemon:
```toml
[agent.permissions]
allow_bash = true
allow_read = true
allow_write = false
allow_edit = false
```

## triage.sh

Optional gate script. Runs before the daemon to check if there's work to do:

```bash
#!/bin/bash
# Only run if there are open PRs
count=$(gh pr list --repo myorg/myrepo --state open --json number -q 'length' 2>/dev/null)
[ "${count:-0}" -gt 0 ] && echo "$count open PRs" && exit 0
exit 1
```

- Exit 0: daemon runs
- Exit 1: daemon skipped (logged as "triage skip")

Without a triage script, the daemon always runs on schedule.

## Supervisor behavior

- **Single instance**: Only one invocation per daemon at a time
- **Failure cooldown**: 5-minute backoff after a failure before retry
- **Startup jitter**: 5-30 seconds random delay before first run (prevents thundering herd)
- **History**: Last 100 runs kept per daemon
- **Output capture**: Full stdout/stderr captured, summarized in NATS events
- **NATS events**: Each run publishes to `fleet.daemon.<name>` with status, duration, trigger

## Built-in daemons

| Daemon | Schedule | Purpose |
|--------|----------|---------|
| fleet-scout | 15m | Check health of all machines and services, report anomalies |
| fleet-memory | event:fleet.> | Consolidate fleet activity into shared Claude memory |
| pr-helper | 30m | Keep PRs mergeable across GitHub orgs (human-frontier-lab, williavs, WillyV3) |
| llm-watchdog | 15m | Monitor LLM server health, alert if down |
| sync-janitor | 30m | Detect Syncthing conflict files, email analysis report |
| librarian | 6h | Audit documentation against live fleet state across all machines |

## Monitoring daemons

Gridwatch page 4 (AGENTS) shows:
- Per-daemon status (idle, running, complete, failed, triage)
- Last run time and duration
- Run history sparkline (last 8 runs)
- Runs per hour rate
- Output text (cleaned and truncated)
- Success/failure counts
