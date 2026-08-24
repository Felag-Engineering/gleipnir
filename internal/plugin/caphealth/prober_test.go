package caphealth

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/model"
)

// stubTargets returns a fixed target list.
type stubTargets struct {
	mu      sync.Mutex
	targets []Target
	err     error
	calls   int
}

func (s *stubTargets) ProbeTargets(context.Context) ([]Target, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.targets, s.err
}

func (s *stubTargets) set(targets []Target) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.targets = targets
}

// stubContainer answers the healthcheck read.
type stubContainer struct {
	mu      sync.Mutex
	healthy bool
	detail  string
	err     error
}

func (s *stubContainer) ContainerHealthy(context.Context, string) (bool, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.healthy, s.detail, s.err
}

func (s *stubContainer) set(healthy bool, detail string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.healthy, s.detail, s.err = healthy, detail, err
}

// stubDiscover answers the server/discover probe.
type stubDiscover struct {
	mu     sync.Mutex
	result DiscoverResult
	err    error
}

func (s *stubDiscover) Discover(context.Context, string) (DiscoverResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.result, s.err
}

func (s *stubDiscover) set(result DiscoverResult, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.result, s.err = result, err
}

// recordingRollup captures what was written to the instance-level column.
type recordingRollup struct {
	mu      sync.Mutex
	last    map[string]model.PluginHealthState
	details map[string]string
	err     error
}

func newRecordingRollup() *recordingRollup {
	return &recordingRollup{
		last:    make(map[string]model.PluginHealthState),
		details: make(map[string]string),
	}
}

func (r *recordingRollup) SetInstanceHealth(_ context.Context, instanceID string, s model.PluginHealthState, detail string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.last[instanceID] = s
	r.details[instanceID] = detail
	return r.err
}

func (r *recordingRollup) get(instanceID string) (model.PluginHealthState, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last[instanceID], r.details[instanceID]
}

// capturePublisher records published events so a test synchronizes on a
// completed pass rather than polling for its side effects.
type capturePublisher struct {
	mu     sync.Mutex
	events []string
}

func (p *capturePublisher) Publish(eventType string, _ json.RawMessage) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, eventType)
}

func (p *capturePublisher) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, e := range p.events {
		if e == EventProbePassCompleted {
			n++
		}
	}
	return n
}

func (p *capturePublisher) waitForPasses(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for p.count() < n {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d probe passes (saw %d)", n, p.count())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

type proberFixture struct {
	prober    *Prober
	registry  *Registry
	targets   *stubTargets
	container *stubContainer
	discover  *stubDiscover
	rollup    *recordingRollup
	publisher *capturePublisher
}

func newProberFixture(t *testing.T, targets []Target) proberFixture {
	t.Helper()

	f := proberFixture{
		registry:  NewRegistry(),
		targets:   &stubTargets{targets: targets},
		container: &stubContainer{healthy: true},
		discover:  &stubDiscover{},
		rollup:    newRecordingRollup(),
		publisher: &capturePublisher{},
	}
	p, err := New(Config{
		Registry:  f.registry,
		Targets:   f.targets,
		Container: f.container,
		Discover:  f.discover,
		Rollup:    f.rollup,
		Publisher: f.publisher,
		Interval:  time.Hour, // kicks drive the tests; the ticker must not race them
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	f.prober = p
	return f
}

// Both liveness checks passing produces a healthy instance, and the rollup is
// written to the column the chip reads.
func TestProbeOnce_HealthyInstance(t *testing.T) {
	ctx := context.Background()
	f := newProberFixture(t, []Target{{
		InstanceID: "i1", ContainerID: "c1",
		Profiles: []Profile{ProfileToolProvider},
	}})

	result, err := f.prober.ProbeOnce(ctx)
	if err != nil {
		t.Fatalf("ProbeOnce: %v", err)
	}
	if result.Probed != 1 || result.Unhealthy != 0 || result.Errors != 0 {
		t.Errorf("result = %+v, want 1 probed and nothing wrong", result)
	}

	h := f.registry.Get("i1")
	if !h.Liveness.OK() {
		t.Errorf("liveness = %+v, want OK", h.Liveness)
	}
	if h.Liveness.ObservedAt.IsZero() {
		t.Error("the liveness verdict was not timestamped; it could never age out")
	}
	if got, _ := f.rollup.get("i1"); got != model.PluginHealthStateHealthy {
		t.Errorf("rolled-up state = %q, want healthy", got)
	}
}

// The two liveness checks answer different questions and fail independently.
func TestProbeOnce_LivenessFailures(t *testing.T) {
	tests := []struct {
		name          string
		containerOK   bool
		containerErr  error
		discoverErr   error
		wantInDetail  string
		wantContainer bool
		wantDiscover  bool
	}{
		{
			name:         "container healthcheck failing",
			containerOK:  false,
			wantInDetail: "container healthcheck failing",
			wantDiscover: true,
		},
		{
			name:         "container inspect errored",
			containerErr: errors.New("socket closed"),
			wantInDetail: "container healthcheck unavailable",
			wantDiscover: true,
		},
		{
			// A container that is up with a wedged server: routing there would
			// hang rather than fail.
			name:          "server wedged",
			containerOK:   true,
			discoverErr:   errors.New("timeout"),
			wantInDetail:  "server/discover probe failed",
			wantContainer: true,
		},
		{
			// A container that is down explains the discover failure; reporting
			// the downstream symptom would send an operator to the wrong place.
			name:         "both failing reports the container cause",
			containerOK:  false,
			discoverErr:  errors.New("connection refused"),
			wantInDetail: "container healthcheck failing",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newProberFixture(t, []Target{{InstanceID: "i1", ContainerID: "c1"}})
			f.container.set(tc.containerOK, "", tc.containerErr)
			f.discover.set(DiscoverResult{}, tc.discoverErr)

			result, err := f.prober.ProbeOnce(ctx)
			if err != nil {
				t.Fatalf("ProbeOnce: %v", err)
			}
			if result.Unhealthy != 1 {
				t.Errorf("unhealthy = %d, want 1", result.Unhealthy)
			}

			h := f.registry.Get("i1")
			if h.Liveness.ContainerHealthy != tc.wantContainer {
				t.Errorf("ContainerHealthy = %v, want %v", h.Liveness.ContainerHealthy, tc.wantContainer)
			}
			if h.Liveness.DiscoverOK != tc.wantDiscover {
				t.Errorf("DiscoverOK = %v, want %v", h.Liveness.DiscoverOK, tc.wantDiscover)
			}
			if !contains(h.Liveness.Detail, tc.wantInDetail) {
				t.Errorf("detail = %q, want it to mention %q", h.Liveness.Detail, tc.wantInDetail)
			}

			got, _ := f.rollup.get("i1")
			if got != model.PluginHealthStateUnhealthy {
				t.Errorf("rolled-up state = %q, want unhealthy", got)
			}
		})
	}
}

// Manifest-vs-discovery drift is a CAPABILITY fault: the event source goes
// unhealthy and the instance keeps serving its tools.
func TestProbeOnce_EventDriftIsCapabilityScoped(t *testing.T) {
	ctx := context.Background()
	f := newProberFixture(t, []Target{{
		InstanceID:         "i1",
		ContainerID:        "c1",
		AttestedEventKinds: []string{"message", "reaction"},
		Profiles:           []Profile{ProfileToolProvider, ProfileEventSource},
	}})
	f.discover.set(DiscoverResult{ExtensionDeclared: true, EventKinds: []string{"message", "rogue"}}, nil)

	result, err := f.prober.ProbeOnce(ctx)
	if err != nil {
		t.Fatalf("ProbeOnce: %v", err)
	}

	h := f.registry.Get("i1")
	if !h.Liveness.OK() {
		t.Fatal("drift took liveness down; it is a capability fault, not a reachability one")
	}
	if h.Serves(Capability{Profile: ProfileEventSource}) {
		t.Error("the drifted event source is still being routed to")
	}
	if !h.Serves(Capability{Profile: ProfileToolProvider}) {
		t.Error("the instance stopped serving tools because its event kinds drifted")
	}
	if !h.Partial() {
		t.Error("Partial() = false; the instance serves tools and not events")
	}
	if result.Partial != 1 {
		t.Errorf("result.Partial = %d, want 1", result.Partial)
	}

	_, detail := f.rollup.get("i1")
	for _, want := range []string{"reaction", "rogue"} {
		if !contains(detail, want) {
			t.Errorf("rolled-up detail = %q, want it to name %q", detail, want)
		}
	}
}

// The DriftDetail two-direction comparison also produces PER-KIND entries, so
// Serves can answer "is THIS kind healthy" for a subscribed-trigger binding,
// not just "is the whole profile healthy" -- and a per-kind fault must not
// take tool_provider down with it (the one-scope-marks-all-unhealthy defect
// caphealth exists to fix).
func TestProbeOnce_EventDriftIsPerKind(t *testing.T) {
	ctx := context.Background()
	f := newProberFixture(t, []Target{{
		InstanceID:         "i1",
		ContainerID:        "c1",
		AttestedEventKinds: []string{"message", "reaction"},
		Profiles:           []Profile{ProfileToolProvider, ProfileEventSource},
	}})
	f.discover.set(DiscoverResult{ExtensionDeclared: true, EventKinds: []string{"reaction", "rogue"}}, nil)

	if _, err := f.prober.ProbeOnce(ctx); err != nil {
		t.Fatalf("ProbeOnce: %v", err)
	}

	h := f.registry.Get("i1")
	if h.Serves(Capability{Profile: ProfileEventSource, Name: "message"}) {
		t.Error("an attested-but-not-discovered kind is still serving")
	}
	if !h.Serves(Capability{Profile: ProfileEventSource, Name: "reaction"}) {
		t.Error("a kind present on both sides was marked unhealthy")
	}
	if h.Serves(Capability{Profile: ProfileEventSource, Name: "rogue"}) {
		t.Error("a discovered-but-not-attested kind is still serving")
	}
	if !h.Serves(Capability{Profile: ProfileToolProvider}) {
		t.Error("a per-kind event fault took tool_provider down with it")
	}
}

// A manifest attesting the event_source profile while the runtime declares no
// io.gleipnir/events extension at all is a distinct fault from a kind-set
// mismatch: the runtime doesn't implement what was consented to, not merely
// got some kinds wrong.
func TestProbeOnce_EventExtensionNotDeclaredIsADistinctFault(t *testing.T) {
	ctx := context.Background()
	f := newProberFixture(t, []Target{{
		InstanceID:         "i1",
		ContainerID:        "c1",
		AttestedEventKinds: []string{"message"},
		Profiles:           []Profile{ProfileEventSource},
	}})
	// discover default (zero-value DiscoverResult): the runtime declared no
	// io.gleipnir/events extension at all.

	if _, err := f.prober.ProbeOnce(ctx); err != nil {
		t.Fatalf("ProbeOnce: %v", err)
	}

	h := f.registry.Get("i1")
	if h.Serves(Capability{Profile: ProfileEventSource}) {
		t.Fatal("event source is serving despite the runtime declaring no extension")
	}
	detail := h.RollupDetail()
	if !contains(detail, "no io.gleipnir/events extension") {
		t.Errorf("detail = %q, want it to name the missing extension", detail)
	}
	if contains(detail, "attested but not discovered") {
		t.Errorf("detail = %q, read as a kind-set mismatch instead of a missing extension", detail)
	}
}

// A declared io.gleipnir/events major version this host cannot read is
// refused, not guessed at, and the fault names the version rather than
// reading as a kind-set mismatch.
func TestProbeOnce_EventExtensionVersionRefusedIsADistinctFault(t *testing.T) {
	ctx := context.Background()
	f := newProberFixture(t, []Target{{
		InstanceID:         "i1",
		ContainerID:        "c1",
		AttestedEventKinds: []string{"message"},
		Profiles:           []Profile{ProfileEventSource},
	}})
	f.discover.set(DiscoverResult{
		ExtensionDeclared: true,
		VersionRefused:    true,
		DeclaredVersion:   "99.0.0",
	}, nil)

	if _, err := f.prober.ProbeOnce(ctx); err != nil {
		t.Fatalf("ProbeOnce: %v", err)
	}

	h := f.registry.Get("i1")
	if h.Serves(Capability{Profile: ProfileEventSource}) {
		t.Fatal("event source is serving despite an unreadable major version")
	}
	detail := h.RollupDetail()
	if !contains(detail, "99.0.0") {
		t.Errorf("detail = %q, want it to name the refused version", detail)
	}
	if contains(detail, "attested but not discovered") {
		t.Errorf("detail = %q, read as a kind-set mismatch instead of a refused version", detail)
	}
}

// Neither an attested profile nor a declared extension means there is
// nothing to compare -- silence is not a fault, and nothing is seeded either.
func TestProbeOnce_UndeclaredProfileAndExtensionSeedsAndFaultsNothing(t *testing.T) {
	ctx := context.Background()
	f := newProberFixture(t, []Target{{
		InstanceID: "i1", ContainerID: "c1",
		Profiles: []Profile{ProfileToolProvider},
	}})
	// discover default: extension undeclared, no kinds.

	if _, err := f.prober.ProbeOnce(ctx); err != nil {
		t.Fatalf("ProbeOnce: %v", err)
	}

	h := f.registry.Get("i1")
	for _, e := range h.Entries {
		if e.Capability.Profile == ProfileEventSource {
			t.Errorf("an event_source entry was created for a plugin that attests neither the profile nor the extension: %+v", e)
		}
	}
	if got := h.Rollup(); got != model.PluginHealthStateHealthy {
		t.Errorf("rollup = %q, want healthy", got)
	}
}

// Agreement clears the fault.
func TestProbeOnce_EventDriftRecovers(t *testing.T) {
	ctx := context.Background()
	f := newProberFixture(t, []Target{{
		InstanceID:         "i1",
		ContainerID:        "c1",
		AttestedEventKinds: []string{"message"},
		Profiles:           []Profile{ProfileEventSource},
	}})

	f.discover.set(DiscoverResult{ExtensionDeclared: true, EventKinds: nil}, nil)
	if _, err := f.prober.ProbeOnce(ctx); err != nil {
		t.Fatalf("ProbeOnce: %v", err)
	}
	if f.registry.Serves("i1", Capability{Profile: ProfileEventSource}) {
		t.Fatal("a drifted event source is serving")
	}

	f.discover.set(DiscoverResult{ExtensionDeclared: true, EventKinds: []string{"message"}}, nil)
	if _, err := f.prober.ProbeOnce(ctx); err != nil {
		t.Fatalf("ProbeOnce: %v", err)
	}
	if !f.registry.Serves("i1", Capability{Profile: ProfileEventSource}) {
		t.Error("the event source did not recover once discovery agreed with the manifest")
	}
}

// A plugin that declares no event source has no event kinds to drift, so the
// probe must not invent that capability for it — and must not fault the
// instance over kinds it never claimed to attest.
func TestProbeOnce_NoEventSourceProfileGetsNoEventEntry(t *testing.T) {
	ctx := context.Background()
	f := newProberFixture(t, []Target{{
		InstanceID: "i1", ContainerID: "c1",
		Profiles: []Profile{ProfileToolProvider},
	}})
	// Discovery reports kinds anyway; the plugin declared no event source.
	f.discover.set(DiscoverResult{ExtensionDeclared: true, EventKinds: []string{"surprise"}}, nil)

	if _, err := f.prober.ProbeOnce(ctx); err != nil {
		t.Fatalf("ProbeOnce: %v", err)
	}

	h := f.registry.Get("i1")
	for _, e := range h.Entries {
		if e.Capability.Profile == ProfileEventSource {
			t.Errorf("an event_source entry was invented for a plugin that declares none: %+v", e)
		}
	}
	if got := h.Rollup(); got != model.PluginHealthStateHealthy {
		t.Errorf("rollup = %q, want healthy", got)
	}
}

// Every declared profile gets an entry, so the registry reflects the whole
// surface rather than only the parts something has complained about — which is
// what lets a partial failure be reported as partial.
func TestProbeOnce_SeedsEveryDeclaredProfile(t *testing.T) {
	ctx := context.Background()
	f := newProberFixture(t, []Target{{
		InstanceID: "i1", ContainerID: "c1",
		Profiles: []Profile{ProfileToolProvider, ProfileHumanChannel, ProfileIdentityProvider},
	}})

	if _, err := f.prober.ProbeOnce(ctx); err != nil {
		t.Fatalf("ProbeOnce: %v", err)
	}

	var got []string
	for _, e := range f.registry.Get("i1").Entries {
		if e.State != model.PluginHealthStateHealthy {
			t.Errorf("%s = %q, want healthy", e.Capability, e.State)
		}
		got = append(got, e.Capability.String())
	}
	want := []string{"human_channel", "identity_provider", "tool_provider"}
	if len(got) != len(want) {
		t.Fatalf("entries = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entries = %v, want %v", got, want)
		}
	}
}

// A fault recorded by something other than the prober must survive a pass that
// had no opinion about it — the seam the host endpoint's SetHealthState lands
// on in M17.
func TestProbeOnce_DoesNotClobberAForeignCapabilityFault(t *testing.T) {
	ctx := context.Background()
	f := newProberFixture(t, []Target{{
		InstanceID: "i1", ContainerID: "c1",
		Profiles: []Profile{ProfileToolProvider, ProfileHumanChannel},
	}})

	f.registry.SetCapability("i1", Entry{
		Capability: Capability{Profile: ProfileHumanChannel},
		State:      model.PluginHealthStateUnhealthy,
		Detail:     "reported by the plugin",
	})

	if _, err := f.prober.ProbeOnce(ctx); err != nil {
		t.Fatalf("ProbeOnce: %v", err)
	}

	h := f.registry.Get("i1")
	if h.Serves(Capability{Profile: ProfileHumanChannel}) {
		t.Error("a probe pass overwrote a fault it did not establish")
	}
	if !h.Serves(Capability{Profile: ProfileToolProvider}) {
		t.Error("the unrelated profile stopped serving")
	}
}

// An unreachable instance keeps its last-known capability detail: blanking it
// would destroy what an operator needs to see what broke.
func TestProbeOnce_UnreachableKeepsLastKnownCapabilityDetail(t *testing.T) {
	ctx := context.Background()
	f := newProberFixture(t, []Target{{
		InstanceID:         "i1",
		ContainerID:        "c1",
		AttestedEventKinds: []string{"message"},
		Profiles:           []Profile{ProfileEventSource},
	}})

	f.discover.set(DiscoverResult{ExtensionDeclared: true, EventKinds: []string{"rogue"}}, nil)
	if _, err := f.prober.ProbeOnce(ctx); err != nil {
		t.Fatalf("ProbeOnce: %v", err)
	}
	before := f.registry.Get("i1").Entries
	// The profile-wide entry plus one per-kind entry for each direction of
	// the mismatch ("message" attested-not-discovered, "rogue"
	// discovered-not-attested).
	if len(before) != 3 {
		t.Fatalf("%d entries, want 3", len(before))
	}

	// The instance goes away.
	f.container.set(false, "", nil)
	if _, err := f.prober.ProbeOnce(ctx); err != nil {
		t.Fatalf("ProbeOnce: %v", err)
	}

	after := f.registry.Get("i1")
	if len(after.Entries) != len(before) {
		t.Fatalf("capability entries changed when the instance went unreachable: before %d, after %d", len(before), len(after.Entries))
	}
	for i := range before {
		if after.Entries[i] != before[i] {
			t.Errorf("capability detail was blanked when the instance went unreachable: before %+v, after %+v", before[i], after.Entries[i])
		}
	}
	if got := after.Rollup(); got != model.PluginHealthStateUnhealthy {
		t.Errorf("rollup = %q, want unhealthy", got)
	}
}

// One instance failing must not stop the host learning about the others.
func TestProbeOnce_OneFailureDoesNotAbortThePass(t *testing.T) {
	ctx := context.Background()
	f := newProberFixture(t, []Target{
		{InstanceID: "i1", ContainerID: "c1"},
		{InstanceID: "i2", ContainerID: "c2"},
	})
	f.discover.set(DiscoverResult{}, errors.New("unreachable"))

	result, err := f.prober.ProbeOnce(ctx)
	if err != nil {
		t.Fatalf("ProbeOnce returned an error for a per-instance failure: %v", err)
	}
	if result.Probed != 2 {
		t.Errorf("probed = %d, want both instances looked at", result.Probed)
	}
	if result.Errors != 2 {
		t.Errorf("errors = %d, want 2", result.Errors)
	}
	for _, id := range []string{"i1", "i2"} {
		if got, _ := f.rollup.get(id); got != model.PluginHealthStateUnhealthy {
			t.Errorf("%s rolled up to %q, want unhealthy", id, got)
		}
	}
}

// A target-listing failure IS fatal to the pass: without a list there is
// nothing to probe, and reporting a converged pass would be a lie.
func TestProbeOnce_TargetListFailureFailsThePass(t *testing.T) {
	f := newProberFixture(t, nil)
	f.targets.err = errors.New("db down")

	if _, err := f.prober.ProbeOnce(context.Background()); err == nil {
		t.Fatal("ProbeOnce succeeded despite being unable to list targets")
	}
}

// A rollup-write failure is logged and does not fail the pass — the in-memory
// picture is still correct and the next pass will retry the write.
func TestProbeOnce_RollupWriteFailureIsNotFatal(t *testing.T) {
	f := newProberFixture(t, []Target{{InstanceID: "i1", ContainerID: "c1"}})
	f.rollup.err = errors.New("db busy")

	if _, err := f.prober.ProbeOnce(context.Background()); err != nil {
		t.Fatalf("ProbeOnce: %v", err)
	}
	if !f.registry.Get("i1").Liveness.OK() {
		t.Error("the in-memory verdict was lost because the rollup write failed")
	}
}

// Start runs a synchronous first pass, so "Start returned" means every instance
// has been looked at once.
func TestProber_StartProbesSynchronouslyThenLoops(t *testing.T) {
	ctx := context.Background()
	f := newProberFixture(t, []Target{{InstanceID: "i1", ContainerID: "c1"}})

	if err := f.prober.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { f.prober.Stop(); f.prober.Wait() })

	if !f.registry.Get("i1").Liveness.OK() {
		t.Error("Start returned before the first pass had established liveness")
	}
	if f.publisher.count() != 1 {
		t.Errorf("%d passes published after Start, want 1", f.publisher.count())
	}

	// A kick drives another pass; the test synchronizes on the published event
	// rather than sleeping until the side effect appears.
	f.prober.Kick()
	f.publisher.waitForPasses(t, 2)
}

// Targets are re-read every pass: instances come and go while the loop runs.
func TestProber_RereadsTargetsEveryPass(t *testing.T) {
	ctx := context.Background()
	f := newProberFixture(t, []Target{{InstanceID: "i1", ContainerID: "c1"}})

	if _, err := f.prober.ProbeOnce(ctx); err != nil {
		t.Fatalf("ProbeOnce: %v", err)
	}
	f.targets.set([]Target{{InstanceID: "i2", ContainerID: "c2"}})

	result, err := f.prober.ProbeOnce(ctx)
	if err != nil {
		t.Fatalf("ProbeOnce: %v", err)
	}
	if result.Probed != 1 {
		t.Fatalf("probed = %d, want 1", result.Probed)
	}
	if !f.registry.Get("i2").Liveness.OK() {
		t.Error("the newly-added instance was not probed")
	}
}

func TestProber_StopIsIdempotent(t *testing.T) {
	f := newProberFixture(t, nil)
	if err := f.prober.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	f.prober.Stop()
	f.prober.Stop()
	f.prober.Wait()
}

func TestNew_RequiresDependencies(t *testing.T) {
	base := Config{
		Registry:  NewRegistry(),
		Targets:   &stubTargets{},
		Container: &stubContainer{},
		Discover:  &stubDiscover{},
	}

	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "registry", mutate: func(c *Config) { c.Registry = nil }},
		{name: "targets", mutate: func(c *Config) { c.Targets = nil }},
		{name: "container", mutate: func(c *Config) { c.Container = nil }},
		{name: "discover", mutate: func(c *Config) { c.Discover = nil }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.mutate(&cfg)
			if _, err := New(cfg); err == nil {
				t.Errorf("New succeeded without %s", tc.name)
			}
		})
	}

	// Rollup is optional: a read-only consumer wants the in-memory picture
	// without writing anything.
	cfg := base
	cfg.Rollup = nil
	if _, err := New(cfg); err != nil {
		t.Errorf("New rejected a nil Rollup: %v", err)
	}
}
