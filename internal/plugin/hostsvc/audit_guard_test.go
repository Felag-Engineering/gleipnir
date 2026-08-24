package hostsvc_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/plugin/hostsvc"
	sdkproto "github.com/felag-engineering/gleipnir/plugin-sdk/proto"
)

// fakeAuditQuerier records InsertPluginAuditEvent calls in memory.
// It mirrors the pattern in internal/plugin/tools/registrar_test.go.
type fakeAuditQuerier struct {
	mu     sync.Mutex
	events []db.InsertPluginAuditEventParams
	err    error // if non-nil, InsertPluginAuditEvent returns this error
}

func (f *fakeAuditQuerier) InsertPluginAuditEvent(_ context.Context, arg db.InsertPluginAuditEventParams) (db.PluginAuditEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return db.PluginAuditEvent{}, f.err
	}
	f.events = append(f.events, arg)
	return db.PluginAuditEvent{}, nil
}

func (f *fakeAuditQuerier) all() []db.InsertPluginAuditEventParams {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]db.InsertPluginAuditEventParams, len(f.events))
	copy(out, f.events)
	return out
}

// contextWithCallID builds a handler-side context with the interceptor's call ID
// already attached, simulating what UnaryCallIDInterceptor does in production.
func contextWithCallID(callID string) context.Context {
	md := metadata.Pairs(sdkproto.CallIDMetadataKey, callID)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	interceptor := hostsvc.UnaryCallIDInterceptor()
	var handlerCtx context.Context
	_, _ = interceptor(ctx, nil, nil, func(c context.Context, _ any) (any, error) {
		handlerCtx = c
		return nil, nil
	})
	return handlerCtx
}

func TestRejectIfDetached(t *testing.T) {
	t.Parallel()

	const instanceID = "inst-001"
	const rpcMethod = "/gleipnir.plugin.host.v1.HostService/SomeRPC"

	t.Run("ctx with call ID — returns nil, no audit row", func(t *testing.T) {
		q := &fakeAuditQuerier{}
		ctx := contextWithCallID("call-111")

		err := hostsvc.RejectIfDetached(ctx, q, instanceID, rpcMethod)
		if err != nil {
			t.Errorf("RejectIfDetached returned %v, want nil", err)
		}
		if rows := q.all(); len(rows) != 0 {
			t.Errorf("unexpected audit rows: %v", rows)
		}
	})

	t.Run("ctx without call ID — returns PermissionDenied + one audit row", func(t *testing.T) {
		q := &fakeAuditQuerier{}

		err := hostsvc.RejectIfDetached(context.Background(), q, instanceID, rpcMethod)
		if err == nil {
			t.Fatal("expected PermissionDenied error, got nil")
		}
		st, ok := status.FromError(err)
		if !ok {
			t.Fatalf("expected gRPC status error, got: %v", err)
		}
		if st.Code() != codes.PermissionDenied {
			t.Errorf("status code = %v, want PermissionDenied", st.Code())
		}
		if st.Message() != "unauthorized_call_context" {
			t.Errorf("status message = %q, want \"unauthorized_call_context\"", st.Message())
		}

		rows := q.all()
		if len(rows) != 1 {
			t.Fatalf("audit row count = %d, want 1", len(rows))
		}
		row := rows[0]

		if row.EventType != hostsvc.EventTypeUnauthorizedCallContext {
			t.Errorf("event_type = %q, want %q", row.EventType, hostsvc.EventTypeUnauthorizedCallContext)
		}
		if row.Severity != "high" {
			t.Errorf("severity = %q, want \"high\"", row.Severity)
		}
		if row.PluginInstanceID == nil || *row.PluginInstanceID != instanceID {
			t.Errorf("plugin_instance_id = %v, want &%q", row.PluginInstanceID, instanceID)
		}
		if row.CreatedAt == "" {
			t.Error("created_at is empty")
		}
		if _, parseErr := time.Parse(time.RFC3339Nano, row.CreatedAt); parseErr != nil {
			t.Errorf("created_at %q is not valid RFC3339Nano: %v", row.CreatedAt, parseErr)
		}
	})

	t.Run("audit insert failure — still returns PermissionDenied, no panic", func(t *testing.T) {
		q := &fakeAuditQuerier{err: errors.New("db unavailable")}

		err := hostsvc.RejectIfDetached(context.Background(), q, instanceID, rpcMethod)
		if err == nil {
			t.Fatal("expected PermissionDenied error, got nil")
		}
		st, ok := status.FromError(err)
		if !ok || st.Code() != codes.PermissionDenied {
			t.Errorf("expected PermissionDenied, got %v", err)
		}
	})
}

// compile-time check: AuditQuerier is satisfied by *fakeAuditQuerier.
var _ hostsvc.AuditQuerier = (*fakeAuditQuerier)(nil)

func TestRejectIfTier2NotDeclared(t *testing.T) {
	t.Parallel()

	const instanceID = "inst-002"
	const capability = "run_history_read"
	const rpcMethod = "/gleipnir.plugin.host.v1.HostService/RunHistoryRead"

	t.Run("hasCapability=true — returns nil, no audit row", func(t *testing.T) {
		q := &fakeAuditQuerier{}

		err := hostsvc.RejectIfTier2NotDeclared(context.Background(), q, instanceID, capability, rpcMethod, true)
		if err != nil {
			t.Errorf("RejectIfTier2NotDeclared returned %v, want nil", err)
		}
		if rows := q.all(); len(rows) != 0 {
			t.Errorf("unexpected audit rows: %v", rows)
		}
	})

	t.Run("hasCapability=false — returns PermissionDenied + one audit row", func(t *testing.T) {
		q := &fakeAuditQuerier{}

		err := hostsvc.RejectIfTier2NotDeclared(context.Background(), q, instanceID, capability, rpcMethod, false)
		if err == nil {
			t.Fatal("expected PermissionDenied error, got nil")
		}
		st, ok := status.FromError(err)
		if !ok {
			t.Fatalf("expected gRPC status error, got: %v", err)
		}
		if st.Code() != codes.PermissionDenied {
			t.Errorf("status code = %v, want PermissionDenied", st.Code())
		}
		if st.Message() != "unauthorized_tier2_call" {
			t.Errorf("status message = %q, want \"unauthorized_tier2_call\"", st.Message())
		}

		rows := q.all()
		if len(rows) != 1 {
			t.Fatalf("audit row count = %d, want 1", len(rows))
		}
		row := rows[0]

		if row.EventType != hostsvc.EventTypeUnauthorizedTier2Call {
			t.Errorf("event_type = %q, want %q", row.EventType, hostsvc.EventTypeUnauthorizedTier2Call)
		}
		if row.Severity != "high" {
			t.Errorf("severity = %q, want \"high\"", row.Severity)
		}
		if row.PluginInstanceID == nil || *row.PluginInstanceID != instanceID {
			t.Errorf("plugin_instance_id = %v, want &%q", row.PluginInstanceID, instanceID)
		}
		if _, parseErr := time.Parse(time.RFC3339Nano, row.CreatedAt); parseErr != nil {
			t.Errorf("created_at %q is not valid RFC3339Nano: %v", row.CreatedAt, parseErr)
		}

		// Verify payload contains rpc_method and capability.
		var payload map[string]string
		if err := json.Unmarshal([]byte(row.PayloadJson), &payload); err != nil {
			t.Fatalf("payload json parse: %v", err)
		}
		if payload["rpc_method"] != rpcMethod {
			t.Errorf("payload.rpc_method = %q, want %q", payload["rpc_method"], rpcMethod)
		}
		if payload["capability"] != capability {
			t.Errorf("payload.capability = %q, want %q", payload["capability"], capability)
		}
	})

	t.Run("audit insert failure — still returns PermissionDenied, no panic", func(t *testing.T) {
		q := &fakeAuditQuerier{err: errors.New("db unavailable")}

		err := hostsvc.RejectIfTier2NotDeclared(context.Background(), q, instanceID, capability, rpcMethod, false)
		if err == nil {
			t.Fatal("expected PermissionDenied error, got nil")
		}
		st, ok := status.FromError(err)
		if !ok || st.Code() != codes.PermissionDenied {
			t.Errorf("expected PermissionDenied, got %v", err)
		}
	})
}
