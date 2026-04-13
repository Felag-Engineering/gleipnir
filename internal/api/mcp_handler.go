package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"

	"github.com/rapp992/gleipnir/internal/db"
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
		WriteError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	if body.URL == "" {
		WriteError(w, http.StatusBadRequest, "url is required", "")
		return
	}
	if err := mcp.ValidateServerURL(r.Context(), body.URL); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid url", err.Error())
		return
	}

	// Throwaway client — never stored in h.registry or h.store.
	client := mcp.NewClient(body.URL)

	// 5-second deadline governs the entire handshake; no separate client timeout needed.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	tools, err := client.DiscoverTools(ctx)
	if err != nil {
		WriteJSON(w, http.StatusOK, testConnectionResponse{
			OK:        false,
			ToolCount: 0,
			Tools:     []string{},
			Error:     err.Error(),
		})
		return
	}

	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Name
	}
	WriteJSON(w, http.StatusOK, testConnectionResponse{
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
		WriteError(w, http.StatusInternalServerError, "failed to list MCP servers", err.Error())
		return
	}

	items := make([]mcpServerResponse, 0, len(rows))
	for _, row := range rows {
		items = append(items, serverToResponse(row))
	}

	WriteJSON(w, http.StatusOK, items)
}

// Create handles POST /api/v1/mcp/servers.
func (h *MCPHandler) Create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}

	if body.Name == "" {
		WriteError(w, http.StatusBadRequest, "name is required", "")
		return
	}
	if err := mcp.ValidateServerURL(r.Context(), body.URL); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid url", err.Error())
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
			WriteError(w, http.StatusConflict, "MCP server name already exists", "")
			return
		}
		WriteError(w, http.StatusInternalServerError, "failed to create MCP server", err.Error())
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

	WriteCreated(w, "/api/v1/mcp/servers/"+server.ID, resp)
}

// Delete handles DELETE /api/v1/mcp/servers/{id}.
func (h *MCPHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	server, err := h.store.GetMCPServer(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			WriteError(w, http.StatusNotFound, "MCP server not found", "")
			return
		}
		WriteError(w, http.StatusInternalServerError, "failed to get MCP server", err.Error())
		return
	}

	// Check whether any active policy references a tool from this server.
	// Tool references use dot-notation: serverName.toolName, so we check for
	// the server name prefix to catch all tools from this server.
	policies, err := h.store.ListPolicies(r.Context())
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to list policies", err.Error())
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
		WriteError(w, http.StatusConflict, "MCP server is referenced by active policies",
			fmt.Sprintf("policies referencing this server: %s", strings.Join(conflicting, ", ")))
		return
	}

	// mcp_tools rows are cascade-deleted by the FK constraint on DELETE.
	if err := h.store.DeleteMCPServer(r.Context(), id); err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to delete MCP server", err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// Discover handles POST /api/v1/mcp/servers/{id}/discover.
func (h *MCPHandler) Discover(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	if _, err := h.store.GetMCPServer(r.Context(), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			WriteError(w, http.StatusNotFound, "MCP server not found", "")
			return
		}
		WriteError(w, http.StatusInternalServerError, "failed to get MCP server", err.Error())
		return
	}

	diff, err := h.registry.RefreshTools(r.Context(), id)
	if err != nil {
		slog.Error("MCP discovery failed", "server_id", id, "err", err)
		WriteError(w, http.StatusInternalServerError, "discovery failed", err.Error())
		return
	}

	WriteJSON(w, http.StatusOK, diffToResponse(diff))
}

type mcpToolResponse struct {
	ID          string          `json:"id"`
	ServerID    string          `json:"server_id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
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
	}
}

// ListTools handles GET /api/v1/mcp/servers/{id}/tools.
func (h *MCPHandler) ListTools(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx := r.Context()

	if _, err := h.store.GetMCPServer(ctx, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			WriteError(w, http.StatusNotFound, "MCP server not found", "")
			return
		}
		WriteError(w, http.StatusInternalServerError, "failed to get MCP server", err.Error())
		return
	}

	rows, err := h.store.ListMCPToolsByServer(ctx, id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to list MCP tools", err.Error())
		return
	}

	items := make([]mcpToolResponse, 0, len(rows))
	for _, row := range rows {
		items = append(items, toolToResponse(row))
	}

	WriteJSON(w, http.StatusOK, items)
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
