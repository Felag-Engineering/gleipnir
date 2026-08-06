# Testing Patterns

Gleipnir's test suite covers the backend (Go, `go test`) and frontend (TypeScript, Vitest). This guide covers the shared test helpers, mock infrastructure, and common patterns.

## Backend test helpers (`internal/testutil/`)

### Database

`NewTestStore(tb)` creates a temporary SQLite database with all migrations applied. Cleanup is automatic via `t.Cleanup()`. Use this for any test that touches the database.

Insert helpers set up test state without going through the full application layer:

| Helper | What it inserts |
|--------|----------------|
| `InsertPolicy()` | Policy row with ID, name, trigger type, YAML |
| `InsertRun()` | Run with specific status |
| `InsertRunWithTime()` | Run with custom timestamps and token costs |
| `InsertRunStep()` | Audit trail step |
| `InsertApprovalRequest()` | Approval row with expiration |
| `InsertQueueEntry()` | Trigger queue entry |
| `InsertMcpServer()` | MCP server definition |

### LLM client mocks

| Constructor | Behavior | When to use |
|------------|----------|-------------|
| `NewFakeClient(responses...)` | Drives scripted responses through the **real** `ProviderAdapter` via `FakeWire` (ADR-022) | Agent/trigger tests that should exercise the adapter choreography (metrics, error wrapping), not bypass it |
| `NewFakeClientOnly(responses...)` | Same as `NewFakeClient` but returns only the `LLMClient` | When you don't need a handle to the underlying `FakeWire` |
| `NewMockLLMClient(responses...)` | Returns pre-canned responses in order (bypasses the adapter) | Agent tests with scripted LLM behavior |
| `NewNoopLLMClient()` | Panics if `CreateMessage` is called | Tests that construct a BoundAgent but never trigger an API call |
| `NewBlockingLLMClient()` | Blocks until context is cancelled | Cancellation and timeout testing |
| `NewErrorLLMClient(err)` | Always returns the given error | Error path testing |

Response builders for `NewMockLLMClient`:

| Builder | Shorthand for |
|---------|---------------|
| `MakeTextResponse(text)` | Text response with default token counts |
| `MakeToolCallResponse(name, id, input)` | Single tool call with defaults |
| `MakeMultiToolCallResponse(calls)` | Multiple tool calls in one response |
| `MakeLLMTextResponse(text, stop, in, out)` | Text with explicit token counts |
| `MakeLLMToolCallResponse(id, name, input, in, out)` | Tool call with explicit token counts |

After running, call `client.Requests()` to assert on what was sent.

### Mock Anthropic HTTP server

`NewMockAnthropicServer(t, responses...)` starts a real HTTP test server that mimics the Anthropic Messages API. It supports both JSON and SSE (streaming) response modes.

```go
srv := testutil.NewMockAnthropicServer(t,
    testutil.MockTextResponse("thinking..."),
    testutil.MockToolUseResponse("tu-1", "my-tool", map[string]any{"key": "val"}),
    testutil.MockTextResponse("done"),
)
client := srv.Client(t) // Returns a configured AnthropicClient
```

Use `srv.Requests()` after the test to verify request bodies. Use `MockErrorResp(status, type, msg)` to test error handling (401, 429, 500).

### Event recording

`testutil.RecordingPublisher` captures published events for assertion. It exposes `EventsByType(eventType)` to filter to a specific event:

```go
pub := &testutil.RecordingPublisher{}
// ... pass pub to components that publish events ...
events := pub.EventsByType("run.status_changed")
```

The agent package has its own in-package `capturePublisher` (see `internal/execution/agent/state_test.go`) that adds a `waitForEvent(t, eventType, timeout)` helper — the canonical "signal-don't-poll" primitive for blocking on an asynchronous side effect (see [Signal, don't poll](#signal-dont-poll) below).

### Reusable policy YAML

`MinimalWebhookPolicy` provides a valid policy YAML string for tests that need one but don't care about the specifics.

## Common patterns

### Table-driven tests

Use for anything with multiple input/output combinations:

```go
cases := []struct {
    name string
    input  string
    want   bool
}{
    {"valid input", "good", true},
    {"empty input", "", false},
}
for _, tc := range cases {
    t.Run(tc.name, func(t *testing.T) {
        got := Validate(tc.input)
        if got != tc.want {
            t.Errorf("Validate(%q) = %v, want %v", tc.input, got, tc.want)
        }
    })
}
```

### BoundAgent integration tests

Full agent loop tests follow this pattern:

1. Create a test store and insert a policy + pending run
2. Construct a `MockLLMClient` with the scripted response sequence
3. Create an `AuditWriter` and `RunStateMachine`
4. Build a `BoundAgent` via `agent.New(Config{...})`
5. Call `ba.Run(ctx, runID, payload)`
6. Close the AuditWriter (flushes buffered writes)
7. Assert on: audit trail steps, final run status, published events

See `internal/execution/agent/agent_test.go` for examples.

### Injecting time

Anything whose behavior depends on `time.Now()` — rate limiters, timeouts, scheduled jobs, audit-coalescing windows — must route through an injectable package-level clock (convention: `var timeNow = func() time.Time { return time.Now() }`), and tests swap it via `t.Cleanup` rather than relying on wall-clock timing.

Rules:

- Production hot paths that tests assert against never call `time.Now()` directly — wrap in `timeNow()`.
- Tests that mutate a shared package-level clock variable must **not** use `t.Parallel()`.
- When `t.Parallel()` is required, use an external test package with a `SetTimeNowForTest(fn) (restore func())` export hook.
- Advancing a fake clock also refills `rate.Limiter` tokens — drain the burst at each new timestamp before probing drop behavior.

The canonical pattern lives in `internal/plugin/hostsvc/event_ratelimit.go` and `event_ratelimit_test.go`. Other live examples: `internal/llm/retry.go`, `internal/plugin/oauth/scanner.go`, `internal/plugin/process/rss_sampler.go`.

### Signal, don't poll

When a test waits for an asynchronous side effect (DB row inserted, status transition committed, audit step flushed), synchronize on an event the system already publishes — do not poll on a tight wall-clock deadline.

Agent tests use `capturePublisher.waitForEvent(t, eventType, timeout)` to block on a specific event-bus message (e.g. `pub.waitForEvent(t, "feedback.created", 5*time.Second)` in `feedback_test.go`); the deadline becomes a generous CI-tolerance bound (seconds), not the actual assertion. If you find yourself writing `time.Sleep` inside a `for time.Now().Before(deadline)` loop, look for a publisher, channel, or callback that fires the moment the work is done. The few waits that genuinely cannot be turned into signals must use deadlines at least 5× the expected duration.

### Mock MCP servers

For tool call tests, spin up an `httptest.Server` that returns JSON-RPC responses:

```go
mcpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    json.NewEncoder(w).Encode(map[string]any{
        "jsonrpc": "2.0",
        "result": map[string]any{
            "content": []map[string]any{{"type": "text", "text": "tool output"}},
            "isError": false,
        },
    })
}))
```

### State machine tests

Test status transitions by injecting a publisher and asserting on published `run.status_changed` events. See `internal/execution/agent/state_test.go`.

### AuditWriter flush ordering

The AuditWriter buffers step inserts in a background goroutine. Always call `w.Close()` before querying `run_steps` — otherwise your assertions may see incomplete data.

## Frontend tests

Frontend tests use Vitest with jsdom. Key patterns:

- **SSE tests** (`hooks/useSSE.test.ts`): Use a custom `makeFakeStream()` harness that simulates a ReadableStream. Push SSE frames and assert on TanStack Query cache invalidations.
- **Component tests**: React Testing Library with `renderHook()` for hooks, `render()` for components.
- **Table-driven**: Use Vitest's `it.each()` for parameterized tests.

## Test fixtures

LLM provider tests use JSON fixture files in `testdata/` directories (e.g., `internal/llm/openai/testdata/`). These contain canned API responses for parsing tests without network calls.

## What to test

From `contributing.md` — test:
- State machine transitions
- Error paths (missing tool, token budget exceeded, MCP server unreachable)
- Concurrent audit writes
- Context cancellation propagation

Don't test trivial getters.

## Hard-won rules (moved from CLAUDE.md, 2026-07-31)

The root CLAUDE.md keeps the compressed rules; these are the full statements with the war stories behind them.

**Testing time-dependent code.** Anything whose behavior depends on `time.Now()` — rate limiters, timeouts, scheduled jobs, audit-coalescing windows — must route through an injectable package-level clock variable (convention: `var timeNow = func() time.Time { return time.Now() }`), and tests must swap it via `t.Cleanup` rather than relying on wall-clock timing. The integration test for issue #222 originally asserted `>= 50` drops to absorb token-bucket refill noise during the loop's wall-clock duration; CI flaked when the loop ran 20ms (2 tokens refilled, 48 drops). The fix: route `rate.Limiter.Allow()` through `AllowN(timeNow(), 1)` — `x/time/rate` exposes the `*N(t time.Time, ...)` variants exactly for this — then freeze `timeNow` in the test for exact-equality assertions (`drops == total - burst`). The same pattern applies to `time.AfterFunc`, `time.NewTimer`, and any custom scheduling. Concrete rules: (1) production code never calls `time.Now()` directly inside hot paths that tests need to assert against — wrap in `timeNow()` instead; (2) tests that mutate a shared package-level clock variable must not run with `t.Parallel()`; (3) when `t.Parallel()` is needed, use external test packages with a `SetTimeNowForTest(fn) (restore func())` export hook; (4) be aware that advancing a fake clock also refills `rate.Limiter` tokens — drain the burst at each new timestamp before probing drop behavior. See `internal/plugin/hostsvc/event_ratelimit.go` and `event_ratelimit_test.go` for the canonical pattern.

**Drain launched runs before cleanup.** Any test that can launch a run (`RunLauncher.Launch` spawns a fire-and-forget goroutine) must keep the `RunManager` in a variable and register `t.Cleanup(mgr.Wait)` **after** the `testutil.NewTestStore` call — cleanup is LIFO, so the manager drains run goroutines before the store closes. Otherwise the goroutine races test teardown: it either logs "sql: database is closed" or, if it reaches the LLM, panics the whole test binary on `NewNoopLLMClient`. Use `testutil.NewMockLLMClient()` (zero responses — errors when exhausted, failing the run fast) for tests whose launched runs can reach the LLM; reserve `NewNoopLLMClient` (panics on any call) for paths that provably never launch. This was a latent race for as long as the suite was slow; faster test setup made it fire deterministically.

**Join trigger sources before the manager drain.** `t.Cleanup(mgr.Wait)` alone is **unsafe when a trigger source (Scheduler, Poller, CronRunner) is still running at cleanup time**: the source can reach `RunLauncher.Launch` — whose `RunManager` `wg.Add` races `mgr.Wait()`, undefined behavior per `sync.WaitGroup`, and a genuine `-race` failure rather than a timing flake (#787). Any test that starts a trigger source must cancel the source's root context AND join its goroutines (`scheduler.Wait()` / `poller.Wait()` / `runner.Wait()`) *before* the manager drain — register that cleanup **after** `t.Cleanup(mgr.Wait)` so LIFO ordering joins the source first. `internal/trigger`'s external tests use the `stopTriggerSource(t, cancel, source.Wait)` helper for this. To wait for one specific run mid-test without draining everything, use `mgr.WaitForDeregistration(runID, timeout)` — never `mgr.Wait()` while a source can still launch. Production is guarded by ordering, not luck: `main.go` quiesces and joins every trigger source before `runManager.Wait()`.

**Signal-don't-poll.** When a test waits for an asynchronous side effect (DB row inserted, status transition committed, audit step flushed), do **not** poll on a tight wall-clock deadline — synchronize on an event the system already publishes. Tests in `internal/execution/agent/` use `capturePublisher.waitForEvent(t, eventType, timeout)` to block on a specific event-bus message; the deadline becomes a generous CI-tolerance bound (seconds, not milliseconds) rather than a real assertion. If you find yourself writing `time.Sleep` inside a `for time.Now().Before(deadline)` loop, ask whether there is a publisher, channel, or callback that fires the moment the work is done — that signal is the correct synchronization primitive. Tight polling budgets ("100ms should be enough") are how CI flakes are born. The few wall-clock waits that genuinely cannot be turned into signals (e.g. waiting for a `time.NewTimer` to fire when the production code can't yet take an injectable clock) must use deadlines at least 5× the expected duration so CI scheduling jitter cannot exceed them.
