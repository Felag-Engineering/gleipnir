package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rapp992/gleipnir/internal/admin"
	"github.com/rapp992/gleipnir/internal/arcade"
	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/http/httputil"
	"github.com/rapp992/gleipnir/internal/mcp"
)

// arcadeHTTPTimeout bounds outbound calls to Arcade. The /wait endpoint uses
// Arcade's wait=10 long-poll, so 30s leaves generous headroom for that single
// request while still preventing a runaway goroutine if Arcade hangs.
const arcadeHTTPTimeout = 30 * time.Second

// ArcadeHandler serves the Arcade pre-authorization endpoints under
// /api/v1/mcp/servers/{id}/arcade. It drives Arcade's /v1/auth/authorize
// REST API on behalf of the operator (ADR-040).
type ArcadeHandler struct {
	store      *db.Store
	encKey     []byte
	httpClient *http.Client

	// newClient is the constructor for the Arcade REST client. It is a field
	// (not a direct call to arcade.NewClient) so tests can inject a stub that
	// points at an httptest.Server without monkey-patching package globals.
	newClient func(httpClient *http.Client, apiKey string, opts ...arcade.Option) *arcade.Client
}

// NewArcadeHandler constructs an ArcadeHandler with the given store and
// encryption key. The encryption key is used to decrypt auth_headers_encrypted
// to extract the Arcade API key and user ID.
func NewArcadeHandler(store *db.Store, encKey []byte) *ArcadeHandler {
	return &ArcadeHandler{
		store:      store,
		encKey:     encKey,
		httpClient: &http.Client{Timeout: arcadeHTTPTimeout},
		newClient:  arcade.NewClient,
	}
}

// ArcadeClientFactory is the type for the newClient seam on ArcadeHandler.
// Exposed so tests in api_test package can construct a stub handler.
type ArcadeClientFactory = func(*http.Client, string, ...arcade.Option) *arcade.Client

// NewArcadeHandlerWithClientFactory constructs an ArcadeHandler with a custom
// client factory. Used in tests to point the handler at a stub Arcade server.
func NewArcadeHandlerWithClientFactory(store *db.Store, encKey []byte, factory ArcadeClientFactory) *ArcadeHandler {
	return &ArcadeHandler{
		store:      store,
		encKey:     encKey,
		httpClient: &http.Client{Timeout: arcadeHTTPTimeout},
		newClient:  factory,
	}
}

// arcadeAuthorizeRequest is the request body for POST .../arcade/authorize.
type arcadeAuthorizeRequest struct {
	Toolkit string `json:"toolkit"`
}

// arcadeAuthorizeWaitRequest is the request body for POST .../arcade/authorize/wait.
type arcadeAuthorizeWaitRequest struct {
	Toolkit string `json:"toolkit"`
	AuthID  string `json:"auth_id"`
}

// arcadeAuthorizeResponse is the shared response shape for both authorize endpoints.
type arcadeAuthorizeResponse struct {
	Status string `json:"status"`
	URL    string `json:"url,omitempty"`
	AuthID string `json:"auth_id,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Authorize handles POST /api/v1/mcp/servers/{id}/arcade/authorize.
// It walks the tools in the named toolkit and calls Arcade's authorize API
// until it hits the first pending grant (which the user must click through)
// or all tools are confirmed authorized.
func (h *ArcadeHandler) Authorize(w http.ResponseWriter, r *http.Request) {
	var body arcadeAuthorizeRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	if body.Toolkit == "" {
		httputil.WriteError(w, http.StatusBadRequest, "toolkit is required", "")
		return
	}

	id := chi.URLParam(r, "id")
	_, apiKey, userID, status, errMsg := h.loadArcadeServer(r.Context(), id)
	if status != 0 {
		httputil.WriteError(w, status, errMsg, "")
		return
	}

	tools, err := h.store.ListMCPToolsByServer(r.Context(), id)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to list MCP tools", err.Error())
		return
	}

	filtered := filterByToolkit(tools, body.Toolkit)
	if len(filtered) == 0 {
		httputil.WriteError(w, http.StatusBadRequest, "toolkit not found", "")
		return
	}

	client := h.newClient(h.httpClient, apiKey)

	for _, tool := range filtered {
		resp, err := client.Authorize(r.Context(), tool.Name, userID)
		if err != nil {
			httputil.WriteError(w, http.StatusBadGateway, "arcade authorize failed", err.Error())
			return
		}
		if resp.Status == "pending" {
			httputil.WriteJSON(w, http.StatusOK, arcadeAuthorizeResponse{
				Status: "pending",
				URL:    resp.URL,
				AuthID: resp.ID,
			})
			return
		}
	}

	httputil.WriteJSON(w, http.StatusOK, arcadeAuthorizeResponse{Status: "completed"})
}

// AuthorizeWait handles POST /api/v1/mcp/servers/{id}/arcade/authorize/wait.
// It calls Arcade's long-poll status endpoint once (bounded to statusWaitSeconds)
// and relays the result to the frontend. If the status is still pending, the
// frontend re-issues this endpoint. When completed, it re-walks the remaining
// toolkit tools to surface any subsequent grant that is still needed.
func (h *ArcadeHandler) AuthorizeWait(w http.ResponseWriter, r *http.Request) {
	var body arcadeAuthorizeWaitRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	if body.Toolkit == "" {
		httputil.WriteError(w, http.StatusBadRequest, "toolkit is required", "")
		return
	}
	if body.AuthID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "auth_id is required", "")
		return
	}

	id := chi.URLParam(r, "id")
	_, apiKey, userID, status, errMsg := h.loadArcadeServer(r.Context(), id)
	if status != 0 {
		httputil.WriteError(w, status, errMsg, "")
		return
	}

	client := h.newClient(h.httpClient, apiKey)

	waited, err := client.WaitForCompletion(r.Context(), body.AuthID)
	if err != nil {
		httputil.WriteError(w, http.StatusBadGateway, "arcade wait failed", err.Error())
		return
	}

	switch waited.Status {
	case "pending":
		// The user has not yet clicked through the OAuth flow. Return pending so
		// the frontend can re-issue this endpoint after a short delay.
		httputil.WriteJSON(w, http.StatusOK, arcadeAuthorizeResponse{
			Status: "pending",
			AuthID: waited.ID,
			URL:    waited.URL,
		})
		return

	case "failed":
		httputil.WriteJSON(w, http.StatusOK, arcadeAuthorizeResponse{
			Status: "failed",
		})
		return

	case "completed":
		// Re-walk the toolkit from the beginning. Most tools will return
		// "completed" immediately; this surfaces the next tool that still needs
		// a grant. Re-walking is cheap because Arcade returns "completed" for
		// already-authorized (user_id, tool) pairs instantly.
		tools, err := h.store.ListMCPToolsByServer(r.Context(), id)
		if err != nil {
			httputil.WriteError(w, http.StatusInternalServerError, "failed to list MCP tools", err.Error())
			return
		}

		filtered := filterByToolkit(tools, body.Toolkit)
		for _, tool := range filtered {
			resp, err := client.Authorize(r.Context(), tool.Name, userID)
			if err != nil {
				httputil.WriteError(w, http.StatusBadGateway, "arcade authorize failed", err.Error())
				return
			}
			if resp.Status == "pending" {
				httputil.WriteJSON(w, http.StatusOK, arcadeAuthorizeResponse{
					Status: "pending",
					URL:    resp.URL,
					AuthID: resp.ID,
				})
				return
			}
		}

		httputil.WriteJSON(w, http.StatusOK, arcadeAuthorizeResponse{Status: "completed"})

	default:
		// Arcade returned a status outside the expected set (pending|completed|failed).
		// Treat as a gateway error so the frontend surfaces a clear failure rather
		// than receiving a silent empty response.
		httputil.WriteError(w, http.StatusBadGateway, "unexpected status from Arcade", waited.Status)
	}
}

// loadArcadeServer loads and validates an MCP server for Arcade operations.
// It decrypts the auth headers, verifies the server is an Arcade gateway,
// and extracts the API key and user ID.
// Returns (server, apiKey, userID, 0, "") on success.
// Returns (zero, "", "", httpStatus, errorMessage) on any failure.
func (h *ArcadeHandler) loadArcadeServer(ctx context.Context, id string) (db.McpServer, string, string, int, string) {
	server, err := h.store.GetMCPServer(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return db.McpServer{}, "", "", http.StatusNotFound, "MCP server not found"
		}
		return db.McpServer{}, "", "", http.StatusInternalServerError, "failed to get MCP server"
	}

	if h.encKey == nil || server.AuthHeadersEncrypted == nil {
		return db.McpServer{}, "", "", http.StatusInternalServerError, "missing required Arcade headers"
	}

	plaintext, err := admin.Decrypt(h.encKey, *server.AuthHeadersEncrypted)
	if err != nil {
		slog.Warn("failed to decrypt MCP server auth headers for Arcade handler",
			"server_id", server.ID, "err", err)
		return db.McpServer{}, "", "", http.StatusInternalServerError, "failed to load auth headers"
	}

	headers, err := mcp.UnmarshalAuthHeaders([]byte(plaintext))
	if err != nil {
		slog.Warn("failed to unmarshal MCP server auth headers for Arcade handler",
			"server_id", server.ID, "err", err)
		return db.McpServer{}, "", "", http.StatusInternalServerError, "failed to load auth headers"
	}

	// Build name→value map and name list for IsArcadeGateway.
	names := make([]string, 0, len(headers))
	valueByName := make(map[string]string, len(headers))
	for _, hdr := range headers {
		names = append(names, hdr.Name)
		valueByName[strings.ToLower(hdr.Name)] = hdr.Value
	}

	if !arcade.IsArcadeGateway(server.Url, names) {
		return db.McpServer{}, "", "", http.StatusBadRequest, "server is not an Arcade gateway"
	}

	rawAuth, ok := valueByName["authorization"]
	if !ok || rawAuth == "" {
		return db.McpServer{}, "", "", http.StatusInternalServerError, "missing required Arcade headers"
	}
	// RFC 6750 requires "Bearer" capitalized; tolerate lowercase as a
	// common defensive practice. Two cases cover all real-world usage.
	apiKey := strings.TrimPrefix(rawAuth, "Bearer ")
	apiKey = strings.TrimPrefix(apiKey, "bearer ")
	if apiKey == "" {
		return db.McpServer{}, "", "", http.StatusInternalServerError, "missing required Arcade headers"
	}

	userID, ok := valueByName["arcade-user-id"]
	if !ok || userID == "" {
		return db.McpServer{}, "", "", http.StatusInternalServerError, "missing required Arcade headers"
	}

	return server, apiKey, userID, 0, ""
}

// filterByToolkit returns the tools whose qualified name belongs to the given
// toolkit prefix (case-sensitive), sorted by name for deterministic traversal.
func filterByToolkit(tools []db.McpTool, toolkit string) []db.McpTool {
	var filtered []db.McpTool
	for _, t := range tools {
		tk, _ := arcade.SplitToolkit(t.Name)
		if tk == toolkit {
			filtered = append(filtered, t)
		}
	}
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].Name < filtered[j].Name
	})
	return filtered
}
