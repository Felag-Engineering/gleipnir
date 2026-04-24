# Package Dependency Graph

Packages are grouped by layer. Arrows show the primary architectural relationships — not every import. Packages in lower layers (`model`, `db`, `infra/config`, `infra/logctx`, `httputil`) are widely imported and omitted from the arrows to reduce noise.

```mermaid
graph TD
    subgraph entrypoints["HTTP layer"]
        direction LR
        API["<b>api</b><br/>Router, middleware"]
        AUTH["<b>auth</b><br/>Sessions, roles"]
        ADMIN["<b>admin</b><br/>Provider keys"]
    end

    subgraph triggers["Trigger layer"]
        direction LR
        TRIGGER["<b>trigger</b><br/>Webhook, manual,<br/>scheduled, poll"]
        RUN["<b>run</b><br/>RunLauncher, RunManager,<br/>concurrency"]
    end

    subgraph orchestration["Agent layer"]
        direction LR
        AGENT["<b>agent</b><br/>BoundAgent, AuditWriter,<br/>approval + feedback handlers"]
        APPROVAL["<b>approval</b> · <b>feedback</b><br/>Timeout wiring"]
    end

    subgraph domain["Domain + integration layer"]
        direction LR
        POLICY["<b>policy</b><br/>YAML parser, validator,<br/>prompt renderer"]
        MCP["<b>mcp</b><br/>HTTP client,<br/>tool registry"]
        LLM["<b>llm</b><br/>Provider interface<br/><i>anthropic · google<br/>openai · openaicompat</i>"]
    end

    subgraph infra["Persistence + pub/sub layer"]
        direction LR
        DB["<b>db</b><br/>SQLite, sqlc queries"]
        SSE["<b>sse</b><br/>Broadcaster"]
        RUNSTATE["<b>runstate</b><br/>Transition table"]
        TIMEOUT["<b>timeout</b><br/>Scan-and-resolve"]
    end

    subgraph leaf["Leaf packages (internal/infra/ + shared types) — imported by nearly everything above"]
        direction LR
        MODEL["<b>model</b>"]
        CONFIG["<b>infra/config</b>"]
        LOGCTX["<b>infra/logctx</b>"]
        EVENT["<b>infra/event</b><br/>Pub/sub"]
        METRICS["<b>infra/metrics</b><br/>Prometheus registry"]
        HTTPUTIL["<b>httputil</b>"]
    end

    %% Primary relationships (top to bottom)
    API --> AUTH
    API --> TRIGGER
    API --> RUN

    TRIGGER --> RUN
    RUN --> AGENT

    AGENT --> LLM
    AGENT --> MCP
    AGENT --> POLICY
    APPROVAL --> TIMEOUT

    MCP -.->|"must never import"| AGENT
```
