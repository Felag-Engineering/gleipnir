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

	cronlib "github.com/robfig/cron/v3"

	"github.com/rapp992/gleipnir/internal/config"
	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/policy"
	"github.com/rapp992/gleipnir/internal/execution/run"
)

// cronLoopHandle wraps a loop's cancel func so the map stores a pointer we can
// identity-compare. Needed so a goroutine's deferred cleanup only deletes the
// map entry it installed — if Notify has since cancelled-and-replaced the loop,
// the stale goroutine must not evict the fresh handle.
type cronLoopHandle struct {
	cancel context.CancelFunc
}

// CronRunner manages one goroutine per active cron policy. Each goroutine waits
// until the next scheduled fire time and then launches a run. Unlike the
// Scheduler (which fires once per fire_at time and then pauses), cron policies
// run indefinitely — the goroutine loops back and waits for the next fire time
// after each launch.
//
// Missed fires are silently skipped: on start or restart, cronlib.Schedule.Next
// returns the next upcoming fire time, so any times that elapsed during downtime
// are never triggered. This is the "no catch-up" behaviour specified in the issue.
type CronRunner struct {
	store         *db.Store
	launcher      *run.RunLauncher
	modelResolver defaultModelResolver
	parser        cronlib.Parser             // 5-field standard parser, built once
	mu            sync.Mutex                 // protects loops, rootCtx, and rootCancel
	loops         map[string]*cronLoopHandle // policyID → handle for that goroutine
	wg            sync.WaitGroup             // tracks all cron loop goroutines
	rootCtx       context.Context            // set in Start; used by Notify to outlive the HTTP request
	rootCancel    context.CancelFunc         // cancels rootCtx; set in Start
}

// NewCronRunner returns a CronRunner ready to be started. modelResolver is used
// to look up the system default model when the policy YAML omits the model block.
func NewCronRunner(store *db.Store, launcher *run.RunLauncher, modelResolver defaultModelResolver) *CronRunner {
	return &CronRunner{
		store:         store,
		launcher:      launcher,
		modelResolver: modelResolver,
		parser:        cronlib.NewParser(cronlib.Minute | cronlib.Hour | cronlib.Dom | cronlib.Month | cronlib.Dow),
		loops:         make(map[string]*cronLoopHandle),
	}
}

// Start loads all active cron policies and starts a loop goroutine for each.
// It also launches a background reconciliation goroutine that periodically syncs
// running goroutines with DB state. This handles policies created, paused, or
// deleted after startup. Start returns immediately; goroutines run in the background.
func (c *CronRunner) Start(ctx context.Context) error {
	rootCtx, rootCancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.rootCtx = rootCtx
	c.rootCancel = rootCancel
	c.mu.Unlock()

	policies, err := c.store.GetCronActivePolicies(rootCtx)
	if err != nil {
		rootCancel()
		return fmt.Errorf("load cron policies: %w", err)
	}

	for _, pol := range policies {
		c.startCronLoop(rootCtx, pol)
	}

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.reconcileLoop(rootCtx)
	}()

	return nil
}

// reconcileLoop periodically reconciles running cron goroutines with the current
// set of active cron policies in the DB.
func (c *CronRunner) reconcileLoop(ctx context.Context) {
	ticker := time.NewTicker(reconcileInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.reconcile(ctx)
		}
	}
}

// reconcile is a single reconciliation pass. Separated from reconcileLoop so it
// can be tested directly.
func (c *CronRunner) reconcile(ctx context.Context) {
	activePolicies, err := c.store.GetCronActivePolicies(ctx)
	if err != nil {
		slog.Error("cron: failed to reconcile cron policies", "err", err)
		return
	}

	activeSet := make(map[string]db.Policy, len(activePolicies))
	for _, pol := range activePolicies {
		activeSet[pol.ID] = pol
	}

	// Determine which policies to start and stop under the lock, but release
	// before calling startCronLoop — that function does DB queries and policy
	// parsing, which must not block Notify calls from API handlers.
	var toStart []db.Policy
	c.mu.Lock()
	for _, pol := range activePolicies {
		if _, running := c.loops[pol.ID]; !running {
			toStart = append(toStart, pol)
		}
	}
	for policyID, h := range c.loops {
		if _, active := activeSet[policyID]; !active {
			h.cancel()
			delete(c.loops, policyID)
		}
	}
	c.mu.Unlock()

	for _, pol := range toStart {
		c.startCronLoop(ctx, pol)
	}
}

// cancelLoopLocked cancels and removes the cron loop for policyID if one is running.
// mu must be held by the caller.
func (c *CronRunner) cancelLoopLocked(policyID string) {
	if h, ok := c.loops[policyID]; ok {
		h.cancel()
		delete(c.loops, policyID)
	}
}

// Notify immediately reconciles the cron loop for the given policy in response
// to a create, update, pause, or delete event from the API. It reads current DB
// state so it converges correctly for all mutation types:
//   - Not found / non-cron / paused → cancel any existing loop.
//   - Active cron policy → restart the loop so YAML changes (e.g. new expression) take effect.
//
// reqCtx is used only for the DB read; the loop itself runs under the root context
// captured in Start so it outlives the HTTP request.
// Errors are logged at warn level; Notify never returns an error (best-effort).
func (c *CronRunner) Notify(reqCtx context.Context, policyID string) {
	pol, err := c.store.GetPolicy(reqCtx, policyID)
	if err != nil {
		c.mu.Lock()
		c.cancelLoopLocked(policyID)
		c.mu.Unlock()
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Warn("cron: notify failed to read policy", "policy_id", policyID, "err", err)
		}
		return
	}

	if pol.TriggerType != string(model.TriggerTypeCron) || pol.PausedAt != nil {
		c.mu.Lock()
		c.cancelLoopLocked(policyID)
		c.mu.Unlock()
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Cancel the existing loop first so that a YAML change (e.g. new cron_expr)
	// takes effect. startCronLoopLocked has an early-return guard for already-running
	// loops, so we must remove the old entry before calling it.
	c.cancelLoopLocked(policyID)
	c.startCronLoopLocked(c.rootCtx, pol)
}

// startCronLoop acquires mu and starts a goroutine for the given policy.
// It is safe to call from Start (no lock held) and from tests.
func (c *CronRunner) startCronLoop(ctx context.Context, policyRow db.Policy) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.startCronLoopLocked(ctx, policyRow)
}

// startCronLoopLocked parses the policy YAML and cron expression, then starts
// the loop goroutine. mu must be held by the caller.
func (c *CronRunner) startCronLoopLocked(ctx context.Context, policyRow db.Policy) {
	if _, already := c.loops[policyRow.ID]; already {
		return
	}

	provider, modelName, err := c.modelResolver.GetSystemDefault(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		slog.Error("cron: failed to load system default model", "policy_id", policyRow.ID, "err", err)
		return
	}

	parsed, err := policy.Parse(policyRow.Yaml, provider, modelName)
	if err != nil {
		slog.Error("cron: failed to parse policy yaml", "policy_id", policyRow.ID, "err", err)
		return
	}
	if parsed.Agent.ModelConfig.Provider == "" || parsed.Agent.ModelConfig.Name == "" {
		slog.Error("cron: no default model configured; skipping policy", "policy_id", policyRow.ID)
		return
	}

	schedule, err := c.parser.Parse(parsed.Trigger.CronExpr)
	if err != nil {
		slog.Error("cron: failed to parse cron expression", "policy_id", policyRow.ID, "expr", parsed.Trigger.CronExpr, "err", err)
		return
	}

	loopCtx, loopCancel := context.WithCancel(ctx)
	handle := &cronLoopHandle{cancel: loopCancel}
	c.loops[policyRow.ID] = handle

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer func() {
			// Remove the entry from the map when the loop exits so reconcile
			// can restart it if the policy becomes active again.
			// Identity check: if Notify (or reconcile) has cancelled this loop
			// and installed a replacement, the map entry will point to a different
			// handle — leave it alone.
			c.mu.Lock()
			if current, ok := c.loops[policyRow.ID]; ok && current == handle {
				delete(c.loops, policyRow.ID)
			}
			c.mu.Unlock()
		}()
		c.cronLoop(loopCtx, policyRow.ID, parsed, schedule)
	}()
}

// cronLoop waits for the next scheduled fire time and launches a run, then loops.
// schedule.Next(time.Now()) always returns the next future fire time, so if the
// server was down when a fire time passed, that fire is silently skipped.
func (c *CronRunner) cronLoop(ctx context.Context, policyID string, parsed *model.ParsedPolicy, schedule cronlib.Schedule) {
	for {
		next := schedule.Next(time.Now().UTC())
		delay := time.Until(next)
		timer := time.NewTimer(delay)

		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			c.fire(ctx, policyID, parsed, next)
		}
	}
}

// fire builds the cron trigger payload, enforces the concurrency policy, and
// launches a run (or enqueues one when there is an active run and the policy
// uses queue concurrency). Unlike scheduled.fire, cron never pauses the policy
// after firing — cron policies run indefinitely until manually paused.
func (c *CronRunner) fire(ctx context.Context, policyID string, parsed *model.ParsedPolicy, firedAt time.Time) {
	// json.Marshal on a map[string]string cannot fail, so the error is safe to ignore.
	payload, _ := json.Marshal(map[string]string{
		"cron_fired_at": firedAt.UTC().Format(config.TimestampFormat),
		"expression":    parsed.Trigger.CronExpr,
	})

	if err := c.launcher.CheckConcurrency(ctx, policyID, parsed.Agent.Concurrency); err != nil {
		switch {
		case errors.Is(err, run.ErrConcurrencySkipActive):
			slog.Info("cron: skipping fire, active run exists (concurrency: skip)",
				"policy_id", policyID, "fired_at", firedAt)
		case errors.Is(err, run.ErrConcurrencyQueueActive):
			if enqErr := c.launcher.Enqueue(ctx, run.LaunchParams{
				PolicyID:       policyID,
				TriggerType:    model.TriggerTypeCron,
				TriggerPayload: string(payload),
				ParsedPolicy:   parsed,
			}, parsed.Agent.QueueDepth); enqErr != nil {
				if errors.Is(enqErr, run.ErrConcurrencyQueueFull) {
					slog.Warn("cron: trigger queue is full", "policy_id", policyID, "fired_at", firedAt)
				} else {
					slog.Error("cron: failed to enqueue trigger", "policy_id", policyID, "fired_at", firedAt, "err", enqErr)
				}
			} else {
				slog.Info("cron: trigger queued (active run exists)", "policy_id", policyID, "fired_at", firedAt)
			}
		default:
			slog.Error("cron: concurrency check failed", "policy_id", policyID, "err", err)
		}
		return
	}

	result, err := c.launcher.Launch(ctx, run.LaunchParams{
		PolicyID:       policyID,
		TriggerType:    model.TriggerTypeCron,
		TriggerPayload: string(payload),
		ParsedPolicy:   parsed,
	})
	if err != nil {
		slog.Error("cron: failed to launch run", "policy_id", policyID, "fired_at", firedAt, "err", err)
		return
	}

	slog.Info("cron: run launched", "run_id", result.RunID, "policy_id", policyID, "fired_at", firedAt)
}

// Wait blocks until all active cron goroutines have exited. Call after cancelling
// the root context to drain cleanly during shutdown.
func (c *CronRunner) Wait() {
	c.wg.Wait()
}

// Stop cancels all active cron goroutines (including the reconcile loop) and
// waits for them to exit. It is equivalent to cancelling the context passed to Start.
func (c *CronRunner) Stop() {
	c.mu.Lock()
	cancel := c.rootCancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	c.wg.Wait()
}
