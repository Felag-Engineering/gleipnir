// Package identity provides an in-memory token registry for plugin subprocess
// identity verification. Each plugin process is assigned a cryptographic token
// at subprocess launch; the host verifies the token on every incoming Host RPC
// to confirm the caller is the expected plugin instance (spec §8.4).
//
// This package is a leaf: it imports only stdlib. No internal packages are
// imported — same boundary discipline as internal/toolregistry.
package identity

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"sync"
)

// Registry maps 256-bit random tokens to stable plugin instance IDs.
//
// Token lifecycle:
//   - Issue(instanceID) generates a new token and revokes any prior token
//     for that instance atomically (one lock acquisition).
//   - Revoke(token) drops the token, e.g. on subprocess exit.
//   - RevokeInstance(instanceID) drops all state for an instance, e.g. on uninstall.
//   - Lookup(token) resolves a token to an instance ID.
type Registry struct {
	mu         sync.RWMutex
	byToken    map[string]string // token → instanceID
	byInstance map[string]string // instanceID → current token
}

// New returns an empty Registry ready for use.
func New() *Registry {
	return &Registry{
		byToken:    make(map[string]string),
		byInstance: make(map[string]string),
	}
}

// Issue generates a fresh 256-bit random token for instanceID and records it in
// the registry. If a prior token already exists for this instance, it is revoked
// atomically under the write lock — a killed-generation token can no longer
// authenticate after the new generation calls Issue.
//
// Returns an error only when the OS random source fails, which should be treated
// as fatal by the caller.
func (r *Registry) Issue(instanceID string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("identity.Issue: read random bytes: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)

	r.mu.Lock()
	defer r.mu.Unlock()

	// Revoke the prior token for this instance, if any, so an old generation
	// cannot authenticate after a new one is issued.
	if prior, ok := r.byInstance[instanceID]; ok {
		delete(r.byToken, prior)
	}

	r.byToken[token] = instanceID
	r.byInstance[instanceID] = token
	return token, nil
}

// Revoke removes token from the registry. It also removes the byInstance entry
// for the owning instance if that instance's current token is the one being
// revoked (so RevokeInstance and Revoke stay consistent).
//
// Revoke is a no-op when token is not registered.
func (r *Registry) Revoke(token string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	instanceID, ok := r.byToken[token]
	if !ok {
		return
	}
	delete(r.byToken, token)

	// Only remove the byInstance entry if it still points at this token; a
	// concurrent Issue for the same instance would have already replaced it.
	if r.byInstance[instanceID] == token {
		delete(r.byInstance, instanceID)
	}
}

// RevokeInstance removes all registry state for instanceID. Called on
// plugin uninstall or permanent teardown.
//
// RevokeInstance is a no-op when instanceID has no registered token.
func (r *Registry) RevokeInstance(instanceID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	token, ok := r.byInstance[instanceID]
	if !ok {
		return
	}
	delete(r.byToken, token)
	delete(r.byInstance, instanceID)
}

// Lookup resolves token to the instance ID that owns it. Returns ("", false)
// when the token is unknown or has been revoked.
func (r *Registry) Lookup(token string) (instanceID string, ok bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	id, found := r.byToken[token]
	return id, found
}
