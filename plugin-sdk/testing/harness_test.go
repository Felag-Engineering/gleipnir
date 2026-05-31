package testing_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
	toolv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/tool/v1"
	triggerv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/trigger/v1"
	"github.com/felag-engineering/gleipnir/plugin-sdk/channel"
	"github.com/felag-engineering/gleipnir/plugin-sdk/serve"
	plugintest "github.com/felag-engineering/gleipnir/plugin-sdk/testing"
	"github.com/felag-engineering/gleipnir/plugin-sdk/tool"
	"github.com/felag-engineering/gleipnir/plugin-sdk/trigger"
)

// ── in-file fake services ─────────────────────────────────────────────────────

// fakeToolService is a minimal tool.Service used only by harness_test. It:
//   - lists a single "ping" tool
//   - echoes the raw input bytes as output
//   - calls EmitMetric and Log via serve.WithCallContext so the test can verify
//     host callbacks round-trip through the harness. WithCallContext is a
//     graceful no-op when the gRPC context carries no gleipnir-call-id (which
//     is the case in harness tests), so AssertMetricEmitted/AssertLogContains
//     still pass — the host just receives the RPCs without a call-id annotation.
type fakeToolService struct {
	host hostv1.HostServiceClient
}

func (s *fakeToolService) ListTools(_ context.Context) ([]tool.ToolSpec, error) {
	return []tool.ToolSpec{
		{Name: "ping", Description: "returns the input unchanged", InputSchema: `{}`},
	}, nil
}

func (s *fakeToolService) Call(ctx context.Context, _ string, input []byte) ([]byte, error) {
	hostCtx := serve.WithCallContext(ctx)
	_, _ = s.host.EmitMetric(hostCtx, &hostv1.EmitMetricRequest{
		Name:  "ping_calls_total",
		Value: 1,
	})
	_, _ = s.host.Log(hostCtx, &hostv1.LogRequest{
		Level: hostv1.LogLevel_LOG_LEVEL_INFO,
		Msg:   "ping handled",
	})
	return input, nil
}

// fakeChannelService is a minimal channel.Service that calls GetInstanceConfig
// on every Notify so the test can verify host connectivity.
type fakeChannelService struct {
	host hostv1.HostServiceClient
}

func (s *fakeChannelService) Notify(ctx context.Context, _ channel.Notification) error {
	hostCtx := serve.WithCallContext(ctx)
	_, err := s.host.GetInstanceConfig(hostCtx, &hostv1.GetInstanceConfigRequest{})
	return err
}

func (s *fakeChannelService) Request(_ context.Context, _ channel.FeedbackRequest) error {
	return nil
}

// fakeTriggerService is a minimal trigger.Service that emits one event then
// returns when ctx is cancelled. The event payload is the watch_scope JSON.
type fakeTriggerService struct{}

func (s *fakeTriggerService) Start(ctx context.Context, scope trigger.StartScope, emit func(trigger.Event) error) error {
	payload, _ := json.Marshal(map[string]string{"scope": string(scope.WatchScope)})
	_ = emit(trigger.Event{
		EventID:   "evt-1",
		EventKind: "test.event",
		Payload:   payload,
	})
	<-ctx.Done()
	return nil
}

// ── TestToolHarness_RoundTrip ─────────────────────────────────────────────────

func TestToolHarness_RoundTrip(t *testing.T) {
	h := plugintest.NewToolHarness(t, func(hc hostv1.HostServiceClient) tool.Service {
		return &fakeToolService{host: hc}
	}, plugintest.WithInstanceConfigJSON(`{"env":"test"}`))

	// Direct proto call — asserts the happy path.
	resp, err := h.Client.Call(context.Background(), &toolv1.CallRequest{
		ToolName:  "ping",
		InputJson: `{"x":1}`,
	})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if resp.GetError() != nil {
		t.Fatalf("unexpected error envelope: %v", resp.GetError().GetMessage())
	}
	if resp.GetOutputJson() != `{"x":1}` {
		t.Errorf("output_json: want %q, got %q", `{"x":1}`, resp.GetOutputJson())
	}

	// Host callback assertions: EmitMetric and Log both went through the live
	// host gRPC connection inside the harness.
	h.Host.AssertMetricEmitted(t, "ping_calls_total", nil)
	h.Host.AssertLogContains(t, slog.LevelInfo, "ping handled")

	// Convenience helper path.
	out, err := h.Call(context.Background(), "ping", []byte(`{"y":2}`))
	if err != nil {
		t.Fatalf("h.Call: %v", err)
	}
	if string(out) != `{"y":2}` {
		t.Errorf("h.Call output: want %q, got %q", `{"y":2}`, string(out))
	}
}

// ── TestToolHarness_ListTools ─────────────────────────────────────────────────

func TestToolHarness_ListTools(t *testing.T) {
	h := plugintest.NewToolHarness(t, func(hc hostv1.HostServiceClient) tool.Service {
		return &fakeToolService{host: hc}
	})

	specs, err := h.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("want 1 tool, got %d", len(specs))
	}
	if specs[0].Name != "ping" {
		t.Errorf("want tool name %q, got %q", "ping", specs[0].Name)
	}
}

// ── TestChannelHarness_RoundTrip ──────────────────────────────────────────────

func TestChannelHarness_RoundTrip(t *testing.T) {
	h := plugintest.NewChannelHarness(t, func(hc hostv1.HostServiceClient) channel.Service {
		return &fakeChannelService{host: hc}
	}, plugintest.WithInstanceConfigJSON(`{"key":"val"}`))

	// The service calls GetInstanceConfig on every Notify; if the host
	// connection is broken this returns a gRPC error, which Notify propagates
	// as a plugin error that wraps inside ok=false. So ok=true proves the host
	// round-trip worked.
	err := h.Notify(context.Background(), channel.Notification{EventType: "test.event"})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}
}

// ── TestTriggerHarness_Start ──────────────────────────────────────────────────

func TestTriggerHarness_Start(t *testing.T) {
	h := plugintest.NewTriggerHarness(t, func(hc hostv1.HostServiceClient) trigger.Service {
		return &fakeTriggerService{}
	})

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := h.Client.Start(ctx, &triggerv1.StartRequest{
		WatchScopeJson: `{"ch":"#ops"}`,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	resp, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if resp.GetEventKind() != "test.event" {
		t.Errorf("event_kind: want %q, got %q", "test.event", resp.GetEventKind())
	}
	if resp.GetEventId() != "evt-1" {
		t.Errorf("event_id: want %q, got %q", "evt-1", resp.GetEventId())
	}

	// Cancelling the context closes the stream and unblocks ctx.Done() inside
	// fakeTriggerService.Start.
	cancel()
}

// ── TestHarness_Cleanup ───────────────────────────────────────────────────────

// TestHarness_Cleanup is a smoke test: if the single t.Cleanup closure
// double-closes or panics the test binary would fail with a runtime error.
// By running through a sub-test that completes cleanly we confirm the cleanup
// runs without issue.
func TestHarness_Cleanup(t *testing.T) {
	t.Run("tool", func(t *testing.T) {
		h := plugintest.NewToolHarness(t, func(hc hostv1.HostServiceClient) tool.Service {
			return &fakeToolService{host: hc}
		})
		// One successful call so the cleanup has live connections to close.
		_, _ = h.Client.ListTools(context.Background(), &toolv1.ListToolsRequest{})
	})

	t.Run("channel", func(t *testing.T) {
		h := plugintest.NewChannelHarness(t, func(hc hostv1.HostServiceClient) channel.Service {
			return &fakeChannelService{host: hc}
		})
		_ = h.Notify(context.Background(), channel.Notification{})
	})
}
