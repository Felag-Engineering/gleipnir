package main

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/plugin-sdk/hostclient"
	"github.com/slack-go/slack/socketmode"
)

// testTimeout is a generous CI bound for channel receives, never an assertion
// on how long something takes.
const testTimeout = 10 * time.Second

type ackCall struct {
	envelopeID string
	payload    any
}

// fakeRunner plays a script: it emits events (unbuffered, so the hub has
// received each one when the send returns), then either returns err or, with
// block set, waits for its context to end.
type fakeRunner struct {
	script []socketmode.Event
	err    error
	block  bool

	events chan socketmode.Event
	acks   chan ackCall
}

func newFakeRunner(err error, script ...socketmode.Event) *fakeRunner {
	return &fakeRunner{script: script, err: err, events: make(chan socketmode.Event), acks: make(chan ackCall, 16)}
}

func (r *fakeRunner) Run(ctx context.Context) error {
	for _, evt := range r.script {
		select {
		case r.events <- evt:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if r.block {
		<-ctx.Done()
		return ctx.Err()
	}
	return r.err
}

func (r *fakeRunner) Events() <-chan socketmode.Event { return r.events }

func (r *fakeRunner) Ack(_ context.Context, id string, payload any) error {
	r.acks <- ackCall{id, payload}
	return nil
}

// fakeClock never sleeps: After advances Now by the requested delay, reports
// the delay on waits, and fires immediately.
type fakeClock struct {
	mu    sync.Mutex
	now   time.Time
	waits chan time.Duration
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), waits: make(chan time.Duration, 1024)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.advance(d)
	c.waits <- d
	ch := make(chan time.Time, 1)
	ch <- c.Now()
	return ch
}

// recordingHealth records reports; onReport, when set, runs inside the call.
type recordingHealth struct {
	mu       sync.Mutex
	calls    []hostclient.SetHealthStateRequest
	reported chan struct{}
	onReport func()
}

func newRecordingHealth() *recordingHealth {
	return &recordingHealth{reported: make(chan struct{}, 16)}
}

func (h *recordingHealth) SetHealthState(_ context.Context, req hostclient.SetHealthStateRequest) (*hostclient.SetHealthStateResponse, error) {
	h.mu.Lock()
	h.calls = append(h.calls, req)
	h.mu.Unlock()
	if h.onReport != nil {
		h.onReport()
	}
	h.reported <- struct{}{}
	return &hostclient.SetHealthStateResponse{OK: true, Applied: true}, nil
}

func (h *recordingHealth) profiles() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, c := range h.calls {
		out = append(out, c.Profile)
	}
	return out
}

func envelope(typ socketmode.EventType, id string) socketmode.Event {
	return socketmode.Event{Type: typ, Request: &socketmode.Request{Type: string(typ), EnvelopeID: id}}
}

func newTestHub(factory runnerFactory, health healthSetter, declared ...string) (*socketHub, *fakeClock) {
	hub := newSocketHub(factory, health, declared)
	clock := newFakeClock()
	hub.clock = clock
	hub.jitter = func() float64 { return 0 }
	return hub, clock
}

// runHub starts hub.Run and returns a stop func that cancels and joins it.
func runHub(t *testing.T, hub *socketHub) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- hub.Run(ctx) }()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("Run returned %v on shutdown", err)
				}
			case <-time.After(testTimeout):
				t.Error("hub did not stop after cancel")
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

func recvAck(t *testing.T, r *fakeRunner) ackCall {
	t.Helper()
	select {
	case a := <-r.acks:
		return a
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for ack")
		return ackCall{}
	}
}

func TestHubDispatchesByEnvelopeTypeAndAcks(t *testing.T) {
	runner := newFakeRunner(nil,
		envelope(socketmode.EventTypeConnected, ""),
		envelope(socketmode.EventTypeEventsAPI, "e1"),
		envelope(socketmode.EventTypeInteractive, "i1"),
		envelope(socketmode.EventTypeSlashCommand, "s1"),
	)
	runner.block = true
	factoryCalls := 0
	hub, _ := newTestHub(func(context.Context) (socketRunner, error) { factoryCalls++; return runner, nil }, newRecordingHealth())

	var mu sync.Mutex
	seen := map[string]string{}
	handle := func(name string, payload any, err error) socketHandler {
		return func(_ context.Context, evt socketmode.Event) (any, error) {
			mu.Lock()
			seen[name] = evt.Request.EnvelopeID
			mu.Unlock()
			return payload, err
		}
	}
	if err := hub.Handle(EnvelopeEventsAPI, handle("events", nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := hub.Handle(EnvelopeInteractive, handle("interactive", map[string]string{"text": "ok"}, nil)); err != nil {
		t.Fatal(err)
	}
	if err := hub.Handle(EnvelopeSlashCommands, handle("slash", "ignored", errors.New("boom"))); err != nil {
		t.Fatal(err)
	}
	runHub(t, hub)

	acks := map[string]any{}
	for range 3 {
		a := recvAck(t, runner)
		acks[a.envelopeID] = a.payload
	}
	want := map[string]any{"e1": nil, "i1": map[string]string{"text": "ok"}, "s1": nil}
	if !reflect.DeepEqual(acks, want) {
		t.Errorf("acks = %v, want %v (a failing handler still acks, bare)", acks, want)
	}
	mu.Lock()
	defer mu.Unlock()
	wantSeen := map[string]string{"events": "e1", "interactive": "i1", "slash": "s1"}
	if !reflect.DeepEqual(seen, wantSeen) {
		t.Errorf("handlers saw %v, want %v", seen, wantSeen)
	}
	if factoryCalls != 1 {
		t.Errorf("factory called %d times, want one shared connection", factoryCalls)
	}
}

func TestHubAcksEnvelopeWithoutHandler(t *testing.T) {
	runner := newFakeRunner(nil, envelope(socketmode.EventTypeSlashCommand, "s1"))
	runner.block = true
	hub, _ := newTestHub(func(context.Context) (socketRunner, error) { return runner, nil }, newRecordingHealth())
	if err := hub.Handle(EnvelopeEventsAPI, func(context.Context, socketmode.Event) (any, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	runHub(t, hub)

	if a := recvAck(t, runner); a.envelopeID != "s1" || a.payload != nil {
		t.Errorf("ack = %+v, want bare ack of s1", a)
	}
}

func TestHubHandleValidation(t *testing.T) {
	noop := func(context.Context, socketmode.Event) (any, error) { return nil, nil }
	hub, _ := newTestHub(nil, newRecordingHealth())

	if err := hub.Handle("hello", noop); err == nil {
		t.Error("unsupported envelope type accepted")
	}
	if err := hub.Handle(EnvelopeEventsAPI, noop); err != nil {
		t.Fatal(err)
	}
	if err := hub.Handle(EnvelopeEventsAPI, noop); err == nil {
		t.Error("duplicate handler accepted")
	}

	runner := newFakeRunner(nil)
	runner.block = true
	connecting := make(chan struct{})
	hub.factory = func(context.Context) (socketRunner, error) {
		close(connecting)
		return runner, nil
	}
	runHub(t, hub)
	select {
	case <-connecting:
	case <-time.After(testTimeout):
		t.Fatal("hub never connected")
	}
	if err := hub.Handle(EnvelopeInteractive, noop); err == nil {
		t.Error("Handle accepted a registration after Run started")
	}
}

func TestHubInertWithoutHandlers(t *testing.T) {
	hub, _ := newTestHub(func(context.Context) (socketRunner, error) {
		t.Error("factory called although no handler is registered")
		return nil, errors.New("unexpected")
	}, newRecordingHealth())
	if err := hub.Run(context.Background()); err != nil {
		t.Errorf("Run = %v, want nil", err)
	}
}

func TestBackoffBounds(t *testing.T) {
	tests := []struct {
		attempt int
		ceiling time.Duration // un-jittered delay
	}{
		{0, time.Second},
		{1, 2 * time.Second},
		{2, 4 * time.Second},
		{5, 32 * time.Second},
		{6, time.Minute},
		{7, time.Minute},
		{50, time.Minute},
	}
	hub, _ := newTestHub(nil, nil)
	for _, tt := range tests {
		hub.jitter = func() float64 { return 0 }
		if got, want := hub.backoff(tt.attempt), tt.ceiling/2; got != want {
			t.Errorf("attempt %d, jitter 0: %v, want %v", tt.attempt, got, want)
		}
		hub.jitter = func() float64 { return 0.5 }
		if got, want := hub.backoff(tt.attempt), tt.ceiling*3/4; got != want {
			t.Errorf("attempt %d, jitter 0.5: %v, want %v", tt.attempt, got, want)
		}
		hub.jitter = func() float64 { return 0.999999 }
		if got := hub.backoff(tt.attempt); got < tt.ceiling/2 || got >= tt.ceiling {
			t.Errorf("attempt %d, jitter ~1: %v outside [%v, %v)", tt.attempt, got, tt.ceiling/2, tt.ceiling)
		}
	}
}

func TestHubReconnectScheduleResetsAfterConnect(t *testing.T) {
	// Three failed attempts, one that connects and then drops, two more failures.
	scripts := []*fakeRunner{
		newFakeRunner(errors.New("down")),
		newFakeRunner(errors.New("down")),
		newFakeRunner(errors.New("down")),
		newFakeRunner(errors.New("dropped"), envelope(socketmode.EventTypeConnected, "")),
		newFakeRunner(errors.New("down")),
		newFakeRunner(errors.New("down")),
	}
	final := newFakeRunner(nil)
	final.block = true
	hub, clock := newTestHub(nil, newRecordingHealth())
	next := 0
	hub.factory = func(context.Context) (socketRunner, error) {
		if next < len(scripts) {
			next++
			return scripts[next-1], nil
		}
		return final, nil
	}
	if err := hub.Handle(EnvelopeEventsAPI, func(context.Context, socketmode.Event) (any, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	runHub(t, hub)

	want := []time.Duration{
		500 * time.Millisecond, time.Second, 2 * time.Second, // failures before any connection
		500 * time.Millisecond, // connected once: schedule restarts
		time.Second,
		2 * time.Second,
	}
	for i, w := range want {
		select {
		case got := <-clock.waits:
			if got != w {
				t.Errorf("wait %d = %v, want %v", i, got, w)
			}
		case <-time.After(testTimeout):
			t.Fatalf("wait %d never requested", i)
		}
	}
}

func TestHubFactoryErrorBacksOff(t *testing.T) {
	calls := 0
	final := newFakeRunner(nil)
	final.block = true
	hub, clock := newTestHub(func(context.Context) (socketRunner, error) {
		calls++
		if calls == 1 {
			return nil, errNoCredentials
		}
		return final, nil
	}, newRecordingHealth())
	if err := hub.Handle(EnvelopeEventsAPI, func(context.Context, socketmode.Event) (any, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	runHub(t, hub)
	select {
	case got := <-clock.waits:
		if got != 500*time.Millisecond {
			t.Errorf("wait = %v, want 500ms", got)
		}
	case <-time.After(testTimeout):
		t.Fatal("no backoff after factory error")
	}
}

func TestHubReportsUnhealthyOnlyAfterSustainedOutage(t *testing.T) {
	health := newRecordingHealth()
	var clock *fakeClock
	waitsAtReport := -1
	health.onReport = func() {
		if waitsAtReport < 0 {
			waitsAtReport = len(clock.waits)
		}
	}
	hub, c := newTestHub(func(context.Context) (socketRunner, error) {
		return newFakeRunner(errors.New("down")), nil
	}, health, hostclient.ProfileToolProvider, hostclient.ProfileHumanChannel)
	clock = c
	if err := hub.Handle(EnvelopeEventsAPI, func(context.Context, socketmode.Event) (any, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	stop := runHub(t, hub)

	select {
	case <-health.reported:
	case <-time.After(testTimeout):
		t.Fatal("no unhealthy report during a sustained outage")
	}
	// Jitter 0: waits are 0.5,1,2,4,8,16,30,30,30 s = 121.5s, so the 10th
	// failure is the first to satisfy both the count (5) and the 2-minute
	// outage; before it, nine waits were requested.
	if waitsAtReport != 9 {
		t.Errorf("reported after %d waits, want 9", waitsAtReport)
	}
	// Let a few more failures pass: the report is not repeated.
	for range 12 {
		select {
		case <-clock.waits:
		case <-time.After(testTimeout):
			t.Fatal("hub stopped reconnecting")
		}
	}
	stop()

	if got := health.profiles(); !slices.Equal(got, []string{hostclient.ProfileHumanChannel}) {
		t.Errorf("reports = %v, want exactly one for human_channel (event_source and tool_provider are not socket-dependent or not declared)", got)
	}
	health.mu.Lock()
	defer health.mu.Unlock()
	if s := health.calls[0].State; s != hostclient.HealthStateUnhealthy {
		t.Errorf("state = %q, want unhealthy", s)
	}
}

func TestRecordFailureHealthPolicy(t *testing.T) {
	tests := []struct {
		name     string
		declared []string
		failures int
		step     time.Duration // clock advance between failures
		want     []string
	}{
		{"blip: many fast failures", []string{"human_channel"}, 20, time.Second, nil},
		{"few failures over a long time", []string{"human_channel"}, 4, time.Hour, nil},
		{"sustained", []string{"human_channel", "event_source"}, 5, 30 * time.Second, []string{"human_channel", "event_source"}},
		{"only declared profiles", []string{"tool_provider", "event_source"}, 5, 30 * time.Second, []string{"event_source"}},
		{"nothing declared that depends on sockets", []string{"tool_provider"}, 5, 30 * time.Second, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			health := newRecordingHealth()
			hub, clock := newTestHub(nil, health, tt.declared...)
			for range tt.failures {
				hub.recordFailure(context.Background(), errors.New("down"))
				clock.advance(tt.step)
			}
			if got := health.profiles(); !slices.Equal(got, tt.want) {
				t.Errorf("reports = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRecordConnectedEndsOutage(t *testing.T) {
	health := newRecordingHealth()
	hub, clock := newTestHub(nil, health, "human_channel")
	for range 4 {
		hub.recordFailure(context.Background(), errors.New("down"))
		clock.advance(time.Minute)
	}
	hub.recordConnected()
	hub.recordFailure(context.Background(), errors.New("down"))
	if got := health.profiles(); len(got) != 0 {
		t.Errorf("reports = %v, want none: a reconnect resets the outage", got)
	}
}

func TestSocketDialerHonoursProxyEnvironment(t *testing.T) {
	d := newSocketDialer()
	if d.Proxy == nil {
		t.Fatal("dialer has no Proxy; HTTP(S)_PROXY from the egress proxy would be ignored")
	}
	// ProxyFromEnvironment snapshots the environment once per process, so the
	// function identity is what can be asserted without ordering hazards.
	if reflect.ValueOf(d.Proxy).Pointer() != reflect.ValueOf(http.ProxyFromEnvironment).Pointer() {
		t.Error("dialer Proxy is not http.ProxyFromEnvironment")
	}
	if d.HandshakeTimeout != socketHandshakeTimeout || d.WriteBufferSize != socketWriteBufferSize {
		t.Errorf("dialer timeouts/buffer not set: %v / %d", d.HandshakeTimeout, d.WriteBufferSize)
	}
}

type fakeConfig struct {
	json string
	err  error
}

func (f fakeConfig) GetInstanceConfig(context.Context) (*hostclient.GetInstanceConfigResponse, error) {
	return &hostclient.GetInstanceConfigResponse{ConfigJSON: f.json}, f.err
}

func TestAppToken(t *testing.T) {
	tests := []struct {
		name    string
		cfg     fakeConfig
		want    string
		wantErr error
	}{
		{"present", fakeConfig{json: `{"app_token":"xapp-1"}`}, "xapp-1", nil},
		{"empty config", fakeConfig{json: ""}, "", errNoAppToken},
		{"no field", fakeConfig{json: `{}`}, "", errNoAppToken},
		{"blank", fakeConfig{json: `{"app_token":""}`}, "", errNoAppToken},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := appToken(context.Background(), tt.cfg)
			if got != tt.want || !errors.Is(err, tt.wantErr) {
				t.Errorf("appToken = (%q, %v), want (%q, %v)", got, err, tt.want, tt.wantErr)
			}
		})
	}
	if _, err := appToken(context.Background(), fakeConfig{json: "{"}); err == nil {
		t.Error("malformed config accepted")
	}
	if _, err := appToken(context.Background(), fakeConfig{err: errors.New("host down")}); err == nil {
		t.Error("host error swallowed")
	}
}

func TestRunSocketHubSkipsWithoutAppToken(t *testing.T) {
	hub, _ := newTestHub(func(context.Context) (socketRunner, error) {
		t.Error("factory called without an app token")
		return nil, errors.New("unexpected")
	}, newRecordingHealth())
	if err := hub.Handle(EnvelopeEventsAPI, func(context.Context, socketmode.Event) (any, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	runSocketHub(context.Background(), hub, fakeConfig{json: `{}`})
}

func TestDeclaredProfilesMatchManifest(t *testing.T) {
	m := loadManifest(t)
	if got, want := declaredProfiles, m.Gleipnir.Profiles.Declared(); !slices.Equal(got, want) {
		t.Errorf("declaredProfiles = %v, manifest declares %v", got, want)
	}
}
