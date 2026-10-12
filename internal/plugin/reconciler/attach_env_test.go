package reconciler

import (
	"context"
	"net"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/plugin/container"
	"github.com/felag-engineering/gleipnir/internal/plugin/egress"
)

// The security precondition from the #958 x #955 review: the URL a generation
// is told to call must name Gleipnir's reserved address, never the gateway.
// Under rootful Docker the gateway is the host, and NO_PROXY exempts the host
// endpoint from the egress proxy, so a URL there would send every generation's
// bearer token to whatever listens on the host.
func TestHostEndpointEnv_URLHostIsGleipnirsAddressNotTheGateway(t *testing.T) {
	ctx := context.Background()
	subnets := testAllocator(t)
	subnet, err := subnets.Allocate(ctx, "inst-1")
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	want, err := egress.GleipnirAddrOf(subnet.String())
	if err != nil {
		t.Fatalf("GleipnirAddrOf: %v", err)
	}

	env := HostEndpointEnv(subnets, DefaultHostEndpointPort)(ctx, "inst-1")
	if len(env) != 1 {
		t.Fatalf("env = %v, want exactly one entry", env)
	}
	u, ok := parseHostEndpointURL(env)
	if !ok {
		t.Fatalf("env = %v has no parseable %s", env, hostEndpointURLEnvVar)
	}
	if u.Hostname() != want.String() {
		t.Errorf("URL host = %s, want gleipnir's reserved address %s", u.Hostname(), want)
	}
	if gateway := subnetGatewayAddr(subnet).String(); u.Hostname() == gateway {
		t.Errorf("URL host = %s is the network gateway", gateway)
	}
	if u.Port() != "8765" {
		t.Errorf("URL port = %s, want 8765", u.Port())
	}
}

func TestEgressEnv_ProxyIsGleipnirsAddress(t *testing.T) {
	ctx := context.Background()
	subnets := testAllocator(t)
	subnet, err := subnets.Allocate(ctx, "inst-1")
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	want, _ := egress.GleipnirAddrOf(subnet.String())

	for _, kv := range EgressEnv(subnets, DefaultEgressProxyPort)(ctx, "inst-1") {
		key, value, _ := strings.Cut(kv, "=")
		if strings.EqualFold(key, "no_proxy") {
			if value != "" {
				t.Errorf("%s = %q, want empty", key, value)
			}
			continue
		}
		u, err := url.Parse(value)
		if err != nil {
			t.Fatalf("%s = %q: %v", key, value, err)
		}
		if u.Hostname() != want.String() || u.Port() != "8118" {
			t.Errorf("%s = %q, want http://%s", key, value, net.JoinHostPort(want.String(), "8118"))
		}
	}
}

func TestEnvHooks_NoSubnetMeansNoEnv(t *testing.T) {
	ctx := context.Background()
	subnets := testAllocator(t) // nothing allocated
	if env := HostEndpointEnv(subnets, 8765)(ctx, "ghost"); env != nil {
		t.Errorf("HostEndpointEnv = %v, want nil", env)
	}
	if env := EgressEnv(subnets, 8118)(ctx, "ghost"); env != nil {
		t.Errorf("EgressEnv = %v, want nil", env)
	}
}

// Whatever proxy configuration an image carries is stripped BEFORE either env
// set is appended; neither the production egress hook nor the host endpoint
// hook can be undercut by an inherited NO_PROXY.
func TestWithGenerationEnv_StripsInheritedProxyEnvBeforeBothHooks(t *testing.T) {
	ctx := context.Background()
	subnets := testAllocator(t)
	subnet, err := subnets.Allocate(ctx, "inst-1")
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	gleipnir, _ := egress.GleipnirAddrOf(subnet.String())
	hostport := net.JoinHostPort(gleipnir.String(), "8765")

	r, err := New(Config{
		Runtime:         container.NewFake(),
		Store:           &fakeStore{},
		Subnets:         subnets,
		EgressEnv:       EgressEnv(subnets, DefaultEgressProxyPort),
		HostEndpointEnv: HostEndpointEnv(subnets, DefaultHostEndpointPort),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	got, err := r.withGenerationEnv(ctx, container.CreateOptions{
		Env: []string{"PATH=/usr/bin", "NO_PROXY=*", "http_proxy=http://attacker:3128", "ALL_PROXY=socks5://attacker:1080"},
	}, "inst-1")
	if err != nil {
		t.Fatalf("withGenerationEnv: %v", err)
	}

	if got.Env[0] != "PATH=/usr/bin" {
		t.Fatalf("env[0] = %q, want the surviving inherited entry first (strip happens before append)", got.Env[0])
	}
	seen := map[string]string{}
	for _, kv := range got.Env[1:] {
		key, value, _ := strings.Cut(kv, "=")
		if _, dup := seen[key]; dup {
			t.Errorf("duplicate env key %q in %v", key, got.Env)
		}
		seen[key] = value
		if strings.Contains(value, "attacker") || key == "ALL_PROXY" {
			t.Errorf("inherited proxy entry %q survived", kv)
		}
	}
	if seen["NO_PROXY"] != hostport || seen["no_proxy"] != hostport {
		t.Errorf("NO_PROXY = %q / %q, want exactly the host endpoint %s", seen["NO_PROXY"], seen["no_proxy"], hostport)
	}
	if seen[hostEndpointURLEnvVar] != "http://"+hostport {
		t.Errorf("%s = %q, want http://%s", hostEndpointURLEnvVar, seen[hostEndpointURLEnvVar], hostport)
	}
	if !strings.HasPrefix(seen["HTTPS_PROXY"], "http://"+gleipnir.String()+":") {
		t.Errorf("HTTPS_PROXY = %q, want gleipnir's address", seen["HTTPS_PROXY"])
	}
}

// End to end through the one generation create path: the container a rotation
// creates is told gleipnir's reserved address for BOTH the proxy and the host
// endpoint.
func TestRotationCreate_UsesGleipnirsAddressForBothEnvSets(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(filepath.Join(t.TempDir(), "env.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	subnets := testAllocator(t)
	subnet, err := subnets.Allocate(ctx, "inst-1")
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	gleipnir, _ := egress.GleipnirAddrOf(subnet.String())

	rt := &countingRuntime{Runtime: container.NewFake()}
	r, err := New(Config{
		Runtime:         rt,
		Store:           store.Queries(),
		Rotations:       store.Queries(),
		Subnets:         subnets,
		Posture:         container.PostureRootlessPodman,
		Interval:        time.Hour,
		EgressEnv:       EgressEnv(subnets, DefaultEgressProxyPort),
		HostEndpointEnv: HostEndpointEnv(subnets, DefaultHostEndpointPort),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	seedInstance(t, store, rt, "inst-1", "sha256:aaa", "cfg-1")
	setDesiredDigest(t, store, "inst-1", "sha256:bbb")
	for i := 0; i < 2; i++ {
		if _, err := r.ReconcileRotations(ctx); err != nil {
			t.Fatalf("rotation pass %d: %v", i+1, err)
		}
	}

	env := map[string]string{}
	for _, kv := range rt.createEnv() {
		key, value, _ := strings.Cut(kv, "=")
		env[key] = value
	}
	if got, want := env[hostEndpointURLEnvVar], "http://"+net.JoinHostPort(gleipnir.String(), "8765"); got != want {
		t.Errorf("%s = %q, want %q", hostEndpointURLEnvVar, got, want)
	}
	if got, want := env["HTTPS_PROXY"], "http://"+net.JoinHostPort(gleipnir.String(), "8118"); got != want {
		t.Errorf("HTTPS_PROXY = %q, want %q", got, want)
	}
}
