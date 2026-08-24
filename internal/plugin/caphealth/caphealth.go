// Package caphealth models plugin health PER CAPABILITY rather than per
// instance (ADR-056, mcp-realignment-spec.md §7 health).
//
// It exists to fix a specific defect rather than to add a layer. In the v1.1
// model an instance has one health state, so a single failing capability —
// classically one missing OAuth scope on one tool — marks the whole instance
// unhealthy, and every OTHER capability it serves perfectly well stops being
// routed to. That is not a conservative failure: it converts a small, precise
// fault into a total outage, and it hides which capability actually broke.
//
// The model here is two-layer, and the split is the entire idea:
//
//   - **Liveness** is instance-wide, because it is about whether the process is
//     reachable at all: the container's healthcheck and a `server/discover`
//     probe. A liveness fault means nothing this instance claims can be
//     trusted, so it overrides every capability state.
//   - **Capability health** is per (profile, name). A fault here narrows what
//     the instance is routed for and nothing else.
//
// The instance-level chip the UI already renders becomes a ROLLUP — the worst
// of liveness and the capability states — so the existing surface keeps working
// while per-capability detail becomes available underneath it.
//
// Health is deliberately NOT persisted per capability. It is a live
// observation, re-derived by the prober every pass; a durable copy would be a
// second source of truth that is wrong every time the prober is between passes.
// The rolled-up instance state is still written to plugin_instances.health_state,
// because that is what the chip reads and what survives a restart as a
// last-known value.
package caphealth

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/state"
)

// Profile identifies which capability surface a health entry belongs to. The
// four values mirror the manifest v2 profiles (spec §4) — a profile a plugin
// did not declare has no health, because it serves nothing.
type Profile string

const (
	ProfileToolProvider     Profile = "tool_provider"
	ProfileEventSource      Profile = "event_source"
	ProfileHumanChannel     Profile = "human_channel"
	ProfileIdentityProvider Profile = "identity_provider"
)

func (p Profile) Valid() bool {
	switch p {
	case ProfileToolProvider, ProfileEventSource, ProfileHumanChannel, ProfileIdentityProvider:
		return true
	}
	return false
}

// Capability names one health-bearing surface.
//
// Name is optional and scopes the entry BELOW the profile — a single tool, a
// single event kind. That optional granularity is the whole point of the
// package: "the deploy tool is missing a scope" and "this plugin is broken" are
// different facts, and only the first one should narrow routing.
type Capability struct {
	Profile Profile
	// Name is empty for a profile-wide entry.
	Name string
}

// String renders a capability for logs and API responses.
func (c Capability) String() string {
	if c.Name == "" {
		return string(c.Profile)
	}
	return string(c.Profile) + "/" + c.Name
}

// Entry is one capability's health.
type Entry struct {
	Capability Capability

	// State reuses the instance health vocabulary rather than coining a
	// parallel one. The severity ordering, the merge rule, and the chip colors
	// are already defined over these values in internal/plugin/state, and a
	// second vocabulary would need all three re-derived and kept in sync.
	State model.PluginHealthState

	// Detail is the operator-facing explanation. Untrusted when it originates
	// from a plugin's self-report.
	Detail string
}

// Liveness is the instance-wide reachability signal: container healthcheck plus
// `server/discover`. Both must pass.
type Liveness struct {
	// ContainerHealthy reports the runtime's own healthcheck verdict.
	ContainerHealthy bool

	// DiscoverOK reports whether the instance answered `server/discover`.
	//
	// It is checked separately from the container healthcheck because they
	// answer different questions: the healthcheck asks whether the process is
	// alive, and this asks whether the MCP server inside it is serving. A
	// container that is up with a wedged server passes the first and fails the
	// second, and routing to it would hang rather than fail.
	DiscoverOK bool

	// Detail explains a failure. Empty when both checks passed.
	Detail string

	// ObservedAt is when this verdict was established. A zero value means
	// never observed.
	ObservedAt time.Time
}

// OK reports whether the instance is reachable.
func (l Liveness) OK() bool { return l.ContainerHealthy && l.DiscoverOK }

// Fresh reports whether a verdict is recent enough to act on, given the
// reference instant and a staleness window.
//
// This exists because a prober that STOPS is indistinguishable, from the
// registry's point of view, from one that keeps finding everything healthy —
// the last verdict just sits there being green. Ageing the observation out
// means a wedged or crashed probe loop degrades routing instead of silently
// certifying instances nobody has looked at in an hour.
//
// A zero window disables the check, for callers that drive the registry
// synchronously and have no loop to lose.
func (l Liveness) Fresh(now time.Time, window time.Duration) bool {
	if window <= 0 {
		return true
	}
	if l.ObservedAt.IsZero() {
		return false
	}
	return now.Sub(l.ObservedAt) <= window
}

// InstanceHealth is the full health picture for one instance.
type InstanceHealth struct {
	InstanceID string
	Liveness   Liveness
	Entries    []Entry

	// Stale is true when the liveness verdict is too old to act on. It is
	// resolved at read time against the registry's staleness window, so every
	// consumer sees the same answer without each one re-deriving it.
	Stale bool
}

// Rollup returns the single state the instance chip shows: the worst of
// liveness and every capability state.
//
// A liveness fault reports `unhealthy` regardless of what the capabilities
// last said, and it does so WITHOUT consulting them. Those states are stale by
// definition once the instance stopped answering — reporting "channel healthy"
// for a process that is not responding would be reporting an observation the
// host can no longer make.
func (h InstanceHealth) Rollup() model.PluginHealthState {
	if h.Stale || !h.Liveness.OK() {
		return model.PluginHealthStateUnhealthy
	}
	states := make([]model.PluginHealthState, 0, len(h.Entries))
	for _, e := range h.Entries {
		states = append(states, e.State)
	}
	return state.WorstHealth(states)
}

// RollupDetail explains the rollup: the liveness failure, or the worst
// capability's detail with the capability named.
//
// Naming the capability is the point. "unhealthy" on its own sends an operator
// to look at the whole plugin; "unhealthy: tool_provider/deploy — missing scope
// repo:write" sends them to the one thing that is actually broken.
func (h InstanceHealth) RollupDetail() string {
	if h.Stale {
		return "health has not been observed recently; the probe loop may not be running"
	}
	if !h.Liveness.OK() {
		if h.Liveness.Detail != "" {
			return h.Liveness.Detail
		}
		return "instance is not reachable"
	}

	worst := Entry{State: model.PluginHealthStateHealthy}
	found := false
	for _, e := range h.Entries {
		if !found || state.Severity(e.State) > state.Severity(worst.State) {
			worst, found = e, true
		}
	}
	if !found || worst.State == model.PluginHealthStateHealthy {
		return ""
	}
	if worst.Detail == "" {
		return worst.Capability.String()
	}
	return worst.Capability.String() + ": " + worst.Detail
}

// Partial reports whether SOME capabilities are degraded while the instance is
// still serving others — the state the old model could not express at all.
func (h InstanceHealth) Partial() bool {
	if h.Stale || !h.Liveness.OK() {
		return false // a liveness fault is total, not partial
	}
	var healthy, degraded int
	for _, e := range h.Entries {
		if e.State == model.PluginHealthStateHealthy {
			healthy++
		} else {
			degraded++
		}
	}
	return healthy > 0 && degraded > 0
}

// Serves reports whether a capability is healthy enough to route to.
//
// This is the routing question, and it is asked per capability precisely so a
// broken one does not answer for the rest. An unhealthy channel capability makes
// audience dispatch fall through to the next entry; an unhealthy event source
// pauses its listen stream — neither touches the instance's tools.
//
// An UNKNOWN capability serves. A plugin is not required to report health for
// every surface it implements, and treating silence as a fault would make the
// absence of a signal indistinguishable from a bad one — which would put every
// plugin that reports nothing permanently out of service.
func (h InstanceHealth) Serves(c Capability) bool {
	if h.Stale || !h.Liveness.OK() {
		return false
	}
	for _, e := range h.Entries {
		if e.Capability != c {
			continue
		}
		return servingState(e.State)
	}
	// No entry for this exact capability. Fall back to the profile-wide entry,
	// so "the channel profile is down" covers a named channel under it.
	if c.Name != "" {
		return h.Serves(Capability{Profile: c.Profile})
	}
	return true
}

// servingState reports whether a state still permits routing.
//
// unsigned_permissive serves: it is an operator's deliberate choice, already
// surfaced as a yellow chip and a high-severity audit event, and refusing to
// route to it would silently undo the escape hatch they opted into.
func servingState(s model.PluginHealthState) bool {
	switch s {
	case model.PluginHealthStateHealthy, model.PluginHealthStateUnsignedPermissive:
		return true
	}
	return false
}

// Registry holds live per-capability health for every instance.
//
// In-memory by design (see the package doc): health is an observation the
// prober re-derives, not durable state. A restart starts from nothing and the
// first pass fills it in, which is honest — the host genuinely does not know an
// instance's health until it has looked.
type Registry struct {
	mu sync.Mutex
	// byInstance maps instance ID to that instance's capability entries, keyed
	// by capability so a repeat report replaces rather than accumulates.
	byInstance map[string]map[Capability]Entry
	liveness   map[string]Liveness

	// staleAfter ages a liveness verdict out. Zero disables the check.
	staleAfter time.Duration
}

// NewRegistry returns a registry whose liveness verdicts never age out. Use
// WithStaleAfter for a registry fed by a background loop, where a stopped loop
// must degrade rather than keep certifying.
func NewRegistry() *Registry {
	return &Registry{
		byInstance: make(map[string]map[Capability]Entry),
		liveness:   make(map[string]Liveness),
	}
}

// NewRegistryWithStaleAfter returns a registry that treats a liveness verdict
// older than window as unusable.
func NewRegistryWithStaleAfter(window time.Duration) *Registry {
	r := NewRegistry()
	r.staleAfter = window
	return r
}

// SetLiveness records an instance's reachability, stamping the observation
// time so it can age out.
func (r *Registry) SetLiveness(instanceID string, l Liveness) {
	if l.ObservedAt.IsZero() {
		l.ObservedAt = timeNow()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.liveness[instanceID] = l
}

// SetCapability records one capability's health, replacing any prior entry for
// the same capability.
func (r *Registry) SetCapability(instanceID string, e Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byInstance[instanceID] == nil {
		r.byInstance[instanceID] = make(map[Capability]Entry)
	}
	r.byInstance[instanceID][e.Capability] = e
}

// SelfReportCapability records a capability's health as reported by the
// plugin itself, enforcing the §8.1 "plugin can only mark itself worse" merge
// rule per capability — the same rule internal/plugin/state applies to the
// v1.1 per-instance reports, resolved with the same severity ranking.
//
// A report that would improve or merely restate an existing entry is a no-op
// (applied=false): recovery is the HOST's observation to make (the prober, or
// ClearCapability), because a plugin that could self-clear a fault could mask
// one — including a drift fault the prober recorded about it. A report for a
// capability with no entry records, whatever its state: seeding healthy is
// what the prober does too, and seeding a fault is the method's purpose.
//
// The check-and-set runs under the registry lock so two racing self-reports
// cannot interleave into an improvement.
func (r *Registry) SelfReportCapability(instanceID string, e Entry) (applied bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if entries, ok := r.byInstance[instanceID]; ok {
		if prior, exists := entries[e.Capability]; exists &&
			state.Severity(e.State) <= state.Severity(prior.State) {
			return false
		}
	}
	if r.byInstance[instanceID] == nil {
		r.byInstance[instanceID] = make(map[Capability]Entry)
	}
	r.byInstance[instanceID][e.Capability] = e
	return true
}

// ClearCapability removes an entry — used when a capability recovers and the
// host would rather say nothing than assert healthiness it has not re-observed.
func (r *Registry) ClearCapability(instanceID string, c Capability) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if entries, ok := r.byInstance[instanceID]; ok {
		delete(entries, c)
	}
}

// Forget drops everything known about an instance, for uninstall/deactivate.
func (r *Registry) Forget(instanceID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byInstance, instanceID)
	delete(r.liveness, instanceID)
}

// Get returns an instance's full health picture. Entries are sorted so the
// output is stable for an API response and a test assertion alike.
func (r *Registry) Get(instanceID string) InstanceHealth {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := InstanceHealth{
		InstanceID: instanceID,
		// An instance nothing has probed yet reports NOT live. The zero value
		// of Liveness is both-checks-false, which is the honest answer before
		// a first observation: the host does not know, and claiming reachable
		// would route traffic at a guess.
		Liveness: r.liveness[instanceID],
	}
	out.Stale = !out.Liveness.Fresh(timeNow(), r.staleAfter)
	for _, e := range r.byInstance[instanceID] {
		out.Entries = append(out.Entries, e)
	}
	sort.Slice(out.Entries, func(i, j int) bool {
		return out.Entries[i].Capability.String() < out.Entries[j].Capability.String()
	})
	return out
}

// Serves is the routing check, resolved against the current registry.
func (r *Registry) Serves(instanceID string, c Capability) bool {
	return r.Get(instanceID).Serves(c)
}

// DriftDetail renders a manifest-vs-discovery drift explanation.
//
// Drift is a CAPABILITY fault, not an instance one (spec §7, #807 attestation):
// the signed manifest attests which event kinds a plugin may emit, discovery
// reports what it actually offers, and a mismatch means that surface cannot be
// trusted. It says nothing about the plugin's tools, so it must not take them
// down with it.
//
// Both directions matter and for different reasons. Attested-but-not-discovered
// is a capability an operator approved and is not getting. Discovered-but-not-
// attested is the more serious one: a surface the plugin is offering that
// nobody signed for.
func DriftDetail(attested, discovered []string) string {
	missing := difference(attested, discovered)
	extra := difference(discovered, attested)
	if len(missing) == 0 && len(extra) == 0 {
		return ""
	}

	var parts []string
	if len(missing) > 0 {
		parts = append(parts, "attested but not discovered: "+strings.Join(missing, ", "))
	}
	if len(extra) > 0 {
		parts = append(parts, "discovered but not attested: "+strings.Join(extra, ", "))
	}
	return "manifest/discovery drift — " + strings.Join(parts, "; ")
}

// difference returns the sorted members of a that are absent from b.
func difference(a, b []string) []string {
	have := make(map[string]struct{}, len(b))
	for _, s := range b {
		have[s] = struct{}{}
	}
	var out []string
	for _, s := range a {
		if _, ok := have[s]; !ok {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// intersect returns the sorted members present in both a and b.
//
// Used alongside difference to give a kind present on BOTH sides an explicit
// healthy per-kind entry when the profile as a whole has drifted (see
// applyEventDrift): without it, Serves for that kind would fall back to the
// profile-wide entry and read unhealthy even though nothing is actually wrong
// with it, which is the same "one broken thing takes everything down with it"
// defect this package exists to fix, just recreated one layer down.
func intersect(a, b []string) []string {
	have := make(map[string]struct{}, len(b))
	for _, s := range b {
		have[s] = struct{}{}
	}
	var out []string
	for _, s := range a {
		if _, ok := have[s]; ok {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
