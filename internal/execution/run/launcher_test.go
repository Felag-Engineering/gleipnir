package run_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/execution/agent"
	"github.com/rapp992/gleipnir/internal/execution/run"
	"github.com/rapp992/gleipnir/internal/llm"
	"github.com/rapp992/gleipnir/internal/mcp"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/policy"
	"github.com/rapp992/gleipnir/internal/testutil"
)

// failingAgentFactory returns an AgentFactory whose New call always fails.
func failingAgentFactory(err error) run.AgentFactory {
	return func(cfg agent.Config) (*agent.BoundAgent, error) {
		return nil, err
	}
}

// newStubMCPServer starts an httptest.Server that handles MCP JSON-RPC over HTTP.
// It responds to tools/list with a single "read_data" tool and to all other
// methods with a stub result.
func newStubMCPServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
						"name":        "read_data",
						"description": "reads data",
						"inputSchema": map[string]any{
							"type": "object", "properties": map[string]any{},
						},
					}},
				},
			})
		default:
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"jsonrpc": "2.0", "id": req["id"],
				"result": map[string]any{
					"content": []map[string]any{{"type": "text", "text": "stub result"}},
					"isError": false,
				},
			})
		}
	}))
}

// setupIntegrationFixture opens a temp SQLite store, starts a stub MCP server,
// and registers it with a fresh Registry.
func setupIntegrationFixture(t *testing.T) (*db.Store, *mcp.Registry) {
	t.Helper()
	store := testutil.NewTestStore(t)
	mcpSrv := newStubMCPServer(t)
	t.Cleanup(mcpSrv.Close)
	registry := mcp.NewRegistry(store.Queries())
	if err := registry.RegisterServer(context.Background(), "stub-server", mcpSrv.URL); err != nil {
		t.Fatalf("RegisterServer: %v", err)
	}
	return store, registry
}

// localAgentFactory returns an AgentFactory that uses a mock LLM client so
// no real API calls are made during launcher tests.
func localAgentFactory() run.AgentFactory {
	return func(cfg agent.Config) (*agent.BoundAgent, error) {
		cfg.LLMClient = testutil.NewMockLLMClient(
			testutil.MakeLLMTextResponse("done", llm.StopReasonEndTurn, 10, 5),
		)
		return agent.New(cfg)
	}
}

func TestCheckConcurrency(t *testing.T) {
	cases := []struct {
		name        string
		hasActive   bool
		concurrency model.ConcurrencyPolicy
		wantErr     error
		wantNil     bool
	}{
		{
			name:        "skip with active run returns ErrConcurrencySkipActive",
			hasActive:   true,
			concurrency: model.ConcurrencySkip,
			wantErr:     run.ErrConcurrencySkipActive,
		},
		{
			name:        "skip with no active runs returns nil",
			hasActive:   false,
			concurrency: model.ConcurrencySkip,
			wantNil:     true,
		},
		{
			name:        "parallel returns nil",
			concurrency: model.ConcurrencyParallel,
			wantNil:     true,
		},
		{
			name:        "queue with active run returns ErrConcurrencyQueueActive",
			hasActive:   true,
			concurrency: model.ConcurrencyQueue,
			wantErr:     run.ErrConcurrencyQueueActive,
		},
		{
			name:        "queue with no active run returns nil",
			hasActive:   false,
			concurrency: model.ConcurrencyQueue,
			wantNil:     true,
		},
		{
			name:        "replace with no active runs returns nil",
			hasActive:   false,
			concurrency: model.ConcurrencyReplace,
			wantNil:     true,
		},
		{
			name:        "unknown concurrency returns ErrConcurrencyUnrecognised",
			concurrency: model.ConcurrencyPolicy("unknown"),
			wantErr:     run.ErrConcurrencyUnrecognised,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := testutil.NewTestStore(t)
			policyID := "cp-test"
			testutil.InsertPolicy(t, store, policyID, "policy-"+policyID, "webhook", testutil.MinimalWebhookPolicy)

			if tc.hasActive {
				testutil.InsertRun(t, store, "r-active", policyID, model.RunStatusRunning)
			}

			registry := mcp.NewRegistry(store.Queries())
			manager := run.NewRunManager()
			// factory is nil — CheckConcurrency never calls it.
			launcher := run.NewRunLauncher(store, registry, manager, nil, nil, 0, nil)

			err := launcher.CheckConcurrency(context.Background(), policyID, tc.concurrency)
			if tc.wantNil {
				if err != nil {
					t.Errorf("CheckConcurrency() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("CheckConcurrency() = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestCheckConcurrency_Replace(t *testing.T) {
	t.Run("cancels active run and returns nil", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		policyID := "cp-replace"
		testutil.InsertPolicy(t, store, policyID, "policy-"+policyID, "webhook", testutil.MinimalWebhookPolicy)
		testutil.InsertRun(t, store, "r-replace-active", policyID, model.RunStatusRunning)

		manager := run.NewRunManager()
		cancelCalled := false
		// Cap-1 channels match the production channels created by launcher.go.
		manager.Register("r-replace-active", func() { cancelCalled = true }, make(chan bool, 1), make(chan string, 1))

		// Simulate the agent goroutine acknowledging cancellation quickly.
		// WaitGroup + t.Cleanup ensures the goroutine has exited before the test
		// is considered done, preventing a race on manager state under -race.
		var wg sync.WaitGroup
		wg.Add(1)
		t.Cleanup(wg.Wait)
		go func() {
			defer wg.Done()
			time.Sleep(20 * time.Millisecond)
			manager.Deregister("r-replace-active")
		}()

		registry := mcp.NewRegistry(store.Queries())
		launcher := run.NewRunLauncher(store, registry, manager, nil, nil, 0, nil)

		err := launcher.CheckConcurrency(context.Background(), policyID, model.ConcurrencyReplace)
		if err != nil {
			t.Errorf("CheckConcurrency() = %v, want nil", err)
		}
		if !cancelCalled {
			t.Error("cancel func was not called for active run")
		}
	})

	t.Run("cancels multiple active runs and returns nil", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		policyID := "cp-replace-multi"
		testutil.InsertPolicy(t, store, policyID, "policy-"+policyID, "webhook", testutil.MinimalWebhookPolicy)

		runIDs := []string{"r-multi-1", "r-multi-2", "r-multi-3"}
		for _, id := range runIDs {
			testutil.InsertRun(t, store, id, policyID, model.RunStatusRunning)
		}

		manager := run.NewRunManager()
		cancelled := make(map[string]bool)
		for _, id := range runIDs {
			id := id
			// Cap-1 channels match the production channels created by launcher.go.
			manager.Register(id, func() { cancelled[id] = true }, make(chan bool, 1), make(chan string, 1))
		}

		// Simulate all goroutines exiting after cancellation.
		// WaitGroup + t.Cleanup ensures all goroutines have exited before the test
		// is considered done, preventing a race on manager state under -race.
		var wg sync.WaitGroup
		t.Cleanup(wg.Wait)
		for _, id := range runIDs {
			id := id
			wg.Add(1)
			go func() {
				defer wg.Done()
				time.Sleep(20 * time.Millisecond)
				manager.Deregister(id)
			}()
		}

		registry := mcp.NewRegistry(store.Queries())
		launcher := run.NewRunLauncher(store, registry, manager, nil, nil, 0, nil)

		err := launcher.CheckConcurrency(context.Background(), policyID, model.ConcurrencyReplace)
		if err != nil {
			t.Errorf("CheckConcurrency() = %v, want nil", err)
		}
		for _, id := range runIDs {
			if !cancelled[id] {
				t.Errorf("cancel func was not called for run %s", id)
			}
		}
	})
}

func TestLaunch_ToolResolutionFailure(t *testing.T) {
	// Policy grants a tool that does not exist in the registry — ResolveForPolicy
	// returns an error. Launch should mark the run failed and return an error.
	store := testutil.NewTestStore(t)
	// No MCP server registered, so any tool reference will fail resolution.
	registry := mcp.NewRegistry(store.Queries())
	manager := run.NewRunManager()
	launcher := run.NewRunLauncher(store, registry, manager, nil, nil, 0, nil)

	const policyWithMissingTool = `
name: tool-failure-policy
trigger:
  type: webhook
capabilities:
  tools:
    - tool: nonexistent-server.some_tool
agent:
  model: claude-opus-4-5
  task: "test task"
  concurrency: parallel
`
	testutil.InsertPolicy(t, store, "p-tool-fail", "policy-p-tool-fail", "webhook", policyWithMissingTool)
	parsed, err := policy.Parse(policyWithMissingTool, "anthropic", "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("policy.Parse: %v", err)
	}

	_, launchErr := launcher.Launch(context.Background(), run.LaunchParams{
		PolicyID:       "p-tool-fail",
		TriggerType:    model.TriggerTypeWebhook,
		TriggerPayload: `{}`,
		ParsedPolicy:   parsed,
	})
	if launchErr == nil {
		t.Fatal("Launch() expected error on tool resolution failure, got nil")
	}

	// The run should exist in DB and be marked failed.
	runs, err := store.ListRuns(context.Background(), db.ListRunsParams{PolicyID: "p-tool-fail", Limit: 100})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) == 0 {
		t.Fatal("expected run to be created before failure, got 0 runs")
	}
	if runs[0].Status != string(model.RunStatusFailed) {
		t.Errorf("run.Status = %q, want %q", runs[0].Status, model.RunStatusFailed)
	}
}

func TestLaunch_AgentConstructionFailure(t *testing.T) {
	// Factory always returns an error — agent construction fails after tools are resolved.
	store, registry := setupIntegrationFixture(t)
	manager := run.NewRunManager()

	agentErr := errors.New("deliberate construction failure")
	launcher := run.NewRunLauncher(store, registry, manager, failingAgentFactory(agentErr), nil, 0, nil)

	const launchPolicy = `
name: agent-fail-policy
trigger:
  type: webhook
capabilities:
  tools:
    - tool: stub-server.read_data
agent:
  model: claude-opus-4-5
  task: "test task"
  concurrency: parallel
`
	testutil.InsertPolicy(t, store, "p-agent-fail", "policy-p-agent-fail", "webhook", launchPolicy)
	parsed, err := policy.Parse(launchPolicy, "anthropic", "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("policy.Parse: %v", err)
	}

	_, launchErr := launcher.Launch(context.Background(), run.LaunchParams{
		PolicyID:       "p-agent-fail",
		TriggerType:    model.TriggerTypeWebhook,
		TriggerPayload: `{}`,
		ParsedPolicy:   parsed,
	})
	if launchErr == nil {
		t.Fatal("Launch() expected error on agent construction failure, got nil")
	}

	// The run should exist in DB and be marked failed.
	runs, err := store.ListRuns(context.Background(), db.ListRunsParams{PolicyID: "p-agent-fail", Limit: 100})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) == 0 {
		t.Fatal("expected run to be created before failure, got 0 runs")
	}
	if runs[0].Status != string(model.RunStatusFailed) {
		t.Errorf("run.Status = %q, want %q", runs[0].Status, model.RunStatusFailed)
	}
}

func TestLaunch_Successful(t *testing.T) {
	// Full happy-path launch: run should appear in DB with correct trigger_type
	// and payload, and LaunchResult.RunID should be non-empty.
	store, registry := setupIntegrationFixture(t)
	manager := run.NewRunManager()
	launcher := run.NewRunLauncher(store, registry, manager, localAgentFactory(), nil, 0, nil)

	const launchPolicy = `
name: launch-success-policy
trigger:
  type: webhook
capabilities:
  tools:
    - tool: stub-server.read_data
agent:
  model: claude-opus-4-5
  task: "test task"
  concurrency: parallel
`
	testutil.InsertPolicy(t, store, "p-launch-ok", "policy-p-launch-ok", "webhook", launchPolicy)
	parsed, err := policy.Parse(launchPolicy, "anthropic", "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("policy.Parse: %v", err)
	}

	result, err := launcher.Launch(context.Background(), run.LaunchParams{
		PolicyID:       "p-launch-ok",
		TriggerType:    model.TriggerTypeWebhook,
		TriggerPayload: `{"event":"test"}`,
		ParsedPolicy:   parsed,
	})
	if err != nil {
		t.Fatalf("Launch() unexpected error: %v", err)
	}
	if result.RunID == "" {
		t.Fatal("Launch() returned empty RunID")
	}

	// Run must exist in the DB immediately (created synchronously before goroutine launches).
	runs, err := store.ListRuns(context.Background(), db.ListRunsParams{PolicyID: "p-launch-ok", Limit: 100})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) == 0 {
		t.Fatal("expected run in DB after successful Launch, got 0")
	}
	r := runs[0]
	if r.ID != result.RunID {
		t.Errorf("run.ID = %q, want %q", r.ID, result.RunID)
	}
	if r.TriggerType != string(model.TriggerTypeWebhook) {
		t.Errorf("run.TriggerType = %q, want %q", r.TriggerType, model.TriggerTypeWebhook)
	}
	if r.TriggerPayload != `{"event":"test"}` {
		t.Errorf("run.TriggerPayload = %q, want %q", r.TriggerPayload, `{"event":"test"}`)
	}

	// Wait for the background goroutine to finish so the test does not leak goroutines.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		updated, _ := store.ListRuns(context.Background(), db.ListRunsParams{PolicyID: "p-launch-ok", Limit: 100})
		if len(updated) > 0 && updated[0].Status != string(model.RunStatusPending) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	manager.Wait()
}

func TestEnqueue(t *testing.T) {
	const queuePolicyYAML = `
name: enqueue-test-policy
trigger:
  type: webhook
capabilities:
  tools:
    - tool: stub-server.read_data
agent:
  model: claude-opus-4-5
  task: "test task"
  concurrency: queue
  queue_depth: 2
`
	cases := []struct {
		name           string
		preloadEntries int
		queueDepth     int
		wantErr        error
		wantNil        bool
	}{
		{
			name:           "enqueues when under depth limit",
			preloadEntries: 0,
			queueDepth:     2,
			wantNil:        true,
		},
		{
			name:           "enqueues when one below limit",
			preloadEntries: 1,
			queueDepth:     2,
			wantNil:        true,
		},
		{
			name:           "returns ErrConcurrencyQueueFull when at limit",
			preloadEntries: 2,
			queueDepth:     2,
			wantErr:        run.ErrConcurrencyQueueFull,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := testutil.NewTestStore(t)
			testutil.InsertPolicy(t, store, "p-enqueue", "policy-p-enqueue", "webhook", queuePolicyYAML)

			for i := 0; i < tc.preloadEntries; i++ {
				testutil.InsertQueueEntry(t, store, "p-enqueue", "webhook")
			}

			registry := mcp.NewRegistry(store.Queries())
			launcher := run.NewRunLauncher(store, registry, run.NewRunManager(), nil, nil, 0, nil)

			parsed, err := policy.Parse(queuePolicyYAML, "anthropic", "claude-sonnet-4-6")
			if err != nil {
				t.Fatalf("policy.Parse: %v", err)
			}

			enqErr := launcher.Enqueue(context.Background(), run.LaunchParams{
				PolicyID:       "p-enqueue",
				TriggerType:    model.TriggerTypeWebhook,
				TriggerPayload: `{"event":"queued"}`,
				ParsedPolicy:   parsed,
			}, tc.queueDepth)

			if tc.wantNil {
				if enqErr != nil {
					t.Errorf("Enqueue() = %v, want nil", enqErr)
				}
			} else {
				if !errors.Is(enqErr, tc.wantErr) {
					t.Errorf("Enqueue() = %v, want %v", enqErr, tc.wantErr)
				}
			}
		})
	}
}

func TestDrainQueue(t *testing.T) {
	t.Run("launches next queued trigger when queue is non-empty", func(t *testing.T) {
		store, registry := setupIntegrationFixture(t)
		const policyYAML = `
name: drain-test-policy
trigger:
  type: webhook
capabilities:
  tools:
    - tool: stub-server.read_data
agent:
  model: claude-opus-4-5
  task: "test task"
  concurrency: queue
`
		testutil.InsertPolicy(t, store, "p-drain", "policy-p-drain", "webhook", policyYAML)
		testutil.InsertQueueEntry(t, store, "p-drain", "webhook")

		manager := run.NewRunManager()
		launcher := run.NewRunLauncher(store, registry, manager, localAgentFactory(), nil, 0, nil)

		parsed, err := policy.Parse(policyYAML, "anthropic", "claude-sonnet-4-6")
		if err != nil {
			t.Fatalf("policy.Parse: %v", err)
		}

		launcher.DrainQueue(context.Background(), "p-drain", parsed)
		manager.Wait()

		runs, err := store.ListRuns(context.Background(), db.ListRunsParams{PolicyID: "p-drain", Limit: 100})
		if err != nil {
			t.Fatalf("ListRuns: %v", err)
		}
		if len(runs) == 0 {
			t.Fatal("DrainQueue: expected run to be created, got 0 runs")
		}
	})

	t.Run("is a no-op when queue is empty", func(t *testing.T) {
		store, registry := setupIntegrationFixture(t)
		const policyYAML = `
name: drain-empty-policy
trigger:
  type: webhook
capabilities:
  tools:
    - tool: stub-server.read_data
agent:
  model: claude-opus-4-5
  task: "test task"
  concurrency: queue
`
		testutil.InsertPolicy(t, store, "p-drain-empty", "policy-p-drain-empty", "webhook", policyYAML)

		manager := run.NewRunManager()
		launcher := run.NewRunLauncher(store, registry, manager, nil, nil, 0, nil)

		parsed, err := policy.Parse(policyYAML, "anthropic", "claude-sonnet-4-6")
		if err != nil {
			t.Fatalf("policy.Parse: %v", err)
		}

		// Should not panic or return an error — queue is empty.
		launcher.DrainQueue(context.Background(), "p-drain-empty", parsed)
		manager.Wait()

		runs, err := store.ListRuns(context.Background(), db.ListRunsParams{PolicyID: "p-drain-empty", Limit: 100})
		if err != nil {
			t.Fatalf("ListRuns: %v", err)
		}
		if len(runs) != 0 {
			t.Errorf("DrainQueue on empty queue: expected 0 runs, got %d", len(runs))
		}
	})

	t.Run("re-enqueues entry at front when launch fails", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		// No MCP server registered, so ResolveForPolicy will fail and Launch will return an error.
		registry := mcp.NewRegistry(store.Queries())
		const policyYAML = `
name: drain-fail-policy
trigger:
  type: webhook
capabilities:
  tools:
    - tool: nonexistent-server.some_tool
agent:
  model: claude-opus-4-5
  task: "test task"
  concurrency: queue
`
		testutil.InsertPolicy(t, store, "p-drain-fail", "policy-p-drain-fail", "webhook", policyYAML)
		// Insert two entries: the first will fail to launch, the second stays.
		testutil.InsertQueueEntry(t, store, "p-drain-fail", "webhook")
		testutil.InsertQueueEntry(t, store, "p-drain-fail", "webhook")

		manager := run.NewRunManager()
		launcher := run.NewRunLauncher(store, registry, manager, nil, nil, 0, nil)

		parsed, err := policy.Parse(policyYAML, "anthropic", "claude-sonnet-4-6")
		if err != nil {
			t.Fatalf("policy.Parse: %v", err)
		}

		launcher.DrainQueue(context.Background(), "p-drain-fail", parsed)

		// Both entries should still be in the queue (one re-enqueued at front,
		// one untouched).
		count, err := store.CountQueuedTriggers(context.Background(), "p-drain-fail")
		if err != nil {
			t.Fatalf("CountQueuedTriggers: %v", err)
		}
		if count != 2 {
			t.Errorf("expected 2 entries (re-enqueued + original), got %d", count)
		}

		// The re-enqueued entry must be dequeued first (FIFO — front of queue).
		front, err := store.DequeueTrigger(context.Background(), "p-drain-fail")
		if err != nil {
			t.Fatalf("DequeueTrigger: %v", err)
		}
		back, err := store.DequeueTrigger(context.Background(), "p-drain-fail")
		if err != nil {
			t.Fatalf("DequeueTrigger: %v", err)
		}
		if front.Position >= back.Position {
			t.Errorf("re-enqueued entry position (%d) should be less than remaining entry (%d)",
				front.Position, back.Position)
		}
	})

	t.Run("logs error when dequeue fails with DB error", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		const policyYAML = `
name: drain-dberr-policy
trigger:
  type: webhook
capabilities:
  tools:
    - tool: stub-server.read_data
agent:
  model: claude-opus-4-5
  task: "test task"
  concurrency: queue
`
		testutil.InsertPolicy(t, store, "p-drain-dberr", "policy-p-drain-dberr", "webhook", policyYAML)

		manager := run.NewRunManager()
		registry := mcp.NewRegistry(store.Queries())
		launcher := run.NewRunLauncher(store, registry, manager, nil, nil, 0, nil)

		parsed, err := policy.Parse(policyYAML, "anthropic", "claude-sonnet-4-6")
		if err != nil {
			t.Fatalf("policy.Parse: %v", err)
		}

		// Close the DB to force a real error (not sql.ErrNoRows) from DequeueTrigger.
		store.Close()

		// Should not panic — errors are logged, not propagated.
		launcher.DrainQueue(context.Background(), "p-drain-dberr", parsed)
	})
}

func TestLaunch_ToolResolutionFailure_PublishesEvent(t *testing.T) {
	store := testutil.NewTestStore(t)
	registry := mcp.NewRegistry(store.Queries())
	manager := run.NewRunManager()
	pub := &testutil.RecordingPublisher{}
	launcher := run.NewRunLauncher(store, registry, manager, nil, pub, 0, nil)

	const policyYAML = `
name: tool-failure-event-policy
trigger:
  type: webhook
capabilities:
  tools:
    - tool: nonexistent-server.some_tool
agent:
  model: claude-opus-4-5
  task: "test task"
  concurrency: parallel
`
	testutil.InsertPolicy(t, store, "p-tool-fail-evt", "policy-p-tool-fail-evt", "webhook", policyYAML)
	parsed, err := policy.Parse(policyYAML, "anthropic", "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("policy.Parse: %v", err)
	}

	_, launchErr := launcher.Launch(context.Background(), run.LaunchParams{
		PolicyID:       "p-tool-fail-evt",
		TriggerType:    model.TriggerTypeWebhook,
		TriggerPayload: `{}`,
		ParsedPolicy:   parsed,
	})
	if launchErr == nil {
		t.Fatal("expected error, got nil")
	}

	events := pub.EventsByType("run.status_changed")
	if len(events) == 0 {
		t.Fatal("expected run.status_changed event on tool resolution failure, got none")
	}
}

func TestLaunch_AgentConstructionFailure_PublishesEvent(t *testing.T) {
	store, registry := setupIntegrationFixture(t)
	manager := run.NewRunManager()
	pub := &testutil.RecordingPublisher{}

	agentErr := errors.New("deliberate construction failure")
	launcher := run.NewRunLauncher(store, registry, manager, failingAgentFactory(agentErr), pub, 0, nil)

	const policyYAML = `
name: agent-fail-event-policy
trigger:
  type: webhook
capabilities:
  tools:
    - tool: stub-server.read_data
agent:
  model: claude-opus-4-5
  task: "test task"
  concurrency: parallel
`
	testutil.InsertPolicy(t, store, "p-agent-fail-evt", "policy-p-agent-fail-evt", "webhook", policyYAML)
	parsed, err := policy.Parse(policyYAML, "anthropic", "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("policy.Parse: %v", err)
	}

	_, launchErr := launcher.Launch(context.Background(), run.LaunchParams{
		PolicyID:       "p-agent-fail-evt",
		TriggerType:    model.TriggerTypeWebhook,
		TriggerPayload: `{}`,
		ParsedPolicy:   parsed,
	})
	if launchErr == nil {
		t.Fatal("expected error, got nil")
	}

	events := pub.EventsByType("run.status_changed")
	if len(events) == 0 {
		t.Fatal("expected run.status_changed event on agent construction failure, got none")
	}
}

// TestNewAgentFactory_ProviderLookup verifies that NewAgentFactory resolves the
// correct LLMClient from the registry using the policy's Agent.Provider field,
// and returns a descriptive error for unknown providers.
//
// Note: TestLaunch_Successful does not exercise the registry path — it uses
// localAgentFactory(), an inline factory that bypasses NewAgentFactory.
func TestNewAgentFactory_ProviderLookup(t *testing.T) {
	anthropicClient := testutil.NewNoopLLMClient()
	googleClient := testutil.NewNoopLLMClient()

	cases := []struct {
		name            string
		registerClients map[string]llm.LLMClient
		policyProvider  string
		wantErrContains string // empty means no error expected from provider lookup
	}{
		{
			name:            "known provider anthropic",
			registerClients: map[string]llm.LLMClient{"anthropic": anthropicClient},
			policyProvider:  "anthropic",
			// error may come from agent.New (missing state machine etc.) but
			// must NOT be a provider lookup error
			wantErrContains: "",
		},
		{
			name:            "known provider google",
			registerClients: map[string]llm.LLMClient{"google": googleClient},
			policyProvider:  "google",
			wantErrContains: "",
		},
		{
			name:            "unknown provider openai",
			registerClients: map[string]llm.LLMClient{"anthropic": anthropicClient},
			policyProvider:  "openai",
			wantErrContains: "openai",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg := llm.NewProviderRegistry()
			for name, client := range tc.registerClients {
				reg.Register(name, client)
			}

			factory := run.NewAgentFactory(reg)

			cfg := agent.Config{
				Policy: &model.ParsedPolicy{
					Agent: model.AgentConfig{
						ModelConfig: model.ModelConfig{Provider: tc.policyProvider},
					},
				},
			}

			_, err := factory(cfg)

			if tc.wantErrContains != "" {
				// Unknown provider — must get an error containing the provider name.
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErrContains)
				}
				if !strings.Contains(err.Error(), tc.wantErrContains) {
					t.Errorf("error %q does not contain %q", err.Error(), tc.wantErrContains)
				}
			} else {
				// Known provider — error must not be a provider lookup failure.
				// agent.New will fail due to the minimal config, which is acceptable.
				if err != nil && strings.Contains(err.Error(), "unknown LLM provider") {
					t.Errorf("unexpected provider lookup error: %v", err)
				}
			}
		})
	}
}
