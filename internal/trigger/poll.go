package trigger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/policy"
)

const (
	// reconcileInterval is how often the Poller checks for newly created,
	// paused, or deleted poll policies. A newly created poll policy may take
	// up to this long to start polling.
	reconcileInterval = 60 * time.Second
)

// Poller manages one goroutine per active poll policy. Each goroutine loops
// independently, calling the policy's configured MCP tools on each interval tick.
// When all checks pass (or any check passes, depending on the match mode), a
// run is launched. Polling is stateless — no deduplication or backoff.
type Poller struct {
	store        *db.Store
	launcher     *RunLauncher
	toolResolver toolResolver
	mu           sync.Mutex                    // protects the loops map
	loops        map[string]context.CancelFunc // policyID -> cancel func for that goroutine
	wg           sync.WaitGroup                // tracks all poll loop goroutines
}

// NewPoller returns a Poller ready to be started. resolver is used to call
// poll tools outside of any agent run context.
func NewPoller(store *db.Store, launcher *RunLauncher, resolver toolResolver) *Poller {
	return &Poller{
		store:        store,
		launcher:     launcher,
		toolResolver: resolver,
		loops:        make(map[string]context.CancelFunc),
	}
}

// Start loads all active poll policies and starts a polling goroutine for each.
// It also launches a background reconciliation goroutine that periodically
// syncs running goroutines with the current DB state. This handles policies
// created, paused, or deleted after startup.
// Start returns immediately; goroutines run in the background.
func (p *Poller) Start(ctx context.Context) error {
	policies, err := p.store.GetPollActivePolicies(ctx)
	if err != nil {
		return fmt.Errorf("load poll policies: %w", err)
	}

	for _, pol := range policies {
		p.startPollLoop(ctx, pol)
	}

	// The reconciliation goroutine is tracked by wg so Wait() waits for it too.
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.reconcileLoop(ctx)
	}()

	return nil
}

// reconcileLoop periodically reconciles running poll goroutines with the
// current set of active poll policies in the DB. It starts loops for newly
// created policies and cancels loops for paused or deleted ones.
func (p *Poller) reconcileLoop(ctx context.Context) {
	ticker := time.NewTicker(reconcileInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.reconcile(ctx)
		}
	}
}

// reconcile is the single reconciliation pass. It is separated from
// reconcileLoop so it can be tested directly.
func (p *Poller) reconcile(ctx context.Context) {
	activePolicies, err := p.store.GetPollActivePolicies(ctx)
	if err != nil {
		slog.Error("poller: failed to reconcile poll policies", "err", err)
		return
	}

	activeSet := make(map[string]db.Policy, len(activePolicies))
	for _, pol := range activePolicies {
		activeSet[pol.ID] = pol
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Start loops for policies not yet running.
	for _, pol := range activePolicies {
		if _, running := p.loops[pol.ID]; !running {
			// startPollLoopLocked is called instead of startPollLoop because
			// we already hold mu and startPollLoop would deadlock.
			p.startPollLoopLocked(ctx, pol)
		}
	}

	// Cancel loops for policies no longer active (paused or deleted).
	for policyID, cancel := range p.loops {
		if _, active := activeSet[policyID]; !active {
			cancel()
			delete(p.loops, policyID)
		}
	}
}

// startPollLoop acquires mu and starts a goroutine for the given policy.
// It is safe to call from Start (no lock held) and from tests.
func (p *Poller) startPollLoop(ctx context.Context, policyRow db.Policy) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.startPollLoopLocked(ctx, policyRow)
}

// startPollLoopLocked starts a poll goroutine. mu must be held by the caller.
func (p *Poller) startPollLoopLocked(ctx context.Context, policyRow db.Policy) {
	if _, already := p.loops[policyRow.ID]; already {
		return
	}

	parsed, err := policy.Parse(policyRow.Yaml, model.DefaultProvider, model.DefaultModelName)
	if err != nil {
		slog.Error("poller: failed to parse policy yaml", "policy_id", policyRow.ID, "err", err)
		return
	}

	loopCtx, loopCancel := context.WithCancel(ctx)
	p.loops[policyRow.ID] = loopCancel

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer func() {
			// Remove the entry from the map when the loop exits so reconcile
			// can restart it if the policy becomes active again.
			// There is a benign race here: reconcile may run between the goroutine
			// exiting and this delete, and observe a stale cancel func that has
			// already been called. The reconcile will simply call cancel() again
			// (a no-op) and delete the entry itself. Both paths converge correctly.
			p.mu.Lock()
			delete(p.loops, policyRow.ID)
			p.mu.Unlock()
		}()
		p.pollLoop(loopCtx, policyRow.ID, parsed)
	}()
}

// pollLoop runs the polling cycle for a single policy until ctx is cancelled.
// The first tick fires after one interval elapses — the loop does not poll
// immediately on startup. This avoids a burst of tool calls when the server
// restarts with many active poll policies.
func (p *Poller) pollLoop(ctx context.Context, policyID string, parsed *model.ParsedPolicy) {
	ticker := time.NewTicker(parsed.Trigger.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.poll(ctx, policyID, parsed)
		}
	}
}

// poll calls each configured check tool, evaluates the conditions, and
// launches a run when the match mode is satisfied.
func (p *Poller) poll(ctx context.Context, policyID string, parsed *model.ParsedPolicy) {
	checks := parsed.Trigger.Checks
	results := make([]checkResult, len(checks))

	for i, check := range checks {
		client, toolName, err := p.toolResolver.ResolveToolByName(ctx, check.Tool)
		if err != nil {
			slog.Error("poller: failed to resolve check tool",
				"policy_id", policyID, "tool", check.Tool, "err", err)
			results[i] = checkResult{Err: err}
			continue
		}

		result, err := client.CallTool(ctx, toolName, check.Input)
		if err != nil {
			slog.Error("poller: check tool call failed",
				"policy_id", policyID, "tool", check.Tool, "err", err)
			results[i] = checkResult{Err: err}
			continue
		}
		if result.IsError {
			callErr := fmt.Errorf("tool returned an error result")
			slog.Error("poller: check tool returned error result",
				"policy_id", policyID, "tool", check.Tool)
			results[i] = checkResult{Err: callErr}
			continue
		}

		results[i] = checkResult{Output: result.Output}
	}

	if !evaluateChecks(results, checks, parsed.Trigger.Match) {
		slog.Debug("poller: checks did not match, no run triggered", "policy_id", policyID)
		return
	}

	// Checks matched — enforce concurrency policy, then launch or queue a run.
	payload := buildTriggerPayload(checks, results)

	if err := p.launcher.CheckConcurrency(ctx, policyID, parsed.Agent.Concurrency); err != nil {
		switch {
		case errors.Is(err, ErrConcurrencySkipActive):
			slog.Info("poller: skipping run, active run exists (concurrency: skip)", "policy_id", policyID)
			return
		case errors.Is(err, ErrConcurrencyQueueActive):
			if enqErr := p.launcher.Enqueue(ctx, LaunchParams{
				PolicyID:       policyID,
				TriggerType:    model.TriggerTypePoll,
				TriggerPayload: payload,
				ParsedPolicy:   parsed,
			}, parsed.Agent.QueueDepth); enqErr != nil {
				if errors.Is(enqErr, ErrConcurrencyQueueFull) {
					slog.Warn("poller: trigger queue is full", "policy_id", policyID)
				} else {
					slog.Error("poller: failed to enqueue trigger", "policy_id", policyID, "err", enqErr)
				}
			} else {
				slog.Info("poller: trigger queued (active run exists)", "policy_id", policyID)
			}
			return
		case errors.Is(err, ErrConcurrencyNotImplemented):
			slog.Warn("poller: concurrency mode not implemented, skipping",
				"policy_id", policyID, "concurrency", parsed.Agent.Concurrency)
			return
		default:
			slog.Error("poller: concurrency check failed", "policy_id", policyID, "err", err)
			return
		}
	}

	launchResult, err := p.launcher.Launch(ctx, LaunchParams{
		PolicyID:       policyID,
		TriggerType:    model.TriggerTypePoll,
		TriggerPayload: payload,
		ParsedPolicy:   parsed,
	})
	if err != nil {
		slog.Error("poller: failed to launch run", "policy_id", policyID, "err", err)
		return
	}

	slog.Info("poller: run launched", "run_id", launchResult.RunID, "policy_id", policyID)
}

// pollResultEntry is one item in the poll_results trigger payload array.
type pollResultEntry struct {
	Tool   string          `json:"tool"`
	Input  map[string]any  `json:"input,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// buildTriggerPayload constructs the JSON trigger payload delivered to the agent
// as its first user message. Each entry includes the tool name, input args, and
// either the raw MCP result or an error string.
func buildTriggerPayload(checks []model.PollCheck, results []checkResult) string {
	entries := make([]pollResultEntry, len(checks))
	for i, c := range checks {
		entry := pollResultEntry{Tool: c.Tool, Input: c.Input}
		if results[i].Err != nil {
			entry.Error = results[i].Err.Error()
		} else {
			entry.Result = results[i].Output
		}
		entries[i] = entry
	}

	payload, _ := json.Marshal(map[string]any{"poll_results": entries})
	return string(payload)
}

// Wait blocks until all active poll goroutines have exited. Call after
// cancelling the root context to drain cleanly during shutdown.
func (p *Poller) Wait() {
	p.wg.Wait()
}

// Stop cancels all active poll goroutines and waits for them to exit.
// It is equivalent to cancelling the context passed to Start, but is
// provided as an explicit method for callers that do not own the context.
func (p *Poller) Stop() {
	p.mu.Lock()
	for _, cancel := range p.loops {
		cancel()
	}
	p.mu.Unlock()
	p.wg.Wait()
}
