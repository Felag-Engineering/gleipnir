# Runtime Object Graph

How the major components are wired together at runtime, from process start through a run completing. Follow the arrows to trace a trigger from HTTP request to agent completion.

```mermaid
graph TD
    MAIN["<b>main.go</b><br/>Bootstrap + wiring"]

    MAIN -->|creates| ROUTER["<b>api.BuildRouter</b><br/>HTTP endpoints"]
    MAIN -->|creates| RL["<b>RunLauncher</b><br/>Concurrency + launch"]
    MAIN -->|creates| RM["<b>RunManager</b><br/>Tracks active runs"]
    MAIN -->|creates| SCHED["<b>Scheduler</b><br/>Scheduled triggers"]
    MAIN -->|creates| POLLER["<b>Poller</b><br/>Poll triggers"]
    MAIN -->|creates| BCAST["<b>SSE Broadcaster</b>"]

    ROUTER -->|routes to| WH["<b>WebhookHandler</b>"]
    ROUTER -->|routes to| MAN["<b>ManualHandler</b>"]

    WH -->|calls| RL
    MAN -->|calls| RL
    SCHED -->|calls| RL
    POLLER -->|calls| RL

    RL -->|registers run in| RM
    RL -->|launches goroutine| BA

    subgraph goroutine["Agent goroutine (one per run)"]
        BA["<b>BoundAgent</b>"]
        BA -->|owns| AW["<b>AuditWriter</b><br/>Serialized step inserts"]
        BA -->|owns| SM["<b>RunStateMachine</b><br/>Status transitions"]
        BA -->|calls| LLM["<b>LLMClient</b><br/><i>Anthropic / Google / OpenAI</i>"]
        BA -->|calls tools via| REG["<b>MCP Registry</b><br/>Resolves tools to MCP clients"]
        REG -->|"JSON-RPC"| MCPS(["MCP servers"])
    end

    AW -->|publishes| BCAST
    SM -->|publishes| BCAST
    BA -->|deregisters on exit| RM
```
