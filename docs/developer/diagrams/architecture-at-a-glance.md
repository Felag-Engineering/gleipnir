# Architecture at a Glance

A single starting page for orienting yourself: which subsystems exist, who creates whom, and how a trigger gets to an agent. Pairs with the more detailed diagrams in this folder — start here, then drill in.

## Subsystem map

The codebase divides into six subsystems. Each box is one job stated in plain English.

```mermaid
graph TB
    subgraph FE["<b>Frontend</b> · React + Vite, embedded via go:embed"]
        FRONT["UI · talks to Go via REST + SSE"]
    end

    subgraph HTTP["<b>HTTP layer</b> · internal/http/*"]
        HTTPL["chi router · auth middleware · per-domain handlers · SSE"]
    end

    subgraph TRIG["<b>Trigger dispatch</b> · internal/trigger/"]
        TRIGL["webhook · manual · scheduled · poll · cron · pre-flight checks"]
    end

    subgraph EXE["<b>Execution</b> · internal/execution/*"]
        EXEL["RunLauncher (concurrency) → BoundAgent (LLM loop) + AuditWriter + RunStateMachine"]
    end

    subgraph INT["<b>Integrations</b> · internal/{llm,mcp,policy,arcade}"]
        INTL["LLM provider clients · MCP HTTP client · policy YAML parser · Arcade gateway helpers"]
    end

    subgraph PER["<b>Persistence + cross-cutting</b> · internal/{db,timeout,runstate,infra/*,admin}"]
        PERL["sqlc-generated queries · timeout scanners · run-state CAS table · config / logctx / metrics / event · encrypted secrets"]
    end

    FE --> HTTP
    HTTP --> TRIG
    HTTP --> EXE
    TRIG --> EXE
    EXE --> INT
    EXE --> PER
    INT --> PER
    HTTP --> PER
```

The single rule worth remembering: **dependencies always point downward**. The HTTP layer talks to triggers and execution; execution talks to integrations and persistence; persistence and `internal/infra/*` talk to nothing internal. `internal/mcp` is also forbidden from importing the agent package — see [package-dependencies.md](package-dependencies.md).

## Ownership tree — what `main.go` creates

`main.go` is the composition root. Every long-lived service is constructed here and wired explicitly. Read this top-down: a parent owns the lifetime and shutdown of its children.

```mermaid
graph TD
    MAIN["<b>main.go</b><br/>process lifetime"]

    %% Persistence + pub/sub
    MAIN --> STORE["<b>db.Store</b><br/>SQLite + sqlc · WAL"]
    MAIN --> BCAST["<b>SSE Broadcaster</b><br/>fan-out to browsers"]

    %% Background timeout scanners
    MAIN --> APPSCAN["<b>ApprovalScanner</b><br/>expires pending approvals"]
    MAIN --> FBSCAN["<b>FeedbackScanner</b><br/>expires pending feedback"]

    %% Provider + tool registries
    MAIN --> PROVREG["<b>llm.ProviderRegistry</b><br/>provider name → LLMClient"]
    MAIN --> ADMIN["<b>admin.Handler</b><br/>API key encryption,<br/>OpenAI-compat config"]
    MAIN --> MCPREG["<b>mcp.Registry</b><br/>MCP servers + tool catalog"]

    %% Run plumbing
    MAIN --> RM["<b>RunManager</b><br/>tracks live agent goroutines"]
    MAIN --> RL["<b>RunLauncher</b><br/>concurrency · DB-create-run · spawn"]
    RL -. uses .-> STORE
    RL -. uses .-> MCPREG
    RL -. uses .-> RM
    RL --> AF["<b>AgentFactory</b><br/>policy → BoundAgent"]
    AF -. uses .-> PROVREG

    %% Trigger handlers (one per trigger type)
    MAIN --> WH["<b>WebhookHandler</b>"]
    MAIN --> SCHED["<b>Scheduler</b><br/>fire_at one-shots"]
    MAIN --> POLLER["<b>Poller</b><br/>recurring polls"]
    MAIN --> CRON["<b>CronRunner</b><br/>cron expressions"]
    WH -. calls .-> RL
    SCHED -. calls .-> RL
    POLLER -. calls .-> RL
    CRON -. calls .-> RL

    %% Auth + HTTP surface
    MAIN --> AUTH["<b>auth.Handler</b><br/>login, sessions, roles"]
    MAIN --> POLSVC["<b>policy.Service</b><br/>YAML CRUD + webhook secrets"]
    MAIN --> ROUTER["<b>api.BuildRouter</b><br/>chi routes + middleware"]
    ROUTER -. routes to .-> WH
    ROUTER -. routes to .-> AUTH
    ROUTER -. routes to .-> ADMIN
    ROUTER -. routes to .-> POLSVC
    ROUTER -. routes to .-> RL

    %% Per-run goroutine
    RL ==>|spawns goroutine per run| BA
    subgraph perrun["Per-run goroutine (created on each Launch, dies when run completes)"]
        BA["<b>BoundAgent</b>"]
        BA --> AW["<b>AuditWriter</b><br/>serialized run_steps inserts"]
        BA --> SM["<b>RunStateMachine</b><br/>status transitions w/ CAS"]
        BA -. calls .-> LLMC["<b>LLMClient</b><br/>(borrowed from ProviderRegistry)"]
        BA -. calls tools via .-> MCPC["<b>MCP client</b><br/>(borrowed from mcp.Registry)"]
    end
    AW -. publishes .-> BCAST
    SM -. publishes .-> BCAST
```

Solid arrows = creates/owns. Dotted arrows = uses (borrowed reference, no ownership). The fat arrow into the per-run subgraph marks where short-lived goroutines fork off.

A few things worth calling out:

- **`main.go` owns everything long-lived.** The order in `run()` matters: persistence → broadcaster → registries → launcher → trigger handlers → router. Shutdown reverses this.
- **Trigger handlers are sinks for a single dependency: `RunLauncher`.** All five trigger types end at the same call site. Adding a sixth means another handler that calls `launcher.Launch(...)` — see [adding-a-trigger-type.md](../adding-a-trigger-type.md).
- **`BoundAgent` borrows, never owns, the LLM client and MCP clients.** Those live in registries with process-wide lifetime; the agent goroutine just holds a reference for the duration of one run.
- **`AuditWriter` is per-run by design.** Every step insert flows through one goroutine to avoid SQLite write contention (see invariants in [architecture.md](../architecture.md)).

## Trigger fan-in

Five trigger types, one launcher. This is the architectural keystone: regardless of how a run is initiated, the rest of the system sees it identically.

```mermaid
graph LR
    WHCALL["<b>HTTP POST</b><br/>/api/v1/webhooks/:id"] --> WH["WebhookHandler<br/>auth: hmac · bearer · none"]
    MANCALL["<b>HTTP POST</b><br/>/api/v1/policies/:id/trigger"] --> MAN["ManualHandler<br/>role: operator+"]
    TIMER1[("<b>Internal timer</b><br/>fire_at list")] --> SCHED["Scheduler<br/>one-shot, auto-pauses"]
    TIMER2[("<b>Internal timer</b><br/>cron expression")] --> CRON["CronRunner<br/>recurring, no catch-up"]
    POLLINT[("<b>Internal timer</b><br/>poll interval")] --> POLLER["Poller<br/>MCP call + JSONPath check"]

    WH --> RL["<b>RunLauncher.Launch</b>"]
    MAN --> RL
    SCHED --> RL
    CRON --> RL
    POLLER --> RL

    RL --> CHECK{"Concurrency<br/>policy"}
    CHECK -->|allow| CREATE["INSERT runs<br/>(status=pending)"]
    CHECK -->|skip / queue / replace| OUT1["return / enqueue / cancel-then-launch"]
    CREATE --> SPAWN["go BoundAgent.Run(...)"]
    SPAWN --> RM2["RunManager.Register"]
```

The funnel point — `RunLauncher.Launch` — is where the concurrency policy is enforced (`skip` / `queue` / `replace`), the run row is inserted, and the goroutine is spawned. From there, [run-execution-flow.md](run-execution-flow.md) takes over.

## Where to go next

| If you're trying to understand… | Read… |
|---|---|
| What happens during a run, step by step | [run-execution-flow.md](run-execution-flow.md) |
| What status a run can be in | [run-state-machine.md](run-state-machine.md) |
| How tools are restricted (and why prompt restrictions are not enough) | [capability-enforcement.md](capability-enforcement.md) |
| How real-time updates reach the browser | [realtime-events.md](realtime-events.md) |
| What's in the database | [data-model.md](data-model.md) |
| How requests flow through middleware | [auth-request-flow.md](auth-request-flow.md) |
| How shutdown is sequenced | [graceful-shutdown.md](graceful-shutdown.md) |
| Internal package boundaries and forbidden imports | [package-dependencies.md](package-dependencies.md) |
