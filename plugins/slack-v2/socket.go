package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"

	"github.com/felag-engineering/gleipnir/plugin-sdk/hostclient"
	"github.com/gorilla/websocket"
	"github.com/slack-go/slack/socketmode"
)

// Envelope types a handler can be registered for. These are the Socket Mode
// envelope types Slack delivers payloads in; hello and disconnect are
// connection housekeeping and never reach a handler.
const (
	EnvelopeEventsAPI     = socketmode.RequestTypeEventsAPI
	EnvelopeInteractive   = socketmode.RequestTypeInteractive
	EnvelopeSlashCommands = socketmode.RequestTypeSlashCommands
)

// Reconnect and health policy. See socketHub.recordFailure for why health is
// gated on both a failure count and an outage duration.
const (
	reconnectBaseDelay = time.Second
	reconnectMaxDelay  = time.Minute

	// unhealthyAfterFailures and unhealthyAfterOutage must BOTH be met before
	// a capability is reported unhealthy.
	unhealthyAfterFailures = 5
	unhealthyAfterOutage   = 2 * time.Minute

	socketHandshakeTimeout = 45 * time.Second
	// socketWriteBufferSize matches slack-go's own dialer: Slack drops
	// responses split across continuation frames, so it must exceed Slack's
	// 20KB response limit.
	socketWriteBufferSize = 32 * 1024
)

// socketHandler handles one Socket Mode envelope. The returned payload is sent
// back in the ack (nil for a bare ack; slash commands and some interactive
// payloads may answer in it). Slack requires the ack within 3 seconds, and the
// hub acks only after the handler returns, so a handler that needs longer must
// hand the work off and return.
//
// A handler error is logged and the envelope is still acked: an unacked
// envelope is redelivered by Slack, and a redelivery would fail the same way.
type socketHandler func(ctx context.Context, evt socketmode.Event) (ackPayload any, err error)

// socketRunner is the narrow slice of *socketmode.Client the hub drives, so
// tests can supply a fake without a network.
type socketRunner interface {
	// Run blocks until the connection is lost for good or ctx is done.
	Run(ctx context.Context) error
	Events() <-chan socketmode.Event
	Ack(ctx context.Context, envelopeID string, payload any) error
}

// runnerFactory builds a runner for one connection attempt. It is called per
// attempt so rotated tokens are picked up on reconnect.
type runnerFactory func(ctx context.Context) (socketRunner, error)

// hubClock is the hub's source of time; tests drive it by hand.
type hubClock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// socketHub owns the plugin's single Socket Mode connection and fans its
// envelopes out to registered handlers. One hub per process: every consumer
// (channel buttons, authorize_actor clicks, the event source) shares the
// connection by calling Handle before Run.
type socketHub struct {
	factory  runnerFactory
	health   healthSetter
	profiles []string
	clock    hubClock
	// jitter returns a value in [0,1).
	jitter func() float64

	mu       sync.Mutex
	handlers map[string]socketHandler
	started  bool

	// Touched only by the Run goroutine.
	failures    int
	outageSince time.Time
	reported    map[string]bool
	wg          sync.WaitGroup
}

// healthProfiles are the capability profiles the hub can mark unhealthy when
// the connection is lost: the ones that depend on Socket Mode.
var healthProfiles = []string{hostclient.ProfileHumanChannel, hostclient.ProfileEventSource}

// newSocketHub creates a hub. declared is the set of profiles the plugin's
// manifest declares; the hub reports health only for those of its own
// dependent profiles (human_channel, event_source) that appear in it, because
// the host rejects a report for a capability the plugin never declared.
func newSocketHub(factory runnerFactory, health healthSetter, declared []string) *socketHub {
	var profiles []string
	for _, p := range healthProfiles {
		for _, d := range declared {
			if p == d {
				profiles = append(profiles, p)
				break
			}
		}
	}
	return &socketHub{
		factory:  factory,
		health:   health,
		profiles: profiles,
		clock:    realClock{},
		jitter:   rand.Float64,
		handlers: make(map[string]socketHandler),
		reported: make(map[string]bool),
	}
}

// Handle registers the handler for an envelope type. It must be called before
// Run, and each type has at most one handler.
func (h *socketHub) Handle(envelopeType string, handler socketHandler) error {
	switch envelopeType {
	case EnvelopeEventsAPI, EnvelopeInteractive, EnvelopeSlashCommands:
	default:
		return fmt.Errorf("unsupported envelope type %q", envelopeType)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.started {
		return errors.New("handlers must be registered before the hub runs")
	}
	if _, exists := h.handlers[envelopeType]; exists {
		return fmt.Errorf("envelope type %q already has a handler", envelopeType)
	}
	h.handlers[envelopeType] = handler
	return nil
}

// Run connects and serves envelopes until ctx is done, reconnecting with
// bounded exponential backoff. It returns nil on shutdown, and immediately
// (inert) when no handler is registered, since an idle Socket Mode connection
// would only consume one of the app's limited connection slots.
func (h *socketHub) Run(ctx context.Context) error {
	h.mu.Lock()
	h.started = true
	idle := len(h.handlers) == 0
	h.mu.Unlock()
	if idle {
		log.Print("slack-v2: socket mode: no handlers registered; not connecting")
		return nil
	}
	defer h.wg.Wait()

	attempt := 0
	for {
		connected, err := h.runOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if connected {
			attempt = 0
		}
		h.recordFailure(ctx, err)

		delay := h.backoff(attempt)
		attempt++
		log.Printf("slack-v2: socket mode: connection lost (%v); reconnecting in %s", err, delay)
		select {
		case <-h.clock.After(delay):
		case <-ctx.Done():
			return nil
		}
	}
}

// backoff is full exponential growth capped at reconnectMaxDelay, with the
// upper half jittered: the result lies in [d/2, d). Jitter keeps a fleet of
// instances from reconnecting in lockstep after a Slack-side outage; the floor
// keeps a lucky draw from collapsing into a hot retry loop.
func (h *socketHub) backoff(attempt int) time.Duration {
	d := reconnectMaxDelay
	if attempt < 6 { // reconnectBaseDelay<<6 already exceeds the cap
		d = min(reconnectBaseDelay<<attempt, reconnectMaxDelay)
	}
	return d/2 + time.Duration(float64(d/2)*h.jitter())
}

// runOnce makes one connection attempt and serves it until it ends. connected
// reports whether the connection was ever established.
func (h *socketHub) runOnce(ctx context.Context) (connected bool, err error) {
	runner, err := h.factory(ctx)
	if err != nil {
		return false, fmt.Errorf("build socket mode client: %w", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runner.Run(runCtx) }()

	for {
		select {
		case evt := <-runner.Events():
			if evt.Type == socketmode.EventTypeConnected {
				connected = true
				h.recordConnected()
				continue
			}
			h.handleEvent(ctx, runner, evt)
		case runErr := <-done:
			if runErr == nil {
				runErr = errors.New("socket mode client stopped")
			}
			return connected, runErr
		}
	}
}

func (h *socketHub) handleEvent(ctx context.Context, runner socketRunner, evt socketmode.Event) {
	switch evt.Type {
	case socketmode.EventTypeConnectionError, socketmode.EventTypeInvalidAuth:
		// slack-go retries these itself; they still count toward the outage.
		h.recordFailure(ctx, fmt.Errorf("socket mode %s", evt.Type))
	case socketmode.EventTypeEventsAPI, socketmode.EventTypeInteractive, socketmode.EventTypeSlashCommand:
		if evt.Request == nil {
			return
		}
		h.mu.Lock()
		handler := h.handlers[string(evt.Type)]
		h.mu.Unlock()
		h.wg.Add(1)
		go func() {
			defer h.wg.Done()
			h.dispatch(ctx, runner, handler, evt)
		}()
	}
}

// dispatch runs the handler for one envelope and acks it. An envelope type
// with no handler is acked bare so Slack does not redeliver it.
func (h *socketHub) dispatch(ctx context.Context, runner socketRunner, handler socketHandler, evt socketmode.Event) {
	var payload any
	if handler != nil {
		var err error
		payload, err = handler(ctx, evt)
		if err != nil {
			log.Printf("slack-v2: socket mode: %s handler: %v", evt.Type, err)
			payload = nil
		}
	}
	if err := runner.Ack(ctx, evt.Request.EnvelopeID, payload); err != nil {
		log.Printf("slack-v2: socket mode: ack %s envelope: %v", evt.Type, err)
	}
}

func (h *socketHub) recordConnected() {
	h.failures = 0
	h.outageSince = time.Time{}
}

// recordFailure counts one failed attempt and, once the outage is sustained,
// reports the dependent capability profiles unhealthy.
//
// Health policy: the host treats a self-reported fault as worsen-only and
// clears it only when it observes this container restart (hostendpoint
// set_health_state, caphealth.SourceSelfReport). A "healthy" report after
// reconnecting would be answered applied=false, so it is never sent, and an
// unhealthy report is effectively permanent until restart. A transient blip
// (Slack rotates connections every few hours, and reconnects within seconds)
// must therefore not report. Only a sustained outage does: at least
// unhealthyAfterFailures consecutive failures spanning at least
// unhealthyAfterOutage. If the connection later recovers the capability stays
// marked until restart; that is a deliberate trade for a signal that is cheap
// to be wrong about only when it fires rarely.
func (h *socketHub) recordFailure(ctx context.Context, cause error) {
	now := h.clock.Now()
	if h.outageSince.IsZero() {
		h.outageSince = now
	}
	h.failures++
	if h.failures < unhealthyAfterFailures || now.Sub(h.outageSince) < unhealthyAfterOutage {
		return
	}
	for _, profile := range h.profiles {
		if h.reported[profile] {
			continue
		}
		_, err := h.health.SetHealthState(ctx, hostclient.SetHealthStateRequest{
			Profile: profile,
			State:   hostclient.HealthStateUnhealthy,
			Detail:  fmt.Sprintf("Slack Socket Mode connection down for %s after %d attempts: %v", now.Sub(h.outageSince).Round(time.Second), h.failures, cause),
		})
		if err != nil {
			// Not remembered, so the next failure retries.
			log.Printf("slack-v2: report %s unhealthy: %v", profile, err)
			continue
		}
		h.reported[profile] = true
	}
}

// newSocketDialer returns the websocket dialer for the Socket Mode
// connection. The plugin container reaches Slack only through the egress
// proxy named in HTTP(S)_PROXY, so the dialer must honour it. slack-go falls
// back to an equivalent dialer when none is given, but depending on that
// default silently would let a future library change drop the proxy, so it is
// set explicitly (and tested). The apps.connections.open call is a plain
// HTTP request and follows the same environment through http.DefaultTransport.
func newSocketDialer() *websocket.Dialer {
	return &websocket.Dialer{
		Proxy:            http.ProxyFromEnvironment,
		HandshakeTimeout: socketHandshakeTimeout,
		WriteBufferSize:  socketWriteBufferSize,
	}
}

// clientRunner adapts *socketmode.Client to socketRunner.
type clientRunner struct{ client *socketmode.Client }

func (r clientRunner) Run(ctx context.Context) error   { return r.client.RunContext(ctx) }
func (r clientRunner) Events() <-chan socketmode.Event { return r.client.Events }
func (r clientRunner) Ack(ctx context.Context, id string, payload any) error {
	return r.client.AckCtx(ctx, id, payload)
}

// instanceConfigSource is the slice of hostclient.Client that reads config.
type instanceConfigSource interface {
	GetInstanceConfig(ctx context.Context) (*hostclient.GetInstanceConfigResponse, error)
}

// errNoAppToken means the instance has no Socket Mode app-level token, so the
// hub has nothing to connect with.
var errNoAppToken = errors.New("no app_token configured")

// appToken reads the Socket Mode app-level token from instance config.
func appToken(ctx context.Context, cfg instanceConfigSource) (string, error) {
	resp, err := cfg.GetInstanceConfig(ctx)
	if err != nil {
		return "", fmt.Errorf("get instance config: %w", err)
	}
	if resp.ConfigJSON == "" {
		return "", errNoAppToken
	}
	var parsed struct {
		AppToken string `json:"app_token"`
	}
	if err := json.Unmarshal([]byte(resp.ConfigJSON), &parsed); err != nil {
		return "", fmt.Errorf("parse instance config: %w", err)
	}
	if parsed.AppToken == "" {
		return "", errNoAppToken
	}
	return parsed.AppToken, nil
}

// newClientRunnerFactory builds real Socket Mode clients from the instance's
// app token and bot token, re-reading both on every attempt.
func newClientRunnerFactory(cfg instanceConfigSource, provider *slackProvider) runnerFactory {
	return func(ctx context.Context) (socketRunner, error) {
		token, err := appToken(ctx, cfg)
		if err != nil {
			return nil, err
		}
		api, err := provider.socketAPI(ctx, token)
		if err != nil {
			return nil, err
		}
		return clientRunner{client: socketmode.New(api, socketmode.OptionDialer(newSocketDialer()))}, nil
	}
}

// runSocketHub runs the hub until ctx is done, unless there is nothing to
// connect for: no registered handler, or no app token (logged once; the tools
// keep working without Socket Mode).
func runSocketHub(ctx context.Context, hub *socketHub, cfg instanceConfigSource) {
	if !hub.hasHandlers() {
		log.Print("slack-v2: socket mode: no handlers registered; not connecting")
		return
	}
	if _, err := appToken(ctx, cfg); err != nil {
		log.Printf("slack-v2: socket mode disabled: %v", err)
		return
	}
	if err := hub.Run(ctx); err != nil {
		log.Printf("slack-v2: socket mode: %v", err)
	}
}

func (h *socketHub) hasHandlers() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.handlers) > 0
}
