package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"

	"github.com/felag-engineering/gleipnir/internal/arcade"
	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/http/httputil"
	"github.com/felag-engineering/gleipnir/internal/infra/crypto"
	"github.com/felag-engineering/gleipnir/internal/llm"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/toolregistry"
)

// MCPHandler serves MCP server management endpoints under /api/v1/mcp/servers.
type MCPHandler struct {
	store    *db.Store
	registry *mcp.Registry
	arbiter  *toolregistry.Registry // cross-source namespace arbiter; nil when not configured
	encKey   []byte                 // AES-256-GCM key; nil when GLEIPNIR_ENCRYPTION_KEY is unset
	features SchemaFeatureLister    // nil disables simplified_for
}

// NewMCPHandler creates an MCPHandler backed by the given store, registry, and
// encryption key. encKey may be nil when the encryption key is not configured;
// in that case, Create/Update requests that include auth_headers return 503.
// arbiter may be nil to disable cross-source uniqueness enforcement.
func NewMCPHandler(store *db.Store, registry *mcp.Registry, encKey []byte, opts ...MCPHandlerOption) *MCPHandler {
	h := &MCPHandler{store: store, registry: registry, encKey: encKey}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// MCPHandlerOption configures an MCPHandler.
type MCPHandlerOption func(*MCPHandler)

// WithToolNamespaceArbiter wires the shared cross-source uniqueness arbiter
// into the handler so Create can enforce namespace uniqueness before writing
// the server row.
func WithToolNamespaceArbiter(a *toolregistry.Registry) MCPHandlerOption {
	return func(h *MCPHandler) {
		h.arbiter = a
	}
}

// SchemaFeatureLister reports the declared schema-feature set of every
// configured LLM provider, keyed by provider name. *llm.ProviderRegistry
// satisfies it.
type SchemaFeatureLister interface {
	SchemaFeaturesByProvider() map[string]llm.SchemaFeatureSet
}

// WithSchemaFeatures wires the LLM provider registry into the handler so
// ListTools/SetToolEnabled can compute simplified_for. Omitting this option
// leaves h.features nil, which disables the field (every tool reports []).
func WithSchemaFeatures(l SchemaFeatureLister) MCPHandlerOption {
	return func(h *MCPHandler) {
		h.features = l
	}
}

// authHeaderPayload is the JSON shape used in Create/Update/TestConnection
// request bodies for individual auth headers. "key" mirrors HTTP header
// naming conventions and maps to mcp.AuthHeader.Name in Go.
type authHeaderPayload struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// toAuthHeader converts a payload row to the mcp.AuthHeader type.
func (p authHeaderPayload) toAuthHeader() mcp.AuthHeader {
	return mcp.AuthHeader{Name: p.Key, Value: p.Value}
}

type mcpServerResponse struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	URL              string   `json:"url"`
	LastDiscoveredAt *string  `json:"last_discovered_at"`
	HasDrift         bool     `json:"has_drift"`
	CreatedAt        string   `json:"created_at"`
	AuthHeaderKeys   []string `json:"auth_header_keys"` // sorted header names; never includes values
	IsArcadeGateway  bool     `json:"is_arcade_gateway"`
	ProtocolVersion  *string  `json:"protocol_version"` // negotiated MCP revision pinned at probe time; null = never probed
}

type mcpServerCreateResponse struct {
	mcpServerResponse
	DiscoveryError *string `json:"discovery_error"`
}

type toolDiffResponse struct {
	Added    []string `json:"added"`
	Removed  []string `json:"removed"`
	Modified []string `json:"modified"`
}

// serverToResponse converts a DB row to the API response struct. It decrypts
// auth_headers_encrypted (if present) to extract header names for the
// auth_header_keys field. Values are never included in any response.
// On decrypt or unmarshal failure, auth_header_keys is returned as an empty
// slice and a warning is logged — the rest of the server data is still usable.
func (h *MCPHandler) serverToResponse(s db.McpServer) mcpServerResponse {
	keys := make([]string, 0)

	if s.AuthHeadersEncrypted != nil && h.encKey != nil {
		plaintext, err := crypto.Decrypt(h.encKey, *s.AuthHeadersEncrypted)
		if err != nil {
			slog.Warn("failed to decrypt mcp server auth headers for response",
				"server_id", s.ID, "err", err)
		} else {
			headers, err := mcp.UnmarshalAuthHeaders([]byte(plaintext))
			if err != nil {
				slog.Warn("failed to unmarshal mcp server auth headers for response",
					"server_id", s.ID, "err", err)
			} else {
				for _, hdr := range headers {
					keys = append(keys, hdr.Name)
				}
				sort.Strings(keys)
			}
		}
	}

	return mcpServerResponse{
		ID:               s.ID,
		Name:             s.Name,
		URL:              s.Url,
		LastDiscoveredAt: s.LastDiscoveredAt,
		HasDrift:         s.HasDrift != 0,
		CreatedAt:        s.CreatedAt,
		AuthHeaderKeys:   keys,
		IsArcadeGateway:  arcade.IsArcadeGateway(s.Url, keys),
		ProtocolVersion:  s.ProtocolVersion,
	}
}

func diffToResponse(d mcp.ToolDiff) toolDiffResponse {
	added := d.Added
	if added == nil {
		added = make([]string, 0)
	}
	removed := d.Removed
	if removed == nil {
		removed = make([]string, 0)
	}
	modified := d.Modified
	if modified == nil {
		modified = make([]string, 0)
	}
	return toolDiffResponse{
		Added:    added,
		Removed:  removed,
		Modified: modified,
	}
}

// testConnectionResponse is the response body for TestConnection.
// Always returns HTTP 200; the ok field conveys whether the MCP handshake succeeded.
type testConnectionResponse struct {
	OK        bool     `json:"ok"`
	ToolCount int      `json:"tool_count"`
	Tools     []string `json:"tools"`
	Error     string   `json:"error"`
}

// TestConnection handles POST /api/v1/mcp/servers/test.
// It performs a one-shot MCP discovery handshake against the provided URL without
// persisting any data — useful for verifying connectivity before saving a server.
// auth_headers are accepted inline and applied to the throwaway client only; they
// are never stored. The frontend must send plaintext values here (no sentinels).
// Always returns HTTP 200; the ok field in the body distinguishes success from failure.
func (h *MCPHandler) TestConnection(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL         string              `json:"url"`
		AuthHeaders []authHeaderPayload `json:"auth_headers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	if body.URL == "" {
		httputil.WriteError(w, http.StatusBadRequest, "url is required", "")
		return
	}
	if err := mcp.ValidateServerURL(r.Context(), body.URL); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid url", err.Error())
		return
	}
	if status, msg := validateHeaderPayloadNames(body.AuthHeaders); status != 0 {
		httputil.WriteError(w, status, msg, "")
		return
	}

	// Build throwaway client — never stored in h.registry or h.store.
	clientOpts := make([]mcp.ClientOption, 0, 1)
	if len(body.AuthHeaders) > 0 {
		headers := make([]mcp.AuthHeader, len(body.AuthHeaders))
		for i, p := range body.AuthHeaders {
			headers[i] = p.toAuthHeader()
		}
		clientOpts = append(clientOpts, mcp.WithAuthHeaders(headers))
	}
	client := mcp.NewClient(body.URL, clientOpts...)

	// 5-second deadline governs the entire handshake; no separate client timeout needed.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	tools, err := client.DiscoverTools(ctx)
	if err != nil {
		slog.Warn("MCP test connection failed", "url", body.URL, "err", err)
		httputil.WriteJSON(w, http.StatusOK, testConnectionResponse{
			OK:        false,
			ToolCount: 0,
			Tools:     []string{},
			Error:     humanizeMCPError(err),
		})
		return
	}

	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Name
	}
	httputil.WriteJSON(w, http.StatusOK, testConnectionResponse{
		OK:        true,
		ToolCount: len(tools),
		Tools:     names,
		Error:     "",
	})
}

// List handles GET /api/v1/mcp/servers.
func (h *MCPHandler) List(w http.ResponseWriter, r *http.Request) {
	rows, err := h.store.ListMCPServers(r.Context())
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to list MCP servers", err.Error())
		return
	}

	items := make([]mcpServerResponse, 0, len(rows))
	for _, row := range rows {
		items = append(items, h.serverToResponse(row))
	}

	httputil.WriteJSON(w, http.StatusOK, items)
}

// Create handles POST /api/v1/mcp/servers.
//
// Flow (atomicity guarantee per #194):
//  1. Validate name and URL.
//  2. Validate and encrypt auth headers.
//     3a. Pre-flight protocol-era probe (ProbeProtocol): probes server/discover
//     to determine the protocol_version to pin. Fail-open, same posture as
//     step 3 — a probe failure never blocks registration, and is NOT
//     reflected in discovery_error (that field is tools-only).
//  3. Pre-flight probe (ProbeTools): discover tools without writing any DB rows.
//     A probe failure is non-fatal — the server is still created so the operator
//     can fix the URL later; discovery_error is populated in the 201 response.
//  4. If probe succeeded and tools were found, reserve their dot-names in the
//     cross-source arbiter. A conflict → 409 with no orphan DB row.
//  5. Create the mcp_servers row. On failure → release arbiter reservations.
//  6. Upsert the probed tools directly (no second round-trip). On failure →
//     release arbiter reservations and return 500.
//  7. Return 201.
func (h *MCPHandler) Create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name        string              `json:"name"`
		URL         string              `json:"url"`
		AuthHeaders []authHeaderPayload `json:"auth_headers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}

	if body.Name == "" {
		httputil.WriteError(w, http.StatusBadRequest, "name is required", "")
		return
	}
	if err := mcp.ValidateServerURL(r.Context(), body.URL); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid url", err.Error())
		return
	}

	// Step 2: validate and encrypt auth headers.
	var ciphertext *string
	if len(body.AuthHeaders) > 0 {
		if h.encKey == nil {
			httputil.WriteError(w, http.StatusServiceUnavailable, "encryption key not configured; cannot store auth headers", "")
			return
		}
		if status, msg := validateHeaderPayloadNames(body.AuthHeaders); status != 0 {
			httputil.WriteError(w, status, msg, "")
			return
		}
		authHeaders := make([]mcp.AuthHeader, len(body.AuthHeaders))
		for i, p := range body.AuthHeaders {
			authHeaders[i] = p.toAuthHeader()
		}
		ct, status := h.encryptHeaders(authHeaders)
		if status != 0 {
			httputil.WriteError(w, status, "failed to encrypt auth headers", "")
			return
		}
		ciphertext = ct
	}

	// Steps 3a and 3 share one bounded context rather than each getting its
	// own full-length allowance. GLEIPNIR_MCP_TIMEOUT bounds a single HTTP
	// round trip (see Registry.ProbeTimeout); against a legacy-classified
	// server the two probes together cost up to 6 round trips, so without a
	// shared budget the worst-case handler hold could reach
	// (round trips × GLEIPNIR_MCP_TIMEOUT) — many times GLEIPNIR_HTTP_WRITE_
	// TIMEOUT (Finding 5, security review, #737 cycle 2).
	probeCtx, cancel := context.WithTimeout(r.Context(), h.registry.ProbeTimeout())
	defer cancel()

	// Step 3a: probe the server's protocol era. Fail-open, exactly like the
	// tool probe below — a probe failure must never block registration.
	// Ordered before tool discovery so that when request shaping branches on
	// the pin, discovery already runs under the negotiated version. A
	// protocol-probe failure must NOT populate discoveryError — that field
	// is about tools and is surfaced in the 201 body.
	var pinnedVersion *string
	if res, err := h.registry.ProbeProtocol(probeCtx, body.Name, body.URL, ciphertext); err != nil {
		slog.Warn("MCP protocol probe failed on server create", "server_name", body.Name, "err", err)
	} else {
		v := res.Version
		pinnedVersion = &v
	}

	// Step 3: pre-flight probe — discover tools without writing any DB rows.
	// A network failure here is non-fatal; we still create the server so the
	// operator can correct the URL later.
	var (
		probedTools    []mcp.DiscoveredTool
		discoveryError *string
	)
	probed, probeErr := h.registry.ProbeTools(probeCtx, body.Name, body.URL, ciphertext)
	if probeErr != nil {
		slog.Warn("MCP pre-flight probe failed on server create", "server_name", body.Name, "err", probeErr)
		errStr := probeErr.Error()
		discoveryError = &errStr
	} else {
		probedTools = probed
	}

	// Step 4: reserve namespace in the arbiter (only when probe succeeded and
	// returned tools). A conflict means another source already owns the name.
	mcpSrc := toolregistry.Source{Kind: toolregistry.KindMCP, Name: body.Name}
	if h.arbiter != nil && len(probedTools) > 0 {
		entries := make([]toolregistry.Reservation, len(probedTools))
		for i, t := range probedTools {
			entries[i] = toolregistry.Reservation{
				DotName: toolregistry.DotName(body.Name, t.Name),
				Owner:   mcpSrc,
			}
		}
		if err := h.arbiter.ReserveBulk(entries); err != nil {
			var ce *toolregistry.ConflictError
			if errors.As(err, &ce) {
				httputil.WriteError(w, http.StatusConflict,
					fmt.Sprintf("tool %q is already provided by %s", ce.DotName, ce.Existing.String()),
					"")
				return
			}
			httputil.WriteError(w, http.StatusInternalServerError, "failed to reserve tool namespace", err.Error())
			return
		}
	}

	// Step 5: create the mcp_servers row. On DB error, release the arbiter
	// reservations we just claimed.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	server, err := h.store.CreateMCPServer(r.Context(), db.CreateMCPServerParams{
		ID:                   model.NewULID(),
		Name:                 body.Name,
		Url:                  body.URL,
		CreatedAt:            now,
		AuthHeadersEncrypted: ciphertext,
	})
	if err != nil {
		if h.arbiter != nil {
			h.arbiter.ReleaseAllFor(mcpSrc)
		}
		if isUniqueConstraintError(err) {
			httputil.WriteError(w, http.StatusConflict, "MCP server name already exists", "")
			return
		}
		httputil.WriteError(w, http.StatusInternalServerError, "failed to create MCP server", err.Error())
		return
	}

	if pinnedVersion != nil {
		if err := h.store.UpdateMCPServerProtocolVersion(r.Context(),
			db.UpdateMCPServerProtocolVersionParams{ProtocolVersion: pinnedVersion, ID: server.ID}); err != nil {
			slog.Warn("failed to persist MCP protocol version after create",
				"server_id", server.ID, "err", err)
		}
	}

	resp := mcpServerCreateResponse{
		mcpServerResponse: h.serverToResponse(server),
		DiscoveryError:    discoveryError,
	}

	// Step 6: upsert the tools discovered in the pre-flight probe. Skipped when
	// probe failed (probedTools is empty) or the server returned no tools.
	if len(probedTools) > 0 {
		for _, t := range probedTools {
			if _, err := h.store.UpsertMCPTool(r.Context(), db.UpsertMCPToolParams{
				ID:              model.NewULID(),
				ServerID:        server.ID,
				Name:            t.Name,
				Description:     t.Description,
				InputSchema:     string(t.InputSchema),
				CanonicalSchema: t.CanonicalSchemaPtr(),
				CreatedAt:       now,
			}); err != nil {
				if h.arbiter != nil {
					h.arbiter.ReleaseAllFor(mcpSrc)
				}
				httputil.WriteError(w, http.StatusInternalServerError, "failed to upsert discovered tools", err.Error())
				return
			}
		}

		// Update last_discovered_at and clear drift to preserve the "first
		// discovery" semantics that RefreshTools provides.
		if err := h.store.UpdateMCPServerLastDiscovered(r.Context(), db.UpdateMCPServerLastDiscoveredParams{
			LastDiscoveredAt: &now,
			ID:               server.ID,
		}); err != nil {
			slog.Warn("failed to set last_discovered_at after create", "server_id", server.ID, "err", err)
		} else if err := h.store.UpdateMCPServerDrift(r.Context(), db.UpdateMCPServerDriftParams{
			HasDrift: 0,
			ID:       server.ID,
		}); err != nil {
			slog.Warn("failed to clear has_drift after create", "server_id", server.ID, "err", err)
		}
	}

	// Re-fetch so the response reflects every write made after the row was created:
	// the protocol-version pin (step 3a) and, when tools were discovered,
	// last_discovered_at / has_drift. Best-effort — on error the pre-write snapshot
	// is still a valid response.
	if updated, err := h.store.GetMCPServer(r.Context(), server.ID); err == nil {
		resp.mcpServerResponse = h.serverToResponse(updated)
	} else {
		slog.Warn("failed to re-fetch MCP server after create", "server_id", server.ID, "err", err)
	}

	httputil.WriteCreated(w, "/api/v1/mcp/servers/"+server.ID, resp)
}

// Delete handles DELETE /api/v1/mcp/servers/{id}.
func (h *MCPHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	server, err := h.store.GetMCPServer(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			httputil.WriteError(w, http.StatusNotFound, "MCP server not found", "")
			return
		}
		httputil.WriteError(w, http.StatusInternalServerError, "failed to get MCP server", err.Error())
		return
	}

	// ?force=true skips the conflict check below. Callers (the delete modal)
	// use this after the user has acknowledged that referencing agents will
	// fail to run.
	force := r.URL.Query().Get("force") == "true"

	if !force {
		// Check whether any active policy references a tool from this server.
		// Tool references use dot-notation: serverName.toolName, so we check for
		// the server name prefix to catch all tools from this server.
		policies, err := h.store.ListPolicies(r.Context())
		if err != nil {
			httputil.WriteError(w, http.StatusInternalServerError, "failed to list policies", err.Error())
			return
		}

		prefix := server.Name + "."
		var conflicting []string
		for _, p := range policies {
			if policyReferencesServer(p.Yaml, prefix) {
				conflicting = append(conflicting, p.Name)
			}
		}

		if len(conflicting) > 0 {
			httputil.WriteError(w, http.StatusConflict, "MCP server is referenced by active policies",
				fmt.Sprintf("policies referencing this server: %s — pass ?force=true to delete anyway", strings.Join(conflicting, ", ")))
			return
		}
	}

	// mcp_tools rows are cascade-deleted by the FK constraint on DELETE.
	if err := h.store.DeleteMCPServer(r.Context(), id); err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to delete MCP server", err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// Update handles PUT /api/v1/mcp/servers/{id}.
// Updates the server's name and url only. Auth headers are managed separately
// via PUT/DELETE /api/v1/mcp/servers/:id/headers/:name (ADR-039).
//
// Rename refreshes the cross-source tool-namespace arbiter (#578). A server's
// tool dot-names are prefixed with its name ("<name>.<tool>"), so renaming
// changes every reservation's key. This handler re-reserves the server's
// currently-known tools under the new name and releases the old-name slots, so
// the arbiter never holds stale reservations and a later server cannot hit a
// false conflict against an orphaned slot. The plugin-side analog is #574.
func (h *MCPHandler) Update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var body struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	if body.Name == "" {
		httputil.WriteError(w, http.StatusBadRequest, "name is required", "")
		return
	}
	if err := mcp.ValidateServerURL(r.Context(), body.URL); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid url", err.Error())
		return
	}

	// Load the current row first so we know the old name. We need it both to
	// detect a rename and to release the old arbiter reservations afterwards.
	existing, err := h.store.GetMCPServer(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			httputil.WriteError(w, http.StatusNotFound, "MCP server not found", "")
			return
		}
		httputil.WriteError(w, http.StatusInternalServerError, "failed to get MCP server", err.Error())
		return
	}

	renamed := h.arbiter != nil && body.Name != existing.Name

	// When renaming, reject a name already used by a *different* server before
	// touching the arbiter. Without this guard, two MCP servers sharing tool
	// names would expose a corruption path: if the target name already owns the
	// same dot-names in the arbiter, ReserveBulk treats them as idempotent and
	// succeeds, but the DB's UNIQUE(name) constraint then rejects the write —
	// and the DB-failure rollback (ReleaseAllFor below) would delete the *other*
	// server's legitimate reservations. Catching the name clash here keeps the
	// arbiter logic on the path where the new name is genuinely free.
	if renamed {
		servers, err := h.store.ListMCPServers(r.Context())
		if err != nil {
			httputil.WriteError(w, http.StatusInternalServerError, "failed to list MCP servers", err.Error())
			return
		}
		for _, s := range servers {
			if s.ID != id && s.Name == body.Name {
				httputil.WriteError(w, http.StatusConflict, "MCP server name already exists", "")
				return
			}
		}
	}

	// On rename, re-reserve the server's tools under the new name *before*
	// touching the DB. A conflict here means another source already owns one of
	// the new dot-names; ReserveBulk rolls back its own partial claims, so the
	// old reservations remain intact and we can fail cleanly with a 409 without
	// any partial apply. The old-name slots are released only after the DB write
	// succeeds, keeping the arbiter consistent with persisted state.
	var newSrc, oldSrc toolregistry.Source
	if renamed {
		newSrc = toolregistry.Source{Kind: toolregistry.KindMCP, Name: body.Name}
		oldSrc = toolregistry.Source{Kind: toolregistry.KindMCP, Name: existing.Name}

		tools, err := h.store.ListMCPToolsByServer(r.Context(), id)
		if err != nil {
			httputil.WriteError(w, http.StatusInternalServerError, "failed to list MCP tools for rename", err.Error())
			return
		}
		if len(tools) > 0 {
			entries := make([]toolregistry.Reservation, len(tools))
			for i, t := range tools {
				entries[i] = toolregistry.Reservation{
					DotName: toolregistry.DotName(body.Name, t.Name),
					Owner:   newSrc,
				}
			}
			if err := h.arbiter.ReserveBulk(entries); err != nil {
				var ce *toolregistry.ConflictError
				if errors.As(err, &ce) {
					httputil.WriteError(w, http.StatusConflict,
						fmt.Sprintf("tool %q is already provided by %s", ce.DotName, ce.Existing.String()),
						"")
					return
				}
				httputil.WriteError(w, http.StatusInternalServerError, "failed to reserve tool namespace", err.Error())
				return
			}
		}
	}

	updated, err := h.store.UpdateMCPServer(r.Context(), db.UpdateMCPServerParams{
		Name: body.Name,
		Url:  body.URL,
		ID:   id,
	})
	if err != nil {
		// The DB write failed, so the name did not change. Release the new-name
		// reservations we optimistically claimed; the old-name slots are still
		// held and remain correct.
		if renamed {
			h.arbiter.ReleaseAllFor(newSrc)
		}
		if errors.Is(err, sql.ErrNoRows) {
			httputil.WriteError(w, http.StatusNotFound, "MCP server not found", "")
			return
		}
		if isUniqueConstraintError(err) {
			httputil.WriteError(w, http.StatusConflict, "MCP server name already exists", "")
			return
		}
		httputil.WriteError(w, http.StatusInternalServerError, "failed to update MCP server", err.Error())
		return
	}

	// Rename committed: drop the stale old-name reservations now that the new
	// name is the source of truth.
	if renamed {
		h.arbiter.ReleaseAllFor(oldSrc)
	}

	httputil.WriteJSON(w, http.StatusOK, h.serverToResponse(updated))
}

// SetAuthHeader handles PUT /api/v1/mcp/servers/{id}/headers/{name}.
// Creates or replaces a single auth header by name. The name comparison is
// case-insensitive — submitting "X-Api-Key" when "x-api-key" exists replaces
// the stored entry and adopts the submitted casing.
func (h *MCPHandler) SetAuthHeader(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	headerName := chi.URLParam(r, "name")

	if err := mcp.ValidateHeaderName(headerName); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, fmt.Sprintf("invalid header name: %s", err), "")
		return
	}
	if h.encKey == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "encryption key not configured; cannot store auth headers", "")
		return
	}

	var body struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}

	updated, status, msg, err := h.withMutatedHeaders(r.Context(), id, func(headers []mcp.AuthHeader) []mcp.AuthHeader {
		// Replace by case-insensitive name match; append if not found.
		for i, hdr := range headers {
			if strings.EqualFold(hdr.Name, headerName) {
				headers[i] = mcp.AuthHeader{Name: headerName, Value: body.Value}
				return headers
			}
		}
		return append(headers, mcp.AuthHeader{Name: headerName, Value: body.Value})
	})
	if status != 0 {
		detail := ""
		if err != nil {
			detail = err.Error()
		}
		httputil.WriteError(w, status, msg, detail)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, h.serverToResponse(updated))
}

// DeleteAuthHeader handles DELETE /api/v1/mcp/servers/{id}/headers/{name}.
// Removes a single auth header by case-insensitive name match. If the header
// is not present, the response is still 200 with the current server state
// (idempotent). Deleting the last header clears the column (sets it to NULL).
func (h *MCPHandler) DeleteAuthHeader(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	headerName := chi.URLParam(r, "name")

	updated, status, msg, err := h.withMutatedHeaders(r.Context(), id, func(headers []mcp.AuthHeader) []mcp.AuthHeader {
		// Filter out the named header (case-insensitive). No-op if absent.
		filtered := headers[:0]
		for _, hdr := range headers {
			if !strings.EqualFold(hdr.Name, headerName) {
				filtered = append(filtered, hdr)
			}
		}
		return filtered
	})
	if status != 0 {
		detail := ""
		if err != nil {
			detail = err.Error()
		}
		httputil.WriteError(w, status, msg, detail)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, h.serverToResponse(updated))
}

// validateHeaderPayloadNames checks every payload's Key against ValidateHeaderName.
// Returns a non-zero HTTP status and a formatted message on the first failure,
// or (0, "") when all names are valid.
func validateHeaderPayloadNames(payloads []authHeaderPayload) (status int, message string) {
	for _, p := range payloads {
		if err := mcp.ValidateHeaderName(p.Key); err != nil {
			return http.StatusBadRequest, fmt.Sprintf("invalid header name %q: %s", p.Key, err)
		}
	}
	return 0, ""
}

// withMutatedHeaders decrypts the stored auth headers for serverID, applies
// mutate to produce a new slice, re-encrypts, persists, and re-fetches the
// updated server row. An empty post-mutation slice sets the column to NULL
// (matching the "delete last header" semantics).
//
// Returns (server, 0, "", nil) on success. On any failure, status and msg
// describe the error; err carries the underlying error when it exists so the
// caller can decide whether to include err.Error() in the response detail.
func (h *MCPHandler) withMutatedHeaders(
	ctx context.Context,
	serverID string,
	mutate func([]mcp.AuthHeader) []mcp.AuthHeader,
) (db.McpServer, int, string, error) {
	var zero db.McpServer

	server, err := h.store.GetMCPServer(ctx, serverID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return zero, http.StatusNotFound, "MCP server not found", nil
		}
		return zero, http.StatusInternalServerError, "failed to get MCP server", err
	}

	headers, err := h.decryptHeaders(server)
	if err != nil {
		return zero, http.StatusInternalServerError, "failed to load existing auth headers", err
	}

	headers = mutate(headers)

	var ct *string
	if len(headers) == 0 {
		ct = nil // clears the column
	} else {
		var encStatus int
		ct, encStatus = h.encryptHeaders(headers)
		if encStatus != 0 {
			return zero, encStatus, "failed to encrypt auth headers", nil
		}
	}

	if err := h.store.UpdateMCPServerAuthHeaders(ctx, db.UpdateMCPServerAuthHeadersParams{
		AuthHeadersEncrypted: ct,
		ID:                   serverID,
	}); err != nil {
		return zero, http.StatusInternalServerError, "failed to persist auth headers", err
	}

	updated, err := h.store.GetMCPServer(ctx, serverID)
	if err != nil {
		return zero, http.StatusInternalServerError, "failed to reload MCP server", err
	}
	return updated, 0, "", nil
}

// decryptHeaders loads and decrypts auth headers from the server row.
// Returns an empty (non-nil) slice when there are no stored headers.
func (h *MCPHandler) decryptHeaders(server db.McpServer) ([]mcp.AuthHeader, error) {
	if server.AuthHeadersEncrypted == nil {
		return []mcp.AuthHeader{}, nil
	}
	if h.encKey == nil {
		return nil, fmt.Errorf("encryption key not configured")
	}
	plaintext, err := crypto.Decrypt(h.encKey, *server.AuthHeadersEncrypted)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	headers, err := mcp.UnmarshalAuthHeaders([]byte(plaintext))
	if err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	if headers == nil {
		return []mcp.AuthHeader{}, nil
	}
	return headers, nil
}

// encryptHeaders serializes and encrypts a non-empty header slice.
// Returns nil ciphertext (and status 0) for an empty slice.
// Returns a non-zero HTTP status code on error.
func (h *MCPHandler) encryptHeaders(headers []mcp.AuthHeader) (*string, int) {
	raw, err := mcp.MarshalAuthHeaders(headers)
	if err != nil {
		return nil, http.StatusInternalServerError
	}
	if raw == nil {
		return nil, 0
	}
	ct, err := crypto.Encrypt(h.encKey, string(raw))
	if err != nil {
		return nil, http.StatusInternalServerError
	}
	return &ct, 0
}

// Discover handles POST /api/v1/mcp/servers/{id}/discover.
func (h *MCPHandler) Discover(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	if _, err := h.store.GetMCPServer(r.Context(), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			httputil.WriteError(w, http.StatusNotFound, "MCP server not found", "")
			return
		}
		httputil.WriteError(w, http.StatusInternalServerError, "failed to get MCP server", err.Error())
		return
	}

	diff, err := h.registry.RefreshTools(r.Context(), id)
	if err != nil {
		slog.Error("MCP discovery failed", "server_id", id, "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "discovery failed", err.Error())
		return
	}

	httputil.WriteJSON(w, http.StatusOK, diffToResponse(diff))
}

type mcpToolResponse struct {
	ID          string          `json:"id"`
	ServerID    string          `json:"server_id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
	Enabled     bool            `json:"enabled"`
	// Source identifies which registry owns this tool. Format: "mcp:<server-name>"
	// for tools from MCP servers, or "plugin:<instance-name>" for plugin tools.
	Source string `json:"source"`
	// SimplifiedFor names configured LLM providers that will be shown a
	// rewritten version of this tool's parameter schema (ADR-059 / spec §10
	// step 2). Sorted; always non-nil ([] when nothing is simplified).
	// Computed per request — it depends on which providers are currently
	// configured, not on anything stored, so it is never cached or
	// persisted. Policy-independent: the runtime actually translates the
	// policy-narrowed schema (NarrowSchema can only remove properties), so
	// this can over-report relative to what a specific policy's agent sees,
	// but never under-report. Plugin-sourced tools go through the same
	// lossy-translation path at runtime but are not listed on this page
	// (ListTools returns only db.McpTool rows scoped to an MCP server), so
	// they never carry this field.
	SimplifiedFor []string `json:"simplified_for"`
}

func toolToResponse(t db.McpTool, serverName string, simplifiedFor []string) mcpToolResponse {
	return mcpToolResponse{
		ID:       t.ID,
		ServerID: t.ServerID,
		Name:     t.Name,
		// InputSchema is stored as a JSON string in the DB; cast directly to
		// json.RawMessage to avoid double-encoding it as a JSON string in the response.
		Description:   t.Description,
		InputSchema:   json.RawMessage(t.InputSchema),
		Enabled:       t.Enabled != 0,
		Source:        "mcp:" + serverName,
		SimplifiedFor: simplifiedFor,
	}
}

// canonicalSchemaForSimplification returns t.CanonicalSchema, or an empty
// json.RawMessage when it is unset (NULL, or a non-nil pointer to an
// empty/whitespace-only string). There is deliberately NO fallback to the
// raw input_schema here, unlike mcp.ResolvedTool.SchemaForHeaderParams:
// that fallback is sound for reading x-mcp-header annotations, whose
// presence is provably invariant under schemanorm's byte-level-only
// normalization, but it is NOT sound for this DTO field, whose whole job is
// to shadow what #744's exact-enforcement gate actually compiled.
// internal/execution/agent/argvalidate.go's compileArgValidator returns nil
// — degrading enforcement to ADR-017 key-presence validation only — for
// exactly the same NULL/empty condition checked here, and
// internal/mcp/canonical.go stores the raw schema with canonical_schema
// left NULL whenever schemanorm.Normalize fails: a state a hostile MCP
// server can select at will by emitting a schema encoding/json accepts but
// schemanorm rejects (duplicate keys, lone surrogates). Falling back to raw
// would render the chip on those rows and have the UI vouch for an exact-
// enforcement control that is actually switched off for that tool. This
// also matches the issue's literal instruction to compute the hint "on the
// canonical schema".
func canonicalSchemaForSimplification(t db.McpTool) json.RawMessage {
	if t.CanonicalSchema == nil || strings.TrimSpace(*t.CanonicalSchema) == "" {
		return json.RawMessage{}
	}
	return json.RawMessage(*t.CanonicalSchema)
}

// restrictedFeatureSets inverts a provider→features map into
// features→provider-names, dropping every wire that declares full support.
// SchemaFeatureSet is comparable (see its doc in
// internal/llm/schema_features.go), so it can key a map. Today only the
// Google wire is restricted, so N configured providers collapse to at most
// one entry and the per-tool cost stays at one TranslateForFeatures call
// regardless of how many providers are configured.
func restrictedFeatureSets(byProvider map[string]llm.SchemaFeatureSet) map[llm.SchemaFeatureSet][]string {
	out := make(map[llm.SchemaFeatureSet][]string)
	for name, features := range byProvider {
		if features.IsFull() {
			continue
		}
		out[features] = append(out[features], name)
	}
	return out
}

// maxSimplificationHintBytes bounds the per-tool schema size
// simplifiedForProviders will run through TranslateForFeatures. Real MCP
// tool schemas run tens of KB; TranslateForFeatures' own 1 MiB / depth-64 /
// 10k-node caps (schema_simplify.go) only fire AFTER json.Decode has
// already built the parsed tree, and ListTools calls this once per row with
// no aggregate cap on a route reachable by the lowest-privilege auditor
// role — the Tools page fans out one request per server in parallel, and
// this also runs on disabled tools. 64 KiB gives generous headroom over
// real-world schemas while keeping one oversized or adversarial row's cost
// close to zero.
const maxSimplificationHintBytes = 64 * 1024

// simplifiedForProviders returns the sorted names of providers whose wire
// will be shown a rewritten version of schema. Always returns a non-nil
// slice.
//
// A schema over maxSimplificationHintBytes skips translation entirely and
// is DEBUG-logged: an oversized schema is a property of the server that
// published it, not a bug in Gleipnir, so it does not warrant WARN.
//
// A translation failure omits the provider and WARN-logs rather than
// failing the whole request: ErrUnsupportedSchemaFeature /
// ErrSchemaLimitExceeded mean "this wire will REJECT the request outright" —
// a stronger condition than "simplified", and listing such a provider here
// would be an outright false statement. An "unsupported for <provider>" chip
// is a deliberate non-goal of this DTO field. The raw schema bytes are never
// logged, matching internal/mcp/canonical.go and
// internal/execution/agent/argvalidate.go — though ErrUnsupportedSchemaFeature
// embeds a JSON Pointer built from schema key names, so a key name (not a
// value) can appear in the WARN below. That is the same parameter-name
// vocabulary this DTO already returns in input_schema, and slog's JSON
// handler escapes it like every other attribute.
func simplifiedForProviders(serverID, toolName string, schema json.RawMessage, restricted map[llm.SchemaFeatureSet][]string) []string {
	out := make([]string, 0)
	if len(schema) == 0 || len(restricted) == 0 {
		return out
	}
	if len(schema) > maxSimplificationHintBytes {
		slog.Debug("tool schema exceeds simplification size threshold; omitted from simplified_for",
			"server_id", serverID, "tool_name", toolName, "schema_bytes", len(schema))
		return out
	}
	for features, names := range restricted {
		_, lossy, err := llm.TranslateForFeatures(schema, features)
		if err != nil {
			slog.Warn("tool schema cannot be presented to provider; omitted from simplified_for",
				"server_id", serverID, "tool_name", toolName, "providers", names, "err", err)
			continue
		}
		if lossy {
			out = append(out, names...)
		}
	}
	sort.Strings(out) // map iteration is randomized; the DTO must be stable
	return out
}

// ListTools handles GET /api/v1/mcp/servers/{id}/tools.
//
// By default only enabled tools are returned. Passing ?include_disabled=true
// returns all tools (enabled and disabled). The flag is honored for any
// authenticated caller of this route; access control is enforced by the
// route's RequireRole middleware in router.go, not by the handler.
func (h *MCPHandler) ListTools(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx := r.Context()

	server, err := h.store.GetMCPServer(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			httputil.WriteError(w, http.StatusNotFound, "MCP server not found", "")
			return
		}
		httputil.WriteError(w, http.StatusInternalServerError, "failed to get MCP server", err.Error())
		return
	}

	includeDisabled := false
	if v := r.URL.Query().Get("include_disabled"); v != "" {
		if parsed, err := strconv.ParseBool(v); err == nil {
			includeDisabled = parsed
		}
	}

	var rows []db.McpTool
	if includeDisabled {
		rows, err = h.store.ListMCPToolsByServer(ctx, id)
	} else {
		rows, err = h.store.ListEnabledMCPToolsByServer(ctx, id)
	}
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to list MCP tools", err.Error())
		return
	}

	// Build the restricted-feature-set map once per request, not once per
	// tool: restrictedFeatureSets is O(providers), and every row shares the
	// same set of configured providers.
	var restricted map[llm.SchemaFeatureSet][]string
	if h.features != nil {
		restricted = restrictedFeatureSets(h.features.SchemaFeaturesByProvider())
	}

	items := make([]mcpToolResponse, 0, len(rows))
	for _, row := range rows {
		simplifiedFor := simplifiedForProviders(server.ID, row.Name, canonicalSchemaForSimplification(row), restricted)
		items = append(items, toolToResponse(row, server.Name, simplifiedFor))
	}

	httputil.WriteJSON(w, http.StatusOK, items)
}

// SetToolEnabled handles PUT /api/v1/mcp/servers/{id}/tools/{toolID}/enabled.
// Body: {"enabled": bool}. Admin or operator only (enforced by router middleware).
func (h *MCPHandler) SetToolEnabled(w http.ResponseWriter, r *http.Request) {
	serverID := chi.URLParam(r, "id")
	toolID := chi.URLParam(r, "toolID")
	ctx := r.Context()

	if _, err := h.store.GetMCPServer(ctx, serverID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			httputil.WriteError(w, http.StatusNotFound, "MCP server not found", "")
			return
		}
		httputil.WriteError(w, http.StatusInternalServerError, "failed to get MCP server", err.Error())
		return
	}

	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}

	tool, err := h.store.GetMCPTool(ctx, toolID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			httputil.WriteError(w, http.StatusNotFound, "MCP tool not found", "")
			return
		}
		httputil.WriteError(w, http.StatusInternalServerError, "failed to get MCP tool", err.Error())
		return
	}

	if tool.ServerID != serverID {
		httputil.WriteError(w, http.StatusBadRequest, "tool does not belong to this server", "")
		return
	}

	var enabledVal int64
	if body.Enabled {
		enabledVal = 1
	}
	if err := h.store.SetMCPToolEnabled(ctx, db.SetMCPToolEnabledParams{
		ID:      toolID,
		Enabled: enabledVal,
	}); err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to update tool", err.Error())
		return
	}

	// Re-fetch to return the canonical post-update row.
	updated, err := h.store.GetMCPTool(ctx, toolID)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to re-fetch tool after update", err.Error())
		return
	}

	// Re-load the server name so the source field in the response is accurate.
	srv, err := h.store.GetMCPServer(ctx, serverID)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to re-fetch server after tool update", err.Error())
		return
	}

	// Compute simplified_for for the single updated row so the mutation
	// response shape matches the list response — the frontend replaces the
	// cached row from this response, so omitting the field here would make
	// the chip vanish on toggle.
	var restricted map[llm.SchemaFeatureSet][]string
	if h.features != nil {
		restricted = restrictedFeatureSets(h.features.SchemaFeaturesByProvider())
	}
	simplifiedFor := simplifiedForProviders(srv.ID, updated.Name, canonicalSchemaForSimplification(updated), restricted)

	httputil.WriteJSON(w, http.StatusOK, toolToResponse(updated, srv.Name, simplifiedFor))
}

// policyReferencesServer returns true if the raw policy YAML contains any tool
// reference starting with the given server name prefix (e.g. "myserver.").
// Parse failures are treated as no match — a corrupt policy YAML cannot block deletion.
// The feedback block is not checked because the new FeedbackConfig does not reference
// MCP servers — it enables a native runtime channel.
func policyReferencesServer(rawYAML, serverPrefix string) bool {
	var v struct {
		Capabilities struct {
			Tools []struct {
				Tool string `yaml:"tool"`
			} `yaml:"tools"`
		} `yaml:"capabilities"`
	}
	if err := yaml.Unmarshal([]byte(rawYAML), &v); err != nil {
		return false
	}
	for _, t := range v.Capabilities.Tools {
		if strings.HasPrefix(t.Tool, serverPrefix) {
			return true
		}
	}
	return false
}

// isUniqueConstraintError reports whether err is a SQLite UNIQUE constraint violation.
func isUniqueConstraintError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// humanizeMCPError converts a low-level Go network/context error into a short,
// user-facing message. The full error chain is always logged server-side before
// this function is called, so diagnostic information is never lost.
func humanizeMCPError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "Connection timed out"
	}
	if errors.Is(err, context.Canceled) {
		return "Connection canceled"
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) {
		// Inspect the underlying syscall errno to produce a specific message.
		// errors.Is walks the chain, so this covers both direct and wrapped errnos.
		switch {
		case errors.Is(opErr.Err, syscall.ECONNREFUSED):
			return "Could not reach server — connection refused"
		case errors.Is(opErr.Err, syscall.EHOSTUNREACH), errors.Is(opErr.Err, syscall.ENETUNREACH):
			return "Could not reach server — host unreachable"
		}
		return "Network error: " + opErr.Op + " failed"
	}

	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		inner := humanizeMCPError(urlErr.Err)
		// If the inner error was not recognized, return a generic message rather
		// than the raw URL error string which contains internal Go call chains.
		if inner == "Could not complete MCP handshake" {
			return "Could not reach server"
		}
		return inner
	}

	return "Could not complete MCP handshake"
}
