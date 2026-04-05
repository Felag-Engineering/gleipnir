package trigger

import (
	"context"
	"crypto/sha256"
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
)

const (
	// reconcileInterval is how often the Poller checks for newly created,
	// paused, or deleted poll policies. A newly created poll policy may take
	// up to this long to start polling.
	reconcileInterval = 60 * time.Second

	// maxBackoffMultiplier caps the exponential back-off at min(10*interval, 1h).
	maxBackoffMultiplier = 10
	maxBackoffCap        = time.Hour
)

// Poller manages one goroutine per active poll policy. Each goroutine loops
// independently, calling the policy's configured MCP tool on each interval tick.
// When the tool returns a non-empty result, a run is launched.
type Poller struct {
	store        *db.Store
	launcher     *RunLauncher
	toolResolver toolResolver
	mu           sync.Mutex             // protects the loops map
	loops        map[string]context.CancelFunc // policyID -> cancel func for that goroutine
	wg           sync.WaitGroup         // tracks all poll loop goroutines
}

// NewPoller returns a Poller ready to be started. resolver is used to call
// the poll tool outside of any agent run context.
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
// On each iteration it waits until next_poll_at, calls the poll tool, and
// decides whether to launch a run.
func (p *Poller) pollLoop(ctx context.Context, policyID string, parsed *model.ParsedPolicy) {
	for {
		// Determine how long to wait before the next poll.
		delay := p.timeUntilNextPoll(ctx, policyID, parsed.Trigger.Interval)

		// Wait until delay elapses or the context is cancelled.
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}

		// Perform the poll and update state.
		err := p.poll(ctx, policyID, parsed)
		if err != nil {
			slog.Error("poller: poll failed", "policy_id", policyID, "err", err)
			p.recordFailure(ctx, policyID, parsed.Trigger.Interval)
		}
		// On success, poll() already updated the state. On failure,
		// recordFailure() updated it with back-off. Either way, the next
		// iteration calls timeUntilNextPoll to pick up the new next_poll_at.
	}
}

// timeUntilNextPoll returns the duration until next_poll_at. If no state
// exists yet, it returns 0 so the first poll runs immediately.
func (p *Poller) timeUntilNextPoll(ctx context.Context, policyID string, interval time.Duration) time.Duration {
	state, err := p.store.GetPollState(ctx, policyID)
	if errors.Is(err, sql.ErrNoRows) {
		// No state yet — initialize it with next_poll_at = now so we poll immediately.
		now := time.Now().UTC()
		p.upsertState(ctx, policyID, db.UpsertPollStateParams{
			PolicyID:            policyID,
			NextPollAt:          now.Format(config.TimestampFormat),
			ConsecutiveFailures: 0,
			CreatedAt:           now.Format(config.TimestampFormat),
			UpdatedAt:           now.Format(config.TimestampFormat),
		})
		return 0
	}
	if err != nil {
		slog.Warn("poller: failed to read poll state, polling immediately", "policy_id", policyID, "err", err)
		return 0
	}

	next, err := time.Parse(config.TimestampFormat, state.NextPollAt)
	if err != nil {
		// Unparseable timestamp — poll immediately.
		return 0
	}

	delay := time.Until(next)
	if delay < 0 {
		return 0
	}
	return delay
}

// poll calls the configured MCP tool and, if the result is non-empty and
// different from the last known result, launches a run.
func (p *Poller) poll(ctx context.Context, policyID string, parsed *model.ParsedPolicy) error {
	client, toolName, err := p.toolResolver.ResolveToolByName(ctx, parsed.Trigger.PollTool)
	if err != nil {
		return fmt.Errorf("resolve tool: %w", err)
	}

	result, err := client.CallTool(ctx, toolName, parsed.Trigger.PollInput)
	if err != nil {
		return fmt.Errorf("call poll tool: %w", err)
	}
	if result.IsError {
		return fmt.Errorf("poll tool returned an error result")
	}

	if isEmpty(result.Output) {
		slog.Debug("poller: tool returned empty result, no run triggered", "policy_id", policyID)
		p.recordSuccess(ctx, policyID, parsed.Trigger.Interval, nil)
		return nil
	}

	newHash := hashOutput(result.Output)

	// Dedup: skip if the result is identical to the last run.
	state, err := p.store.GetPollState(ctx, policyID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read poll state for dedup: %w", err)
	}
	if err == nil && state.LastResultHash != nil && *state.LastResultHash == newHash {
		slog.Debug("poller: result hash unchanged, no run triggered", "policy_id", policyID)
		p.recordSuccess(ctx, policyID, parsed.Trigger.Interval, &newHash)
		return nil
	}

	// Non-empty, new result — enforce concurrency policy then launch a run.
	if err := p.launcher.CheckConcurrency(ctx, policyID, parsed.Agent.Concurrency); err != nil {
		switch {
		case errors.Is(err, ErrConcurrencySkipActive):
			slog.Info("poller: skipping run, active run exists (concurrency: skip)", "policy_id", policyID)
			p.recordSuccess(ctx, policyID, parsed.Trigger.Interval, &newHash)
			return nil
		case errors.Is(err, ErrConcurrencyQueueActive):
			payload := triggerPayload(result.Output)
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
			p.recordSuccess(ctx, policyID, parsed.Trigger.Interval, &newHash)
			return nil
		case errors.Is(err, ErrConcurrencyNotImplemented):
			slog.Warn("poller: concurrency mode not implemented, skipping",
				"policy_id", policyID, "concurrency", parsed.Agent.Concurrency)
			return nil
		default:
			return fmt.Errorf("concurrency check: %w", err)
		}
	}

	payload := triggerPayload(result.Output)
	launchResult, err := p.launcher.Launch(ctx, LaunchParams{
		PolicyID:       policyID,
		TriggerType:    model.TriggerTypePoll,
		TriggerPayload: payload,
		ParsedPolicy:   parsed,
	})
	if err != nil {
		return fmt.Errorf("launch run: %w", err)
	}

	slog.Info("poller: run launched", "run_id", launchResult.RunID, "policy_id", policyID)
	p.recordSuccess(ctx, policyID, parsed.Trigger.Interval, &newHash)
	return nil
}

// isEmpty returns true when the tool output contains no meaningful data.
// The MCP client wraps tool output in a JSON array of content objects, so the
// output is always of the form [{"type":"text","text":"..."},...].
//
// A result is considered empty when:
//   - The JSON value is null, an empty string, an empty object, or an empty array
//   - The output is a content array where every item's "text" field is empty or absent
//
// All other values — including "false" and "0" as text content — are considered
// non-empty and will trigger a run.
func isEmpty(output json.RawMessage) bool {
	if len(output) == 0 {
		return true
	}

	// JSON null
	if string(output) == "null" {
		return true
	}

	var v any
	if err := json.Unmarshal(output, &v); err != nil {
		// Unparseable output is treated as non-empty.
		return false
	}

	switch typed := v.(type) {
	case string:
		return typed == ""
	case map[string]any:
		return len(typed) == 0
	case []any:
		if len(typed) == 0 {
			return true
		}
		// MCP content array: treat as empty only if every item has an absent or
		// empty text field AND no item has a non-text field that could carry data
		// (e.g. image content). An item with type="text" and absent/empty text
		// is considered empty. An item with a non-text type is considered non-empty.
		for _, item := range typed {
			obj, ok := item.(map[string]any)
			if !ok {
				return false // non-object element — treat whole array as non-empty
			}
			itemType, _ := obj["type"].(string)
			if itemType != "text" && itemType != "" {
				// Non-text content (image, resource, etc.) is always non-empty.
				return false
			}
			text, hasText := obj["text"]
			if !hasText {
				// text field absent (omitempty on empty string) — treat as empty text, continue.
				continue
			}
			textStr, ok := text.(string)
			if !ok || textStr != "" {
				return false // non-string or non-empty text — treat as non-empty
			}
		}
		// All items had absent or empty text.
		return true
	}

	// Numbers, booleans (including false), and other types are non-empty.
	return false
}

// hashOutput returns the SHA-256 hex digest of the raw JSON output.
// Used for result deduplication across poll intervals.
func hashOutput(output json.RawMessage) string {
	sum := sha256.Sum256(output)
	return fmt.Sprintf("%x", sum)
}

// triggerPayload wraps the raw poll tool output in a JSON object so the
// trigger_payload column contains a consistent JSON structure. The agent
// receives this as its first user message.
func triggerPayload(output json.RawMessage) string {
	payload, err := json.Marshal(map[string]json.RawMessage{
		"poll_result": output,
	})
	if err != nil {
		// Wrapping a valid json.RawMessage cannot fail; this branch is unreachable.
		return `{"poll_result":null}`
	}
	return string(payload)
}

// recordSuccess writes a successful poll result to the poll_states table.
// hash may be nil if the result was empty (no run triggered). When hash is
// nil we preserve the previous LastResultHash so that a subsequent identical
// non-empty result still deduplicates correctly: empty poll -> same result ->
// without this, the hash would have been cleared and the run fired twice.
func (p *Poller) recordSuccess(ctx context.Context, policyID string, interval time.Duration, hash *string) {
	now := time.Now().UTC()
	next := now.Add(interval)
	nowStr := now.Format(config.TimestampFormat)

	if hash == nil {
		// Empty result — preserve the existing hash so dedup still works on the
		// next non-empty poll with the same output.
		if state, err := p.store.GetPollState(ctx, policyID); err == nil {
			hash = state.LastResultHash
		}
	}

	p.upsertState(ctx, policyID, db.UpsertPollStateParams{
		PolicyID:            policyID,
		LastPollAt:          &nowStr,
		LastResultHash:      hash,
		ConsecutiveFailures: 0,
		NextPollAt:          next.Format(config.TimestampFormat),
		CreatedAt:           nowStr,
		UpdatedAt:           nowStr,
	})
}

// recordFailure increments consecutive_failures and computes an exponential
// back-off for next_poll_at: 2^failures * interval, capped at min(10*interval, 1h).
// LastResultHash and LastPollAt are preserved from the existing state so that
// dedup still works after the error clears: if the tool errored between two
// identical successful results the hash must not be lost.
func (p *Poller) recordFailure(ctx context.Context, policyID string, interval time.Duration) {
	state, err := p.store.GetPollState(ctx, policyID)
	var failures int64
	var lastResultHash *string
	var lastPollAt *string
	if err == nil {
		failures = state.ConsecutiveFailures
		lastResultHash = state.LastResultHash
		lastPollAt = state.LastPollAt
	}
	failures++

	backoff := computeBackoff(failures, interval)
	now := time.Now().UTC()
	next := now.Add(backoff)
	nowStr := now.Format(config.TimestampFormat)
	p.upsertState(ctx, policyID, db.UpsertPollStateParams{
		PolicyID:            policyID,
		LastResultHash:      lastResultHash,
		LastPollAt:          lastPollAt,
		ConsecutiveFailures: failures,
		NextPollAt:          next.Format(config.TimestampFormat),
		CreatedAt:           nowStr,
		UpdatedAt:           nowStr,
	})
}

// computeBackoff returns 2^failures * interval, capped at min(10*interval, 1h).
func computeBackoff(failures int64, interval time.Duration) time.Duration {
	cap := time.Duration(maxBackoffMultiplier) * interval
	if cap > maxBackoffCap {
		cap = maxBackoffCap
	}

	// 2^failures, but stop shifting when we'd overflow or exceed the cap.
	multiplier := int64(1) << failures
	backoff := time.Duration(multiplier) * interval
	if backoff > cap || backoff < 0 { // backoff < 0 catches integer overflow
		return cap
	}
	return backoff
}

// upsertState persists poll state, logging on error (caller is a background goroutine).
func (p *Poller) upsertState(ctx context.Context, policyID string, params db.UpsertPollStateParams) {
	if err := p.store.UpsertPollState(ctx, params); err != nil {
		slog.Error("poller: failed to update poll state", "policy_id", policyID, "err", err)
	}
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
