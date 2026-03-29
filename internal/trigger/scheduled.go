package trigger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/rapp992/gleipnir/internal/config"
	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/policy"
)

// Scheduler watches scheduled policies and fires runs at their configured fire_at times.
// On startup it loads all active (non-paused) scheduled policies, skips already-fired
// timestamps, and sets timers for any future ones. After the final fire time fires for
// a policy it pauses the policy so it is not re-loaded on the next restart.
type Scheduler struct {
	store    *db.Store
	launcher *RunLauncher
}

// NewScheduler returns a Scheduler ready to be started.
func NewScheduler(store *db.Store, launcher *RunLauncher) *Scheduler {
	return &Scheduler{
		store:    store,
		launcher: launcher,
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
		parsed, err := policy.Parse(p.Yaml, model.DefaultProvider, model.DefaultModelName)
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

	// Enforce concurrency policy before launching, consistent with webhook and
	// manual triggers. All non-nil errors prevent the run from firing.
	if err := s.launcher.CheckConcurrency(ctx, policyID, parsed.Agent.Concurrency); err != nil {
		switch {
		case errors.Is(err, ErrConcurrencySkipActive):
			slog.Info("scheduled: skipping fire, active run exists (concurrency: skip)",
				"policy_id", policyID, "fire_at", fireTime)
		case errors.Is(err, ErrConcurrencyNotImplemented):
			slog.Warn("scheduled: concurrency mode not implemented, skipping",
				"policy_id", policyID, "concurrency", parsed.Agent.Concurrency)
		default:
			slog.Error("scheduled: concurrency check failed",
				"policy_id", policyID, "err", err)
		}
		return
	}

	// json.Marshal on a map[string]string with a string value cannot fail,
	// so the error is safe to ignore here.
	payload, _ := json.Marshal(map[string]string{
		"scheduled_for": fireTime.UTC().Format(config.TimestampFormat),
	})

	result, err := s.launcher.Launch(ctx, LaunchParams{
		PolicyID:       policyID,
		TriggerType:    model.TriggerTypeScheduled,
		TriggerPayload: string(payload),
		ParsedPolicy:   parsed,
	})
	if err != nil {
		slog.Error("scheduled: failed to launch run", "policy_id", policyID, "fire_at", fireTime, "err", err)
		return
	}

	slog.Info("scheduled: run launched", "run_id", result.RunID, "policy_id", policyID, "fire_at", fireTime)

	// Pause policy if all fire times are now in the past.
	s.pauseIfExhausted(ctx, policyID, parsed)
}

// alreadyFired returns true if a scheduled run already exists for this policy
// at or after fireTime minus one minute. Using a DB EXISTS query avoids loading
// all runs into memory for what is fundamentally a boolean check.
func (s *Scheduler) alreadyFired(ctx context.Context, policyID string, fireTime time.Time) bool {
	since := fireTime.Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	fired, err := s.store.HasScheduledRunSince(ctx, db.HasScheduledRunSinceParams{
		PolicyID: policyID,
		Since:    since,
	})
	if err != nil {
		slog.Warn("dedup query failed, assuming not fired",
			"policy_id", policyID, "fire_at", fireTime, "err", err)
		return false
	}
	return fired == 1
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
