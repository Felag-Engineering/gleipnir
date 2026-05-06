package toolregistry

import "sync"

// Registry is an in-memory arbiter that enforces cross-source uniqueness for
// tool dot-names. It is safe for concurrent use. All state is process-local;
// there is no persistence or replication.
//
// Typical usage:
//   - MCP side: ReserveBulk on server creation / RefreshTools; ReleaseAllFor on deletion.
//   - Plugin side: ReserveBulk on instance start; ReleaseAllFor on instance stop.
type Registry struct {
	mu     sync.Mutex
	owners map[string]Source // dot-name → owning Source
}

// New returns an empty Registry ready for use.
func New() *Registry {
	return &Registry{owners: make(map[string]Source)}
}

// Reserve attempts to claim dotName for src. Returns nil on success.
//
// Re-reserving with the same Source is idempotent — the call succeeds without
// changing state. Returns *ConflictError (wrapping ErrConflict) when the name
// is already held by a different source.
func (r *Registry) Reserve(dotName string, src Source) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if existing, ok := r.owners[dotName]; ok {
		if existing == src {
			return nil // idempotent
		}
		return &ConflictError{DotName: dotName, Existing: existing}
	}
	r.owners[dotName] = src
	return nil
}

// Release removes the reservation for dotName if and only if the current owner
// equals src. It is a no-op when the name is unregistered or owned by a
// different source, so callers can call it unconditionally during cleanup
// without accidentally releasing another registrant's slot.
func (r *Registry) Release(dotName string, src Source) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if existing, ok := r.owners[dotName]; ok && existing == src {
		delete(r.owners, dotName)
	}
}

// ReleaseAllFor removes every reservation owned by src in a single critical
// section. Used when an MCP server is deleted or a plugin instance stops so
// all of its tool slots become available for new registrations.
func (r *Registry) ReleaseAllFor(src Source) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for dotName, owner := range r.owners {
		if owner == src {
			delete(r.owners, dotName)
		}
	}
}

// ReserveBulk attempts to claim all entries in one atomic critical section.
// On success all names are reserved. On the first conflict the partial
// reservations that were made during this call are rolled back — any names
// that were already reserved before this call are unaffected.
func (r *Registry) ReserveBulk(entries []Reservation) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Track names reserved during this call so we can roll them back on conflict.
	reserved := make([]string, 0, len(entries))

	for _, e := range entries {
		if existing, ok := r.owners[e.DotName]; ok {
			if existing == e.Owner {
				// Idempotent: this owner already holds the slot. Don't add to
				// reserved list so we don't accidentally delete it on rollback.
				continue
			}
			// Conflict: roll back only the names we just claimed.
			for _, name := range reserved {
				delete(r.owners, name)
			}
			return &ConflictError{DotName: e.DotName, Existing: existing}
		}
		r.owners[e.DotName] = e.Owner
		reserved = append(reserved, e.DotName)
	}

	return nil
}

// Lookup returns the Source that currently owns dotName, and whether it exists.
func (r *Registry) Lookup(dotName string) (Source, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	src, ok := r.owners[dotName]
	return src, ok
}

// Snapshot returns a shallow copy of the current ownership map. Intended for
// use in tests to assert arbiter state without holding the lock.
func (r *Registry) Snapshot() map[string]Source {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make(map[string]Source, len(r.owners))
	for k, v := range r.owners {
		out[k] = v
	}
	return out
}
