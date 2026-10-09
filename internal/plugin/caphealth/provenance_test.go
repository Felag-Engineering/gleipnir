package caphealth

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/model"
)

var (
	kindCap    = Capability{Profile: ProfileEventSource, Name: "message"}
	profileCap = Capability{Profile: ProfileEventSource}
)

func selfReportKindFault(t *testing.T, f proberFixture) {
	t.Helper()
	if !f.registry.SelfReportCapability("i1", Entry{
		Capability: kindCap,
		State:      model.PluginHealthStateUnhealthy,
		Detail:     "upstream API rejects our token",
	}) {
		t.Fatal("self-report was not applied")
	}
}

func eventFixture(t *testing.T) proberFixture {
	t.Helper()
	f := newProberFixture(t, []Target{{
		InstanceID:         "i1",
		ContainerID:        "c1",
		AttestedEventKinds: []string{"message"},
		Profiles:           []Profile{ProfileEventSource},
	}})
	f.discover.set(DiscoverResult{ExtensionDeclared: true, EventKinds: []string{"message"}}, nil)
	return f
}

func probe(t *testing.T, f proberFixture) {
	t.Helper()
	if _, err := f.prober.ProbeOnce(context.Background()); err != nil {
		t.Fatalf("ProbeOnce: %v", err)
	}
}

func entryFor(t *testing.T, r *Registry, c Capability) (Entry, bool) {
	t.Helper()
	for _, e := range r.Get("i1").Entries {
		if e.Capability == c {
			return e, true
		}
	}
	return Entry{}, false
}

// Kind-set agreement says nothing about a kind's functional health, so it must
// not clear a fault the plugin reported about that kind (#911).
func TestProbeOnce_AgreementKeepsSelfReportedKindFault(t *testing.T) {
	f := eventFixture(t)
	probe(t, f)
	selfReportKindFault(t, f)

	for pass := 1; pass <= 3; pass++ {
		probe(t, f)
		if f.registry.Serves("i1", kindCap) {
			t.Fatalf("pass %d: drift agreement cleared a self-reported functional fault", pass)
		}
		e, _ := entryFor(t, f.registry, kindCap)
		if e.Source != SourceSelfReport || e.Detail != "upstream API rejects our token" {
			t.Fatalf("pass %d: entry = %+v, want the original self-report", pass, e)
		}
	}
}

func TestProbeOnce_AgreementKeepsSelfReportedProfileFault(t *testing.T) {
	f := eventFixture(t)
	probe(t, f)
	f.registry.SelfReportCapability("i1", Entry{Capability: profileCap, State: model.PluginHealthStateUnhealthy, Detail: "listener wedged"})

	probe(t, f)

	if f.registry.Serves("i1", profileCap) {
		t.Error("drift agreement overwrote a self-reported profile-wide fault to healthy")
	}
}

// The drift pass must still clear its OWN stale verdicts.
func TestProbeOnce_FixedManifestClearsDriftFaultNextPass(t *testing.T) {
	f := eventFixture(t)
	f.discover.set(DiscoverResult{ExtensionDeclared: true, EventKinds: []string{"other"}}, nil)
	probe(t, f)
	if f.registry.Serves("i1", kindCap) || f.registry.Serves("i1", profileCap) {
		t.Fatal("drift fault was not recorded")
	}

	f.discover.set(DiscoverResult{ExtensionDeclared: true, EventKinds: []string{"message"}}, nil)
	probe(t, f)

	for _, c := range []Capability{profileCap, kindCap} {
		if !f.registry.Serves("i1", c) {
			t.Errorf("%s still faulted after the drift was fixed", c)
		}
	}
	if _, ok := entryFor(t, f.registry, Capability{Profile: ProfileEventSource, Name: "other"}); ok {
		t.Error("stale per-kind drift entry for the removed kind survived")
	}
}

// A plugin cannot launder a drift fault by reporting the capability healthy or
// by restating it unhealthy under its own name.
func TestSelfReport_CannotClearOrTakeOverDriftFault(t *testing.T) {
	tests := []struct {
		name  string
		state model.PluginHealthState
	}{
		{"healthy report", model.PluginHealthStateHealthy},
		{"unhealthy restatement", model.PluginHealthStateUnhealthy},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := eventFixture(t)
			f.discover.set(DiscoverResult{ExtensionDeclared: true, EventKinds: nil}, nil)
			probe(t, f)

			applied := f.registry.SelfReportCapability("i1", Entry{Capability: kindCap, State: tt.state})
			if applied {
				t.Error("self-report applied over a drift fault")
			}
			e, _ := entryFor(t, f.registry, kindCap)
			if e.Source != SourceProbe || e.State != model.PluginHealthStateUnhealthy {
				t.Errorf("entry = %+v, want the probe-written unhealthy entry untouched", e)
			}

			// Because the drift entry stayed probe-owned, a fixed manifest still clears it.
			f.discover.set(DiscoverResult{ExtensionDeclared: true, EventKinds: []string{"message"}}, nil)
			probe(t, f)
			if !f.registry.Serves("i1", kindCap) {
				t.Error("fixed manifest did not clear the drift fault")
			}
		})
	}
}

// The recovery observation: a host-observed restart clears self-reported faults.
func TestProbeOnce_RestartClearsSelfReportedFault(t *testing.T) {
	tests := []struct {
		name    string
		restart func(t *testing.T, f proberFixture)
		cleared bool
	}{
		{
			name:    "no restart",
			restart: func(*testing.T, proberFixture) {},
			cleared: false,
		},
		{
			name: "unreachable then reachable again",
			restart: func(t *testing.T, f proberFixture) {
				f.container.set(false, "exited", nil)
				probe(t, f)
				f.container.set(true, "", nil)
			},
			cleared: true,
		},
		{
			name: "container replaced by a new generation",
			restart: func(t *testing.T, f proberFixture) {
				f.targets.set([]Target{{
					InstanceID:         "i1",
					ContainerID:        "c2",
					AttestedEventKinds: []string{"message"},
					Profiles:           []Profile{ProfileEventSource},
				}})
			},
			cleared: true,
		},
		{
			name: "staying down does not clear",
			restart: func(t *testing.T, f proberFixture) {
				f.container.set(false, "exited", nil)
			},
			cleared: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := eventFixture(t)
			probe(t, f)
			selfReportKindFault(t, f)

			tt.restart(t, f)
			probe(t, f)

			_, stillThere := entryFor(t, f.registry, kindCap)
			if stillThere == tt.cleared {
				t.Fatalf("self-reported entry present = %v, want %v", stillThere, !tt.cleared)
			}
			if tt.cleared && !f.registry.Serves("i1", kindCap) {
				t.Error("kind still not serving after the restart cleared its self-report")
			}
		})
	}
}

// A restart clears only self-reports; host-written drift verdicts are not its
// business.
func TestClearSelfReported_LeavesProbeEntries(t *testing.T) {
	r := NewRegistry()
	r.SetCapability("i1", Entry{Capability: profileCap, State: model.PluginHealthStateUnhealthy, Detail: "drift"})
	r.SelfReportCapability("i1", Entry{Capability: kindCap, State: model.PluginHealthStateUnhealthy})

	r.ClearSelfReported("i1")

	if _, ok := entryFor(t, r, kindCap); ok {
		t.Error("self-reported entry survived ClearSelfReported")
	}
	if _, ok := entryFor(t, r, profileCap); !ok {
		t.Error("probe-written entry was removed by ClearSelfReported")
	}
}

// SetCapability's worsen-only merge against self-reports, table-driven.
func TestSetCapability_MergesWorsenOnlyAgainstSelfReport(t *testing.T) {
	tests := []struct {
		name       string
		prior      model.PluginHealthState
		set        model.PluginHealthState
		wantState  model.PluginHealthState
		wantSource Source
	}{
		{"healthy does not clear", model.PluginHealthStateUnhealthy, model.PluginHealthStateHealthy, model.PluginHealthStateUnhealthy, SourceSelfReport},
		{"restatement keeps self-report", model.PluginHealthStateUnhealthy, model.PluginHealthStateUnhealthy, model.PluginHealthStateUnhealthy, SourceSelfReport},
		{"worse verdict takes over", model.PluginHealthStateUnhealthy, model.PluginHealthStateCircuitBroken, model.PluginHealthStateCircuitBroken, SourceProbe},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRegistry()
			r.SelfReportCapability("i1", Entry{Capability: kindCap, State: tt.prior})
			r.SetCapability("i1", Entry{Capability: kindCap, State: tt.set})

			e, _ := entryFor(t, r, kindCap)
			if e.State != tt.wantState || e.Source != tt.wantSource {
				t.Errorf("entry = %+v, want state %q source %v", e, tt.wantState, tt.wantSource)
			}
		})
	}
}

// Self-reports racing the probe-side clear/overwrite must never be lost: every
// self-reported fault recorded before the probe operations run must still be
// there afterwards.
func TestRegistry_ConcurrentProbeWritesNeverEraseSelfReports(t *testing.T) {
	r := NewRegistry()
	const kinds = 32
	for i := 0; i < kinds; i++ {
		r.SelfReportCapability("i1", Entry{
			Capability: Capability{Profile: ProfileEventSource, Name: fmt.Sprintf("k%d", i)},
			State:      model.PluginHealthStateUnhealthy,
		})
	}

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				r.ClearProbeEventKindFaults("i1")
				for k := 0; k < kinds; k++ {
					r.SetCapability("i1", Entry{
						Capability: Capability{Profile: ProfileEventSource, Name: fmt.Sprintf("k%d", k)},
						State:      model.PluginHealthStateHealthy,
					})
				}
			}
		}()
	}
	wg.Wait()

	for i := 0; i < kinds; i++ {
		c := Capability{Profile: ProfileEventSource, Name: fmt.Sprintf("k%d", i)}
		e, ok := entryFor(t, r, c)
		if !ok || e.State != model.PluginHealthStateUnhealthy || e.Source != SourceSelfReport {
			t.Fatalf("%s = %+v (present=%v), want the self-report intact", c, e, ok)
		}
	}
}
