package reconciler

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/plugin/caphealth"
	"github.com/felag-engineering/gleipnir/internal/plugin/container"
	"github.com/felag-engineering/gleipnir/internal/plugin/logcapture"
	"github.com/felag-engineering/gleipnir/internal/plugin/resources"
)

const eventSourceManifest = `schema_version: "2"
name: test-plugin
version: "1.0.0"
package:
  registry_type: oci
  identifier: ghcr.io/acme/p@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  transport:
    type: streamable-http
    port: 8080
gleipnir:
  profiles:
    tool_provider: {}
    event_source: {}
  event_kinds:
    - kind: issue.opened
      description: An issue was opened
      binding_schema:
        type: object
        properties:
          repo: {type: string}
`

// observerRecorder implements every observer interface and records the
// synchronous calls in order. Follow is asynchronous by nature, so it is
// recorded separately and signalled on started rather than polled for.
type observerRecorder struct {
	mu      sync.Mutex
	calls   []string
	probe   []caphealth.Target
	stats   []resources.Target
	started chan logcapture.Target
	ended   chan logcapture.Target
}

func newObserverRecorder() *observerRecorder {
	return &observerRecorder{
		started: make(chan logcapture.Target, 16),
		ended:   make(chan logcapture.Target, 16),
	}
}

func (o *observerRecorder) record(call string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls = append(o.calls, call)
}

func (o *observerRecorder) takeCalls() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := o.calls
	o.calls = nil
	return out
}

func (o *observerRecorder) Start(_ context.Context, id string) { o.record("events_start:" + id) }
func (o *observerRecorder) Stop(id string)                     { o.record("events_stop:" + id) }

func (o *observerRecorder) Forget(id string) { o.record("forget:" + id) }

func (o *observerRecorder) Follow(ctx context.Context, target logcapture.Target) error {
	o.started <- target
	<-ctx.Done()
	o.ended <- target
	return nil
}

type probeRecorder struct{ *observerRecorder }

func (p probeRecorder) SetTargets(targets []caphealth.Target) {
	p.mu.Lock()
	p.probe = targets
	p.mu.Unlock()
	p.record("probe_set")
}

type statsRecorder struct{ *observerRecorder }

func (s statsRecorder) SetTargets(targets []resources.Target) {
	s.mu.Lock()
	s.stats = targets
	s.mu.Unlock()
	s.record("stats_set")
}

func (o *observerRecorder) deps() *ObserveDeps {
	return &ObserveDeps{
		Events: o,
		Probe:  probeRecorder{o},
		Health: healthRecorder{o},
		Logs:   o,
		Stats:  statsRecorder{o},
	}
}

// healthRecorder keeps the registry's Forget distinguishable from log
// capture's in the call log.
type healthRecorder struct{ *observerRecorder }

func (h healthRecorder) Forget(id string) { h.record("health_forget:" + id) }

func observeKinds(actions []ObserveAction) []string {
	out := make([]string, len(actions))
	for i, a := range actions {
		out[i] = string(a.Kind) + ":" + a.InstanceID
	}
	return out
}

func TestPlanObserve(t *testing.T) {
	probeA := caphealth.Target{InstanceID: "a", ContainerID: "ca"}
	statsA := resources.Target{InstanceID: "a", ContainerID: "ca"}
	logA := logcapture.Target{InstanceID: "a", ContainerID: "ca"}
	live := func(eventSource bool) observeInstance {
		return observeInstance{
			InstanceID: "a", EventSource: eventSource,
			Probe: probeA, Stats: statsA, Containers: []logcapture.Target{logA},
		}
	}
	settled := func(events bool) observedState {
		s := observedState{
			Known:     map[string]struct{}{"a": {}},
			Events:    map[string]struct{}{},
			Probe:     map[string]caphealth.Target{"a": probeA},
			Stats:     map[string]resources.Target{"a": statsA},
			Following: map[string]map[container.ContainerID]struct{}{"a": {"ca": {}}},
		}
		if events {
			s.Events["a"] = struct{}{}
		}
		return s
	}

	tests := []struct {
		name      string
		instances []observeInstance
		prior     observedState
		want      []string
	}{
		{
			name:      "new event_source instance starts everything, supervisor last",
			instances: []observeInstance{live(true)},
			want:      []string{"probe_set:", "stats_set:", "log_follow:a", "events_start:a"},
		},
		{
			name:      "instance without event_source gets no stream",
			instances: []observeInstance{live(false)},
			want:      []string{"probe_set:", "stats_set:", "log_follow:a"},
		},
		{
			name:      "converged world plans nothing",
			instances: []observeInstance{live(true)},
			prior:     settled(true),
		},
		{
			name:      "profile removed on upgrade stops the stream only",
			instances: []observeInstance{live(false)},
			prior:     settled(true),
			want:      []string{"events_stop:a"},
		},
		{
			name:  "gone instance tears down in quiesce order",
			prior: settled(true),
			want:  []string{"events_stop:a", "probe_set:", "stats_set:", "log_unfollow:a", "forget:a"},
		},
		{
			name: "held instance keeps stream and targets, still follows logs",
			instances: []observeInstance{{
				InstanceID: "a", HoldReason: "inspecting container: boom",
				Containers: []logcapture.Target{logA},
			}},
			prior: settled(true),
		},
		{
			name: "held instance with nothing prior starts nothing but logs",
			instances: []observeInstance{{
				InstanceID: "a", HoldReason: "no generation has passed its health gate",
				EventSource: true, Containers: []logcapture.Target{logA},
			}},
			want: []string{"log_follow:a"},
		},
		{
			name: "rotation follows the new container and drops the retired one",
			instances: []observeInstance{{
				InstanceID: "a", Probe: caphealth.Target{InstanceID: "a", ContainerID: "cb"},
				Stats:      resources.Target{InstanceID: "a", ContainerID: "cb"},
				Containers: []logcapture.Target{{InstanceID: "a", ContainerID: "cb"}},
			}},
			prior: settled(false),
			want:  []string{"probe_set:", "stats_set:", "log_unfollow:a", "log_follow:a"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.prior.Known == nil {
				tt.prior = observedState{
					Known: map[string]struct{}{}, Events: map[string]struct{}{},
					Probe: map[string]caphealth.Target{}, Stats: map[string]resources.Target{},
					Following: map[string]map[container.ContainerID]struct{}{},
				}
			}
			got := observeKinds(planObserve(tt.instances, tt.prior).Actions)
			if !reflect.DeepEqual(got, tt.want) && !(len(got) == 0 && len(tt.want) == 0) {
				t.Errorf("actions = %v, want %v", got, tt.want)
			}
		})
	}
}

type observeFixture struct {
	*attachFixture
	rec *observerRecorder
}

func newObserveFixture(t *testing.T, posture container.Posture) *observeFixture {
	t.Helper()
	f := &observeFixture{attachFixture: newAttachFixture(t, posture), rec: newObserverRecorder()}
	f.r.observe = f.rec.deps()
	return f
}

func (f *observeFixture) setManifest(t *testing.T, manifest string) {
	t.Helper()
	if _, err := f.store.DB().Exec(`UPDATE plugins SET manifest_snapshot = ?`, manifest); err != nil {
		t.Fatalf("set manifest: %v", err)
	}
}

// pass runs the same two halves the run loop does.
func (f *observeFixture) pass(t *testing.T) []ObserveAction {
	t.Helper()
	if _, err := f.r.AttachPass(context.Background()); err != nil {
		t.Fatalf("AttachPass: %v", err)
	}
	applied, err := f.r.ObservePass(context.Background())
	if err != nil {
		t.Fatalf("ObservePass: %v", err)
	}
	return applied
}

func (f *observeFixture) containerID(t *testing.T, instanceID string, generation int64) container.ContainerID {
	t.Helper()
	gens, err := f.store.Queries().ListContainerGenerationsByInstance(context.Background(), instanceID)
	if err != nil {
		t.Fatalf("list generations: %v", err)
	}
	for _, gen := range gens {
		if gen.Generation == generation && gen.ContainerID != nil {
			return container.ContainerID(*gen.ContainerID)
		}
	}
	t.Fatalf("no container for generation %d", generation)
	return ""
}

func (f *observeFixture) retireInstance(t *testing.T, instanceID string) {
	t.Helper()
	for _, gen := range liveGenerations(t, f.store, instanceID) {
		if _, err := f.store.Queries().UpdateContainerGenerationStatus(context.Background(), db.UpdateContainerGenerationStatusParams{
			Status: GenStopped, UpdatedAt: "2026-08-01T00:00:00Z", ID: gen.ID, ExpectedStatus: gen.Status,
		}); err != nil {
			t.Fatalf("stop generation %d: %v", gen.Generation, err)
		}
	}
}

func TestObservePass_EventSourceInstanceStartsEverything(t *testing.T) {
	f := newObserveFixture(t, container.PostureRootlessPodman)
	f.seed(t, "inst1", "10.83.4.2")
	f.setManifest(t, eventSourceManifest)
	t.Cleanup(f.r.Stop)

	applied := f.pass(t)
	want := []string{"probe_set:", "stats_set:", "log_follow:inst1", "events_start:inst1"}
	if got := observeKinds(applied); !reflect.DeepEqual(got, want) {
		t.Fatalf("applied = %v, want %v", got, want)
	}

	cid := f.containerID(t, "inst1", 1)
	followed := <-f.rec.started
	if followed.ContainerID != cid || followed.InstanceID != "inst1" || followed.Generation != 1 {
		t.Errorf("followed %+v, want instance inst1 container %s generation 1", followed, cid)
	}

	if len(f.rec.probe) != 1 {
		t.Fatalf("probe targets = %+v, want one", f.rec.probe)
	}
	gotProbe := f.rec.probe[0]
	if gotProbe.InstanceID != "inst1" || gotProbe.ContainerID != string(cid) {
		t.Errorf("probe target = %+v, want inst1 / %s", gotProbe, cid)
	}
	if !reflect.DeepEqual(gotProbe.Profiles, []caphealth.Profile{caphealth.ProfileToolProvider, caphealth.ProfileEventSource}) {
		t.Errorf("probe profiles = %v", gotProbe.Profiles)
	}
	if !reflect.DeepEqual(gotProbe.AttestedEventKinds, []string{"issue.opened"}) {
		t.Errorf("attested kinds = %v", gotProbe.AttestedEventKinds)
	}
	if len(f.rec.stats) != 1 || f.rec.stats[0].ContainerID != cid || f.rec.stats[0].InstanceName != "i-inst1" {
		t.Errorf("stats targets = %+v", f.rec.stats)
	}
}

func TestObservePass_SecondPassIsANoOp(t *testing.T) {
	f := newObserveFixture(t, container.PostureRootlessPodman)
	f.seed(t, "inst1", "10.83.4.2")
	f.setManifest(t, eventSourceManifest)
	t.Cleanup(f.r.Stop)

	f.pass(t)
	<-f.rec.started
	f.rec.takeCalls()

	if again := f.pass(t); len(again) != 0 {
		t.Errorf("second pass applied %v, want none", observeKinds(again))
	}
	if calls := f.rec.takeCalls(); len(calls) != 0 {
		t.Errorf("second pass touched observers: %v", calls)
	}
}

func TestObservePass_ProfileRemovedOnUpgradeStopsStream(t *testing.T) {
	f := newObserveFixture(t, container.PostureRootlessPodman)
	f.seed(t, "inst1", "10.83.4.2")
	f.setManifest(t, eventSourceManifest)
	t.Cleanup(f.r.Stop)

	f.pass(t)
	<-f.rec.started
	f.rec.takeCalls()

	f.setManifest(t, attachManifest)
	applied := f.pass(t)

	want := []string{"events_stop:inst1", "probe_set:"}
	if got := observeKinds(applied); !reflect.DeepEqual(got, want) {
		t.Fatalf("applied = %v, want %v", got, want)
	}
	if got := f.rec.probe[0].Profiles; !reflect.DeepEqual(got, []caphealth.Profile{caphealth.ProfileToolProvider}) {
		t.Errorf("probe profiles after upgrade = %v, want tool_provider only", got)
	}
	if len(f.rec.probe[0].AttestedEventKinds) != 0 {
		t.Errorf("attested kinds after upgrade = %v, want none", f.rec.probe[0].AttestedEventKinds)
	}
}

func TestObservePass_GoneInstanceStopsForgetsAndDropsTargets(t *testing.T) {
	f := newObserveFixture(t, container.PostureRootlessPodman)
	f.seed(t, "inst1", "10.83.4.2")
	f.setManifest(t, eventSourceManifest)
	t.Cleanup(f.r.Stop)

	f.pass(t)
	<-f.rec.started
	f.rec.takeCalls()

	f.retireInstance(t, "inst1")
	f.pass(t)

	// The follower must be joined (ended) before the pass returned.
	<-f.rec.ended

	want := []string{"events_stop:inst1", "probe_set", "stats_set", "forget:inst1", "health_forget:inst1"}
	if got := f.rec.takeCalls(); !reflect.DeepEqual(got, want) {
		t.Errorf("calls = %v, want %v", got, want)
	}
	if len(f.rec.probe) != 0 || len(f.rec.stats) != 0 {
		t.Errorf("targets remain: probe=%+v stats=%+v", f.rec.probe, f.rec.stats)
	}

	if again := f.pass(t); len(again) != 0 {
		t.Errorf("pass after teardown applied %v, want none", observeKinds(again))
	}
}

// A live instance whose address cannot be resolved this pass keeps its stream
// and targets: dropping them over a transient inspect failure would churn the
// stream for nothing. Log capture is not held, since it needs no route.
func TestObservePass_UnroutableInstanceHolds(t *testing.T) {
	f := newObserveFixture(t, container.PostureRootlessPodman)
	f.seed(t, "inst1", "10.83.4.2")
	f.setManifest(t, eventSourceManifest)
	t.Cleanup(f.r.Stop)

	f.pass(t)
	<-f.rec.started
	f.rec.takeCalls()

	f.setIP(t, "inst1", 1, "")
	if applied := f.pass(t); len(applied) != 0 {
		t.Errorf("held pass applied %v, want none", observeKinds(applied))
	}
	if calls := f.rec.takeCalls(); len(calls) != 0 {
		t.Errorf("held pass touched observers: %v", calls)
	}
	if len(f.rec.probe) != 1 {
		t.Errorf("probe target dropped while held: %+v", f.rec.probe)
	}
}

// Rotation: both containers are followed while the old one drains, and the
// probe target moves to the new container so Prober.observeRestart sees the
// ContainerID change.
func TestObservePass_RotationFollowsBothThenDropsRetired(t *testing.T) {
	f := newObserveFixture(t, container.PostureRootlessPodman)
	f.seed(t, "inst1", "10.83.4.2")
	f.setManifest(t, eventSourceManifest)
	t.Cleanup(f.r.Stop)

	f.pass(t)
	<-f.rec.started
	f.rec.takeCalls()

	f.switchTo(t, "inst1", "10.83.4.9")
	f.pass(t)
	if got := <-f.rec.started; got.Generation != 2 {
		t.Errorf("followed generation %d, want 2", got.Generation)
	}
	if want := f.containerID(t, "inst1", 2); f.rec.probe[0].ContainerID != string(want) {
		t.Errorf("probe ContainerID = %s, want the new generation's %s", f.rec.probe[0].ContainerID, want)
	}
	for _, c := range f.rec.takeCalls() {
		if c == "events_start:inst1" || c == "events_stop:inst1" {
			t.Errorf("rotation restarted the stream (%s); the supervisor reconnects through the repointed endpoint itself", c)
		}
	}
}

func TestObservePass_ManualPostureObservesMatchedOnly(t *testing.T) {
	tests := []struct {
		name    string
		version int64
		want    []string
	}{
		{name: "matched is observed", version: 1, want: []string{"probe_set:", "stats_set:", "log_follow:inst1", "events_start:inst1"}},
		{name: "wrong_generation is not", version: 2, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newObserveFixture(t, container.PostureManual)
			f.seed(t, "inst1", "10.83.4.2")
			f.setManifest(t, eventSourceManifest)
			if _, err := f.store.DB().Exec(`UPDATE plugin_containers SET version = ?`, tt.version); err != nil {
				t.Fatalf("set desired generation: %v", err)
			}
			t.Cleanup(f.r.Stop)

			writesBefore := f.rt.writes()
			applied := f.pass(t)
			if got := observeKinds(applied); !reflect.DeepEqual(got, tt.want) && len(tt.want) > 0 {
				t.Errorf("applied = %v, want %v", got, tt.want)
			}
			if len(tt.want) == 0 && len(applied) != 0 {
				t.Errorf("applied = %v, want none", observeKinds(applied))
			}
			if got := f.rt.writes() - writesBefore; got != 0 {
				t.Errorf("manual posture made %d socket writes, want 0", got)
			}
		})
	}
}

// Without Observe configured the pass is a no-op and never reads the world.
func TestObservePass_DisabledWithoutDeps(t *testing.T) {
	f := newAttachFixture(t, container.PostureRootlessPodman)
	applied, err := f.r.ObservePass(context.Background())
	if err != nil || applied != nil {
		t.Errorf("ObservePass = %v, %v; want nil, nil", applied, err)
	}
}

func TestObservePass_OnlyConfiguredObserversAreDriven(t *testing.T) {
	f := newAttachFixture(t, container.PostureRootlessPodman)
	rec := newObserverRecorder()
	f.r.observe = &ObserveDeps{Events: rec}
	f.seed(t, "inst1", "10.83.4.2")
	if _, err := f.store.DB().Exec(`UPDATE plugins SET manifest_snapshot = ?`, eventSourceManifest); err != nil {
		t.Fatalf("set manifest: %v", err)
	}

	if _, err := f.r.AttachPass(context.Background()); err != nil {
		t.Fatalf("AttachPass: %v", err)
	}
	applied, err := f.r.ObservePass(context.Background())
	if err != nil {
		t.Fatalf("ObservePass: %v", err)
	}
	if got, want := observeKinds(applied), []string{"events_start:inst1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("applied = %v, want %v (unset observers are skipped, not counted)", got, want)
	}
}

// The run loop owns the ordering and publishes a signal once observe has run.
// Start's boot pass is synchronous, so the event is already in the channel by
// the time Start returns; and Stop must join the follower.
func TestRunPass_ObservesAfterAttachAndSignals(t *testing.T) {
	f := newObserveFixture(t, container.PostureRootlessPodman)
	f.seed(t, "inst1", "10.83.4.2")
	f.setManifest(t, eventSourceManifest)

	signals := make(chan string, 8)
	f.r.publisher = chanPublisher(signals)

	if err := f.r.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-f.rec.started

	var seen []string
	for len(seen) == 0 || seen[len(seen)-1] != EventObservePassCompleted {
		seen = append(seen, <-signals)
	}
	passIdx, observeIdx := -1, -1
	for i, s := range seen {
		switch s {
		case EventPassCompleted:
			passIdx = i
		case EventObservePassCompleted:
			observeIdx = i
		}
	}
	if passIdx < 0 || observeIdx < passIdx {
		t.Errorf("signals = %v, want %s before %s", seen, EventPassCompleted, EventObservePassCompleted)
	}
	if _, ok := f.managedRow(t, "inst1"); !ok {
		t.Error("observe ran before attach routed the endpoint")
	}

	f.r.Stop()
	<-f.rec.ended
}

type chanPublisher chan string

func (c chanPublisher) Publish(eventType string, _ json.RawMessage) { c <- eventType }

func TestNew_ObserveValidation(t *testing.T) {
	base := func() Config {
		return Config{Runtime: newTestFake(), Store: &fakeStore{}, Posture: container.PostureManual}
	}
	rec := newObserverRecorder()

	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{name: "observe requires attach", mutate: func(c *Config) { c.Observe = &ObserveDeps{Events: rec} }, wantErr: "requires Attach"},
		{name: "empty observe is refused", mutate: func(c *Config) {
			c.Attach = &AttachDeps{Store: &db.Queries{}, Endpoints: nopEndpoints{}}
			c.Observe = &ObserveDeps{}
		}, wantErr: "no observer"},
		{name: "one observer is enough", mutate: func(c *Config) {
			c.Attach = &AttachDeps{Store: &db.Queries{}, Endpoints: nopEndpoints{}}
			c.Observe = &ObserveDeps{Logs: rec}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base()
			tt.mutate(&cfg)
			_, err := New(cfg)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("New: %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("New error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

type nopEndpoints struct{}

func (nopEndpoints) Register(context.Context, mcp.ManagedEndpoint) (db.McpServer, error) {
	return db.McpServer{}, fmt.Errorf("unused")
}
func (nopEndpoints) Deregister(context.Context, string) error { return nil }
