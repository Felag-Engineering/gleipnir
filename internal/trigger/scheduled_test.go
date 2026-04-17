package trigger_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
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

// scheduledPolicyYAML builds a minimal scheduled policy YAML with the given
// fire times. The stub-server.read_data tool is granted so the registry can
// resolve tools without additional setup.
func scheduledPolicyYAML(name string, fireTimes []time.Time) string {
	return scheduledPolicyYAMLWithConcurrency(name, fireTimes, "parallel")
}

// scheduledPolicyYAMLWithConcurrency is like scheduledPolicyYAML but allows
// the caller to specify the concurrency mode.
func scheduledPolicyYAMLWithConcurrency(name string, fireTimes []time.Time, concurrency string) string {
	fireAtLines := ""
	for _, t := range fireTimes {
		fireAtLines += fmt.Sprintf("    - %q\n", t.UTC().Format(time.RFC3339))
	}
	return fmt.Sprintf(`
name: %s
trigger:
  type: scheduled
  fire_at:
%scapabilities:
  tools:
    - tool: stub-server.read_data
agent:
  model: claude-opus-4-6
  task: "do thing"
  concurrency: %s
`, name, fireAtLines, concurrency)
}

// schedulerFactory returns an AgentFactory that uses a mock LLM client so
// no real Claude API calls are made during scheduler tests.
func schedulerFactory() run.AgentFactory {
	return func(cfg agent.Config) (*agent.BoundAgent, error) {
		cfg.LLMClient = testutil.NewMockLLMClient(
			testutil.MakeLLMTextResponse("done", llm.StopReasonEndTurn, 10, 5),
		)
		return agent.New(cfg)
	}
}

// setupSchedulerFixture opens a temp SQLite store and registers a stub MCP
// server as "stub-server". Follows the same pattern as setupIntegrationFixture.
func setupSchedulerFixture(t *testing.T) (*db.Store, *mcp.Registry) {
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

// insertTestScheduledPolicy creates a scheduled policy row in the DB.
func insertTestScheduledPolicy(t *testing.T, store *db.Store, policyID, name, yaml string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := store.CreatePolicy(context.Background(), db.CreatePolicyParams{
		ID:          policyID,
		Name:        name,
		TriggerType: "scheduled",
		Yaml:        yaml,
		CreatedAt:   now,
		UpdatedAt:   now,
	})
	if err != nil {
		t.Fatalf("insertTestScheduledPolicy %s: %v", policyID, err)
	}
}

// insertFiredRun inserts a run that claims a given scheduled_for time, simulating
// an already-fired timestamp for dedup testing.
func insertFiredRun(t *testing.T, store *db.Store, policyID string, scheduledFor time.Time) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	payload, _ := json.Marshal(map[string]string{
		"scheduled_for": scheduledFor.UTC().Format(time.RFC3339),
	})
	_, err := store.DB().Exec(
		`INSERT INTO runs(id, policy_id, status, trigger_type, trigger_payload, started_at, created_at)
		 VALUES (?, ?, 'complete', 'scheduled', ?, ?, ?)`,
		model.NewULID(), policyID, string(payload), now, now,
	)
	if err != nil {
		t.Fatalf("insertFiredRun: %v", err)
	}
}

// TestScheduler_SkipsPastTimestampsOnStartup verifies that fire_at times
// already elapsed at startup do not create new runs.
func TestScheduler_SkipsPastTimestampsOnStartup(t *testing.T) {
	store, registry := setupSchedulerFixture(t)

	past := time.Now().Add(-2 * time.Hour)
	yaml := scheduledPolicyYAML("past-policy", []time.Time{past})
	insertTestScheduledPolicy(t, store, "pol-past", "past-policy", yaml)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	manager := run.NewRunManager()
	resolver := stubDefaultModelResolver{provider: "anthropic", name: "claude-sonnet-4-6"}
	launcher := run.NewRunLauncher(store, registry, manager, schedulerFactory(), nil, 0, resolver)
	scheduler := trigger.NewScheduler(store, launcher, resolver)

	if err := scheduler.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Give the scheduler a moment — no goroutines should fire for past times.
	time.Sleep(100 * time.Millisecond)

	runs, err := store.ListRuns(ctx, db.ListRunsParams{PolicyID: "pol-past", Limit: 100})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Errorf("expected 0 runs for past-only scheduled policy, got %d", len(runs))
	}
}

// TestScheduler_FiresFutureTimestamp verifies that a near-future fire time
// eventually creates a run.
func TestScheduler_FiresFutureTimestamp(t *testing.T) {
	store, registry := setupSchedulerFixture(t)

	// Compute future time AFTER setup to avoid the fire time passing during
	// test initialization (DB writes, HTTP server start, etc.).
	future := time.Now().Add(2 * time.Second)
	yaml := scheduledPolicyYAML("future-policy", []time.Time{future})
	insertTestScheduledPolicy(t, store, "pol-future", "future-policy", yaml)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	manager := run.NewRunManager()
	resolver := stubDefaultModelResolver{provider: "anthropic", name: "claude-sonnet-4-6"}
	launcher := run.NewRunLauncher(store, registry, manager, schedulerFactory(), nil, 0, resolver)
	scheduler := trigger.NewScheduler(store, launcher, resolver)

	if err := scheduler.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := store.ListRuns(ctx, db.ListRunsParams{PolicyID: "pol-future", Limit: 100})
		if err != nil {
			t.Fatalf("ListRuns: %v", err)
		}
		if len(runs) > 0 {
			return // success
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("expected a run to be created for future scheduled time, but none appeared")
}

// TestScheduler_AutoPausesAfterAllTimesConsumed verifies that the policy is
// removed from the active scheduled list after its only fire time fires.
func TestScheduler_AutoPausesAfterAllTimesConsumed(t *testing.T) {
	store, registry := setupSchedulerFixture(t)

	// Compute future time AFTER setup so it doesn't expire during initialization.
	future := time.Now().Add(2 * time.Second)
	yaml := scheduledPolicyYAML("exhausted-policy", []time.Time{future})
	insertTestScheduledPolicy(t, store, "pol-exhaust", "exhausted-policy", yaml)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	manager := run.NewRunManager()
	resolver := stubDefaultModelResolver{provider: "anthropic", name: "claude-sonnet-4-6"}
	launcher := run.NewRunLauncher(store, registry, manager, schedulerFactory(), nil, 0, resolver)
	scheduler := trigger.NewScheduler(store, launcher, resolver)

	if err := scheduler.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wait for the policy to be paused (removed from active scheduled policies).
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		policies, err := store.GetScheduledActivePolicies(ctx)
		if err != nil {
			t.Fatalf("GetScheduledActivePolicies: %v", err)
		}
		found := false
		for _, p := range policies {
			if p.ID == "pol-exhaust" {
				found = true
				break
			}
		}
		if !found {
			return // success — policy is no longer in the active list
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("expected policy to be paused after all fire times consumed")
}

// TestScheduler_DeduplicatesAlreadyFiredTime verifies that if a run already
// exists for a scheduled_for timestamp, no duplicate run is created.
func TestScheduler_DeduplicatesAlreadyFiredTime(t *testing.T) {
	store, registry := setupSchedulerFixture(t)

	// Compute future time AFTER setup to ensure it hasn't passed yet.
	future := time.Now().Add(2 * time.Second)
	yaml := scheduledPolicyYAML("dedup-policy", []time.Time{future})
	insertTestScheduledPolicy(t, store, "pol-dedup", "dedup-policy", yaml)
	// Pre-insert a run claiming this exact fire time.
	insertFiredRun(t, store, "pol-dedup", future)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	manager := run.NewRunManager()
	resolver := stubDefaultModelResolver{provider: "anthropic", name: "claude-sonnet-4-6"}
	launcher := run.NewRunLauncher(store, registry, manager, schedulerFactory(), nil, 0, resolver)
	scheduler := trigger.NewScheduler(store, launcher, resolver)

	if err := scheduler.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Let the timer fire and dedup logic run.
	time.Sleep(3 * time.Second)

	runs, err := store.ListRuns(context.Background(), db.ListRunsParams{PolicyID: "pol-dedup", Limit: 100})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	// Only the pre-inserted run; no duplicate.
	if len(runs) != 1 {
		t.Errorf("expected 1 run (pre-inserted), got %d", len(runs))
	}
}

// TestScheduler_ConcurrencySkip_BlocksWhenActive verifies that a scheduled
// trigger with concurrency: skip does NOT launch a new run when an active
// run already exists for the policy.
func TestScheduler_ConcurrencySkip_BlocksWhenActive(t *testing.T) {
	store, registry := setupSchedulerFixture(t)

	future := time.Now().Add(2 * time.Second)
	yaml := scheduledPolicyYAMLWithConcurrency("skip-policy", []time.Time{future}, "skip")
	insertTestScheduledPolicy(t, store, "pol-skip", "skip-policy", yaml)

	// Insert an active (running) run so the concurrency check blocks the new one.
	insertTestRun(t, store, "r-active-sched", "pol-skip", model.RunStatusRunning)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	manager := run.NewRunManager()
	resolver := stubDefaultModelResolver{provider: "anthropic", name: "claude-sonnet-4-6"}
	launcher := run.NewRunLauncher(store, registry, manager, schedulerFactory(), nil, 0, resolver)
	scheduler := trigger.NewScheduler(store, launcher, resolver)

	if err := scheduler.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wait for the timer to fire and the concurrency check to block it.
	time.Sleep(4 * time.Second)

	runs, err := store.ListRuns(context.Background(), db.ListRunsParams{PolicyID: "pol-skip", Limit: 100})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	// Only the pre-inserted active run should exist; no new run created.
	if len(runs) != 1 {
		t.Errorf("expected 1 run (pre-existing active), got %d", len(runs))
	}
}

// TestScheduler_ConcurrencySkip_ProceedsWhenIdle verifies that a scheduled
// trigger with concurrency: skip proceeds normally when no active run exists.
func TestScheduler_ConcurrencySkip_ProceedsWhenIdle(t *testing.T) {
	store, registry := setupSchedulerFixture(t)

	future := time.Now().Add(2 * time.Second)
	yaml := scheduledPolicyYAMLWithConcurrency("skip-idle-policy", []time.Time{future}, "skip")
	insertTestScheduledPolicy(t, store, "pol-skip-idle", "skip-idle-policy", yaml)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	manager := run.NewRunManager()
	resolver := stubDefaultModelResolver{provider: "anthropic", name: "claude-sonnet-4-6"}
	launcher := run.NewRunLauncher(store, registry, manager, schedulerFactory(), nil, 0, resolver)
	scheduler := trigger.NewScheduler(store, launcher, resolver)

	if err := scheduler.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wait for the timer to fire and the run to be created.
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := store.ListRuns(ctx, db.ListRunsParams{PolicyID: "pol-skip-idle", Limit: 100})
		if err != nil {
			t.Fatalf("ListRuns: %v", err)
		}
		if len(runs) > 0 {
			return // success — run was created
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("expected a run to be created for skip policy with no active runs, but none appeared")
}

// TestScheduler_ConcurrencyQueue_EnqueuesWhenActive verifies that a scheduled
// trigger with concurrency: queue enqueues the trigger when an active run exists.
func TestScheduler_ConcurrencyQueue_EnqueuesWhenActive(t *testing.T) {
	store, registry := setupSchedulerFixture(t)

	future := time.Now().Add(2 * time.Second)
	yaml := scheduledPolicyYAMLWithConcurrency("queue-active-policy", []time.Time{future}, "queue")
	insertTestScheduledPolicy(t, store, "pol-queue-active", "queue-active-policy", yaml)

	// Insert an active (running) run so the concurrency check triggers enqueue.
	insertTestRun(t, store, "r-active-queue", "pol-queue-active", model.RunStatusRunning)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	manager := run.NewRunManager()
	resolver := stubDefaultModelResolver{provider: "anthropic", name: "claude-sonnet-4-6"}
	launcher := run.NewRunLauncher(store, registry, manager, schedulerFactory(), nil, 0, resolver)
	scheduler := trigger.NewScheduler(store, launcher, resolver)

	if err := scheduler.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wait for the timer to fire and the trigger to be enqueued.
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		count, err := store.CountQueuedTriggers(context.Background(), "pol-queue-active")
		if err != nil {
			t.Fatalf("CountQueuedTriggers: %v", err)
		}
		if count > 0 {
			// Verify no new run was created (only the pre-existing active run).
			runs, err := store.ListRuns(context.Background(), db.ListRunsParams{PolicyID: "pol-queue-active", Limit: 100})
			if err != nil {
				t.Fatalf("ListRuns: %v", err)
			}
			if len(runs) != 1 {
				t.Errorf("expected 1 run (pre-existing active), got %d", len(runs))
			}
			return // success — trigger was enqueued
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("expected trigger to be enqueued for queue policy with active run, but queue remained empty")
}

// TestScheduler_ConcurrencyQueue_LaunchesWhenIdle verifies that a scheduled
// trigger with concurrency: queue fires a run when no active run exists.
func TestScheduler_ConcurrencyQueue_LaunchesWhenIdle(t *testing.T) {
	store, registry := setupSchedulerFixture(t)

	future := time.Now().Add(2 * time.Second)
	yaml := scheduledPolicyYAMLWithConcurrency("queue-policy", []time.Time{future}, "queue")
	insertTestScheduledPolicy(t, store, "pol-queue", "queue-policy", yaml)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	manager := run.NewRunManager()
	resolver := stubDefaultModelResolver{provider: "anthropic", name: "claude-sonnet-4-6"}
	launcher := run.NewRunLauncher(store, registry, manager, schedulerFactory(), nil, 0, resolver)
	scheduler := trigger.NewScheduler(store, launcher, resolver)

	if err := scheduler.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wait for the timer to fire and the run to be created.
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := store.ListRuns(context.Background(), db.ListRunsParams{PolicyID: "pol-queue", Limit: 100})
		if err != nil {
			t.Fatalf("ListRuns: %v", err)
		}
		if len(runs) > 0 {
			manager.Wait()
			return // success — run was created and launched
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("expected a run to be created for queue policy with no active runs, but none appeared")
}

// scheduledPolicyYAMLNoModel builds a minimal scheduled policy with no model
// block in the agent section. Used to test the empty-system-default code path.
func scheduledPolicyYAMLNoModel(name string, fireTimes []time.Time) string {
	fireAtLines := ""
	for _, t := range fireTimes {
		fireAtLines += fmt.Sprintf("    - %q\n", t.UTC().Format(time.RFC3339))
	}
	return fmt.Sprintf(`
name: %s
trigger:
  type: scheduled
  fire_at:
%scapabilities:
  tools:
    - tool: stub-server.read_data
agent:
  task: "do thing"
  concurrency: parallel
`, name, fireAtLines)
}

// TestScheduler_SkipsPolicy_WhenNoSystemDefaultAndNoModelInYAML verifies that
// a scheduled policy whose YAML omits the model block is not scheduled when the
// system default is also unset (sql.ErrNoRows). The policy silently skips
// rather than arming timers that would fire an invalid run.
func TestScheduler_SkipsPolicy_WhenNoSystemDefaultAndNoModelInYAML(t *testing.T) {
	store, registry := setupSchedulerFixture(t)

	future := time.Now().Add(2 * time.Second)
	yaml := scheduledPolicyYAMLNoModel("no-model-policy", []time.Time{future})
	insertTestScheduledPolicy(t, store, "pol-no-model", "no-model-policy", yaml)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	manager := run.NewRunManager()
	// Resolver that returns sql.ErrNoRows — simulates unconfigured system default.
	noDefault := stubDefaultModelResolver{err: sql.ErrNoRows}
	launcher := run.NewRunLauncher(store, registry, manager, schedulerFactory(), nil, 0, noDefault)
	scheduler := trigger.NewScheduler(store, launcher, noDefault)

	if err := scheduler.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Give the scheduler time to process startup and the future timer (it should not arm).
	time.Sleep(500 * time.Millisecond)

	runs, err := store.ListRuns(context.Background(), db.ListRunsParams{PolicyID: "pol-no-model", Limit: 100})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Errorf("expected 0 runs when system default is unset and policy omits model, got %d", len(runs))
	}
}
