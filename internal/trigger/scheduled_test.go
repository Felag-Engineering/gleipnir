package trigger_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/execution/agent"
	"github.com/felag-engineering/gleipnir/internal/execution/run"
	"github.com/felag-engineering/gleipnir/internal/llm"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/testutil"
	"github.com/felag-engineering/gleipnir/internal/trigger"
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
	resolver := newTestSettings("anthropic", "claude-sonnet-4-6")
	launcher := run.NewRunLauncher(run.RunLauncherConfig{
		Store:                  store,
		Registry:               registry,
		Manager:                manager,
		AgentFactory:           schedulerFactory(),
		Publisher:              nil,
		DefaultFeedbackTimeout: 0,
		ModelResolver:          resolver,
	})
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
	resolver := newTestSettings("anthropic", "claude-sonnet-4-6")
	launcher := run.NewRunLauncher(run.RunLauncherConfig{
		Store:                  store,
		Registry:               registry,
		Manager:                manager,
		AgentFactory:           schedulerFactory(),
		Publisher:              nil,
		DefaultFeedbackTimeout: 0,
		ModelResolver:          resolver,
	})
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
	resolver := newTestSettings("anthropic", "claude-sonnet-4-6")
	launcher := run.NewRunLauncher(run.RunLauncherConfig{
		Store:                  store,
		Registry:               registry,
		Manager:                manager,
		AgentFactory:           schedulerFactory(),
		Publisher:              nil,
		DefaultFeedbackTimeout: 0,
		ModelResolver:          resolver,
	})
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
	resolver := newTestSettings("anthropic", "claude-sonnet-4-6")
	launcher := run.NewRunLauncher(run.RunLauncherConfig{
		Store:                  store,
		Registry:               registry,
		Manager:                manager,
		AgentFactory:           schedulerFactory(),
		Publisher:              nil,
		DefaultFeedbackTimeout: 0,
		ModelResolver:          resolver,
	})
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
	resolver := newTestSettings("anthropic", "claude-sonnet-4-6")
	launcher := run.NewRunLauncher(run.RunLauncherConfig{
		Store:                  store,
		Registry:               registry,
		Manager:                manager,
		AgentFactory:           schedulerFactory(),
		Publisher:              nil,
		DefaultFeedbackTimeout: 0,
		ModelResolver:          resolver,
	})
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

// TestScheduler_ConcurrencySkip_AutoPausesWhenExhausted verifies that when the
// last fire_at time is skipped because a run is active (concurrency: skip), the
// policy still auto-pauses — the skipped time is consumed and will not retry, so
// an exhausted policy must not linger "active" forever with no future timers.
// Regression guard for #488.
func TestScheduler_ConcurrencySkip_AutoPausesWhenExhausted(t *testing.T) {
	store, registry := setupSchedulerFixture(t)

	future := time.Now().Add(2 * time.Second)
	yaml := scheduledPolicyYAMLWithConcurrency("skip-exhaust-policy", []time.Time{future}, "skip")
	insertTestScheduledPolicy(t, store, "pol-skip-exhaust", "skip-exhaust-policy", yaml)

	// Active run forces the only fire_at into the ErrConcurrencySkipActive branch.
	insertTestRun(t, store, "r-active-skip-exhaust", "pol-skip-exhaust", model.RunStatusRunning)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	manager := run.NewRunManager()
	resolver := newTestSettings("anthropic", "claude-sonnet-4-6")
	launcher := run.NewRunLauncher(run.RunLauncherConfig{
		Store:                  store,
		Registry:               registry,
		Manager:                manager,
		AgentFactory:           schedulerFactory(),
		Publisher:              nil,
		DefaultFeedbackTimeout: 0,
		ModelResolver:          resolver,
	})
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
			if p.ID == "pol-skip-exhaust" {
				found = true
				break
			}
		}
		if !found {
			return // success — policy auto-paused after the skipped fire exhausted it
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("expected policy to auto-pause after its only fire time was skipped (concurrency: skip)")
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
	resolver := newTestSettings("anthropic", "claude-sonnet-4-6")
	launcher := run.NewRunLauncher(run.RunLauncherConfig{
		Store:                  store,
		Registry:               registry,
		Manager:                manager,
		AgentFactory:           schedulerFactory(),
		Publisher:              nil,
		DefaultFeedbackTimeout: 0,
		ModelResolver:          resolver,
	})
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
	resolver := newTestSettings("anthropic", "claude-sonnet-4-6")
	launcher := run.NewRunLauncher(run.RunLauncherConfig{
		Store:                  store,
		Registry:               registry,
		Manager:                manager,
		AgentFactory:           schedulerFactory(),
		Publisher:              nil,
		DefaultFeedbackTimeout: 0,
		ModelResolver:          resolver,
	})
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
	resolver := newTestSettings("anthropic", "claude-sonnet-4-6")
	launcher := run.NewRunLauncher(run.RunLauncherConfig{
		Store:                  store,
		Registry:               registry,
		Manager:                manager,
		AgentFactory:           schedulerFactory(),
		Publisher:              nil,
		DefaultFeedbackTimeout: 0,
		ModelResolver:          resolver,
	})
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
			// Wait specifically for this run's goroutine to deregister. Calling
			// manager.Wait() here would race with RunLauncher.Launch: the DB row
			// is created before Register is called, so the poll above can observe
			// the row while Register is still pending — a concurrent wg.Add/Wait
			// is undefined per sync.WaitGroup.
			manager.WaitForDeregistration(runs[0].ID, 5*time.Second)
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
	// Resolver with no default configured — simulates unconfigured system default.
	noDefault := newTestSettings("", "")
	launcher := run.NewRunLauncher(run.RunLauncherConfig{
		Store:                  store,
		Registry:               registry,
		Manager:                manager,
		AgentFactory:           schedulerFactory(),
		Publisher:              nil,
		DefaultFeedbackTimeout: 0,
		ModelResolver:          noDefault,
	})
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

// gatedSchedulerFactory returns an AgentFactory that signals on `entered` when a
// fire() reaches agent construction (the run row already exists at this point),
// then blocks until `release` is closed. Used to hold a fire() in-flight so a
// test can assert Scheduler.Wait() drains it rather than cutting it off.
func gatedSchedulerFactory(entered chan<- struct{}, release <-chan struct{}) run.AgentFactory {
	var once sync.Once
	return func(cfg agent.Config) (*agent.BoundAgent, error) {
		once.Do(func() { close(entered) })
		<-release
		cfg.LLMClient = testutil.NewMockLLMClient(
			testutil.MakeLLMTextResponse("done", llm.StopReasonEndTurn, 10, 5),
		)
		return agent.New(cfg)
	}
}

// TestScheduler_Wait_DrainsInFlightFire verifies that Scheduler.Wait() (called
// after the root context is cancelled) blocks until an in-flight fire() finishes
// instead of returning while the fire goroutine is mid-launch. Previously the
// Scheduler had no WaitGroup, so a fire() in progress at shutdown was undrained
// and could be cut off after writing partial state (#487).
func TestScheduler_Wait_DrainsInFlightFire(t *testing.T) {
	store, registry := setupSchedulerFixture(t)

	// Near-immediate fire so the timer fires almost at once.
	future := time.Now().Add(1 * time.Second)
	yaml := scheduledPolicyYAML("drain-policy", []time.Time{future})
	insertTestScheduledPolicy(t, store, "pol-drain", "drain-policy", yaml)

	entered := make(chan struct{})
	release := make(chan struct{})

	resolver := newTestSettings("anthropic", "claude-sonnet-4-6")
	launcher := run.NewRunLauncher(run.RunLauncherConfig{
		Store:                  store,
		Registry:               registry,
		Manager:                run.NewRunManager(),
		AgentFactory:           gatedSchedulerFactory(entered, release),
		Publisher:              nil,
		DefaultFeedbackTimeout: 0,
		ModelResolver:          resolver,
	})
	scheduler := trigger.NewScheduler(store, launcher, resolver)

	// Pass a cancellable context we will cancel while fire() is held in-flight.
	ctx, cancel := context.WithCancel(context.Background())
	if err := scheduler.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wait until fire() has reached agent construction (it is now in-flight,
	// holding the wg counter above zero).
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("fire() never reached the agent factory")
	}

	// Cancel the root context now — the fire goroutine is past its ctx check and
	// must run to completion regardless.
	cancel()

	waitReturned := make(chan struct{})
	go func() { scheduler.Wait(); close(waitReturned) }()

	// Wait() must NOT return while fire() is still blocked in the factory.
	select {
	case <-waitReturned:
		t.Fatal("Wait() returned while fire() was still in-flight — goroutine not tracked by wg")
	case <-time.After(200 * time.Millisecond):
		// Good: still draining.
	}

	// Release the gate; fire() completes, the goroutine returns, Wait() unblocks.
	close(release)

	select {
	case <-waitReturned:
		// Drained cleanly.
	case <-time.After(5 * time.Second):
		t.Fatal("Wait() did not return after the in-flight fire() completed")
	}

	// The run row created by the drained fire() must exist (proof fire() finished).
	runs, err := store.ListRuns(context.Background(), db.ListRunsParams{PolicyID: "pol-drain", Limit: 10})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) == 0 {
		t.Error("expected the drained fire() to have created a run row")
	}
}

// TestScheduler_Stop_SafeWithoutStart verifies that Stop() and Wait() are no-ops
// (no panic, no hang) when Start was never called — rootCancel is nil and the
// WaitGroup counter is zero (#487).
func TestScheduler_Stop_SafeWithoutStart(t *testing.T) {
	scheduler := trigger.NewScheduler(nil, nil, nil)

	done := make(chan struct{})
	go func() {
		scheduler.Stop()
		scheduler.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop()/Wait() hung when Start was never called")
	}
}
