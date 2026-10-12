package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sort"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/infra/logctx"
	"github.com/felag-engineering/gleipnir/internal/plugin/caphealth"
	"github.com/felag-engineering/gleipnir/internal/plugin/container"
	"github.com/felag-engineering/gleipnir/internal/plugin/logcapture"
	"github.com/felag-engineering/gleipnir/internal/plugin/resources"
)

// EventObservePassCompleted is published after every observe pass. It fires
// strictly AFTER EventPassCompleted and EventRotationPassCompleted within one
// runPass (observe runs last), so a test or caller that needs "the observers
// have been told about this cycle" synchronizes on this event rather than on
// either of those.
const EventObservePassCompleted = "container.reconcile_observe_pass"

// The observe pass (spec §5, §7; issue #959) keeps the four host-side
// observers pointed at what is actually live:
//
//   - events.Supervisor: one `events/listen` stream per event_source instance;
//   - the capability prober: its target set (caphealth.Target);
//   - log capture: one follower per live container, and Forget when an
//     instance is gone;
//   - the resources collector: its target set (resources.Target).
//
// Like the attach pass it is level-triggered and has no callback from the
// generation switch: each pass recomputes the desired observer state from the
// live generations and the consented manifest_snapshot, diffs it against what
// the previous passes left in place, and applies the difference. A second pass
// over an unchanged world applies nothing.
//
// Ordering. docs/developer/mcp-realignment-spec.md does not fix the observer
// order; #962 does, in two places this pass mirrors:
//
//   - boot is "one synchronous reconcile pass -> attach -> observe, then the
//     tickers" (the §13 resurrection-after-convergence rule), which is why
//     runPass calls this after attachPassLocked and why the events supervisor
//     is started from here and never earlier;
//   - quiesce stops the events supervisor first (nothing may reach
//     RunLauncher.Launch while the rest winds down, the #500/#787 rule), and
//     shutdown then joins the prober, the collector and log capture.
//
// applyObserve therefore tears down in that order (events, prober, collector,
// logs) and brings up in the reverse one, supervisor last, so a stream never
// delivers an event for an instance whose health, stats and logs are not yet
// observed.

// EventStreams is the events supervisor side of the observe pass.
// *events.Supervisor satisfies it.
type EventStreams interface {
	Start(ctx context.Context, instanceID string)
	Stop(instanceID string)
}

// ProbeTargetSet receives the prober's target set. *caphealth.TargetSet
// satisfies it. Replacing the set does not touch the capability registry, so
// a plugin's self-reported fault on a still-live instance survives; clearing
// one on a restart stays the prober's own observation (Prober.observeRestart),
// which this pass feeds by passing the active ContainerID through unchanged.
type ProbeTargetSet interface {
	SetTargets(targets []caphealth.Target)
}

// HealthRegistry is the part of the capability registry the observe pass
// writes: dropping a gone instance's entries. *caphealth.Registry satisfies it.
type HealthRegistry interface {
	Forget(instanceID string)
}

// LogCapture is the log capture side of the observe pass.
// *logcapture.Capturer satisfies it.
type LogCapture interface {
	Follow(ctx context.Context, target logcapture.Target) error
	Forget(instanceID string)
}

// StatsTargetSet receives the resources collector's target set.
// *resources.TargetSet satisfies it.
type StatsTargetSet interface {
	SetTargets(targets []resources.Target)
}

// ObserveDeps configures the observe pass. Optional in Config: nil disables
// it. Every member is optional and nil-safe on its own; an unset one is simply
// not driven. Leave members unset rather than passing a typed nil pointer.
type ObserveDeps struct {
	Events EventStreams
	Probe  ProbeTargetSet
	Health HealthRegistry
	Logs   LogCapture
	Stats  StatsTargetSet
}

// ObserveActionKind is one step the observe planner decides.
type ObserveActionKind string

const (
	ObserveEventsStart ObserveActionKind = "events_start"
	ObserveEventsStop  ObserveActionKind = "events_stop"
	ObserveProbeSet    ObserveActionKind = "probe_set"
	ObserveStatsSet    ObserveActionKind = "stats_set"
	ObserveLogFollow   ObserveActionKind = "log_follow"
	ObserveLogUnfollow ObserveActionKind = "log_unfollow"
	// ObserveForget drops a gone instance's log tail and capability entries.
	ObserveForget ObserveActionKind = "forget"
)

// ObserveAction is a planned step.
type ObserveAction struct {
	Kind       ObserveActionKind
	InstanceID string
	// Log is the follow target, for ObserveLogFollow.
	Log logcapture.Target
	// ContainerID is the container to stop following, for ObserveLogUnfollow.
	ContainerID container.ContainerID
	Reason      string
}

// observeInstance is what the planner needs to know about one live instance.
type observeInstance struct {
	InstanceID string

	// HoldReason is set when the instance is live but not routable this pass
	// (same cause as attachInstance.HoldReason). Events, probe and stats state
	// is left exactly as it was: dropping a working stream or target over a
	// transient inspect failure would churn it for nothing. Log capture does
	// not hold — it needs a container, not a route, and a container that
	// cannot yet be routed to is the one whose output explains why.
	HoldReason string

	// EventSource reports whether the consented manifest declares the
	// event_source profile.
	EventSource bool

	Probe caphealth.Target
	Stats resources.Target

	// Containers is every live container, to follow.
	Containers []logcapture.Target
}

// observedState is what earlier passes left in place. A copy is handed to the
// planner so it stays a pure function.
type observedState struct {
	// Known is every instance that was live last pass.
	Known     map[string]struct{}
	Events    map[string]struct{}
	Probe     map[string]caphealth.Target
	Stats     map[string]resources.Target
	Following map[string]map[container.ContainerID]struct{}
}

// observePlan is the planner's output: the steps, plus the full target sets
// the two set-replacing observers should end up holding.
type observePlan struct {
	Actions []ObserveAction
	Probe   []caphealth.Target
	Stats   []resources.Target
}

// planObserve diffs the desired observer state against prior. It is a pure
// function of its inputs, in the style of planAttach. Actions are ordered by
// the teardown-then-bring-up sequence documented at the top of this file and,
// within a kind, by instance ID, so two passes over an unchanged world read
// identically.
func planObserve(instances []observeInstance, prior observedState) observePlan {
	live := make(map[string]observeInstance, len(instances))
	for _, inst := range instances {
		live[inst.InstanceID] = inst
	}

	var eventStops, eventStarts, unfollows, follows, forgets []ObserveAction
	probe := make(map[string]caphealth.Target)
	stats := make(map[string]resources.Target)

	for _, inst := range instances {
		id := inst.InstanceID

		switch {
		case inst.HoldReason != "":
			// Keep whatever is in place, and nothing else.
			if t, ok := prior.Probe[id]; ok {
				probe[id] = t
			}
			if t, ok := prior.Stats[id]; ok {
				stats[id] = t
			}
		default:
			probe[id] = inst.Probe
			stats[id] = inst.Stats

			_, supervised := prior.Events[id]
			switch {
			case inst.EventSource && !supervised:
				eventStarts = append(eventStarts, ObserveAction{Kind: ObserveEventsStart, InstanceID: id,
					Reason: "manifest declares the event_source profile"})
			case !inst.EventSource && supervised:
				eventStops = append(eventStops, ObserveAction{Kind: ObserveEventsStop, InstanceID: id,
					Reason: "manifest no longer declares the event_source profile"})
			}
		}

		wanted := make(map[container.ContainerID]struct{}, len(inst.Containers))
		for _, target := range inst.Containers {
			wanted[target.ContainerID] = struct{}{}
			if _, ok := prior.Following[id][target.ContainerID]; !ok {
				follows = append(follows, ObserveAction{Kind: ObserveLogFollow, InstanceID: id,
					Log: target, ContainerID: target.ContainerID, Reason: "live container"})
			}
		}
		for containerID := range prior.Following[id] {
			if _, ok := wanted[containerID]; !ok {
				unfollows = append(unfollows, ObserveAction{Kind: ObserveLogUnfollow, InstanceID: id,
					ContainerID: containerID, Reason: "container is no longer live"})
			}
		}
	}

	for id := range prior.Known {
		if _, ok := live[id]; ok {
			continue
		}
		const reason = "no live generation serves this instance"
		if _, ok := prior.Events[id]; ok {
			eventStops = append(eventStops, ObserveAction{Kind: ObserveEventsStop, InstanceID: id, Reason: reason})
		}
		for containerID := range prior.Following[id] {
			unfollows = append(unfollows, ObserveAction{Kind: ObserveLogUnfollow, InstanceID: id,
				ContainerID: containerID, Reason: reason})
		}
		forgets = append(forgets, ObserveAction{Kind: ObserveForget, InstanceID: id, Reason: reason})
	}

	plan := observePlan{Probe: sortedProbeTargets(probe), Stats: sortedStatsTargets(stats)}

	var setActions []ObserveAction
	if !reflect.DeepEqual(plan.Probe, sortedProbeTargets(prior.Probe)) {
		setActions = append(setActions, ObserveAction{Kind: ObserveProbeSet, Reason: "probe target set changed"})
	}
	if !reflect.DeepEqual(plan.Stats, sortedStatsTargets(prior.Stats)) {
		setActions = append(setActions, ObserveAction{Kind: ObserveStatsSet, Reason: "stats target set changed"})
	}

	for _, group := range [][]ObserveAction{eventStops, setActions, unfollows, forgets, follows, eventStarts} {
		sort.SliceStable(group, func(i, j int) bool {
			if group[i].InstanceID != group[j].InstanceID {
				return group[i].InstanceID < group[j].InstanceID
			}
			return group[i].ContainerID < group[j].ContainerID
		})
		plan.Actions = append(plan.Actions, group...)
	}
	return plan
}

func sortedProbeTargets(m map[string]caphealth.Target) []caphealth.Target {
	out := make([]caphealth.Target, 0, len(m))
	for _, t := range m {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].InstanceID < out[j].InstanceID })
	return out
}

func sortedStatsTargets(m map[string]resources.Target) []resources.Target {
	out := make([]resources.Target, 0, len(m))
	for _, t := range m {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].InstanceID < out[j].InstanceID })
	return out
}

// observeState is the Reconciler's record of what it has applied. It is only
// touched under rotationMu (and by stopFollows, which takes it).
type observeState struct {
	known     map[string]struct{}
	events    map[string]struct{}
	probe     map[string]caphealth.Target
	stats     map[string]resources.Target
	following map[string]map[container.ContainerID]*logFollow

	// followCtx parents every follower. It outlives the pass that created it
	// (a follower is long-lived), so it is detached from that pass's
	// cancellation and ended only by stopFollows.
	followCtx    context.Context
	followCancel context.CancelFunc
}

// logFollow is one running log follower. It stays recorded after the stream
// ends on its own: a container that exited has been captured, and following
// it again would replay its whole log.
type logFollow struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func newObserveState() *observeState {
	return &observeState{
		known:     make(map[string]struct{}),
		events:    make(map[string]struct{}),
		probe:     make(map[string]caphealth.Target),
		stats:     make(map[string]resources.Target),
		following: make(map[string]map[container.ContainerID]*logFollow),
	}
}

func (s *observeState) snapshot() observedState {
	out := observedState{
		Known:     make(map[string]struct{}, len(s.known)),
		Events:    make(map[string]struct{}, len(s.events)),
		Probe:     s.probe,
		Stats:     s.stats,
		Following: make(map[string]map[container.ContainerID]struct{}, len(s.following)),
	}
	for id := range s.known {
		out.Known[id] = struct{}{}
	}
	for id := range s.events {
		out.Events[id] = struct{}{}
	}
	for id, byContainer := range s.following {
		set := make(map[container.ContainerID]struct{}, len(byContainer))
		for containerID := range byContainer {
			set[containerID] = struct{}{}
		}
		out.Following[id] = set
	}
	return out
}

// ObservePass points the observers at the live generations.
//
// Callers: the run loop calls it after the attach half of every pass. In
// manual posture the loop is inert, so whatever drives discovery and the
// attach pass calls this too; there it observes exactly the instances attach
// does — those Discover reports `matched` — and withdraws the rest, so a
// `wrong_generation` container (running something other than the approved
// plugin) gets no stream, no probe and no stats.
func (r *Reconciler) ObservePass(ctx context.Context) ([]ObserveAction, error) {
	r.rotationMu.Lock()
	defer r.rotationMu.Unlock()
	return r.observePassLocked(ctx)
}

func (r *Reconciler) observePassLocked(ctx context.Context) ([]ObserveAction, error) {
	if r.observe == nil {
		return nil, nil
	}

	world, err := r.loadLiveWorld(ctx)
	if err != nil {
		return nil, fmt.Errorf("reconciler: observe: %w", err)
	}

	instances := make([]observeInstance, 0, len(world.serving))
	for i, sc := range world.serving {
		instances = append(instances, resolveObserveInstance(sc, world.candidates[i], world.instanceByID))
	}

	plan := planObserve(instances, r.observed.snapshot())

	var applied []ObserveAction
	var errs []error
	for _, action := range plan.Actions {
		ran, err := r.applyObserve(ctx, action, plan)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if ran {
			applied = append(applied, action)
		}
	}

	known := make(map[string]struct{}, len(instances))
	for _, inst := range instances {
		known[inst.InstanceID] = struct{}{}
	}
	r.observed.known = known

	r.publishObservePass(len(applied), len(errs))
	return applied, errors.Join(errs...)
}

func resolveObserveInstance(sc servingContainer, cand attachInstance, instanceByID map[string]db.PluginInstance) observeInstance {
	out := observeInstance{InstanceID: sc.InstanceID, HoldReason: cand.HoldReason}

	instance, ok := instanceByID[sc.InstanceID]
	if !ok {
		return out
	}

	for _, c := range sc.Containers {
		out.Containers = append(out.Containers, logcapture.Target{
			InstanceID:  sc.InstanceID,
			PluginID:    instance.PluginID,
			ContainerID: c.ID,
			Generation:  c.Generation,
		})
	}
	sort.Slice(out.Containers, func(i, j int) bool { return out.Containers[i].ContainerID < out.Containers[j].ContainerID })

	if cand.HoldReason != "" {
		return out
	}

	profiles := cand.Manifest.Profiles
	out.EventSource = profiles.EventSource != nil

	var declared []caphealth.Profile
	for _, name := range profiles.Declared() {
		declared = append(declared, caphealth.Profile(name))
	}
	var attested []string
	if out.EventSource {
		for _, kind := range cand.Manifest.EventKinds {
			attested = append(attested, kind.Kind)
		}
	}
	out.Probe = caphealth.Target{
		InstanceID:         sc.InstanceID,
		ContainerID:        string(sc.ContainerID),
		AttestedEventKinds: attested,
		Profiles:           declared,
	}
	out.Stats = resources.Target{
		InstanceID:   sc.InstanceID,
		InstanceName: instance.InstanceName,
		PluginID:     instance.PluginID,
		ContainerID:  sc.ContainerID,
	}
	return out
}

// applyObserve applies one step and reports whether it did anything: a step for
// an observer that is not configured is skipped, not counted.
func (r *Reconciler) applyObserve(ctx context.Context, action ObserveAction, plan observePlan) (bool, error) {
	deps := r.observe
	st := r.observed

	switch action.Kind {
	case ObserveEventsStart:
		if deps.Events == nil {
			return false, nil
		}
		deps.Events.Start(ctx, action.InstanceID)
		st.events[action.InstanceID] = struct{}{}

	case ObserveEventsStop:
		if deps.Events == nil {
			return false, nil
		}
		deps.Events.Stop(action.InstanceID)
		delete(st.events, action.InstanceID)

	case ObserveProbeSet:
		if deps.Probe == nil {
			return false, nil
		}
		deps.Probe.SetTargets(plan.Probe)
		st.probe = make(map[string]caphealth.Target, len(plan.Probe))
		for _, t := range plan.Probe {
			st.probe[t.InstanceID] = t
		}

	case ObserveStatsSet:
		if deps.Stats == nil {
			return false, nil
		}
		deps.Stats.SetTargets(plan.Stats)
		st.stats = make(map[string]resources.Target, len(plan.Stats))
		for _, t := range plan.Stats {
			st.stats[t.InstanceID] = t
		}

	case ObserveLogFollow:
		if deps.Logs == nil {
			return false, nil
		}
		r.startFollow(ctx, action.Log)

	case ObserveLogUnfollow:
		if deps.Logs == nil {
			return false, nil
		}
		r.stopFollow(action.InstanceID, action.ContainerID)

	case ObserveForget:
		if deps.Logs != nil {
			deps.Logs.Forget(action.InstanceID)
		}
		if deps.Health != nil {
			deps.Health.Forget(action.InstanceID)
		}

	default:
		return false, fmt.Errorf("reconciler: observe: unknown action %q", action.Kind)
	}

	logctx.Logger(ctx).InfoContext(ctx, "reconciler: observe: "+string(action.Kind),
		"instance_id", action.InstanceID, "reason", action.Reason)
	return true, nil
}

// startFollow runs one follower. Its context is detached from the pass: the
// follower must outlive it, and is ended by stopFollow, stopFollows or the
// container's own exit.
func (r *Reconciler) startFollow(ctx context.Context, target logcapture.Target) {
	st := r.observed
	if st.followCtx == nil {
		st.followCtx, st.followCancel = context.WithCancel(context.WithoutCancel(ctx))
	}
	followCtx, cancel := context.WithCancel(st.followCtx)
	f := &logFollow{cancel: cancel, done: make(chan struct{})}

	if st.following[target.InstanceID] == nil {
		st.following[target.InstanceID] = make(map[container.ContainerID]*logFollow)
	}
	st.following[target.InstanceID][target.ContainerID] = f

	logs := r.observe.Logs
	go func() {
		defer close(f.done)
		if err := logs.Follow(followCtx, target); err != nil {
			slog.WarnContext(followCtx, "reconciler: observe: following container logs failed",
				"instance_id", target.InstanceID, "container_id", string(target.ContainerID), "err", err)
		}
	}()
}

// stopFollow ends one follower and waits for it, so a Forget that follows
// cannot race a line still being recorded.
func (r *Reconciler) stopFollow(instanceID string, containerID container.ContainerID) {
	st := r.observed
	f, ok := st.following[instanceID][containerID]
	if !ok {
		return
	}
	f.cancel()
	<-f.done
	delete(st.following[instanceID], containerID)
	if len(st.following[instanceID]) == 0 {
		delete(st.following, instanceID)
	}
}

// stopFollows ends every follower and waits for them. The run loop calls it on
// exit and Stop calls it too, so a stopped reconciler leaves no goroutine
// reading a container's logs.
func (r *Reconciler) stopFollows() {
	r.rotationMu.Lock()
	defer r.rotationMu.Unlock()

	st := r.observed
	if st == nil {
		return
	}
	if st.followCancel != nil {
		st.followCancel()
	}
	for id, byContainer := range st.following {
		for containerID, f := range byContainer {
			f.cancel()
			<-f.done
			delete(byContainer, containerID)
		}
		delete(st.following, id)
	}
	st.followCtx, st.followCancel = nil, nil
}

func (r *Reconciler) publishObservePass(applied, errCount int) {
	if r.publisher == nil {
		return
	}
	data, err := json.Marshal(struct {
		ActionCount int `json:"action_count"`
		Errors      int `json:"errors"`
	}{ActionCount: applied, Errors: errCount})
	if err != nil {
		slog.Warn("reconciler: marshalling observe pass event failed", "err", err)
		return
	}
	r.publisher.Publish(EventObservePassCompleted, data)
}
