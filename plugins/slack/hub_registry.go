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

// dead reports whether the underlying hub's Run goroutine has exited. A dead
// entry must not be returned from Acquire — its Done channel is already
// closed, so any caller that selects on hub.Done() would receive immediately.
func (e *hubEntry) dead() bool {
	select {
	case <-e.hub.Done():
		return true
	default:
		return false
	}
}

// hubRegistry is a process-global registry that creates at most one *socketHub
// per xapp-token. The same Socket Mode WebSocket is shared by TriggerService
// (EventsAPI events) and ChannelService (interactive callbacks).
//
// Dead hubs are detected on each Acquire and replaced with a fresh entry: when
// Slack's connection fails (network blip, auth revoked, etc.) the supervisor
// re-Acquires and gets a working hub rather than a corpse. The release closure
// only removes its entry from the map if the entry is still the current one,
// so a late release from a replaced hub does not evict its successor.
//
// STALE-HUB ON TOKEN ROTATION (v1 limitation): when an instance config update
// rotates the xapp-token, TriggerService.Restart Acquires a new hub for the
// new token while ChannelService continues holding the OLD hub until that
// token's Socket Mode connection fails. The old connection will fail next time
// Slack rejects the revoked token; ChannelService's maintainer goroutine
// observes hub.Done() and re-acquires under the new token. Cross-service
// token-rotation coordination is OUT OF SCOPE — file Phase-8 follow-up if needed.
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
//
// If a previously-created hub has died (Run goroutine exited), Acquire treats
// the map entry as absent and creates a fresh hub. Callers that block on
// hub.Done() will see the new hub's Done channel, not the dead one.
func (r *hubRegistry) Acquire(xappToken string) (*socketHub, releaseFn, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry, ok := r.hubs[xappToken]
	if ok && entry.dead() {
		// Replace the dead entry. Outstanding refs from the previous
		// generation can still call their release closures — those will
		// observe that the map slot no longer contains their entry and
		// simply decrement without deleting (the swap below is the
		// authoritative eviction).
		delete(r.hubs, xappToken)
		ok = false
	}
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
			// Only evict from the map if our entry is still the current one.
			// A previously-replaced entry (after dead-detection) must not
			// delete its successor.
			if current, ok := r.hubs[xappToken]; ok && current == entry {
				delete(r.hubs, xappToken)
			}
		}
	}

	return entry.hub, release, nil
}
