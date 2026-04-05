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
	"github.com/rapp992/gleipnir/internal/testutil"
	"github.com/rapp992/gleipnir/internal/trigger"
)

// pollResultServer starts a test HTTP server that behaves as a minimal MCP server.
// tools/list returns a single "check" tool. tools/call returns the configured
// content items. Callers can swap the content at runtime via the setter.
// content is a slice of MCP content items (e.g. []map[string]any{{"type":"text","text":"data"}}).
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

// pollPolicyYAML builds a minimal poll policy YAML. interval is a Go duration
// string (e.g. "100ms"). The stub-server tool is "poll-server.check".
// Tests intentionally pass "100ms" which is below the 30 s validator minimum;
// that minimum is enforced only at policy creation time, not by the Poller
// itself, so tests construct the YAML directly to bypass it.
func pollPolicyYAML(name string, intervalStr string) string {
	return pollPolicyYAMLWithConcurrency(name, intervalStr, "parallel")
}

func pollPolicyYAMLWithConcurrency(name, intervalStr, concurrency string) string {
	return fmt.Sprintf(`
name: %s
trigger:
  type: poll
  interval: %s
  tool: poll-server.check
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
func pollerFactory() trigger.AgentFactory {
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

// TestPoller_PollsAndTriggersRun verifies that a non-empty tool result causes
// a run to be created.
func TestPoller_PollsAndTriggersRun(t *testing.T) {
	mcpSrv, _ := pollResultServer(t, textContent("some data"))
	store, registry := setupPollerFixture(t, mcpSrv)

	// Interval of 100ms so the test completes quickly.
	yamlStr := pollPolicyYAML("poll-trigger-run", "100ms")
	insertTestPollPolicy(t, store, "pol-trigger-run", "poll-trigger-run", yamlStr)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	manager := trigger.NewRunManager()
	launcher := trigger.NewRunLauncher(store, registry, manager, pollerFactory(), nil, 0)
	poller := trigger.NewPoller(store, launcher, registry)

	if err := poller.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	runs := waitForRuns(t, store, "pol-trigger-run", 1, 8*time.Second)
	if len(runs) == 0 {
		t.Fatal("expected at least one run to be created, but none appeared")
	}
	manager.Wait()
}

// TestPoller_SkipsEmptyResult verifies that empty/null MCP tool results do not
// create runs. Cases cover the actual JSON structures that CallTool can return.
func TestPoller_SkipsEmptyResult(t *testing.T) {
	emptyCases := []struct {
		name    string
		content []map[string]any
	}{
		// No content items at all — the tool returned nothing.
		{"empty_content_array", []map[string]any{}},
		// Single text item with empty text.
		{"empty_text", []map[string]any{{"type": "text", "text": ""}}},
		// Multiple text items all with empty text.
		{"all_empty_text", []map[string]any{{"type": "text", "text": ""}, {"type": "text", "text": ""}}},
	}

	for _, tc := range emptyCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			mcpSrv, _ := pollResultServer(t, tc.content)
			store, registry := setupPollerFixture(t, mcpSrv)

			yamlStr := pollPolicyYAML("poll-empty-"+tc.name, "100ms")
			insertTestPollPolicy(t, store, "pol-empty-"+tc.name, "poll-empty-"+tc.name, yamlStr)

			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()

			manager := trigger.NewRunManager()
			launcher := trigger.NewRunLauncher(store, registry, manager, pollerFactory(), nil, 0)
			poller := trigger.NewPoller(store, launcher, registry)

			if err := poller.Start(ctx); err != nil {
				t.Fatalf("Start: %v", err)
			}

			// Run for 1.5 seconds (several poll intervals). No run should appear.
			time.Sleep(1500 * time.Millisecond)

			runs, err := store.ListRuns(context.Background(), db.ListRunsParams{
				PolicyID: "pol-empty-" + tc.name, Limit: 100,
			})
			if err != nil {
				t.Fatalf("ListRuns: %v", err)
			}
			if len(runs) != 0 {
				t.Errorf("expected no runs for empty result case %q, got %d", tc.name, len(runs))
			}
			_ = manager
		})
	}
}

// TestPoller_DoesNotTreatFalseOrZeroAsEmpty verifies that text content "false"
// and "0" are treated as non-empty results and cause runs to be launched.
func TestPoller_DoesNotTreatFalseOrZeroAsEmpty(t *testing.T) {
	cases := []struct {
		name    string
		content []map[string]any
	}{
		{"false", textContent("false")},
		{"zero", textContent("0")},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			mcpSrv, _ := pollResultServer(t, tc.content)
			store, registry := setupPollerFixture(t, mcpSrv)

			yamlStr := pollPolicyYAML("poll-truthy-"+tc.name, "100ms")
			insertTestPollPolicy(t, store, "pol-truthy-"+tc.name, "poll-truthy-"+tc.name, yamlStr)

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			manager := trigger.NewRunManager()
			launcher := trigger.NewRunLauncher(store, registry, manager, pollerFactory(), nil, 0)
			poller := trigger.NewPoller(store, launcher, registry)

			if err := poller.Start(ctx); err != nil {
				t.Fatalf("Start: %v", err)
			}

			runs := waitForRuns(t, store, "pol-truthy-"+tc.name, 1, 8*time.Second)
			if len(runs) == 0 {
				t.Errorf("expected a run for content case %q but none appeared", tc.name)
			}
			manager.Wait()
		})
	}
}

// TestPoller_DeduplicatesSameResult verifies that polling the same result twice
// in a row only creates one run (hash-based dedup).
func TestPoller_DeduplicatesSameResult(t *testing.T) {
	mcpSrv, _ := pollResultServer(t, textContent("constant result"))
	store, registry := setupPollerFixture(t, mcpSrv)

	yamlStr := pollPolicyYAMLWithConcurrency("poll-dedup", "150ms", "parallel")
	insertTestPollPolicy(t, store, "pol-dedup-poll", "poll-dedup", yamlStr)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	manager := trigger.NewRunManager()
	launcher := trigger.NewRunLauncher(store, registry, manager, pollerFactory(), nil, 0)
	poller := trigger.NewPoller(store, launcher, registry)

	if err := poller.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wait for the first run to appear.
	runs := waitForRuns(t, store, "pol-dedup-poll", 1, 5*time.Second)
	if len(runs) == 0 {
		t.Fatal("expected at least one run but none appeared")
	}

	// Wait for several more poll intervals — result is identical so no second run.
	time.Sleep(600 * time.Millisecond)
	manager.Wait()

	finalRuns, err := store.ListRuns(context.Background(), db.ListRunsParams{
		PolicyID: "pol-dedup-poll", Limit: 100,
	})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(finalRuns) != 1 {
		t.Errorf("expected exactly 1 run (dedup), got %d", len(finalRuns))
	}
}

// TestPoller_ConcurrencySkip verifies that when an active run already exists,
// a new poll result does not create a second run under concurrency: skip.
func TestPoller_ConcurrencySkip(t *testing.T) {
	mcpSrv, _ := pollResultServer(t, textContent("data"))
	store, registry := setupPollerFixture(t, mcpSrv)

	yamlStr := pollPolicyYAMLWithConcurrency("poll-skip", "100ms", "skip")
	insertTestPollPolicy(t, store, "pol-poll-skip", "poll-skip", yamlStr)

	// Insert an active run so the concurrency check blocks new ones.
	insertTestRun(t, store, "r-poll-skip-active", "pol-poll-skip", model.RunStatusRunning)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	manager := trigger.NewRunManager()
	launcher := trigger.NewRunLauncher(store, registry, manager, pollerFactory(), nil, 0)
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

// TestPoller_ConcurrencyQueue verifies that when an active run exists, a new
// poll result is enqueued under concurrency: queue.
func TestPoller_ConcurrencyQueue(t *testing.T) {
	mcpSrv, _ := pollResultServer(t, textContent("data"))
	store, registry := setupPollerFixture(t, mcpSrv)

	yamlStr := pollPolicyYAMLWithConcurrency("poll-queue", "100ms", "queue")
	insertTestPollPolicy(t, store, "pol-poll-queue", "poll-queue", yamlStr)

	// Insert an active run so the concurrency check routes to enqueue.
	insertTestRun(t, store, "r-poll-queue-active", "pol-poll-queue", model.RunStatusRunning)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	manager := trigger.NewRunManager()
	launcher := trigger.NewRunLauncher(store, registry, manager, pollerFactory(), nil, 0)
	poller := trigger.NewPoller(store, launcher, registry)

	if err := poller.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wait for the trigger to be enqueued.
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		count, err := store.CountQueuedTriggers(context.Background(), "pol-poll-queue")
		if err != nil {
			t.Fatalf("CountQueuedTriggers: %v", err)
		}
		if count > 0 {
			return // success — trigger was enqueued
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("expected trigger to be enqueued for queue policy with active run, but queue remained empty")
}

// TestPoller_BackoffOnFailure verifies that poll tool errors increment
// consecutive_failures and push next_poll_at into the future.
func TestPoller_BackoffOnFailure(t *testing.T) {
	mcpSrv := errorResultServer(t)
	store, registry := setupPollerFixture(t, mcpSrv)

	yamlStr := pollPolicyYAML("poll-backoff", "100ms")
	insertTestPollPolicy(t, store, "pol-backoff", "poll-backoff", yamlStr)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	manager := trigger.NewRunManager()
	launcher := trigger.NewRunLauncher(store, registry, manager, pollerFactory(), nil, 0)
	poller := trigger.NewPoller(store, launcher, registry)

	if err := poller.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wait a moment for at least one poll attempt and failure to be recorded.
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		state, err := store.GetPollState(context.Background(), "pol-backoff")
		if err == nil && state.ConsecutiveFailures > 0 {
			// Success: failure was recorded.
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = manager

	// Final check: read the state.
	state, err := store.GetPollState(context.Background(), "pol-backoff")
	if err != nil {
		t.Fatalf("GetPollState: %v", err)
	}
	if state.ConsecutiveFailures == 0 {
		t.Error("expected consecutive_failures > 0 after poll tool errors, got 0")
	}
}

// TestPoller_ResetBackoffOnSuccess verifies that a successful poll after
// failures resets consecutive_failures to 0.
func TestPoller_ResetBackoffOnSuccess(t *testing.T) {
	mcpSrv, setResult := pollResultServer(t, []map[string]any{{"type": "text", "text": ""}})
	store, registry := setupPollerFixture(t, mcpSrv)

	yamlStr := pollPolicyYAML("poll-reset", "100ms")
	insertTestPollPolicy(t, store, "pol-reset", "poll-reset", yamlStr)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Pre-seed the poll state with failures so we can verify the reset.
	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339Nano)
	nextStr := now.Add(100 * time.Millisecond).Format(time.RFC3339Nano)
	failures := int64(3)
	if err := store.UpsertPollState(context.Background(), db.UpsertPollStateParams{
		PolicyID:            "pol-reset",
		ConsecutiveFailures: failures,
		NextPollAt:          nextStr,
		CreatedAt:           nowStr,
		UpdatedAt:           nowStr,
	}); err != nil {
		t.Fatalf("UpsertPollState: %v", err)
	}

	// Switch the server to return a non-empty result so the next poll succeeds.
	setResult(textContent("good data"))

	manager := trigger.NewRunManager()
	launcher := trigger.NewRunLauncher(store, registry, manager, pollerFactory(), nil, 0)
	poller := trigger.NewPoller(store, launcher, registry)

	if err := poller.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wait for the success to reset the failure count.
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		state, err := store.GetPollState(context.Background(), "pol-reset")
		if err == nil && state.ConsecutiveFailures == 0 {
			manager.Wait()
			return // success
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("expected consecutive_failures to be reset to 0 after successful poll")
}
