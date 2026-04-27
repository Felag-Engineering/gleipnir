package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/felag-engineering/gleipnir/internal/admin"
	"github.com/felag-engineering/gleipnir/internal/arcade"
	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/http/api"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

// newArcadeRouter wires a chi router with the Arcade handler for testing.
// Pass a non-nil newClient to inject a stub pointing at an httptest.Server.
func newArcadeRouter(store *db.Store, encKey []byte, newClient api.ArcadeClientFactory) http.Handler {
	r := chi.NewRouter()
	var h *api.ArcadeHandler
	if newClient != nil {
		h = api.NewArcadeHandlerWithClientFactory(store, encKey, newClient)
	} else {
		h = api.NewArcadeHandler(store, encKey)
	}
	r.Post("/servers/{id}/arcade/authorize", h.Authorize)
	r.Post("/servers/{id}/arcade/authorize/wait", h.AuthorizeWait)
	return r
}

// encryptArcadeHeaders encrypts a set of auth headers for an MCP server row.
func encryptArcadeHeaders(t *testing.T, encKey []byte, headers []mcp.AuthHeader) *string {
	t.Helper()
	raw, err := mcp.MarshalAuthHeaders(headers)
	if err != nil {
		t.Fatalf("MarshalAuthHeaders: %v", err)
	}
	ct, err := admin.Encrypt(encKey, string(raw))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	return &ct
}

// insertArcadeServer inserts an MCP server with Arcade-compatible URL and
// encrypted headers.
func insertArcadeServer(t *testing.T, store *db.Store, encKey []byte, extraHeaders ...mcp.AuthHeader) string {
	t.Helper()
	headers := []mcp.AuthHeader{
		{Name: "Authorization", Value: "Bearer test-api-key"},
		{Name: "Arcade-User-ID", Value: "user@example.com"},
	}
	headers = append(headers, extraHeaders...)
	ct := encryptArcadeHeaders(t, encKey, headers)

	id := model.NewULID()
	_, err := store.CreateMCPServer(context.Background(), db.CreateMCPServerParams{
		ID:                   id,
		Name:                 "arcade-test",
		Url:                  "https://api.arcade.dev/mcp/test",
		CreatedAt:            "2024-01-01T00:00:00Z",
		AuthHeadersEncrypted: ct,
	})
	if err != nil {
		t.Fatalf("insertArcadeServer: %v", err)
	}
	return id
}

// stubArcadeClient returns a newClient function that points at the given
// httptest.Server URL, using that server's client.
func stubArcadeClient(stub *httptest.Server) func(*http.Client, string, ...arcade.Option) *arcade.Client {
	return func(_ *http.Client, apiKey string, opts ...arcade.Option) *arcade.Client {
		return arcade.NewClient(stub.Client(), apiKey, append(opts, arcade.WithBaseURL(stub.URL))...)
	}
}

// decodeArcadeResponse decodes the data envelope into an arcadeAuthorizeResponse-shaped struct.
func decodeArcadeResponse(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode response: %v; body: %s", err, string(body))
	}
	return envelope.Data
}

func TestArcadeAuthorize_NotArcadeServer(t *testing.T) {
	store := testutil.NewTestStore(t)
	encKey := make([]byte, 32)

	// Insert a non-Arcade server (wrong URL).
	headers := []mcp.AuthHeader{
		{Name: "Authorization", Value: "Bearer test-key"},
		{Name: "Arcade-User-ID", Value: "user@example.com"},
	}
	ct := encryptArcadeHeaders(t, encKey, headers)
	id := model.NewULID()
	_, err := store.CreateMCPServer(context.Background(), db.CreateMCPServerParams{
		ID:                   id,
		Name:                 "not-arcade",
		Url:                  "https://api.notarcade.dev/mcp/test",
		CreatedAt:            "2024-01-01T00:00:00Z",
		AuthHeadersEncrypted: ct,
	})
	if err != nil {
		t.Fatalf("CreateMCPServer: %v", err)
	}

	r := newArcadeRouter(store, encKey, nil)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	body, _ := json.Marshal(map[string]string{"toolkit": "Gmail"})
	resp, err := http.Post(srv.URL+"/servers/"+id+"/arcade/authorize", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestArcadeAuthorize_ToolkitNotFound(t *testing.T) {
	store := testutil.NewTestStore(t)
	encKey := make([]byte, 32)
	id := insertArcadeServer(t, store, encKey)

	// Insert tools for a different toolkit.
	insertTestMCPTool(t, store, id, "Slack_SendMessage")

	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"id": "a1", "status": "completed"})
	}))
	t.Cleanup(stub.Close)

	r := newArcadeRouter(store, encKey, stubArcadeClient(stub))
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	body, _ := json.Marshal(map[string]string{"toolkit": "Gmail"})
	resp, err := http.Post(srv.URL+"/servers/"+id+"/arcade/authorize", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestArcadeAuthorize_AllCompleted(t *testing.T) {
	store := testutil.NewTestStore(t)
	encKey := make([]byte, 32)
	id := insertArcadeServer(t, store, encKey)

	insertTestMCPTool(t, store, id, "Gmail_SendEmail")
	insertTestMCPTool(t, store, id, "Gmail_ListEmails")

	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"id": "a1", "status": "completed"})
	}))
	t.Cleanup(stub.Close)

	r := newArcadeRouter(store, encKey, stubArcadeClient(stub))
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	body, _ := json.Marshal(map[string]string{"toolkit": "Gmail"})
	resp, err := http.Post(srv.URL+"/servers/"+id+"/arcade/authorize", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	respBody := make([]byte, 0, 256)
	buf := bytes.NewBuffer(respBody)
	buf.ReadFrom(resp.Body)

	data := decodeArcadeResponse(t, buf.Bytes())
	if data["status"] != "completed" {
		t.Errorf("expected status completed, got %v", data["status"])
	}
}

// TestArcadeAuthorize_ConvertsUnderscoreToDotForRestAPI verifies that when
// the handler calls Arcade's REST /v1/auth/authorize, it converts MCP-style
// underscore tool names ("Gmail_SendEmail") to the dot form
// ("Gmail.SendEmail") that the REST endpoint expects. MCP discovery returns
// underscore-separated names; the REST authorize API uses dots.
func TestArcadeAuthorize_ConvertsUnderscoreToDotForRestAPI(t *testing.T) {
	store := testutil.NewTestStore(t)
	encKey := make([]byte, 32)
	id := insertArcadeServer(t, store, encKey)

	insertTestMCPTool(t, store, id, "Gmail_SendEmail")

	var gotToolName string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotToolName = body["tool_name"]
		json.NewEncoder(w).Encode(map[string]string{"id": "a1", "status": "completed"})
	}))
	t.Cleanup(stub.Close)

	r := newArcadeRouter(store, encKey, stubArcadeClient(stub))
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	body, _ := json.Marshal(map[string]string{"toolkit": "Gmail"})
	resp, err := http.Post(srv.URL+"/servers/"+id+"/arcade/authorize", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if gotToolName != "Gmail.SendEmail" {
		t.Errorf("expected Arcade REST to receive dot-form tool name %q, got %q",
			"Gmail.SendEmail", gotToolName)
	}
}

func TestArcadeAuthorize_FirstToolPending(t *testing.T) {
	store := testutil.NewTestStore(t)
	encKey := make([]byte, 32)
	id := insertArcadeServer(t, store, encKey)

	insertTestMCPTool(t, store, id, "Gmail_ListEmails")
	insertTestMCPTool(t, store, id, "Gmail_SendEmail")

	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// First call returns pending; subsequent calls return completed.
		json.NewEncoder(w).Encode(map[string]string{
			"id":     "auth-123",
			"status": "pending",
			"url":    "https://arcade.dev/oauth",
		})
	}))
	t.Cleanup(stub.Close)

	r := newArcadeRouter(store, encKey, stubArcadeClient(stub))
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	body, _ := json.Marshal(map[string]string{"toolkit": "Gmail"})
	resp, err := http.Post(srv.URL+"/servers/"+id+"/arcade/authorize", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var buf bytes.Buffer
	buf.ReadFrom(resp.Body)
	data := decodeArcadeResponse(t, buf.Bytes())
	if data["status"] != "pending" {
		t.Errorf("expected status pending, got %v", data["status"])
	}
	if data["auth_id"] != "auth-123" {
		t.Errorf("expected auth_id auth-123, got %v", data["auth_id"])
	}
}

func TestArcadeAuthorize_MissingAuthHeadersEncrypted(t *testing.T) {
	store := testutil.NewTestStore(t)
	encKey := make([]byte, 32)

	// Insert server with no encrypted headers at all.
	id := model.NewULID()
	_, err := store.CreateMCPServer(context.Background(), db.CreateMCPServerParams{
		ID:        id,
		Name:      "arcade-no-headers",
		Url:       "https://api.arcade.dev/mcp/test",
		CreatedAt: "2024-01-01T00:00:00Z",
		// AuthHeadersEncrypted intentionally nil.
	})
	if err != nil {
		t.Fatalf("CreateMCPServer: %v", err)
	}

	r := newArcadeRouter(store, encKey, nil)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	body, _ := json.Marshal(map[string]string{"toolkit": "Gmail"})
	resp, err := http.Post(srv.URL+"/servers/"+id+"/arcade/authorize", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", resp.StatusCode)
	}
}

func TestArcadeAuthorize_MissingArcadeUserID(t *testing.T) {
	store := testutil.NewTestStore(t)
	encKey := make([]byte, 32)

	// Headers only have Authorization — missing Arcade-User-ID.
	headers := []mcp.AuthHeader{
		{Name: "Authorization", Value: "Bearer test-key"},
	}
	ct := encryptArcadeHeaders(t, encKey, headers)
	id := model.NewULID()
	_, err := store.CreateMCPServer(context.Background(), db.CreateMCPServerParams{
		ID:                   id,
		Name:                 "arcade-no-userid",
		Url:                  "https://api.arcade.dev/mcp/test",
		CreatedAt:            "2024-01-01T00:00:00Z",
		AuthHeadersEncrypted: ct,
	})
	if err != nil {
		t.Fatalf("CreateMCPServer: %v", err)
	}

	r := newArcadeRouter(store, encKey, nil)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	body, _ := json.Marshal(map[string]string{"toolkit": "Gmail"})
	resp, err := http.Post(srv.URL+"/servers/"+id+"/arcade/authorize", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	// Not Arcade gateway (missing required header) → 400
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

// Operators paste header values into a UI text field; trailing whitespace and
// the lowercase "bearer" form must round-trip without mangling the API key.
func TestArcadeAuthorize_BearerHeaderTolerantOfWhitespaceAndCase(t *testing.T) {
	cases := []struct {
		name      string
		authValue string
	}{
		{"trailing newline", "Bearer test-api-key\n"},
		{"leading whitespace", "  Bearer test-api-key"},
		{"tab between scheme and key", "Bearer\ttest-api-key"},
		{"lowercase scheme", "bearer test-api-key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := testutil.NewTestStore(t)
			encKey := make([]byte, 32)
			headers := []mcp.AuthHeader{
				{Name: "Authorization", Value: tc.authValue},
				{Name: "Arcade-User-ID", Value: "user@example.com"},
			}
			ct := encryptArcadeHeaders(t, encKey, headers)
			id := model.NewULID()
			_, err := store.CreateMCPServer(context.Background(), db.CreateMCPServerParams{
				ID:                   id,
				Name:                 "arcade-ws",
				Url:                  "https://api.arcade.dev/mcp/test",
				CreatedAt:            "2024-01-01T00:00:00Z",
				AuthHeadersEncrypted: ct,
			})
			if err != nil {
				t.Fatalf("CreateMCPServer: %v", err)
			}
			insertTestMCPTool(t, store, id, "Gmail_SendEmail")

			var gotKey string
			stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotKey = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				json.NewEncoder(w).Encode(map[string]string{"id": "a1", "status": "completed"})
			}))
			t.Cleanup(stub.Close)

			r := newArcadeRouter(store, encKey, stubArcadeClient(stub))
			srv := httptest.NewServer(r)
			t.Cleanup(srv.Close)

			body, _ := json.Marshal(map[string]string{"toolkit": "Gmail"})
			resp, err := http.Post(srv.URL+"/servers/"+id+"/arcade/authorize", "application/json", bytes.NewReader(body))
			if err != nil {
				t.Fatalf("POST: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				t.Fatalf("expected 200, got %d", resp.StatusCode)
			}
			if gotKey != "test-api-key" {
				t.Errorf("Bearer parsing produced %q, want %q", gotKey, "test-api-key")
			}
		})
	}
}

func TestArcadeAuthorizeWait_Pending(t *testing.T) {
	store := testutil.NewTestStore(t)
	encKey := make([]byte, 32)
	id := insertArcadeServer(t, store, encKey)

	var capturedURL string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedURL = r.URL.String()
		json.NewEncoder(w).Encode(map[string]string{
			"id":     "auth-456",
			"status": "pending",
			"url":    "https://arcade.dev/oauth",
		})
	}))
	t.Cleanup(stub.Close)

	r := newArcadeRouter(store, encKey, stubArcadeClient(stub))
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	body, _ := json.Marshal(map[string]string{"toolkit": "Gmail", "auth_id": "auth-456"})
	resp, err := http.Post(srv.URL+"/servers/"+id+"/arcade/authorize/wait", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var buf bytes.Buffer
	buf.ReadFrom(resp.Body)
	data := decodeArcadeResponse(t, buf.Bytes())
	if data["status"] != "pending" {
		t.Errorf("expected status pending, got %v", data["status"])
	}
	if !strings.Contains(capturedURL, "wait=10") {
		t.Errorf("expected wait=10 in URL, got %q", capturedURL)
	}
}

func TestArcadeAuthorizeWait_Completed(t *testing.T) {
	store := testutil.NewTestStore(t)
	encKey := make([]byte, 32)
	id := insertArcadeServer(t, store, encKey)

	insertTestMCPTool(t, store, id, "Gmail_SendEmail")

	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"id": "a1", "status": "completed"})
	}))
	t.Cleanup(stub.Close)

	r := newArcadeRouter(store, encKey, stubArcadeClient(stub))
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	body, _ := json.Marshal(map[string]string{"toolkit": "Gmail", "auth_id": "auth-789"})
	resp, err := http.Post(srv.URL+"/servers/"+id+"/arcade/authorize/wait", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var buf bytes.Buffer
	buf.ReadFrom(resp.Body)
	data := decodeArcadeResponse(t, buf.Bytes())
	if data["status"] != "completed" {
		t.Errorf("expected status completed, got %v", data["status"])
	}
}

func TestArcadeAuthorizeWait_URLContainsWait10(t *testing.T) {
	store := testutil.NewTestStore(t)
	encKey := make([]byte, 32)
	id := insertArcadeServer(t, store, encKey)

	var capturedStatusURL string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "auth/status") {
			capturedStatusURL = r.URL.String()
		}
		json.NewEncoder(w).Encode(map[string]string{"id": "a1", "status": "completed"})
	}))
	t.Cleanup(stub.Close)

	r := newArcadeRouter(store, encKey, stubArcadeClient(stub))
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	body, _ := json.Marshal(map[string]string{"toolkit": "Gmail", "auth_id": "auth-xyz"})
	_, err := http.Post(srv.URL+"/servers/"+id+"/arcade/authorize/wait", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}

	if !strings.Contains(capturedStatusURL, "wait=10") {
		t.Errorf("expected wait=10 in status URL, got %q", capturedStatusURL)
	}
}
