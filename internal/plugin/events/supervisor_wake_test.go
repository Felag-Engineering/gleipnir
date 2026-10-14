package events_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/plugin/caphealth"
	"github.com/felag-engineering/gleipnir/internal/plugin/events"
)

const (
	wakeBackoffInitial = time.Second
	wakeBackoffMax     = time.Minute
)

// recordingTimers is an events.Config.After that never fires on its own: every
// wait the supervisor starts is reported on requested and then lasts until the
// test wakes it by another route (a registry write) or cancels it. A test that
// sees the stream open therefore proves the wake, not a timer.
type recordingTimers struct {
	requested chan time.Duration

	mu      sync.Mutex
	stopped int
}

func newRecordingTimers() *recordingTimers {
	return &recordingTimers{requested: make(chan time.Duration, 64)}
}

func (r *recordingTimers) after(d time.Duration) (<-chan time.Time, func()) {
	r.requested <- d
	return make(chan time.Time), func() {
		r.mu.Lock()
		r.stopped++
		r.mu.Unlock()
	}
}

func (r *recordingTimers) stopCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stopped
}

type wakeFixture struct {
	sup    *events.Supervisor
	health *caphealth.Registry
	opener *fakeOpener
	timers *recordingTimers
}

func newWakeFixture(t *testing.T) *wakeFixture {
	t.Helper()

	q := newFakeSupervisorQuerier()
	q.setInstance(db.PluginInstance{ID: "inst-1", PluginID: "plug-1", SubscriptionScopeJson: `{}`})
	q.setPlugin(db.Plugin{ID: "plug-1", ManifestSnapshot: buildEventManifestYAML(t, "message")})

	f := &wakeFixture{
		health: caphealth.NewRegistry(),
		opener: newFakeOpener(func(int) *fakeStream { return &fakeStream{} }),
		timers: newRecordingTimers(),
	}
	f.sup = events.NewSupervisor(events.Config{
		Querier:            q,
		TestStreamResolver: &fakeResolver{opener: f.opener},
		Capability:         f.health,
		Cursor:             newFakeCursorStore(),
		Sink:               newFakeSink(),
		After:              f.timers.after,
		BackoffInitial:     wakeBackoffInitial,
		BackoffMax:         wakeBackoffMax,
		UnhealthyAfter:     3,
	})
	t.Cleanup(f.sup.StopAll)
	return f
}

var liveOK = caphealth.Liveness{ContainerHealthy: true, DiscoverOK: true}

// A new instance's first connect must not wait out a timer: the prober's
// first verdict wakes it.
func TestSupervisor_UnobservedThenLive_ConnectsOnWake(t *testing.T) {
	t.Parallel()
	f := newWakeFixture(t)

	f.sup.Start(context.Background(), "inst-1")

	// The supervisor is parked in its gate wait; the timer never fires.
	waitOnChan(t, f.timers.requested, testTimeout, "gate wait to start")
	if got := f.opener.callCount(); got != 0 {
		t.Fatalf("connect attempts before liveness = %d, want 0", got)
	}

	f.health.SetLiveness("inst-1", liveOK)
	waitOnChan(t, f.opener.opened, testTimeout, "stream to open on the liveness wake")
}

// Liveness arriving while the supervisor is between its check and its wait
// must not be lost.
func TestSupervisor_LivenessRecordedBeforeStart_ConnectsWithoutWaiting(t *testing.T) {
	t.Parallel()
	f := newWakeFixture(t)
	f.health.SetLiveness("inst-1", liveOK)

	f.sup.Start(context.Background(), "inst-1")

	waitOnChan(t, f.opener.opened, testTimeout, "stream to open")
	select {
	case d := <-f.timers.requested:
		t.Fatalf("supervisor waited %v although the instance was already live", d)
	default:
	}
}

// "Not observed yet" retries on the short ramp; "known unhealthy" takes the
// long backoff. Both wake on a change.
func TestSupervisor_GateWaitLength_DependsOnWhetherHealthIsKnown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		liveness *caphealth.Liveness
		check    func(t *testing.T, d time.Duration)
	}{
		{
			name:     "unobserved retries on the short ramp",
			liveness: nil,
			check: func(t *testing.T, d time.Duration) {
				// First ramp step is BackoffInitial with +-25% jitter.
				if d > wakeBackoffInitial*5/4 {
					t.Errorf("unobserved wait = %v, want at most ~%v", d, wakeBackoffInitial)
				}
			},
		},
		{
			name:     "known unhealthy takes the full backoff",
			liveness: &caphealth.Liveness{ContainerHealthy: true, DiscoverOK: false, Detail: "discover failed"},
			check: func(t *testing.T, d time.Duration) {
				if d != wakeBackoffMax {
					t.Errorf("known-unhealthy wait = %v, want %v", d, wakeBackoffMax)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newWakeFixture(t)
			if tt.liveness != nil {
				f.health.SetLiveness("inst-1", *tt.liveness)
			}

			f.sup.Start(context.Background(), "inst-1")

			tt.check(t, waitOnChan(t, f.timers.requested, testTimeout, "gate wait to start"))
			if got := f.opener.callCount(); got != 0 {
				t.Errorf("connect attempts while not serving = %d, want 0", got)
			}
		})
	}
}

// Recovery of a known-unhealthy instance is also prompt, and an unrelated
// write that leaves it unhealthy only re-checks and waits again.
func TestSupervisor_KnownUnhealthy_RechecksOnChangeAndConnectsOnRecovery(t *testing.T) {
	t.Parallel()
	f := newWakeFixture(t)
	f.health.SetLiveness("inst-1", caphealth.Liveness{Detail: "down"})

	f.sup.Start(context.Background(), "inst-1")
	if d := waitOnChan(t, f.timers.requested, testTimeout, "first gate wait"); d != wakeBackoffMax {
		t.Fatalf("first wait = %v, want %v", d, wakeBackoffMax)
	}

	// Still unhealthy: wakes, re-checks, waits the full backoff again.
	f.health.SetLiveness("inst-1", caphealth.Liveness{Detail: "still down"})
	if d := waitOnChan(t, f.timers.requested, testTimeout, "second gate wait"); d != wakeBackoffMax {
		t.Fatalf("wait after a still-unhealthy verdict = %v, want %v", d, wakeBackoffMax)
	}
	if got := f.opener.callCount(); got != 0 {
		t.Fatalf("connect attempts while unhealthy = %d, want 0", got)
	}

	f.health.SetLiveness("inst-1", liveOK)
	waitOnChan(t, f.opener.opened, testTimeout, "stream to open on recovery")
}

// A capability flip alone (liveness already fine) wakes the stream too.
func TestSupervisor_EventSourceFlipsToServing_ConnectsOnWake(t *testing.T) {
	t.Parallel()
	f := newWakeFixture(t)
	f.health.SetLiveness("inst-1", liveOK)
	f.health.SetCapability("inst-1", caphealth.Entry{
		Capability: caphealth.Capability{Profile: caphealth.ProfileEventSource},
		State:      "unhealthy",
		Detail:     "drift",
	})

	f.sup.Start(context.Background(), "inst-1")
	waitOnChan(t, f.timers.requested, testTimeout, "gate wait to start")

	f.health.ClearCapability("inst-1", caphealth.Capability{Profile: caphealth.ProfileEventSource})
	waitOnChan(t, f.opener.opened, testTimeout, "stream to open once event_source serves")
}

// Stop and StopAll during a gate wait return (they join the goroutine) and
// release the wait's timer.
func TestSupervisor_StopDuringGateWait_ExitsCleanly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		stop func(s *events.Supervisor)
	}{
		{"Stop", func(s *events.Supervisor) { s.Stop("inst-1") }},
		{"StopAll", func(s *events.Supervisor) { s.StopAll() }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newWakeFixture(t)
			f.sup.Start(context.Background(), "inst-1")
			waitOnChan(t, f.timers.requested, testTimeout, "gate wait to start")

			stopped := make(chan struct{})
			go func() {
				tt.stop(f.sup)
				close(stopped)
			}()
			waitOnChan(t, stopped, testTimeout, "stop to join the stream goroutine")

			if got := f.timers.stopCount(); got != 1 {
				t.Errorf("released timers = %d, want 1", got)
			}
			if got := f.opener.callCount(); got != 0 {
				t.Errorf("connect attempts = %d, want 0", got)
			}
		})
	}
}
