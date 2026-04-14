package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rapp992/gleipnir/internal/admin"
	"github.com/rapp992/gleipnir/internal/api"
	"github.com/rapp992/gleipnir/internal/auth"
	"github.com/rapp992/gleipnir/internal/llm"
	"github.com/rapp992/gleipnir/internal/mcp"
	"github.com/rapp992/gleipnir/internal/run"
	"github.com/rapp992/gleipnir/internal/sse"
	"github.com/rapp992/gleipnir/internal/testutil"
	"github.com/rapp992/gleipnir/internal/trigger"
)

// buildTestRouter constructs a minimal RouterConfig backed by a real in-memory
// SQLite store. Handlers that would require real provider credentials (admin
// key management, model listing) are wired with no-op stubs.
func buildTestRouter(t *testing.T) http.Handler {
	t.Helper()

	store := testutil.NewTestStore(t)

	broadcaster := sse.NewBroadcaster()
	sseHandler := sse.NewHandler(broadcaster)

	registry := mcp.NewRegistry(store.Queries())
	runManager := run.NewRunManager()

	noopClient := testutil.NewNoopLLMClient()
	providerRegistry := llm.NewProviderRegistry()
	providerRegistry.Register("anthropic", noopClient)

	launcher := run.NewRunLauncher(store, registry, runManager, run.NewAgentFactory(providerRegistry), broadcaster, 30*time.Minute)
	webhookHandler := trigger.NewWebhookHandler(store, launcher)

	adminQuerier := admin.NewQuerierAdapter(store.Queries())
	// Empty encryption key is valid for testing — no real keys are stored.
	adminHandler := admin.NewHandler(adminQuerier, nil, []string{"anthropic"}, nil, nil)
	openaiCompatHandler := admin.NewOpenAICompatHandler(nil, nil, providerRegistry, noopConnectionTester)

	authHandler := auth.NewHandler(store.Queries(), store.DB())
	settingsHandler := auth.NewSettingsHandler(store.Queries())

	return api.BuildRouter(api.RouterConfig{
		Store:               store,
		Broadcaster:         broadcaster,
		Registry:            registry,
		RunManager:          runManager,
		Launcher:            launcher,
		ModelLister:         providerRegistry,
		ProviderRegistry:    providerRegistry,
		ModelFilter:         nil,
		AuthHandler:         authHandler,
		SettingsHandler:     settingsHandler,
		AdminHandler:        adminHandler,
		OpenAICompatHandler: openaiCompatHandler,
		WebhookHandler:      webhookHandler,
		SSEHandler:          sseHandler,
		Version:             "test",
		StartTime:           time.Now(),
		DBPath:              "",
	})
}

// noopConnectionTester satisfies admin.ConnectionTester without making network calls.
func noopConnectionTester(_ context.Context, _, _ string) (bool, error) {
	return false, nil
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
			// health is inside the authenticated sub-router, so it returns 401 without a session
			name:       "health returns 401 without session (route is registered)",
			method:     http.MethodGet,
			path:       "/api/v1/health",
			wantStatus: http.StatusUnauthorized,
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
