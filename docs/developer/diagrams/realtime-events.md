# Real-time Event Flow

SSE delivers live updates from the backend to all connected browser clients.

```mermaid
graph LR
    subgraph producers["Event producers"]
        AW["AuditWriter<br/><i>run.step_added</i>"]
        SM["RunStateMachine<br/><i>run.status_changed ·<br/>approval.created · feedback.created</i>"]
        SCAN["Timeout scanners<br/><i>approval.resolved ·<br/>feedback.resolved</i>"]
        PLG["Plugin runtime<br/><i>plugin.health_changed ·<br/>plugin.event_matched · …</i>"]
    end

    BROADCASTER["<b>SSE Broadcaster</b><br/>Replay ring buffer (2048 slots)<br/>Fan-out to all subscribers"]

    subgraph consumers["Browser clients"]
        B1["Browser 1<br/><i>useSSE hook</i>"]
        B2["Browser 2<br/><i>useSSE hook</i>"]
        BN["Browser N<br/><i>useSSE hook</i>"]
    end

    AW --> BROADCASTER
    SM --> BROADCASTER
    SCAN --> BROADCASTER
    PLG --> BROADCASTER

    BROADCASTER -->|"GET /api/v1/events<br/>text/event-stream"| B1
    BROADCASTER --> B2
    BROADCASTER --> BN

    RECONNECT["Last-Event-ID<br/>replay on reconnect"] -.-> BROADCASTER
```
