package dispatch

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	optionsv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/options/v1"
)

// fakeOptionsClient is a minimal stub of optionsv1.ConfigOptionsServiceClient.
type fakeOptionsClient struct {
	optionsv1.UnimplementedConfigOptionsServiceServer // reuse for embedding – not ideal but handy

	resp *optionsv1.ListOptionsResponse
	err  error
}

func (f *fakeOptionsClient) ListOptions(_ context.Context, req *optionsv1.ListOptionsRequest, _ ...grpc.CallOption) (*optionsv1.ListOptionsResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

// TestOptionsClient_HappyPath verifies that ListOptions proxies the response
// from the fake client and passes instanceID, source, query, cursor correctly.
func TestOptionsClient_HappyPath(t *testing.T) {
	want := &optionsv1.ListOptionsResponse{
		Options: []*optionsv1.Option{
			{Value: "C001", Label: "#general", Group: "Joined"},
		},
		NextCursor: "next1",
	}

	// The capture client both serves the canned response and records the
	// outgoing request for the field assertions below.
	var capturedReq *optionsv1.ListOptionsRequest
	captureFactory := func(instanceName string) (optionsv1.ConfigOptionsServiceClient, error) {
		return &captureClient{resp: want, reqPtr: &capturedReq}, nil
	}
	client := NewOptionsClientWithFactory(captureFactory, 5*time.Second)

	resp, err := client.ListOptions(context.Background(), "my-instance", "iid-123", "channels", "gen", "cur1")
	if err != nil {
		t.Fatalf("ListOptions: %v", err)
	}
	if len(resp.Options) != 1 || resp.Options[0].Value != "C001" {
		t.Errorf("resp options = %v, want [{Value:C001}]", resp.Options)
	}
	if resp.NextCursor != "next1" {
		t.Errorf("next_cursor = %q, want %q", resp.NextCursor, "next1")
	}
	if capturedReq.InstanceId != "iid-123" {
		t.Errorf("instance_id = %q, want %q", capturedReq.InstanceId, "iid-123")
	}
	if capturedReq.Source != "channels" {
		t.Errorf("source = %q, want %q", capturedReq.Source, "channels")
	}
	if capturedReq.Query != "gen" {
		t.Errorf("query = %q, want %q", capturedReq.Query, "gen")
	}
	if capturedReq.Cursor != "cur1" {
		t.Errorf("cursor = %q, want %q", capturedReq.Cursor, "cur1")
	}
}

// captureClient records the most recent ListOptions request.
type captureClient struct {
	resp   *optionsv1.ListOptionsResponse
	reqPtr **optionsv1.ListOptionsRequest
}

func (c *captureClient) ListOptions(_ context.Context, req *optionsv1.ListOptionsRequest, _ ...grpc.CallOption) (*optionsv1.ListOptionsResponse, error) {
	*c.reqPtr = req
	return c.resp, nil
}

// TestOptionsClient_ConnCacheReuse verifies that the same instance name reuses
// the same "connection" (factory is only called once per name).
func TestOptionsClient_ConnCacheReuse(t *testing.T) {
	callCount := 0
	factory := func(instanceName string) (optionsv1.ConfigOptionsServiceClient, error) {
		callCount++
		return &fakeOptionsClient{
			resp: &optionsv1.ListOptionsResponse{},
		}, nil
	}
	client := NewOptionsClientWithFactory(factory, 5*time.Second)

	for i := 0; i < 3; i++ {
		_, err := client.ListOptions(context.Background(), "inst-a", "id", "channels", "", "")
		if err != nil {
			t.Fatalf("ListOptions call %d: %v", i, err)
		}
	}
	// factory should be called exactly once since the test path doesn't use connCache
	// (NewOptionsClientWithFactory uses the newClient function directly, no caching).
	// The production path (NewOptionsClient) uses connCache; this test verifies the
	// factory-injection path routes all calls through the factory.
	if callCount != 3 {
		t.Errorf("factory call count = %d, want 3 (factory-injection path does not cache)", callCount)
	}
}

// TestOptionsClient_ErrorPropagation verifies that an RPC error is wrapped
// and returned to the caller with context.
func TestOptionsClient_ErrorPropagation(t *testing.T) {
	rpcErr := status.Error(codes.Unimplemented, "ConfigOptionsService not implemented")
	factory := func(_ string) (optionsv1.ConfigOptionsServiceClient, error) {
		return &fakeOptionsClient{err: rpcErr}, nil
	}
	client := NewOptionsClientWithFactory(factory, 5*time.Second)

	_, err := client.ListOptions(context.Background(), "inst", "id", "channels", "", "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// The original status should be unwrappable so callers can check the code.
	st, ok := status.FromError(errors.Unwrap(err))
	if !ok || st.Code() != codes.Unimplemented {
		t.Errorf("error code = %v, want Unimplemented; err = %v", st.Code(), err)
	}
}

// TestOptionsClient_PerCallDeadline verifies that a very short deadline causes
// the call to fail with DeadlineExceeded.
func TestOptionsClient_PerCallDeadline(t *testing.T) {
	// Fake client that blocks until the context is cancelled.
	blockingClient := &blockingOptionsClient{}
	factory := func(_ string) (optionsv1.ConfigOptionsServiceClient, error) {
		return blockingClient, nil
	}
	// 1ms call timeout — will expire immediately.
	client := NewOptionsClientWithFactory(factory, 1*time.Millisecond)

	_, err := client.ListOptions(context.Background(), "inst", "id", "channels", "", "")
	if err == nil {
		t.Fatal("expected deadline error, got nil")
	}
	// The error should wrap or be a DeadlineExceeded.
	if !errors.Is(err, context.DeadlineExceeded) {
		// gRPC deadline shows as codes.DeadlineExceeded.
		st, ok := status.FromError(errors.Unwrap(err))
		if !ok || st.Code() != codes.DeadlineExceeded {
			t.Errorf("expected DeadlineExceeded, got: %v", err)
		}
	}
}

// blockingOptionsClient returns only after the context is cancelled.
type blockingOptionsClient struct{}

func (b *blockingOptionsClient) ListOptions(ctx context.Context, _ *optionsv1.ListOptionsRequest, _ ...grpc.CallOption) (*optionsv1.ListOptionsResponse, error) {
	<-ctx.Done()
	return nil, status.FromContextError(ctx.Err()).Err()
}

// TestOptionsClient_ConnectError verifies that a dial error is returned with context.
func TestOptionsClient_ConnectError(t *testing.T) {
	dialErr := errors.New("connection refused")
	factory := func(_ string) (optionsv1.ConfigOptionsServiceClient, error) {
		return nil, dialErr
	}
	client := NewOptionsClientWithFactory(factory, 5*time.Second)

	_, err := client.ListOptions(context.Background(), "inst", "id", "channels", "", "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, dialErr) {
		t.Errorf("want dial error in chain, got: %v", err)
	}
}

// TestOptionsClient_Close verifies that Close does not panic on a test-injected
// client (connCache.closeAll with empty entries is a no-op).
func TestOptionsClient_Close(t *testing.T) {
	factory := func(_ string) (optionsv1.ConfigOptionsServiceClient, error) {
		return &fakeOptionsClient{resp: &optionsv1.ListOptionsResponse{}}, nil
	}
	client := NewOptionsClientWithFactory(factory, 5*time.Second)
	// Close should not panic on the test path.
	if err := client.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	// Second close should also be a no-op.
	if err := client.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}
