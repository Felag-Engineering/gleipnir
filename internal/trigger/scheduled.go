package trigger

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/rapp992/gleipnir/internal/agent"
	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/mcp"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/policy"
)

// Scheduler watches scheduled policies and fires runs at their configured fire_at times.
// On startup it loads all active (non-paused) scheduled policies, skips already-fired
// timestamps, and sets timers for any future ones. After the final fire time fires for
// a policy it pauses the policy so it is not re-loaded on the next restart.
type Scheduler struct {
	store     *db.Store
	registry  *mcp.Registry
	manager   *RunManager
	newAgent  AgentFactory
	publisher agent.Publisher
}

// NewScheduler returns a Scheduler ready to be started.
// publisher may be nil, in which case no real-time events are emitted.
func NewScheduler(store *db.Store, registry *mcp.Registry, manager *RunManager, factory AgentFactory, publisher agent.Publisher) *Scheduler {
	return &Scheduler{
		store:     store,
		registry:  registry,
		manager:   manager,
		newAgent:  factory,
		publisher: publisher,
	}
}

// Start loads all active scheduled policies and arms timers for their future fire times.
// It returns immediately after scheduling; timers fire in background goroutines.
// Cancelling ctx stops any pending timers before they fire.
func (s *Scheduler) Start(ctx context.Context) error {
	policies, err := s.store.GetScheduledActivePolicies(ctx)
	if err != nil {
		return fmt.Errorf("load scheduled policies: %w", err)
	}

	for _, p := range policies {
		parsed, err := policy.Parse(p.Yaml)
		if err != nil {
			slog.Error("scheduled: failed to parse policy yaml", "policy_id", p.ID, "err", err)
			continue
		}
		s.schedulePolicy(ctx, p.ID, parsed)
	}
	return nil
}

// schedulePolicy arms timers for all future fire_at times in the parsed policy.
func (s *Scheduler) schedulePolicy(ctx context.Context, policyID string, parsed *model.ParsedPolicy) {
	now := time.Now().UTC()

	for _, fireTime := range parsed.Trigger.FireAt {
		if !fireTime.After(now) {
			// Already passed — check whether a run was created for this time.
			// If so, skip silently. If not (e.g. server was down), we also skip
			// because firing stale times would surprise operators.
			continue
		}

		// Capture loop variable for goroutine.
		ft := fireTime
		delay := ft.Sub(now)

		go func() {
			timer := time.NewTimer(delay)
			defer timer.Stop()

			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				s.fire(ctx, policyID, parsed, ft)
			}
		}()
	}
}

// fire creates a run for the given scheduled fire time and launches the agent.
// After creating the run it checks whether all fire_at times are now consumed
// and pauses the policy if so.
func (s *Scheduler) fire(ctx context.Context, policyID string, parsed *model.ParsedPolicy, fireTime time.Time) {
	// Check dedup: if a run already exists for this policy with a trigger_payload
	// containing scheduled_for within ±1 minute, skip.
	if s.alreadyFired(ctx, policyID, fireTime) {
		slog.Info("scheduled: skipping already-fired time", "policy_id", policyID, "fire_at", fireTime)
		return
	}

	payload, _ := json.Marshal(map[string]string{
		"scheduled_for": fireTime.UTC().Format(time.RFC3339),
	})

	now := time.Now().UTC().Format(time.RFC3339Nano)
	run, err := s.store.CreateRun(ctx, db.CreateRunParams{
		ID:             model.NewULID(),
		PolicyID:       policyID,
		TriggerType:    string(model.TriggerTypeScheduled),
		TriggerPayload: string(payload),
		StartedAt:      now,
		CreatedAt:      now,
	})
	if err != nil {
		slog.Error("scheduled: failed to create run", "policy_id", policyID, "fire_at", fireTime, "err", err)
		return
	}

	tools, err := s.registry.ResolveForPolicy(ctx, parsed)
	if err != nil {
		markRunFailed(s.store, run.ID, err)
		slog.Error("scheduled: failed to resolve tools", "run_id", run.ID, "err", err)
		return
	}

	audit := agent.NewAuditWriter(s.store.Queries, agent.WithPublisher(s.publisher))
	sm := agent.NewRunStateMachine(run.ID, model.RunStatusPending, s.store.Queries, agent.WithStateMachinePublisher(s.publisher))

	ba, err := s.newAgent(agent.Config{
		Tools:        tools,
		Policy:       parsed,
		Audit:        audit,
		StateMachine: sm,
		ApprovalCh:   make(chan bool),
	})
	if err != nil {
		markRunFailed(s.store, run.ID, err)
		audit.Close()
		slog.Error("scheduled: failed to construct agent", "run_id", run.ID, "err", err)
		return
	}

	runCtx, cancel := context.WithCancel(context.Background())
	s.manager.Register(run.ID, cancel)

	go func() {
		defer cancel()
		defer s.manager.Deregister(run.ID)
		if err := ba.Run(runCtx, run.ID, string(payload)); err != nil {
			slog.Error("scheduled run failed", "run_id", run.ID, "err", err)
		}
	}()

	// Pause policy if all fire times are now in the past.
	s.pauseIfExhausted(ctx, policyID, parsed)
}

// alreadyFired returns true if a run exists for this policy whose trigger_payload
// contains a scheduled_for time within ±1 minute of fireTime.
func (s *Scheduler) alreadyFired(ctx context.Context, policyID string, fireTime time.Time) bool {
	runs, err := s.store.ListRunsByPolicy(ctx, policyID)
	if err != nil {
		// Can't determine — assume not fired to avoid skipping silently.
		return false
	}

	window := time.Minute
	for _, r := range runs {
		if r.TriggerType != string(model.TriggerTypeScheduled) {
			continue
		}
		var payload struct {
			ScheduledFor string `json:"scheduled_for"`
		}
		if err := json.Unmarshal([]byte(r.TriggerPayload), &payload); err != nil {
			continue
		}
		if payload.ScheduledFor == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, strings.TrimSpace(payload.ScheduledFor))
		if err != nil {
			continue
		}
		diff := t.UTC().Sub(fireTime.UTC())
		if diff < 0 {
			diff = -diff
		}
		if diff <= window {
			return true
		}
	}
	return false
}

// pauseIfExhausted pauses the policy if all fire_at times are now in the past.
func (s *Scheduler) pauseIfExhausted(ctx context.Context, policyID string, parsed *model.ParsedPolicy) {
	now := time.Now().UTC()
	for _, t := range parsed.Trigger.FireAt {
		if t.After(now) {
			return // still has future times
		}
	}
	pausedAt := now.Format(time.RFC3339Nano)
	if err := s.store.SetPolicyPausedAt(ctx, db.SetPolicyPausedAtParams{
		PausedAt: &pausedAt,
		ID:       policyID,
	}); err != nil {
		slog.Error("scheduled: failed to pause exhausted policy", "policy_id", policyID, "err", err)
	} else {
		slog.Info("scheduled: policy paused after all fire times consumed", "policy_id", policyID)
	}
}
