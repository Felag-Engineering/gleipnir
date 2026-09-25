// Cross-module contract test: internal/mcp.Client's io.gleipnir/channel
// CLIENT (channel.go, #800) driven against plugin-sdk/channelext's SERVER
// helper mounted on a plugin-sdk/mcpserver.Server — two independently
// written implementations of
// docs/developer/extension-io-gleipnir-channel.md's channel/notify leg.
// Mirrors sdkevents_integration_test.go (the same pin for io.gleipnir/events)
// and sdkserver_interop_test.go (the same pin for tools/list+tools/call).
package mcp_test

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/plugin-sdk/channelext"
	"github.com/felag-engineering/gleipnir/plugin-sdk/manifestv2"
	"github.com/felag-engineering/gleipnir/plugin-sdk/mcpserver"
)

// recordingNotifier is a channelext.NotifyFunc that records every delivery it
// receives, so a test can assert on what crossed the wire without a real
// channel behind it.
type recordingNotifier struct {
	mu            sync.Mutex
	notifications []channelext.Notification
}

func (r *recordingNotifier) notify(_ context.Context, n channelext.Notification) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.notifications = append(r.notifications, n)
	return nil
}

func (r *recordingNotifier) last() (channelext.Notification, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.notifications) == 0 {
		return channelext.Notification{}, false
	}
	return r.notifications[len(r.notifications)-1], true
}

// startSDKChannelServer mounts channelext on a plugin-sdk/mcpserver.Server,
// serves it over HTTP, and returns a probed managed modern client pointed at
// it. The managed tier is declared explicitly because io.gleipnir/* is
// host-plane (gate.go): an external server's declaration is dropped, so a
// channel test that did not say which tier it was exercising would be
// testing the drop instead of the extension.
func startSDKChannelServer(t *testing.T, notifier *recordingNotifier) *mcp.Client {
	t.Helper()

	ext, err := channelext.New(channelext.Declaration{
		Version:    channelext.ExtensionVersion,
		Assurance:  manifestv2.AssuranceAuthenticated,
		Deliveries: []string{channelext.DeliveryDirect, channelext.DeliveryShared},
	}, notifier.notify)
	if err != nil {
		t.Fatalf("channelext.New: %v", err)
	}

	srv := mcpserver.NewServer("cross-module-test", "1.0.0")
	if err := srv.Mount(ext); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	httpSrv := httptest.NewServer(srv)
	t.Cleanup(httpSrv.Close)

	client := mcp.NewClient(httpSrv.URL,
		mcp.WithProtocolVersion(mcp.ProtocolVersion20260728),
		mcp.WithTrustTier(mcp.TrustTierManaged),
	)
	probe, err := client.ProbeProtocolVersion(context.Background())
	if err != nil {
		t.Fatalf("ProbeProtocolVersion against the SDK channel server: %v", err)
	}
	if !probe.ChannelDeclared {
		t.Fatalf("SDK channel server's server/discover did not declare %s to the client", mcp.ExtensionChannel)
	}
	if probe.Channel.Assurance != mcp.ChannelAssuranceAuthenticated {
		t.Fatalf("declared assurance = %q, want authenticated", probe.Channel.Assurance)
	}
	if !probe.Channel.Supports(mcp.ChannelDeliveryDirect) || !probe.Channel.Supports(mcp.ChannelDeliveryShared) {
		t.Fatalf("declared deliveries = %v, want both direct and shared", probe.Channel)
	}
	return client
}

func TestCrossModule_ChannelNotifyDeliversToTheSDKExtension(t *testing.T) {
	notifier := &recordingNotifier{}
	client := startSDKChannelServer(t, notifier)

	err := client.ChannelNotify(context.Background(), mcp.ChannelNotification{
		Target:  mcp.ChannelTarget{Delivery: mcp.ChannelDeliveryShared, Address: "space-42"},
		Message: "input requested: https://gleipnir.example/runs/r-1",
	})
	if err != nil {
		t.Fatalf("ChannelNotify: %v", err)
	}

	got, ok := notifier.last()
	if !ok {
		t.Fatal("the SDK extension's notify handler was never called")
	}
	if got.Target.Delivery != channelext.DeliveryShared || got.Target.Address != "space-42" {
		t.Errorf("target = %+v, want shared/space-42", got.Target)
	}
	if got.Message != "input requested: https://gleipnir.example/runs/r-1" {
		t.Errorf("message = %q, did not round-trip", got.Message)
	}
}

func TestCrossModule_ChannelNotifyRejectsAnUnknownDelivery(t *testing.T) {
	// internal/mcp.Client already refuses an invalid target client-side
	// (validateChannelTarget), so this instead proves the SDK server's own
	// refusal by bypassing that guard: an unknown delivery must never reach
	// the notify handler as a silent success.
	notifier := &recordingNotifier{}
	ext, err := channelext.New(channelext.Declaration{
		Version:    channelext.ExtensionVersion,
		Assurance:  manifestv2.AssuranceAuthenticated,
		Deliveries: []string{channelext.DeliveryDirect},
	}, notifier.notify)
	if err != nil {
		t.Fatalf("channelext.New: %v", err)
	}
	srv := mcpserver.NewServer("cross-module-test", "1.0.0")
	if err := srv.Mount(ext); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	httpSrv := httptest.NewServer(srv)
	t.Cleanup(httpSrv.Close)

	client := mcp.NewClient(httpSrv.URL,
		mcp.WithProtocolVersion(mcp.ProtocolVersion20260728),
		mcp.WithTrustTier(mcp.TrustTierManaged),
	)
	if _, err := client.ProbeProtocolVersion(context.Background()); err != nil {
		t.Fatalf("ProbeProtocolVersion: %v", err)
	}

	// Client-side validation rejects this target before it is ever sent, so
	// this pins the guarantee from the client's own perspective: the call
	// fails, and nothing reaches the SDK extension.
	err = client.ChannelNotify(context.Background(), mcp.ChannelNotification{
		Target:  mcp.ChannelTarget{Delivery: "dm", Address: "u1"},
		Message: "x",
	})
	if err == nil {
		t.Fatal("ChannelNotify with an unknown delivery succeeded, want an error")
	}
	if _, ok := notifier.last(); ok {
		t.Fatal("the SDK extension's notify handler ran despite an invalid target")
	}
}

// TestCrossModule_ChannelRequestFailsWithoutARequestHandler pins the
// contract's worked example C from the client's own perspective: a
// channelext server mounted with no request handler (#967 not yet landed)
// never claims channel/request, so the call fails rather than hanging or
// returning a mis-shapen success. (mcpserver answers method-not-found with
// HTTP 404, which internal/mcp.Client surfaces as an opaque transport error
// rather than a decoded JSON-RPC error for any RPC but server/discover —
// the same shape TestCrossModule_CallToolUnknownName pins for tools/call.)
func TestCrossModule_ChannelRequestFailsWithoutARequestHandler(t *testing.T) {
	notifier := &recordingNotifier{}
	client := startSDKChannelServer(t, notifier)

	_, err := client.ChannelRequest(context.Background(), mcp.ChannelRequestParams{
		Target:  mcp.ChannelTarget{Delivery: mcp.ChannelDeliveryDirect, Address: "u1"},
		Message: "approve?",
		Options: []mcp.ChannelOption{{ID: "approve", Label: "Approve"}},
	})
	if err == nil {
		t.Fatal("ChannelRequest against a notify-only channelext server succeeded, want an error")
	}
}
