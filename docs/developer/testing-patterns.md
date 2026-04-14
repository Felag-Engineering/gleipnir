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
| `NewMockLLMClient(responses...)` | Returns pre-canned responses in order | Agent tests with scripted LLM behavior |
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

`RecordingPublisher` captures published events for assertion:

```go
pub := &testutil.RecordingPublisher{}
// ... pass pub to components that publish events ...
events := pub.Events()
```

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

See `internal/agent/agent_test.go` for examples.

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

Test status transitions by injecting a `RecordingPublisher` and asserting on published `run.status_changed` events. See `internal/agent/state_test.go`.

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
