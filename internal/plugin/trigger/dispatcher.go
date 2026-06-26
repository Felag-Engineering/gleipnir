// Package trigger owns the host-side plugin event dispatch pipeline. It
// receives events from the trigger supervisor (one goroutine per plugin
// instance stream), deduplicates them, scans subscribed policies, evaluates
// per-policy bindings, and fires matching policies through RunLauncher.
//
// The package name is "trigger" — importers that also import internal/trigger
// (webhook/manual/cron etc.) must use an import alias such as plugintrigger.
package trigger

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/felag-engineering/gleipnir/internal/db"
	run "github.com/felag-engineering/gleipnir/internal/execution/run"
	"github.com/felag-engineering/gleipnir/internal/infra/event"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/binding"
	"github.com/felag-engineering/gleipnir/internal/plugin/dedup"
	"github.com/felag-engineering/gleipnir/internal/policy"
	sdkmanifest "github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
)

// RunLauncher is the narrow interface that Dispatcher uses from
// *run.RunLauncher. Defined as an interface so tests can inject a fake.
type RunLauncher interface {
	LaunchWithConcurrency(ctx context.Context, params run.LaunchParams) (run.LaunchResult, error)
}

// Querier is the narrow DB interface the Dispatcher needs. Using an interface
// (not *db.Queries directly) keeps Dispatcher unit-testable with a fake.
type Querier interface {
	GetSubscribedActivePolicies(ctx context.Context) ([]db.Policy, error)
	GetPluginByID(ctx context.Context, id string) (db.Plugin, error)
	GetPluginInstanceByID(ctx context.Context, id string) (db.PluginInstance, error)
	ListPluginsByStatus(ctx context.Context, status string) ([]db.Plugin, error)
	ListPluginInstancesByPlugin(ctx context.Context, pluginID string) ([]db.PluginInstance, error)
}

// ModelResolver fetches the system default LLM provider and model name.
// Satisfied by *settings.Service.
type ModelResolver interface {
	GetSystemDefault(ctx context.Context) (provider, modelName string, err error)
}

// Event carries a single plugin-emitted substrate event through the host
// dispatch pipeline.
type Event struct {
	InstanceID  string
	PluginID    string
	EventKind   string
	EventID     string
	PayloadJSON []byte
	ObservedAt  time.Time
}

// DispatcherConfig holds all dependencies for a Dispatcher.
type DispatcherConfig struct {
	// Launcher fires a policy run when a binding matches an event.
	// Satisfied by *run.RunLauncher in production; tests inject a fake.
	Launcher RunLauncher

	// Querier provides read access to policy and plugin rows.
	Querier Querier

	// Dedup short-circuits dispatch when an event has already been seen within
	// the dedup window. Noop{} is the zero-value default used by tests;
	// production wires the SQLite-backed dedup.NewDBStore (see pluginruntime.go).
	Dedup dedup.Store

	// Publisher emits observability events onto the SSE bus.
	Publisher event.Publisher

	// ModelResolver fetches the system default model for policy parsing.
	// When nil, policy.Parse is called with ("", "") and relies on the policy
	// YAML to carry an explicit model configuration.
	ModelResolver ModelResolver

	// Logger is the base structured logger. If nil, slog.Default() is used.
	Logger *slog.Logger
}

// Dispatcher receives plugin-emitted events, deduplicates them, scans
// subscribed policies, evaluates bindings, and launches matching runs.
//
// All public methods are safe for concurrent use.
type Dispatcher struct {
	launcher      RunLauncher
	q             Querier
	dedup         dedup.Store
	publisher     event.Publisher
	modelResolver ModelResolver
	log           *slog.Logger

	// policyCache maps policy_id → cachedPolicy. Entries are reused as long
	// as updated_at matches — a changed policy invalidates the cached parse.
	// Protected by mu.
	mu          sync.Mutex
	policyCache map[string]cachedPolicy
}

// cachedPolicy holds a parsed policy and its source updated_at timestamp.
// When updated_at changes the entry is re-parsed on the next Handle call.
type cachedPolicy struct {
	updatedAt string
	parsed    *model.ParsedPolicy
}

// NewDispatcher constructs a Dispatcher ready to accept events via Handle.
func NewDispatcher(cfg DispatcherConfig) *Dispatcher {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Dispatcher{
		launcher:      cfg.Launcher,
		q:             cfg.Querier,
		dedup:         cfg.Dedup,
		publisher:     cfg.Publisher,
		modelResolver: cfg.ModelResolver,
		log:           log,
		policyCache:   make(map[string]cachedPolicy),
	}
}

// Handle runs the full dispatch pipeline for a single event:
//  1. Dedup check — return early if the event was already seen.
//  2. Policy scan — load active subscribed policies from the DB.
//  3. Source match — keep only policies subscribed to this instance+kind.
//  4. Binding evaluate — compile and evaluate per-policy bindings.
//  5. Launch — fire matching policies through RunLauncher.
//
// Errors from individual policy steps (parse failure, binding compile, launch)
// are logged at Error and isolated: one bad policy never drops the stream.
// Handle itself returns nil unless the context is cancelled.
func (d *Dispatcher) Handle(ctx context.Context, evt Event) error {
	// 1. Dedup — fail-open: a store error is logged as Warn and dispatch
	//    proceeds so a degraded store does not silently drop events.
	//
	// The claim is atomic (INSERT ... ON CONFLICT DO NOTHING): the first of two
	// concurrently-arriving copies of an event wins the row and dispatches; the
	// second sees seen==true and short-circuits. This is what excludes the
	// concurrent-duplicate double-fire across the two ingestion paths (the
	// supervisor recvLoop and the EmitEvent host RPC) — keep the claim here.
	key := dedup.Key{
		InstanceID: evt.InstanceID,
		EventKind:  evt.EventKind,
		EventID:    evt.EventID,
	}
	seen, err := d.dedup.Seen(ctx, key)
	if err != nil {
		d.log.WarnContext(ctx, "dedup store error; proceeding with dispatch (fail-open)",
			"instance_id", evt.InstanceID,
			"event_id", evt.EventID,
			"err", err,
		)
	}
	if seen {
		d.log.DebugContext(ctx, "trigger dispatcher: dropping duplicate event",
			"reason", "duplicate",
			"instance_id", evt.InstanceID,
			"event_id", evt.EventID,
			"event_kind", evt.EventKind,
		)
		return nil
	}

	// claimed is true only when this call freshly committed the dedup row
	// (Seen returned (false, nil)). On the fail-open path (err != nil) nothing
	// was committed, so there is no claim to roll back.
	claimed := err == nil

	// At-least-once redelivery (#585): if we freshly claimed the dedup slot and
	// any transient failure prevents this event from being dispatched to its
	// matched policies, roll the claim back so the plugin's next redelivery of
	// this event is treated as novel rather than silently suppressed for the
	// full dedup window. "Transient failure" covers both a downstream launch
	// error (per-policy) and a pre-loop infrastructure error (policy scan or
	// instance fetch) — in every case nothing was launched, so suppressing the
	// redelivery would convert at-least-once into at-most-once.
	//
	// The rollback runs in a defer with a detached context so it fires even when
	// the policy loop returns early on ctx cancellation (mid-scan shutdown would
	// otherwise strand a committed-but-unlaunched key). context.Background()
	// mirrors RunLauncher's failure-path DB writes, which must not inherit a
	// cancelled run context.
	//
	// Consequence for the multi-policy case: when one event matches N policies
	// and any one launch fails transiently, the redelivery re-fires ALL matched
	// policies, including ones that already launched. This is the accepted cost
	// of at-least-once delivery — the launcher's per-policy concurrency check
	// (skip/queue/replace) absorbs the re-fire; only a `parallel`-concurrency
	// policy will produce a genuine duplicate run, which is exactly what that
	// mode opts into. A possible duplicate is strictly better than the silent
	// permanent drop of policies that never launched, which is the bug here.
	var rollbackClaim bool
	defer func() {
		if !claimed || !rollbackClaim {
			return
		}
		if uErr := d.dedup.Unsee(context.Background(), key); uErr != nil {
			d.log.WarnContext(ctx, "dedup rollback failed; event may be suppressed until TTL",
				"instance_id", evt.InstanceID,
				"event_id", evt.EventID,
				"err", uErr,
			)
		}
	}()

	// 2. Policy scan. A scan failure is transient and nothing was launched —
	// roll the claim back so the redelivery can retry the whole pipeline.
	policies, err := d.q.GetSubscribedActivePolicies(ctx)
	if err != nil {
		rollbackClaim = true
		return err
	}

	// Resolve the instance once per event so dispatchOne can compare the
	// human-readable instance name (trig.Source) without an extra DB round-trip
	// per policy. Missing instance is a hard error: the event came from a valid
	// token-authenticated subprocess, so the row must exist. As with the scan,
	// nothing launched — roll the claim back so the redelivery can retry.
	instance, err := d.q.GetPluginInstanceByID(ctx, evt.InstanceID)
	if err != nil {
		d.log.ErrorContext(ctx, "trigger dispatcher: failed to fetch instance; dropping event",
			"instance_id", evt.InstanceID,
			"event_id", evt.EventID,
			"err", err,
		)
		rollbackClaim = true
		return err
	}

	// Fetch the system default model once per Handle call; all policy parses
	// in this call share the same snapshot of the system default.
	provider, modelName := d.systemDefault(ctx)

	// 3–5. For each policy: match source, compile binding, evaluate, launch.
	// The deferred rollback above consumes anyTransientFailure once the loop
	// completes (or returns early on ctx cancellation).
	var evaluated, matched int
	for _, pol := range policies {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		outcome := d.dispatchOne(ctx, pol, evt, provider, modelName, instance)
		if outcome.evaluated {
			evaluated++
		}
		if outcome.matched {
			matched++
		}
		if outcome.launchFailed {
			rollbackClaim = true
		}
	}
	d.log.DebugContext(ctx, "trigger dispatcher: event evaluated against policies",
		"evaluated", evaluated,
		"matched", matched,
		"instance_id", evt.InstanceID,
		"event_id", evt.EventID,
		"event_kind", evt.EventKind,
	)
	return nil
}

// dispatchOutcome carries the per-policy result of a dispatchOne call.
//
//   - evaluated: true when the policy passed the source+kind filter and was
//     submitted to binding evaluation. Used to build the per-event aggregate
//     "evaluated N policies, matched M" debug log in Handle.
//   - matched: true when the binding evaluated to true (implies evaluated).
//   - launchFailed: true when the policy matched but its launch returned a
//     transient error — the signal Handle uses to roll back the dedup claim
//     (#585). Preserves the exact semantics of the previous bool return.
type dispatchOutcome struct {
	evaluated    bool
	matched      bool
	launchFailed bool
}

// dispatchOne runs steps 3–5 for a single policy row. All errors are logged
// and isolated; dispatchOne never returns an error to the recv loop.
func (d *Dispatcher) dispatchOne(ctx context.Context, pol db.Policy, evt Event, provider, modelName string, instance db.PluginInstance) dispatchOutcome {
	// 3. Parse and match source + event kind.
	//
	// trig.Source stores the human-readable instance name
	// (plugin_instances.instance_name), not the ULID; we compare it to the
	// InstanceName resolved by the caller.
	parsed, err := d.parsedPolicy(pol, provider, modelName)
	if err != nil {
		d.log.ErrorContext(ctx, "trigger dispatcher: failed to parse policy YAML; skipping",
			"policy_id", pol.ID,
			"event_id", evt.EventID,
			"err", err,
		)
		return dispatchOutcome{}
	}

	trig := parsed.Trigger
	if trig.Source != instance.InstanceName {
		return dispatchOutcome{} // policy watches a different instance
	}
	if trig.EventKind != evt.EventKind {
		return dispatchOutcome{} // policy watches a different event kind
	}

	// Past the source+kind filter: this policy counts as evaluated regardless
	// of what happens next (binding error, no-match, or successful launch).

	// 4. Compile and evaluate binding.
	schema, compErr := d.bindingSchema(ctx, instance.PluginID, evt.EventKind)
	if compErr != nil {
		d.log.ErrorContext(ctx, "trigger dispatcher: failed to load binding schema; skipping",
			"policy_id", pol.ID,
			"plugin_id", instance.PluginID,
			"event_kind", evt.EventKind,
			"err", compErr,
		)
		d.publishNoMatch(evt, pol.ID)
		return dispatchOutcome{evaluated: true}
	}

	compiled, compErr := binding.Compile(trig.Binding, schema)
	if compErr != nil {
		d.log.ErrorContext(ctx, "trigger dispatcher: failed to compile binding; skipping",
			"policy_id", pol.ID,
			"event_kind", evt.EventKind,
			"err", compErr,
		)
		d.publishNoMatch(evt, pol.ID)
		return dispatchOutcome{evaluated: true}
	}

	var payload map[string]any
	if len(evt.PayloadJSON) > 0 {
		// Decode with UseNumber so 64-bit integer IDs (Slack/Snowflake-style,
		// above 2^53) survive as json.Number rather than being coerced to a
		// lossy float64. The binding evaluator compares such fields exactly
		// (#586).
		dec := json.NewDecoder(bytes.NewReader(evt.PayloadJSON))
		dec.UseNumber()
		if err := dec.Decode(&payload); err != nil {
			d.log.WarnContext(ctx, "trigger dispatcher: event payload is not valid JSON; treating as empty",
				"policy_id", pol.ID,
				"event_id", evt.EventID,
				"err", err,
			)
		}
	}

	match, _ := compiled.Evaluate(payload)
	if !match {
		d.log.DebugContext(ctx, "trigger dispatcher: binding did not match policy",
			"reason", "binding_no_match",
			"policy_id", pol.ID,
			"event_id", evt.EventID,
			"event_kind", evt.EventKind,
		)
		d.publishNoMatch(evt, pol.ID)
		return dispatchOutcome{evaluated: true}
	}

	d.publishMatch(evt, pol.ID)

	// 5. Launch.
	res, err := d.launcher.LaunchWithConcurrency(ctx, run.LaunchParams{
		PolicyID:       pol.ID,
		TriggerType:    model.TriggerTypeSubscribed,
		TriggerPayload: string(evt.PayloadJSON),
		ParsedPolicy:   parsed,
	})
	if err != nil {
		switch {
		case errors.Is(err, run.ErrConcurrencyQueueFull):
			d.log.WarnContext(ctx, "trigger dispatcher: trigger queue is full",
				"policy_id", pol.ID, "event_id", evt.EventID)
		case errors.Is(err, run.ErrConcurrencyUnrecognised):
			// Error-level log for the unrecognised policy, matching the old
			// launchOrQueueBackground default "concurrency check failed" branch.
			d.log.ErrorContext(ctx, "trigger dispatcher: concurrency check failed",
				"policy_id", pol.ID, "event_id", evt.EventID, "err", err)
		case run.IsConcurrencyCheckError(err):
			d.log.ErrorContext(ctx, "trigger dispatcher: concurrency check failed",
				"policy_id", pol.ID, "event_id", evt.EventID, "err", err)
		case run.IsEnqueueError(err):
			d.log.ErrorContext(ctx, "trigger dispatcher: failed to enqueue trigger",
				"policy_id", pol.ID, "event_id", evt.EventID, "err", err)
		default:
			d.log.ErrorContext(ctx, "trigger dispatcher: failed to launch run",
				"policy_id", pol.ID, "run_id", res.RunID, "event_id", evt.EventID, "err", err)
		}
		// Every launch error here is transient (queue full, concurrency-check
		// DB error, enqueue DB error, or a raw launch failure). Signal the
		// caller to roll back the dedup claim so a redelivery can fire.
		return dispatchOutcome{evaluated: true, matched: true, launchFailed: true}
	}

	switch res.Outcome {
	case run.OutcomeSkipped:
		d.log.InfoContext(ctx, "trigger dispatcher: skipping fire, active run exists (concurrency: skip)",
			"policy_id", pol.ID, "event_id", evt.EventID)
	case run.OutcomeQueued:
		d.log.InfoContext(ctx, "trigger dispatcher: trigger queued (active run exists)",
			"policy_id", pol.ID, "event_id", evt.EventID)
	case run.OutcomeLaunched:
		// No info log on plain launch — avoids a log-volume regression.
	}
	// Skip/queue/launched are all successful consumption of the event — keep
	// the dedup slot.
	return dispatchOutcome{evaluated: true, matched: true}
}

// parsedPolicy returns a parsed policy, using a cache keyed on (id, updated_at).
// When updated_at changes the entry is re-parsed so a policy edit takes effect
// on the next event without restarting the trigger stream.
func (d *Dispatcher) parsedPolicy(pol db.Policy, provider, modelName string) (*model.ParsedPolicy, error) {
	d.mu.Lock()
	entry, ok := d.policyCache[pol.ID]
	if ok && entry.updatedAt == pol.UpdatedAt {
		d.mu.Unlock()
		return entry.parsed, nil
	}
	d.mu.Unlock()

	parsed, err := policy.Parse(pol.Yaml, provider, modelName)
	if err != nil {
		return nil, err
	}

	d.mu.Lock()
	d.policyCache[pol.ID] = cachedPolicy{
		updatedAt: pol.UpdatedAt,
		parsed:    parsed,
	}
	d.mu.Unlock()

	return parsed, nil
}

// bindingSchema fetches the plugin manifest for pluginID and returns the
// BindingSchema yaml.Node for the given event kind. Returns nil when the event
// kind has no binding schema (which is valid — nil schema compiles to a binding
// that matches everything).
func (d *Dispatcher) bindingSchema(ctx context.Context, pluginID, eventKind string) (*yaml.Node, error) {
	plugin, err := d.q.GetPluginByID(ctx, pluginID)
	if err != nil {
		return nil, err
	}

	// manifest_snapshot is stored as YAML (not JSON) — use manifest.Unmarshal.
	var m sdkmanifest.Manifest
	if err := sdkmanifest.Unmarshal([]byte(plugin.ManifestSnapshot), &m); err != nil {
		return nil, err
	}

	for i := range m.EventKinds {
		if m.EventKinds[i].Kind == eventKind {
			return m.EventKinds[i].BindingSchema, nil
		}
	}
	// Event kind not declared in manifest; treat as no schema (matches everything).
	return nil, nil
}

// systemDefault fetches the system default model. On error it logs and returns
// ("", "") so policy.Parse still works — it will rely on any model the policy
// YAML declares explicitly.
func (d *Dispatcher) systemDefault(ctx context.Context) (provider, modelName string) {
	if d.modelResolver == nil {
		return "", ""
	}
	p, m, err := d.modelResolver.GetSystemDefault(ctx)
	if err != nil {
		d.log.WarnContext(ctx, "trigger dispatcher: failed to fetch system default model; using blank",
			"err", err,
		)
		return "", ""
	}
	return p, m
}

// publishMatch emits a plugin.event_matched SSE event so operators can observe
// binding matches in the real-time feed.
func (d *Dispatcher) publishMatch(evt Event, policyID string) {
	payload, err := json.Marshal(map[string]string{
		"event_id":    evt.EventID,
		"event_kind":  evt.EventKind,
		"instance_id": evt.InstanceID,
		"plugin_id":   evt.PluginID,
		"policy_id":   policyID,
	})
	if err != nil {
		return
	}
	d.publisher.Publish("plugin.event_matched", payload)
}

// publishNoMatch emits a plugin.event_no_match SSE event for observability.
func (d *Dispatcher) publishNoMatch(evt Event, policyID string) {
	payload, err := json.Marshal(map[string]string{
		"event_id":    evt.EventID,
		"event_kind":  evt.EventKind,
		"instance_id": evt.InstanceID,
		"plugin_id":   evt.PluginID,
		"policy_id":   policyID,
	})
	if err != nil {
		return
	}
	d.publisher.Publish("plugin.event_no_match", payload)
}
