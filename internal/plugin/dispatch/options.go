package dispatch

import (
	"context"
	"fmt"
	"time"

	optionsv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/options/v1"
)

// OptionsClient proxies ConfigOptionsService.ListOptions calls to plugin
// instances over gRPC connections from the same connFactory used by the channel
// Dispatcher and tool Pool.
//
// Connections are cached per instance name (lazy dial, double-check semantics)
// via connCache, exactly like the channel Dispatcher. Close() drains the cache.
//
// Per-call deadline matches GLEIPNIR_MCP_TIMEOUT (injected via callTimeout).
// The host endpoint applies its own 30s deadline on top; the per-call deadline
// is a tighter guardrail for individual plugin round-trips.
type OptionsClient struct {
	callTimeout time.Duration
	conns       *connCache[optionsv1.ConfigOptionsServiceClient]

	// newClient is an injectable factory for the gRPC client, enabling test
	// injection without standing up a real gRPC server. nil means use
	// conns.getOrConnect (production path).
	newClient func(instanceName string) (optionsv1.ConfigOptionsServiceClient, error)
}

// NewOptionsClient creates an OptionsClient ready to use.
// connect is the ConnFactory that dials plugin instances (shared with the
// channel Dispatcher and tool Pool). callTimeout is applied per ListOptions call;
// a zero value defaults to 30s.
//
// For tests, use NewOptionsClientWithFactory to inject a fake client directly.
func NewOptionsClient(connect ConnFactory, callTimeout time.Duration) *OptionsClient {
	if callTimeout == 0 {
		callTimeout = 30 * time.Second
	}
	return &OptionsClient{
		callTimeout: callTimeout,
		conns:       newConnCache(connect, optionsv1.NewConfigOptionsServiceClient),
	}
}

// NewOptionsClientWithFactory creates an OptionsClient that obtains its gRPC
// clients from the provided factory function instead of dialing real gRPC
// connections. Intended for tests only.
func NewOptionsClientWithFactory(factory func(instanceName string) (optionsv1.ConfigOptionsServiceClient, error), callTimeout time.Duration) *OptionsClient {
	if callTimeout == 0 {
		callTimeout = 30 * time.Second
	}
	return &OptionsClient{
		callTimeout: callTimeout,
		// conns is nil — never consulted when newClient is set.
		conns:     newConnCache[optionsv1.ConfigOptionsServiceClient](nil, nil),
		newClient: factory,
	}
}

// optionsGRPCClient returns the gRPC client for the given instance name.
// Uses the injected factory when set (test path), otherwise dials via conns.
func (c *OptionsClient) optionsGRPCClient(instanceName string) (optionsv1.ConfigOptionsServiceClient, error) {
	if c.newClient != nil {
		return c.newClient(instanceName)
	}
	return c.conns.getOrConnect(instanceName)
}

// ListOptions calls the plugin's ConfigOptionsService.ListOptions RPC and
// returns the response. instanceName is the plugin instance name (used as the
// connection key). The call is bounded by c.callTimeout.
//
// The caller is responsible for graceful degradation: codes.Unimplemented and
// codes.Unavailable from the plugin indicate "no provider" and should be
// converted to a degraded response by the admin handler.
func (c *OptionsClient) ListOptions(ctx context.Context, instanceName, instanceID, source, query, cursor string) (*optionsv1.ListOptionsResponse, error) {
	client, err := c.optionsGRPCClient(instanceName)
	if err != nil {
		return nil, fmt.Errorf("connecting to plugin instance %q for options: %w", instanceName, err)
	}

	callCtx, cancel := context.WithTimeout(ctx, c.callTimeout)
	defer cancel()

	resp, err := client.ListOptions(callCtx, &optionsv1.ListOptionsRequest{
		InstanceId: instanceID,
		Source:     source,
		Query:      query,
		Cursor:     cursor,
	})
	if err != nil {
		return nil, fmt.Errorf("ListOptions(instance=%q, source=%q): %w", instanceName, source, err)
	}
	return resp, nil
}

// Close closes every cached gRPC connection opened by the options client.
// Safe (no-op) when constructed with NewOptionsClientWithFactory (test path).
// Idempotent: a second call returns nil and does not panic.
func (c *OptionsClient) Close() error { return c.conns.closeAll() }
