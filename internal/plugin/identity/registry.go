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
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"sync"
)

// tokenEntry holds the instance ID and raw token bytes stored for a registered
// token. The map key is SHA-256(raw), a non-secret index used for O(1) lookup;
// the raw bytes are compared with subtle.ConstantTimeCompare at verification
// time so the comparison does not leak timing information about the secret.
type tokenEntry struct {
	instanceID string
	raw        [32]byte
}

// Registry maps 256-bit random tokens to stable plugin instance IDs.
//
// Token lifecycle:
//   - Issue(instanceID) generates a new token and revokes any prior token
//     for that instance atomically (one lock acquisition).
//   - Revoke(token) drops the token, e.g. on subprocess exit.
//   - RevokeInstance(instanceID) drops all state for an instance, e.g. on uninstall.
//   - Lookup(token) resolves a token to an instance ID.
//
// Token comparison in Lookup uses crypto/subtle.ConstantTimeCompare to avoid
// timing side-channels. The map is keyed by SHA-256(token bytes), a non-secret
// deterministic index, so lookup remains O(1).
type Registry struct {
	mu         sync.RWMutex
	byHash     map[[32]byte]tokenEntry // SHA-256(token) → entry
	byInstance map[string][32]byte     // instanceID → SHA-256(token) hash key
}

// New returns an empty Registry ready for use.
func New() *Registry {
	return &Registry{
		byHash:     make(map[[32]byte]tokenEntry),
		byInstance: make(map[string][32]byte),
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
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("identity.Issue: read random bytes: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	hash := sha256.Sum256(raw[:])

	r.mu.Lock()
	defer r.mu.Unlock()

	// Revoke the prior token for this instance, if any, so an old generation
	// cannot authenticate after a new one is issued.
	if priorHash, ok := r.byInstance[instanceID]; ok {
		delete(r.byHash, priorHash)
	}

	r.byHash[hash] = tokenEntry{instanceID: instanceID, raw: raw}
	r.byInstance[instanceID] = hash
	return token, nil
}

// Revoke removes token from the registry. It also removes the byInstance entry
// for the owning instance if that instance's current token is the one being
// revoked (so RevokeInstance and Revoke stay consistent).
//
// Revoke is a no-op when token is not registered or cannot be decoded.
func (r *Registry) Revoke(token string) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 32 {
		return
	}
	var rawArr [32]byte
	copy(rawArr[:], raw)
	hash := sha256.Sum256(rawArr[:])

	r.mu.Lock()
	defer r.mu.Unlock()

	entry, ok := r.byHash[hash]
	if !ok {
		return
	}
	delete(r.byHash, hash)

	// Only remove the byInstance entry if it still points at this token's hash;
	// a concurrent Issue for the same instance would have already replaced it.
	if r.byInstance[entry.instanceID] == hash {
		delete(r.byInstance, entry.instanceID)
	}
}

// RevokeInstance removes all registry state for instanceID. Called on
// plugin uninstall or permanent teardown.
//
// RevokeInstance is a no-op when instanceID has no registered token.
func (r *Registry) RevokeInstance(instanceID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	hash, ok := r.byInstance[instanceID]
	if !ok {
		return
	}
	delete(r.byHash, hash)
	delete(r.byInstance, instanceID)
}

// Lookup resolves token to the instance ID that owns it. Returns ("", false)
// when the token is unknown, has been revoked, or cannot be decoded.
//
// The comparison is performed in constant time using crypto/subtle.ConstantTimeCompare
// to prevent timing side-channels. The SHA-256 hash of the decoded token bytes
// is used as a non-secret map key for O(1) lookup; the actual secret bytes are
// compared only after the map entry is found.
func (r *Registry) Lookup(token string) (instanceID string, ok bool) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 32 {
		return "", false
	}
	var rawArr [32]byte
	copy(rawArr[:], raw)
	hash := sha256.Sum256(rawArr[:])

	r.mu.RLock()
	defer r.mu.RUnlock()

	entry, found := r.byHash[hash]
	if !found {
		return "", false
	}

	// Constant-time comparison of the decoded token bytes against the stored
	// raw bytes. This prevents timing attacks even if hash collisions were
	// somehow forced — the attacker cannot learn anything from comparison timing.
	if subtle.ConstantTimeCompare(rawArr[:], entry.raw[:]) != 1 {
		return "", false
	}

	return entry.instanceID, true
}
