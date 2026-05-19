package main

import (
	"context"
	"sync"
)

// releaseFn is called by a hub consumer when it no longer needs the hub.
// The registry decrements the ref count and tears down the hub on zero.
type releaseFn func()

// hubEntry holds a running socketHub and its lifecycle state.
type hubEntry struct {
	hub       *socketHub
	runCtx    context.Context
	runCancel context.CancelFunc
	refCount  int // 1 per caller of Acquire
}

// hubRegistry is a process-global registry that creates at most one *socketHub
// per xapp-token. The same Socket Mode WebSocket is shared by TriggerService
// (EventsAPI events) and ChannelService (interactive callbacks).
//
// STALE-HUB ON TOKEN ROTATION (v1 limitation): when an instance config update
// rotates the xapp-token, TriggerService.Restart Acquires a new hub for the
// new token while ChannelService continues holding the OLD hub until that
// token's Socket Mode connection fails. The old connection will fail next time
// Slack rejects the revoked token; the hub will tear down via ctx cancel from
// its internal error path, ChannelService's interactive handler will stop
// receiving callbacks, and any in-flight Request will time out via the host's
// feedback-timeout path (matching the documented 'in-memory correlation loss on
// restart' behavior in spec §4.2). Cross-service token-rotation coordination is
// OUT OF SCOPE — file Phase-8 follow-up if needed.
type hubRegistry struct {
	mu      sync.Mutex
	factory socketModeFactory
	hubs    map[string]*hubEntry
}

// newHubRegistry creates a hubRegistry using the given socketModeFactory.
func newHubRegistry(factory socketModeFactory) *hubRegistry {
	return &hubRegistry{
		factory: factory,
		hubs:    make(map[string]*hubEntry),
	}
}

// Acquire returns the socketHub for xappToken, creating one if needed and
// starting its Run goroutine on first use. Subsequent calls with the same token
// return the SAME hub. The returned releaseFn must be called when the caller no
// longer needs the hub; when the ref count reaches zero, the hub is torn down.
func (r *hubRegistry) Acquire(xappToken string) (*socketHub, releaseFn, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry, ok := r.hubs[xappToken]
	if !ok {
		hub, err := newSocketHub(r.factory, xappToken)
		if err != nil {
			return nil, nil, err
		}
		ctx, cancel := context.WithCancel(context.Background())
		entry = &hubEntry{
			hub:       hub,
			runCtx:    ctx,
			runCancel: cancel,
			refCount:  0,
		}
		r.hubs[xappToken] = entry
		go func() {
			if err := hub.Run(ctx); err != nil {
				// Log but don't propagate — callers detect failure via their own context.
			}
		}()
	}

	entry.refCount++

	release := func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		entry.refCount--
		if entry.refCount <= 0 {
			entry.runCancel()
			delete(r.hubs, xappToken)
		}
	}

	return entry.hub, release, nil
}
