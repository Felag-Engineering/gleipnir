package reconciler

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strconv"

	"github.com/felag-engineering/gleipnir/internal/infra/logctx"
	"github.com/felag-engineering/gleipnir/internal/plugin/egress"
)

// DefaultEgressProxyPort is the port the egress proxy listens on, on Gleipnir's
// reserved address of every instance network. A constant for the same reason
// DefaultHostEndpointPort is: host configuration lives in the app, not in
// environment variables.
const DefaultEgressProxyPort = 8118

// gleipnirAddrFor is the ONE place the attach pass and the two env hooks learn
// where Gleipnir is on an instance's network: egress.GleipnirAddrOf over the
// subnet the database allocated. The listeners bind it, the gateway registry is
// keyed by it, and the containers are told to dial it — three consumers that
// must never disagree, which is why none of them computes it themselves.
//
// It is Gleipnir's own reserved address (.2), deliberately never the network's
// gateway (.1). Under rootful Docker the gateway is the host: a host-endpoint
// URL pointed there would be exempted from the proxy by NO_PROXY and would send
// every generation's bearer token to whatever listens on the host.
func gleipnirAddrFor(ctx context.Context, subnets *SubnetAllocator, instanceID string) (net.IP, netip.Prefix, error) {
	if subnets == nil {
		return nil, netip.Prefix{}, fmt.Errorf("no subnet allocator configured")
	}
	prefix, found, err := subnets.Lookup(ctx, instanceID)
	if err != nil {
		return nil, netip.Prefix{}, fmt.Errorf("looking up subnet for instance %q: %w", instanceID, err)
	}
	if !found {
		return nil, netip.Prefix{}, fmt.Errorf("instance %q has no allocated subnet", instanceID)
	}
	addr, err := egress.GleipnirAddrOf(prefix.String())
	if err != nil {
		return nil, netip.Prefix{}, fmt.Errorf("computing gleipnir's address on %s: %w", prefix, err)
	}
	return addr, prefix, nil
}

// EgressEnv builds the Config.EgressEnv hook: the proxy variables a container
// is created with, pointing at Gleipnir's reserved address on that instance's
// network.
//
// An instance whose address cannot be computed gets no proxy environment. That
// is the safe direction: on an internal-only network no proxy means the
// instance reaches nothing.
func EgressEnv(subnets *SubnetAllocator, port int) func(ctx context.Context, instanceID string) []string {
	return func(ctx context.Context, instanceID string) []string {
		addr, _, err := gleipnirAddrFor(ctx, subnets, instanceID)
		if err != nil {
			logctx.Logger(ctx).WarnContext(ctx, "reconciler: no egress proxy environment for instance",
				"instance_id", instanceID, "err", err)
			return nil
		}
		return egress.ProxyEnv(addr, port)
	}
}

// HostEndpointEnv builds the Config.HostEndpointEnv hook: the URL a generation's
// SDK client calls for server→host callbacks, at Gleipnir's reserved address on
// the instance's network (see gleipnirAddrFor for why never the gateway).
//
// An instance whose address cannot be computed gets no URL rather than a wrong
// one; its plugin cannot call back until a later generation, which is a fault
// the operator can see, where a URL at the wrong host would be a leak.
func HostEndpointEnv(subnets *SubnetAllocator, port int) func(ctx context.Context, instanceID string) []string {
	return func(ctx context.Context, instanceID string) []string {
		addr, _, err := gleipnirAddrFor(ctx, subnets, instanceID)
		if err != nil {
			logctx.Logger(ctx).WarnContext(ctx, "reconciler: no host endpoint URL for instance",
				"instance_id", instanceID, "err", err)
			return nil
		}
		return []string{hostEndpointURLEnvVar + "=http://" + net.JoinHostPort(addr.String(), strconv.Itoa(port))}
	}
}
