package hostendpoint

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/infra/netguard"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/container"
	"github.com/felag-engineering/gleipnir/internal/plugin/egress"
	"github.com/felag-engineering/gleipnir/internal/plugin/generation"
	"github.com/felag-engineering/gleipnir/internal/plugin/reconciler"
)

type staticResolver struct{ instanceID string }

func (s staticResolver) ResolveToken(_ context.Context, token string) (Identity, bool, error) {
	if token != "good" {
		return Identity{}, false, nil
	}
	return Identity{InstanceID: s.instanceID}, true, nil
}

// The real ListenerSet, real GatewayRegistry and real egress proxy, driven by
// the real reconciler over the Fake runtime. The subnet pool is inside
// 127.0.0.0/8 so that gleipnir's reserved address (.2 of the instance /24) is
// bindable on the test host.
func TestAttachPass_RealListenersBindGleipnirsAddress(t *testing.T) {
	ctx := context.Background()

	store, err := db.Open(filepath.Join(t.TempDir(), "listeners.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	q := store.Queries()

	const instanceID = "inst-1"
	now := time.Now().UTC().Format(time.RFC3339Nano)
	pluginID := model.NewULID()
	if _, err := q.CreatePlugin(ctx, db.CreatePluginParams{
		ID: pluginID, Name: "p", PluginVersion: "1.0.0",
		ManifestSnapshot: attachTestManifest + "  egress:\n    - domain: api.example.com\n",
		TrustedPubkey:    "k", Status: "active", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreatePlugin: %v", err)
	}
	if _, err := q.CreatePluginInstance(ctx, db.CreatePluginInstanceParams{
		ID: instanceID, PluginID: pluginID, InstanceName: "acme",
		ConfigJson: "{}", SubscriptionScopeJson: "{}", HandshakeVersions: "{}",
		HealthState: "unhealthy", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreatePluginInstance: %v", err)
	}
	if _, err := q.CreatePluginContainer(ctx, db.CreatePluginContainerParams{
		ID: model.NewULID(), PluginInstanceID: instanceID,
		ImageRef: "ghcr.io/acme/p:1", ImageDigest: "sha256:aaa", ConfigHash: "cfg",
		NetworkName: "gleipnir-plugin-" + instanceID, DesiredState: reconciler.DesiredRunning,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreatePluginContainer: %v", err)
	}

	pool := netip.MustParsePrefix("127.60.0.0/16")
	subnets, err := reconciler.NewSubnetAllocator(q, pool, func() string { return now })
	if err != nil {
		t.Fatalf("NewSubnetAllocator: %v", err)
	}

	fake := container.NewFake()
	selfID, err := fake.Create(ctx, container.CreateOptions{
		Name: "gleipnir", Image: "gleipnir/gleipnir:test", Network: "gleipnir-self-net",
		CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"}, Sysctls: container.RequiredSysctls(),
	})
	if err != nil {
		t.Fatalf("self container: %v", err)
	}
	guardLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = guardLn.Close() })
	guarded, err := netguard.Wrap(guardLn, pool, nil)
	if err != nil {
		t.Fatalf("netguard.Wrap: %v", err)
	}

	registry := egress.NewGatewayRegistry()
	proxy, err := egress.New(egress.Config{Resolver: registry})
	if err != nil {
		t.Fatalf("egress.New: %v", err)
	}
	ctl := generation.New()
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	hostListeners := NewListenerSet(Chain(ok, staticResolver{instanceID}, ctl))
	egressListeners := NewListenerSet(proxy)
	t.Cleanup(func() {
		_ = hostListeners.Close(ctx)
		_ = egressListeners.Close(ctx)
	})

	registrar, err := mcp.NewManagedRegistrar(q, model.NewULID, time.Now)
	if err != nil {
		t.Fatalf("NewManagedRegistrar: %v", err)
	}
	r, err := reconciler.New(reconciler.Config{
		Runtime:                 fake,
		Store:                   q,
		Rotations:               q,
		Subnets:                 subnets,
		Posture:                 container.PostureRootlessPodman,
		Interval:                time.Hour,
		SelfContainerID:         selfID,
		OperatorAPIGuard:        guarded.Guard(),
		CheckForwardingDisabled: func() error { return nil },
		Attach:                  &reconciler.AttachDeps{Store: q, Endpoints: registrar},
		HostListeners:           hostListeners,
		EgressListeners:         egressListeners,
		Gateways:                registry,
		Generations:             ctl,
	})
	if err != nil {
		t.Fatalf("reconciler.New: %v", err)
	}

	for i := 0; i < 2; i++ { // create network, self-attach
		if _, err := r.ReconcileOnce(ctx); err != nil {
			t.Fatalf("ReconcileOnce %d: %v", i+1, err)
		}
	}
	if _, err := r.AttachPass(ctx); err != nil {
		t.Fatalf("AttachPass: %v", err)
	}

	subnet, found, err := subnets.Lookup(ctx, instanceID)
	if err != nil || !found {
		t.Fatalf("Lookup: found=%v err=%v", found, err)
	}
	gleipnir, err := egress.GleipnirAddrOf(subnet.String())
	if err != nil {
		t.Fatalf("GleipnirAddrOf: %v", err)
	}
	if got, want := hostListeners.Addr(instanceID), net.JoinHostPort(gleipnir.String(), "8765"); got != want {
		t.Fatalf("host listener bound %q, want %q", got, want)
	}
	if got, want := egressListeners.Addr(instanceID), net.JoinHostPort(gleipnir.String(), "8118"); got != want {
		t.Fatalf("egress listener bound %q, want %q", got, want)
	}

	// The host endpoint chain is intact: no token is refused before the
	// handler, and a good token is admitted only because the reconciler
	// registered the instance with the generation controller.
	get := func(token string) int {
		req, _ := http.NewRequest(http.MethodGet, "http://"+hostListeners.Addr(instanceID)+"/", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
		if err != nil {
			t.Fatalf("GET host endpoint: %v", err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := get(""); got != http.StatusUnauthorized {
		t.Errorf("tokenless request = %d, want 401", got)
	}
	if got := get("good"); got != http.StatusOK {
		t.Errorf("authenticated request = %d, want 200 (instance registered, slot acquired)", got)
	}

	// The proxy resolves the caller from the address the connection arrived on:
	// a caller on the instance's /24 is identified and judged against ITS
	// allowlist (not_granted), not refused as an unknown instance.
	base := subnet.Addr().As4()
	callerIP := net.IPv4(base[0], base[1], base[2], 9)
	dialer := &net.Dialer{LocalAddr: &net.TCPAddr{IP: callerIP}}
	conn, err := dialer.Dial("tcp", egressListeners.Addr(instanceID))
	if err != nil {
		t.Fatalf("dial egress proxy from %s: %v", callerIP, err)
	}
	if _, err := conn.Write([]byte("CONNECT evil.example.org:443 HTTP/1.1\r\nHost: evil.example.org:443\r\n\r\n")); err != nil {
		t.Fatalf("write CONNECT: %v", err)
	}
	buf := make([]byte, 64)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, _ := conn.Read(buf)
	_ = conn.Close()
	if !strings.Contains(string(buf[:n]), "403") {
		t.Fatalf("proxy response = %q, want a 403 refusal", buf[:n])
	}
	_, denied := proxy.Counters()
	if denied[egress.DenyNotGranted] != 1 || denied[egress.DenyUnknownInstance] != 0 {
		t.Errorf("denials = %v, want exactly one not_granted and no unknown_instance", denied)
	}

	// Teardown on uninstall closes both sockets and the registry entry.
	if _, err := store.DB().Exec(`DELETE FROM plugin_containers`); err != nil {
		t.Fatalf("delete desired row: %v", err)
	}
	if _, err := r.AttachPass(ctx); err != nil {
		t.Fatalf("AttachPass after uninstall: %v", err)
	}
	if len(hostListeners.Instances()) != 0 || len(egressListeners.Instances()) != 0 || registry.Len() != 0 {
		t.Errorf("teardown left host=%v egress=%v registry=%d",
			hostListeners.Instances(), egressListeners.Instances(), registry.Len())
	}
	if c, err := net.DialTimeout("tcp", net.JoinHostPort(gleipnir.String(), "8765"), time.Second); err == nil {
		c.Close()
		t.Error("host endpoint still accepting connections after teardown")
	}
}

func TestListenerSet_Instances(t *testing.T) {
	ls := NewListenerSet(http.NotFoundHandler())
	t.Cleanup(func() { _ = ls.Close(context.Background()) })
	if got := ls.Instances(); len(got) != 0 {
		t.Fatalf("Instances on an empty set = %v", got)
	}
	for _, id := range []string{"b", "a"} {
		if _, err := ls.Add(id, "127.0.0.1:0"); err != nil {
			t.Fatalf("Add(%s): %v", id, err)
		}
	}
	if got := strings.Join(ls.Instances(), ","); got != "a,b" {
		t.Errorf("Instances = %q, want a,b", got)
	}
	if err := ls.Remove(context.Background(), "a"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if got := strings.Join(ls.Instances(), ","); got != "b" {
		t.Errorf("Instances after Remove = %q, want b", got)
	}
}

// Both listener sets refuse a wildcard bind, so a mis-computed address can
// never expose the host endpoint or the proxy on every interface.
func TestListenerSets_RefuseWildcardBinds(t *testing.T) {
	ok := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	for name, set := range map[string]*ListenerSet{
		"host endpoint": NewListenerSet(ok),
		"egress proxy":  NewListenerSet(ok),
	} {
		for _, addr := range []string{":8765", "0.0.0.0:8765", "[::]:8765"} {
			if _, err := set.Add("inst", addr); !errors.Is(err, ErrWildcardAddr) {
				t.Errorf("%s: Add(%q) = %v, want ErrWildcardAddr", name, addr, err)
			}
		}
		if got := set.Instances(); len(got) != 0 {
			t.Errorf("%s: Instances after refused binds = %v", name, got)
		}
	}
}
