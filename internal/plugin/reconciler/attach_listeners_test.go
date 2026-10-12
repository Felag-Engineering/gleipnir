package reconciler

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/plugin/container"
	"github.com/felag-engineering/gleipnir/internal/plugin/egress"
)

func mustAllowlist(t *testing.T, domains ...string) egress.Allowlist {
	t.Helper()
	list, err := egress.NewAllowlist(domains)
	if err != nil {
		t.Fatalf("NewAllowlist: %v", err)
	}
	return list
}

func kinds(actions []AttachAction) []AttachActionKind {
	out := make([]AttachActionKind, 0, len(actions))
	for _, a := range actions {
		out = append(out, a.Kind)
	}
	return out
}

func TestPlanListeners(t *testing.T) {
	ip := net.ParseIP("10.83.4.2")
	otherIP := net.ParseIP("10.83.9.2")
	list := mustAllowlist(t, "api.example.com")
	wider := mustAllowlist(t, "api.example.com", "*.cdn.example.com")

	want := func(overrides func(*attachListener)) attachListener {
		l := attachListener{
			InstanceID: "a", Want: true,
			ManageHost: true, ManageEgress: true, ManageGateway: true,
			GleipnirIP: ip, HostAddr: "10.83.4.2:8765", EgressAddr: "10.83.4.2:8118",
			Allowlist: list,
		}
		if overrides != nil {
			overrides(&l)
		}
		return l
	}

	tests := []struct {
		name string
		in   attachListener
		want []AttachActionKind
	}{
		{
			name: "live network with nothing bound adds all three, gateway last",
			in:   want(nil),
			want: []AttachActionKind{AttachHostListen, AttachEgressListen, AttachGatewaySet},
		},
		{
			name: "converged is a no-op (idempotent re-assert)",
			in: want(func(l *attachListener) {
				l.HostBound, l.EgressBound = l.HostAddr, l.EgressAddr
				l.GatewayBound, l.GatewayList = ip, list
			}),
			want: nil,
		},
		{
			name: "changed consent re-sets the gateway entry only",
			in: want(func(l *attachListener) {
				l.HostBound, l.EgressBound = l.HostAddr, l.EgressAddr
				l.GatewayBound, l.GatewayList = ip, wider
			}),
			want: []AttachActionKind{AttachGatewaySet},
		},
		{
			name: "a lost listener is re-added",
			in: want(func(l *attachListener) {
				l.HostBound = l.HostAddr
				l.GatewayBound, l.GatewayList = ip, list
			}),
			want: []AttachActionKind{AttachEgressListen},
		},
		{
			name: "bound to a different address is rebound, stale gateway removed first",
			in: want(func(l *attachListener) {
				l.HostBound, l.EgressBound = "10.83.9.2:8765", "10.83.9.2:8118"
				l.GatewayBound, l.GatewayList = otherIP, list
			}),
			want: []AttachActionKind{
				AttachGatewayRemove,
				AttachHostUnlisten, AttachHostListen,
				AttachEgressUnlisten, AttachEgressListen,
				AttachGatewaySet,
			},
		},
		{
			name: "gone network reverses all three, gateway first",
			in: attachListener{
				InstanceID: "a", ManageHost: true, ManageEgress: true, ManageGateway: true,
				HostBound: "10.83.4.2:8765", EgressBound: "10.83.4.2:8118", GatewayBound: ip,
			},
			want: []AttachActionKind{AttachGatewayRemove, AttachEgressUnlisten, AttachHostUnlisten},
		},
		{
			name: "gone network with nothing bound is a no-op",
			in:   attachListener{InstanceID: "a", ManageHost: true, ManageEgress: true, ManageGateway: true},
			want: nil,
		},
		{
			name: "held (self-attach pending) leaves everything alone",
			in:   want(func(l *attachListener) { l.Want = false; l.Hold = "self-attach pending" }),
			want: []AttachActionKind{AttachNone},
		},
		{
			name: "unconfigured pieces are not driven",
			in: want(func(l *attachListener) {
				l.ManageEgress, l.ManageGateway = false, false
			}),
			want: []AttachActionKind{AttachHostListen},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := kinds(planListeners(tt.in))
			if fmt.Sprint(got) != fmt.Sprint(tt.want) && !(len(got) == 0 && len(tt.want) == 0) {
				t.Errorf("planListeners = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPlanAttach_ListenerActionsFollowTheRowAction(t *testing.T) {
	listeners := []attachListener{{
		InstanceID: "a", Want: true, ManageHost: true,
		GleipnirIP: net.ParseIP("10.83.4.2"), HostAddr: "10.83.4.2:8765",
	}}
	got := planAttach(
		[]attachInstance{{InstanceID: "a", InstanceName: "i-a", URL: "http://10.83.4.3:8080/"}},
		nil, listeners)
	if fmt.Sprint(kinds(got)) != fmt.Sprint([]AttachActionKind{AttachRegister, AttachHostListen}) {
		t.Errorf("planAttach = %v, want register then host_listen for the same instance", kinds(got))
	}
}

func TestAllowlistFromSnapshot(t *testing.T) {
	const v1 = `schema_version: v1
name: old
version: "1.0.0"
`
	tests := []struct {
		name      string
		snapshot  string
		allows    map[string]bool
		wantError string
	}{
		{
			name:     "consented grants build the allowlist",
			snapshot: attachManifest + "  egress:\n    - domain: api.example.com\n    - domain: \"*.cdn.example.com\"\n",
			allows:   map[string]bool{"api.example.com": true, "x.cdn.example.com": true, "evil.com": false, "cdn.example.com": false},
		},
		{
			name:     "egress: [] grants nothing",
			snapshot: attachManifest,
			allows:   map[string]bool{"api.example.com": false},
		},
		{name: "v1 snapshot is an error", snapshot: v1, wantError: "version 1"},
		{name: "unparseable snapshot is an error", snapshot: "{", wantError: "reading consented manifest snapshot"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			list, err := allowlistFromSnapshot([]byte(tt.snapshot))
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("err = %v, want it to contain %q", err, tt.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("allowlistFromSnapshot: %v", err)
			}
			for host, want := range tt.allows {
				if got := list.Allows(host); got != want {
					t.Errorf("Allows(%q) = %v, want %v", host, got, want)
				}
			}
		})
	}
}

// fakeListenerSet mirrors hostendpoint.ListenerSet's contract (including the
// wildcard refusal) without binding sockets; the real set is exercised against
// this reconciler in internal/plugin/hostendpoint.
type fakeListenerSet struct {
	mu    sync.Mutex
	addrs map[string]string
	adds  []string
}

func newFakeListenerSet() *fakeListenerSet { return &fakeListenerSet{addrs: map[string]string{}} }

func (f *fakeListenerSet) Add(instanceID, addr string) (string, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		return "", errors.New("wildcard bind refused")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.addrs[instanceID]; ok {
		return "", errors.New("already has a listener")
	}
	f.addrs[instanceID] = addr
	f.adds = append(f.adds, addr)
	return addr, nil
}

func (f *fakeListenerSet) Addr(instanceID string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.addrs[instanceID]
}

func (f *fakeListenerSet) Instances() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := make([]string, 0, len(f.addrs))
	for id := range f.addrs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (f *fakeListenerSet) Remove(_ context.Context, instanceID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.addrs, instanceID)
	return nil
}

type fakeSlots struct {
	mu         sync.Mutex
	registered map[string]bool
	drains     []string
	drainErr   error
}

func (f *fakeSlots) RegisterInstance(id string) uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.registered == nil {
		f.registered = map[string]bool{}
	}
	f.registered[id] = true
	return 0
}

func (f *fakeSlots) UnregisterInstance(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.registered, id)
}

func (f *fakeSlots) BeginDrain(_ context.Context, id string, _ time.Duration) (uint64, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.drains = append(f.drains, id)
	return 1, true, f.drainErr
}

type listenerFixture struct {
	*attachFixture
	selfID  container.ContainerID
	subnets *SubnetAllocator
	host    *fakeListenerSet
	egress  *fakeListenerSet
	gateway *egress.GatewayRegistry
	slots   *fakeSlots
}

const egressManifest = attachManifest + "  egress:\n    - domain: api.example.com\n"

func newListenerFixture(t *testing.T) *listenerFixture {
	t.Helper()
	ctx := context.Background()

	store, err := db.Open(filepath.Join(t.TempDir(), "listeners.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	fake := container.NewFake()
	selfID, err := fake.Create(ctx, container.CreateOptions{
		Name: "gleipnir", Image: "gleipnir/gleipnir:test", Network: "gleipnir-self-net",
		CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"}, Sysctls: container.RequiredSysctls(),
	})
	if err != nil {
		t.Fatalf("creating self container: %v", err)
	}
	rt := &countingRuntime{Runtime: fake}

	registrar, err := mcp.NewManagedRegistrar(store.Queries(),
		func() string { return "srv-1" },
		func() time.Time { return time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatalf("NewManagedRegistrar: %v", err)
	}

	f := &listenerFixture{
		selfID:  selfID,
		subnets: testAllocator(t),
		host:    newFakeListenerSet(),
		egress:  newFakeListenerSet(),
		gateway: egress.NewGatewayRegistry(),
		slots:   &fakeSlots{},
	}
	r, err := New(Config{
		Runtime:                 rt,
		Store:                   store.Queries(),
		Rotations:               store.Queries(),
		Subnets:                 f.subnets,
		Posture:                 container.PostureRootlessPodman,
		Interval:                time.Hour,
		SelfContainerID:         selfID,
		OperatorAPIGuard:        guardedOperatorAPI(t),
		CheckForwardingDisabled: func() error { return nil },
		Attach:                  &AttachDeps{Store: store.Queries(), Endpoints: registrar},
		HostListeners:           f.host,
		EgressListeners:         f.egress,
		Gateways:                f.gateway,
		Generations:             f.slots,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	f.attachFixture = &attachFixture{store: store, fake: fake, rt: rt, r: r}

	seedFreshInstance(t, store, "inst1", "sha256:aaa", "cfg-1")
	f.setSnapshot(t, egressManifest)
	return f
}

func (f *listenerFixture) setSnapshot(t *testing.T, snapshot string) {
	t.Helper()
	if _, err := f.store.DB().Exec(`UPDATE plugins SET manifest_snapshot = ?`, snapshot); err != nil {
		t.Fatalf("set manifest snapshot: %v", err)
	}
}

// passes runs the core loop n times: network create, then self-attach.
func (f *listenerFixture) passes(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := f.r.ReconcileOnce(context.Background()); err != nil {
			t.Fatalf("ReconcileOnce %d: %v", i+1, err)
		}
	}
}

func (f *listenerFixture) gleipnirAddr(t *testing.T) net.IP {
	t.Helper()
	prefix, ok, err := f.subnets.Lookup(context.Background(), "inst1")
	if err != nil || !ok {
		t.Fatalf("Lookup: ok=%v err=%v", ok, err)
	}
	addr, err := egress.GleipnirAddrOf(prefix.String())
	if err != nil {
		t.Fatalf("GleipnirAddrOf: %v", err)
	}
	return addr
}

// Fake runtime, full lifecycle: nothing before Gleipnir has joined the network,
// then all three once it has, and all three gone when the instance is.
func TestAttachPass_ListenersConvergeAndTearDown(t *testing.T) {
	ctx := context.Background()
	f := newListenerFixture(t)

	// Pass 1 creates the network; Gleipnir has not joined it, so the address
	// does not exist yet. Holding is the correct, quiet answer.
	f.passes(t, 1)
	if _, err := f.r.AttachPass(ctx); err != nil {
		t.Fatalf("AttachPass before self-attach: %v", err)
	}
	if got := f.host.Instances(); len(got) != 0 {
		t.Fatalf("host listeners before self-attach = %v, want none", got)
	}
	if f.gateway.Len() != 0 {
		t.Fatalf("gateway entries before self-attach = %d, want 0", f.gateway.Len())
	}

	f.passes(t, 1) // self-attach
	if _, err := f.r.AttachPass(ctx); err != nil {
		t.Fatalf("AttachPass: %v", err)
	}

	gleipnir := f.gleipnirAddr(t)
	if got, want := f.host.Addr("inst1"), net.JoinHostPort(gleipnir.String(), "8765"); got != want {
		t.Errorf("host listener = %q, want %q", got, want)
	}
	if got, want := f.egress.Addr("inst1"), net.JoinHostPort(gleipnir.String(), "8118"); got != want {
		t.Errorf("egress listener = %q, want %q", got, want)
	}
	id, list, ok := f.gateway.InstanceForGateway(gleipnir)
	if !ok || id != "inst1" {
		t.Fatalf("gateway registry at %s = (%q, %v), want inst1", gleipnir, id, ok)
	}
	if !list.Allows("api.example.com") || list.Allows("evil.com") {
		t.Errorf("registered allowlist does not reflect the consented snapshot")
	}
	if !f.slots.registered["inst1"] {
		t.Error("instance was not registered with the generation controller")
	}

	// Idempotent: a second pass changes nothing.
	again, err := f.r.AttachPass(ctx)
	if err != nil {
		t.Fatalf("second AttachPass: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("second pass applied %v, want none", kinds(again))
	}

	// Uninstall: the desired row goes away.
	if _, err := f.store.DB().Exec(`DELETE FROM plugin_containers`); err != nil {
		t.Fatalf("delete desired row: %v", err)
	}
	if _, err := f.r.AttachPass(ctx); err != nil {
		t.Fatalf("AttachPass after uninstall: %v", err)
	}
	if len(f.host.Instances()) != 0 || len(f.egress.Instances()) != 0 || f.gateway.Len() != 0 {
		t.Errorf("after teardown: host=%v egress=%v gateway=%d, want all gone",
			f.host.Instances(), f.egress.Instances(), f.gateway.Len())
	}
	if f.slots.registered["inst1"] {
		t.Error("instance is still registered with the generation controller")
	}
}

// The network disappearing under a still-desired instance reverses everything
// too: the address Gleipnir bound is gone with it.
func TestAttachPass_ListenersRemovedWhenNetworkIsGone(t *testing.T) {
	ctx := context.Background()
	f := newListenerFixture(t)
	f.passes(t, 2)
	if _, err := f.r.AttachPass(ctx); err != nil {
		t.Fatalf("AttachPass: %v", err)
	}
	if f.gateway.Len() != 1 {
		t.Fatalf("precondition: gateway entries = %d, want 1", f.gateway.Len())
	}

	nets, err := f.fake.ListNetworksByLabel(ctx, LabelManaged, ManagedValue)
	if err != nil || len(nets) != 1 {
		t.Fatalf("networks = %v, err = %v", nets, err)
	}
	if err := f.fake.RemoveNetwork(ctx, nets[0].ID); err != nil {
		t.Fatalf("RemoveNetwork: %v", err)
	}

	if _, err := f.r.AttachPass(ctx); err != nil {
		t.Fatalf("AttachPass: %v", err)
	}
	if len(f.host.Instances()) != 0 || len(f.egress.Instances()) != 0 || f.gateway.Len() != 0 {
		t.Errorf("listeners survived their network: host=%v egress=%v gateway=%d",
			f.host.Instances(), f.egress.Instances(), f.gateway.Len())
	}
}

// A changed consented snapshot is picked up on the next pass without touching
// the listeners.
func TestAttachPass_AllowlistFollowsTheConsentedSnapshot(t *testing.T) {
	ctx := context.Background()
	f := newListenerFixture(t)
	f.passes(t, 2)
	if _, err := f.r.AttachPass(ctx); err != nil {
		t.Fatalf("AttachPass: %v", err)
	}

	f.setSnapshot(t, attachManifest+"  egress:\n    - domain: other.example.com\n")
	applied, err := f.r.AttachPass(ctx)
	if err != nil {
		t.Fatalf("AttachPass: %v", err)
	}
	if fmt.Sprint(kinds(applied)) != fmt.Sprint([]AttachActionKind{AttachGatewaySet}) {
		t.Errorf("applied = %v, want only a gateway_set", kinds(applied))
	}
	_, list, _ := f.gateway.InstanceForGateway(f.gleipnirAddr(t))
	if list.Allows("api.example.com") || !list.Allows("other.example.com") {
		t.Error("allowlist did not follow the snapshot")
	}
}

// A v1 snapshot has no egress concept: the pass reports an error and fails
// closed -- no listener, no registry entry -- rather than reading it as
// "no grants" and serving an instance with an empty allowlist.
func TestAttachPass_V1SnapshotIsAnErrorAndBindsNothing(t *testing.T) {
	ctx := context.Background()
	f := newListenerFixture(t)
	f.setSnapshot(t, "schema_version: v1\nname: old\nversion: \"1.0.0\"\n")
	f.passes(t, 2)

	_, err := f.r.AttachPass(ctx)
	if err == nil || !strings.Contains(err.Error(), "version 1") {
		t.Fatalf("AttachPass error = %v, want a v1 snapshot error", err)
	}
	if len(f.host.Instances()) != 0 || len(f.egress.Instances()) != 0 || f.gateway.Len() != 0 {
		t.Errorf("a v1 snapshot bound listeners: host=%v egress=%v gateway=%d",
			f.host.Instances(), f.egress.Instances(), f.gateway.Len())
	}
}

// A bind address is never a wildcard and never the gateway, whatever else is
// true: everything the pass hands a listener set is gleipnir's reserved address.
func TestAttachPass_NeverBindsWildcardOrGateway(t *testing.T) {
	ctx := context.Background()
	f := newListenerFixture(t)
	f.passes(t, 2)
	if _, err := f.r.AttachPass(ctx); err != nil {
		t.Fatalf("AttachPass: %v", err)
	}
	prefix, _, _ := f.subnets.Lookup(ctx, "inst1")
	gateway := subnetGatewayAddr(prefix).String()
	for _, addr := range append(f.host.adds, f.egress.adds...) {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			t.Fatalf("bind address %q: %v", addr, err)
		}
		if ip := net.ParseIP(host); ip == nil || ip.IsUnspecified() {
			t.Errorf("bind address %q is a wildcard", addr)
		}
		if host == gateway {
			t.Errorf("bind address %q is the network gateway", addr)
		}
	}
}

func TestAttachPass_ManualPostureHasNoListeners(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "manual.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	host := newFakeListenerSet()
	r, err := New(Config{
		Runtime:       container.NewFake(),
		Store:         store.Queries(),
		Posture:       container.PostureManual,
		Attach:        &AttachDeps{Store: store.Queries(), Endpoints: &mcp.ManagedRegistrar{}},
		HostListeners: host,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if r.listenersEnabled() {
		t.Error("listeners enabled in manual posture")
	}
	if _, err := r.AttachPass(context.Background()); err != nil {
		t.Fatalf("AttachPass: %v", err)
	}
	if len(host.adds) != 0 {
		t.Errorf("manual posture bound %v", host.adds)
	}
}

func TestNew_ListenersRequireAttachAndSubnets(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	base := Config{Runtime: container.NewFake(), Store: store.Queries(), Posture: container.PostureRootlessPodman, Rotations: store.Queries()}

	noAttach := base
	noAttach.Subnets = testAllocator(t)
	noAttach.HostListeners = newFakeListenerSet()
	if _, err := New(noAttach); err == nil {
		t.Error("New() accepted listeners without Attach")
	}

	noSubnets := base
	noSubnets.Attach = &AttachDeps{Store: store.Queries(), Endpoints: &mcp.ManagedRegistrar{}}
	noSubnets.Gateways = egress.NewGatewayRegistry()
	if _, err := New(noSubnets); err == nil {
		t.Error("New() accepted listeners without Subnets")
	}
}

// Rotation's drain step waits out in-flight host-endpoint calls of the
// superseded generation before stopping its container.
func TestDrainGeneration_CallsBeginDrainBeforeStopping(t *testing.T) {
	ctx := context.Background()
	slots := &fakeSlots{}
	fake := container.NewFake()
	rt := &countingRuntime{Runtime: fake}
	r, err := New(Config{Runtime: rt, Store: &fakeStore{}, Generations: slots})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	id, err := rt.Create(ctx, container.CreateOptions{
		Name: "c", Image: "img@sha256:a", Network: "n",
		CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"}, Sysctls: container.RequiredSysctls(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := rt.Start(ctx, id); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// An unparseable UpdatedAt reads as an exhausted window.
	gens := []db.PluginContainerGeneration{{ID: "g1", PluginInstanceID: "inst1", Generation: 1, Status: GenDraining}}
	act := RotationAction{InstanceID: "inst1", GenerationID: "g1", Generation: 1, ContainerID: id}

	slots.drainErr = errors.New("drain already in progress")
	if err := r.drainGeneration(ctx, act, gens, ""); err == nil {
		t.Fatal("drainGeneration succeeded despite BeginDrain failing")
	}
	if rt.stops != 0 {
		t.Errorf("container stopped (%d) although host calls were still draining", rt.stops)
	}

	slots.drainErr = nil
	if err := r.drainGeneration(ctx, act, gens, ""); err != nil {
		t.Fatalf("drainGeneration: %v", err)
	}
	if rt.stops != 1 {
		t.Errorf("stops = %d, want 1", rt.stops)
	}
	if len(slots.drains) != 2 || slots.drains[1] != "inst1" {
		t.Errorf("BeginDrain calls = %v, want one per attempt for inst1", slots.drains)
	}
}
