package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rapp992/gleipnir/internal/admin"
	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/execution/run"
	"github.com/rapp992/gleipnir/internal/http/api"
	"github.com/rapp992/gleipnir/internal/http/auth"
	"github.com/rapp992/gleipnir/internal/http/sse"
	"github.com/rapp992/gleipnir/internal/llm"
	"github.com/rapp992/gleipnir/internal/mcp"
	"github.com/rapp992/gleipnir/internal/policy"
	"github.com/rapp992/gleipnir/internal/testutil"
	"github.com/rapp992/gleipnir/internal/trigger"
)

// buildTestRouterWithStore is the shared core: it lets callers inject the store
// so they can seed rows before the router is built.
func buildTestRouterWithStore(t *testing.T, store *db.Store) http.Handler {
	t.Helper()

	broadcaster := sse.NewBroadcaster()
	sseHandler := sse.NewHandler(broadcaster)

	registry := mcp.NewRegistry(store.Queries())
	runManager := run.NewRunManager()

	noopClient := testutil.NewNoopLLMClient()
	providerRegistry := llm.NewProviderRegistry()
	providerRegistry.Register("anthropic", noopClient)

	adminQuerier := admin.NewQuerierAdapter(store.Queries())
	adminHandler := admin.NewHandler(adminQuerier, nil, []string{"anthropic"}, nil, nil, nil)

	launcher := run.NewRunLauncher(run.RunLauncherConfig{
		Store:                  store,
		Registry:               registry,
		Manager:                runManager,
		AgentFactory:           run.NewAgentFactory(providerRegistry),
		Publisher:              broadcaster,
		DefaultFeedbackTimeout: 30 * time.Minute,
		ModelResolver:          adminHandler,
	})
	webhookHandler := trigger.NewWebhookHandler(store, launcher, trigger.NewSecretLoader(store.Queries(), nil), adminHandler)
	openaiCompatHandler := admin.NewOpenAICompatHandler(nil, nil, providerRegistry, noopConnectionTester)

	authHandler := auth.NewHandler(store.Queries(), store.DB())
	settingsHandler := auth.NewSettingsHandler(store.Queries())

	policyService := policy.NewService(store, nil, providerRegistry, providerRegistry, adminHandler)
	policyWebhookHandler := api.NewPolicyWebhookHandler(policyService)

	return api.BuildRouter(api.RouterConfig{
		Handlers: api.HandlerBundle{
			AuthHandler:          authHandler,
			SettingsHandler:      settingsHandler,
			AdminHandler:         adminHandler,
			OpenAICompatHandler:  openaiCompatHandler,
			WebhookHandler:       webhookHandler,
			SSEHandler:           sseHandler,
			PolicyWebhookHandler: policyWebhookHandler,
		},
		Services: api.BackgroundServices{
			Store:            store,
			Broadcaster:      broadcaster,
			Registry:         registry,
			RunManager:       runManager,
			Launcher:         launcher,
			ModelLister:      providerRegistry,
			ProviderRegistry: providerRegistry,
			// ModelFilter, Poller, Scheduler, Cron, EncryptionKey intentionally
			// left as zero values — tests don't require them.
		},
		Metadata: api.Metadata{
			Version:   "test",
			StartTime: time.Now(),
			DBPath:    "",
		},
	})
}

// buildTestRouter constructs a minimal RouterConfig backed by a real in-memory
// SQLite store. Handlers that would require real provider credentials (admin
// key management, model listing) are wired with no-op stubs.
func buildTestRouter(t *testing.T) http.Handler {
	t.Helper()
	return buildTestRouterWithStore(t, testutil.NewTestStore(t))
}

// noopConnectionTester satisfies admin.ConnectionTester without making network calls.
func noopConnectionTester(_ context.Context, _, _ string) (bool, error) {
	return false, nil
}

// insertUserWithSession creates a user with the given role in the store,
// creates an active session for them, and returns the raw session token (the
// value to put in the gleipnir_session cookie).
func insertUserWithSession(t *testing.T, store *db.Store, username, role string) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339)
	userID := "user-" + username

	_, err := store.Queries().CreateUser(ctx, db.CreateUserParams{
		ID:           userID,
		Username:     username,
		PasswordHash: "x",
		CreatedAt:    now,
	})
	if err != nil {
		t.Fatalf("CreateUser %s: %v", username, err)
	}
	if err := store.Queries().AssignRole(ctx, db.AssignRoleParams{
		UserID:    userID,
		Role:      role,
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("AssignRole %s/%s: %v", username, role, err)
	}

	rawToken := "test-token-" + username
	expires := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)
	_, err = store.Queries().CreateSession(ctx, db.CreateSessionParams{
		ID:        "sess-" + username,
		UserID:    userID,
		Token:     auth.HashSessionToken(rawToken),
		CreatedAt: now,
		ExpiresAt: expires,
		UserAgent: "test",
		IpAddress: "127.0.0.1",
	})
	if err != nil {
		t.Fatalf("CreateSession %s: %v", username, err)
	}
	return rawToken
}

// testSSERoute verifies that GET /api/v1/events returns text/event-stream headers.
// The SSE handler blocks until the client disconnects, so we cancel the request
// context immediately after reading the initial headers.
func testSSERoute(t *testing.T, router http.Handler) {
	t.Helper()
	t.Run("SSE endpoint returns text/event-stream", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		req := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil).WithContext(ctx)
		w := httptest.NewRecorder()

		done := make(chan struct{})
		go func() {
			defer close(done)
			router.ServeHTTP(w, req)
		}()

		// Cancel the request immediately so the SSE handler exits.
		cancel()
		<-done

		ct := w.Header().Get("Content-Type")
		if !strings.Contains(ct, "text/event-stream") {
			t.Errorf("Content-Type = %q, want text/event-stream", ct)
		}
	})
}

// TestSecurityHeaders verifies that the SecurityHeaders middleware is wired into
// BuildRouter and fires on real routes — both an API endpoint and the SPA catch-all.
// Exact header values are validated in the unit tests in internal/httputil.
func TestSecurityHeaders(t *testing.T) {
	router := buildTestRouter(t)

	routes := []struct {
		name   string
		method string
		path   string
	}{
		{"API endpoint", http.MethodGet, "/api/v1/health"},
		{"SPA catch-all", http.MethodGet, "/some-frontend-route"},
	}

	securityHeaders := []string{
		"X-Content-Type-Options",
		"X-Frame-Options",
		"Referrer-Policy",
		"Permissions-Policy",
		"Content-Security-Policy",
	}

	for _, route := range routes {
		t.Run(route.name, func(t *testing.T) {
			req := httptest.NewRequest(route.method, route.path, nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			for _, header := range securityHeaders {
				if got := w.Header().Get(header); got == "" {
					t.Errorf("route %s: header %q is missing", route.path, header)
				}
			}
		})
	}
}

// TestWebhookSecretEndpointsRoleGating verifies that the two webhook secret
// management endpoints enforce the admin|operator role requirement. These
// endpoints are registered inside the authenticated group with RequireRole, so
// this test exercises the middleware wiring end-to-end, not just the handlers.
func TestWebhookSecretEndpointsRoleGating(t *testing.T) {
	store := testutil.NewTestStore(t)
	router := buildTestRouterWithStore(t, store)

	// Seed one user per role we want to test.
	adminToken := insertUserWithSession(t, store, "alice", "admin")
	operatorToken := insertUserWithSession(t, store, "bob", "operator")
	auditorToken := insertUserWithSession(t, store, "carol", "auditor")
	approverToken := insertUserWithSession(t, store, "dave", "approver")

	type endpointCase struct {
		name   string
		method string
		path   string
		// adminStatus / operatorStatus are what the handler returns when
		// auth passes. The exact code depends on handler logic (e.g. encryption
		// unavailable vs policy not found) — what matters is it's not 403/401.
		adminStatus    int
		operatorStatus int
	}
	endpoints := []endpointCase{
		{
			name:   "rotate",
			method: http.MethodPost,
			path:   "/api/v1/policies/nonexistent/webhook/rotate",
			// RotateWebhookSecret checks policy existence before encryption key,
			// so nonexistent policy → 404.
			adminStatus:    http.StatusNotFound,
			operatorStatus: http.StatusNotFound,
		},
		{
			name:   "secret",
			method: http.MethodGet,
			path:   "/api/v1/policies/nonexistent/webhook/secret",
			// GetWebhookSecret checks encryption key first (before DB lookup),
			// so encryption key unset → 503.
			adminStatus:    http.StatusServiceUnavailable,
			operatorStatus: http.StatusServiceUnavailable,
		},
	}

	for _, ep := range endpoints {
		t.Run(ep.name+"/admin allowed", func(t *testing.T) {
			req := httptest.NewRequest(ep.method, ep.path, nil)
			req.AddCookie(&http.Cookie{Name: "gleipnir_session", Value: adminToken})
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != ep.adminStatus {
				t.Errorf("admin: status = %d, want %d (not a 403/401); body: %s", w.Code, ep.adminStatus, w.Body.String())
			}
		})
		t.Run(ep.name+"/operator allowed", func(t *testing.T) {
			req := httptest.NewRequest(ep.method, ep.path, nil)
			req.AddCookie(&http.Cookie{Name: "gleipnir_session", Value: operatorToken})
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != ep.operatorStatus {
				t.Errorf("operator: status = %d, want %d (not a 403/401); body: %s", w.Code, ep.operatorStatus, w.Body.String())
			}
		})
		t.Run(ep.name+"/auditor forbidden", func(t *testing.T) {
			req := httptest.NewRequest(ep.method, ep.path, nil)
			req.AddCookie(&http.Cookie{Name: "gleipnir_session", Value: auditorToken})
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != http.StatusForbidden {
				t.Errorf("auditor: status = %d, want 403; body: %s", w.Code, w.Body.String())
			}
		})
		t.Run(ep.name+"/approver forbidden", func(t *testing.T) {
			req := httptest.NewRequest(ep.method, ep.path, nil)
			req.AddCookie(&http.Cookie{Name: "gleipnir_session", Value: approverToken})
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != http.StatusForbidden {
				t.Errorf("approver: status = %d, want 403; body: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestBuildRouter(t *testing.T) {
	cases := []struct {
		name            string
		method          string
		path            string
		body            string
		wantStatus      int
		wantNotStatus   int // assert status != this value (used when exact status depends on build state)
		wantContentType string
	}{
		{
			// Health is public: Docker HEALTHCHECK, load balancer probes, and uptime
			// monitors all hit this endpoint without session cookies.
			name:       "health returns 200 without session (public endpoint)",
			method:     http.MethodGet,
			path:       "/api/v1/health",
			wantStatus: http.StatusOK,
		},
		// SSE is tested separately below because it blocks until client disconnects.

		{
			name:       "protected runs endpoint returns 401 without session",
			method:     http.MethodGet,
			path:       "/api/v1/runs",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "webhook endpoint is registered (non-404)",
			method:     http.MethodPost,
			path:       "/api/v1/webhooks/nonexistent-policy",
			body:       "{}",
			wantStatus: http.StatusNotFound, // 404 because policy doesn't exist, not because route doesn't exist
		},
		{
			// SPA catch-all: verify the route is registered (not a 404).
			// With frontend/dist/ built: 200 (index.html served).
			// Without frontend/dist/: 500 ("index.html not found").
			// Either proves the catch-all route exists.
			name:          "SPA catch-all is registered (non-404)",
			method:        http.MethodGet,
			path:          "/some-frontend-route",
			wantNotStatus: http.StatusNotFound,
		},
	}

	router := buildTestRouter(t)
	testSSERoute(t, router)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var bodyReader *strings.Reader
			if tc.body != "" {
				bodyReader = strings.NewReader(tc.body)
			} else {
				bodyReader = strings.NewReader("")
			}

			req := httptest.NewRequest(tc.method, tc.path, bodyReader)
			if tc.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if tc.wantNotStatus != 0 {
				if w.Code == tc.wantNotStatus {
					t.Errorf("status = %d, want anything but %d; body: %s", w.Code, tc.wantNotStatus, w.Body.String())
				}
			} else if w.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d; body: %s", w.Code, tc.wantStatus, w.Body.String())
			}
			if tc.wantContentType != "" {
				ct := w.Header().Get("Content-Type")
				if !strings.Contains(ct, tc.wantContentType) {
					t.Errorf("Content-Type = %q, want it to contain %q", ct, tc.wantContentType)
				}
			}
		})
	}
}
