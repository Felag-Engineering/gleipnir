package events

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/infra/event"
	"github.com/felag-engineering/gleipnir/internal/infra/metrics"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/caphealth"
	sdkmanifest "github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
)

// Event carries a single events/listen-delivered event through the host
// dispatch pipeline. Deliberately package-local rather than the shared
// internal/plugin/trigger.Event type — see the package doc for why this
// package must not import internal/plugin/trigger. The trigger package's own
// listen_sink.go converts one to the other.
type Event struct {
	InstanceID  string
	PluginID    string
	EventKind   string
	EventID     string
	PayloadJSON []byte
	ObservedAt  time.Time
}

// Sink is the narrow interface the Supervisor hands each received event to.
// In production it is satisfied by internal/plugin/trigger's
// ListenSinkAdapter, which converts Event and forwards to *Dispatcher.Handle
// — the same dedup → GetSubscribedActivePolicies → binding evaluate →
// RunLauncher.LaunchWithConcurrency pipeline the v1.1 gRPC trigger stream
// already uses.
type Sink interface {
	Handle(ctx context.Context, e Event) error
}

// EventStream is the narrow interface the Supervisor needs from an open
// events/listen connection: deliver the next event or terminal sentinel, and
// release the connection. *mcp.EventStream satisfies this structurally.
type EventStream interface {
	Next(ctx context.Context) (mcp.CloudEvent, error)
	Close() error
}

// StreamOpener opens one events/listen stream. *mcp.Client satisfies this
// (via the mcpStreamOpener adapter) once resolved for a plugin instance.
type StreamOpener interface {
	ListenEvents(ctx context.Context, p mcp.EventsListenParams) (EventStream, error)
}

// StreamResolver resolves a plugin instance to a StreamOpener capable of
// opening its events/listen stream. Production wires managedStreamResolver
// (InstanceServerLookup + ClientResolver, the same split discoverprobe.go
// uses so events/listen and events/discover resolve a managed instance's MCP
// endpoint identically); tests inject a fake directly via
// Config.TestStreamResolver.
type StreamResolver interface {
	ResolveStream(ctx context.Context, instanceID string) (StreamOpener, error)
}

// managedStreamResolver is the production StreamResolver: resolve the
// instance's managed mcp_servers row, then a ready *mcp.Client for it.
type managedStreamResolver struct {
	servers InstanceServerLookup
	clients ClientResolver
}

func (r *managedStreamResolver) ResolveStream(ctx context.Context, instanceID string) (StreamOpener, error) {
	srv, err := r.servers.GetMCPServerByPluginInstance(ctx, &instanceID)
	if err != nil {
		return nil, fmt.Errorf("resolve managed endpoint for instance %s: %w", instanceID, err)
	}
	client, err := r.clients.ClientForServerID(ctx, srv.ID)
	if err != nil {
		return nil, fmt.Errorf("resolve mcp client for instance %s: %w", instanceID, err)
	}
	return mcpStreamOpener{c: client}, nil
}

// mcpStreamOpener adapts *mcp.Client to StreamOpener.
type mcpStreamOpener struct {
	c *mcp.Client
}

// ListenEvents primes the client with a fresh server/discover round trip
// before opening the stream. This is what populates the client's declared
// io.gleipnir/events heartbeat (mcp.Client.eventsHeartbeatInterval) so
// EventStream.Next honors the server's actual declared cadence rather than
// the package-private 30s fallback every connect would otherwise silently
// fall back to when nothing else has probed this client first — the
// caphealth prober is wired-but-not-started (see doc.go), so nothing else in
// the live system currently does this priming.
func (o mcpStreamOpener) ListenEvents(ctx context.Context, p mcp.EventsListenParams) (EventStream, error) {
	if _, err := o.c.ProbeProtocolVersion(ctx); err != nil {
		return nil, fmt.Errorf("prime server/discover before events/listen: %w", err)
	}
	return o.c.ListenEvents(ctx, p)
}

// CapabilityChecker is the narrow interface the Supervisor uses to consult
// caphealth before opening a stream. *caphealth.Registry satisfies it.
type CapabilityChecker interface {
	Serves(instanceID string, c caphealth.Capability) bool
}

// PluginQuerier is the narrow DB interface the Supervisor needs to enumerate
// active plugins/instances and read the state each connect attempt depends
// on. Named distinctly from cursor.go's Querier (the durable-cursor DB
// interface) to avoid a name collision within this package. Mirrors
// internal/plugin/trigger.Supervisor's own Querier, widened with
// GetPluginByID so the manifest's attested event kinds can be resolved
// per-instance.
type PluginQuerier interface {
	ListPluginsByStatus(ctx context.Context, status string) ([]db.Plugin, error)
	ListPluginInstancesByPlugin(ctx context.Context, pluginID string) ([]db.PluginInstance, error)
	GetPluginInstanceByID(ctx context.Context, id string) (db.PluginInstance, error)
	GetPluginByID(ctx context.Context, id string) (db.Plugin, error)
}

const (
	defaultBackoffInitial = time.Second
	defaultBackoffMax     = 60 * time.Second
	defaultUnhealthyAfter = 5
)

// package-level Prometheus collectors, registered once at import time —
// mirrors internal/plugin/dedup/store.go's promauto.With(metrics.Registry())
// pattern so tests cannot trigger double-registration panics.
var (
	eventsReceived = promauto.With(metrics.Registry()).NewCounterVec(
		prometheus.CounterOpts{
			Name: "gleipnir_plugin_events_received_total",
			Help: "Events delivered over an events/listen stream, before dedup.",
		},
		[]string{metrics.LabelPlugin, metrics.LabelInstance, "event_kind"},
	)

	streamReconnects = promauto.With(metrics.Registry()).NewCounterVec(
		prometheus.CounterOpts{
			Name: "gleipnir_plugin_events_stream_reconnects_total",
			Help: "events/listen stream reconnect attempts, after the first connect.",
		},
		[]string{metrics.LabelPlugin, metrics.LabelInstance},
	)

	streamUp = promauto.With(metrics.Registry()).NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gleipnir_plugin_events_stream_up",
			Help: "1 while an instance's events/listen stream is connected, 0 otherwise.",
		},
		[]string{metrics.LabelPlugin, metrics.LabelInstance},
	)
)

// Config holds all dependencies for a Supervisor.
type Config struct {
	// Querier enumerates active plugins/instances and reads per-instance
	// state (subscription scope, manifest).
	Querier PluginQuerier

	// Servers and Clients resolve a plugin instance to its managed MCP
	// endpoint, mirroring discoverprobe.go's split. Ignored when
	// TestStreamResolver is set.
	Servers InstanceServerLookup
	Clients ClientResolver

	// TestStreamResolver overrides Servers/Clients for unit tests so a test
	// can inject a fake stream without an HTTP server. Production callers
	// must leave this nil.
	TestStreamResolver StreamResolver

	// Capability gates each connect attempt on caphealth's routing verdict
	// for the event_source profile. Optional — nil skips the gate, which
	// tests that do not exercise caphealth wiring rely on.
	Capability CapabilityChecker

	// Cursor is the durable resume-point store (cursor.go). Defaults to
	// Noop{} when nil, matching internal/plugin/dedup's zero-value posture.
	Cursor Store

	// Sink receives every delivered event, synchronously, after which the
	// cursor is advanced. Required.
	Sink Sink

	// Publisher emits plugin.event_emitted onto the SSE bus, matching
	// hostsvc.EmitEvent's payload keys. Optional — nil means no publish.
	Publisher event.Publisher

	// HealthSetter mirrors trigger.Supervisor.Config.HealthSetter.
	HealthSetter func(ctx context.Context, instanceID string, target model.PluginHealthState, detail string)

	// RootCtx is the long-lived parent context for all stream goroutines.
	// See trigger.Supervisor.Config.RootCtx's doc for why per-call ctx must
	// never parent a stream goroutine (#401): the same reasoning applies
	// here — Restart is reachable from a request handler whose ctx dies at
	// response flush. Defaults to context.Background() when nil so tests
	// that omit it still compile; production callers must set it.
	RootCtx context.Context

	// Logger is the base logger. If nil, slog.Default() is used.
	Logger *slog.Logger

	// BackoffInitial, BackoffMax, UnhealthyAfter mirror
	// trigger.Supervisor.Config's fields exactly, including defaults.
	BackoffInitial time.Duration
	BackoffMax     time.Duration
	UnhealthyAfter int
}

// Supervisor manages per-instance events/listen stream goroutines, mirroring
// internal/plugin/trigger.Supervisor's public surface exactly
// (StartAll/Start/Stop/Restart/StopAll) so the two run side by side under
// the same operational model during the ADR-053 realignment.
//
// All public methods are safe for concurrent use.
type Supervisor struct {
	cfg            Config
	rootCtx        context.Context
	streamResolver StreamResolver
	capability     CapabilityChecker
	cursor         Store
	sink           Sink

	mu        sync.Mutex
	instances map[string]context.CancelFunc
	done      map[string]chan struct{}
}

// NewSupervisor constructs a Supervisor with the given config, applying
// defaults for zero-value fields.
func NewSupervisor(cfg Config) *Supervisor {
	if cfg.BackoffInitial == 0 {
		cfg.BackoffInitial = defaultBackoffInitial
	}
	if cfg.BackoffMax == 0 {
		cfg.BackoffMax = defaultBackoffMax
	}
	if cfg.UnhealthyAfter == 0 {
		cfg.UnhealthyAfter = defaultUnhealthyAfter
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Cursor == nil {
		cfg.Cursor = Noop{}
	}

	rootCtx := cfg.RootCtx
	if rootCtx == nil {
		rootCtx = context.Background()
	}

	var resolver StreamResolver
	switch {
	case cfg.TestStreamResolver != nil:
		resolver = cfg.TestStreamResolver
	case cfg.Servers != nil && cfg.Clients != nil:
		resolver = &managedStreamResolver{servers: cfg.Servers, clients: cfg.Clients}
	}

	return &Supervisor{
		cfg:            cfg,
		rootCtx:        rootCtx,
		streamResolver: resolver,
		capability:     cfg.Capability,
		cursor:         cfg.Cursor,
		sink:           cfg.Sink,
		instances:      make(map[string]context.CancelFunc),
		done:           make(map[string]chan struct{}),
	}
}

// StartAll enumerates active plugins via the DB Querier, skips plugins whose
// manifest declares no event kinds, and spawns one stream goroutine per
// instance via Start. Intended to be called once at boot:
//
//	go eventsSupervisor.StartAll(ctx)
func (s *Supervisor) StartAll(ctx context.Context) error {
	plugins, err := s.cfg.Querier.ListPluginsByStatus(ctx, string(model.PluginStatusActive))
	if err != nil {
		return err
	}

	for _, p := range plugins {
		kinds, parseErr := parseManifestKinds(p.ManifestSnapshot)
		if parseErr != nil {
			s.cfg.Logger.Warn("events supervisor: failed to parse manifest snapshot; skipping plugin",
				"plugin_id", p.ID, "err", parseErr)
			continue
		}
		if len(kinds) == 0 {
			// Plugin declares no event kinds — nothing to listen for.
			continue
		}

		instances, err := s.cfg.Querier.ListPluginInstancesByPlugin(ctx, p.ID)
		if err != nil {
			s.cfg.Logger.Warn("events supervisor: failed to list instances for plugin; skipping",
				"plugin_id", p.ID, "err", err)
			continue
		}
		for _, inst := range instances {
			s.Start(ctx, inst.ID)
		}
	}
	return nil
}

// Start spawns a stream goroutine for instanceID if one is not already
// running. See trigger.Supervisor.Start's doc for why the ctx parameter is
// accepted but not used to parent the goroutine (#401) — the same reasoning
// applies verbatim here.
func (s *Supervisor) Start(_ context.Context, instanceID string) {
	s.mu.Lock()
	if _, alreadyRunning := s.instances[instanceID]; alreadyRunning {
		s.mu.Unlock()
		return
	}
	streamCtx, cancel := context.WithCancel(s.rootCtx)
	doneCh := make(chan struct{})
	s.instances[instanceID] = cancel
	s.done[instanceID] = doneCh
	s.mu.Unlock()

	go s.streamLoop(streamCtx, instanceID, doneCh)
}

// Stop cancels the stream goroutine for instanceID and waits for it to exit.
// No-op if instanceID is not supervised.
func (s *Supervisor) Stop(instanceID string) {
	s.mu.Lock()
	cancel, ok := s.instances[instanceID]
	doneCh := s.done[instanceID]
	if ok {
		delete(s.instances, instanceID)
		delete(s.done, instanceID)
	}
	s.mu.Unlock()

	if !ok {
		return
	}
	cancel()
	<-doneCh
}

// Restart stops the existing stream goroutine for instanceID (if any) and
// starts a fresh one, which re-fetches the instance's subscription scope and
// attested kinds from the DB. No-op if instanceID is not currently
// supervised. See trigger.Supervisor.Restart's doc for the lock-discipline
// and #401 per-call-ctx reasoning — identical here.
func (s *Supervisor) Restart(ctx context.Context, instanceID string) {
	s.mu.Lock()
	oldCancel, ok := s.instances[instanceID]
	oldDone := s.done[instanceID]
	if ok {
		delete(s.instances, instanceID)
		delete(s.done, instanceID)
	}
	s.mu.Unlock()

	if !ok {
		return
	}
	oldCancel()
	<-oldDone

	s.Start(ctx, instanceID)
}

// StopAll cancels every running stream goroutine and waits for all of them
// to exit. Intended to be called from the host shutdown path.
func (s *Supervisor) StopAll() {
	s.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(s.instances))
	dones := make([]chan struct{}, 0, len(s.done))
	for _, cancel := range s.instances {
		cancels = append(cancels, cancel)
	}
	for _, ch := range s.done {
		dones = append(dones, ch)
	}
	s.instances = make(map[string]context.CancelFunc)
	s.done = make(map[string]chan struct{})
	s.mu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
	for _, ch := range dones {
		<-ch
	}
}

// streamEndReason classifies why recvLoop returned, so streamLoop knows
// whether to back off and count a failure, or reconnect immediately.
type streamEndReason int

const (
	endShutdown streamEndReason = iota
	endCleanClose
	endFailure
)

// streamLoop is the goroutine body for a single instance's events/listen
// stream. Per-connection sequence (spec §5, doc §7):
//
//  1. Consult CapabilityChecker.Serves(event_source) — not a failure when it
//     refuses, just a wait, mirroring trigger.Supervisor's scope gate.
//  2. Resolve the instance's attested event kinds from its manifest. Empty
//     kinds is the same kind of wait as an unserved capability.
//  3. Compute scopeHash from (sorted kinds, subscription scope JSON),
//     recomputed on every connect attempt so an operator scope change
//     resets the cursor with no special code path — a scope change simply
//     produces a hash Store.Load has never seen, which reports "no cursor"
//     by construction (cursor.go's documented Load contract).
//  4. Load the stored cursor for (instanceID, scopeHash).
//  5. Open the stream.
//  6. Drain events synchronously via recvLoop, exactly the reasoning
//     trigger.Supervisor.recvLoop's doc gives for its own synchronous
//     dispatch: blocking until Sink.Handle completes provides back-pressure
//     to the plugin and preserves per-stream ordering.
func (s *Supervisor) streamLoop(ctx context.Context, instanceID string, doneCh chan struct{}) {
	defer close(doneCh)

	log := s.cfg.Logger.With("instance_id", instanceID)

	consecutive := 0
	markedUnhealthy := false
	loggedGate := false
	initialHealthSet := false
	attempted := false

	for {
		if ctx.Err() != nil {
			return
		}

		if s.streamResolver == nil {
			// No resolver configured — cannot open a stream. Exit, mirroring
			// trigger.Supervisor's nil-lookup posture.
			return
		}

		if s.capability != nil && !s.capability.Serves(instanceID, caphealth.Capability{Profile: caphealth.ProfileEventSource}) {
			if !loggedGate {
				log.InfoContext(ctx, "events supervisor: capability not currently serving event_source; waiting")
				loggedGate = true
			}
			if !s.sleep(ctx, s.cfg.BackoffMax) {
				return
			}
			continue
		}
		loggedGate = false

		dbInst, err := s.cfg.Querier.GetPluginInstanceByID(ctx, instanceID)
		if err != nil {
			log.WarnContext(ctx, "events supervisor: failed to fetch instance; will retry",
				"err", err, "consecutive", consecutive)
			if !s.sleep(ctx, s.backoff(consecutive)) {
				return
			}
			consecutive++
			s.maybeMarkUnhealthy(ctx, instanceID, consecutive, &markedUnhealthy)
			continue
		}

		plugin, err := s.cfg.Querier.GetPluginByID(ctx, dbInst.PluginID)
		if err != nil {
			log.WarnContext(ctx, "events supervisor: failed to fetch plugin; will retry",
				"err", err, "consecutive", consecutive)
			if !s.sleep(ctx, s.backoff(consecutive)) {
				return
			}
			consecutive++
			s.maybeMarkUnhealthy(ctx, instanceID, consecutive, &markedUnhealthy)
			continue
		}

		kinds, err := parseManifestKinds(plugin.ManifestSnapshot)
		if err != nil {
			log.WarnContext(ctx, "events supervisor: failed to parse manifest; will retry",
				"err", err, "consecutive", consecutive)
			if !s.sleep(ctx, s.backoff(consecutive)) {
				return
			}
			consecutive++
			s.maybeMarkUnhealthy(ctx, instanceID, consecutive, &markedUnhealthy)
			continue
		}
		if len(kinds) == 0 {
			if !loggedGate {
				log.InfoContext(ctx, "events supervisor: plugin declares no event kinds; waiting")
				loggedGate = true
			}
			if !s.sleep(ctx, s.cfg.BackoffMax) {
				return
			}
			continue
		}
		loggedGate = false

		hash := scopeHash(kinds, dbInst.SubscriptionScopeJson)

		cursor, _, err := s.cursor.Load(ctx, instanceID, hash)
		if err != nil {
			log.WarnContext(ctx, "events supervisor: failed to load cursor; will retry",
				"err", err, "consecutive", consecutive)
			if !s.sleep(ctx, s.backoff(consecutive)) {
				return
			}
			consecutive++
			s.maybeMarkUnhealthy(ctx, instanceID, consecutive, &markedUnhealthy)
			continue
		}

		opener, err := s.streamResolver.ResolveStream(ctx, instanceID)
		if err != nil {
			log.WarnContext(ctx, "events supervisor: failed to resolve stream endpoint; will retry",
				"err", err, "consecutive", consecutive)
			if !s.sleep(ctx, s.backoff(consecutive)) {
				return
			}
			consecutive++
			s.maybeMarkUnhealthy(ctx, instanceID, consecutive, &markedUnhealthy)
			continue
		}

		var scope json.RawMessage
		if dbInst.SubscriptionScopeJson != "" {
			scope = json.RawMessage(dbInst.SubscriptionScopeJson)
		}

		if attempted {
			streamReconnects.WithLabelValues(dbInst.PluginID, instanceID).Inc()
		}
		attempted = true

		stream, err := opener.ListenEvents(ctx, mcp.EventsListenParams{
			Kinds:  kinds,
			Scope:  scope,
			Cursor: cursor,
		})
		if err != nil {
			// A cursor-unknown refusal (doc §7.2, mcp.ErrEventsCursorUnknown)
			// is not a failure of the SERVER — it is the server honestly
			// reporting its buffer cannot bridge the gap our stored cursor
			// names (a restarted in-memory buffer being the ordinary cause).
			// The recovery the contract prescribes is: reset the stored
			// cursor and reconnect from empty, paying the redelivery cost
			// plugin_event_dedup absorbs. Retrying the SAME cursor on a
			// backoff loop would never converge, and counting it toward
			// UnhealthyAfter would mark a healthy plugin unhealthy for the
			// host's own stale cursor.
			if errors.Is(err, mcp.ErrEventsCursorUnknown) {
				log.WarnContext(ctx, "events supervisor: server cannot satisfy stored cursor; resetting and reconnecting from empty",
					"cursor", cursor, "err", err)
				if resetErr := s.cursor.Reset(ctx, instanceID); resetErr != nil {
					log.WarnContext(ctx, "events supervisor: cursor reset failed; will retry",
						"err", resetErr)
					if !s.sleep(ctx, s.backoff(consecutive)) {
						return
					}
					consecutive++
					continue
				}
				continue
			}
			log.WarnContext(ctx, "events supervisor: failed to open events/listen stream; will retry",
				"err", err, "consecutive", consecutive)
			if !s.sleep(ctx, s.backoff(consecutive)) {
				return
			}
			consecutive++
			s.maybeMarkUnhealthy(ctx, instanceID, consecutive, &markedUnhealthy)
			continue
		}

		streamUp.WithLabelValues(dbInst.PluginID, instanceID).Set(1)

		if !initialHealthSet {
			initialHealthSet = true
			consecutive = 0
			if s.cfg.HealthSetter != nil {
				s.cfg.HealthSetter(ctx, instanceID, model.PluginHealthStateHealthy, "")
			}
		}

		reason, recvErr := s.recvLoop(ctx, instanceID, dbInst.PluginID, hash, stream, log, &markedUnhealthy, &consecutive)
		stream.Close()
		streamUp.WithLabelValues(dbInst.PluginID, instanceID).Set(0)

		switch reason {
		case endShutdown:
			return
		case endCleanClose:
			log.InfoContext(ctx, "events supervisor: stream closed cleanly by server; reconnecting", "detail", recvErr)
			continue
		default: // endFailure
			log.WarnContext(ctx, "events supervisor: stream ended; will retry",
				"err", recvErr, "consecutive", consecutive)
			if !s.sleep(ctx, s.backoff(consecutive)) {
				return
			}
			consecutive++
			s.maybeMarkUnhealthy(ctx, instanceID, consecutive, &markedUnhealthy)
		}
	}
}

// recvLoop drains events from stream until a terminal sentinel or ctx
// cancellation. Every event is handed to the Sink synchronously, and the
// cursor is advanced only AFTER Handle returns — regardless of whether it
// returned an error (a sink that errors still advances: the dispatcher's own
// claim/rollback owns redelivery from there, and double-owning would race
// two rollback mechanisms). A sink that blocks is, by the same rule, never
// advanced past while it blocks.
func (s *Supervisor) recvLoop(
	ctx context.Context,
	instanceID, pluginID, hash string,
	stream EventStream,
	log *slog.Logger,
	markedUnhealthy *bool,
	consecutive *int,
) (streamEndReason, error) {
	firstEvent := true

	for {
		ev, err := stream.Next(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return endShutdown, nil
			}

			var closed *mcp.EventsStreamClosed
			if errors.As(err, &closed) {
				s.applyCleanCloseCursor(ctx, instanceID, hash, closed, log)
				return endCleanClose, err
			}
			// Heartbeat starvation and any other transport error are both
			// failures that count toward UnhealthyAfter and drive backoff —
			// only a clean close is exempted (doc §7.4/§7.1).
			return endFailure, err
		}

		if firstEvent {
			firstEvent = false
			if *markedUnhealthy {
				if s.cfg.HealthSetter != nil {
					s.cfg.HealthSetter(ctx, instanceID, model.PluginHealthStateHealthy, "")
				}
				*markedUnhealthy = false
			}
			*consecutive = 0
		}

		evt := Event{
			InstanceID:  instanceID,
			PluginID:    pluginID,
			EventKind:   ev.Type,
			EventID:     ev.ID,
			PayloadJSON: []byte(ev.Data),
			ObservedAt:  ev.Time,
		}
		if evt.ObservedAt.IsZero() {
			evt.ObservedAt = time.Now()
		}

		eventsReceived.WithLabelValues(pluginID, instanceID, ev.Type).Inc()
		s.publish(evt)

		// Handle is synchronous by design — see trigger.Supervisor.recvLoop's
		// doc for why: blocking until dispatch completes provides
		// back-pressure to the plugin and preserves per-stream ordering.
		if s.sink != nil {
			if handleErr := s.sink.Handle(ctx, evt); handleErr != nil {
				if ctx.Err() != nil {
					return endShutdown, nil
				}
				log.WarnContext(ctx, "events supervisor: sink Handle error",
					"event_id", evt.EventID, "err", handleErr)
			}
		}

		cursorStr := strconv.FormatUint(ev.Sequence, 10)
		if advErr := s.cursor.Advance(ctx, instanceID, hash, cursorStr, ev.Sequence); advErr != nil {
			log.WarnContext(ctx, "events supervisor: cursor advance failed",
				"event_id", evt.EventID, "err", advErr)
		}
	}
}

// applyCleanCloseCursor advances the stored cursor to the point a clean
// close's carried cursor names, when that point is newer than what is
// already stored. The carried cursor follows this implementation's own
// decimal-gleipnirseq convention (the same one every cursor this package
// writes uses) — an unparseable or not-newer value is simply left alone, and
// the next Load falls back to whatever was last durably advanced by an
// actually-consumed event.
func (s *Supervisor) applyCleanCloseCursor(ctx context.Context, instanceID, hash string, closed *mcp.EventsStreamClosed, log *slog.Logger) {
	seq, ok := parseCursorSeq(closed.Cursor)
	if !ok {
		return
	}
	_, curSeq, err := s.cursor.Load(ctx, instanceID, hash)
	if err != nil {
		log.WarnContext(ctx, "events supervisor: failed to read cursor before applying clean-close cursor", "err", err)
		return
	}
	if seq <= curSeq {
		return
	}
	if err := s.cursor.Advance(ctx, instanceID, hash, closed.Cursor, seq); err != nil {
		log.WarnContext(ctx, "events supervisor: failed to apply clean-close cursor", "err", err)
	}
}

// parseCursorSeq decodes a cursor string using this package's own
// decimal-gleipnirseq convention (mirrors mcp.FakeEventsServer's
// parseCursorSeq, the same convention this package's own cursor writes use).
func parseCursorSeq(cursor string) (uint64, bool) {
	if cursor == "" {
		return 0, false
	}
	n, err := strconv.ParseUint(cursor, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// publish emits plugin.event_emitted onto the SSE bus, matching
// hostsvc.EmitEvent's payload keys exactly so real-time consumers see the
// same shape regardless of which ingestion path produced the event.
func (s *Supervisor) publish(evt Event) {
	if s.cfg.Publisher == nil {
		return
	}
	payload, err := json.Marshal(map[string]string{
		"event_id":    evt.EventID,
		"event_kind":  evt.EventKind,
		"plugin_id":   evt.PluginID,
		"instance_id": evt.InstanceID,
		"payload":     string(evt.PayloadJSON),
	})
	if err != nil {
		return
	}
	s.cfg.Publisher.Publish("plugin.event_emitted", payload)
}

// maybeMarkUnhealthy calls HealthSetter with Unhealthy when consecutive
// failures reach UnhealthyAfter for the first time. Mirrors
// trigger.Supervisor.maybeMarkUnhealthy exactly.
func (s *Supervisor) maybeMarkUnhealthy(ctx context.Context, instanceID string, consecutive int, marked *bool) {
	if !*marked && consecutive >= s.cfg.UnhealthyAfter {
		if s.cfg.HealthSetter != nil {
			s.cfg.HealthSetter(ctx, instanceID, model.PluginHealthStateUnhealthy,
				"events/listen stream reconnect failed")
		}
		*marked = true
	}
}

// backoff mirrors trigger.Supervisor.backoff exactly: BackoffInitial * 2^n
// capped at BackoffMax, ±25% jitter.
func (s *Supervisor) backoff(consecutive int) time.Duration {
	if consecutive < 0 {
		consecutive = 0
	}
	const maxShift = 20
	shift := consecutive
	if shift > maxShift {
		shift = maxShift
	}
	d := s.cfg.BackoffInitial * (1 << shift)
	if d > s.cfg.BackoffMax || d <= 0 {
		d = s.cfg.BackoffMax
	}
	jitter := 0.75 + rand.Float64()*0.5
	d = time.Duration(float64(d) * jitter)
	if d > s.cfg.BackoffMax {
		d = s.cfg.BackoffMax
	}
	return d
}

// sleep waits for d or ctx cancellation. Returns false when ctx is done.
func (s *Supervisor) sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// parseManifestKinds decodes a plugin's manifest_snapshot (YAML) and returns
// its attested event kind names, in manifest order.
func parseManifestKinds(manifestSnapshot string) ([]string, error) {
	var m sdkmanifest.Manifest
	if err := sdkmanifest.Unmarshal([]byte(manifestSnapshot), &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	kinds := make([]string, len(m.EventKinds))
	for i, k := range m.EventKinds {
		kinds[i] = k.Kind
	}
	return kinds, nil
}

// scopeHash computes a stable identity for (kinds, scope) so a change to
// either resets the durable cursor with no special code path — a fresh hash
// is simply a hash Store.Load has never seen, which reports "no cursor" by
// construction. Kinds are sorted before hashing so the identity does not
// depend on the manifest's declaration order.
func scopeHash(kinds []string, scopeJSON string) string {
	sorted := append([]string(nil), kinds...)
	sort.Strings(sorted)

	h := sha256.New()
	for _, k := range sorted {
		h.Write([]byte(k))
		h.Write([]byte{0})
	}
	h.Write([]byte(scopeJSON))
	return hex.EncodeToString(h.Sum(nil))
}
