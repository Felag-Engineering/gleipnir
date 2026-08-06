// Package mcp — this file makes a healthy managed-plugin generation an MCP
// server entry like any other (ADR-053, mcp-realignment-spec.md §3; issue
// #819).
//
// The realignment's claim is that there is ONE MCP client stack. A plugin stops
// being something Gleipnir talks to over a bespoke gRPC dispatcher and becomes
// a server it talks to over the same transport, through the same discovery,
// into the same `<source>.<tool>` namespace, with the same canonical-schema
// persistence. That claim is only true if a managed plugin's endpoint is an
// ordinary `mcp_servers` row — anything else reintroduces the second path the
// realignment exists to delete.
//
// What separates the two is a trust TIER, and it is derived from one column
// rather than stored beside it. See TrustTierOf.
package mcp

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
)

// TrustTier says where a registry entry came from, which decides what it is
// allowed to participate in.
type TrustTier string

const (
	// TrustTierManaged is a plugin instance Gleipnir itself built from a
	// signed bundle, runs in a container it created, and reaches on a network
	// it allocated.
	TrustTierManaged TrustTier = "managed"

	// TrustTierExternal is a server an operator registered by URL. Gleipnir
	// knows nothing about what is behind it beyond what it says about itself.
	TrustTierExternal TrustTier = "external"
)

// TrustTierOf derives a server's tier from whether it backs a plugin instance.
//
// Derived rather than stored: a separate trust_tier column would be a second
// fact that must agree with the first, and two facts that must agree are two
// facts that can disagree. A row could then claim to be managed while pointing
// at no instance, or claim to be external while the reconciler rotates its URL
// underneath it. Deriving costs one branch and cannot drift.
func TrustTierOf(srv db.McpServer) TrustTier {
	if srv.PluginInstanceID != nil && *srv.PluginInstanceID != "" {
		return TrustTierManaged
	}
	return TrustTierExternal
}

// IsManaged reports whether srv is a managed plugin instance's endpoint.
//
// This is the operator-editability gate. A managed entry's URL is owned by the
// generation lifecycle and its identity by the signed bundle; letting an admin
// retype either would either break routing until the next rotation overwrote
// the edit, or point a consented plugin's tool grants at somewhere else
// entirely.
func IsManaged(srv db.McpServer) bool { return TrustTierOf(srv) == TrustTierManaged }

// ErrNotManaged reports an operation that only applies to a managed entry being
// attempted against an external one, or the reverse. Handlers turn it into a
// 409.
var ErrNotManaged = errors.New("mcp: server is not a managed plugin endpoint")

// ManagedEndpoint is a healthy generation's MCP endpoint, as the reconciler
// knows it at the moment of a generation switch.
type ManagedEndpoint struct {
	// InstanceID is the plugin instance. It is the identity of the registry
	// entry — one row per instance for its whole life, NOT one per generation.
	InstanceID string

	// InstanceName becomes the server name, which is the `<source>` half of
	// every tool's dot-name. It must therefore be the instance's stable name:
	// a namespace prefix that changed across a rotation would invalidate every
	// policy's tool grants at the moment of an upgrade.
	InstanceName string

	// URL is the container's address on its per-instance network.
	URL string
}

// Validate rejects an endpoint that cannot back a registry entry.
func (e ManagedEndpoint) Validate() error {
	switch {
	case e.InstanceID == "":
		return errors.New("mcp: managed endpoint requires an instance ID")
	case e.InstanceName == "":
		return errors.New("mcp: managed endpoint requires an instance name")
	case e.URL == "":
		return errors.New("mcp: managed endpoint requires a URL")
	}
	return nil
}

// managedStore is the narrow persistence a registrar needs. *db.Queries
// satisfies it.
type managedStore interface {
	GetMCPServerByPluginInstance(ctx context.Context, pluginInstanceID *string) (db.McpServer, error)
	CreateManagedMCPServer(ctx context.Context, arg db.CreateManagedMCPServerParams) (db.McpServer, error)
	UpdateManagedMCPServerURL(ctx context.Context, arg db.UpdateManagedMCPServerURLParams) (int64, error)
	DeleteManagedMCPServer(ctx context.Context, pluginInstanceID *string) (int64, error)
}

// ManagedRegistrar registers and deregisters managed plugin endpoints.
//
// Both operations are idempotent, because the caller is the reconciler and the
// reconciler is level-triggered: "already registered" and "already gone" are
// the common outcomes of a pass, not errors to escalate.
type ManagedRegistrar struct {
	store managedStore
	newID func() string
	now   func() time.Time
}

// NewManagedRegistrar returns a registrar over queries. newID mints row IDs
// (ULIDs in production); nil uses a package default that is not available here,
// so callers must supply one.
func NewManagedRegistrar(store managedStore, newID func() string, now func() time.Time) (*ManagedRegistrar, error) {
	if store == nil {
		return nil, errors.New("mcp: managed registrar requires a store")
	}
	if newID == nil {
		return nil, errors.New("mcp: managed registrar requires an ID source")
	}
	if now == nil {
		now = time.Now
	}
	return &ManagedRegistrar{store: store, newID: newID, now: now}, nil
}

// Register makes an instance's endpoint resolvable, or repoints an existing
// entry at a new generation's address.
//
// The repoint IS the routing flip, and it needs no explicit invalidation. `url`
// is part of the registry cache's invalidation key (serverConfig, cache.go), so
// the next resolve rebuilds the client against the new address on its own —
// while a *Client already handed to a running run keeps the base URL it was
// built with and drains against the generation it started on. The old
// generation's containers are retired by the rotation's own drain step, not by
// anything here.
//
// The entry is pinned to the modern protocol at registration rather than
// probed for it. That is an assertion, but a SIGNED one: the bundle's manifest
// declares the protocol and the Minisign signature covers the manifest, so a
// managed plugin that does not speak 2026-07-28 is a bundle that lied about
// itself. The `server/discover` that follows still fails loudly if the
// container disagrees — pinning decides the request shape, it does not skip the
// handshake.
func (m *ManagedRegistrar) Register(ctx context.Context, ep ManagedEndpoint) (db.McpServer, error) {
	if err := ep.Validate(); err != nil {
		return db.McpServer{}, err
	}

	existing, err := m.store.GetMCPServerByPluginInstance(ctx, &ep.InstanceID)
	switch {
	case err == nil:
		if existing.Url == ep.URL {
			return existing, nil
		}
		affected, err := m.store.UpdateManagedMCPServerURL(ctx, db.UpdateManagedMCPServerURLParams{
			Url:              ep.URL,
			PluginInstanceID: &ep.InstanceID,
		})
		if err != nil {
			return db.McpServer{}, fmt.Errorf("repointing managed endpoint for instance %s: %w", ep.InstanceID, err)
		}
		if affected == 0 {
			// The row vanished between the read and the write — an instance
			// deleted mid-rotation. Report it rather than re-creating: a
			// deleted instance is not one to route to.
			return db.McpServer{}, fmt.Errorf("%w: instance %s", ErrNotManaged, ep.InstanceID)
		}
		existing.Url = ep.URL
		return existing, nil

	case errors.Is(err, sql.ErrNoRows):
		protocol := ProtocolVersion20260728
		created, err := m.store.CreateManagedMCPServer(ctx, db.CreateManagedMCPServerParams{
			ID:               m.newID(),
			Name:             ep.InstanceName,
			Url:              ep.URL,
			CreatedAt:        m.now().UTC().Format(time.RFC3339Nano),
			PluginInstanceID: &ep.InstanceID,
			ProtocolVersion:  &protocol,
		})
		if err != nil {
			return db.McpServer{}, fmt.Errorf("registering managed endpoint for instance %s: %w", ep.InstanceID, err)
		}
		return created, nil

	default:
		return db.McpServer{}, fmt.Errorf("looking up managed endpoint for instance %s: %w", ep.InstanceID, err)
	}
}

// Deregister removes an instance's endpoint.
//
// Deleting the row cascades to mcp_tools, which is what releases the instance's
// tool names — the toolregistry reservations are rebuilt from the DB, so the
// namespace frees itself rather than needing a second bookkeeping call that
// could be forgotten.
//
// Removing an endpoint nothing registered is not an error: a stop pass runs
// more than once, and the goal state ("this instance is not resolvable") holds
// either way.
func (m *ManagedRegistrar) Deregister(ctx context.Context, instanceID string) error {
	if instanceID == "" {
		return errors.New("mcp: deregister requires an instance ID")
	}
	if _, err := m.store.DeleteManagedMCPServer(ctx, &instanceID); err != nil {
		return fmt.Errorf("deregistering managed endpoint for instance %s: %w", instanceID, err)
	}
	return nil
}
