package trigger_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/rapp992/gleipnir/internal/agent"
	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/mcp"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/testutil"
	"github.com/rapp992/gleipnir/internal/trigger"
)

// scheduledPolicyYAML builds a minimal scheduled policy YAML with the given
// fire times. The stub-server.read_data sensor is granted so the registry can
// resolve tools without additional setup.
func scheduledPolicyYAML(name string, fireTimes []time.Time) string {
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
  sensors:
    - tool: stub-server.read_data
agent:
  model: claude-opus-4-6
  task: "do thing"
  concurrency: parallel
`, name, fireAtLines)
}

// schedulerFactory returns an AgentFactory that uses integrationFakeMessages so
// no real Claude API calls are made during scheduler tests.
func schedulerFactory() trigger.AgentFactory {
	return func(cfg agent.Config) (*agent.BoundAgent, error) {
		cfg.MessagesOverride = &integrationFakeMessages{
			responses: []*anthropic.Message{makeTextMsg("done")},
		}
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
	registry := mcp.NewRegistry(store.Queries)
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

	manager := trigger.NewRunManager()
	launcher := trigger.NewRunLauncher(store, registry, manager, schedulerFactory(), nil)
	scheduler := trigger.NewScheduler(store, launcher)

	if err := scheduler.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Give the scheduler a moment — no goroutines should fire for past times.
	time.Sleep(100 * time.Millisecond)

	runs, err := store.ListRunsByPolicy(ctx, "pol-past")
	if err != nil {
		t.Fatalf("ListRunsByPolicy: %v", err)
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

	manager := trigger.NewRunManager()
	launcher := trigger.NewRunLauncher(store, registry, manager, schedulerFactory(), nil)
	scheduler := trigger.NewScheduler(store, launcher)

	if err := scheduler.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := store.ListRunsByPolicy(ctx, "pol-future")
		if err != nil {
			t.Fatalf("ListRunsByPolicy: %v", err)
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

	manager := trigger.NewRunManager()
	launcher := trigger.NewRunLauncher(store, registry, manager, schedulerFactory(), nil)
	scheduler := trigger.NewScheduler(store, launcher)

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

	manager := trigger.NewRunManager()
	launcher := trigger.NewRunLauncher(store, registry, manager, schedulerFactory(), nil)
	scheduler := trigger.NewScheduler(store, launcher)

	if err := scheduler.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Let the timer fire and dedup logic run.
	time.Sleep(3 * time.Second)

	runs, err := store.ListRunsByPolicy(context.Background(), "pol-dedup")
	if err != nil {
		t.Fatalf("ListRunsByPolicy: %v", err)
	}
	// Only the pre-inserted run; no duplicate.
	if len(runs) != 1 {
		t.Errorf("expected 1 run (pre-inserted), got %d", len(runs))
	}
}
