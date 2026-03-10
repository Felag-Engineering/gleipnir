# Gleipnir

Gleipnir is a homelab-scale autonomous agent orchestrator. It runs AI agents as **BoundAgents** — agents with hard capability enforcement, a full audit trail, and first-class human-in-the-loop controls.

Named after the Norse mythological binding that held Fenrir: smooth as silk, stronger than iron, invisible in its constraint.

---

## What it does

Gleipnir lets you define **policies** — YAML configurations that describe what an agent is allowed to do, what triggers it, and what constraints apply. When a trigger fires, Gleipnir launches a BoundAgent that can only use the tools you explicitly granted it.

Agents operate with three categories of tools:

- **Sensors** — read-only tools for observing the world. Called freely.
- **Actuators** — world-affecting tools. Can be approval-gated.
- **Feedback** — a channel to consult a human operator when uncertain.

Capability enforcement is hard: tools not granted to an agent for a run are never registered with it. They don't exist from the agent's perspective. Prompt-based restrictions are not used.

Every agent run produces a full reasoning trace — thoughts, tool calls, tool results, approval requests — stored and visible in the UI.

---

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│  Docker Compose                                         │
│                                                         │
│  ┌──────────────┐        ┌──────────────────────────┐  │
│  │   React UI   │        │      Go API Server        │  │
│  │   (nginx)    │◄──────►│  chi · sqlc · Anthropic  │  │
│  └──────────────┘  /api  └────────────┬─────────────┘  │
│                           proxy        │                 │
│                                   ┌───▼────┐            │
│                                   │ SQLite │            │
│                                   │  WAL   │            │
│                                   └────────┘            │
└─────────────────────────────────────────────────────────┘
                              │
                    MCP HTTP transport
                              │
              ┌───────────────┼───────────────┐
              ▼               ▼               ▼
         MCP Server      MCP Server      MCP Server
        (Vikunja)       (Grafana)       (kubectl)
```

**Backend:** Go, [chi](https://github.com/go-chi/chi) router, [sqlc](https://sqlc.dev/) for type-safe queries, official [Anthropic Go SDK](https://github.com/anthropics/anthropic-sdk-go).

**Frontend:** React, served via nginx, proxies `/api` to the Go container.

**Storage:** SQLite with WAL mode. Single file, zero ops, ships in the container.

**Tools:** All tools are MCP tools over HTTP transport. Gleipnir maintains its own capability tag registry (sensor/actuator/feedback) — this metadata lives in Gleipnir's DB, not in the MCP server.

---

## Getting started

### Prerequisites

- Docker and Docker Compose
- An Anthropic API key
- At least one MCP server reachable over HTTP

### Setup

1. Clone the repository and copy the environment template:

```bash
git clone https://github.com/your-org/gleipnir.git
cd gleipnir
cp .env.example .env
```

2. Edit `.env` and set your Anthropic API key and any MCP server configuration.

3. Start Gleipnir:

```bash
docker compose up -d
```

4. Open the UI at `http://localhost:3000`.

### First run

1. **Register an MCP server** — go to Settings → MCP Servers, add the URL of your MCP server. Gleipnir will discover its available tools.

2. **Tag capabilities** — assign each discovered tool a role: `sensor`, `actuator`, or `feedback`.

3. **Create a policy** — go to Policies → New, write your policy YAML. The policy defines the trigger, which tools the agent can use, and what the agent should do.

4. **Trigger a run** — for a webhook policy, POST to `/api/v1/webhooks/:policy_id`. The request body becomes the agent's trigger payload.

5. **Watch the trace** — open the run in the UI to see the full reasoning timeline as the agent works.

---

## Policy schema

Policies are YAML. A minimal webhook-triggered policy:

```yaml
name: vikunja-triage
description: Triage newly opened DevOps tasks.

trigger:
  type: webhook

capabilities:
  sensors:
    - tool: vikunja.task_get
    - tool: grafana.get_alerts
    - tool: kubectl.get_pods
  actuators:
    - tool: vikunja.task_comment
    - tool: vikunja.task_close
      approval: required
      timeout: 30m
      on_timeout: reject

agent:
  task: |
    A new task has been opened. Triage it:
    1. Read the task from the trigger payload.
    2. Check Grafana for related alerts.
    3. Check pod health if a service is mentioned.
    4. Post a comment with your findings and recommended priority.
    5. Close if clearly duplicate — but this requires approval.
  limits:
    max_tokens_per_run: 20000
    max_tool_calls_per_run: 50
  concurrency: skip
```

See [`policy-schema.yaml`](docs/policy-schema.yaml) for the full schema reference including cron and poll triggers.

---

## Trigger types

| Type | Description |
|---|---|
| `webhook` | HTTP POST to `/api/v1/webhooks/:policy_id`. Request body is the trigger payload. |
| `cron` | Standard 5-field cron expression. Runs on schedule. |
| `poll` | Calls an HTTP endpoint on an interval, evaluates a JSONPath filter, fires a run if matched. |

---

## Human-in-the-loop

Gleipnir supports two approval modes simultaneously:

**Agent-initiated** — the agent voluntarily uses the feedback tool when uncertain. Encouraged via the system prompt. The agent sends a message, the operator responds via the UI, and the agent continues with the response as a tool result.

**Policy-gated** — actuators marked `approval: required` in the policy are intercepted by the runtime before execution. The run suspends, an approval request appears in the UI, and the run resumes or fails based on the operator's decision. This is a hard guarantee — it applies regardless of the agent's reasoning.

Approval requests show the tool name, proposed input, and a summary of the agent's reasoning up to the pause point.

---

## Run states

```
pending → running → complete
                 → failed
                 → waiting_for_approval → running (approved)
                                       → failed (rejected / timeout)
interrupted  (run was active when Gleipnir restarted)
```

In-flight runs do not survive a Gleipnir restart. On startup, any run in `running` or `waiting_for_approval` is marked `interrupted` with the last known step preserved.

---

## Security

Read [`SECURITY.md`](SECURITY.md) before deploying. Key points:

- **MCP servers are fully trusted.** A compromised MCP server has full control over every tool it implements — it can silently alter tool behavior, fabricate results, and poison discovery with new tool names that operators might accidentally grant later. It cannot inject tools the policy did not grant, but the tools it does own are fully under its control. Treat MCP server containers as part of your trust boundary.
- **Webhook endpoints have no signature verification in v0.1.** The webhook URL is a secret — treat it as a credential. HMAC verification is planned for v0.4.
- **Prompt injection via tool results is a known risk.** MCP tool results enter the agent's context window. Structured result wrapping is a planned mitigation, not yet implemented.

---

## Operations

### Backing up the database

The SQLite database lives at the path set by `GLEIPNIR_DB_PATH` (default: `/data/gleipnir.db`) inside the `gleipnir_data` Docker volume.

WAL mode means the database is spread across up to three files at any moment: the main `.db` file, a `.db-wal` write-ahead log, and a `.db-shm` shared-memory index. A raw file copy taken while the stack is live may capture these files in an inconsistent state, producing a corrupt backup.

**Safe offline backup** (always consistent):

```bash
docker compose stop
# Copy the database file out of the volume while the stack is stopped.
# Adjust the destination path to suit your backup strategy.
docker run --rm \
  -v gleipnir_data:/data \
  -v "$(pwd)":/backup \
  alpine cp /data/gleipnir.db /backup/gleipnir.backup.db
docker compose up -d
```

**Online backup** (no downtime, SQLite handles consistency):

```bash
docker compose exec api sqlite3 /data/gleipnir.db ".backup /data/gleipnir.backup.db"
```

The `.backup` command uses SQLite's built-in online backup API, which is safe to run against a live database. Copy `/data/gleipnir.backup.db` out of the volume once the command completes.

### Viewing structured logs

Stream live logs from the API container:

```bash
docker compose logs -f api
```

Logs are emitted as JSON by `slog.NewJSONHandler`. Pipe through `jq` for readable output:

```bash
docker compose logs api | jq .
```

Key fields in every log line:

| Field | Description |
|---|---|
| `time` | RFC 3339 timestamp |
| `level` | `DEBUG`, `INFO`, `WARN`, or `ERROR` |
| `msg` | Human-readable event description |
| `run_id` | Present on all log lines tied to a specific run |
| `err` | Error string, present only on `WARN`/`ERROR` lines |

Filter to a single run:

```bash
docker compose logs api | jq 'select(.run_id == "<run_id>")'
```

### Resetting a stuck run

On restart, Gleipnir automatically marks any run in `running` or `waiting_for_approval` as `interrupted`. This handles the common case of a clean restart after a crash or deployment.

If a run is genuinely stuck — for example, after a manual DB edit left it in an inconsistent state — it can be reset directly with a SQL update:

```bash
docker compose exec api sqlite3 /data/gleipnir.db \
  "UPDATE runs SET status = 'failed', error = 'manually reset' WHERE id = '<run_id>';"
```

**Warning:** This bypasses the normal state machine entirely. The run will be recorded as `failed` with no additional audit steps written. Only use this for runs that are truly stuck and will not recover on their own. Always verify the run ID before executing — there is no confirmation prompt.

---

## Roadmap

Gleipnir is in active development. The current phase is v0.1 MVP.

| Phase | Goal |
|---|---|
| v0.1 | Webhook trigger, BoundAgent runner, full reasoning trace, basic UI |
| v0.2 | Approval gates and human-in-the-loop, agent-initiated feedback |
| v0.3 | Cron and poll triggers, concurrency policy |
| v0.4 | Hardening: health checks, HMAC verification, drift detection, basic auth |
| v0.5 | Slack integration: approval messages, threaded notifications |

See [`ROADMAP.md`](ROADMAP.md) for open design questions and the full decision log.

---

## License

[MIT](LICENSE)