package egress

import (
	"net"
	"sync"
)

// GatewayRegistry maps the address Gleipnir occupies on each managed
// network (GleipnirAddrOf — despite the type's name, NOT the network's own
// gateway address; see that function's doc) to the instance that owns it, and
// holds that instance's consented allowlist. The proxy's listener binds
// Gleipnir's address on every instance network, so a connection's LocalAddr
// is exactly the key this registry looks up on (see egress-containment.md).
//
// It is in-memory and rebuilt from the database by whoever owns the reconcile
// loop. Deliberately not a cache with a TTL: an allowlist that is stale in the
// permissive direction is a grant an admin revoked and the plugin still has, so
// the refresh is a push (Set/Remove on a converge pass), not a pull.
type GatewayRegistry struct {
	mu      sync.RWMutex
	entries map[string]gatewayEntry // keyed by Gleipnir's address on the network, as a string
}

type gatewayEntry struct {
	instanceID string
	list       Allowlist
}

func NewGatewayRegistry() *GatewayRegistry {
	return &GatewayRegistry{entries: make(map[string]gatewayEntry)}
}

// Set records (or replaces) the mapping for one instance's network gateway.
func (r *GatewayRegistry) Set(gateway net.IP, instanceID string, list Allowlist) {
	if gateway == nil || instanceID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[gateway.String()] = gatewayEntry{instanceID: instanceID, list: list}
}

// Remove drops a gateway mapping. An instance whose mapping is gone resolves to
// nothing, and the proxy fails closed on it — which is the correct behavior for
// an instance that was just deleted while a connection was in flight.
func (r *GatewayRegistry) Remove(gateway net.IP) {
	if gateway == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.entries, gateway.String())
}

// Replace swaps the whole table atomically. This is the shape a level-triggered
// reconcile pass wants: it re-derives the world every pass, and an entry that
// is simply absent from the new world should disappear rather than linger
// because nothing thought to delete it.
func (r *GatewayRegistry) Replace(entries map[string]Allowlist, instanceByGateway map[string]string) {
	next := make(map[string]gatewayEntry, len(entries))
	for gateway, list := range entries {
		instanceID, ok := instanceByGateway[gateway]
		if !ok || instanceID == "" {
			continue
		}
		next[gateway] = gatewayEntry{instanceID: instanceID, list: list}
	}
	r.mu.Lock()
	r.entries = next
	r.mu.Unlock()
}

// InstanceForGateway implements Resolver.
func (r *GatewayRegistry) InstanceForGateway(localIP net.IP) (string, Allowlist, bool) {
	if localIP == nil {
		return "", Allowlist{}, false
	}
	r.mu.RLock()
	entry, ok := r.entries[localIP.String()]
	r.mu.RUnlock()
	if !ok {
		return "", Allowlist{}, false
	}
	return entry.instanceID, entry.list, true
}

// Len reports how many gateways are mapped.
func (r *GatewayRegistry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.entries)
}

// GleipnirAddrOf returns the address Gleipnir occupies on a per-instance
// subnet: the SECOND usable address, one past the network's own gateway.
//
// It is deliberately NOT the gateway address (the first usable address, e.g.
// .1). The gateway is the bridge interface itself — owned by the
// container-runtime daemon, not by any container — so no container can ever
// be assigned it; an implementation that assumed otherwise could not
// actually bind there. Gleipnir instead reserves the next address and PINS
// it at attach time (container.Runtime.ConnectNetwork's pinnedIPv4), so the
// address is deterministic regardless of attach order rather than whatever
// IPAM would hand out next.
//
// This is the ONE function that computes Gleipnir's address on an instance
// subnet — every consumer that needs to know where Gleipnir is reachable on a
// given instance's network (the egress proxy's own listener, ProxyEnv, the
// gateway registry despite its name, and eventually the host endpoint's bind
// address) must call this rather than re-deriving an offset locally, so a
// future change to which address Gleipnir reserves cannot go stale in one of
// them.
//
// Deriving it from the subnet CIDR alone (rather than reading it back from the
// runtime after attach) keeps this buildable from the database alone — which
// matters on a cold start, before anything has been inspected.
func GleipnirAddrOf(subnet string) (net.IP, error) {
	_, network, err := net.ParseCIDR(subnet)
	if err != nil {
		return nil, err
	}
	addr := make(net.IP, len(network.IP))
	copy(addr, network.IP)
	addr[len(addr)-1] += 2
	return addr, nil
}
