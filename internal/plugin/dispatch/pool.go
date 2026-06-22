package dispatch

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/felag-engineering/gleipnir/internal/model"
	commonv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/common/v1"
	toolv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/tool/v1"
	sdkproto "github.com/felag-engineering/gleipnir/plugin-sdk/proto"
)

// ConnFactory creates or returns a gRPC connection to the named plugin instance.
// The pool calls this once per instance (lazy, cached). The production
// implementation is a UDS-backed conn via the process.Manager, wired in
// pluginruntime.go (managerConnFactory.Connect); tests supply a stub.
type ConnFactory func(instanceName string) (*grpc.ClientConn, error)

// Config holds all tunable parameters for a Pool.
type Config struct {
	// CallTimeout is the per-call deadline. Defaults to 30s if zero, matching
	// GLEIPNIR_MCP_TIMEOUT.
	CallTimeout time.Duration
	// CancelTimeout is the deadline applied to each Cancel RPC. The host
	// force-disconnects (conn.Close) when this expires (spec §13.8).
	// Defaults to 5s if zero.
	CancelTimeout time.Duration
	// DefaultMaxConcurrent is the per-instance semaphore capacity.
	// Defaults to 50 if zero.
	DefaultMaxConcurrent int
	// DefaultMaxQueueDepth is the per-instance bounded queue depth.
	// Calls beyond this cap are rejected with ErrQueueFull immediately.
	// Defaults to 50 if zero.
	DefaultMaxQueueDepth int
	// PerInstanceMax overrides DefaultMaxConcurrent for named instances.
	PerInstanceMax map[string]int
	// Connect is called once per instance to obtain a gRPC connection.
	Connect ConnFactory
}

// instanceState holds per-plugin-instance connection and concurrency state.
//
// Pool deliberately does NOT use connCache (internal/plugin/dispatch/conncache.go)
// even though both paths share the fast-path/lock/double-check/connect pattern.
// Pool needs per-instance sem, queueGate, and closeOnce fields that are
// intrinsic to the concurrency and force-disconnect semantics (CancelRun closes
// the conn directly via closeOnce, then evicts the entry); folding those concerns
// into the generic cache would couple the helper to Pool's call-lifecycle policy.
// The asymmetry is intentional.
type instanceState struct {
	conn      *grpc.ClientConn
	client    toolv1.ToolServiceClient
	sem       chan struct{} // capacity = max_concurrent_calls
	queueGate chan struct{} // capacity = max_queue_depth; controls queue accounting

	// closeOnce ensures conn.Close() is called at most once. Multiple CancelRun
	// goroutines may each decide to force-disconnect the same connection when their
	// Cancel RPC fails; without this guard the second close is undefined behavior.
	closeOnce sync.Once
}

// inflightCall records the instance backing a single in-flight Call so
// CancelRun can route the Cancel RPC to the right connection.
type inflightCall struct {
	instanceName string
	policyID     string
}

// callBinding is the index entry stored in the callID → binding reverse map.
// It carries everything needed by host-service RPCs that resolve context from a call_id.
type callBinding struct {
	runID        string
	policyID     string
	instanceName string
}

// CallInfo is the public view of a callBinding returned by LookupCall.
// hostsvc uses this to resolve run/policy/instance context from a call_id.
type CallInfo struct {
	RunID        string
	PolicyID     string
	InstanceName string
}

// Pool routes agent tool calls to plugin instances via gRPC. It owns:
//   - lazy connection acquisition via ConnFactory
//   - per-call deadline (Config.CallTimeout)
//   - per-instance concurrency semaphore (Config.DefaultMaxConcurrent)
//   - bounded queue gate (Config.DefaultMaxQueueDepth)
//   - in-flight tracking by (runID, callID) for CancelRun
//
// conn.Close() on Cancel-deadline timeout kills every in-flight RPC on that
// connection (process-wide blast radius for that instance). This is acceptable
// per spec §13.8 v1; a per-call sub-stream model is deferred to the
// subprocess-lifecycle follow-up.
type Pool struct {
	cfg Config

	// instances caches per-instance state (conn + semaphores).
	instancesMu sync.RWMutex
	instances   map[string]*instanceState

	// inflight maps runID → callID → inflightCall.
	// inflightByCallID is the reverse index: callID → callBinding.
	// Both maps are guarded by inflightMu. LookupCall and snapshotInflightForRun
	// acquire RLock; all writes (register, deregister) acquire the full Lock.
	inflightMu       sync.RWMutex
	inflight         map[string]map[string]*inflightCall
	inflightByCallID map[string]callBinding

	// cancelWg tracks goroutines spawned by CancelRun. Pool.Close waits on this
	// before tearing down connections so cancel goroutines never reference an
	// already-closed conn.
	cancelWg sync.WaitGroup
}

// New returns a Pool ready to use. cfg.Connect must be non-nil.
func New(cfg Config) *Pool {
	if cfg.CallTimeout == 0 {
		cfg.CallTimeout = 30 * time.Second
	}
	if cfg.CancelTimeout == 0 {
		cfg.CancelTimeout = 5 * time.Second
	}
	if cfg.DefaultMaxConcurrent == 0 {
		cfg.DefaultMaxConcurrent = 50
	}
	if cfg.DefaultMaxQueueDepth == 0 {
		cfg.DefaultMaxQueueDepth = 50
	}
	return &Pool{
		cfg:              cfg,
		instances:        make(map[string]*instanceState),
		inflight:         make(map[string]map[string]*inflightCall),
		inflightByCallID: make(map[string]callBinding),
	}
}

// getOrCreate returns the cached instanceState for the given instance, or
// lazily creates it via cfg.Connect.
func (p *Pool) getOrCreate(instanceName string) (*instanceState, error) {
	p.instancesMu.RLock()
	st, ok := p.instances[instanceName]
	p.instancesMu.RUnlock()
	if ok {
		return st, nil
	}

	// Not cached; acquire write lock and initialise.
	p.instancesMu.Lock()
	defer p.instancesMu.Unlock()
	// Double-check after acquiring write lock.
	if st, ok = p.instances[instanceName]; ok {
		return st, nil
	}

	conn, err := p.cfg.Connect(instanceName)
	if err != nil {
		return nil, fmt.Errorf("connecting to plugin %q: %w", instanceName, err)
	}

	maxConc := p.cfg.DefaultMaxConcurrent
	if override, ok := p.cfg.PerInstanceMax[instanceName]; ok && override > 0 {
		maxConc = override
	}

	st = &instanceState{
		conn:      conn,
		client:    toolv1.NewToolServiceClient(conn),
		sem:       make(chan struct{}, maxConc),
		queueGate: make(chan struct{}, p.cfg.DefaultMaxQueueDepth),
	}
	p.instances[instanceName] = st
	return st, nil
}

// registerInflight records (runID, callID) → instanceName in the inflight map
// and in the callID reverse index. The caller must deregister via deregisterInflight
// when the call completes.
func (p *Pool) registerInflight(runID, policyID, callID, instanceName string) {
	p.inflightMu.Lock()
	defer p.inflightMu.Unlock()
	if p.inflight[runID] == nil {
		p.inflight[runID] = make(map[string]*inflightCall)
	}
	p.inflight[runID][callID] = &inflightCall{instanceName: instanceName, policyID: policyID}
	p.inflightByCallID[callID] = callBinding{runID: runID, policyID: policyID, instanceName: instanceName}
}

// deregisterInflight removes a single (runID, callID) pair from the inflight map
// and the callID reverse index.
func (p *Pool) deregisterInflight(runID, callID string) {
	p.inflightMu.Lock()
	defer p.inflightMu.Unlock()
	delete(p.inflight[runID], callID)
	if len(p.inflight[runID]) == 0 {
		delete(p.inflight, runID)
	}
	delete(p.inflightByCallID, callID)
}

// InflightCountByInstance returns the number of in-flight tool calls currently
// dispatched to the named plugin instance. This is used by the admin deactivate
// and delete-instance handlers to gate actions that must not disrupt active work.
//
// The scan is O(n) over total in-flight calls, but n is bounded by
// DefaultMaxConcurrent × instanceCount (~50×N) so it is acceptable for
// infrequent admin actions.
func (p *Pool) InflightCountByInstance(instanceName string) int {
	p.inflightMu.RLock()
	defer p.inflightMu.RUnlock()
	count := 0
	for _, b := range p.inflightByCallID {
		if b.instanceName == instanceName {
			count++
		}
	}
	return count
}

// LookupCall returns context information for the in-flight call identified by callID.
// Returns (CallInfo{}, false) when no call with that ID is currently in-flight.
// Safe for concurrent use; reads under inflightMu.RLock so concurrent lookups
// do not serialize with each other (host-RPC hot path).
func (p *Pool) LookupCall(callID string) (CallInfo, bool) {
	p.inflightMu.RLock()
	defer p.inflightMu.RUnlock()
	b, ok := p.inflightByCallID[callID]
	if !ok {
		return CallInfo{}, false
	}
	return CallInfo{RunID: b.runID, PolicyID: b.policyID, InstanceName: b.instanceName}, true
}

// snapshotInflightForRun returns a copy of the inflight map for the given run.
// The copy is safe to iterate outside the lock.
func (p *Pool) snapshotInflightForRun(runID string) map[string]inflightCall {
	p.inflightMu.RLock()
	defer p.inflightMu.RUnlock()
	calls := p.inflight[runID]
	if len(calls) == 0 {
		return nil
	}
	snap := make(map[string]inflightCall, len(calls))
	for callID, ic := range calls {
		snap[callID] = *ic
	}
	return snap
}

// Call dispatches a single tool call to the named plugin instance over gRPC.
//
// Steps:
//  1. Acquire/lazy-init the instance connection.
//  2. Generate a call ID and attempt to claim a queue slot (non-blocking).
//  3. Block on the concurrency semaphore (or parent ctx cancellation).
//  4. Register the call in the inflight map; start the per-call timeout.
//  5. Invoke the gRPC Call RPC.
//  6. Classify the result and return to the caller.
func (p *Pool) Call(ctx context.Context, runID, policyID, instanceName, toolName, inputJSON string) (output string, isError bool, err error) {
	st, err := p.getOrCreate(instanceName)
	if err != nil {
		return "", false, err
	}

	callID := model.NewULID()

	// Step 2: non-blocking queue-slot claim. If the bounded channel is full,
	// the call is immediately rejected — the agent receives a tool_result error
	// and can reason about the backpressure rather than the run stalling.
	select {
	case st.queueGate <- struct{}{}:
		// queue slot claimed
	default:
		return "", false, ErrQueueFull
	}

	// Step 3: blocking semaphore acquire. Release the queue slot once we hold
	// the semaphore (so another caller can enter the queue).
	select {
	case st.sem <- struct{}{}:
		<-st.queueGate // admitted; free the queue slot
	case <-ctx.Done():
		<-st.queueGate
		return "", false, ctx.Err()
	}

	// Always release the semaphore and deregister the inflight entry on exit.
	defer func() {
		<-st.sem
		p.deregisterInflight(runID, callID)
	}()

	// Step 4: register inflight and build the per-call context.
	p.registerInflight(runID, policyID, callID, instanceName)

	callCtx, cancelCall := context.WithTimeout(ctx, p.cfg.CallTimeout)
	defer cancelCall()
	callCtx = metadata.AppendToOutgoingContext(callCtx, sdkproto.CallIDMetadataKey, callID)

	// Step 5: invoke gRPC.
	req := &toolv1.CallRequest{
		Context: &commonv1.RequestContext{
			RunId:    runID,
			PolicyId: policyID,
			CallId:   callID,
		},
		ToolName:  toolName,
		InputJson: inputJSON,
	}

	resp, rpcErr := st.client.Call(callCtx, req)
	if rpcErr != nil {
		// Distinguish per-call timeout from parent-run cancellation.
		// parentCtx is the ctx argument (not callCtx), so we can tell
		// whether the deadline came from us or from the run.
		if status.Code(rpcErr) == codes.DeadlineExceeded && ctx.Err() == nil {
			return "", false, ErrCallTimeout
		}
		if status.Code(rpcErr) == codes.Canceled {
			return "", false, ctx.Err()
		}
		return "", false, fmt.Errorf("plugin %q tool %q: %w", instanceName, toolName, rpcErr)
	}

	// Step 6: successful RPC — check for plugin-side error envelope.
	if resp.GetError() != nil {
		msg := formatErrorEnvelope(instanceName, toolName, resp.GetError())
		return msg, true, nil
	}
	return resp.GetOutputJson(), false, nil
}

// CancelRun issues a Cancel RPC for every in-flight call belonging to runID.
// It fires a goroutine per call; each goroutine applies cfg.CancelTimeout and
// calls conn.Close() if the plugin does not respond in time.
//
// CancelRun returns as soon as all cancel goroutines have been started; it does
// NOT wait for them to finish. Pool.Close waits on the internal WaitGroup so
// cancel goroutines always complete before connections are torn down.
func (p *Pool) CancelRun(runID string) {
	snap := p.snapshotInflightForRun(runID)
	if len(snap) == 0 {
		return
	}

	for callID, ic := range snap {
		callID := callID
		ic := ic

		p.cancelWg.Add(1)
		go func() {
			defer p.cancelWg.Done()

			st, err := p.getOrCreate(ic.instanceName)
			if err != nil {
				// Connection not established; nothing to cancel.
				return
			}

			cancelCtx, cancel := context.WithTimeout(context.Background(), p.cfg.CancelTimeout)
			defer cancel()

			_, rpcErr := st.client.Cancel(cancelCtx, &toolv1.CancelRequest{CallId: callID})
			if rpcErr != nil {
				// If the cancel deadline expired (or the RPC failed), force-disconnect
				// the connection so the in-flight Call RPC is unblocked. This has
				// process-wide blast radius for this instance (spec §13.8 v1).
				slog.Warn("plugin cancel RPC failed; force-disconnecting",
					"instance", ic.instanceName,
					"call_id", callID,
					"run_id", runID,
					"err", rpcErr,
				)
				// closeOnce guards against multiple goroutines (one per call on
				// the same instance) each trying to close the same connection.
				st.closeOnce.Do(func() {
					if closeErr := st.conn.Close(); closeErr != nil {
						slog.Warn("plugin conn.Close after cancel timeout",
							"instance", ic.instanceName,
							"run_id", runID,
							"call_id", callID,
							"err", closeErr,
						)
					}
				})
				// Remove the cached (now-closed) connection so the next call
				// triggers a fresh ConnFactory call.
				p.instancesMu.Lock()
				delete(p.instances, ic.instanceName)
				p.instancesMu.Unlock()
			}
		}()
	}
}

// Close cancels all in-flight runs (best-effort) and closes all connections.
// Used during host shutdown.
func (p *Pool) Close() error {
	// Collect all in-flight run IDs under read lock; CancelRun acquires its own
	// locks internally so we release early.
	p.inflightMu.RLock()
	runIDs := make([]string, 0, len(p.inflight))
	for runID := range p.inflight {
		runIDs = append(runIDs, runID)
	}
	p.inflightMu.RUnlock()

	for _, runID := range runIDs {
		p.CancelRun(runID)
	}

	// Wait for all cancel goroutines to finish before closing connections.
	// Without this, a cancel goroutine that races against Close could call
	// conn.Close() on a connection that Close has already torn down.
	p.cancelWg.Wait()

	// Close all cached connections.
	p.instancesMu.Lock()
	defer p.instancesMu.Unlock()
	var firstErr error
	for name, st := range p.instances {
		// closeOnce ensures we don't double-close a connection that a cancel
		// goroutine already force-closed before Pool.Close ran.
		st.closeOnce.Do(func() {
			if err := st.conn.Close(); err != nil && firstErr == nil {
				firstErr = fmt.Errorf("closing connection to plugin %q: %w", name, err)
			}
		})
	}
	p.instances = make(map[string]*instanceState)
	return firstErr
}

// formatErrorEnvelope converts a plugin-side ErrorEnvelope to a human-readable
// string that the agent can reason about.
func formatErrorEnvelope(instanceName, toolName string, env *commonv1.ErrorEnvelope) string {
	if env.GetMessage() != "" {
		return fmt.Sprintf("plugin %q tool %q error: %s", instanceName, toolName, env.GetMessage())
	}
	return fmt.Sprintf("plugin %q tool %q returned an error (code %v)", instanceName, toolName, env.GetCode())
}
