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
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"

	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/http/auth"
	"github.com/rapp992/gleipnir/internal/http/httputil"
	"github.com/rapp992/gleipnir/internal/mcp"
	"github.com/rapp992/gleipnir/internal/model"
)

// MCPHandler serves MCP server management endpoints under /api/v1/mcp/servers.
type MCPHandler struct {
	store    *db.Store
	registry *mcp.Registry
}

// NewMCPHandler creates an MCPHandler backed by the given store and registry.
func NewMCPHandler(store *db.Store, registry *mcp.Registry) *MCPHandler {
	return &MCPHandler{store: store, registry: registry}
}

type mcpServerResponse struct {
	ID               string  `json:"id"`
	Name             string  `json:"name"`
	URL              string  `json:"url"`
	LastDiscoveredAt *string `json:"last_discovered_at"`
	HasDrift         bool    `json:"has_drift"`
	CreatedAt        string  `json:"created_at"`
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

func serverToResponse(s db.McpServer) mcpServerResponse {
	return mcpServerResponse{
		ID:               s.ID,
		Name:             s.Name,
		URL:              s.Url,
		LastDiscoveredAt: s.LastDiscoveredAt,
		HasDrift:         s.HasDrift != 0,
		CreatedAt:        s.CreatedAt,
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
// Always returns HTTP 200; the ok field in the body distinguishes success from failure.
func (h *MCPHandler) TestConnection(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL string `json:"url"`
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

	// Throwaway client — never stored in h.registry or h.store.
	client := mcp.NewClient(body.URL)

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
		items = append(items, serverToResponse(row))
	}

	httputil.WriteJSON(w, http.StatusOK, items)
}

// Create handles POST /api/v1/mcp/servers.
func (h *MCPHandler) Create(w http.ResponseWriter, r *http.Request) {
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

	now := time.Now().UTC().Format(time.RFC3339Nano)
	server, err := h.store.CreateMCPServer(r.Context(), db.CreateMCPServerParams{
		ID:        model.NewULID(),
		Name:      body.Name,
		Url:       body.URL,
		CreatedAt: now,
	})
	if err != nil {
		if isUniqueConstraintError(err) {
			httputil.WriteError(w, http.StatusConflict, "MCP server name already exists", "")
			return
		}
		httputil.WriteError(w, http.StatusInternalServerError, "failed to create MCP server", err.Error())
		return
	}

	resp := mcpServerCreateResponse{
		mcpServerResponse: serverToResponse(server),
	}

	// Attempt auto-discovery; a failure is non-fatal — we still return 201 with the
	// server record plus a discovery_error field so the caller knows to retry.
	if _, err := h.registry.RefreshTools(r.Context(), server.ID); err != nil {
		slog.Warn("MCP auto-discovery failed on server create", "server_id", server.ID, "server_name", body.Name, "err", err)
		errStr := err.Error()
		resp.DiscoveryError = &errStr
	} else {
		// Re-fetch so the response reflects the updated last_discovered_at.
		if updated, err := h.store.GetMCPServer(r.Context(), server.ID); err == nil {
			resp.mcpServerResponse = serverToResponse(updated)
		}
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
			fmt.Sprintf("policies referencing this server: %s", strings.Join(conflicting, ", ")))
		return
	}

	// mcp_tools rows are cascade-deleted by the FK constraint on DELETE.
	if err := h.store.DeleteMCPServer(r.Context(), id); err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to delete MCP server", err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
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
}

func toolToResponse(t db.McpTool) mcpToolResponse {
	return mcpToolResponse{
		ID:          t.ID,
		ServerID:    t.ServerID,
		Name:        t.Name,
		Description: t.Description,
		// InputSchema is stored as a JSON string in the DB; cast directly to
		// json.RawMessage to avoid double-encoding it as a JSON string in the response.
		InputSchema: json.RawMessage(t.InputSchema),
		Enabled:     t.Enabled != 0,
	}
}

// ListTools handles GET /api/v1/mcp/servers/{id}/tools.
//
// By default only enabled tools are returned, so the policy form's capability
// registry never surfaces disabled tools. Passing ?include_disabled=true
// returns all tools (enabled and disabled), but only when the caller holds
// admin or operator role — auditors receive the default enabled-only list.
func (h *MCPHandler) ListTools(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx := r.Context()

	if _, err := h.store.GetMCPServer(ctx, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			httputil.WriteError(w, http.StatusNotFound, "MCP server not found", "")
			return
		}
		httputil.WriteError(w, http.StatusInternalServerError, "failed to get MCP server", err.Error())
		return
	}

	// include_disabled is honored only for admin and operator; silently ignored
	// for auditors so their existing read access continues to work unchanged.
	includeDisabled := false
	if v := r.URL.Query().Get("include_disabled"); v != "" {
		if parsed, err := strconv.ParseBool(v); err == nil && parsed {
			if user, ok := auth.UserFromContext(ctx); ok &&
				(user.HasRole(model.RoleAdmin) || user.HasRole(model.RoleOperator)) {
				includeDisabled = true
			}
		}
	}

	var (
		rows []db.McpTool
		err  error
	)
	if includeDisabled {
		rows, err = h.store.ListMCPToolsByServer(ctx, id)
	} else {
		rows, err = h.store.ListEnabledMCPToolsByServer(ctx, id)
	}
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to list MCP tools", err.Error())
		return
	}

	items := make([]mcpToolResponse, 0, len(rows))
	for _, row := range rows {
		items = append(items, toolToResponse(row))
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

	httputil.WriteJSON(w, http.StatusOK, toolToResponse(updated))
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
