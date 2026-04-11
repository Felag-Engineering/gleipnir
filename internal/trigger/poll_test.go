package trigger_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rapp992/gleipnir/internal/agent"
	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/llm"
	"github.com/rapp992/gleipnir/internal/mcp"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/run"
	"github.com/rapp992/gleipnir/internal/testutil"
	"github.com/rapp992/gleipnir/internal/trigger"
)

// pollResultServer starts a test HTTP server that behaves as a minimal MCP server.
// tools/list returns a single "check" tool. tools/call returns the configured
// content items. Callers can swap the content at runtime via the returned setter.
func pollResultServer(t *testing.T, initialContent []map[string]any) (*httptest.Server, func([]map[string]any)) {
	t.Helper()
	var current atomic.Value
	current.Store(initialContent)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck
		w.Header().Set("Content-Type", "application/json")
		method, _ := req["method"].(string)
		switch method {
		case "tools/list":
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"jsonrpc": "2.0", "id": req["id"],
				"result": map[string]any{
					"tools": []map[string]any{{
						"name":        "check",
						"description": "poll check tool",
						"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
					}},
				},
			})
		case "tools/call":
			content := current.Load().([]map[string]any)
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"jsonrpc": "2.0", "id": req["id"],
				"result": map[string]any{
					"content": content,
					"isError": false,
				},
			})
		default:
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"jsonrpc": "2.0", "id": req["id"],
				"result": map[string]any{},
			})
		}
	}))
	t.Cleanup(srv.Close)

	setter := func(c []map[string]any) { current.Store(c) }
	return srv, setter
}

// textContent is a helper to build a single-item MCP content array with the given text.
func textContent(text string) []map[string]any {
	return []map[string]any{{"type": "text", "text": text}}
}

// errorResultServer starts an MCP stub that returns isError=true for tools/call.
func errorResultServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck
		w.Header().Set("Content-Type", "application/json")
		method, _ := req["method"].(string)
		switch method {
		case "tools/list":
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"jsonrpc": "2.0", "id": req["id"],
				"result": map[string]any{
					"tools": []map[string]any{{
						"name":        "check",
						"description": "poll check tool",
						"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
					}},
				},
			})
		case "tools/call":
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"jsonrpc": "2.0", "id": req["id"],
				"result": map[string]any{
					"content": []map[string]any{{"type": "text", "text": "error"}},
					"isError": true,
				},
			})
		default:
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"jsonrpc": "2.0", "id": req["id"],
				"result": map[string]any{},
			})
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// setupPollerFixture opens a temp SQLite store and registers the given test
// MCP server under the name "poll-server".
func setupPollerFixture(t *testing.T, mcpSrv *httptest.Server) (*db.Store, *mcp.Registry) {
	t.Helper()
	store := testutil.NewTestStore(t)
	registry := mcp.NewRegistry(store.Queries())
	if err := registry.RegisterServer(context.Background(), "poll-server", mcpSrv.URL); err != nil {
		t.Fatalf("RegisterServer: %v", err)
	}
	return store, registry
}

// pollPolicyYAML builds a poll policy YAML using match=all with a single check
// that expects $.status to equal "degraded". Tests pass "100ms" which is below
// the 1m validator minimum, bypassing the minimum by inserting directly into the
// DB (the Poller does not re-validate at runtime).
func pollPolicyYAML(name string, intervalStr string) string {
	return pollPolicyYAMLWithConcurrency(name, intervalStr, "parallel")
}

func pollPolicyYAMLWithConcurrency(name, intervalStr, concurrency string) string {
	return fmt.Sprintf(`
name: %s
trigger:
  type: poll
  interval: %s
  match: all
  checks:
    - tool: poll-server.check
      path: "$.status"
      equals: degraded
capabilities:
  tools:
    - tool: poll-server.check
agent:
  task: "process poll result"
  concurrency: %s
`, name, intervalStr, concurrency)
}

// pollPolicyYAMLMatchAny builds a poll policy with match=any and two checks
// (equals "degraded" or equals "critical").
func pollPolicyYAMLMatchAny(name, intervalStr, concurrency string) string {
	return fmt.Sprintf(`
name: %s
trigger:
  type: poll
  interval: %s
  match: any
  checks:
    - tool: poll-server.check
      path: "$.status"
      equals: degraded
    - tool: poll-server.check
      path: "$.status"
      equals: critical
capabilities:
  tools:
    - tool: poll-server.check
agent:
  task: "process poll result"
  concurrency: %s
`, name, intervalStr, concurrency)
}

// insertTestPollPolicy creates a poll policy row in the DB.
func insertTestPollPolicy(t *testing.T, store *db.Store, policyID, name, yamlStr string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := store.CreatePolicy(context.Background(), db.CreatePolicyParams{
		ID:          policyID,
		Name:        name,
		TriggerType: "poll",
		Yaml:        yamlStr,
		CreatedAt:   now,
		UpdatedAt:   now,
	})
	if err != nil {
		t.Fatalf("insertTestPollPolicy %s: %v", policyID, err)
	}
}

// pollerFactory returns an AgentFactory backed by a mock LLM so tests do not
// make real Claude API calls.
func pollerFactory() run.AgentFactory {
	return func(cfg agent.Config) (agent.Runner, error) {
		cfg.LLMClient = testutil.NewMockLLMClient(
			testutil.MakeLLMTextResponse("done", llm.StopReasonEndTurn, 10, 5),
		)
		return agent.New(cfg)
	}
}

// waitForRuns polls the DB until at least wantCount runs exist for the given
// policy, or the deadline passes.
func waitForRuns(t *testing.T, store *db.Store, policyID string, wantCount int, deadline time.Duration) []db.Run {
	t.Helper()
	ctx := context.Background()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		runs, err := store.ListRuns(ctx, db.ListRunsParams{PolicyID: policyID, Limit: 100})
		if err != nil {
			t.Fatalf("ListRuns: %v", err)
		}
		if len(runs) >= wantCount {
			return runs
		}
		time.Sleep(50 * time.Millisecond)
	}
	runs, _ := store.ListRuns(ctx, db.ListRunsParams{PolicyID: policyID, Limit: 100})
	return runs
}

// TestPoller_CheckMatchFires verifies that when the MCP tool returns a JSON
// response matching the configured check, a run is created.
func TestPoller_CheckMatchFires(t *testing.T) {
	// Return {"status":"degraded"} so the check `$.status equals "degraded"` passes.
	mcpSrv, _ := pollResultServer(t, textContent(`{"status":"degraded"}`))
	store, registry := setupPollerFixture(t, mcpSrv)

	yamlStr := pollPolicyYAML("poll-match-fires", "100ms")
	insertTestPollPolicy(t, store, "pol-match-fires", "poll-match-fires", yamlStr)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	manager := run.NewRunManager()
	launcher := run.NewRunLauncher(store, registry, manager, pollerFactory(), nil, 0)
	poller := trigger.NewPoller(store, launcher, registry)

	if err := poller.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	runs := waitForRuns(t, store, "pol-match-fires", 1, 8*time.Second)
	if len(runs) == 0 {
		t.Fatal("expected at least one run to be created, but none appeared")
	}
	manager.Wait()
}

// TestPoller_CheckNoMatchNoRun verifies that when the check condition does not
// match the tool response, no run is created.
func TestPoller_CheckNoMatchNoRun(t *testing.T) {
	// Return {"status":"healthy"} — does NOT match equals "degraded".
	mcpSrv, _ := pollResultServer(t, textContent(`{"status":"healthy"}`))
	store, registry := setupPollerFixture(t, mcpSrv)

	yamlStr := pollPolicyYAML("poll-no-match", "100ms")
	insertTestPollPolicy(t, store, "pol-no-match", "poll-no-match", yamlStr)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	manager := run.NewRunManager()
	launcher := run.NewRunLauncher(store, registry, manager, pollerFactory(), nil, 0)
	poller := trigger.NewPoller(store, launcher, registry)

	if err := poller.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wait several poll intervals — no run should appear.
	time.Sleep(800 * time.Millisecond)

	runs, err := store.ListRuns(context.Background(), db.ListRunsParams{
		PolicyID: "pol-no-match", Limit: 100,
	})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Errorf("expected no runs when check does not match, got %d", len(runs))
	}
	_ = manager
}

// TestPoller_MatchAny_OnePassFires verifies that match=any fires when at least
// one check passes. The server returns "degraded"; the first check (equals
// "degraded") passes even though the second (equals "critical") fails.
func TestPoller_MatchAny_OnePassFires(t *testing.T) {
	mcpSrv, _ := pollResultServer(t, textContent(`{"status":"degraded"}`))
	store, registry := setupPollerFixture(t, mcpSrv)

	yamlStr := pollPolicyYAMLMatchAny("poll-any-pass", "100ms", "parallel")
	insertTestPollPolicy(t, store, "pol-any-pass", "poll-any-pass", yamlStr)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	manager := run.NewRunManager()
	launcher := run.NewRunLauncher(store, registry, manager, pollerFactory(), nil, 0)
	poller := trigger.NewPoller(store, launcher, registry)

	if err := poller.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	runs := waitForRuns(t, store, "pol-any-pass", 1, 8*time.Second)
	if len(runs) == 0 {
		t.Fatal("expected a run when match=any and one check passes, but none appeared")
	}
	manager.Wait()
}

// TestPoller_MatchAll_OneFailNoRun verifies that match=all does not fire when
// any check fails. The server returns "degraded"; the second check (equals
// "critical") fails, so with match=all no run fires.
func TestPoller_MatchAll_OneFailNoRun(t *testing.T) {
	mcpSrv, _ := pollResultServer(t, textContent(`{"status":"degraded"}`))
	store, registry := setupPollerFixture(t, mcpSrv)

	// Uses match=any policy shape but we swap to all manually via YAML.
	// The two checks are: equals "degraded" and equals "critical".
	// Server returns "degraded": first passes, second fails.
	yamlStr := fmt.Sprintf(`
name: poll-all-fail
trigger:
  type: poll
  interval: 100ms
  match: all
  checks:
    - tool: poll-server.check
      path: "$.status"
      equals: degraded
    - tool: poll-server.check
      path: "$.status"
      equals: critical
capabilities:
  tools:
    - tool: poll-server.check
agent:
  task: "process poll result"
  concurrency: parallel
`)
	insertTestPollPolicy(t, store, "pol-all-fail", "poll-all-fail", yamlStr)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	manager := run.NewRunManager()
	launcher := run.NewRunLauncher(store, registry, manager, pollerFactory(), nil, 0)
	poller := trigger.NewPoller(store, launcher, registry)

	if err := poller.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	time.Sleep(800 * time.Millisecond)

	runs, err := store.ListRuns(context.Background(), db.ListRunsParams{
		PolicyID: "pol-all-fail", Limit: 100,
	})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Errorf("expected no runs when match=all and one check fails, got %d", len(runs))
	}
	_ = manager
}

// TestPoller_ToolErrorTreatedAsNotPassed verifies that when the poll tool
// returns an error, the check is treated as not-passed. With match=all, no
// run fires.
func TestPoller_ToolErrorTreatedAsNotPassed(t *testing.T) {
	mcpSrv := errorResultServer(t)
	store, registry := setupPollerFixture(t, mcpSrv)

	yamlStr := pollPolicyYAML("poll-tool-error", "100ms")
	insertTestPollPolicy(t, store, "pol-tool-error", "poll-tool-error", yamlStr)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	manager := run.NewRunManager()
	launcher := run.NewRunLauncher(store, registry, manager, pollerFactory(), nil, 0)
	poller := trigger.NewPoller(store, launcher, registry)

	if err := poller.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	time.Sleep(800 * time.Millisecond)

	runs, err := store.ListRuns(context.Background(), db.ListRunsParams{
		PolicyID: "pol-tool-error", Limit: 100,
	})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Errorf("expected no runs when tool errors with match=all, got %d", len(runs))
	}
	_ = manager
}

// TestPoller_ConcurrencySkip verifies that when an active run already exists,
// a new matching poll result does not create a second run under concurrency: skip.
func TestPoller_ConcurrencySkip(t *testing.T) {
	mcpSrv, _ := pollResultServer(t, textContent(`{"status":"degraded"}`))
	store, registry := setupPollerFixture(t, mcpSrv)

	yamlStr := pollPolicyYAMLWithConcurrency("poll-skip", "100ms", "skip")
	insertTestPollPolicy(t, store, "pol-poll-skip", "poll-skip", yamlStr)

	// Insert an active run so the concurrency check blocks new ones.
	insertTestRun(t, store, "r-poll-skip-active", "pol-poll-skip", model.RunStatusRunning)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	manager := run.NewRunManager()
	launcher := run.NewRunLauncher(store, registry, manager, pollerFactory(), nil, 0)
	poller := trigger.NewPoller(store, launcher, registry)

	if err := poller.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Several poll intervals pass. The active run blocks new launches.
	time.Sleep(600 * time.Millisecond)

	runs, err := store.ListRuns(context.Background(), db.ListRunsParams{
		PolicyID: "pol-poll-skip", Limit: 100,
	})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	// Only the pre-inserted active run should exist.
	if len(runs) != 1 {
		t.Errorf("expected 1 run (pre-existing active), got %d", len(runs))
	}
}

// TestPoller_GracefulShutdown verifies that cancelling the context causes
// pollLoop goroutines to exit and Wait() returns promptly.
func TestPoller_GracefulShutdown(t *testing.T) {
	mcpSrv, _ := pollResultServer(t, textContent(`{"status":"healthy"}`))
	store, registry := setupPollerFixture(t, mcpSrv)

	yamlStr := pollPolicyYAML("poll-shutdown", "100ms")
	insertTestPollPolicy(t, store, "pol-shutdown", "poll-shutdown", yamlStr)

	ctx, cancel := context.WithCancel(context.Background())

	manager := run.NewRunManager()
	launcher := run.NewRunLauncher(store, registry, manager, pollerFactory(), nil, 0)
	poller := trigger.NewPoller(store, launcher, registry)

	if err := poller.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Let a couple of intervals pass, then cancel.
	time.Sleep(250 * time.Millisecond)
	cancel()

	done := make(chan struct{})
	go func() {
		poller.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Goroutines exited cleanly.
	case <-time.After(5 * time.Second):
		t.Error("poller.Wait() did not return within 5s after context cancel")
	}
}
