package trigger

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/rapp992/gleipnir/internal/config"
	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/policy"
	"github.com/rapp992/gleipnir/internal/execution/run"
)

// Scheduler watches scheduled policies and fires runs at their configured fire_at times.
// On startup it loads all active (non-paused) scheduled policies, skips already-fired
// timestamps, and sets timers for any future ones. After the final fire time fires for
// a policy it pauses the policy so it is not re-loaded on the next restart.
type Scheduler struct {
	store         *db.Store
	launcher      *run.RunLauncher
	modelResolver defaultModelResolver
	mu            sync.Mutex                      // protects timers and rootCtx
	timers        map[string][]context.CancelFunc // policyID -> per-fire-time cancel funcs
	rootCtx       context.Context                 // set in Start; used by Notify to outlive the HTTP request
}

// NewScheduler returns a Scheduler ready to be started.
func NewScheduler(store *db.Store, launcher *run.RunLauncher, modelResolver defaultModelResolver) *Scheduler {
	return &Scheduler{
		store:         store,
		launcher:      launcher,
		modelResolver: modelResolver,
		timers:        make(map[string][]context.CancelFunc),
	}
}

// Start loads all active scheduled policies and arms timers for their future fire times.
// It returns immediately after scheduling; timers fire in background goroutines.
// Cancelling ctx stops any pending timers before they fire.
func (s *Scheduler) Start(ctx context.Context) error {
	s.mu.Lock()
	s.rootCtx = ctx
	s.mu.Unlock()

	policies, err := s.store.GetScheduledActivePolicies(ctx)
	if err != nil {
		return fmt.Errorf("load scheduled policies: %w", err)
	}

	for _, p := range policies {
		provider, modelName := s.resolveDefaults(ctx)
		parsed, err := policy.Parse(p.Yaml, provider, modelName)
		if err != nil {
			slog.Error("scheduled: failed to parse policy yaml", "policy_id", p.ID, "err", err)
			continue
		}
		if parsed.Agent.ModelConfig.Provider == "" || parsed.Agent.ModelConfig.Name == "" {
			slog.Error("scheduled: no default model configured; skipping policy",
				"policy_id", p.ID)
			continue
		}
		s.schedulePolicy(ctx, p.ID, parsed)
	}
	return nil
}

// resolveDefaults fetches the system default provider and model name. On error
// (including sql.ErrNoRows for an unconfigured default) it returns ("", "") so
// policy.Parse leaves ModelConfig blank — the caller is responsible for
// checking whether the result is usable.
func (s *Scheduler) resolveDefaults(ctx context.Context) (string, string) {
	provider, modelName, err := s.modelResolver.GetSystemDefault(ctx)
	if err != nil {
		return "", ""
	}
	return provider, modelName
}

// cancelTimersLocked cancels all pending timers for policyID and removes the
// entry. mu must be held by the caller.
func (s *Scheduler) cancelTimersLocked(policyID string) {
	for _, cancel := range s.timers[policyID] {
		cancel()
	}
	delete(s.timers, policyID)
}

// schedulePolicy arms timers for all future fire_at times in the parsed policy.
// Each timer goroutine derives its context from ctx so that cancelling ctx (or
// calling cancelTimersLocked) stops timers that have not yet fired.
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

		// Give each timer its own cancel so Notify can stop individual timers
		// without cancelling unrelated ones.
		timerCtx, timerCancel := context.WithCancel(ctx)

		s.mu.Lock()
		s.timers[policyID] = append(s.timers[policyID], timerCancel)
		s.mu.Unlock()

		go func() {
			timer := time.NewTimer(delay)
			defer timer.Stop()

			select {
			case <-timerCtx.Done():
				return
			case <-timer.C:
				s.fire(timerCtx, policyID, parsed, ft)
			}
		}()
	}
}

// Notify immediately reconciles the scheduled timers for the given policy in
// response to a create, update, pause, or delete event from the API. It cancels
// any existing timers for the policy, then re-arms new ones if the policy is
// active (non-paused, type=scheduled).
//
// reqCtx is used only for the DB read; timers run under the root context
// captured in Start so they outlive the HTTP request.
// Errors are logged at warn level; Notify never returns an error (best-effort).
func (s *Scheduler) Notify(reqCtx context.Context, policyID string) {
	// Cancel existing timers before deciding whether to re-arm — regardless of
	// what DB state says, old timers are stale after any mutation.
	s.mu.Lock()
	s.cancelTimersLocked(policyID)
	rootCtx := s.rootCtx
	s.mu.Unlock()

	pol, err := s.store.GetPolicy(reqCtx, policyID)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Warn("scheduler: notify failed to read policy", "policy_id", policyID, "err", err)
		}
		return
	}

	// Policy exists but should not have active timers.
	if pol.TriggerType != string(model.TriggerTypeScheduled) || pol.PausedAt != nil {
		return
	}

	provider, modelName := s.resolveDefaults(reqCtx)
	parsed, err := policy.Parse(pol.Yaml, provider, modelName)
	if err != nil {
		slog.Warn("scheduler: notify failed to parse policy yaml", "policy_id", policyID, "err", err)
		return
	}
	if parsed.Agent.ModelConfig.Provider == "" || parsed.Agent.ModelConfig.Name == "" {
		slog.Error("scheduled: no default model configured; skipping policy", "policy_id", policyID)
		return
	}

	s.schedulePolicy(rootCtx, policyID, parsed)
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

	// json.Marshal on a map[string]string with a string value cannot fail,
	// so the error is safe to ignore here.
	payload, _ := json.Marshal(map[string]string{
		"scheduled_for": fireTime.UTC().Format(config.TimestampFormat),
	})

	// Enforce concurrency policy before launching, consistent with webhook and
	// manual triggers. All non-nil errors prevent the run from firing.
	if err := s.launcher.CheckConcurrency(ctx, policyID, parsed.Agent.Concurrency); err != nil {
		switch {
		case errors.Is(err, run.ErrConcurrencySkipActive):
			slog.Info("scheduled: skipping fire, active run exists (concurrency: skip)",
				"policy_id", policyID, "fire_at", fireTime)
		case errors.Is(err, run.ErrConcurrencyQueueActive):
			if enqErr := s.launcher.Enqueue(ctx, run.LaunchParams{
				PolicyID:       policyID,
				TriggerType:    model.TriggerTypeScheduled,
				TriggerPayload: string(payload),
				ParsedPolicy:   parsed,
			}, parsed.Agent.QueueDepth); enqErr != nil {
				if errors.Is(enqErr, run.ErrConcurrencyQueueFull) {
					slog.Warn("scheduled: trigger queue is full",
						"policy_id", policyID, "fire_at", fireTime)
				} else {
					slog.Error("scheduled: failed to enqueue trigger",
						"policy_id", policyID, "fire_at", fireTime, "err", enqErr)
				}
			} else {
				slog.Info("scheduled: trigger queued (active run exists)",
					"policy_id", policyID, "fire_at", fireTime)
				// The fire time was consumed (enqueued). Pause the policy if
				// all fire_at times are now exhausted — DrainQueue calls Launch
				// directly and does not invoke pauseIfExhausted.
				s.pauseIfExhausted(ctx, policyID, parsed)
			}
		default:
			slog.Error("scheduled: concurrency check failed",
				"policy_id", policyID, "err", err)
		}
		return
	}

	result, err := s.launcher.Launch(ctx, run.LaunchParams{
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
