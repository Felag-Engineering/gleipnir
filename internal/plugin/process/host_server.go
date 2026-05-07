// Package process owns the per-instance subprocess lifecycle for plugin instances.
// It wraps plugin-sdk/hostwire (HashiCorp go-plugin) with identity-token issuance,
// per-instance log labelling, graceful Stop, and crash-health callbacks.
//
// The HostServer seam allows #292 to inject hostsvc.Server without changing this
// package. For #291 we ship NoopHostServer as the default so the go-plugin broker
// handshake completes without functional Host RPCs yet.
package process

import (
	"google.golang.org/grpc"
)

// NoopHostServer is a hostwire.HostServer that registers no services on the
// broker-allocated gRPC server. It is the default host server for #291 wiring;
// #292 will replace it with hostsvc.Server once the Host RPC surface is wired.
type NoopHostServer struct{}

// Register satisfies hostwire.HostServer. It intentionally does nothing so the
// broker handshake can succeed without a real host service behind it.
func (NoopHostServer) Register(_ *grpc.Server) {}
