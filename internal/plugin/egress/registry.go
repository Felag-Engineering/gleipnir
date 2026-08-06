package egress

import (
	"net"
	"sync"
)

// GatewayRegistry maps each managed network's gateway address to the instance
// that owns it, and holds that instance's consented allowlist.
//
// It is in-memory and rebuilt from the database by whoever owns the reconcile
// loop. Deliberately not a cache with a TTL: an allowlist that is stale in the
// permissive direction is a grant an admin revoked and the plugin still has, so
// the refresh is a push (Set/Remove on a converge pass), not a pull.
type GatewayRegistry struct {
	mu      sync.RWMutex
	entries map[string]gatewayEntry // keyed by gateway IP string
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

// GatewayOf returns the address Gleipnir occupies on a per-instance subnet.
//
// The reconciler allocates a /24 per instance and the runtime assigns the
// gateway the first usable address in it. Deriving it here rather than reading
// it back from the runtime keeps the proxy's identity table buildable from the
// database alone — which matters on a cold start, before anything has been
// inspected.
func GatewayOf(subnet string) (net.IP, error) {
	_, network, err := net.ParseCIDR(subnet)
	if err != nil {
		return nil, err
	}
	gateway := make(net.IP, len(network.IP))
	copy(gateway, network.IP)
	gateway[len(gateway)-1]++
	return gateway, nil
}
