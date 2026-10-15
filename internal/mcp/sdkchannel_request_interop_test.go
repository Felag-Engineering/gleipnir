// Cross-module contract test for the request leg of io.gleipnir/channel:
// internal/mcp.Client.ChannelRequest and PollScheduler (the host side) driven
// against plugin-sdk/channelext's Tasks-backed server — two independently
// written implementations of docs/developer/extension-io-gleipnir-channel.md §8.
// Mirrors sdkchannel_interop_test.go, which pins the notify leg.
package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/testutil"
	"github.com/felag-engineering/gleipnir/plugin-sdk/channelext"
	"github.com/felag-engineering/gleipnir/plugin-sdk/manifestv2"
	"github.com/felag-engineering/gleipnir/plugin-sdk/mcpserver"
)

type singleClientResolver struct{ client *mcp.Client }

func (r singleClientResolver) ClientForServerID(context.Context, string) (*mcp.Client, error) {
	return r.client, nil
}

type requestServer struct {
	client    *mcp.Client
	tasks     channelext.TaskStore
	mu        sync.Mutex
	asked     []channelext.Request
	cancelled []string
}

func (s *requestServer) cancelledIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.cancelled...)
}

func startSDKRequestServer(t *testing.T) *requestServer {
	t.Helper()
	rs := &requestServer{}

	ext, err := channelext.New(channelext.Declaration{
		Version:    channelext.ExtensionVersion,
		Assurance:  manifestv2.AssuranceAuthenticated,
		Deliveries: []string{channelext.DeliveryDirect, channelext.DeliveryShared},
	}, func(context.Context, channelext.Notification) error { return nil },
		channelext.WithRequest(func(_ context.Context, _ string, req channelext.Request) error {
			rs.mu.Lock()
			defer rs.mu.Unlock()
			rs.asked = append(rs.asked, req)
			return nil
		}),
		channelext.WithOnCancel(func(_ context.Context, taskID, _ string) {
			rs.mu.Lock()
			defer rs.mu.Unlock()
			rs.cancelled = append(rs.cancelled, taskID)
		}),
	)
	if err != nil {
		t.Fatalf("channelext.New: %v", err)
	}
	rs.tasks = ext.Tasks()

	srv := mcpserver.NewServer("cross-module-test", "1.0.0")
	if err := srv.Mount(ext); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	httpSrv := httptest.NewServer(srv)
	t.Cleanup(httpSrv.Close)

	rs.client = mcp.NewClient(httpSrv.URL,
		mcp.WithProtocolVersion(mcp.ProtocolVersion20260728),
		mcp.WithTrustTier(mcp.TrustTierManaged),
	)
	if _, err := rs.client.ProbeProtocolVersion(context.Background()); err != nil {
		t.Fatalf("ProbeProtocolVersion: %v", err)
	}
	return rs
}

// openTrackedTask asks the SDK server a question and records the returned
// handle as a resumable mcp_tasks row, the way the host does for a channel
// request.
func openTrackedTask(t *testing.T, s *db.Store, rs *requestServer) (rowID string, remote mcp.TaskStatus) {
	t.Helper()
	remote, err := rs.client.ChannelRequest(context.Background(), mcp.ChannelRequestParams{
		Target:  mcp.ChannelTarget{Delivery: mcp.ChannelDeliveryDirect, Address: "u1"},
		Message: "Approve the deploy?",
		Options: []mcp.ChannelOption{{ID: "approve", Label: "Approve"}, {ID: "deny", Label: "Deny"}},
		Kind:    mcp.ElicitationKindPermission,
	})
	if err != nil {
		t.Fatalf("ChannelRequest: %v", err)
	}
	if remote.TaskID == "" || remote.Status != mcp.TaskStatusWorking {
		t.Fatalf("ChannelRequest = %+v, want a working task with an id", remote)
	}

	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", testutil.MinimalWebhookPolicy)
	testutil.InsertRun(t, s, "r1", "p1", "running")
	testutil.InsertMcpServer(t, s, "srv1", "channel-server", "http://example.invalid")
	serverID := "srv1"
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := s.Queries().CreateMCPTask(context.Background(), db.CreateMCPTaskParams{
		ID: "task1", RunID: "r1", ServerID: &serverID, TaskID: remote.TaskID,
		Kind: "channel_request", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreateMCPTask: %v", err)
	}
	return "task1", remote
}

func TestCrossModule_ChannelRequestResolvesThroughPollScheduler(t *testing.T) {
	rs := startSDKRequestServer(t)
	s := testutil.NewTestStore(t)
	rowID, remote := openTrackedTask(t, s, rs)

	if remote.PollInterval <= 0 || remote.TTL <= 0 {
		t.Errorf("envelope pollInterval=%v ttl=%v, want both advertised", remote.PollInterval, remote.TTL)
	}
	if len(rs.asked) != 1 || rs.asked[0].Message != "Approve the deploy?" || rs.asked[0].Kind != "permission" || len(rs.asked[0].Options) != 2 {
		t.Fatalf("the SDK RequestFunc saw %+v, did not round-trip", rs.asked)
	}

	var resolvedErr error
	resolved := make(chan struct{})
	sched := mcp.NewPollScheduler(s.Queries(), singleClientResolver{client: rs.client},
		mcp.WithOnResolved(func(_ context.Context, _ db.McpTask, err error) {
			resolvedErr = err
			close(resolved)
		}),
	)

	// Still working: a poll leaves the row open.
	if err := sched.PollNow(context.Background(), rowID); err != nil {
		t.Fatalf("PollNow (working): %v", err)
	}
	if row, _ := s.Queries().GetMCPTask(context.Background(), rowID); row.Status != "working" {
		t.Fatalf("row status = %q, want working before the human answers", row.Status)
	}

	if _, err := rs.tasks.Complete(context.Background(), remote.TaskID, channelext.Resolution{
		OptionID: "approve", ActorExternalID: "U-alice",
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if err := sched.PollNow(context.Background(), rowID); err != nil {
		t.Fatalf("PollNow (completed): %v", err)
	}
	select {
	case <-resolved:
	default:
		t.Fatal("PollScheduler did not report the task resolved")
	}
	if resolvedErr != nil {
		t.Fatalf("OnResolved err = %v, want nil for a completed task", resolvedErr)
	}

	row, err := s.Queries().GetMCPTask(context.Background(), rowID)
	if err != nil {
		t.Fatalf("GetMCPTask: %v", err)
	}
	if row.Status != "complete" || row.Result == nil {
		t.Fatalf("row = status %q result %v, want complete with a result", row.Status, row.Result)
	}
	res, err := mcp.DecodeChannelResolution(json.RawMessage(*row.Result))
	if err != nil {
		t.Fatalf("DecodeChannelResolution: %v", err)
	}
	if res.OptionID != "approve" || res.ActorExternalID != "U-alice" {
		t.Errorf("resolution = %+v, want approve by U-alice", res)
	}
}

func TestCrossModule_ChannelRequestCancelReachesTheSDKHook(t *testing.T) {
	rs := startSDKRequestServer(t)
	s := testutil.NewTestStore(t)
	rowID, remote := openTrackedTask(t, s, rs)

	sched := mcp.NewPollScheduler(s.Queries(), singleClientResolver{client: rs.client})
	if err := sched.Cancel(context.Background(), rowID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	if got := rs.cancelledIDs(); len(got) != 1 || got[0] != remote.TaskID {
		t.Errorf("cancel hook calls = %v, want [%s]", got, remote.TaskID)
	}
	if row, _ := s.Queries().GetMCPTask(context.Background(), rowID); row.Status != "cancelled" {
		t.Errorf("row status = %q, want cancelled", row.Status)
	}
	// The human's late answer must lose to the cancel.
	_, err := rs.tasks.Complete(context.Background(), remote.TaskID, channelext.Resolution{OptionID: "approve"})
	if !errors.Is(err, channelext.ErrTaskTerminal) {
		t.Errorf("Complete after cancel = %v, want ErrTaskTerminal", err)
	}
}
