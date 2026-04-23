package trigger

// Internal-package tests for CronRunner.
// Using package trigger (not trigger_test) so we can inspect c.loops directly
// without exporting them, matching the pattern in notify_test.go.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/rapp992/gleipnir/internal/agent"
	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/llm"
	"github.com/rapp992/gleipnir/internal/mcp"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/policy"
	"github.com/rapp992/gleipnir/internal/run"
	"github.com/rapp992/gleipnir/internal/testutil"
)

// cronAgentFactory returns an AgentFactory backed by a mock LLM client.
func cronAgentFactory() run.AgentFactory {
	return func(cfg agent.Config) (*agent.BoundAgent, error) {
		cfg.LLMClient = testutil.NewMockLLMClient(
			testutil.MakeLLMTextResponse("done", llm.StopReasonEndTurn, 10, 5),
		)
		return agent.New(cfg)
	}
}

// setupCronFixture creates a CronRunner wired with a store but does NOT call
// Start — tests call Notify or fire() directly.
func setupCronFixture(t *testing.T) (*db.Store, *CronRunner) {
	t.Helper()
	store := testutil.NewTestStore(t)
	registry := mcp.NewRegistry(store.Queries())
	manager := run.NewRunManager()
	resolver := stubDefaultModelResolver{provider: "anthropic", name: "claude-sonnet-4-6"}
	launcher := run.NewRunLauncher(store, registry, manager, cronAgentFactory(), nil, 0, resolver)
	runner := NewCronRunner(store, launcher, resolver)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	runner.mu.Lock()
	runner.rootCtx = ctx
	runner.mu.Unlock()

	return store, runner
}

// hasCronLoop reports whether a cron loop exists for policyID.
func hasCronLoop(c *CronRunner, policyID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.loops[policyID]
	return ok
}

// cronLoopCount returns the number of active cron loops.
func cronLoopCount(c *CronRunner) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.loops)
}

// minCronYAML returns a minimal cron policy YAML for the given name and expression.
func minCronYAML(name, expr string) string {
	return minCronYAMLWithConcurrency(name, expr, "parallel")
}

// minCronYAMLWithConcurrency is like minCronYAML but allows specifying the
// concurrency mode. Used by concurrency-specific fire() tests.
func minCronYAMLWithConcurrency(name, expr, concurrency string) string {
	return "name: " + name + `
trigger:
  type: cron
  cron_expr: "` + expr + `"
capabilities:
  tools:
    - tool: stub-server.read_data
agent:
  task: "cron task"
  concurrency: ` + concurrency + `
`
}

// insertCronPolicy inserts a cron policy row in the store.
func insertCronPolicy(t *testing.T, store *db.Store, id, name, yaml string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.CreatePolicy(context.Background(), db.CreatePolicyParams{
		ID:          id,
		Name:        name,
		TriggerType: "cron",
		Yaml:        yaml,
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("insertCronPolicy %s: %v", id, err)
	}
}

func TestCronRunner_Notify(t *testing.T) {
	t.Run("new cron policy: Notify starts a loop", func(t *testing.T) {
		store, runner := setupCronFixture(t)
		yaml := minCronYAML("cron-new", "0 9 * * 1")
		insertCronPolicy(t, store, "pol-cron-new", "cron-new", yaml)

		runner.Notify(context.Background(), "pol-cron-new")

		if !hasCronLoop(runner, "pol-cron-new") {
			t.Error("expected a loop to be started for new cron policy")
		}
	})

	t.Run("updated cron policy: Notify restarts the loop", func(t *testing.T) {
		store, runner := setupCronFixture(t)
		yaml := minCronYAML("cron-update", "0 9 * * 1")
		insertCronPolicy(t, store, "pol-cron-update", "cron-update", yaml)

		runner.Notify(context.Background(), "pol-cron-update")
		if !hasCronLoop(runner, "pol-cron-update") {
			t.Fatal("setup: loop not started")
		}

		// Notify again simulates a YAML update (e.g. changed cron_expr).
		runner.Notify(context.Background(), "pol-cron-update")

		if !hasCronLoop(runner, "pol-cron-update") {
			t.Error("expected loop to be present after second Notify (restart)")
		}
		if cronLoopCount(runner) != 1 {
			t.Errorf("expected 1 loop after update, got %d", cronLoopCount(runner))
		}
	})

	t.Run("paused cron policy: Notify cancels the loop", func(t *testing.T) {
		store, runner := setupCronFixture(t)
		yaml := minCronYAML("cron-pause", "0 9 * * 1")
		insertCronPolicy(t, store, "pol-cron-pause", "cron-pause", yaml)

		runner.Notify(context.Background(), "pol-cron-pause")
		if !hasCronLoop(runner, "pol-cron-pause") {
			t.Fatal("setup: loop not started")
		}

		now := time.Now().UTC().Format(time.RFC3339Nano)
		if err := store.SetPolicyPausedAt(context.Background(), db.SetPolicyPausedAtParams{
			PausedAt: &now,
			ID:       "pol-cron-pause",
		}); err != nil {
			t.Fatalf("SetPolicyPausedAt: %v", err)
		}

		runner.Notify(context.Background(), "pol-cron-pause")

		if hasCronLoop(runner, "pol-cron-pause") {
			t.Error("expected loop to be cancelled after policy paused")
		}
	})

	t.Run("deleted policy: Notify cancels the loop without error", func(t *testing.T) {
		store, runner := setupCronFixture(t)
		yaml := minCronYAML("cron-delete", "0 9 * * 1")
		insertCronPolicy(t, store, "pol-cron-delete", "cron-delete", yaml)

		runner.Notify(context.Background(), "pol-cron-delete")
		if !hasCronLoop(runner, "pol-cron-delete") {
			t.Fatal("setup: loop not started")
		}

		if err := store.DeletePolicy(context.Background(), "pol-cron-delete"); err != nil {
			t.Fatalf("DeletePolicy: %v", err)
		}

		runner.Notify(context.Background(), "pol-cron-delete")

		if hasCronLoop(runner, "pol-cron-delete") {
			t.Error("expected loop to be cancelled after policy deleted")
		}
	})

	t.Run("non-cron trigger type: Notify is a no-op", func(t *testing.T) {
		store, runner := setupCronFixture(t)
		insertNotifyWebhookPolicy(t, store, "pol-cron-webhook", "cron-webhook")

		runner.Notify(context.Background(), "pol-cron-webhook")

		if hasCronLoop(runner, "pol-cron-webhook") {
			t.Error("expected no loop for non-cron trigger type")
		}
	})

	t.Run("invalid cron expression: Notify does not start a loop", func(t *testing.T) {
		store, runner := setupCronFixture(t)
		yaml := minCronYAML("cron-invalid", "not-a-cron-expression")
		insertCronPolicy(t, store, "pol-cron-invalid", "cron-invalid", yaml)

		runner.Notify(context.Background(), "pol-cron-invalid")

		if hasCronLoop(runner, "pol-cron-invalid") {
			t.Error("expected no loop for policy with invalid cron expression")
		}
	})
}

func TestCronRunner_Start_LoadsActivePolicies(t *testing.T) {
	store := testutil.NewTestStore(t)
	registry := mcp.NewRegistry(store.Queries())
	manager := run.NewRunManager()
	resolver := stubDefaultModelResolver{provider: "anthropic", name: "claude-sonnet-4-6"}
	launcher := run.NewRunLauncher(store, registry, manager, cronAgentFactory(), nil, 0, resolver)
	runner := NewCronRunner(store, launcher, resolver)

	yaml := minCronYAML("cron-start", "0 9 * * 1")
	insertCronPolicy(t, store, "pol-cron-start", "cron-start", yaml)

	ctx, cancel := context.WithCancel(context.Background())

	if err := runner.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if !hasCronLoop(runner, "pol-cron-start") {
		t.Error("expected a loop to be started for existing cron policy on Start")
	}

	// Cancel the root context first so all goroutines (including the reconcile
	// loop) exit cleanly, then wait for them to finish.
	cancel()
	runner.Wait()
}

func TestCronRunner_Fire_PayloadShape(t *testing.T) {
	store, runner := setupCronFixture(t)
	yaml := minCronYAML("cron-fire", "0 9 * * 1")
	insertCronPolicy(t, store, "pol-cron-fire", "cron-fire", yaml)

	resolver := stubDefaultModelResolver{provider: "anthropic", name: "claude-sonnet-4-6"}
	provider, modelName, _ := resolver.GetSystemDefault(context.Background())

	polRow, err := store.GetPolicy(context.Background(), "pol-cron-fire")
	if err != nil {
		t.Fatalf("GetPolicy: %v", err)
	}

	parsed, err := policy.Parse(polRow.Yaml, provider, modelName)
	if err != nil {
		t.Fatalf("policy.Parse: %v", err)
	}

	firedAt := time.Date(2024, 1, 1, 9, 0, 0, 0, time.UTC)
	runner.fire(context.Background(), "pol-cron-fire", parsed, firedAt)

	// The run may transition to failed immediately (no MCP tool registered in
	// this unit test store), but we can confirm it was created via CountRuns.
	count, err := store.Queries().CountRuns(context.Background(), db.CountRunsParams{
		PolicyID: "pol-cron-fire",
	})
	if err != nil {
		t.Fatalf("CountRuns: %v", err)
	}
	if count == 0 {
		t.Fatal("expected at least one run to be created by fire()")
	}

	// Retrieve the run via ListRunsAsc to check trigger_type and payload.
	runs, err := store.Queries().ListRunsAsc(context.Background(), db.ListRunsAscParams{
		PolicyID: "pol-cron-fire",
		Limit:    1,
	})
	if err != nil {
		t.Fatalf("ListRunsAsc: %v", err)
	}
	if len(runs) == 0 {
		t.Fatal("expected runs from ListRunsAsc")
	}

	r := runs[0]
	if r.TriggerType != "cron" {
		t.Errorf("run.trigger_type = %q, want %q", r.TriggerType, "cron")
	}

	var payload map[string]string
	if err := json.Unmarshal([]byte(r.TriggerPayload), &payload); err != nil {
		t.Fatalf("unmarshal trigger_payload: %v", err)
	}
	if payload["cron_fired_at"] == "" {
		t.Error("trigger_payload.cron_fired_at is empty")
	}
	if payload["expression"] != "0 9 * * 1" {
		t.Errorf("trigger_payload.expression = %q, want %q", payload["expression"], "0 9 * * 1")
	}
}

// TestCronRunner_Fire_ConcurrencySkip verifies that fire() does not create a
// run when the policy uses concurrency: skip and an active run already exists.
func TestCronRunner_Fire_ConcurrencySkip(t *testing.T) {
	store, runner := setupCronFixture(t)
	yaml := minCronYAMLWithConcurrency("cron-skip", "0 9 * * 1", "skip")
	insertCronPolicy(t, store, "pol-cron-skip", "cron-skip", yaml)

	// Insert a running run so CheckConcurrency returns ErrConcurrencySkipActive.
	testutil.InsertRun(t, store, "r-cron-skip-active", "pol-cron-skip", model.RunStatusRunning)

	resolver := stubDefaultModelResolver{provider: "anthropic", name: "claude-sonnet-4-6"}
	provider, modelName, _ := resolver.GetSystemDefault(context.Background())

	polRow, err := store.GetPolicy(context.Background(), "pol-cron-skip")
	if err != nil {
		t.Fatalf("GetPolicy: %v", err)
	}
	parsed, err := policy.Parse(polRow.Yaml, provider, modelName)
	if err != nil {
		t.Fatalf("policy.Parse: %v", err)
	}

	firedAt := time.Date(2024, 1, 1, 9, 0, 0, 0, time.UTC)
	runner.fire(context.Background(), "pol-cron-skip", parsed, firedAt)

	// Only the pre-existing running run should exist; fire() must not create a new one.
	count, err := store.Queries().CountRuns(context.Background(), db.CountRunsParams{
		PolicyID: "pol-cron-skip",
	})
	if err != nil {
		t.Fatalf("CountRuns: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 run (pre-existing active), got %d", count)
	}
}

// TestCronRunner_Fire_ConcurrencyQueue verifies that fire() enqueues a trigger
// entry (rather than launching a run) when concurrency: queue is set and an
// active run already exists.
func TestCronRunner_Fire_ConcurrencyQueue(t *testing.T) {
	store, runner := setupCronFixture(t)
	yaml := minCronYAMLWithConcurrency("cron-queue", "0 9 * * 1", "queue")
	insertCronPolicy(t, store, "pol-cron-queue", "cron-queue", yaml)

	// Insert a running run so CheckConcurrency returns ErrConcurrencyQueueActive.
	testutil.InsertRun(t, store, "r-cron-queue-active", "pol-cron-queue", model.RunStatusRunning)

	resolver := stubDefaultModelResolver{provider: "anthropic", name: "claude-sonnet-4-6"}
	provider, modelName, _ := resolver.GetSystemDefault(context.Background())

	polRow, err := store.GetPolicy(context.Background(), "pol-cron-queue")
	if err != nil {
		t.Fatalf("GetPolicy: %v", err)
	}
	parsed, err := policy.Parse(polRow.Yaml, provider, modelName)
	if err != nil {
		t.Fatalf("policy.Parse: %v", err)
	}

	firedAt := time.Date(2024, 1, 1, 9, 0, 0, 0, time.UTC)
	runner.fire(context.Background(), "pol-cron-queue", parsed, firedAt)

	// A trigger queue entry should have been created instead of a new run.
	queued, err := store.Queries().CountQueuedTriggers(context.Background(), "pol-cron-queue")
	if err != nil {
		t.Fatalf("CountQueuedTriggers: %v", err)
	}
	if queued == 0 {
		t.Error("expected a trigger queue entry to be created by fire() for queue concurrency")
	}

	// No additional run should have been created (only the pre-existing one).
	count, err := store.Queries().CountRuns(context.Background(), db.CountRunsParams{
		PolicyID: "pol-cron-queue",
	})
	if err != nil {
		t.Fatalf("CountRuns: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 run (pre-existing active), got %d", count)
	}
}

// TestCronRunner_Notify_TypeSwitch covers two transitions:
//  1. A webhook policy updated to trigger_type=cron: Notify should start a loop.
//  2. A running cron loop whose policy is updated to trigger_type=webhook: Notify
//     should cancel the loop (no entry left in loops map).
func TestCronRunner_Notify_TypeSwitch(t *testing.T) {
	t.Run("webhook→cron: Notify starts a loop", func(t *testing.T) {
		store, runner := setupCronFixture(t)

		// Insert as webhook initially.
		insertNotifyWebhookPolicy(t, store, "pol-type-switch-to-cron", "type-switch-to-cron")

		// Notify while it's still a webhook — no loop expected.
		runner.Notify(context.Background(), "pol-type-switch-to-cron")
		if hasCronLoop(runner, "pol-type-switch-to-cron") {
			t.Fatal("setup: unexpected loop for webhook policy")
		}

		// Update DB to cron trigger type.
		cronYAML := minCronYAML("type-switch-to-cron", "0 9 * * 1")
		if _, err := store.UpdatePolicy(context.Background(), db.UpdatePolicyParams{
			ID:          "pol-type-switch-to-cron",
			Name:        "type-switch-to-cron",
			TriggerType: "cron",
			Yaml:        cronYAML,
			UpdatedAt:   time.Now().UTC().Format(time.RFC3339Nano),
		}); err != nil {
			t.Fatalf("UpdatePolicy: %v", err)
		}

		runner.Notify(context.Background(), "pol-type-switch-to-cron")

		if !hasCronLoop(runner, "pol-type-switch-to-cron") {
			t.Error("expected a loop to be started after policy switched to cron trigger type")
		}
	})

	t.Run("cron→webhook: Notify cancels the loop", func(t *testing.T) {
		store, runner := setupCronFixture(t)

		// Insert as cron and start its loop.
		cronYAML := minCronYAML("type-switch-to-webhook", "0 9 * * 1")
		insertCronPolicy(t, store, "pol-type-switch-to-webhook", "type-switch-to-webhook", cronYAML)

		runner.Notify(context.Background(), "pol-type-switch-to-webhook")
		if !hasCronLoop(runner, "pol-type-switch-to-webhook") {
			t.Fatal("setup: loop not started")
		}

		// Update DB to webhook trigger type.
		webhookYAML := "name: type-switch-to-webhook\ntrigger:\n  type: webhook\nagent:\n  task: t\n"
		if _, err := store.UpdatePolicy(context.Background(), db.UpdatePolicyParams{
			ID:          "pol-type-switch-to-webhook",
			Name:        "type-switch-to-webhook",
			TriggerType: "webhook",
			Yaml:        webhookYAML,
			UpdatedAt:   time.Now().UTC().Format(time.RFC3339Nano),
		}); err != nil {
			t.Fatalf("UpdatePolicy: %v", err)
		}

		runner.Notify(context.Background(), "pol-type-switch-to-webhook")

		if hasCronLoop(runner, "pol-type-switch-to-webhook") {
			t.Error("expected loop to be cancelled after policy switched away from cron trigger type")
		}
	})
}

// TestCronRunner_Stop_DoesNotDeadlock verifies that Stop() exits cleanly,
// including the reconcile goroutine (which previously was not cancelled by Stop).
func TestCronRunner_Stop_DoesNotDeadlock(t *testing.T) {
	store := testutil.NewTestStore(t)
	registry := mcp.NewRegistry(store.Queries())
	manager := run.NewRunManager()
	resolver := stubDefaultModelResolver{provider: "anthropic", name: "claude-sonnet-4-6"}
	launcher := run.NewRunLauncher(store, registry, manager, cronAgentFactory(), nil, 0, resolver)
	runner := NewCronRunner(store, launcher, resolver)

	yaml := minCronYAML("cron-stop", "0 9 * * 1")
	insertCronPolicy(t, store, "pol-cron-stop", "cron-stop", yaml)

	if err := runner.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !hasCronLoop(runner, "pol-cron-stop") {
		t.Fatal("expected loop to be running after Start")
	}

	done := make(chan struct{})
	go func() { runner.Stop(); close(done) }()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() deadlocked — reconcile goroutine was not cancelled")
	}
}
