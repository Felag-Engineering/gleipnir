// Package process owns the per-instance subprocess lifecycle for plugin instances.
// It wraps plugin-sdk/hostwire (HashiCorp go-plugin) with identity-token issuance,
// per-instance log labelling, graceful Stop, and crash-health callbacks.
//
// Production wiring of a real hostsvc.Server is injected via Manager.HostServerFor
// from cmd/server (issue #295); NoopHostServer{} is the default for
// handshake-only tests.
package process

import (
	"google.golang.org/grpc"
)

// NoopHostServer is a hostwire.HostServer that registers no services on the
// broker-allocated gRPC server. Production wiring uses Manager.HostServerFor
// injected from cmd/server (issue #295); NoopHostServer{} remains the default
// for handshake-only tests.
type NoopHostServer struct{}

// Register satisfies hostwire.HostServer. It intentionally does nothing so the
// broker handshake can succeed without a real host service behind it.
func (NoopHostServer) Register(_ *grpc.Server) {}
