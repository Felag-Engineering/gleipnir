package trigger_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rapp992/gleipnir/internal/agent"
	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/llm"
	"github.com/rapp992/gleipnir/internal/mcp"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/policy"
	"github.com/rapp992/gleipnir/internal/testutil"
	"github.com/rapp992/gleipnir/internal/trigger"
)

// failingAgentFactory returns an AgentFactory whose New call always fails.
func failingAgentFactory(err error) trigger.AgentFactory {
	return func(cfg agent.Config) (agent.Runner, error) {
		return nil, err
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
			wantErr:     trigger.ErrConcurrencySkipActive,
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
			wantErr:     trigger.ErrConcurrencyQueueActive,
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
			wantErr:     trigger.ErrConcurrencyUnrecognised,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := testutil.NewTestStore(t)
			policyID := "cp-test"
			insertTestPolicy(t, store, policyID, minimalWebhookPolicy)

			if tc.hasActive {
				insertTestRun(t, store, "r-active", policyID, model.RunStatusRunning)
			}

			registry := mcp.NewRegistry(store.Queries())
			manager := trigger.NewRunManager()
			// factory is nil — CheckConcurrency never calls it.
			launcher := trigger.NewRunLauncher(store, registry, manager, nil, nil, 0)

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
		insertTestPolicy(t, store, policyID, minimalWebhookPolicy)
		insertTestRun(t, store, "r-replace-active", policyID, model.RunStatusRunning)

		manager := trigger.NewRunManager()
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
		launcher := trigger.NewRunLauncher(store, registry, manager, nil, nil, 0)

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
		insertTestPolicy(t, store, policyID, minimalWebhookPolicy)

		runIDs := []string{"r-multi-1", "r-multi-2", "r-multi-3"}
		for _, id := range runIDs {
			insertTestRun(t, store, id, policyID, model.RunStatusRunning)
		}

		manager := trigger.NewRunManager()
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
		launcher := trigger.NewRunLauncher(store, registry, manager, nil, nil, 0)

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
	manager := trigger.NewRunManager()
	launcher := trigger.NewRunLauncher(store, registry, manager, nil, nil, 0)

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
	insertTestPolicy(t, store, "p-tool-fail", policyWithMissingTool)
	parsed, err := policy.Parse(policyWithMissingTool, model.DefaultProvider, model.DefaultModelName)
	if err != nil {
		t.Fatalf("policy.Parse: %v", err)
	}

	_, launchErr := launcher.Launch(context.Background(), trigger.LaunchParams{
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
	manager := trigger.NewRunManager()

	agentErr := errors.New("deliberate construction failure")
	launcher := trigger.NewRunLauncher(store, registry, manager, failingAgentFactory(agentErr), nil, 0)

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
	insertTestPolicy(t, store, "p-agent-fail", launchPolicy)
	parsed, err := policy.Parse(launchPolicy, model.DefaultProvider, model.DefaultModelName)
	if err != nil {
		t.Fatalf("policy.Parse: %v", err)
	}

	_, launchErr := launcher.Launch(context.Background(), trigger.LaunchParams{
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
	manager := trigger.NewRunManager()
	launcher := trigger.NewRunLauncher(store, registry, manager, schedulerFactory(), nil, 0)

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
	insertTestPolicy(t, store, "p-launch-ok", launchPolicy)
	parsed, err := policy.Parse(launchPolicy, model.DefaultProvider, model.DefaultModelName)
	if err != nil {
		t.Fatalf("policy.Parse: %v", err)
	}

	result, err := launcher.Launch(context.Background(), trigger.LaunchParams{
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
	run := runs[0]
	if run.ID != result.RunID {
		t.Errorf("run.ID = %q, want %q", run.ID, result.RunID)
	}
	if run.TriggerType != string(model.TriggerTypeWebhook) {
		t.Errorf("run.TriggerType = %q, want %q", run.TriggerType, model.TriggerTypeWebhook)
	}
	if run.TriggerPayload != `{"event":"test"}` {
		t.Errorf("run.TriggerPayload = %q, want %q", run.TriggerPayload, `{"event":"test"}`)
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
			wantErr:        trigger.ErrConcurrencyQueueFull,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := testutil.NewTestStore(t)
			insertTestPolicy(t, store, "p-enqueue", queuePolicyYAML)

			for i := 0; i < tc.preloadEntries; i++ {
				testutil.InsertQueueEntry(t, store, "p-enqueue", "webhook")
			}

			registry := mcp.NewRegistry(store.Queries())
			launcher := trigger.NewRunLauncher(store, registry, trigger.NewRunManager(), nil, nil, 0)

			parsed, err := policy.Parse(queuePolicyYAML, model.DefaultProvider, model.DefaultModelName)
			if err != nil {
				t.Fatalf("policy.Parse: %v", err)
			}

			enqErr := launcher.Enqueue(context.Background(), trigger.LaunchParams{
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
		insertTestPolicy(t, store, "p-drain", policyYAML)
		testutil.InsertQueueEntry(t, store, "p-drain", "webhook")

		manager := trigger.NewRunManager()
		launcher := trigger.NewRunLauncher(store, registry, manager, schedulerFactory(), nil, 0)

		parsed, err := policy.Parse(policyYAML, model.DefaultProvider, model.DefaultModelName)
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
		insertTestPolicy(t, store, "p-drain-empty", policyYAML)

		manager := trigger.NewRunManager()
		launcher := trigger.NewRunLauncher(store, registry, manager, nil, nil, 0)

		parsed, err := policy.Parse(policyYAML, model.DefaultProvider, model.DefaultModelName)
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
		insertTestPolicy(t, store, "p-drain-fail", policyYAML)
		// Insert two entries: the first will fail to launch, the second stays.
		testutil.InsertQueueEntry(t, store, "p-drain-fail", "webhook")
		testutil.InsertQueueEntry(t, store, "p-drain-fail", "webhook")

		manager := trigger.NewRunManager()
		launcher := trigger.NewRunLauncher(store, registry, manager, nil, nil, 0)

		parsed, err := policy.Parse(policyYAML, model.DefaultProvider, model.DefaultModelName)
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
		insertTestPolicy(t, store, "p-drain-dberr", policyYAML)

		manager := trigger.NewRunManager()
		registry := mcp.NewRegistry(store.Queries())
		launcher := trigger.NewRunLauncher(store, registry, manager, nil, nil, 0)

		parsed, err := policy.Parse(policyYAML, model.DefaultProvider, model.DefaultModelName)
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
	manager := trigger.NewRunManager()
	pub := &testutil.RecordingPublisher{}
	launcher := trigger.NewRunLauncher(store, registry, manager, nil, pub, 0)

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
	insertTestPolicy(t, store, "p-tool-fail-evt", policyYAML)
	parsed, err := policy.Parse(policyYAML, model.DefaultProvider, model.DefaultModelName)
	if err != nil {
		t.Fatalf("policy.Parse: %v", err)
	}

	_, launchErr := launcher.Launch(context.Background(), trigger.LaunchParams{
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
	manager := trigger.NewRunManager()
	pub := &testutil.RecordingPublisher{}

	agentErr := errors.New("deliberate construction failure")
	launcher := trigger.NewRunLauncher(store, registry, manager, failingAgentFactory(agentErr), pub, 0)

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
	insertTestPolicy(t, store, "p-agent-fail-evt", policyYAML)
	parsed, err := policy.Parse(policyYAML, model.DefaultProvider, model.DefaultModelName)
	if err != nil {
		t.Fatalf("policy.Parse: %v", err)
	}

	_, launchErr := launcher.Launch(context.Background(), trigger.LaunchParams{
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
// schedulerFactory(), an inline factory that bypasses NewAgentFactory.
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

			factory := trigger.NewAgentFactory(reg)

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
