package hostsvc

import "context"

// contextBinder implements InstanceBinder by reading the instance ID resolved
// by UnaryInstanceTokenInterceptor from the request context. This replaces the
// UDS-peer-identity framing from the original stub: identity is now verified
// per-call via the gleipnir-instance-token metadata key (spec §8.4).
type contextBinder struct{}

// InstanceIDFromContext returns the plugin instance ID attached to ctx by
// UnaryInstanceTokenInterceptor. Returns ("", false) when no identity has been
// set (e.g. the interceptor was not in the chain, or the token was missing/invalid).
func (contextBinder) InstanceIDFromContext(ctx context.Context) (string, bool) {
	return InstanceIDFromTokenContext(ctx)
}

// NewContextBinder returns an InstanceBinder that reads instance identity from
// the per-call context value set by UnaryInstanceTokenInterceptor.
//
// The loader/subprocess wiring (#158) passes this to NewServer alongside a
// real *identity.Registry, so every Host RPC is authenticated against the
// token the broker assigned at subprocess launch. The plugin-disabled path in
// main.go is unchanged (gated by GLEIPNIR_PLUGINS_ENABLED=false).
func NewContextBinder() InstanceBinder {
	return contextBinder{}
}
