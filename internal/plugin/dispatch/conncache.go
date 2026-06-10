package dispatch

import (
	"fmt"
	"sync"

	"google.golang.org/grpc"
)

// connEntry pairs a gRPC connection with the client that wraps it.
//
// The conn is stored alongside the client so that Close can call conn.Close().
// Storing only the client (the original channel.go bug, issue #497) makes the
// underlying connection unreachable after the entry is cached, so it leaks on
// shutdown.
type connEntry[T any] struct {
	conn   *grpc.ClientConn
	client T
}

// connCache is a lazily-populated, concurrency-safe cache that maps instance
// names to gRPC client connections.  It implements the fast-path / lock /
// double-check / connect / cache-close-loser pattern that was previously
// duplicated in channel.go (channel-path) and pool.go (tool-path).
//
// The generic parameter T is the gRPC client type (e.g.
// channelv1.ChannelServiceClient).  newClient wraps a *grpc.ClientConn into T
// exactly once per winning entry; losers of the double-check race have their
// connection closed immediately to avoid leaking transport goroutines.
//
// newClient accepts grpc.ClientConnInterface (not *grpc.ClientConn) because
// gRPC-generated NewXxxServiceClient functions take the interface, and
// *grpc.ClientConn implements it.  The conn field still holds the concrete
// *grpc.ClientConn so Close can call conn.Close().
type connCache[T any] struct {
	mu        sync.Mutex
	entries   map[string]connEntry[T]
	newClient func(grpc.ClientConnInterface) T
	connect   ConnFactory
}

// newConnCache returns a connCache ready to use.  connect may be nil in the
// test path when NewChannelClient is set on the Dispatcher — getOrConnect is
// never reached in that case.
func newConnCache[T any](connect ConnFactory, newClient func(grpc.ClientConnInterface) T) *connCache[T] {
	return &connCache[T]{
		entries:   make(map[string]connEntry[T]),
		newClient: newClient,
		connect:   connect,
	}
}

// getOrConnect returns the cached client for instanceName, dialing once if
// needed.  Concurrent callers that race on the same name all dial, but only
// the first to re-acquire the lock survives in the cache; every loser closes
// the connection it just dialed to prevent goroutine leaks.
func (c *connCache[T]) getOrConnect(instanceName string) (T, error) {
	// Fast path: already cached.
	c.mu.Lock()
	if e, ok := c.entries[instanceName]; ok {
		c.mu.Unlock()
		return e.client, nil
	}
	c.mu.Unlock()

	// Slow path: dial outside the lock so we do not hold it during I/O.
	conn, err := c.connect(instanceName)
	if err != nil {
		var zero T
		return zero, fmt.Errorf("connecting to plugin instance %q: %w", instanceName, err)
	}
	client := c.newClient(conn)

	// Re-acquire the lock and double-check: another goroutine may have won the
	// race while we were dialing.
	c.mu.Lock()
	if existing, ok := c.entries[instanceName]; ok {
		c.mu.Unlock()
		// Close the connection we just created to avoid leaking it.
		conn.Close()
		return existing.client, nil
	}
	c.entries[instanceName] = connEntry[T]{conn: conn, client: client}
	c.mu.Unlock()
	return client, nil
}

// closeAll closes every cached connection and resets entries to an empty map.
// It returns the first conn.Close() error encountered (mirrors Pool.Close's
// firstErr capture), but unconditionally replaces entries with a fresh empty
// map regardless of errors.  This ensures:
//   - a subsequent getOrConnect re-dials rather than returning a half-closed conn
//   - a second closeAll() is a clean no-op
//
// The lock is held for the whole iteration, blocking concurrent getOrConnect.
// That is acceptable because closeAll runs only at shutdown and the number of
// cached instances is small (homelab scale).
func (c *connCache[T]) closeAll() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var firstErr error
	for _, e := range c.entries {
		if err := e.conn.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	// Always reset — even when a Close errored, callers expect a clean slate.
	c.entries = make(map[string]connEntry[T])
	return firstErr
}
