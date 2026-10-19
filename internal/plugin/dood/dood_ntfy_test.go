//go:build dood && substratev2

package dood_test

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/plugin/egress"
	"github.com/felag-engineering/gleipnir/internal/plugin/hitl"
	"github.com/felag-engineering/gleipnir/internal/plugin/hostendpoint"
	"github.com/felag-engineering/gleipnir/internal/plugin/reconciler"
)

// ntfyBundle is the signed ntfy bundle (make ntfy-bundle), kept beside the
// conformance-stub bundles in the bundle directory.
const ntfyBundle = "ntfy"

// assemblyClients resolves a plugin instance to its channel client the way the
// assembly does for the Notify path: the instance's managed mcp_servers row,
// then the one shared registry. It returns errors rather than failing the test
// because Notifier calls it from its own goroutines.
type assemblyClients struct{ h *harness }

func (c assemblyClients) ChannelClientFor(instanceID string) (hitl.NotifyClient, error) {
	srv, err := c.h.ManagedServer(instanceID)
	if err != nil {
		return nil, fmt.Errorf("no managed endpoint for instance %s: %w", instanceID, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := c.h.mcpReg.ClientForServerID(ctx, srv.ID)
	if err != nil {
		return nil, fmt.Errorf("no mcp client for instance %s: %w", instanceID, err)
	}
	return client, nil
}

// SetInstanceConfig writes an instance's config_json, as the admin config
// endpoint would after validating it against the manifest.
func (h *harness) SetInstanceConfig(instanceID, configJSON string) {
	h.t.Helper()
	ctx := context.Background()
	inst, err := h.q.GetPluginInstanceByID(ctx, instanceID)
	if err != nil {
		h.t.Fatalf("GetPluginInstanceByID(%s): %v", instanceID, err)
	}
	n, err := h.q.UpdatePluginInstanceConfig(ctx, db.UpdatePluginInstanceConfigParams{
		ConfigJson:      configJSON,
		UpdatedAt:       time.Now().UTC().Format(time.RFC3339Nano),
		ID:              instanceID,
		ExpectedVersion: inst.Version,
	})
	if err != nil || n != 1 {
		h.t.Fatalf("writing config of instance %s: rows=%d err=%v", instanceID, n, err)
	}
}

// WaitUntil re-evaluates ready after every reconciler pass until it returns "",
// failing with the last reason if the convergence budget runs out.
func (h *harness) WaitUntil(what string, ready func() string) {
	h.t.Helper()
	deadline := time.After(convergeBudget)
	for {
		why := ready()
		if why == "" {
			return
		}
		select {
		case <-h.pub.passes:
		case <-deadline:
			h.t.Fatalf("%s did not happen within %s; last state: %s", what, convergeBudget, why)
		}
	}
}

// TestDooD_NtfyNotifyEndToEnd drives the real ntfy v2 plugin from install to
// teardown against a real runtime: it is installed from its signed bundle,
// reconciled, discovered, and sent a channel/notify through hitl.Notifier,
// next to a second entry whose instance is stopped.
//
// What is asserted, and what is not:
//   - The plugin handled the notification: it called host/get_instance_config
//     and host/log on the host endpoint, authenticated as this instance's
//     first-boot generation.
//   - The egress proxy allowed a tunnel to ntfy.sh (its only grant) and refused
//     a CONNECT to an undeclared domain issued from the instance's own network.
//   - The stopped instance's entry is recorded as notify_failed and does not
//     touch the live one.
//   - Teardown withdraws the endpoint row and both listeners.
//
// Whether ntfy.sh accepted the message is deliberately not asserted. The proxy
// refuses private-range targets, so a local stand-in for ntfy.sh is impossible
// without a test-only dial override in the proxy, which would be a security
// regression; and the public service is not ours to depend on for a verdict.
func TestDooD_NtfyNotifyEndToEnd(t *testing.T) {
	h := newHarness(t)

	// 1. Install through the production installer: TOFU pin and image digest
	// check happen there, and approval goes through the admin handler.
	pluginID := h.InstallBundleFile(ntfyBundle)

	// 2. Two instances. The live one is configured the way an operator would.
	live := h.CreateInstance(pluginID, "ntfy-live")
	dead := h.CreateInstance(pluginID, "ntfy-dead")
	h.SetInstanceConfig(live, `{"server_url":"https://ntfy.sh"}`)

	// 3. Converge. ntfy serves no tools, so there is nothing to discover.
	h.WaitRunning(live, dead)

	// 4. server/discover shows the channel extension, on both instances (the
	// dead one has to have been a healthy channel before it stopped).
	for id, name := range map[string]string{live: "ntfy-live", dead: "ntfy-dead"} {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		probe, err := h.Client(id).ProbeProtocolVersion(ctx)
		cancel()
		if err != nil {
			t.Fatalf("server/discover on %s: %v", name, err)
		}
		if !probe.ChannelDeclared {
			t.Fatalf("server/discover on %s did not declare io.gleipnir/channel: %+v", name, probe)
		}
		if !probe.Channel.Supports(mcp.ChannelDeliveryShared) {
			t.Errorf("%s declares channel %+v, want shared delivery", name, probe.Channel)
		}
	}
	if !slices.Contains(h.asm.HostListenerInstances(), live) || !slices.Contains(h.asm.EgressListenerInstances(), live) {
		t.Fatalf("instance has host listeners %v and egress listeners %v; want both to include it",
			h.asm.HostListenerInstances(), h.asm.EgressListenerInstances())
	}

	// 5. Take the second instance out of service the way an operator does. A
	// deactivation is a desired-state change, so unlike killing its container
	// the reconciler will not bring it back mid-assertion.
	if rec := h.post("/plugins/"+pluginID+"/instances/"+dead+"/deactivate", ""); rec.Code != http.StatusOK {
		t.Fatalf("deactivate = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	h.WaitUntil("the second instance's container stopping", func() string {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, running := h.runningContainer(ctx, dead); running {
			return "container still running"
		}
		return ""
	})

	// 6. Notify through the real Notifier.
	notifier := hitl.NewNotifier(hitl.NotifyConfig{Clients: assemblyClients{h}, Audit: h.q})
	target := mcp.ChannelTarget{Delivery: mcp.ChannelDeliveryShared, Address: "gleipnir-dood-" + h.runID}
	entries := []hitl.Entry{
		{EntryID: "entry-ntfy-live", InstanceID: live, Notify: true, Target: target},
		{EntryID: "entry-ntfy-dead", InstanceID: dead, Notify: true, Target: target},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	report := notifier.Notify(ctx, "", "Gleipnir DooD suite: notification test", entries)

	if report.Attempted != 2 {
		t.Errorf("attempted %d entries, want 2", report.Attempted)
	}
	failures := map[string]hitl.NotifyFailure{}
	for _, f := range report.Failed {
		failures[f.EntryID] = f
	}
	deadFailure, ok := failures["entry-ntfy-dead"]
	if !ok {
		t.Fatalf("no failure recorded for the stopped instance's entry: %+v", report.Failed)
	}
	t.Logf("stopped instance's entry failed as %s: %s", deadFailure.Reason, deadFailure.Detail)
	if deadFailure.InstanceID != dead {
		t.Errorf("failure names instance %q, want %q", deadFailure.InstanceID, dead)
	}
	// Delivery to ntfy.sh is the one thing not asserted, so an rpc failure on
	// the live entry (the service refusing or unreachable) is tolerated. Every
	// other reason is a fault on the host's side of the contract.
	if f, failed := failures["entry-ntfy-live"]; failed {
		if f.Reason != hitl.NotifyFailureRPCError {
			t.Errorf("live entry failed as %s (%s); only a plugin-reported delivery error is tolerated", f.Reason, f.Detail)
		} else {
			t.Logf("live entry reported a delivery error (not asserted): %s", f.Detail)
		}
	}

	// The failure is persisted as the operational record.
	audit, err := h.q.ListPluginAuditEventsByInstance(ctx, db.ListPluginAuditEventsByInstanceParams{
		PluginInstanceID: &dead, Limit: 50,
	})
	if err != nil {
		t.Fatalf("listing audit events: %v", err)
	}
	var sawNotifyFailed bool
	for _, e := range audit {
		if e.EventType == "notify_failed" && strings.Contains(e.PayloadJson, "entry-ntfy-dead") {
			sawNotifyFailed = true
		}
	}
	if !sawNotifyFailed {
		t.Errorf("no notify_failed audit event for the stopped instance among %d events", len(audit))
	}

	// 7. The plugin handled channel/notify: its calls reached the host
	// endpoint, authenticated as this instance's first-boot generation.
	calls := h.HostCalls(live)
	var sawConfig, sawLog bool
	for _, c := range calls {
		if c.Generation != 1 {
			t.Errorf("host call %+v authenticated as generation %d, want the first-boot generation 1", c, c.Generation)
		}
		switch c.Tool {
		case hostendpoint.ToolGetInstanceConfig:
			sawConfig = true
		case hostendpoint.ToolLog:
			sawLog = true
		}
	}
	if !sawConfig || !sawLog {
		t.Errorf("host calls from the live instance = %+v, want host/get_instance_config and host/log", calls)
	}
	gens, err := h.q.ListContainerGenerationsByInstance(ctx, live)
	if err != nil {
		t.Fatalf("listing generations: %v", err)
	}
	if len(gens) != 1 || gens[0].Generation != 1 {
		t.Errorf("live instance generations = %+v, want exactly the first-boot generation: a rotation would make the generation check meaningless", gens)
	}

	// 8. Egress. The tunnel the plugin opened to ntfy.sh was allowed; the
	// deny is checked from a probe sharing the instance's network, which the
	// proxy identifies by the address it arrived on, so it holds the live
	// instance's grants and nothing else.
	proxy := h.asm.EgressProxy()
	allowed, _ := proxy.Counters()
	if allowed < 1 {
		t.Errorf("egress allowed = %d, want a tunnel to ntfy.sh to have been allowed", allowed)
	}

	topo := h.Topology(live)
	egressAddr := fmt.Sprintf("%s:%d", topo.Host, reconciler.DefaultEgressProxyPort)
	_, deniedBefore := proxy.Counters()
	// stdin stays open while the proxy answers: an EOF right after the request
	// reads to the server as a disconnect and cancels the request context the
	// refusal's audit write runs on.
	output := h.runProbe("egress-deny", topo.Network.Name, nil, fmt.Sprintf(
		`(printf 'CONNECT undeclared.example:443 HTTP/1.1\r\nHost: undeclared.example:443\r\n\r\n'; sleep 2) | nc -w 3 %s %d`,
		topo.Host, reconciler.DefaultEgressProxyPort))
	if !strings.Contains(output, " 403 ") || !strings.Contains(output, "undeclared.example") {
		t.Errorf("CONNECT to an undeclared domain via %s answered %q, want a 403 naming the host", egressAddr, strings.TrimSpace(output))
	}
	allowedAfter, deniedAfter := proxy.Counters()
	if got := deniedAfter[egress.DenyNotGranted] - deniedBefore[egress.DenyNotGranted]; got != 1 {
		t.Errorf("%s denials rose by %d, want 1: %v -> %v", egress.DenyNotGranted, got, deniedBefore, deniedAfter)
	}
	if allowedAfter != allowed {
		t.Errorf("allowed count moved from %d to %d across the denied CONNECT", allowed, allowedAfter)
	}
	denials, err := h.q.ListPluginAuditEventsByType(ctx, db.ListPluginAuditEventsByTypeParams{
		EventType: "plugin_egress_denied", Limit: 50,
	})
	if err != nil {
		t.Fatalf("listing egress denial audit events: %v", err)
	}
	var sawDenialAudit bool
	for _, e := range denials {
		if e.PluginInstanceID != nil && *e.PluginInstanceID == live &&
			strings.Contains(e.PayloadJson, "undeclared.example") && strings.Contains(e.PayloadJson, string(egress.DenyNotGranted)) {
			sawDenialAudit = true
		}
	}
	if !sawDenialAudit {
		t.Errorf("no plugin_egress_denied audit event names undeclared.example for the instance among %d events", len(denials))
	}

	// 9. Teardown withdraws the endpoint row and both listeners.
	for _, id := range []string{live, dead} {
		if rec := h.do(http.MethodDelete, "/plugins/"+pluginID+"/instances/"+id, ""); rec.Code != http.StatusNoContent {
			t.Fatalf("delete %s = %d, want 204: %s", id, rec.Code, rec.Body.String())
		}
	}
	for _, id := range []string{live, dead} {
		if _, err := h.ManagedServer(id); err == nil {
			t.Errorf("instance %s still has a managed mcp_servers row after delete", id)
		}
	}
	h.WaitUntil("both listeners and the container going away", func() string {
		switch {
		case slices.Contains(h.asm.HostListenerInstances(), live):
			return "host endpoint listener still bound"
		case slices.Contains(h.asm.EgressListenerInstances(), live):
			return "egress proxy listener still bound"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, id := range []string{live, dead} {
			list, err := h.rt.ListByLabel(ctx, reconciler.LabelInstance, id)
			if err != nil || len(list) != 0 {
				return fmt.Sprintf("instance %s still has %d containers (err %v)", id, len(list), err)
			}
		}
		return ""
	})
}
