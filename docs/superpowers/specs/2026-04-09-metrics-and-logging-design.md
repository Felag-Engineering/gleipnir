# Metrics & Logging Design

**Date:** 2026-04-09
**Status:** Proposed
**Scope:** Operational observability for Gleipnir administrators — system health, diagnostics, and resource monitoring. Not agent behavior analytics.

## Context

Gleipnir has structured JSON logging via `slog` and on-demand stats computed from DB aggregations, but no real-time system metrics, no request correlation in logs, and chi's default Logger middleware produces unstructured access logs. The admin persona is a small team with an existing Prometheus + Loki + Grafana stack.

## Decisions

- **Prometheus `/metrics` endpoint** using `prometheus/client_golang`. Pull model, standard scrape.
- **Hybrid metrics architecture** — thin `internal/metrics` package provides the registry, naming conventions, and helpers. Domain packages own their collectors.
- **Structured JSON logging to stdout only** — enriched with correlation context. Docker/Loki handles collection. No files, no in-app log storage.
- **DB instrumentation: hot-path queries only** — wrap the 5-6 most important sqlc queries, not all of them. Expand later as needed.

## Architecture

### New package: `internal/metrics`

A thin coordination layer (~100 lines) providing:

- Custom Prometheus `Registry` (not the global default — isolates Gleipnir metrics, controls Go runtime opt-in, cleaner testing)
- Naming convention: all metrics use the `gleipnir_` prefix
- Histogram bucket presets:
  - Fast operations (MCP, DB): 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10
  - Slow operations (LLM, runs): 0.1, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600
- Label constant definitions for reuse across packages
- `Handler()` function returning the `promhttp.Handler` for the registry

### Per-package instrumentation

Each domain package registers its own collectors using `promauto` with the shared registry. Metrics live next to the code they measure.

### Middleware additions

Three new middleware functions in `internal/api`, replacing chi's `Logger`:

**Chain order:** RequestID → RealIP → `slogContext` → `httpMetrics` → `slogAccess` → Recoverer → Handler

- **`slogContext`** — reads request ID and remote IP from context, creates `slog.With()` logger, stores in request context
- **`httpMetrics`** — wraps `http.ResponseWriter` to capture status code and duration, records to Prometheus histogram/counter
- **`slogAccess`** — emits structured JSON access log on response completion (replaces chi.Logger)

## Metrics Catalog

### Priority 1: Run Engine (`internal/agent`, `internal/runstate`)

| Metric | Type | Labels | Purpose |
|--------|------|--------|---------|
| `gleipnir_runs_active` | Gauge | state | In-flight runs by state (running, waiting_for_approval, waiting_for_feedback) |
| `gleipnir_runs_total` | Counter | trigger_type, status | Cumulative runs by trigger and terminal status |
| `gleipnir_run_duration_seconds` | Histogram | trigger_type, status | End-to-end run duration (slow bucket preset) |
| `gleipnir_run_state_transitions_total` | Counter | from, to | State machine transition counts |
| `gleipnir_run_steps_total` | Counter | step_type | Step volume by type (thought, tool_call, error, etc.) |
| `gleipnir_audit_queue_depth` | Gauge | — | Backpressure indicator for SQLite audit writer |
| `gleipnir_approval_timeouts_total` | Counter | — | Expired approvals |

### Priority 2: Downstream Dependencies (`internal/mcp`, `internal/llm`)

| Metric | Type | Labels | Purpose |
|--------|------|--------|---------|
| `gleipnir_mcp_call_duration_seconds` | Histogram | server, tool | MCP tool invocation latency (fast bucket preset) |
| `gleipnir_mcp_errors_total` | Counter | server, error_type | MCP failures by server and error class |
| `gleipnir_llm_request_duration_seconds` | Histogram | provider, model | LLM API round-trip time (slow bucket preset) |
| `gleipnir_llm_errors_total` | Counter | provider, error_type | LLM failures by provider and error class |
| `gleipnir_llm_tokens_total` | Counter | provider, model, direction | Token throughput (input/output) |

### Priority 3: Runtime & Resources (`internal/metrics`, `internal/sse`)

| Metric | Type | Labels | Purpose |
|--------|------|--------|---------|
| `go_*` (runtime collector) | Various | — | Goroutines, memory, GC (opt-in via custom registry) |
| `gleipnir_sse_connections_active` | Gauge | — | Current SSE subscriber count |
| `gleipnir_db_query_duration_seconds` | Histogram | query | SQLite query performance (fast bucket preset, hot-path queries only) |

### Priority 4: HTTP Layer (`internal/api`)

| Metric | Type | Labels | Purpose |
|--------|------|--------|---------|
| `gleipnir_http_request_duration_seconds` | Histogram | method, route, code | Latency distribution per endpoint (fast bucket preset) |
| `gleipnir_http_requests_total` | Counter | method, route, code | Request volume and error rates |

**Total: 16 metric families across 4 packages.**

### Label conventions

- No unbounded labels (`policy_id`, `run_id`) — these belong in logs, not metrics
- `error_type` uses a fixed enum: `timeout`, `connection`, `rate_limit`, `auth`, `server_error`, `protocol`
- `state`, `status`, `step_type`, `trigger_type` use existing `model` package enums
- `direction` for tokens: `input`, `output`
- `query` for DB: sqlc method name (e.g., `GetRun`, `ListPolicies`)

## Structured Logging

### Replace chi.Logger

Current unstructured output:
```
"GET /api/v1/runs HTTP/1.1" from 192.168.1.5 - 200 34.2ms
```

Replaced with structured JSON:
```json
{
  "time": "2026-04-09T14:32:01Z",
  "level": "INFO",
  "msg": "http request",
  "method": "GET",
  "path": "/api/v1/runs",
  "status": 200,
  "duration_ms": 34.2,
  "request_id": "abc123",
  "remote_addr": "192.168.1.5"
}
```

### Request-scoped correlation

The `slogContext` middleware injects a logger into request context with baseline fields (`request_id`, `remote_addr`). When a request enters the run engine, the logger is enriched with `run_id` and `policy_id`. All downstream code using `slog.InfoContext(ctx, ...)` automatically includes these fields.

### What doesn't change

- Output target: stdout only (Docker handles collection)
- Format: JSON (already the case)
- Log level config: `GLEIPNIR_LOG_LEVEL` env var (already exists)
- No per-package log levels

## `/metrics` Endpoint

- Mounted in `main.go` on the same port (8080), no auth middleware
- Docker network provides isolation; a separate `GLEIPNIR_METRICS_LISTEN_ADDR` can be added later if needed
- Handler serves the custom registry only (not the default global registry)

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `GLEIPNIR_METRICS_ENABLED` | `true` | Kill switch for `/metrics` endpoint |
| `GLEIPNIR_METRICS_PATH` | `/metrics` | Endpoint path |

No changes to existing configuration. `GLEIPNIR_LOG_LEVEL` continues to work as-is.

## DB Query Instrumentation

A thin `InstrumentedQueries` wrapper around sqlc's generated `Queries` struct. Only hot-path queries are wrapped initially:

- `CreateRun` / `GetRun` — run lifecycle
- `InsertRunStep` — audit write path (highest volume)
- `ListPolicies` / `GetPolicy` — policy resolution
- `GetApprovalRequest` — approval flow

Each wrapper method starts a timer, calls the inner method, observes duration with a `query` label, and returns the result. New queries are added to the wrapper only when there's a diagnostic need.

## Instrumentation Touch-Points

| Domain | Where | What's recorded |
|--------|-------|-----------------|
| Run lifecycle | `BoundAgent.Run()` start/end | runs_active gauge, run_duration histogram, runs_total counter |
| State transitions | `runstate.Transition()` | run_state_transitions_total counter |
| Audit writes | `AuditWriter.enqueue()` | audit_queue_depth gauge, run_steps_total counter |
| MCP calls | `mcp.Client.CallTool()` | mcp_call_duration histogram, mcp_errors_total counter |
| LLM calls | `llm.Provider.Chat()` | llm_request_duration histogram, llm_errors_total counter, llm_tokens_total counter |
| SSE connections | `sse.Handler` subscribe/unsubscribe | sse_connections_active gauge |
| DB queries | `InstrumentedQueries` wrapper | db_query_duration histogram |

## Performance Impact

- Metric operations (Inc, Observe): ~50-100ns each (lock-free atomics)
- slog context injection: one small allocation per request (comparable to existing RequestID middleware)
- DB wrapper: ~200ns overhead per query (time.Now + histogram observe)
- `/metrics` scrape: sub-millisecond for 16 metric families
- Memory: ~5KB for all time series combined

## Testing Strategy

- `internal/metrics`: test that registry creates collectors without panics, handler returns valid Prometheus text format
- Middleware: table-driven tests with `httptest.ResponseRecorder` — verify log output contains expected fields, verify metrics are recorded with correct labels
- Per-package collectors: test in existing package test suites — verify counters increment and histograms observe on the expected code paths
- DB wrapper: test that wrapper delegates correctly and records timing

## Out of Scope

- In-app metrics dashboard (Grafana handles this)
- Agent behavior analytics (separate product feature)
- Distributed tracing / OpenTelemetry
- Per-package log levels
- Log aggregation or in-app log viewer
- Alerting rules (configured in Grafana, not in Gleipnir)
- Grafana dashboard JSON provisioning (can be added as a follow-up)
