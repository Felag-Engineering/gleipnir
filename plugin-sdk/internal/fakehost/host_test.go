package fakehost_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
	"github.com/felag-engineering/gleipnir/plugin-sdk/internal/fakehost"
)

// newDefault creates a Host with default options for concise test setup.
func newDefault() *fakehost.Host {
	return fakehost.New(fakehost.Options{})
}

// ── Tier-1 happy paths ───────────────────────────────────────────────────────

func TestGetInstanceConfig_DefaultsToEmptyObject(t *testing.T) {
	h := newDefault()
	resp, err := h.GetInstanceConfig(context.Background(), &hostv1.GetInstanceConfigRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetConfigJson() != "{}" {
		t.Errorf("want {}, got %q", resp.GetConfigJson())
	}
}

func TestGetInstanceConfig_CustomJSON(t *testing.T) {
	h := fakehost.New(fakehost.Options{InstanceConfigJSON: `{"key":"val"}`})
	resp, err := h.GetInstanceConfig(context.Background(), &hostv1.GetInstanceConfigRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetConfigJson() != `{"key":"val"}` {
		t.Errorf("want {\"key\":\"val\"}, got %q", resp.GetConfigJson())
	}
}

func TestGetCredentials_DefaultsToEmptyObject(t *testing.T) {
	h := newDefault()
	resp, err := h.GetCredentials(context.Background(), &hostv1.GetCredentialsRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetCredentialsJson() != "{}" {
		t.Errorf("want {}, got %q", resp.GetCredentialsJson())
	}
}

func TestGetRunContext_Defaults(t *testing.T) {
	h := newDefault()
	resp, err := h.GetRunContext(context.Background(), &hostv1.GetRunContextRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetRunId() != "fake-run" {
		t.Errorf("want fake-run, got %q", resp.GetRunId())
	}
	if resp.GetPolicyId() != "fake-policy" {
		t.Errorf("want fake-policy, got %q", resp.GetPolicyId())
	}
	if resp.GetStepIndex() != 0 {
		t.Errorf("want step_index 0, got %d", resp.GetStepIndex())
	}
	if resp.GetStartedAt() == "" {
		t.Error("started_at should not be empty")
	}
}

func TestGetRunContext_CustomValues(t *testing.T) {
	started := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	h := fakehost.New(fakehost.Options{
		RunContext: fakehost.RunContext{
			RunID:     "run-123",
			PolicyID:  "policy-abc",
			StepIndex: 5,
			StartedAt: started,
		},
	})
	resp, err := h.GetRunContext(context.Background(), &hostv1.GetRunContextRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetRunId() != "run-123" {
		t.Errorf("want run-123, got %q", resp.GetRunId())
	}
	if resp.GetStepIndex() != 5 {
		t.Errorf("want 5, got %d", resp.GetStepIndex())
	}
}

func TestEmitMetric_Recorded(t *testing.T) {
	h := newDefault()
	req := &hostv1.EmitMetricRequest{Name: "my_metric", Value: 42.0}
	resp, err := h.EmitMetric(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.GetOk() {
		t.Error("expected ok=true")
	}
	metrics := h.Metrics()
	if len(metrics) != 1 {
		t.Fatalf("want 1 metric, got %d", len(metrics))
	}
	if metrics[0].GetName() != "my_metric" {
		t.Errorf("want my_metric, got %q", metrics[0].GetName())
	}
}

func TestEmitEvent_Recorded(t *testing.T) {
	h := newDefault()
	req := &hostv1.EmitEventRequest{
		EventId:     "evt-1",
		EventKind:   "github.push",
		PayloadJson: `{"ref":"main"}`,
	}
	resp, err := h.EmitEvent(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.GetOk() {
		t.Error("expected ok=true")
	}
	events := h.Events()
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	if events[0].GetEventId() != "evt-1" {
		t.Errorf("want evt-1, got %q", events[0].GetEventId())
	}
}

func TestEmitEvent_CallsOnEmitEventCallback(t *testing.T) {
	var called int
	var got *hostv1.EmitEventRequest
	h := fakehost.New(fakehost.Options{
		OnEmitEvent: func(req *hostv1.EmitEventRequest) {
			called++
			got = req
		},
	})

	req := &hostv1.EmitEventRequest{EventId: "cb-test"}
	_, err := h.EmitEvent(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called != 1 {
		t.Fatalf("callback called %d times, want 1", called)
	}
	if got.GetEventId() != "cb-test" {
		t.Errorf("callback got wrong event_id: %q", got.GetEventId())
	}
}

func TestLog_Recorded(t *testing.T) {
	h := newDefault()
	req := &hostv1.LogRequest{Level: hostv1.LogLevel_LOG_LEVEL_INFO, Msg: "hello from plugin"}
	resp, err := h.Log(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.GetOk() {
		t.Error("expected ok=true")
	}
	logs := h.Logs()
	if len(logs) != 1 {
		t.Fatalf("want 1 log, got %d", len(logs))
	}
	if logs[0].GetMsg() != "hello from plugin" {
		t.Errorf("want 'hello from plugin', got %q", logs[0].GetMsg())
	}
}

func TestSetHealthState_Recorded(t *testing.T) {
	h := newDefault()
	req := &hostv1.SetHealthStateRequest{
		State:  hostv1.PluginHealthState_PLUGIN_HEALTH_STATE_UNHEALTHY,
		Detail: "db gone",
	}
	resp, err := h.SetHealthState(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.GetOk() {
		t.Error("expected ok=true")
	}
	last := h.HealthStates()
	if last == nil {
		t.Fatal("expected health state to be recorded")
	}
	if last.GetDetail() != "db gone" {
		t.Errorf("want 'db gone', got %q", last.GetDetail())
	}
}

// ── Tier-2 stubs return Unimplemented ────────────────────────────────────────

func TestTier2_RunHistoryRead_Unimplemented(t *testing.T) {
	h := newDefault()
	_, err := h.RunHistoryRead(context.Background(), &hostv1.RunHistoryReadRequest{})
	assertUnimplemented(t, err)
}

func TestTier2_UserDirectoryRead_Unimplemented(t *testing.T) {
	h := newDefault()
	_, err := h.UserDirectoryRead(context.Background(), &hostv1.UserDirectoryReadRequest{})
	assertUnimplemented(t, err)
}

func assertUnimplemented(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected Unimplemented error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.Unimplemented {
		t.Errorf("want Unimplemented, got %v", st.Code())
	}
}

// ── Tier-2 stubs with canned data ───────────────────────────────────────────

func TestTier2_RunHistoryRead_WithCannedData(t *testing.T) {
	h := fakehost.New(fakehost.Options{
		RunHistoryRuns: []*hostv1.RunSummary{
			{RunId: "r-1", Status: "complete"},
			{RunId: "r-2", Status: "failed"},
		},
	})

	resp, err := h.RunHistoryRead(context.Background(), &hostv1.RunHistoryReadRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.GetRuns()) != 2 {
		t.Fatalf("want 2 runs, got %d", len(resp.GetRuns()))
	}
	if resp.GetRuns()[0].GetRunId() != "r-1" {
		t.Errorf("want r-1, got %q", resp.GetRuns()[0].GetRunId())
	}
	if h.RunHistoryCalls() != 1 {
		t.Errorf("want RunHistoryCalls=1, got %d", h.RunHistoryCalls())
	}
}

func TestTier2_UserDirectoryRead_WithCannedData(t *testing.T) {
	h := fakehost.New(fakehost.Options{
		UserDirectoryUsers: []*hostv1.UserEntry{
			{UserId: "u-1", Username: "alice", Role: "admin"},
		},
	})

	resp, err := h.UserDirectoryRead(context.Background(), &hostv1.UserDirectoryReadRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.GetUsers()) != 1 {
		t.Fatalf("want 1 user, got %d", len(resp.GetUsers()))
	}
	if resp.GetUsers()[0].GetUsername() != "alice" {
		t.Errorf("want alice, got %q", resp.GetUsers()[0].GetUsername())
	}
	if h.UserDirectoryCalls() != 1 {
		t.Errorf("want UserDirectoryCalls=1, got %d", h.UserDirectoryCalls())
	}
}

// ── Concurrent EmitEvent under -race ────────────────────────────────────────

func TestEmitEvent_ConcurrentSafe(t *testing.T) {
	const n = 100
	h := newDefault()

	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, _ = h.EmitEvent(context.Background(), &hostv1.EmitEventRequest{
				EventId:   "concurrent-evt",
				EventKind: "test.event",
			})
		}()
	}
	wg.Wait()

	events := h.Events()
	if len(events) != n {
		t.Errorf("want %d events, got %d", n, len(events))
	}
}
