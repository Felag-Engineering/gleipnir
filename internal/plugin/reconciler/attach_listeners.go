package reconciler

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/infra/logctx"
	"github.com/felag-engineering/gleipnir/internal/plugin/container"
	"github.com/felag-engineering/gleipnir/internal/plugin/egress"
	"github.com/felag-engineering/gleipnir/internal/plugin/manifest"
)

// The listener half of the attach pass (#957) binds the two host-side
// services a plugin reaches Gleipnir through — the host endpoint and the egress
// proxy — to each instance's network, and keys the egress gateway registry so
// the proxy can tell whose connection it is serving.
//
// Everything here is bound to Gleipnir's RESERVED address on the instance
// network (egress.GleipnirAddrOf, .2), never the network's gateway (.1) and
// never a wildcard. That address exists only once self-attach has pinned it, so
// the listeners come up after the core loop's ActionAttachSelf and go away once
// Gleipnir has left the network. Until then the pass holds quietly: "not yet" is
// the normal state of a freshly created network, not a fault.
//
// Manual posture has no listeners at all. There Gleipnir neither creates the
// network nor pins its own address on it (the operator's compose file attaches
// it), so the .2 this design depends on is not a guarantee, and the host
// endpoint's per-generation tokens do not exist without generations. An operator
// running manual mode wires their own proxy address, as docs/developer/manual-mode.md
// describes.

// InstanceListeners is the per-instance listener lifecycle the attach pass
// drives. *hostendpoint.ListenerSet satisfies it; it is an interface here
// because hostendpoint imports this package. Both the host endpoint and the
// egress proxy use a ListenerSet, so the wildcard refusal
// (hostendpoint.ErrWildcardAddr) protects both.
type InstanceListeners interface {
	Add(instanceID, addr string) (string, error)
	Addr(instanceID string) string
	Instances() []string
	Remove(ctx context.Context, instanceID string) error
}

// GatewayTable is the egress registry the attach pass keeps in step with the
// listeners. *egress.GatewayRegistry satisfies it.
type GatewayTable interface {
	Set(gateway net.IP, instanceID string, list egress.Allowlist)
	Remove(gateway net.IP)
	InstanceForGateway(localIP net.IP) (string, egress.Allowlist, bool)
}

// GenerationSlots is the host-endpoint generation controller. Registration
// keeps hostendpoint.RequireGenerationSlot from answering 503 for a live
// instance; BeginDrain lets a rotation's drain step wait out in-flight calls.
// *generation.Controller satisfies it.
type GenerationSlots interface {
	RegisterInstance(instanceID string) uint64
	UnregisterInstance(instanceID string)
	BeginDrain(ctx context.Context, instanceID string, grace time.Duration) (uint64, bool, error)
}

// listenerShutdownTimeout bounds a graceful listener removal. A host-endpoint
// call in flight must not hold up the teardown of an instance that is going
// away.
const listenerShutdownTimeout = 5 * time.Second

// attachListener is one instance's listener state: what should exist, and what
// does. It is the planner's whole input for the listener half.
type attachListener struct {
	InstanceID string

	// Hold is set when the pass cannot decide this instance now (self-attach
	// not pinned yet, status unknown, ambiguous network). Existing listeners
	// are left exactly as they are.
	Hold string

	// Want is true when the instance has a live network on which Gleipnir holds
	// its reserved address. False means everything is torn down.
	Want bool

	// GleipnirIP and the two bind addresses are set when Want is true.
	GleipnirIP net.IP
	HostAddr   string
	EgressAddr string
	// Allowlist is built from the consented manifest snapshot.
	Allowlist egress.Allowlist

	// Which pieces this reconciler is configured to drive.
	ManageHost, ManageEgress, ManageGateway bool

	// Observed state. Empty / nil means absent.
	HostBound    string
	EgressBound  string
	GatewayBound net.IP
	GatewayList  egress.Allowlist
}

// planListeners decides the listener steps for one instance. Pure, like the
// rest of the planner. The order within an instance is load-bearing: stale
// things are removed first, the gateway entry is set last (so the proxy never
// resolves an instance whose listener is not up), and on teardown the gateway
// entry goes first (so a closing proxy listener never has a live identity).
func planListeners(l attachListener) []AttachAction {
	act := func(kind AttachActionKind, reason string) AttachAction {
		return AttachAction{Kind: kind, InstanceID: l.InstanceID, Reason: reason}
	}

	if l.Hold != "" {
		return []AttachAction{act(AttachNone, l.Hold)}
	}

	var out []AttachAction
	if !l.Want {
		if l.ManageGateway && l.GatewayBound != nil {
			a := act(AttachGatewayRemove, "network is gone or gleipnir has left it")
			a.GatewayIP = l.GatewayBound
			out = append(out, a)
		}
		if l.ManageEgress && l.EgressBound != "" {
			out = append(out, act(AttachEgressUnlisten, "network is gone or gleipnir has left it"))
		}
		if l.ManageHost && l.HostBound != "" {
			out = append(out, act(AttachHostUnlisten, "network is gone or gleipnir has left it"))
		}
		return out
	}

	gatewayStale := l.ManageGateway && l.GatewayBound != nil && !l.GatewayBound.Equal(l.GleipnirIP)
	if gatewayStale {
		a := act(AttachGatewayRemove, "gleipnir's address on the network changed")
		a.GatewayIP = l.GatewayBound
		out = append(out, a)
	}
	if l.ManageHost {
		out = append(out, planBind(l.InstanceID, l.HostBound, l.HostAddr, AttachHostUnlisten, AttachHostListen)...)
	}
	if l.ManageEgress {
		out = append(out, planBind(l.InstanceID, l.EgressBound, l.EgressAddr, AttachEgressUnlisten, AttachEgressListen)...)
	}
	if l.ManageGateway {
		if gatewayStale || l.GatewayBound == nil || !l.GatewayList.Equal(l.Allowlist) {
			a := act(AttachGatewaySet, "registering the consented egress allowlist")
			a.GatewayIP = l.GleipnirIP
			a.Allowlist = l.Allowlist
			out = append(out, a)
		}
	}
	return out
}

func planBind(instanceID, bound, want string, unlisten, listen AttachActionKind) []AttachAction {
	switch {
	case bound == want:
		return nil
	case bound == "":
		return []AttachAction{{Kind: listen, InstanceID: instanceID, ListenAddr: want, Reason: "network is live and gleipnir holds its address"}}
	default:
		return []AttachAction{
			{Kind: unlisten, InstanceID: instanceID, Reason: "bound to a different address than gleipnir's reserved one"},
			{Kind: listen, InstanceID: instanceID, ListenAddr: want, Reason: "rebinding to gleipnir's reserved address"},
		}
	}
}

func (r *Reconciler) listenersEnabled() bool {
	if r.posture == container.PostureManual {
		return false
	}
	return r.hostListeners != nil || r.egressListeners != nil || r.gateways != nil
}

// resolveListeners reads the world for the listener planner. The returned
// errors are instances whose consented snapshot could not produce an allowlist;
// those are planned as torn down (fail closed) and reported to the caller.
func (r *Reconciler) resolveListeners(ctx context.Context, instanceByID map[string]db.PluginInstance, pluginByID map[string]db.Plugin) ([]attachListener, []error) {
	if !r.listenersEnabled() {
		return nil, nil
	}

	desired, err := r.store.ListPluginContainers(ctx)
	if err != nil {
		return nil, []error{fmt.Errorf("reconciler: attach: listing desired containers for listeners: %w", err)}
	}
	desiredIDs := make(map[string]struct{}, len(desired))
	for _, row := range desired {
		desiredIDs[row.PluginInstanceID] = struct{}{}
	}

	networks, err := r.runtime.ListNetworksByLabel(ctx, LabelManaged, ManagedValue)
	if err != nil {
		return nil, []error{fmt.Errorf("reconciler: attach: listing managed networks for listeners: %w", err)}
	}
	networksByInstance := make(map[string][]container.NetworkInfo)
	for _, n := range networks {
		if id := n.Labels[LabelInstance]; id != "" {
			networksByInstance[id] = append(networksByInstance[id], n)
		}
	}

	selfUnknown := false
	var selfInfo *container.ContainerInfo
	if r.selfAttacher != nil {
		info, err := r.runtime.Inspect(ctx, r.selfAttacher.ContainerID())
		if err != nil {
			logctx.Logger(ctx).WarnContext(ctx, "reconciler: attach: inspecting gleipnir's own container failed; holding listeners", "err", err)
			selfUnknown = true
		} else {
			selfInfo = &info
		}
	}

	// Union of everything that could need a decision. A listener whose instance
	// has vanished from every other source is found only through the set itself.
	ids := make(map[string]struct{})
	for id := range desiredIDs {
		ids[id] = struct{}{}
	}
	if r.hostListeners != nil {
		for _, id := range r.hostListeners.Instances() {
			ids[id] = struct{}{}
		}
	}
	if r.egressListeners != nil {
		for _, id := range r.egressListeners.Instances() {
			ids[id] = struct{}{}
		}
	}
	sorted := make([]string, 0, len(ids))
	for id := range ids {
		sorted = append(sorted, id)
	}
	sort.Strings(sorted)

	var out []attachListener
	var errs []error
	for _, id := range sorted {
		l := attachListener{
			InstanceID:    id,
			ManageHost:    r.hostListeners != nil,
			ManageEgress:  r.egressListeners != nil,
			ManageGateway: r.gateways != nil,
		}
		if r.hostListeners != nil {
			l.HostBound = r.hostListeners.Addr(id)
		}
		if r.egressListeners != nil {
			l.EgressBound = r.egressListeners.Addr(id)
		}
		r.observeGateway(&l)

		anyBound := l.HostBound != "" || l.EgressBound != "" || l.GatewayBound != nil
		if err := r.decideListener(ctx, &l, desiredIDs, networksByInstance[id], selfInfo, selfUnknown, anyBound, instanceByID, pluginByID); err != nil {
			errs = append(errs, fmt.Errorf("reconciler: attach: listeners for instance %s: %w", id, err))
		}
		out = append(out, l)
	}
	return out, errs
}

// decideListener fills in Want / Hold and the desired addresses. A non-nil
// error means the instance is planned as torn down (l.Want is false).
func (r *Reconciler) decideListener(
	ctx context.Context, l *attachListener,
	desiredIDs map[string]struct{}, nets []container.NetworkInfo,
	selfInfo *container.ContainerInfo, selfUnknown, anyBound bool,
	instanceByID map[string]db.PluginInstance, pluginByID map[string]db.Plugin,
) error {
	if _, ok := desiredIDs[l.InstanceID]; !ok {
		return nil // uninstalled: tear down
	}
	switch {
	case len(nets) == 0:
		return nil // network gone: tear down
	case len(nets) > 1:
		l.Hold = fmt.Sprintf("%d networks are labelled for this instance, want exactly one", len(nets))
		return nil
	}
	net0 := nets[0]

	if selfUnknown {
		l.Hold = "gleipnir's own network membership is unknown this pass"
		return nil
	}
	if selfInfo == nil || !container.SelfAttached(*selfInfo, net0) {
		if anyBound {
			return nil // gleipnir left the network; its address is gone
		}
		l.Hold = "gleipnir has not joined this instance's network yet (self-attach pending)"
		return nil
	}

	ip, prefix, err := gleipnirAddrFor(ctx, r.subnets, l.InstanceID)
	if err != nil {
		l.Hold = err.Error()
		return nil
	}
	if net0.Subnet != "" && net0.Subnet != prefix.String() {
		l.Hold = fmt.Sprintf("network reports subnet %s but %s is allocated", net0.Subnet, prefix)
		return nil
	}
	if ip.To4() == nil || ip.IsUnspecified() {
		return fmt.Errorf("computed bind address %q is not a concrete IPv4 address", ip)
	}

	instance, ok := instanceByID[l.InstanceID]
	if !ok {
		l.Hold = "instance row not found"
		return nil
	}
	plugin, ok := pluginByID[instance.PluginID]
	if !ok {
		l.Hold = "plugin row not found"
		return nil
	}
	list, err := allowlistFromSnapshot([]byte(plugin.ManifestSnapshot))
	if err != nil {
		return err
	}

	l.Want = true
	l.GleipnirIP = ip
	l.HostAddr = net.JoinHostPort(ip.String(), strconv.Itoa(DefaultHostEndpointPort))
	l.EgressAddr = net.JoinHostPort(ip.String(), strconv.Itoa(DefaultEgressProxyPort))
	l.Allowlist = list
	return nil
}

// allowlistFromSnapshot builds an instance's egress allowlist from the
// CONSENTED manifest snapshot, never from the manifest on disk: ADR-045's
// material-change block means the snapshot is what an admin approved, and a
// plugin that ships an update cannot widen its own egress by shipping one. A v1
// snapshot has no egress concept and is an error — treating it as "no grants"
// would be indistinguishable from a v2 plugin that really asked for none.
func allowlistFromSnapshot(snapshot []byte) (egress.Allowlist, error) {
	snap, err := manifest.Read(snapshot)
	if err != nil {
		return egress.Allowlist{}, fmt.Errorf("reading consented manifest snapshot: %w", err)
	}
	if snap.Version != 2 {
		return egress.Allowlist{}, fmt.Errorf("consented manifest snapshot is schema version %d; egress allowlists require version 2", snap.Version)
	}
	domains := make([]string, 0, len(snap.Egress))
	for _, grant := range snap.Egress {
		domains = append(domains, grant.Domain)
	}
	list, err := egress.NewAllowlist(domains)
	if err != nil {
		return egress.Allowlist{}, fmt.Errorf("compiling consented egress grants: %w", err)
	}
	return list, nil
}

// observeGateway finds the registry entry that belongs to l's instance. The
// registry is keyed by address and has no reverse lookup, so the candidates are
// the addresses the instance's listeners are bound to — a gateway entry exists
// only alongside them, because it is set last and removed first.
func (r *Reconciler) observeGateway(l *attachListener) {
	if r.gateways == nil {
		return
	}
	for _, bound := range []string{l.EgressBound, l.HostBound} {
		host, _, err := net.SplitHostPort(bound)
		if err != nil {
			continue
		}
		ip := net.ParseIP(host)
		if ip == nil {
			continue
		}
		owner, list, ok := r.gateways.InstanceForGateway(ip)
		if ok && owner == l.InstanceID {
			l.GatewayBound = ip
			l.GatewayList = list
			return
		}
	}
}

// applyListenerAction performs one listener step. Kinds that are not listener
// kinds are the caller's.
func (r *Reconciler) applyListenerAction(ctx context.Context, action AttachAction) error {
	switch action.Kind {
	case AttachHostListen:
		if _, err := r.hostListeners.Add(action.InstanceID, action.ListenAddr); err != nil {
			return fmt.Errorf("reconciler: attach: host endpoint listener for instance %s: %w", action.InstanceID, err)
		}
		if r.generations != nil {
			r.generations.RegisterInstance(action.InstanceID)
		}
	case AttachHostUnlisten:
		if err := r.removeListener(ctx, r.hostListeners, action.InstanceID); err != nil {
			return fmt.Errorf("reconciler: attach: removing host endpoint listener for instance %s: %w", action.InstanceID, err)
		}
		if r.generations != nil {
			r.generations.UnregisterInstance(action.InstanceID)
		}
	case AttachEgressListen:
		if _, err := r.egressListeners.Add(action.InstanceID, action.ListenAddr); err != nil {
			return fmt.Errorf("reconciler: attach: egress proxy listener for instance %s: %w", action.InstanceID, err)
		}
	case AttachEgressUnlisten:
		if err := r.removeListener(ctx, r.egressListeners, action.InstanceID); err != nil {
			return fmt.Errorf("reconciler: attach: removing egress proxy listener for instance %s: %w", action.InstanceID, err)
		}
	case AttachGatewaySet:
		r.gateways.Set(action.GatewayIP, action.InstanceID, action.Allowlist)
	case AttachGatewayRemove:
		r.gateways.Remove(action.GatewayIP)
	default:
		return fmt.Errorf("reconciler: attach: unknown listener action %q for instance %s", action.Kind, action.InstanceID)
	}
	return nil
}

func (r *Reconciler) removeListener(ctx context.Context, set InstanceListeners, instanceID string) error {
	shutdownCtx, cancel := context.WithTimeout(ctx, listenerShutdownTimeout)
	defer cancel()
	err := set.Remove(shutdownCtx, instanceID)
	if err != nil && errors.Is(err, context.DeadlineExceeded) {
		logctx.Logger(ctx).WarnContext(ctx, "reconciler: attach: listener did not drain before the shutdown timeout",
			"instance_id", instanceID)
		return nil
	}
	return err
}

// hostCallDrainGrace is how long a rotation's drain step lets in-flight
// host-endpoint calls of the superseded generation finish before they are
// force-cancelled.
const hostCallDrainGrace = 5 * time.Second

// drainHostCalls pauses the instance's host-endpoint traffic and waits out the
// superseded generation's in-flight calls, so stopping its container does not
// cut a callback off mid-flight. A no-op without a generation controller.
func (r *Reconciler) drainHostCalls(ctx context.Context, instanceID string) error {
	if r.generations == nil {
		return nil
	}
	// Idempotent and never resets the counter; guards an instance whose listener
	// has not been registered yet from failing the drain on "not registered".
	r.generations.RegisterInstance(instanceID)
	if _, _, err := r.generations.BeginDrain(ctx, instanceID, hostCallDrainGrace); err != nil {
		return fmt.Errorf("draining host endpoint calls for instance %s: %w", instanceID, err)
	}
	return nil
}
