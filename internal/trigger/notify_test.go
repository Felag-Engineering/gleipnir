package trigger

// Internal-package tests for Poller.Notify and Scheduler.Notify.
// Using package trigger (not trigger_test) so we can inspect p.loops and
// s.timers directly without exporting them.

import (
	"context"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/execution/agent"
	"github.com/felag-engineering/gleipnir/internal/execution/run"
	"github.com/felag-engineering/gleipnir/internal/llm"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

// ---- Poller.Notify tests ---------------------------------------------------

// notifyPollerFactory returns an AgentFactory backed by a mock LLM client.
func notifyPollerFactory() run.AgentFactory {
	return func(cfg agent.Config) (*agent.BoundAgent, error) {
		cfg.LLMClient = testutil.NewMockLLMClient(
			testutil.MakeLLMTextResponse("done", llm.StopReasonEndTurn, 10, 5),
		)
		return agent.New(cfg)
	}
}

// setupNotifyPollerFixture creates a Poller wired with an MCP server but does
// NOT call Start — tests call Notify directly.
func setupNotifyPollerFixture(t *testing.T) (*db.Store, *Poller) {
	t.Helper()
	store := testutil.NewTestStore(t)
	registry := mcp.NewRegistry(store.Queries())
	manager := run.NewRunManager()
	resolver := stubDefaultModelResolver{provider: "anthropic", name: "claude-sonnet-4-6"}
	launcher := run.NewRunLauncher(run.RunLauncherConfig{
		Store:                  store,
		Registry:               registry,
		Manager:                manager,
		AgentFactory:           notifyPollerFactory(),
		Publisher:              nil,
		DefaultFeedbackTimeout: 0,
		ModelResolver:          resolver,
	})
	poller := NewPoller(store, launcher, registry, resolver)

	// Set a root context so Notify can start loops.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	poller.mu.Lock()
	poller.rootCtx = ctx
	poller.mu.Unlock()

	return store, poller
}

// hasLoop reports whether a loop exists for policyID.
func hasLoop(p *Poller, policyID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.loops[policyID]
	return ok
}

// loopCount returns the number of active poll loops.
func loopCount(p *Poller) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.loops)
}

func TestPoller_Notify(t *testing.T) {
	t.Run("new poll policy: Notify starts a loop", func(t *testing.T) {
		store, poller := setupNotifyPollerFixture(t)

		yaml := minPollYAML("notify-new")
		insertNotifyPollPolicy(t, store, "pol-notify-new", "notify-new", yaml)

		poller.Notify(context.Background(), "pol-notify-new")

		if !hasLoop(poller, "pol-notify-new") {
			t.Error("expected a loop to be started for new poll policy")
		}
	})

	t.Run("updated poll policy: Notify restarts the loop", func(t *testing.T) {
		store, poller := setupNotifyPollerFixture(t)

		yaml := minPollYAML("notify-update")
		insertNotifyPollPolicy(t, store, "pol-notify-update", "notify-update", yaml)

		// Start a loop via first Notify.
		poller.Notify(context.Background(), "pol-notify-update")
		if !hasLoop(poller, "pol-notify-update") {
			t.Fatal("setup: loop not started")
		}

		// Call Notify again (simulates a YAML update).
		poller.Notify(context.Background(), "pol-notify-update")

		// Loop should still be present (restarted) and there should be exactly one.
		if !hasLoop(poller, "pol-notify-update") {
			t.Error("expected loop to be present after second Notify (restart)")
		}
		if loopCount(poller) != 1 {
			t.Errorf("expected 1 loop after update, got %d", loopCount(poller))
		}
	})

	t.Run("paused poll policy: Notify cancels the loop", func(t *testing.T) {
		store, poller := setupNotifyPollerFixture(t)

		yaml := minPollYAML("notify-pause")
		insertNotifyPollPolicy(t, store, "pol-notify-pause", "notify-pause", yaml)

		poller.Notify(context.Background(), "pol-notify-pause")
		if !hasLoop(poller, "pol-notify-pause") {
			t.Fatal("setup: loop not started")
		}

		// Pause the policy in DB.
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if err := store.SetPolicyPausedAt(context.Background(), db.SetPolicyPausedAtParams{
			PausedAt: &now,
			ID:       "pol-notify-pause",
		}); err != nil {
			t.Fatalf("SetPolicyPausedAt: %v", err)
		}

		poller.Notify(context.Background(), "pol-notify-pause")

		if hasLoop(poller, "pol-notify-pause") {
			t.Error("expected loop to be cancelled after policy paused")
		}
	})

	t.Run("deleted policy: Notify cancels the loop without error", func(t *testing.T) {
		store, poller := setupNotifyPollerFixture(t)

		yaml := minPollYAML("notify-delete")
		insertNotifyPollPolicy(t, store, "pol-notify-delete", "notify-delete", yaml)

		poller.Notify(context.Background(), "pol-notify-delete")
		if !hasLoop(poller, "pol-notify-delete") {
			t.Fatal("setup: loop not started")
		}

		if err := store.DeletePolicy(context.Background(), "pol-notify-delete"); err != nil {
			t.Fatalf("DeletePolicy: %v", err)
		}

		// Must not panic and must cancel the loop.
		poller.Notify(context.Background(), "pol-notify-delete")

		if hasLoop(poller, "pol-notify-delete") {
			t.Error("expected loop to be cancelled after policy deleted")
		}
	})

	t.Run("non-poll trigger type: Notify is a no-op", func(t *testing.T) {
		store, poller := setupNotifyPollerFixture(t)

		insertNotifyWebhookPolicy(t, store, "pol-notify-webhook", "notify-webhook")

		poller.Notify(context.Background(), "pol-notify-webhook")

		if hasLoop(poller, "pol-notify-webhook") {
			t.Error("expected no loop for non-poll trigger type")
		}
	})
}

// ---- Scheduler.Notify tests ------------------------------------------------

// setupNotifySchedulerFixture creates a Scheduler but does NOT call Start.
func setupNotifySchedulerFixture(t *testing.T) (*db.Store, *Scheduler) {
	t.Helper()
	store := testutil.NewTestStore(t)
	registry := mcp.NewRegistry(store.Queries())
	manager := run.NewRunManager()
	resolver := stubDefaultModelResolver{provider: "anthropic", name: "claude-sonnet-4-6"}
	launcher := run.NewRunLauncher(run.RunLauncherConfig{
		Store:                  store,
		Registry:               registry,
		Manager:                manager,
		AgentFactory:           notifyPollerFactory(),
		Publisher:              nil,
		DefaultFeedbackTimeout: 0,
		ModelResolver:          resolver,
	})
	scheduler := NewScheduler(store, launcher, resolver)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	scheduler.mu.Lock()
	scheduler.rootCtx = ctx
	scheduler.mu.Unlock()

	return store, scheduler
}

// timerCount returns the number of armed timers for policyID.
func timerCount(s *Scheduler, policyID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.timers[policyID])
}

func TestScheduler_Notify(t *testing.T) {
	t.Run("scheduled policy with future fire_at: Notify arms timers", func(t *testing.T) {
		store, scheduler := setupNotifySchedulerFixture(t)

		future := time.Now().Add(10 * time.Second)
		yaml := minScheduledYAML("notify-sched-new", []time.Time{future})
		insertNotifyScheduledPolicy(t, store, "pol-notify-sched-new", "notify-sched-new", yaml)

		scheduler.Notify(context.Background(), "pol-notify-sched-new")

		if timerCount(scheduler, "pol-notify-sched-new") != 1 {
			t.Errorf("expected 1 timer, got %d", timerCount(scheduler, "pol-notify-sched-new"))
		}
	})

	t.Run("updated fire_at: Notify cancels old timers and arms new ones", func(t *testing.T) {
		store, scheduler := setupNotifySchedulerFixture(t)

		future1 := time.Now().Add(10 * time.Second)
		yaml := minScheduledYAML("notify-sched-update", []time.Time{future1})
		insertNotifyScheduledPolicy(t, store, "pol-notify-sched-upd", "notify-sched-update", yaml)

		scheduler.Notify(context.Background(), "pol-notify-sched-upd")
		if timerCount(scheduler, "pol-notify-sched-upd") != 1 {
			t.Fatal("setup: expected 1 timer after first Notify")
		}

		// Update DB with two future fire_at times.
		future2 := time.Now().Add(12 * time.Second)
		future3 := time.Now().Add(14 * time.Second)
		newYAML := minScheduledYAML("notify-sched-update", []time.Time{future2, future3})
		if _, err := store.UpdatePolicy(context.Background(), db.UpdatePolicyParams{
			ID:          "pol-notify-sched-upd",
			Name:        "notify-sched-update",
			TriggerType: "scheduled",
			Yaml:        newYAML,
			UpdatedAt:   time.Now().UTC().Format(time.RFC3339Nano),
		}); err != nil {
			t.Fatalf("UpdatePolicy: %v", err)
		}

		scheduler.Notify(context.Background(), "pol-notify-sched-upd")

		if timerCount(scheduler, "pol-notify-sched-upd") != 2 {
			t.Errorf("expected 2 timers after update, got %d", timerCount(scheduler, "pol-notify-sched-upd"))
		}
	})

	t.Run("paused policy: Notify cancels timers and arms none", func(t *testing.T) {
		store, scheduler := setupNotifySchedulerFixture(t)

		future := time.Now().Add(10 * time.Second)
		yaml := minScheduledYAML("notify-sched-pause", []time.Time{future})
		insertNotifyScheduledPolicy(t, store, "pol-notify-sched-pause", "notify-sched-pause", yaml)

		scheduler.Notify(context.Background(), "pol-notify-sched-pause")
		if timerCount(scheduler, "pol-notify-sched-pause") != 1 {
			t.Fatal("setup: expected 1 timer")
		}

		now := time.Now().UTC().Format(time.RFC3339Nano)
		if err := store.SetPolicyPausedAt(context.Background(), db.SetPolicyPausedAtParams{
			PausedAt: &now,
			ID:       "pol-notify-sched-pause",
		}); err != nil {
			t.Fatalf("SetPolicyPausedAt: %v", err)
		}

		scheduler.Notify(context.Background(), "pol-notify-sched-pause")

		if timerCount(scheduler, "pol-notify-sched-pause") != 0 {
			t.Errorf("expected 0 timers after pause, got %d", timerCount(scheduler, "pol-notify-sched-pause"))
		}
	})

	t.Run("deleted policy: Notify cancels timers without error", func(t *testing.T) {
		store, scheduler := setupNotifySchedulerFixture(t)

		future := time.Now().Add(10 * time.Second)
		yaml := minScheduledYAML("notify-sched-del", []time.Time{future})
		insertNotifyScheduledPolicy(t, store, "pol-notify-sched-del", "notify-sched-del", yaml)

		scheduler.Notify(context.Background(), "pol-notify-sched-del")
		if timerCount(scheduler, "pol-notify-sched-del") != 1 {
			t.Fatal("setup: expected 1 timer")
		}

		if err := store.DeletePolicy(context.Background(), "pol-notify-sched-del"); err != nil {
			t.Fatalf("DeletePolicy: %v", err)
		}

		// Must not panic; timers should be cancelled.
		scheduler.Notify(context.Background(), "pol-notify-sched-del")

		if timerCount(scheduler, "pol-notify-sched-del") != 0 {
			t.Errorf("expected 0 timers after delete, got %d", timerCount(scheduler, "pol-notify-sched-del"))
		}
	})

	t.Run("non-scheduled trigger type: Notify is a no-op", func(t *testing.T) {
		store, scheduler := setupNotifySchedulerFixture(t)

		insertNotifyWebhookPolicy(t, store, "pol-notify-sched-webhook", "notify-sched-webhook")

		scheduler.Notify(context.Background(), "pol-notify-sched-webhook")

		if timerCount(scheduler, "pol-notify-sched-webhook") != 0 {
			t.Error("expected no timers for non-scheduled trigger type")
		}
	})
}

// ---- helpers ---------------------------------------------------------------

func minPollYAML(name string) string {
	return "name: " + name + `
trigger:
  type: poll
  interval: 100ms
  match: all
  checks:
    - tool: poll-server.check
      path: "$.status"
      equals: ok
capabilities:
  tools:
    - tool: poll-server.check
agent:
  task: "poll task"
  concurrency: parallel
`
}

func minScheduledYAML(name string, fireTimes []time.Time) string {
	fireAtLines := ""
	for _, ft := range fireTimes {
		fireAtLines += "    - " + ft.UTC().Format(time.RFC3339) + "\n"
	}
	return "name: " + name + `
trigger:
  type: scheduled
  fire_at:
` + fireAtLines + `capabilities:
  tools:
    - tool: stub-server.read_data
agent:
  task: "scheduled task"
  concurrency: parallel
`
}

func insertNotifyPollPolicy(t *testing.T, store *db.Store, id, name, yaml string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.CreatePolicy(context.Background(), db.CreatePolicyParams{
		ID:          id,
		Name:        name,
		TriggerType: "poll",
		Yaml:        yaml,
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("insertNotifyPollPolicy %s: %v", id, err)
	}
}

func insertNotifyScheduledPolicy(t *testing.T, store *db.Store, id, name, yaml string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.CreatePolicy(context.Background(), db.CreatePolicyParams{
		ID:          id,
		Name:        name,
		TriggerType: "scheduled",
		Yaml:        yaml,
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("insertNotifyScheduledPolicy %s: %v", id, err)
	}
}

func insertNotifyWebhookPolicy(t *testing.T, store *db.Store, id, name string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.CreatePolicy(context.Background(), db.CreatePolicyParams{
		ID:          id,
		Name:        name,
		TriggerType: "webhook",
		Yaml:        "name: " + name + "\ntrigger:\n  type: webhook\nagent:\n  task: t\n",
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("insertNotifyWebhookPolicy %s: %v", id, err)
	}
}
