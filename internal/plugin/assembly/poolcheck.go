//go:build substratev2

package assembly

import (
	"context"
	"log/slog"
	"net/netip"

	"github.com/felag-engineering/gleipnir/internal/infra/netguard"
	"github.com/felag-engineering/gleipnir/internal/plugin/container"
	"github.com/felag-engineering/gleipnir/internal/plugin/egress"
	"github.com/felag-engineering/gleipnir/internal/plugin/reconciler"
)

// SelfAttachmentExclusions reports the plugin-network attachments Gleipnir's own
// container currently holds, in the form netguard.CheckPoolOverlapExcluding
// accepts. The startup pool-overlap check runs before New, and a restart that
// keeps those attachments would otherwise make that check refuse to start.
//
// Every fact comes from the container runtime, never from interface names or
// addresses on the host. An attachment is reported only if its network is
// Gleipnir-managed, internal and instance-labelled, its subnet is a /24 inside
// pool, and the address Gleipnir holds on it is the reserved one
// (egress.GleipnirAddrOf). Anything else is dropped with a WARN, so it still
// trips the overlap check.
//
// Every way of not knowing (no socket, manual posture, not in a container,
// runtime error) yields nil, which makes the overlap check behave exactly as it
// does without this function: it fails closed. Options are the ones New takes,
// so a caller and a test share one way to pin the posture and runtime.
func SelfAttachmentExclusions(ctx context.Context, pool netip.Prefix, opts ...Option) []netguard.ExcludedAttachment {
	var o options
	for _, opt := range opts {
		opt(&o)
	}

	detect := container.LoadDetectConfig()
	if o.detect != nil {
		detect = *o.detect
	}
	posture, err := container.DetectPosture(detect)
	if err != nil {
		slog.Debug("pool overlap check: no container runtime, no attachments excluded", "err", err)
		return nil
	}
	if posture.Posture == container.PostureManual {
		return nil
	}

	rt := o.runtime
	if rt == nil {
		docker, err := container.NewDockerRuntime(posture.SocketPath)
		if err != nil {
			slog.Warn("pool overlap check: cannot reach the container runtime, no attachments excluded", "socket", posture.SocketPath, "err", err)
			return nil
		}
		defer docker.Close()
		rt = docker
	}

	selfID := container.ContainerID("")
	if o.selfContainerID != nil {
		selfID = *o.selfContainerID
	} else {
		selfID, err = container.ResolveSelfContainerID(ctx, rt)
		if err != nil {
			slog.Warn("pool overlap check: cannot resolve gleipnir's own container, no attachments excluded", "err", err)
			return nil
		}
	}
	if selfID == "" {
		return nil
	}

	self, err := rt.Inspect(ctx, selfID)
	if err != nil {
		slog.Warn("pool overlap check: cannot inspect gleipnir's own container, no attachments excluded", "container", string(selfID), "err", err)
		return nil
	}
	networks, err := rt.ListNetworksByLabel(ctx, reconciler.LabelManaged, reconciler.ManagedValue)
	if err != nil {
		slog.Warn("pool overlap check: cannot list managed plugin networks, no attachments excluded", "err", err)
		return nil
	}

	var exclusions []netguard.ExcludedAttachment
	for _, network := range networks {
		if !container.SelfAttached(self, network) {
			continue
		}
		ex, reason := verifiedAttachment(pool, self, network)
		if reason != "" {
			slog.Warn("pool overlap check: attachment not excluded", "network", network.Name, "subnet", network.Subnet, "reason", reason)
			continue
		}
		exclusions = append(exclusions, ex)
	}
	return exclusions
}

// verifiedAttachment returns the exclusion for one attached network, or a
// non-empty reason it cannot be trusted.
func verifiedAttachment(pool netip.Prefix, self container.ContainerInfo, network container.NetworkInfo) (netguard.ExcludedAttachment, string) {
	if !network.Internal {
		return netguard.ExcludedAttachment{}, "network is not internal"
	}
	if network.Labels[reconciler.LabelInstance] == "" {
		return netguard.ExcludedAttachment{}, "network has no plugin instance label"
	}
	subnet, err := netip.ParsePrefix(network.Subnet)
	if err != nil {
		return netguard.ExcludedAttachment{}, "subnet is unreadable"
	}
	if subnet.Bits() != 24 || subnet != subnet.Masked() {
		return netguard.ExcludedAttachment{}, "subnet is not a /24"
	}
	if pool.Bits() > subnet.Bits() || !pool.Contains(subnet.Addr()) {
		return netguard.ExcludedAttachment{}, "subnet is outside the plugin subnet pool"
	}

	want, err := egress.GleipnirAddrOf(network.Subnet)
	if err != nil {
		return netguard.ExcludedAttachment{}, "cannot derive the reserved address"
	}
	wantAddr, ok := netip.AddrFromSlice(want)
	if !ok {
		return netguard.ExcludedAttachment{}, "cannot derive the reserved address"
	}
	wantAddr = wantAddr.Unmap()

	held, err := netip.ParseAddr(self.NetworkAddresses[network.Name])
	if err != nil {
		return netguard.ExcludedAttachment{}, "runtime reports no address for gleipnir on this network"
	}
	if held != wantAddr {
		return netguard.ExcludedAttachment{}, "gleipnir's address on this network is not the reserved " + wantAddr.String()
	}
	return netguard.ExcludedAttachment{Subnet: subnet, Addr: held}, ""
}
