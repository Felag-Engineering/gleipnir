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
	notifyHook  func(ctx context.Context, req *channelv1.NotifyRequest) (*channelv1.NotifyResponse, error)
	requestHook func(ctx context.Context, req *channelv1.RequestRequest) (*channelv1.RequestResponse, error)
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
		Queries:       ds.store.Queries(),
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

	// Wall clock must be well under NotifyTimeout * 1.5 (parallel, not serial).
	if elapsed > 750*time.Millisecond {
		t.Errorf("Notify took %v, want < 750ms (serial execution suspected)", elapsed)
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

	// Notify returns nil regardless of failures.
	slack := 100 * time.Millisecond
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

	// Resolve must flip it to resolved.
	if err := d.Resolve(context.Background(), reqID, `{"answer":"yes"}`); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if err := ds.store.DB().QueryRow(`SELECT status FROM plugin_pending_requests WHERE id = ?`, reqID).Scan(&status); err != nil {
		t.Fatalf("query after Resolve: %v", err)
	}
	if status != "resolved" {
		t.Errorf("status after Resolve = %q, want resolved", status)
	}
}

// TestRequest_PreAckAckedFalse verifies that acked=false causes a
// feedback_dispatch_error step, returns ErrPreAckFailed, and does not insert a
// pending row.
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

	// No row should be in plugin_pending_requests.
	var count int
	if err := ds.store.DB().QueryRow(`SELECT COUNT(*) FROM plugin_pending_requests WHERE run_id = 'run1'`).Scan(&count); err != nil {
		t.Fatalf("count pending requests: %v", err)
	}
	if count != 0 {
		t.Errorf("pending requests count = %d, want 0", count)
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

// TestResolve_UnknownRequestID returns ErrUnknownRequestID.
func TestResolve_UnknownRequestID(t *testing.T) {
	ds := newSetup(t)
	d := ds.newDispatcher(200*time.Millisecond, 100*time.Millisecond)
	err := d.Resolve(context.Background(), "01JUNK00000NOTREAL0000000", `{}`)
	if !errors.Is(err, dispatch.ErrUnknownRequestID) {
		t.Errorf("expected ErrUnknownRequestID, got %v", err)
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

	// Resolve must return nil (conflict swallowed).
	if err := d.Resolve(context.Background(), reqID, `{"answer":"late"}`); err != nil {
		t.Errorf("Resolve returned error after scanner won race: %v", err)
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
