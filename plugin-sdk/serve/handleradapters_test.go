package serve

// handleradapters_test.go — unit tests for the ergonomic adapter layer.
//
// These tests sit in package serve (not serve_test) so they can reference the
// unexported adapter structs for compile-time interface assertions. The tests
// themselves only call the public API: NewToolServer, NewChannelServer,
// NewTriggerServer, and the adapted methods via the generated server interfaces.

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/felag-engineering/gleipnir/plugin-sdk/channel"
	channelv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/channel/v1"
	commonv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/common/v1"
	toolv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/tool/v1"
	triggerv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/trigger/v1"
	"github.com/felag-engineering/gleipnir/plugin-sdk/pluginerr"
	"github.com/felag-engineering/gleipnir/plugin-sdk/tool"
	"github.com/felag-engineering/gleipnir/plugin-sdk/trigger"
)

// ── compile-time interface assertions ─────────────────────────────────────────

// These ensure that the unexported adapter structs satisfy the generated server
// interfaces; a missing method surfaces here at compile time rather than at
// RegisterXServiceServer registration time.
var _ toolv1.ToolServiceServer = (*toolHandlerAdapter)(nil)
var _ channelv1.ChannelServiceServer = (*channelHandlerAdapter)(nil)
var _ triggerv1.TriggerServiceServer = (*triggerHandlerAdapter)(nil)

// ── fakes ────────────────────────────────────────────────────────────────────

// fakeToolService is an in-test tool.Service implementation used by the table
// tests. It returns fixed (specs, err) from ListTools and (output, err) from Call.
type fakeToolService struct {
	listToolsSpecs []tool.ToolSpec
	listToolsErr   error
	callOutput     []byte
	callErr        error
}

func (f *fakeToolService) ListTools(_ context.Context) ([]tool.ToolSpec, error) {
	return f.listToolsSpecs, f.listToolsErr
}

func (f *fakeToolService) Call(_ context.Context, _ string, _ []byte) ([]byte, error) {
	return f.callOutput, f.callErr
}

// fakeChannelService is an in-test channel.Service implementation.
type fakeChannelService struct {
	notifyErr  error
	requestErr error
}

func (f *fakeChannelService) Notify(_ context.Context, _ channel.Notification) error {
	return f.notifyErr
}

func (f *fakeChannelService) Request(_ context.Context, _ channel.FeedbackRequest) error {
	return f.requestErr
}

// fakeTriggerService is an in-test trigger.Service implementation.
// The emitted slice holds events to emit before returning emitErr.
type fakeTriggerService struct {
	emitted  []trigger.Event
	startErr error
}

func (f *fakeTriggerService) Start(_ context.Context, _ trigger.StartScope, emit func(trigger.Event) error) error {
	for _, e := range f.emitted {
		if err := emit(e); err != nil {
			return err
		}
	}
	return f.startErr
}

// fakeStartStream captures Send calls and returns a fixed context.
// It satisfies grpc.ServerStreamingServer[triggerv1.StartResponse].
type fakeStartStream struct {
	sent    []*triggerv1.StartResponse
	sendErr error
	ctx     context.Context
}

func (s *fakeStartStream) Send(resp *triggerv1.StartResponse) error {
	if s.sendErr != nil {
		return s.sendErr
	}
	s.sent = append(s.sent, resp)
	return nil
}

func (s *fakeStartStream) Context() context.Context {
	if s.ctx != nil {
		return s.ctx
	}
	return context.Background()
}

// The following methods implement grpc.ServerStream but are never called in our tests.
func (s *fakeStartStream) SetHeader(metadata.MD) error  { return nil }
func (s *fakeStartStream) SendHeader(metadata.MD) error { return nil }
func (s *fakeStartStream) SetTrailer(metadata.MD)       {}
func (s *fakeStartStream) SendMsg(any) error            { return nil }
func (s *fakeStartStream) RecvMsg(any) error            { return nil }

// ── toolHandlerAdapter tests ─────────────────────────────────────────────────

// TestToolAdapter_Call_ErrorMapping verifies that each pluginerr code is
// translated to its corresponding commonv1.ErrorCode in the Call response
// envelope, and that a plain error becomes ERROR_CODE_INTERNAL.
func TestToolAdapter_Call_ErrorMapping(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode commonv1.ErrorCode
	}{
		{"InvalidArg", pluginerr.InvalidArg("bad arg"), commonv1.ErrorCode_ERROR_CODE_INVALID_ARG},
		{"NotFound", pluginerr.NotFound("not found"), commonv1.ErrorCode_ERROR_CODE_NOT_FOUND},
		{"Internal", pluginerr.Internal("oops"), commonv1.ErrorCode_ERROR_CODE_INTERNAL},
		{"Unavailable", pluginerr.Unavailable("retry"), commonv1.ErrorCode_ERROR_CODE_UNAVAILABLE},
		{"Permission", pluginerr.Permission("no access"), commonv1.ErrorCode_ERROR_CODE_PERMISSION},
		{"RateLimited", pluginerr.RateLimited("slow down"), commonv1.ErrorCode_ERROR_CODE_RATE_LIMITED},
		{"Unimplemented", pluginerr.Unimplemented("not impl"), commonv1.ErrorCode_ERROR_CODE_UNIMPLEMENTED},
		{"PlainError", errors.New("something broke"), commonv1.ErrorCode_ERROR_CODE_INTERNAL},
		// A CodedError wrapped with %w is still mapped to its real code (errors.As
		// walks the chain), not silently downgraded to INTERNAL.
		{"WrappedCoded", fmt.Errorf("while calling: %w", pluginerr.InvalidArg("bad arg")), commonv1.ErrorCode_ERROR_CODE_INVALID_ARG},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := NewToolServer(&fakeToolService{callErr: tc.err})
			resp, err := srv.Call(context.Background(), &toolv1.CallRequest{
				ToolName:  "test",
				InputJson: `{}`,
			})
			if err != nil {
				t.Fatalf("Call returned gRPC error: %v (want nil gRPC error, error in envelope)", err)
			}
			if resp.GetError() == nil {
				t.Fatal("expected error envelope, got nil")
			}
			if resp.GetError().GetCode() != tc.wantCode {
				t.Errorf("code: want %v, got %v", tc.wantCode, resp.GetError().GetCode())
			}
		})
	}
}

// TestToolAdapter_Call_Success verifies that a successful Call returns
// OutputJson and a nil Error envelope.
func TestToolAdapter_Call_Success(t *testing.T) {
	wantOutput := `{"echoed":"hi"}`
	srv := NewToolServer(&fakeToolService{callOutput: []byte(wantOutput)})

	resp, err := srv.Call(context.Background(), &toolv1.CallRequest{
		ToolName:  "echo",
		InputJson: `{"message":"hi"}`,
	})
	if err != nil {
		t.Fatalf("Call gRPC error: %v", err)
	}
	if resp.GetError() != nil {
		t.Fatalf("unexpected error envelope: %v", resp.GetError().GetMessage())
	}
	if resp.GetOutputJson() != wantOutput {
		t.Errorf("OutputJson: want %q, got %q", wantOutput, resp.GetOutputJson())
	}
}

// TestToolAdapter_ListTools_Success verifies that ToolSpec values are
// translated field-by-field into ToolSchema protos.
func TestToolAdapter_ListTools_Success(t *testing.T) {
	specs := []tool.ToolSpec{
		{Name: "foo", Description: "does foo", InputSchema: `{"type":"object"}`},
		{Name: "bar", Description: "does bar", InputSchema: `{}`},
	}
	srv := NewToolServer(&fakeToolService{listToolsSpecs: specs})

	resp, err := srv.ListTools(context.Background(), &toolv1.ListToolsRequest{})
	if err != nil {
		t.Fatalf("ListTools gRPC error: %v", err)
	}
	if len(resp.GetTools()) != len(specs) {
		t.Fatalf("want %d tools, got %d", len(specs), len(resp.GetTools()))
	}
	for i, s := range specs {
		got := resp.GetTools()[i]
		if got.GetName() != s.Name {
			t.Errorf("[%d] Name: want %q, got %q", i, s.Name, got.GetName())
		}
		if got.GetDescription() != s.Description {
			t.Errorf("[%d] Description: want %q, got %q", i, s.Description, got.GetDescription())
		}
		if got.GetInputSchema() != s.InputSchema {
			t.Errorf("[%d] InputSchema: want %q, got %q", i, s.InputSchema, got.GetInputSchema())
		}
	}
}

// TestToolAdapter_ListTools_Error verifies that an error from ListTools
// becomes a gRPC codes.Internal status (not an application envelope), because
// ListToolsResponse has no Error field.
func TestToolAdapter_ListTools_Error(t *testing.T) {
	srv := NewToolServer(&fakeToolService{listToolsErr: errors.New("discovery failed")})

	resp, err := srv.ListTools(context.Background(), &toolv1.ListToolsRequest{})
	if err == nil {
		t.Fatal("expected gRPC error from ListTools, got nil; resp:", resp)
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("error is not a gRPC status: %v", err)
	}
	if st.Code() != codes.Internal {
		t.Errorf("status code: want codes.Internal, got %s", st.Code())
	}
}

// TestToolAdapter_Cancel_Noop verifies that Cancel always succeeds with an
// empty response — cancellation is via ctx.Done() inside Call.
func TestToolAdapter_Cancel_Noop(t *testing.T) {
	srv := NewToolServer(&fakeToolService{})
	resp, err := srv.Cancel(context.Background(), &toolv1.CancelRequest{})
	if err != nil {
		t.Fatalf("Cancel gRPC error: %v", err)
	}
	if resp == nil {
		t.Error("Cancel returned nil response")
	}
}

// ── channelHandlerAdapter tests ──────────────────────────────────────────────

// TestChannelAdapter_Notify_ErrorMapping verifies that each pluginerr code
// appears in the NotifyResponse envelope and that a plain error → INTERNAL.
func TestChannelAdapter_Notify_ErrorMapping(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode commonv1.ErrorCode
	}{
		{"InvalidArg", pluginerr.InvalidArg("bad"), commonv1.ErrorCode_ERROR_CODE_INVALID_ARG},
		{"Internal", pluginerr.Internal("oops"), commonv1.ErrorCode_ERROR_CODE_INTERNAL},
		{"Unimplemented", pluginerr.Unimplemented("not impl"), commonv1.ErrorCode_ERROR_CODE_UNIMPLEMENTED},
		{"PlainError", errors.New("plain"), commonv1.ErrorCode_ERROR_CODE_INTERNAL},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := NewChannelServer(&fakeChannelService{notifyErr: tc.err})
			resp, err := srv.Notify(context.Background(), &channelv1.NotifyRequest{})
			if err != nil {
				t.Fatalf("Notify gRPC error: %v", err)
			}
			if resp.GetOk() {
				t.Fatal("expected Ok=false, got Ok=true")
			}
			if resp.GetError() == nil {
				t.Fatal("expected error envelope, got nil")
			}
			if resp.GetError().GetCode() != tc.wantCode {
				t.Errorf("code: want %v, got %v", tc.wantCode, resp.GetError().GetCode())
			}
		})
	}
}

// TestChannelAdapter_Notify_Success verifies that a nil error → Ok=true, no envelope.
func TestChannelAdapter_Notify_Success(t *testing.T) {
	srv := NewChannelServer(&fakeChannelService{notifyErr: nil})
	resp, err := srv.Notify(context.Background(), &channelv1.NotifyRequest{
		PayloadJson:       `{"body":"test"}`,
		ChannelConfigJson: `{"topic":"alerts"}`,
		EventType:         "run_failed",
	})
	if err != nil {
		t.Fatalf("Notify gRPC error: %v", err)
	}
	if !resp.GetOk() {
		t.Error("expected Ok=true")
	}
	if resp.GetError() != nil {
		t.Errorf("unexpected error envelope: %v", resp.GetError().GetMessage())
	}
}

// TestChannelAdapter_Request_ErrorMapping verifies that Unimplemented (the
// common ntfy case) maps to an application-level envelope, not a gRPC status.
func TestChannelAdapter_Request_ErrorMapping(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode commonv1.ErrorCode
	}{
		{"Unimplemented", pluginerr.Unimplemented("not impl"), commonv1.ErrorCode_ERROR_CODE_UNIMPLEMENTED},
		{"Internal", pluginerr.Internal("oops"), commonv1.ErrorCode_ERROR_CODE_INTERNAL},
		{"PlainError", errors.New("plain"), commonv1.ErrorCode_ERROR_CODE_INTERNAL},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := NewChannelServer(&fakeChannelService{requestErr: tc.err})
			resp, err := srv.Request(context.Background(), &channelv1.RequestRequest{})
			if err != nil {
				t.Fatalf("Request gRPC error: %v (want nil gRPC error, error in envelope)", err)
			}
			if resp.GetAcked() {
				t.Fatal("expected Acked=false")
			}
			if resp.GetError() == nil {
				t.Fatal("expected error envelope, got nil")
			}
			if resp.GetError().GetCode() != tc.wantCode {
				t.Errorf("code: want %v, got %v", tc.wantCode, resp.GetError().GetCode())
			}
		})
	}
}

// TestChannelAdapter_Request_Success verifies nil error → Acked=true, no envelope.
func TestChannelAdapter_Request_Success(t *testing.T) {
	srv := NewChannelServer(&fakeChannelService{requestErr: nil})
	resp, err := srv.Request(context.Background(), &channelv1.RequestRequest{
		RequestId: "req-1",
		Prompt:    "approve?",
	})
	if err != nil {
		t.Fatalf("Request gRPC error: %v", err)
	}
	if !resp.GetAcked() {
		t.Error("expected Acked=true")
	}
	if resp.GetError() != nil {
		t.Errorf("unexpected error envelope: %v", resp.GetError().GetMessage())
	}
}

// ── triggerHandlerAdapter tests ──────────────────────────────────────────────

// TestTriggerAdapter_Start_EmitForwardedToStream verifies that events passed
// to emit are forwarded via stream.Send with correct field mapping.
func TestTriggerAdapter_Start_EmitForwardedToStream(t *testing.T) {
	events := []trigger.Event{
		{EventID: "id-1", EventKind: "slack_message", Payload: []byte(`{"text":"hello"}`)},
		{EventID: "id-2", EventKind: "slack_message", Payload: []byte(`{"text":"world"}`)},
	}
	svc := &fakeTriggerService{emitted: events}
	srv := NewTriggerServer(svc)

	stream := &fakeStartStream{}
	req := &triggerv1.StartRequest{WatchScopeJson: `{"channels":["#gen"]}`}

	if err := srv.Start(req, stream); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	if len(stream.sent) != len(events) {
		t.Fatalf("want %d sent messages, got %d", len(events), len(stream.sent))
	}
	for i, e := range events {
		got := stream.sent[i]
		if got.GetEventId() != e.EventID {
			t.Errorf("[%d] EventId: want %q, got %q", i, e.EventID, got.GetEventId())
		}
		if got.GetEventKind() != e.EventKind {
			t.Errorf("[%d] EventKind: want %q, got %q", i, e.EventKind, got.GetEventKind())
		}
		if got.GetPayloadJson() != string(e.Payload) {
			t.Errorf("[%d] PayloadJson: want %q, got %q", i, string(e.Payload), got.GetPayloadJson())
		}
	}
}

// TestTriggerAdapter_Start_AuthorErrorPropagates verifies that an error
// returned by Start propagates directly as the gRPC stream status.
func TestTriggerAdapter_Start_AuthorErrorPropagates(t *testing.T) {
	wantErr := errors.New("substrate disconnected")
	svc := &fakeTriggerService{startErr: wantErr}
	srv := NewTriggerServer(svc)

	stream := &fakeStartStream{}
	err := srv.Start(&triggerv1.StartRequest{}, stream)
	if err == nil {
		t.Fatal("expected error from Start, got nil")
	}
	if err.Error() != wantErr.Error() {
		t.Errorf("error: want %q, got %q", wantErr.Error(), err.Error())
	}
}

// TestTriggerAdapter_Start_SendErrorShortCircuits verifies that when stream.Send
// returns an error, the emit function returns it and Start exits.
func TestTriggerAdapter_Start_SendErrorShortCircuits(t *testing.T) {
	sendErr := errors.New("stream closed")
	events := []trigger.Event{
		{EventID: "id-1", EventKind: "kind", Payload: []byte(`{}`)},
		{EventID: "id-2", EventKind: "kind", Payload: []byte(`{}`)},
	}
	svc := &fakeTriggerService{emitted: events}
	srv := NewTriggerServer(svc)

	// Stream that fails on every Send.
	stream := &fakeStartStream{sendErr: sendErr}
	err := srv.Start(&triggerv1.StartRequest{}, stream)

	// The emit error propagates out through Start.
	if err == nil {
		t.Fatal("expected error from Start after Send failure, got nil")
	}
	if err.Error() != sendErr.Error() {
		t.Errorf("error: want %q, got %q", sendErr.Error(), err.Error())
	}
	// No messages should have been appended to stream.sent (sendErr fires before append).
	if len(stream.sent) != 0 {
		t.Errorf("want 0 sent messages (send failed), got %d", len(stream.sent))
	}
}

// TestChannelAdapter_Notify_FieldMapping verifies that the proto request fields
// are correctly mapped onto the channel.Notification passed to Notify.
func TestChannelAdapter_Notify_FieldMapping(t *testing.T) {
	var gotNotification channel.Notification
	captureService := &capturingChannelService{onNotify: func(n channel.Notification) {
		gotNotification = n
	}}
	srv := NewChannelServer(captureService)

	_, err := srv.Notify(context.Background(), &channelv1.NotifyRequest{
		EventType:         "run_failed",
		PayloadJson:       `{"body":"alert"}`,
		ChannelConfigJson: `{"topic":"ops"}`,
	})
	if err != nil {
		t.Fatalf("Notify gRPC error: %v", err)
	}

	if gotNotification.EventType != "run_failed" {
		t.Errorf("EventType: want %q, got %q", "run_failed", gotNotification.EventType)
	}
	if string(gotNotification.Payload) != `{"body":"alert"}` {
		t.Errorf("Payload: want %q, got %q", `{"body":"alert"}`, string(gotNotification.Payload))
	}
	if string(gotNotification.ChannelConfig) != `{"topic":"ops"}` {
		t.Errorf("ChannelConfig: want %q, got %q", `{"topic":"ops"}`, string(gotNotification.ChannelConfig))
	}
}

// capturingChannelService lets tests inspect what was passed to the service method.
type capturingChannelService struct {
	onNotify func(channel.Notification)
}

func (c *capturingChannelService) Notify(_ context.Context, n channel.Notification) error {
	if c.onNotify != nil {
		c.onNotify(n)
	}
	return nil
}

func (c *capturingChannelService) Request(_ context.Context, _ channel.FeedbackRequest) error {
	return nil
}
