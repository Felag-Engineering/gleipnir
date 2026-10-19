//go:build dood && substratev2

package dood_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/mcp"
)

// slackToolCount is the number of tools plugins/slack-v2 registers (its
// manifest's tools list); toolslist_golden_test.go in the plugin pins the names.
const slackToolCount = 9

// TestDooD_SlackV2_InstallConvergeDiscover installs the real slack-v2 bundle
// (make slack-v2-bundle) and checks that it comes up as a managed workload and
// that discovery through the managed endpoint shows all three things the
// plugin offers: its tools, its channel, and its events.
//
// The instance has no credentials and no app token, so the Slack-facing half
// is idle by design (the socket hub logs "disabled" and waits). Discovery does
// not need either: this proves the packaged plugin installs, converges and
// speaks the contract on a real runtime. The flow that needs Slack —
// event, run, approval, decision record — is the in-process e2e in
// internal/plugin/e2e, against a fake Slack.
func TestDooD_SlackV2_InstallConvergeDiscover(t *testing.T) {
	h := newHarness(t)

	pluginID := h.InstallBundleFile("slack-v2")
	instanceID := h.CreateInstance(pluginID, "dood-slack")
	h.WaitConverged(instanceID)

	srv, err := h.ManagedServer(instanceID)
	if err != nil {
		t.Fatalf("instance has no managed endpoint row: %v", err)
	}
	if !mcp.IsManaged(srv) || srv.Name != "dood-slack" {
		t.Errorf("managed row = %+v, want a managed row named dood-slack", srv)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := h.Client(instanceID)

	// tools/list, live, and what the attach pass persisted from it.
	tools, err := client.DiscoverTools(ctx)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	if len(tools) != slackToolCount {
		t.Errorf("tools/list returned %d tools, want %d", len(tools), slackToolCount)
	}
	stored, err := h.q.ListMCPToolsByServer(ctx, srv.ID)
	if err != nil {
		t.Fatalf("ListMCPToolsByServer: %v", err)
	}
	if len(stored) != slackToolCount {
		t.Errorf("%d tools discovered into the registry, want %d", len(stored), slackToolCount)
	}

	// The channel declaration, which the handshake above established.
	channel, declared := client.ChannelCapabilityOf()
	if !declared {
		t.Fatal("slack-v2 did not declare the io.gleipnir/channel extension")
	}
	if channel.Assurance != mcp.ChannelAssuranceAuthenticated {
		t.Errorf("channel assurance = %q, want %q (the manifest's human_channel profile)", channel.Assurance, mcp.ChannelAssuranceAuthenticated)
	}

	// The events declaration and the kinds it serves.
	kinds, err := client.DiscoverEventKinds(ctx)
	if err != nil {
		t.Fatalf("events/discover: %v", err)
	}
	if _, declared := client.EventsCapabilityOf(); !declared {
		t.Error("slack-v2 did not declare the io.gleipnir/events extension")
	}
	var names []string
	for _, k := range kinds {
		names = append(names, k.Kind)
	}
	if !slices.Contains(names, "channel_message") {
		t.Errorf("events/discover kinds = %v, want channel_message among them", names)
	}
}
