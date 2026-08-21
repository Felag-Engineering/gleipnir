package hostendpoint

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
)

// ErrWildcardAddr rejects a bind address that is not a single concrete host.
// "The listener binds exclusively to the per-instance internal networks"
// (spec §8) is enforced here structurally rather than left to caller
// discipline — the same posture container.ValidateCreate takes for the
// self-constrained create calls. A wildcard bind would expose every host
// tool on every interface the host has, including the operator API's.
var ErrWildcardAddr = errors.New("hostendpoint: refusing wildcard bind address — the host endpoint binds only to a specific per-instance network gateway address")

// ListenerSet serves one Server on one listener per plugin instance. The
// reconciler drives it as instance networks come and go: Add when an
// instance's network exists, Remove when it is torn down. Nothing starts it
// globally — there is no "the" host-endpoint port, only the per-network
// gateway addresses, which is itself half of the host-plane invariant.
//
// All listeners share the one handler; which instance is calling is
// established per-request by the token middleware (#876), not by which
// listener the request arrived on. (The egress proxy's LocalAddr trick
// identifies instances for grant lookup; here the token must prove identity
// anyway, so the listener stays routing-only.)
type ListenerSet struct {
	handler http.Handler

	mu      sync.Mutex
	servers map[string]*instanceListener // keyed by instance ID
}

type instanceListener struct {
	srv  *http.Server
	addr string // the bound address, including the resolved port
	done chan struct{}
}

// NewListenerSet returns a ListenerSet serving handler.
func NewListenerSet(handler http.Handler) *ListenerSet {
	return &ListenerSet{
		handler: handler,
		servers: make(map[string]*instanceListener),
	}
}

// Add binds addr for the given instance and starts serving. It returns the
// bound address (useful when addr carries port 0). Adding an instance that
// already has a listener is an error — the reconciler owns the lifecycle,
// and a second Add for a live instance means two components think they do.
func (l *ListenerSet) Add(instanceID, addr string) (string, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("hostendpoint: bind address %q: %w", addr, err)
	}
	if host == "" {
		return "", ErrWildcardAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		return "", ErrWildcardAddr
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if _, exists := l.servers[instanceID]; exists {
		return "", fmt.Errorf("hostendpoint: instance %s already has a listener", instanceID)
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", fmt.Errorf("hostendpoint: listen %s for instance %s: %w", addr, instanceID, err)
	}

	il := &instanceListener{
		srv:  &http.Server{Handler: l.handler},
		addr: ln.Addr().String(),
		done: make(chan struct{}),
	}
	l.servers[instanceID] = il

	go func() {
		defer close(il.done)
		// ErrServerClosed is the normal Remove/Close outcome; anything else
		// is surfaced by the next Add or by health, not swallowed here —
		// Serve's error after close is the only signal this goroutine has.
		_ = il.srv.Serve(ln)
	}()
	return il.addr, nil
}

// Addr reports the bound address for an instance, or "" when it has none.
func (l *ListenerSet) Addr(instanceID string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if il, ok := l.servers[instanceID]; ok {
		return il.addr
	}
	return ""
}

// Remove gracefully shuts down an instance's listener. Removing an instance
// with no listener is a no-op: the reconciler is level-triggered, and
// "already gone" is a converged state, not an error.
func (l *ListenerSet) Remove(ctx context.Context, instanceID string) error {
	l.mu.Lock()
	il, ok := l.servers[instanceID]
	if ok {
		delete(l.servers, instanceID)
	}
	l.mu.Unlock()
	if !ok {
		return nil
	}
	err := il.srv.Shutdown(ctx)
	<-il.done
	return err
}

// Close shuts down every listener. Used at host shutdown; per-instance
// teardown goes through Remove.
func (l *ListenerSet) Close(ctx context.Context) error {
	l.mu.Lock()
	all := make([]*instanceListener, 0, len(l.servers))
	for id, il := range l.servers {
		all = append(all, il)
		delete(l.servers, id)
	}
	l.mu.Unlock()

	var firstErr error
	for _, il := range all {
		if err := il.srv.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
		<-il.done
	}
	return firstErr
}
