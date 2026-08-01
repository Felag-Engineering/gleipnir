package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/infra/crypto"
	"github.com/felag-engineering/gleipnir/internal/infra/headervalidate"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/toolregistry"
)

// ErrToolNamespaceConflict is returned by RefreshTools when an added tool's
// dot-name is already owned by a different source (e.g. a plugin instance).
// Callers can use errors.As to recover the *toolregistry.ConflictError for the
// specific conflicting name and its existing owner.
var ErrToolNamespaceConflict = errors.New("tool namespace conflict")

// ResolvedTool pairs a granted tool's model metadata with a ready Client
// targeting its server. Used by the agent runner to call tools.
type ResolvedTool struct {
	model.GrantedTool
	Client      *Client
	Description string          // tool description from the MCP registry
	InputSchema json.RawMessage // raw JSON schema from the MCP tool record

	// CanonicalSchema is the schemanorm-normalized form of InputSchema, or
	// nil when no canonical form is stored (mcp_tools.canonical_schema was
	// NULL, or the tool is plugin-sourced -- ResolvedTool literals built in
	// execution/agent/tools.go for plugin tools never populate this field).
	// Consumers must NOT silently fall back to InputSchema: nil means "no
	// canonical form", and treating raw as canonical would defeat the point
	// of storing it. Nothing consumes this field in this issue.
	//
	// A non-nil CanonicalSchema is NOT a safety attestation: schemanorm only
	// performs byte-level normalization (sorted keys, canonical escapes) and
	// does not reject or resolve anything schema-semantic, including a
	// "$ref" pointing at "file:///etc/passwd" or another external/remote
	// location. Rejecting such refs at discovery time is spec §10 work that
	// is deliberately out of scope here. A future enforcement consumer of
	// this field must still construct its JSON Schema compiler with a
	// deny-all URLLoader, and must not read "canonical" as "vetted".
	CanonicalSchema json.RawMessage
}

// ToolDiff describes the set of changes detected between two successive tool
// discovery snapshots for a server. Names are sorted for deterministic output.
type ToolDiff struct {
	Added    []string
	Removed  []string
	Modified []string
}

// Registry resolves policy capability references to live MCP clients.
// It reads server and tool records from the DB and builds Client instances
// on demand.
type Registry struct {
	queries    *db.Queries
	mcpTimeout time.Duration
	encKey     []byte                 // AES-256-GCM key for decrypting auth_headers_encrypted; nil if unset
	arbiter    *toolregistry.Registry // cross-source tool namespace arbiter; nil means no uniqueness enforcement
}

// RegistryOption configures a Registry.
type RegistryOption func(*Registry)

// WithMCPTimeout sets the HTTP timeout applied to every MCP Client created
// by the Registry. When zero, the Client default (30 s) is used.
func WithMCPTimeout(d time.Duration) RegistryOption {
	return func(r *Registry) {
		r.mcpTimeout = d
	}
}

// defaultProbeTimeout matches the zero-value Client's default HTTP timeout
// (see NewClient) and is used by ProbeTimeout when WithMCPTimeout was not
// configured.
const defaultProbeTimeout = 30 * time.Second

// probeTimeoutMultiplier scales the per-request GLEIPNIR_MCP_TIMEOUT up into
// a whole-sequence budget. See ProbeTimeout's doc comment (Finding 5,
// security review, #737 cycle 3) for why a bare 1x multiplier regresses a
// slow-but-reachable legacy server that previously got a full
// GLEIPNIR_MCP_TIMEOUT allowance on each of 3 round trips.
const probeTimeoutMultiplier = 2

// ProbeTimeout returns the wall-clock budget for a full multi-round-trip
// protocol+tool probe sequence (e.g. MCPHandler.Create's protocol probe
// followed by its tool probe, or RefreshTools's protocol re-probe followed
// by tool discovery). It is probeTimeoutMultiplier times the per-client
// mcpTimeout configured via WithMCPTimeout, or times the Client default when
// unset.
//
// Finding 5 (security review, #737 cycle 2): GLEIPNIR_MCP_TIMEOUT bounds a
// single HTTP round trip (it becomes http.Client.Timeout on each Client),
// not a whole probe sequence. A legacy-classified server costs up to 6
// round trips per create/refresh; without a shared budget the worst-case
// handler hold is (round trips × GLEIPNIR_MCP_TIMEOUT), which can exceed
// GLEIPNIR_HTTP_WRITE_TIMEOUT many times over. Callers wrap the whole
// sequence in one context.WithTimeout(ctx, r.ProbeTimeout()) so the
// cumulative time is capped regardless of how many round trips the era
// classification ends up needing.
//
// Finding 5 (security review, #737 cycle 3): a bare single-mcpTimeout budget
// over-corrects. Before this package existed, a legacy server made 3 round
// trips (initialize, notifications/initialized, tools/list) and each got its
// own full GLEIPNIR_MCP_TIMEOUT allowance — so a slow-cold-start server
// taking, say, 11s per round trip worked fine. After #737, the same server
// still makes those 3 round trips (plus server/discover) but now shares ONE
// budget, so a bare 1x multiplier would make that same server start failing
// registration/refresh even though nothing about its actual responsiveness
// changed. probeTimeoutMultiplier restores headroom for a slow-but-reachable
// server while keeping the sequence bounded (closing Finding 5 from cycle 2)
// rather than reverting to the unbounded per-request behavior.
func (r *Registry) ProbeTimeout() time.Duration {
	if r.mcpTimeout > 0 {
		return probeTimeoutMultiplier * r.mcpTimeout
	}
	return probeTimeoutMultiplier * defaultProbeTimeout
}

// WithEncryptionKey sets the AES-256 key used to decrypt auth_headers_encrypted
// when building a Client for an MCP server. When nil, auth headers stored in
// the DB are silently dropped (with a log warning) rather than causing errors.
func WithEncryptionKey(key []byte) RegistryOption {
	return func(r *Registry) {
		r.encKey = key
	}
}

// WithToolNamespaceArbiter wires the shared cross-source uniqueness arbiter
// into the Registry. When set, RefreshTools will reserve dot-names for tools
// it adds and release them for tools it removes. A nil arbiter (the default)
// disables uniqueness enforcement so existing tests remain unaffected.
func WithToolNamespaceArbiter(a *toolregistry.Registry) RegistryOption {
	return func(r *Registry) {
		r.arbiter = a
	}
}

// NewRegistry returns a Registry backed by the given sqlc Queries.
func NewRegistry(queries *db.Queries, opts ...RegistryOption) *Registry {
	r := &Registry{queries: queries}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// newClientForServer creates an MCP Client for srv, applying the Registry's
// mcpTimeout and decrypting any stored auth headers. On decrypt or unmarshal
// error, or when the encryption key is unset, the client is returned with no
// auth headers and a warning is logged — matching the fail-open pattern used
// by the webhook secret loader.
func (r *Registry) newClientForServer(srv db.McpServer) *Client {
	opts := make([]ClientOption, 0, 3)
	if r.mcpTimeout > 0 {
		opts = append(opts, WithTimeout(r.mcpTimeout))
	}

	if srv.AuthHeadersEncrypted != nil {
		if r.encKey == nil {
			slog.Warn("encryption key unset; mcp server has stored auth headers but they will not be sent",
				"server_id", srv.ID, "server_name", srv.Name)
		} else {
			plaintext, err := crypto.Decrypt(r.encKey, *srv.AuthHeadersEncrypted)
			if err != nil {
				slog.Warn("failed to decrypt mcp server auth headers; headers will not be sent",
					"server_id", srv.ID, "server_name", srv.Name, "err", err)
			} else {
				headers, err := UnmarshalAuthHeaders([]byte(plaintext))
				if err != nil {
					slog.Warn("failed to unmarshal mcp server auth headers; headers will not be sent",
						"server_id", srv.ID, "server_name", srv.Name, "err", err)
				} else {
					// Finding 2 (security review, #737 cycle 2/3):
					// headervalidate.ValidateName gates new writes at
					// POST/PUT config time, but rows persisted before a
					// header name was reserved — or before this filter
					// existed — are grandfathered in the DB. This is the
					// injection-time backstop: a reserved-name header can
					// never reach the wire regardless of when it was stored.
					headers = dropReservedAuthHeaders(headers, srv.ID, srv.Name)
					if len(headers) > 0 {
						opts = append(opts, WithAuthHeaders(headers))
					}
				}
			}
		}
	}

	// NULL or empty protocol_version means unpinned, which keeps legacy
	// request shaping (NULL ⇒ legacy behavior everywhere).
	if srv.ProtocolVersion != nil && *srv.ProtocolVersion != "" {
		opts = append(opts, WithProtocolVersion(*srv.ProtocolVersion))
	}

	cl := NewClient(srv.Url, opts...)
	cl.serverName = srv.Name
	return cl
}

// dropReservedAuthHeaders filters headers down to only those whose name is
// not in headervalidate.ReservedHeaderNames, logging a WARN for each dropped
// entry so the operator can see why a configured header vanished.
//
// headervalidate.ValidateName already rejects a reserved name at
// POST/PUT config-write time (internal/http/api/mcp_handler.go), but that
// gate cannot retroactively scrub rows written before a name joined the
// reserved list — or before this filter existed. This is the injection-time
// backstop: even a grandfathered, already-persisted reserved-name header can
// never reach the wire (Finding 2, security review, #737 cycle 2/3).
func dropReservedAuthHeaders(headers []AuthHeader, serverID, serverName string) []AuthHeader {
	kept := make([]AuthHeader, 0, len(headers))
	for _, h := range headers {
		reserved := false
		for _, r := range headervalidate.ReservedHeaderNames {
			if strings.EqualFold(h.Name, r) {
				reserved = true
				break
			}
		}
		if reserved {
			slog.Warn("dropping stored mcp auth header: name is reserved and cannot be sent",
				"server_id", serverID, "server_name", serverName, "header_name", h.Name)
			continue
		}
		kept = append(kept, h)
	}
	return kept
}

// splitToolName splits a dot-notation tool name (e.g. "my-server.read_pods")
// into its server and tool components. Both parts must be non-empty.
func splitToolName(dotName string) (serverName, toolName string, err error) {
	parts := strings.SplitN(dotName, ".", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("tool name %q must be in server.tool dot-notation", dotName)
	}
	return parts[0], parts[1], nil
}

// ResolveForPolicy resolves the granted tool list for a parsed policy,
// returning a ResolvedTool for each entry in capabilities.tools. Returns
// an error if any tool reference is not found in the DB — this is the
// fail-fast check at run start.
func (r *Registry) ResolveForPolicy(ctx context.Context, p *model.ParsedPolicy) ([]ResolvedTool, error) {
	var result []ResolvedTool
	clients := make(map[string]*Client)

	for _, t := range p.Capabilities.Tools {
		serverName, toolName, err := splitToolName(t.Tool)
		if err != nil {
			return nil, fmt.Errorf("resolve tool %q: %w", t.Tool, err)
		}

		tool, err := r.queries.GetMCPToolByServerAndName(ctx, db.GetMCPToolByServerAndNameParams{
			ServerName: serverName,
			ToolName:   toolName,
		})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, fmt.Errorf("tool %q not found in registry", t.Tool)
			}
			return nil, fmt.Errorf("look up tool %q: %w", t.Tool, err)
		}

		if tool.Enabled == 0 {
			return nil, fmt.Errorf("tool %q on server %q is disabled", toolName, serverName)
		}

		srv, err := r.queries.GetMCPServer(ctx, tool.ServerID)
		if err != nil {
			return nil, fmt.Errorf("get server for tool %q: %w", t.Tool, err)
		}

		var timeout time.Duration
		if t.Timeout != "" {
			timeout, err = time.ParseDuration(t.Timeout)
			if err != nil {
				return nil, fmt.Errorf("parse timeout for tool %q: %w", t.Tool, err)
			}
		}

		cl, ok := clients[srv.Url]
		if !ok {
			cl = r.newClientForServer(srv)
			clients[srv.Url] = cl
		}

		var canonical json.RawMessage
		if tool.CanonicalSchema != nil && *tool.CanonicalSchema != "" {
			canonical = json.RawMessage(*tool.CanonicalSchema)
		}

		result = append(result, ResolvedTool{
			GrantedTool: model.GrantedTool{
				ServerName: serverName,
				ToolName:   toolName,
				Approval:   t.Approval,
				Timeout:    timeout,
				OnTimeout:  t.OnTimeout,
				Params:     t.Params,
			},
			Client:          cl,
			Description:     tool.Description,
			InputSchema:     json.RawMessage(tool.InputSchema),
			CanonicalSchema: canonical,
		})
	}

	return result, nil
}

// ResolveToolByName resolves a single tool by dot-notation name and returns
// a ready MCP Client plus the bare tool name. Used by the poll trigger engine
// to call a tool outside the agent runtime context.
func (r *Registry) ResolveToolByName(ctx context.Context, dotName string) (*Client, string, error) {
	serverName, toolName, err := splitToolName(dotName)
	if err != nil {
		return nil, "", fmt.Errorf("resolve tool %q: %w", dotName, err)
	}

	tool, err := r.queries.GetMCPToolByServerAndName(ctx, db.GetMCPToolByServerAndNameParams{
		ServerName: serverName,
		ToolName:   toolName,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", fmt.Errorf("tool %q not found in registry", dotName)
		}
		return nil, "", fmt.Errorf("look up tool %q: %w", dotName, err)
	}

	if tool.Enabled == 0 {
		return nil, "", fmt.Errorf("tool %q on server %q is disabled", toolName, serverName)
	}

	srv, err := r.queries.GetMCPServer(ctx, tool.ServerID)
	if err != nil {
		return nil, "", fmt.Errorf("get server for tool %q: %w", dotName, err)
	}

	return r.newClientForServer(srv), toolName, nil
}

// ProbeTools performs a one-shot tool discovery against the MCP server at
// urlStr without writing any DB rows. It is used by MCPHandler.Create to
// discover tools before committing the server row, so a namespace conflict can
// be rejected with HTTP 409 before an orphan row is created.
//
// The synthetic db.McpServer passed to newClientForServer carries ID "<probe>"
// so log output is not misleading about a real server ID.
//
// The returned tools are canonicalized (see canonicalizeDiscovered) so the
// caller can persist both the raw and canonical schema forms.
func (r *Registry) ProbeTools(ctx context.Context, name, urlStr string, encryptedAuthHeaders *string) ([]DiscoveredTool, error) {
	synthetic := db.McpServer{
		ID:                   "<probe>",
		Name:                 name,
		Url:                  urlStr,
		AuthHeadersEncrypted: encryptedAuthHeaders,
	}
	tools, err := r.newClientForServer(synthetic).DiscoverTools(ctx)
	if err != nil {
		return nil, fmt.Errorf("probe tools for %q: %w", name, err)
	}
	return canonicalizeDiscovered(synthetic.ID, name, tools), nil
}

// ProbeProtocol performs a one-shot protocol-era probe against the MCP
// server at urlStr without writing any DB rows. Mirrors ProbeTools; used by
// MCPHandler.Create before the server row exists.
func (r *Registry) ProbeProtocol(ctx context.Context, name, urlStr string, encryptedAuthHeaders *string) (ProbeResult, error) {
	synthetic := db.McpServer{
		ID:                   "<probe>",
		Name:                 name,
		Url:                  urlStr,
		AuthHeadersEncrypted: encryptedAuthHeaders,
	}
	res, err := r.newClientForServer(synthetic).ProbeProtocolVersion(ctx)
	if err != nil {
		return ProbeResult{}, fmt.Errorf("probe protocol for %q: %w", name, err)
	}
	return res, nil
}

// refreshProtocolVersion probes srv for its protocol era, persists the
// result to mcp_servers.protocol_version, and updates srv in place so the
// client built for this refresh already carries the fresh pin. Every
// failure is logged and swallowed by design: the pin is an optimization and
// must never fail tool discovery.
//
// Finding 2 (security review, #737 cycle 2): an established modern pin is
// NEVER auto-downgraded to a legacy one. A single ambiguous 4xx on
// server/discover — a WAF, a hostile forward proxy, a flaky deploy — must
// not be able to silently and permanently demote a server that has already
// proven itself modern; that would drop #741's header-binding protections
// for all subsequent tool traffic with no visible trace. Demoting a modern
// pin requires explicit operator action (re-registering the server, or a
// future "reset pin" affordance); this probe only WARNs and leaves the
// existing pin untouched. Every OTHER pin write (a brand new pin, a legacy
// re-negotiation, or an upgrade to modern) is logged at WARN too, so any pin
// change is visible in the logs even though there is no dedicated audit
// event for it: mcp_servers config changes have no audit-event path in this
// repo today (plugin_audit_events is plugin-scoped per ADR-046, and
// inventing a new audit subsystem is out of scope for this issue).
//
// Finding 1 (security review, #737 cycle 3): the guard used to read
// `previous` from srv's in-memory snapshot and then write unconditionally —
// two concurrent refreshes (e.g. an operator double-clicking Discover) could
// both observe the same stale NULL/legacy `previous`, race past the
// in-memory check, and whichever write landed last would win even if it
// demoted a server the other goroutine had just proven modern. The guard
// now lives entirely in UpdateMCPServerProtocolVersionIfNotModern's SQL
// WHERE clause, evaluated by SQLite against the row's LIVE state inside the
// UPDATE itself, so it cannot be raced by a stale application-level read.
func (r *Registry) refreshProtocolVersion(ctx context.Context, srv *db.McpServer) {
	res, err := r.newClientForServer(*srv).ProbeProtocolVersion(ctx)
	if err != nil {
		slog.Warn("mcp protocol probe failed; keeping existing pin",
			"server_id", srv.ID, "server_name", srv.Name, "err", err)
		return
	}

	previous := ""
	if srv.ProtocolVersion != nil {
		previous = *srv.ProtocolVersion
	}
	if previous == res.Version {
		return
	}

	rowsAffected, err := r.queries.UpdateMCPServerProtocolVersionIfNotModern(ctx, db.UpdateMCPServerProtocolVersionIfNotModernParams{
		ProtocolVersion: &res.Version,
		ID:              srv.ID,
		ModernVersions:  modernVersionsForQuery(),
	})
	if err != nil {
		slog.Warn("failed to persist mcp protocol version",
			"server_id", srv.ID, "server_name", srv.Name, "err", err)
		return
	}
	if rowsAffected == 0 {
		slog.Warn("mcp protocol probe would downgrade an established modern pin; keeping existing pin, explicit operator action required",
			"server_id", srv.ID, "server_name", srv.Name, "current_pin", previous, "probed_version", res.Version, "probed_era", res.Era)
		return
	}

	slog.Warn("mcp protocol version pin changed",
		"server_id", srv.ID, "server_name", srv.Name, "previous_pin", previous, "new_pin", res.Version, "era", res.Era)
	srv.ProtocolVersion = &res.Version
}

// RefreshTools re-discovers tools for a registered server, computes the diff
// against the current DB state, upserts all fresh tools, deletes tools that
// have disappeared, and updates last_discovered_at.
//
// Freshly-discovered schemas are canonicalized via schemanorm before the diff
// and the upsert (see canonicalizeDiscovered), so a cosmetic key-order-only
// schema change does not flag a tool as Modified, and every upsert backfills
// canonical_schema for rows that predate this column. Normalization failure
// is fail-open: the tool is still discovered and stored, just with a NULL
// canonical_schema and a logged warning.
func (r *Registry) RefreshTools(ctx context.Context, serverID string) (ToolDiff, error) {
	// Fetch current tool state from DB so we can compute the diff and
	// preserve tool IDs for existing tools.
	oldTools, err := r.queries.ListMCPToolsByServer(ctx, serverID)
	if err != nil {
		return ToolDiff{}, fmt.Errorf("list existing tools: %w", err)
	}

	oldByName := make(map[string]db.McpTool, len(oldTools))
	for _, t := range oldTools {
		oldByName[t.Name] = t
	}

	srv, err := r.queries.GetMCPServer(ctx, serverID)
	if err != nil {
		return ToolDiff{}, fmt.Errorf("get mcp server %q: %w", serverID, err)
	}

	// Re-probe the protocol era before discovery so a fresh pin is available
	// for the discovery client below. Fail-open: the probe client is
	// separate from the discovery client (built from the pre-probe pin, not
	// reused), so a legacy server sees two initialize handshakes per
	// refresh — deliberate, so request shaping can later branch on the
	// freshly-probed pin (#741) rather than the stale one.
	//
	// Finding 5 (security review, #737 cycle 2): the protocol probe and tool
	// discovery share ONE context.WithTimeout budget (r.ProbeTimeout()) so a
	// slow-but-reachable server cannot make the handler hold the connection
	// for (round trips × GLEIPNIR_MCP_TIMEOUT) — see ProbeTimeout's doc
	// comment. probeCtx is scoped to this function only; the DB writes below
	// still use the caller's ctx.
	probeCtx, cancel := context.WithTimeout(ctx, r.ProbeTimeout())
	defer cancel()

	r.refreshProtocolVersion(probeCtx, &srv)

	freshTools, err := r.newClientForServer(srv).DiscoverTools(probeCtx)
	if err != nil {
		return ToolDiff{}, fmt.Errorf("discover tools for server %q: %w", serverID, err)
	}
	discovered := canonicalizeDiscovered(srv.ID, srv.Name, freshTools)

	freshByName := make(map[string]DiscoveredTool, len(discovered))
	for _, t := range discovered {
		freshByName[t.Name] = t
	}

	// Compute diff.
	var diff ToolDiff
	for name := range freshByName {
		if _, exists := oldByName[name]; !exists {
			diff.Added = append(diff.Added, name)
		}
	}
	for name, old := range oldByName {
		fresh, exists := freshByName[name]
		if !exists {
			diff.Removed = append(diff.Removed, name)
			continue
		}
		if old.Description != fresh.Description ||
			toolSchemaChanged(old.InputSchema, old.CanonicalSchema, fresh.InputSchema, fresh.CanonicalSchema) {
			diff.Modified = append(diff.Modified, name)
		}
	}

	sort.Strings(diff.Added)
	sort.Strings(diff.Removed)
	sort.Strings(diff.Modified)

	// Reserve newly-added dot-names in the cross-source arbiter before touching
	// the DB. If another source (e.g. a plugin) already owns a name, return
	// ErrToolNamespaceConflict so the caller can surface it as a real failure
	// rather than silently overwriting the namespace.
	mcpSrc := toolregistry.Source{Kind: toolregistry.KindMCP, Name: srv.Name}
	if r.arbiter != nil && len(diff.Added) > 0 {
		entries := make([]toolregistry.Reservation, len(diff.Added))
		for i, name := range diff.Added {
			entries[i] = toolregistry.Reservation{
				DotName: toolregistry.DotName(srv.Name, name),
				Owner:   mcpSrc,
			}
		}
		if err := r.arbiter.ReserveBulk(entries); err != nil {
			var ce *toolregistry.ConflictError
			if errors.As(err, &ce) {
				return ToolDiff{}, fmt.Errorf("refresh tools for %q: %w", srv.Name, ErrToolNamespaceConflict)
			}
			return ToolDiff{}, fmt.Errorf("reserve tool namespace for %q: %w", srv.Name, err)
		}
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)

	// Upsert all fresh tools. Preserve the existing ID for tools already in the
	// DB so foreign key references (e.g. in audit steps) remain stable.
	// ON CONFLICT does not touch the enabled column — operator-set disable state
	// survives rediscovery (see the UpsertMCPTool query for the ON CONFLICT clause).
	//
	// On any DB error after a successful arbiter reservation, release all
	// reservations for this server so the namespace is not permanently locked.
	for _, t := range discovered {
		toolID := model.NewULID()
		if old, exists := oldByName[t.Name]; exists {
			toolID = old.ID
		}

		if _, err := r.queries.UpsertMCPTool(ctx, db.UpsertMCPToolParams{
			ID:              toolID,
			ServerID:        serverID,
			Name:            t.Name,
			Description:     t.Description,
			InputSchema:     string(t.InputSchema),
			CanonicalSchema: t.CanonicalSchemaPtr(),
			CreatedAt:       now,
		}); err != nil {
			if r.arbiter != nil {
				// Releases all reservations for this server, including pre-existing ones,
				// because the server's tool set is in an unknown state after the partial failure.
				r.arbiter.ReleaseAllFor(mcpSrc)
			}
			return ToolDiff{}, fmt.Errorf("upsert tool %q: %w", t.Name, err)
		}
	}

	// Delete tools that are no longer present on the server.
	for _, name := range diff.Removed {
		if err := r.queries.DeleteMCPToolByServerAndName(ctx, db.DeleteMCPToolByServerAndNameParams{
			ServerID: serverID,
			Name:     name,
		}); err != nil {
			if r.arbiter != nil {
				// Releases all reservations for this server, including pre-existing ones,
				// because the server's tool set is in an unknown state after the partial failure.
				r.arbiter.ReleaseAllFor(mcpSrc)
			}
			return ToolDiff{}, fmt.Errorf("delete removed tool %q: %w", name, err)
		}
	}

	// Release arbiter slots for tools that are no longer on the server. This
	// must happen after successful deletion so the names are genuinely free.
	if r.arbiter != nil {
		for _, name := range diff.Removed {
			r.arbiter.Release(toolregistry.DotName(srv.Name, name), mcpSrc)
		}
	}

	if err := r.queries.UpdateMCPServerLastDiscovered(ctx, db.UpdateMCPServerLastDiscoveredParams{
		LastDiscoveredAt: &now,
		ID:               serverID,
	}); err != nil {
		return ToolDiff{}, fmt.Errorf("update last_discovered_at: %w", err)
	}

	hasDrift := int64(0)
	isFirstDiscovery := len(oldTools) == 0
	if !isFirstDiscovery && (len(diff.Added) > 0 || len(diff.Removed) > 0 || len(diff.Modified) > 0) {
		hasDrift = 1
	}
	if err := r.queries.UpdateMCPServerDrift(ctx, db.UpdateMCPServerDriftParams{
		HasDrift: hasDrift,
		ID:       serverID,
	}); err != nil {
		return ToolDiff{}, fmt.Errorf("update has_drift: %w", err)
	}

	return diff, nil
}
