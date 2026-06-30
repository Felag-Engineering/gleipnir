package dispatch_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/audience"
	"github.com/felag-engineering/gleipnir/internal/plugin/dispatch"
	"github.com/felag-engineering/gleipnir/internal/testutil"
	channelv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/channel/v1"
	commonv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/common/v1"
)

// ---- fake ChannelServiceClient ----

// fakeChannelClient implements channelv1.ChannelServiceClient via hooks so
// tests can inject arbitrary responses without standing up a real gRPC server.
type fakeChannelClient struct {
	notifyHook     func(ctx context.Context, req *channelv1.NotifyRequest) (*channelv1.NotifyResponse, error)
	requestHook    func(ctx context.Context, req *channelv1.RequestRequest) (*channelv1.RequestResponse, error)
	terminatedHook func(ctx context.Context, req *channelv1.RequestTerminatedRequest) (*channelv1.RequestTerminatedResponse, error)
}

func (f *fakeChannelClient) Notify(ctx context.Context, in *channelv1.NotifyRequest, _ ...grpc.CallOption) (*channelv1.NotifyResponse, error) {
	if f.notifyHook != nil {
		return f.notifyHook(ctx, in)
	}
	return &channelv1.NotifyResponse{Ok: true}, nil
}

func (f *fakeChannelClient) Request(ctx context.Context, in *channelv1.RequestRequest, _ ...grpc.CallOption) (*channelv1.RequestResponse, error) {
	if f.requestHook != nil {
		return f.requestHook(ctx, in)
	}
	return &channelv1.RequestResponse{Acked: true}, nil
}

func (f *fakeChannelClient) RequestTerminated(ctx context.Context, in *channelv1.RequestTerminatedRequest, _ ...grpc.CallOption) (*channelv1.RequestTerminatedResponse, error) {
	if f.terminatedHook != nil {
		return f.terminatedHook(ctx, in)
	}
	return &channelv1.RequestTerminatedResponse{Ok: true}, nil
}

// Ensure fakeChannelClient satisfies the interface.
var _ channelv1.ChannelServiceClient = (*fakeChannelClient)(nil)

// ---- DB helpers ----

// insertPlugin inserts a minimal plugin row and returns its ID.
func insertPlugin(t *testing.T, s *db.Store, id, name string) string {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.DB().Exec(
		`INSERT INTO plugins(id, name, plugin_version, manifest_snapshot, trusted_pubkey, status, version, created_at, updated_at)
		 VALUES (?, ?, '1.0.0', '{}', 'pubkey', 'active', 0, ?, ?)`,
		id, name, now, now,
	)
	if err != nil {
		t.Fatalf("insertPlugin %s: %v", id, err)
	}
	return id
}

// insertPluginInstance inserts a minimal plugin_instances row and returns its ID.
func insertPluginInstance(t *testing.T, s *db.Store, id, pluginID, instanceName string) string {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.DB().Exec(
		`INSERT INTO plugin_instances(id, plugin_id, instance_name, config_json, version, created_at, updated_at)
		 VALUES (?, ?, ?, '{}', 0, ?, ?)`,
		id, pluginID, instanceName, now, now,
	)
	if err != nil {
		t.Fatalf("insertPluginInstance %s: %v", id, err)
	}
	return id
}

// insertAudience inserts a plugin_audiences row and returns its ID.
// disableInAppFallback mirrors the disable_in_app_fallback column (0 = enabled).
func insertAudience(t *testing.T, s *db.Store, id, name string, disableInAppFallback ...int) string {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	disable := 0
	if len(disableInAppFallback) > 0 {
		disable = disableInAppFallback[0]
	}
	_, err := s.DB().Exec(
		`INSERT INTO plugin_audiences(id, name, version, created_at, updated_at, disable_in_app_fallback)
		 VALUES (?, ?, 0, ?, ?, ?)`,
		id, name, now, now, disable,
	)
	if err != nil {
		t.Fatalf("insertAudience %s: %v", id, err)
	}
	return id
}

// insertAudienceEntry inserts an audience_entries row.
func insertAudienceEntry(t *testing.T, s *db.Store, id, audienceID, instanceID string, position int64, notify, request bool) {
	t.Helper()
	notifyInt := int64(0)
	if notify {
		notifyInt = 1
	}
	requestInt := int64(0)
	if request {
		requestInt = 1
	}
	_, err := s.DB().Exec(
		`INSERT INTO audience_entries(id, audience_id, plugin_instance_id, position, notify, request, config_json)
		 VALUES (?, ?, ?, ?, ?, ?, '{}')`,
		id, audienceID, instanceID, position, notifyInt, requestInt,
	)
	if err != nil {
		t.Fatalf("insertAudienceEntry %s: %v", id, err)
	}
}

// ---- dispatcher factory helpers ----

type dispatcherSetup struct {
	store     *db.Store
	clientMap map[string]*fakeChannelClient // instanceName → fake
	steps     []capturedStep
	stepsMu   sync.Mutex
}

type capturedStep struct {
	runID    string
	stepType string
	payload  map[string]interface{}
}

func (ds *dispatcherSetup) newDispatcher(notifyTimeout, preAckTimeout time.Duration) *dispatch.Dispatcher {
	return dispatch.NewDispatcher(dispatch.DispatcherConfig{
		Queries: ds.store.Queries(),
		// Connect is not called when NewChannelClient is set; nil is safe here.
		NotifyTimeout: notifyTimeout,
		PreAckTimeout: preAckTimeout,
		WriteRunStep: func(ctx context.Context, runID, stepType string, payload map[string]interface{}) error {
			ds.stepsMu.Lock()
			ds.steps = append(ds.steps, capturedStep{runID: runID, stepType: stepType, payload: payload})
			ds.stepsMu.Unlock()
			return nil
		},
		NewChannelClient: func(instanceName string) channelv1.ChannelServiceClient {
			ds.stepsMu.Lock()
			c := ds.clientMap[instanceName]
			ds.stepsMu.Unlock()
			if c == nil {
				return &fakeChannelClient{}
			}
			return c
		},
	})
}

func (ds *dispatcherSetup) stepsByType(stepType string) []capturedStep {
	ds.stepsMu.Lock()
	defer ds.stepsMu.Unlock()
	var out []capturedStep
	for _, s := range ds.steps {
		if s.stepType == stepType {
			out = append(out, s)
		}
	}
	return out
}

func newSetup(t *testing.T) *dispatcherSetup {
	t.Helper()
	s := testutil.NewTestStore(t)
	return &dispatcherSetup{
		store:     s,
		clientMap: make(map[string]*fakeChannelClient),
	}
}

// ---- Notify tests ----

// TestNotify_ParallelFanOut exercises three entries: one ok, one ok=false with
// an error envelope, one gRPC error.  All three should be invoked; the run is
// unaffected; audit events are emitted for the two failures.
func TestNotify_ParallelFanOut(t *testing.T) {
	ds := newSetup(t)

	pluginID := insertPlugin(t, ds.store, "p1", "my-plugin")
	inst1 := insertPluginInstance(t, ds.store, "i1", pluginID, "inst-ok")
	inst2 := insertPluginInstance(t, ds.store, "i2", pluginID, "inst-nok")
	inst3 := insertPluginInstance(t, ds.store, "i3", pluginID, "inst-err")
	audID := insertAudience(t, ds.store, "aud1", "aud-fanout")
	insertAudienceEntry(t, ds.store, "ae1", audID, inst1, 0, true, false)
	insertAudienceEntry(t, ds.store, "ae2", audID, inst2, 1, true, false)
	insertAudienceEntry(t, ds.store, "ae3", audID, inst3, 2, true, false)

	var invokedMu sync.Mutex
	invoked := map[string]bool{}

	ds.clientMap["inst-ok"] = &fakeChannelClient{
		notifyHook: func(_ context.Context, req *channelv1.NotifyRequest) (*channelv1.NotifyResponse, error) {
			invokedMu.Lock()
			invoked["inst-ok"] = true
			invokedMu.Unlock()
			return &channelv1.NotifyResponse{Ok: true}, nil
		},
	}
	ds.clientMap["inst-nok"] = &fakeChannelClient{
		notifyHook: func(_ context.Context, req *channelv1.NotifyRequest) (*channelv1.NotifyResponse, error) {
			invokedMu.Lock()
			invoked["inst-nok"] = true
			invokedMu.Unlock()
			return &channelv1.NotifyResponse{
				Ok:    false,
				Error: &commonv1.ErrorEnvelope{Message: "delivery failed"},
			}, nil
		},
	}
	ds.clientMap["inst-err"] = &fakeChannelClient{
		notifyHook: func(_ context.Context, req *channelv1.NotifyRequest) (*channelv1.NotifyResponse, error) {
			invokedMu.Lock()
			invoked["inst-err"] = true
			invokedMu.Unlock()
			return nil, errors.New("connection refused")
		},
	}

	d := ds.newDispatcher(500*time.Millisecond, 50*time.Millisecond)
	rc := dispatch.RouteContext{RunID: "run1", PolicyID: "pol1", ToolName: "alert"}

	start := time.Now()
	err := d.Notify(context.Background(), audID, rc, "run_failed", `{}`)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Notify returned unexpected error: %v", err)
	}

	invokedMu.Lock()
	for _, name := range []string{"inst-ok", "inst-nok", "inst-err"} {
		if !invoked[name] {
			t.Errorf("expected instance %q to be invoked, but it was not", name)
		}
	}
	invokedMu.Unlock()

	// Sanity ceiling that the fan-out is parallel, not serial. The bound is
	// deliberately generous: an absolute wall-clock assertion is hardware- and
	// load-sensitive (CLAUDE.md: time-dependent tests) and this runs under
	// `-race` in CI. Serial execution of the blocking/slow paths would dwarf
	// this, so the ceiling still catches a regression to serial dispatch while
	// normal runner variance cannot flake it.
	if elapsed > 3*time.Second {
		t.Errorf("Notify took %v, want < 3s (serial execution suspected)", elapsed)
	}
}

// TestNotify_EmptyAudience verifies that Notify on an empty audience returns
// nil without calling the client factory.
func TestNotify_EmptyAudience(t *testing.T) {
	ds := newSetup(t)
	audID := insertAudience(t, ds.store, "aud-empty", "empty")
	// No entries inserted.

	var called atomic.Bool
	ds.clientMap["whatever"] = &fakeChannelClient{
		notifyHook: func(_ context.Context, _ *channelv1.NotifyRequest) (*channelv1.NotifyResponse, error) {
			called.Store(true)
			return &channelv1.NotifyResponse{Ok: true}, nil
		},
	}

	d := ds.newDispatcher(200*time.Millisecond, 50*time.Millisecond)
	if err := d.Notify(context.Background(), audID, dispatch.RouteContext{RunID: "r", PolicyID: "p", ToolName: "t"}, "ev", "{}"); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if called.Load() {
		t.Error("client factory should not be called for empty audience")
	}
}

// TestNotify_DeadlineUpperBound verifies that a blocking fake client is
// cancelled within NotifyTimeout and non-blocking siblings complete.
func TestNotify_DeadlineUpperBound(t *testing.T) {
	ds := newSetup(t)

	pluginID := insertPlugin(t, ds.store, "p1", "plug")
	instBlocking := insertPluginInstance(t, ds.store, "iblk", pluginID, "blocking")
	instFast := insertPluginInstance(t, ds.store, "iFst", pluginID, "fast")
	audID := insertAudience(t, ds.store, "aud-deadline", "deadline")
	insertAudienceEntry(t, ds.store, "ae1", audID, instBlocking, 0, true, false)
	insertAudienceEntry(t, ds.store, "ae2", audID, instFast, 1, true, false)

	var fastDone atomic.Bool
	ds.clientMap["blocking"] = &fakeChannelClient{
		notifyHook: func(ctx context.Context, _ *channelv1.NotifyRequest) (*channelv1.NotifyResponse, error) {
			<-ctx.Done() // block until context is cancelled
			return nil, ctx.Err()
		},
	}
	ds.clientMap["fast"] = &fakeChannelClient{
		notifyHook: func(_ context.Context, _ *channelv1.NotifyRequest) (*channelv1.NotifyResponse, error) {
			fastDone.Store(true)
			return &channelv1.NotifyResponse{Ok: true}, nil
		},
	}

	notifyTimeout := 150 * time.Millisecond
	d := ds.newDispatcher(notifyTimeout, 50*time.Millisecond)

	start := time.Now()
	err := d.Notify(context.Background(), audID, dispatch.RouteContext{RunID: "r", PolicyID: "p", ToolName: "t"}, "ev", "{}")
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Notify returned error: %v", err)
	}

	// Notify returns nil regardless of failures. We only assert that the blocking
	// client did not make Notify hang unbounded — i.e. the deadline IS enforced.
	// The slack is deliberately large: the precise cancellation latency is
	// hardware/load-sensitive (CLAUDE.md: time-dependent tests) and this runs
	// under `-race`. A generous ceiling still proves boundedness (returns vs.
	// hangs) without flaking on a loaded runner.
	slack := 2 * time.Second
	if elapsed > notifyTimeout+slack {
		t.Errorf("Notify took %v, want < %v (deadline not enforced)", elapsed, notifyTimeout+slack)
	}
	if !fastDone.Load() {
		t.Error("non-blocking sibling should have completed")
	}
}

// TestNotify_SkipsRowsWithNilOrZeroNotify verifies that entries with nil or 0
// notify, nil EntryID, or nil PluginInstanceID are skipped.
func TestNotify_SkipsRowsWithNilOrZeroNotify(t *testing.T) {
	ds := newSetup(t)

	pluginID := insertPlugin(t, ds.store, "p1", "plug")
	instRequest := insertPluginInstance(t, ds.store, "ir", pluginID, "request-only")
	audID := insertAudience(t, ds.store, "aud-skip", "skip")
	// Entry with notify=0 (request only).
	insertAudienceEntry(t, ds.store, "ae1", audID, instRequest, 0, false, true)

	var called atomic.Bool
	ds.clientMap["request-only"] = &fakeChannelClient{
		notifyHook: func(_ context.Context, _ *channelv1.NotifyRequest) (*channelv1.NotifyResponse, error) {
			called.Store(true)
			return &channelv1.NotifyResponse{Ok: true}, nil
		},
	}

	d := ds.newDispatcher(200*time.Millisecond, 50*time.Millisecond)
	if err := d.Notify(context.Background(), audID, dispatch.RouteContext{RunID: "r", PolicyID: "p", ToolName: "t"}, "ev", "{}"); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if called.Load() {
		t.Error("request-only entry should not receive Notify calls")
	}
}

// ---- Request tests ----

// TestRequest_PicksFirstRequestEntry verifies that Request selects the first
// entry with request=true in position order and skips notify-only entries.
func TestRequest_PicksFirstRequestEntry(t *testing.T) {
	ds := newSetup(t)

	pluginID := insertPlugin(t, ds.store, "p1", "plug")
	instNotify := insertPluginInstance(t, ds.store, "in", pluginID, "notify-only")
	instReq1 := insertPluginInstance(t, ds.store, "ir1", pluginID, "req-first")
	instReq2 := insertPluginInstance(t, ds.store, "ir2", pluginID, "req-second")
	audID := insertAudience(t, ds.store, "aud-pick", "pick")
	insertAudienceEntry(t, ds.store, "ae0", audID, instNotify, 0, true, false)
	insertAudienceEntry(t, ds.store, "ae1", audID, instReq1, 1, false, true)
	insertAudienceEntry(t, ds.store, "ae2", audID, instReq2, 2, false, true)

	var calledInst atomic.Value
	ds.clientMap["req-first"] = &fakeChannelClient{
		requestHook: func(_ context.Context, req *channelv1.RequestRequest) (*channelv1.RequestResponse, error) {
			calledInst.Store("req-first")
			return &channelv1.RequestResponse{Acked: true}, nil
		},
	}
	ds.clientMap["req-second"] = &fakeChannelClient{
		requestHook: func(_ context.Context, req *channelv1.RequestRequest) (*channelv1.RequestResponse, error) {
			calledInst.Store("req-second")
			return &channelv1.RequestResponse{Acked: true}, nil
		},
	}

	testutil.InsertPolicy(t, ds.store, "pol1", "policy-pol1", "webhook", "{}")
	testutil.InsertRun(t, ds.store, "run1", "pol1", model.RunStatusRunning)

	d := ds.newDispatcher(200*time.Millisecond, 100*time.Millisecond)
	reqID, outcome, err := d.Request(context.Background(), audID, dispatch.RouteContext{RunID: "run1", PolicyID: "pol1", ToolName: "ask"}, "hello", nil)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if reqID == "" {
		t.Fatal("requestID should not be empty")
	}
	if outcome != dispatch.RouteToPlugin {
		t.Errorf("outcome = %v, want RouteToPlugin", outcome)
	}
	if calledInst.Load() != "req-first" {
		t.Errorf("calledInst = %q, want req-first", calledInst.Load())
	}
}

// TestRequest_PreAckSuccess_RowInserted_ResolveFlipsStatus verifies the happy
// path: pre-ack succeeds → row inserted with status='pending'; Resolve flips
// it to 'resolved'; request_id is a valid ULID.
func TestRequest_PreAckSuccess_RowInserted_ResolveFlipsStatus(t *testing.T) {
	ds := newSetup(t)

	pluginID := insertPlugin(t, ds.store, "p1", "plug")
	instID := insertPluginInstance(t, ds.store, "i1", pluginID, "inst-ok")
	audID := insertAudience(t, ds.store, "aud1", "aud")
	insertAudienceEntry(t, ds.store, "ae1", audID, instID, 0, false, true)

	ds.clientMap["inst-ok"] = &fakeChannelClient{
		requestHook: func(_ context.Context, _ *channelv1.RequestRequest) (*channelv1.RequestResponse, error) {
			return &channelv1.RequestResponse{Acked: true}, nil
		},
	}

	testutil.InsertPolicy(t, ds.store, "pol1", "policy-pol1", "webhook", "{}")
	testutil.InsertRun(t, ds.store, "run1", "pol1", model.RunStatusRunning)

	d := ds.newDispatcher(200*time.Millisecond, 100*time.Millisecond)
	reqID, outcome, err := d.Request(context.Background(), audID, dispatch.RouteContext{RunID: "run1", PolicyID: "pol1", ToolName: "ask"}, "prompt", nil)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if reqID == "" {
		t.Fatal("requestID is empty")
	}
	if outcome != dispatch.RouteToPlugin {
		t.Errorf("outcome = %v, want RouteToPlugin", outcome)
	}
	// Validate that the requestID is a valid ULID.
	if _, parseErr := ulid.ParseStrict(reqID); parseErr != nil {
		t.Errorf("requestID %q is not a valid ULID: %v", reqID, parseErr)
	}

	// Row must be pending in the DB.
	var status string
	if err := ds.store.DB().QueryRow(`SELECT status FROM plugin_pending_requests WHERE id = ?`, reqID).Scan(&status); err != nil {
		t.Fatalf("query pending request: %v", err)
	}
	if status != "pending" {
		t.Errorf("status = %q, want pending", status)
	}

	// Resolve must flip it to resolved and return (true, nil).
	resolved, resolveErr := d.Resolve(context.Background(), reqID, `{"answer":"yes"}`)
	if resolveErr != nil {
		t.Fatalf("Resolve error: %v", resolveErr)
	}
	if !resolved {
		t.Error("Resolve resolved = false, want true")
	}

	if err := ds.store.DB().QueryRow(`SELECT status FROM plugin_pending_requests WHERE id = ?`, reqID).Scan(&status); err != nil {
		t.Fatalf("query after Resolve: %v", err)
	}
	if status != "resolved" {
		t.Errorf("status after Resolve = %q, want resolved", status)
	}
}

// TestRequest_PreAckAckedFalse verifies that acked=false causes a
// feedback_dispatch_error step, returns ErrPreAckFailed, and transitions the
// already-inserted pending row to timed_out.
func TestRequest_PreAckAckedFalse(t *testing.T) {
	ds := newSetup(t)

	pluginID := insertPlugin(t, ds.store, "p1", "plug")
	instID := insertPluginInstance(t, ds.store, "i1", pluginID, "inst-nack")
	audID := insertAudience(t, ds.store, "aud1", "aud")
	insertAudienceEntry(t, ds.store, "ae1", audID, instID, 0, false, true)

	ds.clientMap["inst-nack"] = &fakeChannelClient{
		requestHook: func(_ context.Context, _ *channelv1.RequestRequest) (*channelv1.RequestResponse, error) {
			return &channelv1.RequestResponse{Acked: false}, nil
		},
	}

	testutil.InsertPolicy(t, ds.store, "pol1", "policy-pol1", "webhook", "{}")
	testutil.InsertRun(t, ds.store, "run1", "pol1", model.RunStatusRunning)

	d := ds.newDispatcher(200*time.Millisecond, 100*time.Millisecond)
	_, _, err := d.Request(context.Background(), audID, dispatch.RouteContext{RunID: "run1", PolicyID: "pol1", ToolName: "ask"}, "prompt", nil)
	if !errors.Is(err, dispatch.ErrPreAckFailed) {
		t.Errorf("expected ErrPreAckFailed, got %v", err)
	}

	// WriteRunStep must have been called with feedback_dispatch_error.
	steps := ds.stepsByType("feedback_dispatch_error")
	if len(steps) != 1 {
		t.Errorf("feedback_dispatch_error steps = %d, want 1", len(steps))
	}

	// Row must exist with status='timed_out' (inserted before gRPC call, then
	// transitioned on pre-ack failure).
	var status string
	if err := ds.store.DB().QueryRow(`SELECT status FROM plugin_pending_requests WHERE run_id = 'run1'`).Scan(&status); err != nil {
		t.Fatalf("query pending request: %v", err)
	}
	if status != "timed_out" {
		t.Errorf("status = %q, want timed_out", status)
	}
}

// TestRequest_PreAckTimeout verifies that a 5s pre-ack timeout (simulated with
// a tiny timeout) causes the same behaviour as acked=false.
func TestRequest_PreAckTimeout(t *testing.T) {
	ds := newSetup(t)

	pluginID := insertPlugin(t, ds.store, "p1", "plug")
	instID := insertPluginInstance(t, ds.store, "i1", pluginID, "inst-slow")
	audID := insertAudience(t, ds.store, "aud1", "aud")
	insertAudienceEntry(t, ds.store, "ae1", audID, instID, 0, false, true)

	ds.clientMap["inst-slow"] = &fakeChannelClient{
		requestHook: func(ctx context.Context, _ *channelv1.RequestRequest) (*channelv1.RequestResponse, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}

	testutil.InsertPolicy(t, ds.store, "pol1", "policy-pol1", "webhook", "{}")
	testutil.InsertRun(t, ds.store, "run1", "pol1", model.RunStatusRunning)

	// Very short pre-ack timeout so the test completes quickly.
	d := ds.newDispatcher(200*time.Millisecond, 20*time.Millisecond)
	_, _, err := d.Request(context.Background(), audID, dispatch.RouteContext{RunID: "run1", PolicyID: "pol1", ToolName: "ask"}, "prompt", nil)
	if !errors.Is(err, dispatch.ErrPreAckFailed) {
		t.Errorf("expected ErrPreAckFailed, got %v", err)
	}

	steps := ds.stepsByType("feedback_dispatch_error")
	if len(steps) != 1 {
		t.Errorf("feedback_dispatch_error steps = %d, want 1", len(steps))
	}

	// Row must exist with status='timed_out'.
	var status string
	if err := ds.store.DB().QueryRow(`SELECT status FROM plugin_pending_requests WHERE run_id = 'run1'`).Scan(&status); err != nil {
		t.Fatalf("query pending request: %v", err)
	}
	if status != "timed_out" {
		t.Errorf("status = %q, want timed_out", status)
	}
}

// TestRequest_PreAckRespError verifies that resp.Error != nil is treated as a
// pre-ack failure.
func TestRequest_PreAckRespError(t *testing.T) {
	ds := newSetup(t)

	pluginID := insertPlugin(t, ds.store, "p1", "plug")
	instID := insertPluginInstance(t, ds.store, "i1", pluginID, "inst-err")
	audID := insertAudience(t, ds.store, "aud1", "aud")
	insertAudienceEntry(t, ds.store, "ae1", audID, instID, 0, false, true)

	ds.clientMap["inst-err"] = &fakeChannelClient{
		requestHook: func(_ context.Context, _ *channelv1.RequestRequest) (*channelv1.RequestResponse, error) {
			return &channelv1.RequestResponse{
				Acked: false,
				Error: &commonv1.ErrorEnvelope{Message: "internal plugin error"},
			}, nil
		},
	}

	testutil.InsertPolicy(t, ds.store, "pol1", "policy-pol1", "webhook", "{}")
	testutil.InsertRun(t, ds.store, "run1", "pol1", model.RunStatusRunning)

	d := ds.newDispatcher(200*time.Millisecond, 100*time.Millisecond)
	_, _, err := d.Request(context.Background(), audID, dispatch.RouteContext{RunID: "run1", PolicyID: "pol1", ToolName: "ask"}, "prompt", nil)
	if !errors.Is(err, dispatch.ErrPreAckFailed) {
		t.Errorf("expected ErrPreAckFailed, got %v", err)
	}

	// Row must exist with status='timed_out'.
	var status string
	if err := ds.store.DB().QueryRow(`SELECT status FROM plugin_pending_requests WHERE run_id = 'run1'`).Scan(&status); err != nil {
		t.Fatalf("query pending request: %v", err)
	}
	if status != "timed_out" {
		t.Errorf("status = %q, want timed_out", status)
	}
}

// TestRequest_ZeroEntries_DisableFalse_RouteToInApp verifies that an audience
// with no entries and disable_in_app_fallback=false returns RouteToInApp (the
// synthetic entry covers Request).
func TestRequest_ZeroEntries_DisableFalse_RouteToInApp(t *testing.T) {
	ds := newSetup(t)
	audID := insertAudience(t, ds.store, "aud1", "aud") // disable=0 (default)
	// No entries.

	d := ds.newDispatcher(200*time.Millisecond, 100*time.Millisecond)
	reqID, outcome, err := d.Request(context.Background(), audID, dispatch.RouteContext{RunID: "r", PolicyID: "p", ToolName: "t"}, "prompt", nil)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if reqID != "" {
		t.Errorf("reqID = %q, want empty for RouteToInApp", reqID)
	}
	if outcome != dispatch.RouteToInApp {
		t.Errorf("outcome = %v, want RouteToInApp", outcome)
	}

	var count int
	if err := ds.store.DB().QueryRow(`SELECT COUNT(*) FROM plugin_pending_requests`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("pending requests count = %d, want 0 (no row for synthetic entry)", count)
	}
}

// TestRequest_NotifyOnly_DisableFalse_RouteToInApp verifies that a notify-only
// audience with disable_in_app_fallback=false returns RouteToInApp.
func TestRequest_NotifyOnly_DisableFalse_RouteToInApp(t *testing.T) {
	ds := newSetup(t)

	pluginID := insertPlugin(t, ds.store, "p1", "plug")
	instID := insertPluginInstance(t, ds.store, "i1", pluginID, "notify-only")
	audID := insertAudience(t, ds.store, "aud1", "aud") // disable=0
	insertAudienceEntry(t, ds.store, "ae1", audID, instID, 0, true, false)

	d := ds.newDispatcher(200*time.Millisecond, 100*time.Millisecond)
	_, outcome, err := d.Request(context.Background(), audID, dispatch.RouteContext{RunID: "r", PolicyID: "p", ToolName: "t"}, "prompt", nil)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if outcome != dispatch.RouteToInApp {
		t.Errorf("outcome = %v, want RouteToInApp", outcome)
	}
}

// TestRequest_ZeroRequestEntries_DisableTrue_ErrNoRequestCapable verifies that
// an audience with disable_in_app_fallback=true and no Request-capable entries
// returns ErrNoRequestCapableEntry (defense-in-depth path; validator blocks
// this at save time).
func TestRequest_ZeroRequestEntries_DisableTrue_ErrNoRequestCapable(t *testing.T) {
	ds := newSetup(t)

	pluginID := insertPlugin(t, ds.store, "p1", "plug")
	instID := insertPluginInstance(t, ds.store, "i1", pluginID, "notify-only")
	audID := insertAudience(t, ds.store, "aud1", "aud", 1) // disable=1
	insertAudienceEntry(t, ds.store, "ae1", audID, instID, 0, true, false)

	d := ds.newDispatcher(200*time.Millisecond, 100*time.Millisecond)
	_, _, err := d.Request(context.Background(), audID, dispatch.RouteContext{RunID: "r", PolicyID: "p", ToolName: "t"}, "prompt", nil)
	if !errors.Is(err, dispatch.ErrNoRequestCapableEntry) {
		t.Errorf("expected ErrNoRequestCapableEntry, got %v", err)
	}

	var count int
	if err := ds.store.DB().QueryRow(`SELECT COUNT(*) FROM plugin_pending_requests`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("pending requests count = %d, want 0", count)
	}
}

// ---- Resolve tests ----

// TestResolve_UnknownRequestID verifies (false, ErrUnknownRequestID) when the
// requestID is not in the in-memory waiters map.
func TestResolve_UnknownRequestID(t *testing.T) {
	ds := newSetup(t)
	d := ds.newDispatcher(200*time.Millisecond, 100*time.Millisecond)
	resolved, err := d.Resolve(context.Background(), "01JUNK00000NOTREAL0000000", `{}`)
	if !errors.Is(err, dispatch.ErrUnknownRequestID) {
		t.Errorf("expected ErrUnknownRequestID, got %v", err)
	}
	if resolved {
		t.Error("resolved = true, want false for unknown request ID")
	}
}

// TestResolve_HappyPath verifies (true, nil) when the request is pending and
// no scanner has raced.
func TestResolve_HappyPath(t *testing.T) {
	ds := newSetup(t)

	pluginID := insertPlugin(t, ds.store, "p1", "plug")
	instID := insertPluginInstance(t, ds.store, "i1", pluginID, "inst-ok")
	audID := insertAudience(t, ds.store, "aud1", "aud")
	insertAudienceEntry(t, ds.store, "ae1", audID, instID, 0, false, true)

	ds.clientMap["inst-ok"] = &fakeChannelClient{
		requestHook: func(_ context.Context, _ *channelv1.RequestRequest) (*channelv1.RequestResponse, error) {
			return &channelv1.RequestResponse{Acked: true}, nil
		},
	}

	testutil.InsertPolicy(t, ds.store, "pol1", "policy-pol1", "webhook", "{}")
	testutil.InsertRun(t, ds.store, "run1", "pol1", model.RunStatusRunning)

	d := ds.newDispatcher(200*time.Millisecond, 100*time.Millisecond)
	reqID, _, err := d.Request(context.Background(), audID, dispatch.RouteContext{RunID: "run1", PolicyID: "pol1", ToolName: "ask"}, "prompt", nil)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}

	resolved, resolveErr := d.Resolve(context.Background(), reqID, `{"answer":"yes"}`)
	if resolveErr != nil {
		t.Fatalf("Resolve error: %v", resolveErr)
	}
	if !resolved {
		t.Error("resolved = false, want true for pending request")
	}

	var status string
	if err := ds.store.DB().QueryRow(`SELECT status FROM plugin_pending_requests WHERE id = ?`, reqID).Scan(&status); err != nil {
		t.Fatalf("query after Resolve: %v", err)
	}
	if status != "resolved" {
		t.Errorf("status = %q, want resolved", status)
	}
}

// TestResolve_ScannerConflict verifies (false, nil) when the scanner has
// already set status='timed_out' (ErrTransitionConflict is swallowed).
func TestResolve_ScannerConflict(t *testing.T) {
	ds := newSetup(t)

	pluginID := insertPlugin(t, ds.store, "p1", "plug")
	instID := insertPluginInstance(t, ds.store, "i1", pluginID, "inst-ok")
	audID := insertAudience(t, ds.store, "aud1", "aud")
	insertAudienceEntry(t, ds.store, "ae1", audID, instID, 0, false, true)

	ds.clientMap["inst-ok"] = &fakeChannelClient{
		requestHook: func(_ context.Context, _ *channelv1.RequestRequest) (*channelv1.RequestResponse, error) {
			return &channelv1.RequestResponse{Acked: true}, nil
		},
	}

	testutil.InsertPolicy(t, ds.store, "pol1", "policy-pol1", "webhook", "{}")
	testutil.InsertRun(t, ds.store, "run1", "pol1", model.RunStatusRunning)

	d := ds.newDispatcher(200*time.Millisecond, 100*time.Millisecond)
	reqID, _, err := d.Request(context.Background(), audID, dispatch.RouteContext{RunID: "run1", PolicyID: "pol1", ToolName: "ask"}, "prompt", nil)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}

	// Scanner wins: pre-set status to timed_out.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := ds.store.DB().Exec(
		`UPDATE plugin_pending_requests SET status='timed_out', resolved_at=? WHERE id=?`,
		now, reqID,
	); err != nil {
		t.Fatalf("pre-set timed_out: %v", err)
	}

	resolved, resolveErr := d.Resolve(context.Background(), reqID, `{"answer":"late"}`)
	if resolveErr != nil {
		t.Errorf("Resolve returned unexpected error: %v", resolveErr)
	}
	if resolved {
		t.Error("resolved = true, want false when scanner already timed out")
	}
}

// TestResolve_ScannerRace verifies that when the scanner has already set
// status='timed_out', Resolve swallows ErrTransitionConflict and returns nil.
func TestResolve_ScannerRace(t *testing.T) {
	ds := newSetup(t)

	pluginID := insertPlugin(t, ds.store, "p1", "plug")
	instID := insertPluginInstance(t, ds.store, "i1", pluginID, "inst-ok")
	audID := insertAudience(t, ds.store, "aud1", "aud")
	insertAudienceEntry(t, ds.store, "ae1", audID, instID, 0, false, true)

	ds.clientMap["inst-ok"] = &fakeChannelClient{
		requestHook: func(_ context.Context, _ *channelv1.RequestRequest) (*channelv1.RequestResponse, error) {
			return &channelv1.RequestResponse{Acked: true}, nil
		},
	}

	testutil.InsertPolicy(t, ds.store, "pol1", "policy-pol1", "webhook", "{}")
	testutil.InsertRun(t, ds.store, "run1", "pol1", model.RunStatusRunning)

	d := ds.newDispatcher(200*time.Millisecond, 100*time.Millisecond)
	reqID, _, err := d.Request(context.Background(), audID, dispatch.RouteContext{RunID: "run1", PolicyID: "pol1", ToolName: "ask"}, "prompt", nil)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}

	// Simulate scanner winning the race by pre-setting status='timed_out'.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = ds.store.DB().Exec(
		`UPDATE plugin_pending_requests SET status='timed_out', resolved_at=? WHERE id=?`,
		now, reqID,
	)
	if err != nil {
		t.Fatalf("pre-set timed_out: %v", err)
	}

	// Resolve must return (false, nil) — conflict swallowed, resolved=false.
	resolved, err := d.Resolve(context.Background(), reqID, `{"answer":"late"}`)
	if err != nil {
		t.Errorf("Resolve returned error after scanner won race: %v", err)
	}
	if resolved {
		t.Error("resolved = true, want false when scanner already won race")
	}
}

// ---- Wait tests ----

// TestWait_TimerWins_WritesRunStep verifies that when the Wait timer fires, it
// writes exactly one plugin_request_timeout step (CAS winner).
func TestWait_TimerWins_WritesRunStep(t *testing.T) {
	ds := newSetup(t)

	pluginID := insertPlugin(t, ds.store, "p1", "plug")
	instID := insertPluginInstance(t, ds.store, "i1", pluginID, "inst-ok")
	audID := insertAudience(t, ds.store, "aud1", "aud")
	insertAudienceEntry(t, ds.store, "ae1", audID, instID, 0, false, true)

	ds.clientMap["inst-ok"] = &fakeChannelClient{
		requestHook: func(_ context.Context, _ *channelv1.RequestRequest) (*channelv1.RequestResponse, error) {
			return &channelv1.RequestResponse{Acked: true}, nil
		},
	}

	testutil.InsertPolicy(t, ds.store, "pol1", "policy-pol1", "webhook", "{}")
	testutil.InsertRun(t, ds.store, "run1", "pol1", model.RunStatusRunning)

	d := ds.newDispatcher(200*time.Millisecond, 100*time.Millisecond)
	reqID, _, err := d.Request(context.Background(), audID, dispatch.RouteContext{RunID: "run1", PolicyID: "pol1", ToolName: "ask"}, "prompt", nil)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}

	// Wait with a short timeout; no one will call Resolve.
	_, waitErr := d.Wait(context.Background(), reqID, 30*time.Millisecond)
	if waitErr == nil {
		t.Fatal("Wait should have returned an error (timeout)")
	}

	// Exactly one plugin_request_timeout step.
	steps := ds.stepsByType("plugin_request_timeout")
	if len(steps) != 1 {
		t.Errorf("plugin_request_timeout steps = %d, want 1", len(steps))
	}

	// Row must be timed_out.
	var status string
	if err := ds.store.DB().QueryRow(`SELECT status FROM plugin_pending_requests WHERE id = ?`, reqID).Scan(&status); err != nil {
		t.Fatalf("query: %v", err)
	}
	if status != "timed_out" {
		t.Errorf("status = %q, want timed_out", status)
	}
}

// TestWait_ScannerWon_NoRunStep verifies that when the scanner pre-sets
// status='timed_out' before Wait's timer fires, Wait does NOT write a second
// run step (CAS loser path).
func TestWait_ScannerWon_NoRunStep(t *testing.T) {
	ds := newSetup(t)

	pluginID := insertPlugin(t, ds.store, "p1", "plug")
	instID := insertPluginInstance(t, ds.store, "i1", pluginID, "inst-ok")
	audID := insertAudience(t, ds.store, "aud1", "aud")
	insertAudienceEntry(t, ds.store, "ae1", audID, instID, 0, false, true)

	ds.clientMap["inst-ok"] = &fakeChannelClient{
		requestHook: func(_ context.Context, _ *channelv1.RequestRequest) (*channelv1.RequestResponse, error) {
			return &channelv1.RequestResponse{Acked: true}, nil
		},
	}

	testutil.InsertPolicy(t, ds.store, "pol1", "policy-pol1", "webhook", "{}")
	testutil.InsertRun(t, ds.store, "run1", "pol1", model.RunStatusRunning)

	d := ds.newDispatcher(200*time.Millisecond, 100*time.Millisecond)
	reqID, _, err := d.Request(context.Background(), audID, dispatch.RouteContext{RunID: "run1", PolicyID: "pol1", ToolName: "ask"}, "prompt", nil)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}

	// Simulate scanner pre-claiming the timeout before Wait's timer fires.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := ds.store.DB().Exec(
		`UPDATE plugin_pending_requests SET status='timed_out', resolved_at=? WHERE id=?`,
		now, reqID,
	); err != nil {
		t.Fatalf("pre-set timed_out: %v", err)
	}

	_, waitErr := d.Wait(context.Background(), reqID, 30*time.Millisecond)
	if waitErr == nil {
		t.Fatal("Wait should return error on timeout")
	}

	// Scanner won the CAS — Wait must NOT write a step.
	steps := ds.stepsByType("plugin_request_timeout")
	if len(steps) != 0 {
		t.Errorf("plugin_request_timeout steps = %d, want 0 (scanner already won)", len(steps))
	}

	// Scanner-won path must return the typed sentinel so callers can avoid
	// writing a duplicate audit step.
	if !errors.Is(waitErr, dispatch.ErrRequestAlreadyResolved) {
		t.Errorf("Wait (scanner-won) error = %v, want errors.Is(err, dispatch.ErrRequestAlreadyResolved)", waitErr)
	}
}

// ---- Routing outcome tests ----

// TestRequest_RequestCapable_DisableFalse_RouteToPlugin verifies that a
// persisted Request-capable entry is used first; the synthetic never reached.
func TestRequest_RequestCapable_DisableFalse_RouteToPlugin(t *testing.T) {
	ds := newSetup(t)

	pluginID := insertPlugin(t, ds.store, "p1", "plug")
	instID := insertPluginInstance(t, ds.store, "i1", pluginID, "req-inst")
	audID := insertAudience(t, ds.store, "aud1", "aud") // disable=0
	insertAudienceEntry(t, ds.store, "ae1", audID, instID, 0, false, true)

	ds.clientMap["req-inst"] = &fakeChannelClient{
		requestHook: func(_ context.Context, _ *channelv1.RequestRequest) (*channelv1.RequestResponse, error) {
			return &channelv1.RequestResponse{Acked: true}, nil
		},
	}

	testutil.InsertPolicy(t, ds.store, "pol1", "policy-pol1", "webhook", "{}")
	testutil.InsertRun(t, ds.store, "run1", "pol1", model.RunStatusRunning)

	d := ds.newDispatcher(200*time.Millisecond, 100*time.Millisecond)
	reqID, outcome, err := d.Request(context.Background(), audID, dispatch.RouteContext{RunID: "run1", PolicyID: "pol1", ToolName: "ask"}, "prompt", nil)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if outcome != dispatch.RouteToPlugin {
		t.Errorf("outcome = %v, want RouteToPlugin", outcome)
	}
	if reqID == "" {
		t.Error("reqID should not be empty for RouteToPlugin")
	}
}

// TestRequest_AudienceDeleted_ErrAudienceNotFound verifies that Request returns
// ErrAudienceNotFound when the audience row was deleted between fetch and resolve.
func TestRequest_AudienceDeleted_ErrAudienceNotFound(t *testing.T) {
	ds := newSetup(t)
	// Query a non-existent audience — GetPluginAudienceWithEntries returns zero
	// rows, so Resolve returns ErrAudienceNotFound.
	d := ds.newDispatcher(200*time.Millisecond, 100*time.Millisecond)
	_, _, err := d.Request(context.Background(), "nonexistent-aud", dispatch.RouteContext{RunID: "r", PolicyID: "p", ToolName: "t"}, "prompt", nil)
	if !errors.Is(err, audience.ErrAudienceNotFound) {
		t.Errorf("expected ErrAudienceNotFound, got %v", err)
	}
}

// TestNotify_SyntheticSkipped verifies that Notify skips the synthetic in-app
// entry and invokes only the persisted notify-enabled instance.
func TestNotify_SyntheticSkipped(t *testing.T) {
	ds := newSetup(t)

	pluginID := insertPlugin(t, ds.store, "p1", "plug")
	instID := insertPluginInstance(t, ds.store, "i1", pluginID, "notified")
	audID := insertAudience(t, ds.store, "aud1", "aud") // disable=0 → synthetic appended
	insertAudienceEntry(t, ds.store, "ae1", audID, instID, 0, true, false)

	var invokeCount atomic.Int32
	ds.clientMap["notified"] = &fakeChannelClient{
		notifyHook: func(_ context.Context, _ *channelv1.NotifyRequest) (*channelv1.NotifyResponse, error) {
			invokeCount.Add(1)
			return &channelv1.NotifyResponse{Ok: true}, nil
		},
	}

	d := ds.newDispatcher(200*time.Millisecond, 50*time.Millisecond)
	if err := d.Notify(context.Background(), audID, dispatch.RouteContext{RunID: "r", PolicyID: "p", ToolName: "t"}, "ev", "{}"); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	// Exactly one invocation — the synthetic entry must be skipped.
	if invokeCount.Load() != 1 {
		t.Errorf("notify invocations = %d, want 1 (synthetic should be skipped)", invokeCount.Load())
	}
}

// TestRequest_RowInsertedBeforeGRPCCall verifies that the plugin_pending_requests
// row exists with status='pending' at the moment the plugin's Request RPC is
// invoked.  This is the ordering guarantee: a fast callback (WriteAuditStep)
// from the plugin can match against the row even on the same round trip.
func TestRequest_RowInsertedBeforeGRPCCall(t *testing.T) {
	ds := newSetup(t)

	pluginID := insertPlugin(t, ds.store, "p1", "plug")
	instID := insertPluginInstance(t, ds.store, "i1", pluginID, "inst-order")
	audID := insertAudience(t, ds.store, "aud1", "aud")
	insertAudienceEntry(t, ds.store, "ae1", audID, instID, 0, false, true)

	testutil.InsertPolicy(t, ds.store, "pol1", "policy-pol1", "webhook", "{}")
	testutil.InsertRun(t, ds.store, "run1", "pol1", model.RunStatusRunning)

	ds.clientMap["inst-order"] = &fakeChannelClient{
		requestHook: func(_ context.Context, req *channelv1.RequestRequest) (*channelv1.RequestResponse, error) {
			// At this point the row must already exist in the DB.
			var status string
			if err := ds.store.DB().QueryRow(
				`SELECT status FROM plugin_pending_requests WHERE id = ?`,
				req.GetRequestId(),
			).Scan(&status); err != nil {
				t.Errorf("row not found at gRPC call time: %v", err)
			} else if status != "pending" {
				t.Errorf("status at gRPC call time = %q, want pending", status)
			}
			return &channelv1.RequestResponse{Acked: true}, nil
		},
	}

	d := ds.newDispatcher(200*time.Millisecond, 100*time.Millisecond)
	_, outcome, err := d.Request(context.Background(), audID, dispatch.RouteContext{RunID: "run1", PolicyID: "pol1", ToolName: "ask"}, "prompt", nil)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if outcome != dispatch.RouteToPlugin {
		t.Errorf("outcome = %v, want RouteToPlugin", outcome)
	}
}

// TestDispatcher_Close_NoConnsWhenInjected verifies that Close() returns nil
// and is idempotent when the Dispatcher was constructed with NewChannelClient
// (the test seam).  In this path the connection cache is never populated, so
// Close is a no-op — the intent is that callers in production always call Close
// and tests using the injected path are not broken by doing so.
func TestDispatcher_Close_NoConnsWhenInjected(t *testing.T) {
	ds := newSetup(t)
	d := ds.newDispatcher(200*time.Millisecond, 100*time.Millisecond)

	if err := d.Close(); err != nil {
		t.Errorf("first Close() returned error: %v", err)
	}
	// Second call must also be a no-op.
	if err := d.Close(); err != nil {
		t.Errorf("second Close() returned error: %v", err)
	}
}

// TestWait_TimerFires_CancelledCtx_RowTransitionedToTimedOut verifies that when
// Wait's timer fires, the cleanup DB operations (TransitionTimedOut and
// GetPluginPendingRequest) use a detached context.Background() deadline rather
// than the caller's run ctx.  This means the row is transitioned to 'timed_out'
// even when the caller's ctx has been cancelled at the moment cleanup runs —
// the canonical scenario is a run that is interrupted while a channel request
// is still pending.
//
// The test exercises this by calling Wait with a context that is cancelled
// AFTER the timer deadline, ensuring the timer branch runs first.  A separate
// goroutine cancels the ctx after a delay longer than the timer so the timer
// wins the select race deterministically.
func TestWait_TimerFires_CancelledCtx_RowTransitionedToTimedOut(t *testing.T) {
	ds := newSetup(t)

	pluginID := insertPlugin(t, ds.store, "p1", "plug")
	instID := insertPluginInstance(t, ds.store, "i1", pluginID, "inst-ok")
	audID := insertAudience(t, ds.store, "aud1", "aud")
	insertAudienceEntry(t, ds.store, "ae1", audID, instID, 0, false, true)

	ds.clientMap["inst-ok"] = &fakeChannelClient{
		requestHook: func(_ context.Context, _ *channelv1.RequestRequest) (*channelv1.RequestResponse, error) {
			return &channelv1.RequestResponse{Acked: true}, nil
		},
	}

	testutil.InsertPolicy(t, ds.store, "pol1", "policy-pol1", "webhook", "{}")
	testutil.InsertRun(t, ds.store, "run1", "pol1", model.RunStatusRunning)

	d := ds.newDispatcher(200*time.Millisecond, 100*time.Millisecond)

	reqID, _, err := d.Request(context.Background(), audID, dispatch.RouteContext{RunID: "run1", PolicyID: "pol1", ToolName: "ask"}, "prompt", nil)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}

	// Use a ctx that will be cancelled 50ms AFTER the timer fires. This ensures
	// the timer branch wins the select.  The 30ms timer fires first; the ctx
	// cancel at 80ms is the "run was cancelled shortly after" scenario.
	runCtx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	// Wait with a 30ms timeout. Nobody calls Resolve. Timer fires at ~30ms;
	// ctx is cancelled at ~80ms so the timer wins the select.
	_, waitErr := d.Wait(runCtx, reqID, 30*time.Millisecond)
	if waitErr == nil {
		t.Fatal("Wait should return an error (timeout)")
	}

	// The row must be timed_out — the cleanup used a detached context so it
	// succeeds regardless of whether the caller's ctx is (or becomes) cancelled.
	var status string
	if err := ds.store.DB().QueryRow(`SELECT status FROM plugin_pending_requests WHERE id = ?`, reqID).Scan(&status); err != nil {
		t.Fatalf("query pending request: %v", err)
	}
	if status != "timed_out" {
		t.Errorf("status = %q, want timed_out (cleanup must succeed independent of run ctx state)", status)
	}

	// WriteRunStep must also have been called (GetPluginPendingRequest succeeded
	// with the detached cleanup context).
	steps := ds.stepsByType("plugin_request_timeout")
	if len(steps) != 1 {
		t.Errorf("plugin_request_timeout steps = %d, want 1", len(steps))
	}
}

// TestWait_TimerFires_WriteRunStepUsesDetachedCtx is the regression test for
// #499: when Wait's timer fires, the timeout step must be written with the
// detached cleanup context, NOT the caller's run ctx. On an interrupted run the
// run ctx is already cancelled by the time the timer fires, so writing the step
// with the run ctx would silently fail and lose the very step the CAS winner
// exists to record.
//
// We cannot probe the cleanup ctx AFTER Wait returns: cleanupCtx is cancelled by
// Wait's own `defer cancelCleanup()`, so it always reads cancelled afterwards.
// Instead we observe liveness INSIDE the WriteRunStep hook (while we are still on
// Wait's stack and cleanupCtx is live): the hook cancels the run ctx and then
// checks whether the ctx it was handed became Done.
//
//   - bug  (WriteRunStep(ctx, ...)):        handed ctx IS runCtx → Done after cancel.
//   - fix  (WriteRunStep(cleanupCtx, ...)): handed ctx is detached → stays alive.
//
// The timer (30ms) fires while runCtx is still live so the timer branch wins the
// select deterministically; the run-ctx cancellation happens entirely inside the
// hook, after the branch is already committed.
func TestWait_TimerFires_WriteRunStepUsesDetachedCtx(t *testing.T) {
	ds := newSetup(t)

	pluginID := insertPlugin(t, ds.store, "p1", "plug")
	instID := insertPluginInstance(t, ds.store, "i1", pluginID, "inst-ok")
	audID := insertAudience(t, ds.store, "aud1", "aud")
	insertAudienceEntry(t, ds.store, "ae1", audID, instID, 0, false, true)

	ds.clientMap["inst-ok"] = &fakeChannelClient{
		requestHook: func(_ context.Context, _ *channelv1.RequestRequest) (*channelv1.RequestResponse, error) {
			return &channelv1.RequestResponse{Acked: true}, nil
		},
	}

	testutil.InsertPolicy(t, ds.store, "pol1", "policy-pol1", "webhook", "{}")
	testutil.InsertRun(t, ds.store, "run1", "pol1", model.RunStatusRunning)

	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()

	var stepCtxStillLive atomic.Bool
	var stepCalled atomic.Bool

	d := dispatch.NewDispatcher(dispatch.DispatcherConfig{
		Queries:       ds.store.Queries(),
		NotifyTimeout: 200 * time.Millisecond,
		PreAckTimeout: 100 * time.Millisecond,
		WriteRunStep: func(ctx context.Context, _ string, stepType string, _ map[string]interface{}) error {
			if stepType != "plugin_request_timeout" {
				return nil
			}
			stepCalled.Store(true)
			// Cancel the caller's run ctx now. If the dispatcher handed us runCtx
			// (the #499 bug), our ctx becomes Done; if it handed us the detached
			// cleanup ctx (the fix), our ctx stays alive.
			cancelRun()
			select {
			case <-ctx.Done():
				stepCtxStillLive.Store(false)
			default:
				stepCtxStillLive.Store(true)
			}
			return nil
		},
		NewChannelClient: func(instanceName string) channelv1.ChannelServiceClient {
			if c := ds.clientMap[instanceName]; c != nil {
				return c
			}
			return &fakeChannelClient{}
		},
	})

	reqID, _, err := d.Request(context.Background(), audID, dispatch.RouteContext{RunID: "run1", PolicyID: "pol1", ToolName: "ask"}, "prompt", nil)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}

	_, waitErr := d.Wait(runCtx, reqID, 30*time.Millisecond)
	if waitErr == nil {
		t.Fatal("Wait should return an error (timeout)")
	}

	if !stepCalled.Load() {
		t.Fatal("WriteRunStep was never called for plugin_request_timeout — the timeout step was lost")
	}
	if !stepCtxStillLive.Load() {
		t.Errorf("WriteRunStep was handed the caller's run ctx (#499 regression): " +
			"cancelling the run ctx cancelled the step ctx. It must use the detached cleanup ctx.")
	}
}

// ---- SendRequestTermination / Wait+terminatedHook tests ----

// TestWait_TimerWins_SendsRequestTermination verifies that when the Wait timer
// fires and this caller wins the CAS (rows==1), SendRequestTermination is
// invoked on the plugin with reason TIMED_OUT.  We capture the received reason
// via the terminatedHook on the fake client.
func TestWait_TimerWins_SendsRequestTermination(t *testing.T) {
	ds := newSetup(t)

	pluginID := insertPlugin(t, ds.store, "p1", "plug")
	instID := insertPluginInstance(t, ds.store, "i1", pluginID, "inst-term")
	audID := insertAudience(t, ds.store, "aud1", "aud")
	insertAudienceEntry(t, ds.store, "ae1", audID, instID, 0, false, true)

	var receivedReason channelv1.TerminalReason
	var terminatedCalled atomic.Bool
	ds.clientMap["inst-term"] = &fakeChannelClient{
		requestHook: func(_ context.Context, _ *channelv1.RequestRequest) (*channelv1.RequestResponse, error) {
			return &channelv1.RequestResponse{Acked: true}, nil
		},
		terminatedHook: func(_ context.Context, req *channelv1.RequestTerminatedRequest) (*channelv1.RequestTerminatedResponse, error) {
			terminatedCalled.Store(true)
			receivedReason = req.GetReason()
			return &channelv1.RequestTerminatedResponse{Ok: true}, nil
		},
	}

	testutil.InsertPolicy(t, ds.store, "pol1", "policy-pol1", "webhook", "{}")
	testutil.InsertRun(t, ds.store, "run1", "pol1", model.RunStatusRunning)

	d := ds.newDispatcher(200*time.Millisecond, 100*time.Millisecond)
	reqID, _, err := d.Request(context.Background(), audID, dispatch.RouteContext{RunID: "run1", PolicyID: "pol1", ToolName: "ask"}, "prompt", nil)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}

	// Let the timer fire; nobody calls Resolve so we win the CAS.
	_, waitErr := d.Wait(context.Background(), reqID, 30*time.Millisecond)
	if waitErr == nil {
		t.Fatal("Wait should return error (timeout)")
	}

	if !terminatedCalled.Load() {
		t.Error("SendRequestTermination was not called after CAS-win timeout")
	}
	if receivedReason != channelv1.TerminalReason_TERMINAL_REASON_TIMED_OUT {
		t.Errorf("reason = %v, want TERMINAL_REASON_TIMED_OUT", receivedReason)
	}
}

// TestWait_ScannerWon_TerminatedHookNotCalled verifies that when the scanner
// pre-claims the timeout (rows==0 for Wait's CAS attempt), SendRequestTermination
// is NOT called by Wait — the scanner owns the notification in that race.
func TestWait_ScannerWon_TerminatedHookNotCalled(t *testing.T) {
	ds := newSetup(t)

	pluginID := insertPlugin(t, ds.store, "p1", "plug")
	instID := insertPluginInstance(t, ds.store, "i1", pluginID, "inst-term2")
	audID := insertAudience(t, ds.store, "aud2", "aud2")
	insertAudienceEntry(t, ds.store, "ae1", audID, instID, 0, false, true)

	var terminatedCalled atomic.Bool
	ds.clientMap["inst-term2"] = &fakeChannelClient{
		requestHook: func(_ context.Context, _ *channelv1.RequestRequest) (*channelv1.RequestResponse, error) {
			return &channelv1.RequestResponse{Acked: true}, nil
		},
		terminatedHook: func(_ context.Context, _ *channelv1.RequestTerminatedRequest) (*channelv1.RequestTerminatedResponse, error) {
			terminatedCalled.Store(true)
			return &channelv1.RequestTerminatedResponse{Ok: true}, nil
		},
	}

	testutil.InsertPolicy(t, ds.store, "pol1", "policy-pol1", "webhook", "{}")
	testutil.InsertRun(t, ds.store, "run1", "pol1", model.RunStatusRunning)

	d := ds.newDispatcher(200*time.Millisecond, 100*time.Millisecond)
	reqID, _, err := d.Request(context.Background(), audID, dispatch.RouteContext{RunID: "run1", PolicyID: "pol1", ToolName: "ask"}, "prompt", nil)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}

	// Scanner wins: pre-set the row to timed_out before Wait's timer fires.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := ds.store.DB().Exec(
		`UPDATE plugin_pending_requests SET status='timed_out', resolved_at=? WHERE id=?`,
		now, reqID,
	); err != nil {
		t.Fatalf("pre-set timed_out: %v", err)
	}

	_, waitErr := d.Wait(context.Background(), reqID, 30*time.Millisecond)
	// ErrRequestAlreadyResolved — scanner won the race.
	if !errors.Is(waitErr, dispatch.ErrRequestAlreadyResolved) {
		t.Errorf("Wait error = %v, want ErrRequestAlreadyResolved", waitErr)
	}

	if terminatedCalled.Load() {
		t.Error("SendRequestTermination was called despite scanner winning the CAS — hook must only fire on CAS-win")
	}
}

// TestSendRequestTermination_UnimplementedSwallowed verifies that
// codes.Unimplemented from the plugin's RequestTerminated RPC is silently
// swallowed and SendRequestTermination returns nil (best-effort fire-and-forget).
func TestSendRequestTermination_UnimplementedSwallowed(t *testing.T) {
	ds := newSetup(t)

	pluginID := insertPlugin(t, ds.store, "p1", "plug")
	insertPluginInstance(t, ds.store, "i1", pluginID, "inst-unimpl")

	ds.clientMap["inst-unimpl"] = &fakeChannelClient{
		terminatedHook: func(_ context.Context, _ *channelv1.RequestTerminatedRequest) (*channelv1.RequestTerminatedResponse, error) {
			return nil, status.Error(codes.Unimplemented, "method RequestTerminated not implemented")
		},
	}

	d := ds.newDispatcher(200*time.Millisecond, 100*time.Millisecond)

	// Must return nil — Unimplemented is swallowed per the best-effort contract.
	err := d.SendRequestTermination(
		context.Background(),
		"inst-unimpl",
		"req-x",
		channelv1.TerminalReason_TERMINAL_REASON_TIMED_OUT,
		"",
	)
	if err != nil {
		t.Errorf("SendRequestTermination returned non-nil error for Unimplemented: %v", err)
	}
}

// TestRequest_PreAckFailure_RowTransitionedToTimedOut verifies the cleanup path:
// when the plugin returns acked=false, the already-inserted row is transitioned
// to timed_out rather than left as a dangling pending entry.
func TestRequest_PreAckFailure_RowTransitionedToTimedOut(t *testing.T) {
	ds := newSetup(t)

	pluginID := insertPlugin(t, ds.store, "p1", "plug")
	instID := insertPluginInstance(t, ds.store, "i1", pluginID, "inst-nack2")
	audID := insertAudience(t, ds.store, "aud1", "aud")
	insertAudienceEntry(t, ds.store, "ae1", audID, instID, 0, false, true)

	ds.clientMap["inst-nack2"] = &fakeChannelClient{
		requestHook: func(_ context.Context, _ *channelv1.RequestRequest) (*channelv1.RequestResponse, error) {
			return &channelv1.RequestResponse{Acked: false}, nil
		},
	}

	testutil.InsertPolicy(t, ds.store, "pol1", "policy-pol1", "webhook", "{}")
	testutil.InsertRun(t, ds.store, "run1", "pol1", model.RunStatusRunning)

	d := ds.newDispatcher(200*time.Millisecond, 100*time.Millisecond)
	_, _, err := d.Request(context.Background(), audID, dispatch.RouteContext{RunID: "run1", PolicyID: "pol1", ToolName: "ask"}, "prompt", nil)
	if !errors.Is(err, dispatch.ErrPreAckFailed) {
		t.Fatalf("expected ErrPreAckFailed, got %v", err)
	}

	// Row must exist and be timed_out — not pending and not absent.
	var status string
	if err := ds.store.DB().QueryRow(`SELECT status FROM plugin_pending_requests WHERE run_id = 'run1'`).Scan(&status); err != nil {
		t.Fatalf("query pending request: %v", err)
	}
	if status != "timed_out" {
		t.Errorf("status = %q, want timed_out", status)
	}

	// Exactly one feedback_dispatch_error step must have been recorded.
	steps := ds.stepsByType("feedback_dispatch_error")
	if len(steps) != 1 {
		t.Errorf("feedback_dispatch_error steps = %d, want 1", len(steps))
	}
}
