//go:build dood && substratev2

// The `dood` and `substratev2` build tags make this opt-in at COMPILE time
// rather than skipped at run time, and that is the point: a t.Skip() when no
// socket is present reads as a passing test in every log that only counts
// failures, so the one suite whose whole job is "we checked against a real
// runtime, from where a plugin's neighbours can be reached" would be silently
// absent exactly where nobody is watching. A build tag makes running it a
// decision someone made.
//
// Do not run it with `go test` directly: the test binary has to run inside a
// container with the runtime socket mounted, which is what
// scripts/run-dood-suite.sh arranges for CI and developers alike.

package dood_test

import (
	"context"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/plugin/hostendpoint"
	"github.com/felag-engineering/gleipnir/internal/plugin/reconciler"
)

// stubMCPPort is the port the conformance stub serves its MCP endpoint on, from
// its manifest's package.transport.port.
const stubMCPPort uint16 = 8080

// TestDooD_InstallReconcileCallIsolateHostPlane walks the substrate end to end
// against a real runtime: two instances of a signed bundle are installed,
// reconciled to running, called through their managed endpoints, and then
// checked for east-west isolation and the host-plane invariant.
//
// Both instances are of the "all" bundle, the only mode that is correct under
// the image's default environment: the other modes need STUB_PROFILES set, and
// an instance has no way to carry environment today.
func TestDooD_InstallReconcileCallIsolateHostPlane(t *testing.T) {
	h := newHarness(t)

	// 1. Install and approve the bundle, then create the two instances.
	pluginID := h.InstallBundle("all")
	instanceA := h.CreateInstance(pluginID, "dood-a")
	instanceB := h.CreateInstance(pluginID, "dood-b")

	// 2. Converge both.
	h.WaitConverged(instanceA, instanceB)

	// 3. Two managed rows appeared, one per instance, named for it.
	for id, name := range map[string]string{instanceA: "dood-a", instanceB: "dood-b"} {
		srv, err := h.ManagedServer(id)
		if err != nil {
			t.Fatalf("instance %s has no managed endpoint row: %v", name, err)
		}
		if !mcp.IsManaged(srv) {
			t.Errorf("instance %s endpoint %+v is not a managed row", name, srv)
		}
		if srv.Name != name {
			t.Errorf("managed row name = %q, want %q", srv.Name, name)
		}
	}

	// 4. A tool call through the managed endpoint, on both instances.
	for id, name := range map[string]string{instanceA: "dood-a", instanceB: "dood-b"} {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		result, err := h.Client(id).CallTool(ctx, "conformance.echo", map[string]any{"message": "hello from " + name}, mcp.CallOptions{})
		cancel()
		if err != nil {
			t.Fatalf("conformance.echo on %s: %v", name, err)
		}
		if result.IsError || !strings.Contains(string(result.Output), "hello from "+name) {
			t.Errorf("conformance.echo on %s = %s (isError=%v), want it echoed back", name, result.Output, result.IsError)
		}
	}

	// 5. Isolation. Every probe is a container attached ONLY to A's network.
	a, b := h.Topology(instanceA), h.Topology(instanceB)
	if a.Subnet.Overlaps(b.Subnet) {
		t.Fatalf("instance networks %s and %s overlap; isolation is topological and overlapping ranges defeat it",
			a.Subnet, b.Subnet)
	}

	hostPort := uint16(reconciler.DefaultHostEndpointPort)
	probes := []struct {
		name      string
		target    string
		addr      netip.AddrPort
		wantReach bool
		why       string
	}{
		// The controls come first: if the probe cannot reach A's own endpoints,
		// "cannot reach B's" proves only that the probe is broken.
		{"own-mcp", "A's own MCP endpoint", netip.AddrPortFrom(a.Plugin, stubMCPPort), true,
			"the probe must be able to reach its own network's plugin or its negatives mean nothing"},
		{"own-host", "A's host endpoint", netip.AddrPortFrom(a.Host, hostPort), true,
			"a plugin must reach the host endpoint on its own network"},
		{"sibling-mcp", "B's MCP endpoint", netip.AddrPortFrom(b.Plugin, stubMCPPort), false,
			"east-west isolation: a plugin must not reach a sibling's MCP endpoint"},
		{"sibling-host", "B's host listener", netip.AddrPortFrom(b.Host, hostPort), false,
			"a plugin must not reach the host listener bound on another instance's network"},
	}
	for _, p := range probes {
		reached, output := h.Dial("a-to-"+p.name, a.Network.Name, p.addr)
		if reached != p.wantReach {
			t.Errorf("probe on A's network -> %s (%s): reached=%v, want %v: %s\noutput: %s",
				p.target, p.addr, reached, p.wantReach, p.why, strings.TrimSpace(output))
		} else {
			t.Logf("probe on A's network -> %s (%s): reached=%v, as required", p.target, p.addr, reached)
		}
	}

	// 6. The host plane never reached the shared tool namespace. The snapshot
	// must be non-empty first, or the assertions below would pass vacuously.
	snapshot := h.arbiter.Snapshot()
	if _, ok := snapshot["dood-a.conformance.echo"]; !ok {
		t.Fatalf("tool namespace %v has no dood-a.conformance.echo; the host-plane check would be vacuous", snapshot)
	}
	if err := hostendpoint.AssertHostPlane(h.arbiter); err != nil {
		t.Errorf("AssertHostPlane: %v", err)
	}
	for name := range snapshot {
		if strings.HasPrefix(name, "host/") || strings.Contains(name, ".host/") {
			t.Errorf("tool namespace holds host-plane name %q", name)
		}
	}
}
