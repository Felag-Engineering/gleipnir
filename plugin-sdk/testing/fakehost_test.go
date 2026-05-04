package testing_test

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
	plugintest "github.com/felag-engineering/gleipnir/plugin-sdk/testing"
)

// recordingTB captures the first Fatalf call so tests can assert that an
// assertion helper fires (or does not fire).
type recordingTB struct {
	t     *testing.T
	fatal string
}

func (r *recordingTB) Helper() { r.t.Helper() }
func (r *recordingTB) Fatalf(format string, args ...any) {
	r.fatal = fmt.Sprintf(format, args...)
}

// ── helpers ──────────────────────────────────────────────────────────────────

// startFakeServer registers fh on a new gRPC server listening on a random
// port, starts it, and returns the server address and a stop function.
func startFakeServer(t *testing.T, fh *plugintest.FakeHost) (addr string, stop func()) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	fh.Register(srv)
	go func() { _ = srv.Serve(lis) }()
	return lis.Addr().String(), srv.Stop
}

// dialFakeHost dials addr and returns a HostServiceClient and a close func.
func dialFakeHost(t *testing.T, addr string) (hostv1.HostServiceClient, func()) {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return hostv1.NewHostServiceClient(conn), func() { conn.Close() }
}

// ── NewFakeHost defaults ─────────────────────────────────────────────────────

func TestNewFakeHost_DefaultInstanceConfig(t *testing.T) {
	fh := plugintest.NewFakeHost()
	addr, stop := startFakeServer(t, fh)
	defer stop()
	client, close := dialFakeHost(t, addr)
	defer close()

	resp, err := client.GetInstanceConfig(context.Background(), &hostv1.GetInstanceConfigRequest{})
	if err != nil {
		t.Fatalf("GetInstanceConfig: %v", err)
	}
	if resp.GetConfigJson() != "{}" {
		t.Errorf("default config_json: want {}, got %q", resp.GetConfigJson())
	}
}

func TestNewFakeHost_DefaultRunContext(t *testing.T) {
	fh := plugintest.NewFakeHost()
	addr, stop := startFakeServer(t, fh)
	defer stop()
	client, close := dialFakeHost(t, addr)
	defer close()

	resp, err := client.GetRunContext(context.Background(), &hostv1.GetRunContextRequest{})
	if err != nil {
		t.Fatalf("GetRunContext: %v", err)
	}
	if resp.GetRunId() != "fake-run" {
		t.Errorf("default run_id: want fake-run, got %q", resp.GetRunId())
	}
}

// ── WithInstanceConfigJSON / WithCredentialsJSON ─────────────────────────────

func TestWithInstanceConfigJSON(t *testing.T) {
	fh := plugintest.NewFakeHost(plugintest.WithInstanceConfigJSON(`{"key":"val"}`))
	addr, stop := startFakeServer(t, fh)
	defer stop()
	client, close := dialFakeHost(t, addr)
	defer close()

	resp, err := client.GetInstanceConfig(context.Background(), &hostv1.GetInstanceConfigRequest{})
	if err != nil {
		t.Fatalf("GetInstanceConfig: %v", err)
	}
	if resp.GetConfigJson() != `{"key":"val"}` {
		t.Errorf("want {\"key\":\"val\"}, got %q", resp.GetConfigJson())
	}
}

func TestWithCredentialsJSON(t *testing.T) {
	fh := plugintest.NewFakeHost(plugintest.WithCredentialsJSON(`{"token":"abc"}`))
	addr, stop := startFakeServer(t, fh)
	defer stop()
	client, close := dialFakeHost(t, addr)
	defer close()

	resp, err := client.GetCredentials(context.Background(), &hostv1.GetCredentialsRequest{})
	if err != nil {
		t.Fatalf("GetCredentials: %v", err)
	}
	if resp.GetCredentialsJson() != `{"token":"abc"}` {
		t.Errorf("want {\"token\":\"abc\"}, got %q", resp.GetCredentialsJson())
	}
}

// ── WithRunContext ────────────────────────────────────────────────────────────

func TestWithRunContext(t *testing.T) {
	started := time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC)
	fh := plugintest.NewFakeHost(plugintest.WithRunContext(plugintest.RunContext{
		RunID:     "run-xyz",
		PolicyID:  "pol-abc",
		StartedAt: started,
	}))
	addr, stop := startFakeServer(t, fh)
	defer stop()
	client, close := dialFakeHost(t, addr)
	defer close()

	resp, err := client.GetRunContext(context.Background(), &hostv1.GetRunContextRequest{})
	if err != nil {
		t.Fatalf("GetRunContext: %v", err)
	}
	if resp.GetRunId() != "run-xyz" {
		t.Errorf("want run-xyz, got %q", resp.GetRunId())
	}
	if resp.GetPolicyId() != "pol-abc" {
		t.Errorf("want pol-abc, got %q", resp.GetPolicyId())
	}
	if resp.GetStartedAt() != started.Format(time.RFC3339) {
		t.Errorf("want %s, got %q", started.Format(time.RFC3339), resp.GetStartedAt())
	}
}

// ── OnEmitEvent ──────────────────────────────────────────────────────────────

func TestOnEmitEvent_FiresSynchronously(t *testing.T) {
	var mu sync.Mutex
	var received []plugintest.Event

	fh := plugintest.NewFakeHost(plugintest.OnEmitEvent(func(e plugintest.Event) {
		mu.Lock()
		received = append(received, e)
		mu.Unlock()
	}))
	addr, stop := startFakeServer(t, fh)
	defer stop()
	client, close := dialFakeHost(t, addr)
	defer close()

	_, err := client.EmitEvent(context.Background(), &hostv1.EmitEventRequest{
		EventId:   "e-1",
		EventKind: "test.kind",
	})
	if err != nil {
		t.Fatalf("EmitEvent: %v", err)
	}

	mu.Lock()
	n := len(received)
	mu.Unlock()

	if n != 1 {
		t.Fatalf("callback fired %d times, want 1", n)
	}
	if received[0].EventKind != "test.kind" {
		t.Errorf("callback got kind %q, want test.kind", received[0].EventKind)
	}
}

// ── WithRunHistory ────────────────────────────────────────────────────────────

func TestWithRunHistory_ServesData(t *testing.T) {
	fh := plugintest.NewFakeHost(plugintest.WithRunHistory([]plugintest.RunSummary{
		{RunID: "r-1", Status: "complete"},
		{RunID: "r-2", Status: "failed"},
	}))
	addr, stop := startFakeServer(t, fh)
	defer stop()
	client, close := dialFakeHost(t, addr)
	defer close()

	resp, err := client.RunHistoryRead(context.Background(), &hostv1.RunHistoryReadRequest{})
	if err != nil {
		t.Fatalf("RunHistoryRead: %v", err)
	}
	if len(resp.GetRuns()) != 2 {
		t.Errorf("want 2 runs, got %d", len(resp.GetRuns()))
	}
	if resp.GetRuns()[0].GetRunId() != "r-1" {
		t.Errorf("want r-1, got %q", resp.GetRuns()[0].GetRunId())
	}
}

func TestWithRunHistory_IncrementsCallCount(t *testing.T) {
	fh := plugintest.NewFakeHost(plugintest.WithRunHistory([]plugintest.RunSummary{
		{RunID: "r-1"},
	}))
	addr, stop := startFakeServer(t, fh)
	defer stop()
	client, close := dialFakeHost(t, addr)
	defer close()

	for i := 0; i < 3; i++ {
		if _, err := client.RunHistoryRead(context.Background(), &hostv1.RunHistoryReadRequest{}); err != nil {
			t.Fatalf("RunHistoryRead: %v", err)
		}
	}
	fh.AssertRunHistoryRead(t, 3)
}

func TestWithoutRunHistory_ReturnsUnimplemented(t *testing.T) {
	fh := plugintest.NewFakeHost() // no WithRunHistory
	addr, stop := startFakeServer(t, fh)
	defer stop()
	client, close := dialFakeHost(t, addr)
	defer close()

	_, err := client.RunHistoryRead(context.Background(), &hostv1.RunHistoryReadRequest{})
	if err == nil {
		t.Fatal("expected Unimplemented error, got nil")
	}
}

// ── WithUserDirectory ─────────────────────────────────────────────────────────

func TestWithUserDirectory_ServesData(t *testing.T) {
	fh := plugintest.NewFakeHost(plugintest.WithUserDirectory([]plugintest.UserEntry{
		{UserID: "u-1", Username: "alice", Role: "admin"},
	}))
	addr, stop := startFakeServer(t, fh)
	defer stop()
	client, close := dialFakeHost(t, addr)
	defer close()

	resp, err := client.UserDirectoryRead(context.Background(), &hostv1.UserDirectoryReadRequest{})
	if err != nil {
		t.Fatalf("UserDirectoryRead: %v", err)
	}
	if len(resp.GetUsers()) != 1 {
		t.Errorf("want 1 user, got %d", len(resp.GetUsers()))
	}
	if resp.GetUsers()[0].GetUsername() != "alice" {
		t.Errorf("want alice, got %q", resp.GetUsers()[0].GetUsername())
	}
}

func TestWithUserDirectory_IncrementsCallCount(t *testing.T) {
	fh := plugintest.NewFakeHost(plugintest.WithUserDirectory([]plugintest.UserEntry{
		{UserID: "u-1"},
	}))
	addr, stop := startFakeServer(t, fh)
	defer stop()
	client, close := dialFakeHost(t, addr)
	defer close()

	if _, err := client.UserDirectoryRead(context.Background(), &hostv1.UserDirectoryReadRequest{}); err != nil {
		t.Fatalf("UserDirectoryRead: %v", err)
	}
	fh.AssertUserDirectoryRead(t, 1)
}

func TestWithoutUserDirectory_ReturnsUnimplemented(t *testing.T) {
	fh := plugintest.NewFakeHost() // no WithUserDirectory
	addr, stop := startFakeServer(t, fh)
	defer stop()
	client, close := dialFakeHost(t, addr)
	defer close()

	_, err := client.UserDirectoryRead(context.Background(), &hostv1.UserDirectoryReadRequest{})
	if err == nil {
		t.Fatal("expected Unimplemented error, got nil")
	}
}

// ── AssertMetricEmitted ──────────────────────────────────────────────────────

func TestAssertMetricEmitted_Pass(t *testing.T) {
	fh := plugintest.NewFakeHost()
	addr, stop := startFakeServer(t, fh)
	defer stop()
	client, close := dialFakeHost(t, addr)
	defer close()

	_, _ = client.EmitMetric(context.Background(), &hostv1.EmitMetricRequest{
		Name:   "my_metric",
		Value:  1,
		Labels: map[string]string{"env": "test"},
	})
	fh.AssertMetricEmitted(t, "my_metric", map[string]string{"env": "test"})
}

func TestAssertMetricEmitted_Fail(t *testing.T) {
	t.Run("no metric emitted", func(t *testing.T) {
		fh := plugintest.NewFakeHost()
		rtb := &recordingTB{t: t}
		fh.AssertMetricEmitted(rtb, "missing_metric", nil)
		if rtb.fatal == "" {
			t.Fatal("expected Fatalf to be called")
		}
	})

	t.Run("label mismatch", func(t *testing.T) {
		fh := plugintest.NewFakeHost()
		addr, stop := startFakeServer(t, fh)
		defer stop()
		client, close := dialFakeHost(t, addr)
		defer close()

		_, _ = client.EmitMetric(context.Background(), &hostv1.EmitMetricRequest{
			Name:   "my_metric",
			Value:  1,
			Labels: map[string]string{"env": "prod"},
		})

		rtb := &recordingTB{t: t}
		fh.AssertMetricEmitted(rtb, "my_metric", map[string]string{"env": "test"})
		if rtb.fatal == "" {
			t.Fatal("expected Fatalf to be called for label mismatch")
		}
	})
}

func TestAssertMetricEmitted_SubsetLabelMatch(t *testing.T) {
	fh := plugintest.NewFakeHost()
	addr, stop := startFakeServer(t, fh)
	defer stop()
	client, close := dialFakeHost(t, addr)
	defer close()

	// metric has extra label "instance" not in the assertion
	_, _ = client.EmitMetric(context.Background(), &hostv1.EmitMetricRequest{
		Name:   "calls_total",
		Value:  1,
		Labels: map[string]string{"tool": "echo", "instance": "i-1"},
	})
	fh.AssertMetricEmitted(t, "calls_total", map[string]string{"tool": "echo"})
}

// ── AssertNoMetricEmitted ────────────────────────────────────────────────────

func TestAssertNoMetricEmitted_Pass(t *testing.T) {
	fh := plugintest.NewFakeHost()
	fh.AssertNoMetricEmitted(t, "absent_metric")
}

func TestAssertNoMetricEmitted_Fail(t *testing.T) {
	fh := plugintest.NewFakeHost()
	addr, stop := startFakeServer(t, fh)
	defer stop()
	client, close := dialFakeHost(t, addr)
	defer close()

	_, _ = client.EmitMetric(context.Background(), &hostv1.EmitMetricRequest{Name: "present"})
	rtb := &recordingTB{t: t}
	fh.AssertNoMetricEmitted(rtb, "present")
	if rtb.fatal == "" {
		t.Fatal("expected Fatalf to be called")
	}
}

// ── AssertEventEmitted ────────────────────────────────────────────────────────

func TestAssertEventEmitted_Pass(t *testing.T) {
	fh := plugintest.NewFakeHost()
	addr, stop := startFakeServer(t, fh)
	defer stop()
	client, close := dialFakeHost(t, addr)
	defer close()

	_, _ = client.EmitEvent(context.Background(), &hostv1.EmitEventRequest{
		EventKind: "github.push",
	})
	fh.AssertEventEmitted(t, "github.push")
}

func TestAssertEventEmitted_Fail(t *testing.T) {
	fh := plugintest.NewFakeHost()
	rtb := &recordingTB{t: t}
	fh.AssertEventEmitted(rtb, "missing.event")
	if rtb.fatal == "" {
		t.Fatal("expected Fatalf to be called")
	}
}

// ── AssertLogContains ─────────────────────────────────────────────────────────

func TestAssertLogContains_Pass(t *testing.T) {
	fh := plugintest.NewFakeHost()
	addr, stop := startFakeServer(t, fh)
	defer stop()
	client, close := dialFakeHost(t, addr)
	defer close()

	_, _ = client.Log(context.Background(), &hostv1.LogRequest{
		Level: hostv1.LogLevel_LOG_LEVEL_INFO,
		Msg:   "tool executed successfully",
	})
	fh.AssertLogContains(t, slog.LevelInfo, "executed")
}

func TestAssertLogContains_Fail(t *testing.T) {
	t.Run("empty log", func(t *testing.T) {
		fh := plugintest.NewFakeHost()
		rtb := &recordingTB{t: t}
		fh.AssertLogContains(rtb, slog.LevelInfo, "not here")
		if rtb.fatal == "" {
			t.Fatal("expected Fatalf to be called")
		}
	})

	t.Run("level mismatch", func(t *testing.T) {
		fh := plugintest.NewFakeHost()
		addr, stop := startFakeServer(t, fh)
		defer stop()
		client, close := dialFakeHost(t, addr)
		defer close()

		_, _ = client.Log(context.Background(), &hostv1.LogRequest{
			Level: hostv1.LogLevel_LOG_LEVEL_INFO,
			Msg:   "hi",
		})

		rtb := &recordingTB{t: t}
		fh.AssertLogContains(rtb, slog.LevelError, "hi")
		if rtb.fatal == "" {
			t.Fatal("expected Fatalf to be called for level mismatch")
		}
	})
}

// ── AssertAuditStep ───────────────────────────────────────────────────────────

func TestAssertAuditStep_Pass(t *testing.T) {
	fh := plugintest.NewFakeHost()
	addr, stop := startFakeServer(t, fh)
	defer stop()
	client, close := dialFakeHost(t, addr)
	defer close()

	_, _ = client.WriteAuditStep(context.Background(), &hostv1.WriteAuditStepRequest{
		StepType:  "feedback_response",
		RequestId: "req-1",
	})
	fh.AssertAuditStep(t, "feedback_response", "req-1")
}

func TestAssertAuditStep_Fail(t *testing.T) {
	fh := plugintest.NewFakeHost()
	rtb := &recordingTB{t: t}
	fh.AssertAuditStep(rtb, "feedback_response", "nonexistent")
	if rtb.fatal == "" {
		t.Fatal("expected Fatalf to be called")
	}
}

// ── AssertHealth ──────────────────────────────────────────────────────────────

func TestAssertHealth_RoundTrip(t *testing.T) {
	fh := plugintest.NewFakeHost()
	addr, stop := startFakeServer(t, fh)
	defer stop()
	client, close := dialFakeHost(t, addr)
	defer close()

	_, _ = client.SetHealthState(context.Background(), &hostv1.SetHealthStateRequest{
		State: hostv1.PluginHealthState_PLUGIN_HEALTH_STATE_HEALTHY,
	})
	fh.AssertHealth(t, plugintest.HealthStateHealthy)
}

func TestAssertHealth_NoState_Fails(t *testing.T) {
	fh := plugintest.NewFakeHost()
	rtb := &recordingTB{t: t}
	fh.AssertHealth(rtb, plugintest.HealthStateHealthy)
	if rtb.fatal == "" {
		t.Fatal("expected Fatalf to be called")
	}
}

func TestAssertHealth_WrongState_Fails(t *testing.T) {
	fh := plugintest.NewFakeHost()
	addr, stop := startFakeServer(t, fh)
	defer stop()
	client, close := dialFakeHost(t, addr)
	defer close()

	_, _ = client.SetHealthState(context.Background(), &hostv1.SetHealthStateRequest{
		State: hostv1.PluginHealthState_PLUGIN_HEALTH_STATE_UNHEALTHY,
	})
	rtb := &recordingTB{t: t}
	fh.AssertHealth(rtb, plugintest.HealthStateHealthy)
	if rtb.fatal == "" {
		t.Fatal("expected Fatalf to be called")
	}
}

// ── AssertRunHistoryRead / AssertUserDirectoryRead ────────────────────────────

func TestAssertRunHistoryRead_Negative(t *testing.T) {
	fh := plugintest.NewFakeHost(plugintest.WithRunHistory([]plugintest.RunSummary{{RunID: "r-1"}}))
	rtb := &recordingTB{t: t}
	fh.AssertRunHistoryRead(rtb, 1) // zero calls made, want 1
	if rtb.fatal == "" {
		t.Fatal("expected Fatalf to be called")
	}
}

func TestAssertUserDirectoryRead_Negative(t *testing.T) {
	fh := plugintest.NewFakeHost(plugintest.WithUserDirectory([]plugintest.UserEntry{{UserID: "u-1"}}))
	rtb := &recordingTB{t: t}
	fh.AssertUserDirectoryRead(rtb, 1) // zero calls made, want 1
	if rtb.fatal == "" {
		t.Fatal("expected Fatalf to be called")
	}
}

// ── Reset ─────────────────────────────────────────────────────────────────────

func TestReset_ClearsRecorders(t *testing.T) {
	fh := plugintest.NewFakeHost(plugintest.WithRunHistory([]plugintest.RunSummary{{RunID: "r-1"}}))
	addr, stop := startFakeServer(t, fh)
	defer stop()
	client, close := dialFakeHost(t, addr)
	defer close()

	_, _ = client.EmitMetric(context.Background(), &hostv1.EmitMetricRequest{Name: "m"})
	_, _ = client.RunHistoryRead(context.Background(), &hostv1.RunHistoryReadRequest{})

	fh.Reset()

	if len(fh.Metrics()) != 0 {
		t.Errorf("expected 0 metrics after Reset, got %d", len(fh.Metrics()))
	}
	if fh.RunHistoryCalls() != 0 {
		t.Errorf("expected 0 RunHistoryCalls after Reset, got %d", fh.RunHistoryCalls())
	}
}

// ── Concurrent EmitEvent/EmitMetric under -race ───────────────────────────────

func TestConcurrent_EmitEventAndMetric(t *testing.T) {
	const n = 1000
	fh := plugintest.NewFakeHost()
	addr, stop := startFakeServer(t, fh)
	defer stop()
	client, close := dialFakeHost(t, addr)
	defer close()

	var wg sync.WaitGroup
	wg.Add(n * 2)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, _ = client.EmitEvent(context.Background(), &hostv1.EmitEventRequest{
				EventKind: "concurrent.event",
			})
		}()
		go func() {
			defer wg.Done()
			_, _ = client.EmitMetric(context.Background(), &hostv1.EmitMetricRequest{
				Name:  "concurrent_metric",
				Value: 1,
			})
		}()
	}
	wg.Wait()

	if len(fh.Events()) != n {
		t.Errorf("want %d events, got %d", n, len(fh.Events()))
	}
	if len(fh.Metrics()) != n {
		t.Errorf("want %d metrics, got %d", n, len(fh.Metrics()))
	}
}
