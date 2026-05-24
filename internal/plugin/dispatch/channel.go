package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/infra/event"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/audience"
	channelv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/channel/v1"
	commonv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/common/v1"
)

// RouteContext carries the run/policy/tool identifiers that accompany a
// channel dispatch.  It maps to the commonv1.RequestContext that is sent to
// the plugin with every RPC.
type RouteContext struct {
	RunID    string
	PolicyID string
	ToolName string
}

// DispatcherConfig holds all tunable parameters for a Dispatcher.
type DispatcherConfig struct {
	// Queries is the sqlc query layer used for audience lookup and request persistence.
	Queries *db.Queries

	// Connect returns a gRPC connection to the named plugin instance.
	// Reuses the ConnFactory alias defined in pool.go.
	// Not called when NewChannelClient is set.
	Connect ConnFactory

	// Publisher, if non-nil, receives audit events on Notify failures.
	Publisher event.Publisher

	// NotifyTimeout is the per-Notify-call deadline.  Defaults to 10s if zero.
	NotifyTimeout time.Duration

	// PreAckTimeout is the deadline given to the plugin to return its
	// synchronous pre-ack from a Request RPC.  Defaults to 5s if zero.
	PreAckTimeout time.Duration

	// WriteRunStep persists a run step into the audit log.  The signature is
	// intentionally narrow so this package never needs to import
	// internal/execution/agent (ADR package-boundary constraint).
	//
	//   ctx      — caller's context
	//   runID    — run to attach the step to
	//   stepType — e.g. "feedback_dispatch_error", "plugin_request_timeout"
	//   payload  — arbitrary JSON-serialisable map
	WriteRunStep func(ctx context.Context, runID, stepType string, payload map[string]interface{}) error

	// NewChannelClient is an injectable factory for the gRPC channel client.
	// nil means use Connect + channelv1.NewChannelServiceClient (production).
	// Tests set this to inject a fake without standing up a real gRPC server.
	NewChannelClient func(instanceName string) channelv1.ChannelServiceClient
}

// Dispatcher routes ChannelService.Notify and ChannelService.Request calls to
// plugin instances.
//
// Package boundary: internal/plugin must not import internal/execution/agent.
// WriteRunStep is injected via DispatcherConfig to preserve that boundary.
type Dispatcher struct {
	cfg DispatcherConfig

	// waitersMu guards the waiters map.
	waitersMu sync.Mutex
	// waiters maps requestID → buffered channel that carries the resolved
	// response JSON string.  The channel has capacity 1 so Resolve never
	// blocks even when the caller has already timed out.
	waiters map[string]chan string
}

// NewDispatcher creates a Dispatcher ready to use.
func NewDispatcher(cfg DispatcherConfig) *Dispatcher {
	if cfg.NotifyTimeout == 0 {
		cfg.NotifyTimeout = 10 * time.Second
	}
	if cfg.PreAckTimeout == 0 {
		cfg.PreAckTimeout = 5 * time.Second
	}
	return &Dispatcher{
		cfg:     cfg,
		waiters: make(map[string]chan string),
	}
}

// channelClient returns a ChannelServiceClient for the given instance.
// When NewChannelClient is set (e.g. in tests), the factory is called directly
// without going through Connect.  In production, Connect is used to obtain a
// gRPC connection which is then wrapped with channelv1.NewChannelServiceClient.
func (d *Dispatcher) channelClient(instanceName string) (channelv1.ChannelServiceClient, error) {
	if d.cfg.NewChannelClient != nil {
		return d.cfg.NewChannelClient(instanceName), nil
	}
	conn, err := d.cfg.Connect(instanceName)
	if err != nil {
		return nil, fmt.Errorf("connecting to plugin instance %q: %w", instanceName, err)
	}
	return channelv1.NewChannelServiceClient(conn), nil
}

// Notify dispatches a fire-and-forget notification to every audience entry
// that has notify: true.  Failures are logged and metric-counted but never
// propagated — the run is unaffected regardless of how many entries fail.
// Worst-case wall-clock is bounded by cfg.NotifyTimeout.
func (d *Dispatcher) Notify(ctx context.Context, audienceID string, rc RouteContext, eventType, payloadJSON string) error {
	rawRows, err := d.cfg.Queries.GetPluginAudienceWithEntries(ctx, audienceID)
	if err != nil {
		return fmt.Errorf("get audience %s: %w", audienceID, err)
	}

	effective, err := audience.Resolve(rawRows)
	if err != nil {
		return fmt.Errorf("resolve audience %s: %w", audienceID, err)
	}

	// Collect the entries that are eligible for Notify dispatch.
	type target struct {
		entryID      string
		instanceName string
		instanceID   string
		pluginID     string
		configJSON   string
	}
	var targets []target
	for _, ae := range effective {
		if !ae.Notify {
			continue
		}
		// Skip the synthetic in-app entry — inAppChannel.Notify is a no-op
		// and there is no plugin_instances row to look up.
		if ae.Auto && ae.PluginInstanceID == audience.InAppPluginID {
			continue
		}
		inst, err := d.cfg.Queries.GetPluginInstanceByID(ctx, ae.PluginInstanceID)
		if err != nil {
			slog.Warn("notify: could not load plugin instance",
				"entry_id", ae.EntryID,
				"instance_id", ae.PluginInstanceID,
				"err", err,
			)
			continue
		}
		targets = append(targets, target{
			entryID:      ae.EntryID,
			instanceName: inst.InstanceName,
			instanceID:   inst.ID,
			pluginID:     inst.PluginID,
			configJSON:   ae.ConfigJSON,
		})
	}

	if len(targets) == 0 {
		return nil
	}

	// All goroutines share a single NotifyTimeout-bounded context so the
	// worst-case wall clock is bounded regardless of fan-out width.
	notifyCtx, cancel := context.WithTimeout(ctx, d.cfg.NotifyTimeout)
	defer cancel()

	var wg sync.WaitGroup
	for _, t := range targets {
		t := t // capture loop variable
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.notifyOne(notifyCtx, rc, t.instanceName, t.instanceID, t.pluginID, t.configJSON, eventType, payloadJSON)
		}()
	}
	wg.Wait()
	return nil
}

// notifyOne performs the Notify RPC for a single audience entry.  Any failure
// is logged and metric-counted but never returned; the caller (Notify) ignores
// per-entry errors per spec §6.2.
func (d *Dispatcher) notifyOne(ctx context.Context, rc RouteContext, instanceName, instanceID, pluginID, configJSON, eventType, payloadJSON string) {
	client, err := d.channelClient(instanceName)
	if err != nil {
		slog.Warn("notify: connect failed",
			"instance", instanceName,
			"run_id", rc.RunID,
			"err", err,
		)
		incRPCError("Notify", pluginID, instanceID)
		d.publishAuditEvent(rc.RunID, instanceName, "notify_failed", err.Error())
		return
	}

	start := time.Now()
	resp, rpcErr := client.Notify(ctx, &channelv1.NotifyRequest{
		Context: &commonv1.RequestContext{
			RunId:    rc.RunID,
			PolicyId: rc.PolicyID,
			CallId:   model.NewULID(), // unique call ID per notify invocation
		},
		EventType:         eventType,
		PayloadJson:       payloadJSON,
		ChannelConfigJson: configJSON,
	})
	observeRPC("Notify", pluginID, instanceID, start)

	// Treat any of: gRPC error, ok==false, non-nil error envelope as failure.
	var failureMsg string
	switch {
	case rpcErr != nil:
		failureMsg = rpcErr.Error()
	case resp != nil && !resp.GetOk():
		failureMsg = "plugin returned ok=false"
		if resp.GetError() != nil {
			failureMsg = fmt.Sprintf("plugin returned ok=false: %s", resp.GetError().GetMessage())
		}
	case resp != nil && resp.GetError() != nil:
		failureMsg = fmt.Sprintf("plugin returned error: %s", resp.GetError().GetMessage())
	}

	if failureMsg != "" {
		slog.Warn("notify: RPC failed",
			"instance", instanceName,
			"run_id", rc.RunID,
			"event_type", eventType,
			"err", failureMsg,
		)
		incRPCError("Notify", pluginID, instanceID)
		d.publishAuditEvent(rc.RunID, instanceName, "notify_failed", failureMsg)
	}
}

// Request dispatches a channel request to the first audience entry with
// request: true (in position order).  It inserts a plugin_pending_requests row
// BEFORE making the gRPC call so that a fast plugin callback (WriteAuditStep)
// always finds a row to match against.  It applies a 5s pre-ack deadline; on
// pre-ack success it returns the requestID and RouteToPlugin so the caller can
// block on Wait.
//
// When the first Request-capable entry is the synthetic gleipnir.in-app entry,
// it returns ("", RouteToInApp, nil) without making any gRPC call or inserting
// a DB row — the feedback_requests substrate handles in-app Requests.
//
// On pre-ack failure, it transitions the already-inserted row to timed_out and
// calls WriteRunStep with a "feedback_dispatch_error" step, then returns
// ErrPreAckFailed.  The caller should map this to runstate.TransitionRunFailed.
//
// ErrNoRequestCapableEntry is returned only when disable_in_app_fallback=true
// and the audience has no persisted Request-capable entries (a state the
// audience validator blocks at save time; this is defense-in-depth only).
func (d *Dispatcher) Request(ctx context.Context, audienceID string, rc RouteContext, prompt string, expiresAt *time.Time) (requestID string, outcome RoutingOutcome, err error) {
	rawRows, err := d.cfg.Queries.GetPluginAudienceWithEntries(ctx, audienceID)
	if err != nil {
		return "", 0, fmt.Errorf("get audience %s: %w", audienceID, err)
	}

	effective, err := audience.Resolve(rawRows)
	if err != nil {
		return "", 0, fmt.Errorf("resolve audience %s: %w", audienceID, err)
	}

	// Find the first entry with request capability.
	var firstRequest *audience.EffectiveEntry
	for i := range effective {
		if effective[i].Request {
			firstRequest = &effective[i]
			break
		}
	}

	if firstRequest == nil {
		// Defense-in-depth: validator blocks this at save time when disable=true.
		return "", 0, ErrNoRequestCapableEntry
	}

	// Synthetic in-app entry: no gRPC call, no DB row.
	if firstRequest.Auto && firstRequest.PluginInstanceID == audience.InAppPluginID {
		// TODO(#209): wire RouteToInApp into the caller so it can await
		// the feedback_requests substrate instead of plugin_pending_requests.
		return "", RouteToInApp, nil
	}

	targetInstance, lookupErr := d.cfg.Queries.GetPluginInstanceByID(ctx, firstRequest.PluginInstanceID)
	if lookupErr != nil {
		return "", 0, fmt.Errorf("load plugin instance %q: %w", firstRequest.PluginInstanceID, lookupErr)
	}

	client, err := d.channelClient(targetInstance.InstanceName)
	if err != nil {
		return "", 0, fmt.Errorf("connecting to plugin instance %q: %w", targetInstance.InstanceName, err)
	}

	// §4.2: request_id is instance-scoped, NOT generation-scoped. Never append a
	// generation counter here — the ID must survive plugin hot-reloads so that
	// callbacks from the plugin (WriteAuditStep) can match across generations.
	reqID := model.NewULID()

	// Register the waiter BEFORE the gRPC call so that a very fast Resolve
	// (or a test that calls Resolve synchronously) does not miss the channel.
	// Capacity 1 ensures Resolve never blocks even if Wait has already timed out.
	waiter := make(chan string, 1)
	d.waitersMu.Lock()
	d.waiters[reqID] = waiter
	d.waitersMu.Unlock()

	// Insert the DB row BEFORE the gRPC call so that a fast plugin callback
	// (WriteAuditStep with feedback_response) always finds an existing row.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var expiresAtStr *string
	if expiresAt != nil {
		s := expiresAt.UTC().Format(time.RFC3339Nano)
		expiresAtStr = &s
	}
	var entryID *string
	if firstRequest.EntryID != "" {
		entryID = &firstRequest.EntryID
	}
	if _, dbErr := d.cfg.Queries.CreatePluginPendingRequest(ctx, db.CreatePluginPendingRequestParams{
		ID:               reqID,
		PluginInstanceID: targetInstance.ID,
		RunID:            rc.RunID,
		AudienceEntryID:  entryID,
		ToolName:         rc.ToolName,
		ExpiresAt:        expiresAtStr,
		CreatedAt:        now,
	}); dbErr != nil {
		// Row insert failed; clean up the waiter to avoid leaking the channel.
		d.waitersMu.Lock()
		delete(d.waiters, reqID)
		d.waitersMu.Unlock()
		return "", 0, fmt.Errorf("persist plugin pending request: %w", dbErr)
	}

	preCtx, cancelPre := context.WithTimeout(ctx, d.cfg.PreAckTimeout)
	defer cancelPre()

	start := time.Now()
	resp, rpcErr := client.Request(preCtx, &channelv1.RequestRequest{
		Context: &commonv1.RequestContext{
			RunId:    rc.RunID,
			PolicyId: rc.PolicyID,
			CallId:   model.NewULID(),
		},
		RequestId:         reqID,
		Prompt:            prompt,
		ChannelConfigJson: firstRequest.ConfigJSON,
	})
	observeRPC("Request", targetInstance.PluginID, targetInstance.ID, start)

	// Classify pre-ack failure.
	var preAckFailMsg string
	switch {
	case rpcErr != nil:
		preAckFailMsg = rpcErr.Error()
	case resp != nil && !resp.GetAcked():
		preAckFailMsg = "plugin returned acked=false"
		if resp.GetError() != nil {
			preAckFailMsg = fmt.Sprintf("plugin returned acked=false: %s", resp.GetError().GetMessage())
		}
	case resp != nil && resp.GetError() != nil:
		preAckFailMsg = fmt.Sprintf("plugin returned error: %s", resp.GetError().GetMessage())
	}

	if preAckFailMsg != "" {
		// Deregister the waiter — no one will send on this channel now.
		d.waitersMu.Lock()
		delete(d.waiters, reqID)
		d.waitersMu.Unlock()

		incRPCError("Request", targetInstance.PluginID, targetInstance.ID)

		// The row was inserted before the gRPC call; transition it to timed_out
		// so it does not leak as a dangling pending row.  Swallow
		// ErrTransitionConflict: the scanner may have beaten us to it.
		if transErr := TransitionTimedOut(ctx, d.cfg.Queries, reqID); transErr != nil {
			if !errors.Is(transErr, ErrTransitionConflict) {
				slog.Warn("pre-ack failure: could not transition row to timed_out",
					"request_id", reqID,
					"err", transErr,
				)
			}
		}

		if d.cfg.WriteRunStep != nil {
			_ = d.cfg.WriteRunStep(ctx, rc.RunID, "feedback_dispatch_error", map[string]interface{}{
				"message":    preAckFailMsg,
				"code":       "feedback_dispatch_error",
				"instance":   targetInstance.InstanceName,
				"request_id": reqID,
			})
		}
		return "", 0, fmt.Errorf("%w: %s", ErrPreAckFailed, preAckFailMsg)
	}

	return reqID, RouteToPlugin, nil
}

// Resolve delivers the operator's response to the in-flight Request identified
// by requestID.  It sends the response on the buffered waiter channel and
// advances the persisted status to "resolved".
//
// Return values:
//   - (true, nil)  — CAS transition succeeded; waiter notified.
//   - (false, nil) — ErrTransitionConflict from TransitionResolved: the scanner
//     already timed out the row; caller should treat this as a late callback.
//   - (false, ErrUnknownRequestID) — requestID not in the in-memory waiters map;
//     may mean the server restarted (evicted waiter) or the ID was never issued.
//   - (false, err) — unexpected DB error; caller should return Internal.
func (d *Dispatcher) Resolve(ctx context.Context, requestID, responseJSON string) (resolved bool, err error) {
	d.waitersMu.Lock()
	ch, ok := d.waiters[requestID]
	if !ok {
		d.waitersMu.Unlock()
		return false, ErrUnknownRequestID
	}
	delete(d.waiters, requestID)
	d.waitersMu.Unlock()

	// Deliver the response to the waiting Wait call (non-blocking; channel has
	// capacity 1 so this never stalls even if Wait already returned on timeout).
	select {
	case ch <- responseJSON:
	default:
		// Wait already drained or closed — response discarded but that is fine.
	}

	if err := TransitionResolved(ctx, d.cfg.Queries, requestID, responseJSON); err != nil {
		if errors.Is(err, ErrTransitionConflict) {
			// The scanner already timed out this row while we were delivering the
			// response.  The run is already being failed; nothing more to do.
			slog.Debug("resolve: transition conflict — scanner already timed out request",
				"request_id", requestID,
			)
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Wait blocks until the request identified by requestID is resolved, the
// supplied timeout fires, or ctx is cancelled.
//
// When the local timer wins the race: TransitionTimedOut is attempted.  Only
// when CAS reports rows == 1 (this caller won) is WriteRunStep called with a
// "plugin_request_timeout" step — avoiding duplicate steps when the background
// scanner beat us to it.
func (d *Dispatcher) Wait(ctx context.Context, requestID string, timeout time.Duration) (responseJSON string, err error) {
	d.waitersMu.Lock()
	ch, ok := d.waiters[requestID]
	d.waitersMu.Unlock()
	if !ok {
		// Caller may have already called Resolve before Wait; return immediately.
		return "", ErrUnknownRequestID
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case msg := <-ch:
		return msg, nil

	case <-ctx.Done():
		d.waitersMu.Lock()
		delete(d.waiters, requestID)
		d.waitersMu.Unlock()
		return "", ctx.Err()

	case <-timer.C:
		d.waitersMu.Lock()
		delete(d.waiters, requestID)
		d.waitersMu.Unlock()

		// Attempt to claim the timeout via CAS.  Only write the run step when
		// this caller won (rows == 1).  When ErrTransitionConflict (rows == 0)
		// the scanner already wrote the step; writing a second one would
		// duplicate it.
		if transErr := TransitionTimedOut(ctx, d.cfg.Queries, requestID); transErr != nil {
			if errors.Is(transErr, ErrTransitionConflict) {
				// Scanner already timed out this request and wrote the step.
				slog.Debug("wait timer: transition conflict — scanner already timed out request",
					"request_id", requestID,
				)
				return "", fmt.Errorf("plugin request timed out")
			}
			return "", fmt.Errorf("mark plugin request timed out: %w", transErr)
		}

		// CAS winner: write the timeout step to the run trace.
		if d.cfg.WriteRunStep != nil {
			req, dbErr := d.cfg.Queries.GetPluginPendingRequest(ctx, requestID)
			if dbErr == nil {
				_ = d.cfg.WriteRunStep(ctx, req.RunID, "plugin_request_timeout", map[string]interface{}{
					"message":    fmt.Sprintf("plugin request timed out after %s", timeout),
					"code":       "plugin_request_timeout",
					"request_id": requestID,
					"tool_name":  req.ToolName,
				})
			}
		}
		return "", fmt.Errorf("plugin request timed out")
	}
}

// publishAuditEvent emits a generic audit event via the Publisher (if configured).
func (d *Dispatcher) publishAuditEvent(runID, instance, eventType, message string) {
	if d.cfg.Publisher == nil {
		return
	}
	payload := map[string]string{
		"run_id":   runID,
		"instance": instance,
		"message":  message,
	}
	if data, err := json.Marshal(payload); err == nil {
		d.cfg.Publisher.Publish("plugin."+eventType, data)
	}
}
