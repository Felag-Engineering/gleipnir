# Gleipnir Architecture

## Package dependency graph

Arrows represent import relationships. External systems are shown in rounded boxes.

```mermaid
graph TD
    subgraph ext["External systems"]
        HTTP(["HTTP clients\n(webhook callers)"])
        CLAUDE(["Claude API\n(Anthropic SDK)"])
        MCPEXT(["MCP servers\n(tool providers, HTTP)"])
    end

    subgraph pkgs["internal/"]
        direction TB

        MODEL["<b>model</b><br/>─────────────────────────────<br/>Domain enums: RunStatus · TriggerType<br/>StepType · ApprovalStatus<br/>+ 3 more, all with String() / Valid()<br/>─────────────────────────────<br/>Config structs: ParsedPolicy · AgentConfig<br/>CapabilitiesConfig · GrantedTool · ...<br/>─────────────────────────────<br/>Domain entities: Run · RunStep · Policy<br/>ApprovalRequest · MCPServer · MCPTool<br/>─────────────────────────────<br/>NewULID() — monotonic, goroutine-safe"]

        DB["<b>db</b><br/>─────────────────────────<br/>Store: opens SQLite, enables WAL<br/>mode and foreign keys on startup<br/>─────────────────────────<br/>Migrate(): idempotent schema<br/>migration on every startup<br/>─────────────────────────<br/>ScanOrphanedRuns(): resets any<br/>running/waiting runs to interrupted<br/>─────────────────────────<br/>Queries (sqlc-generated):<br/>all fields are plain string / int64"]

        POLICY["<b>policy</b><br/>───────────────────<br/>parser.go: YAML blob<br/>→ model.ParsedPolicy<br/>───────────────────<br/>validator.go: validates<br/>ParsedPolicy fields<br/>───────────────────<br/>renderer.go: renders<br/>Claude system prompt"]

        MCP["<b>mcp</b><br/>─────────────────────────────<br/>Client: calls one MCP server<br/>over HTTP — DiscoverTools,<br/>CallTool<br/>─────────────────────────────<br/>Registry: resolves policy<br/>capability references to live<br/>Client instances; fail-fast<br/>if any tool is missing<br/>─────────────────────────────<br/>⚠ must never import agent"]

        AGENT["<b>agent</b><br/>─────────────────────────────────<br/>BoundAgent: drives Claude API loop;<br/>registers only granted tools (hard<br/>capability enforcement); intercepts<br/>approval-gated actuators before<br/>calling them — hard runtime guarantee<br/>─────────────────────────────────<br/>AuditWriter: single background writer<br/>goroutine; serialises run_steps inserts<br/>through a buffered channel to avoid<br/>SQLite write contention; assigns<br/>sequential step_number per run"]

        TRIGGER["<b>trigger</b><br/>──────────────────────────────────<br/>WebhookHandler: validates request,<br/>applies concurrency policy, creates<br/>run record, launches BoundAgent<br/>goroutine, responds 202 Accepted<br/>──────────────────────────────────<br/>ManualTriggerHandler: operator-initiated<br/>run without a webhook payload<br/>──────────────────────────────────<br/>Scheduler: cron-triggered runs;<br/>RunLauncher: shared launch logic;<br/>RunManager: tracks active runs<br/>──────────────────────────────────<br/>cron.go: schedule-triggered runs<br/>poll.go: HTTP poll-triggered runs<br/>(stubs — v0.3)"]

        NOTIFY["<b>notify</b><br/>──────────────────────────<br/>Feedback channel: agent sends<br/>message, run suspends, operator<br/>responds via UI, run resumes<br/>──────────────────────────<br/>Approval notifications (v0.2)<br/>Slack integration (v0.5)<br/>──────────────────────────<br/>stub — not yet implemented"]
    end

    %% External → trigger
    HTTP -->|"POST /api/v1/webhooks/:policy_id"| TRIGGER

    %% trigger → internal
    TRIGGER -->|imports| DB
    TRIGGER -->|imports| POLICY
    TRIGGER -->|imports| MCP
    TRIGGER -->|imports| AGENT

    %% agent → external + internal
    AGENT -->|"Messages.Create"| CLAUDE
    AGENT -->|imports| MCP
    AGENT -->|imports| MODEL
    AGENT -->|imports| POLICY
    AGENT -->|imports| DB

    %% mcp → external + model
    MCP -->|"JSON-RPC HTTP"| MCPEXT
    MCP -->|imports| MODEL

    %% policy → model
    POLICY -->|imports| MODEL

    %% approval flow
    TRIGGER -->|"ApprovalDecision channel\n(operator response)"| AGENT
```

## Data flow: webhook-triggered run

```mermaid
sequenceDiagram
    actor Op as Operator
    participant T as trigger
    participant DB as db
    participant P as policy
    participant Reg as mcp.Registry
    participant A as agent.BoundAgent
    participant AW as agent.AuditWriter
    participant C as Claude API
    participant M as MCP server

    Op->>T: POST /api/v1/webhooks/:policy_id
    T->>DB: GetPolicy(policyID)
    T->>P: Parse(policy.YAML)
    T->>DB: CreateRun(status=pending)
    T->>Reg: ResolveForPolicy(parsedPolicy)
    Reg->>DB: look up mcp_servers + mcp_tools
    T->>A: go agent.Run(ctx, triggerPayload)
    T-->>Op: 202 Accepted {run_id}

    loop Claude API loop
        A->>C: Messages.Create (granted tools only)
        C-->>A: response (text / tool_use / end_turn)
        A->>AW: Write(ThoughtStep)

        alt tool_use — sensor or approved actuator
            A->>M: CallTool(name, input)
            M-->>A: ToolResult
            A->>AW: Write(ToolCallStep + ToolResultStep)
        else tool_use — actuator with approval:required
            A->>DB: UpdateRunStatus(waiting_for_approval)
            A->>AW: Write(ApprovalRequestStep)
            A->>Op: [notification via notify — v0.2]
            Op-->>A: ApprovalDecision (approved / rejected)
            alt approved
                A->>M: CallTool(name, input)
                M-->>A: ToolResult
                A->>DB: UpdateRunStatus(running)
            else rejected / timeout
                A->>DB: UpdateRunStatus(failed)
            end
        end
    end

    A->>DB: UpdateRunStatus(complete | failed)
    AW->>DB: flush remaining run_steps
```

## Implementation status

| Package | Status | Notes |
|---|---|---|
| `internal/model` | ✅ Complete | Enums, config structs, domain entities, `NewULID()` |
| `internal/db` | ✅ Complete | `Store`, `Migrate`, `ScanOrphanedRuns`, sqlc queries |
| `internal/policy` | ✅ Complete | Parser, validator, prompt renderer, model validator, service |
| `internal/mcp` | ✅ Complete | Client, Registry, schema narrowing, URL checker |
| `internal/agent` | ✅ Complete | BoundAgent runner, AuditWriter, RunStateMachine, approval interception |
| `internal/trigger` | ⚙ Partial | WebhookHandler, ManualTriggerHandler, Scheduler, RunLauncher, RunManager, SSE integration; cron/poll stubs remain |
| `internal/notify` | 📋 Planned | Empty package; v0.2 feedback channel, v0.5 Slack |

## Key invariants

- **`internal/model` imports nothing internal.** It is the shared vocabulary; circular imports here would collapse the whole dependency graph.
- **`internal/mcp` must never import `internal/agent`.** Enforced by the Go compiler the moment it happens.
- **`internal/db` types stay as plain strings.** sqlc generates them from SQLite TEXT columns. Conversion to typed model enums happens once in the caller (trigger/agent), never inside `db`.
- **Approval interception is a hard runtime guarantee.** `BoundAgent.handleToolCall` blocks on `approvalCh` before forwarding to the MCP server — it is not prompt-based and cannot be bypassed by the model.
- **Audit writes are serialized.** `AuditWriter` funnels all `run_steps` inserts through a single goroutine to avoid SQLite write contention under parallel runs.

## Stack overview

```
┌─────────────────────────────────────────────────────────┐
│  Docker Compose                                         │
│                                                         │
│  ┌──────────────────────────────────────────────────┐  │
│  │                  Go Binary                        │  │
│  │  chi · sqlc · Anthropic · go:embed (React UI)    │  │
│  │                       │                           │  │
│  │                  ┌────▼───┐                       │  │
│  │                  │ SQLite │                       │  │
│  │                  │  WAL   │                       │  │
│  │                  └────────┘                       │  │
│  └──────────────────────────────────────────────────┘  │
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

**Frontend:** React, embedded in the Go binary via `go:embed` and served directly by the chi router.

**Storage:** SQLite with WAL mode. Single file, zero ops, ships in the container.

**Tools:** All tools are MCP tools over HTTP transport. Gleipnir maintains its own capability metadata (tool approval gates, feedback channel) — this metadata lives in Gleipnir's DB, not in the MCP server. For stdio-only MCP servers, see the [Supergateway sidecar guide](../stdio-mcp-servers.md).
