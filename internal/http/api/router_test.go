package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/admin"
	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/execution/run"
	"github.com/felag-engineering/gleipnir/internal/http/api"
	"github.com/felag-engineering/gleipnir/internal/http/auth"
	"github.com/felag-engineering/gleipnir/internal/http/sse"
	"github.com/felag-engineering/gleipnir/internal/llm"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/policy"
	"github.com/felag-engineering/gleipnir/internal/settings"
	"github.com/felag-engineering/gleipnir/internal/testutil"
	"github.com/felag-engineering/gleipnir/internal/trigger"
)

// Compile-time checks: the stub must satisfy the interface, and the real
// *admin.Handler must still satisfy it (guards against future signature drift).
var _ api.AdminHandler = (*stubAdminHandler)(nil)
var _ api.AdminHandler = (*admin.Handler)(nil)

// stubAdminHandler implements api.AdminHandler with a sentinel response so
// tests can verify that BuildRouter dispatches to the injected implementation
// rather than requiring a real *admin.Handler.
type stubAdminHandler struct{}

const stubSentinelBody = `{"stub":"admin"}`
const stubSentinelStatus = http.StatusTeapot

func (s *stubAdminHandler) respond(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(stubSentinelStatus)
	_, _ = w.Write([]byte(stubSentinelBody))
}

func (s *stubAdminHandler) GetPublicConfig(w http.ResponseWriter, r *http.Request) { s.respond(w) }
func (s *stubAdminHandler) ListProviders(w http.ResponseWriter, r *http.Request)   { s.respond(w) }
func (s *stubAdminHandler) SetProviderKey(w http.ResponseWriter, r *http.Request)  { s.respond(w) }
func (s *stubAdminHandler) DeleteProviderKey(w http.ResponseWriter, r *http.Request) {
	s.respond(w)
}
func (s *stubAdminHandler) GetSettings(w http.ResponseWriter, r *http.Request)    { s.respond(w) }
func (s *stubAdminHandler) UpdateSettings(w http.ResponseWriter, r *http.Request) { s.respond(w) }
func (s *stubAdminHandler) SetDefaultModel(w http.ResponseWriter, r *http.Request) {
	s.respond(w)
}
func (s *stubAdminHandler) ListModelsAdmin(w http.ResponseWriter, r *http.Request) { s.respond(w) }
func (s *stubAdminHandler) ListAllModels(lister llm.ModelLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { s.respond(w) }
}
func (s *stubAdminHandler) SetModelEnabled(w http.ResponseWriter, r *http.Request) { s.respond(w) }

// TestBuildRouter_AdminHandlerStub verifies that BuildRouter wires the injected
// AdminHandler into routes rather than requiring a concrete *admin.Handler. It
// builds the router with a stubAdminHandler and asserts the stub's sentinel
// response is returned for the public-config route (all authenticated users)
// and the admin-gated settings route (admin role required).
func TestBuildRouter_AdminHandlerStub(t *testing.T) {
	store := testutil.NewTestStore(t)
	stub := &stubAdminHandler{}
	router := buildTestRouterWithStoreAndAdmin(t, store, stub)

	adminToken := insertUserWithSession(t, store, "stub-admin", "admin")

	t.Run("GET /api/v1/config dispatches to stub", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/config", nil)
		req.AddCookie(&http.Cookie{Name: "gleipnir_session", Value: adminToken})
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != stubSentinelStatus {
			t.Errorf("status = %d, want %d (stub sentinel); body: %s", w.Code, stubSentinelStatus, w.Body.String())
		}
		if got := w.Body.String(); got != stubSentinelBody {
			t.Errorf("body = %q, want %q", got, stubSentinelBody)
		}
	})

	t.Run("GET /api/v1/admin/settings dispatches to stub", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings", nil)
		req.AddCookie(&http.Cookie{Name: "gleipnir_session", Value: adminToken})
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != stubSentinelStatus {
			t.Errorf("status = %d, want %d (stub sentinel); body: %s", w.Code, stubSentinelStatus, w.Body.String())
		}
		if got := w.Body.String(); got != stubSentinelBody {
			t.Errorf("body = %q, want %q", got, stubSentinelBody)
		}
	})
}

// buildTestRouterWithStoreAndAdmin is the shared core: it lets callers inject
// the store (so they can seed rows before the router is built) and the
// AdminHandler implementation (so tests can substitute a stub).
func buildTestRouterWithStoreAndAdmin(t *testing.T, store *db.Store, adminHandler api.AdminHandler) http.Handler {
	t.Helper()
	return buildTestRouterWithAdminAndSubscribedValidator(t, store, adminHandler, nil)
}

// buildTestRouterWithSubscribedValidator builds the standard test router with an
// ADR-048 binding validator attached, so a test can exercise the save path the
// way production wires it (#870).
func buildTestRouterWithSubscribedValidator(t *testing.T, store *db.Store, v *policy.SubscribedBindingValidator) http.Handler {
	t.Helper()
	adminQuerier := admin.NewQuerierAdapter(store.Queries())
	systemSettings := settings.NewService(store.Queries())
	adminHandler := admin.NewHandler(adminQuerier, systemSettings, nil, []string{"anthropic"}, nil, nil, nil)
	return buildTestRouterWithAdminAndSubscribedValidator(t, store, adminHandler, v)
}

func buildTestRouterWithAdminAndSubscribedValidator(t *testing.T, store *db.Store, adminHandler api.AdminHandler, subscribedValidator *policy.SubscribedBindingValidator) http.Handler {
	t.Helper()

	broadcaster := sse.NewBroadcaster()
	sseHandler := sse.NewHandler(broadcaster)

	registry := mcp.NewRegistry(store.Queries())
	runManager := run.NewRunManager()

	noopClient := testutil.NewNoopLLMClient()
	providerRegistry := llm.NewProviderRegistry()
	providerRegistry.Register("anthropic", noopClient)

	systemSettings := settings.NewService(store.Queries())

	launcher := run.NewRunLauncher(run.RunLauncherConfig{
		Store:                  store,
		Resolver:               run.NewDefaultToolResolver(registry, nil, nil),
		Manager:                runManager,
		AgentFactory:           run.NewAgentFactory(providerRegistry),
		Publisher:              broadcaster,
		DefaultFeedbackTimeout: 30 * time.Minute,
		ModelResolver:          systemSettings,
	})
	webhookHandler := trigger.NewWebhookHandler(store, launcher, trigger.NewSecretLoader(store.Queries(), nil), systemSettings)
	openaiCompatHandler := admin.NewOpenAICompatHandler(nil, nil, providerRegistry, noopConnectionTester)

	authHandler := auth.NewHandler(store.Queries(), store.DB())
	settingsHandler := auth.NewSettingsHandler(store.Queries())

	policyService := policy.NewService(store, nil, providerRegistry, providerRegistry, systemSettings)
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
			Settings:         systemSettings,
			// Nil for most tests, which is the historical default. Tests that
			// care about ADR-048 binding validation on save pass one in.
			SubscribedValidator: subscribedValidator,
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

// buildTestRouterWithStore builds the router with the real *admin.Handler backed
// by the given store. Callers that need a different AdminHandler implementation
// should use buildTestRouterWithStoreAndAdmin directly.
func buildTestRouterWithStore(t *testing.T, store *db.Store) http.Handler {
	t.Helper()
	adminQuerier := admin.NewQuerierAdapter(store.Queries())
	systemSettings := settings.NewService(store.Queries())
	adminHandler := admin.NewHandler(adminQuerier, systemSettings, nil, []string{"anthropic"}, nil, nil, nil)
	return buildTestRouterWithStoreAndAdmin(t, store, adminHandler)
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
func testSSERoute(t *testing.T) {
	t.Helper()
	store := testutil.NewTestStore(t)
	router := buildTestRouterWithStore(t, store)

	// Without a session the stream must be rejected — it discloses live run,
	// approval, and feedback activity to whoever connects (#486).
	t.Run("SSE endpoint requires a session", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("unauthenticated SSE: status = %d, want %d; body: %s", w.Code, http.StatusUnauthorized, w.Body.String())
		}
	})

	// With a valid session the stream is served. Use a real server + client so we
	// read the response headers exactly like an SSE client would: Client.Do
	// returns once the handler flushes its initial headers (after auth passes),
	// and we cancel to unblock the still-streaming handler. An in-process recorder
	// with an immediate cancel would race the auth DB lookup and spuriously 401.
	t.Run("SSE endpoint returns text/event-stream for an authenticated session", func(t *testing.T) {
		token := insertUserWithSession(t, store, "sse-user", "auditor")
		srv := httptest.NewServer(router)
		defer srv.Close()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/events", nil)
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		req.AddCookie(&http.Cookie{Name: "gleipnir_session", Value: token})

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET /api/v1/events: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("authenticated SSE: status = %d, want %d", resp.StatusCode, http.StatusOK)
		}
		ct := resp.Header.Get("Content-Type")
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

// TestPluginEndpointsRoleGating verifies that the two new plugin endpoints
// (install + create-instance) require an active session and the admin role.
func TestPluginEndpointsRoleGating(t *testing.T) {
	store := testutil.NewTestStore(t)
	router := buildTestRouterWithStore(t, store)

	adminToken := insertUserWithSession(t, store, "plugin-admin", "admin")
	operatorToken := insertUserWithSession(t, store, "plugin-operator", "operator")

	endpoints := []struct {
		name   string
		method string
		path   string
	}{
		{"install", http.MethodPost, "/api/v1/admin/plugins"},
		{"create-instance", http.MethodPost, "/api/v1/admin/plugins/some-id/instances"},
		{"settings-default-model", http.MethodPut, "/api/v1/admin/settings/default-model"},
		{"instance-config", http.MethodPut, "/api/v1/admin/plugins/some-id/instances/some-iid/config"},
		{"oauth-token", http.MethodPut, "/api/v1/admin/plugins/some-id/instances/some-iid/credentials/oauth-token"},
	}

	for _, ep := range endpoints {
		t.Run(ep.name+"/unauthenticated returns 401", func(t *testing.T) {
			req := httptest.NewRequest(ep.method, ep.path, strings.NewReader("{}"))
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401; body: %s", w.Code, w.Body.String())
			}
		})
		t.Run(ep.name+"/operator returns 403", func(t *testing.T) {
			req := httptest.NewRequest(ep.method, ep.path, strings.NewReader("{}"))
			req.AddCookie(&http.Cookie{Name: "gleipnir_session", Value: operatorToken})
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403; body: %s", w.Code, w.Body.String())
			}
		})
		t.Run(ep.name+"/admin passes auth gate", func(t *testing.T) {
			req := httptest.NewRequest(ep.method, ep.path, strings.NewReader("{}"))
			req.AddCookie(&http.Cookie{Name: "gleipnir_session", Value: adminToken})
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			// PluginAdminHandler is nil in the test router, so the route is not
			// registered — the request reaches the SPA catch-all and returns non-401/403.
			// We only assert the auth gate passes (not 401 or 403).
			if w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden {
				t.Errorf("status = %d, want auth to pass (not 401/403); body: %s", w.Code, w.Body.String())
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
	testSSERoute(t)

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

// postLogin fires a login request from the given client IP (via X-Real-IP, which
// middleware.RealIP folds into RemoteAddr for the rate limiter to key on).
//
// The body intentionally omits the password so the handler short-circuits with a
// 400 ("password is required") BEFORE the constant-time dummy-bcrypt branch
// (auth.Login). That branch hashes at cost 12, which under `-race` + parallel
// package load takes seconds per call; ten such calls let the burst straddle a
// wall-clock minute boundary, and httprate's sliding-window count decays below
// the limit so the over-limit request is admitted (was a flaky 401 instead of
// 429). The rate-limit middleware runs ahead of the handler and counts every
// request regardless of status, so a 400 exercises the limiter identically while
// keeping the whole burst inside a single window. (#494 CI flake.)
func postLogin(t *testing.T, router http.Handler, ip string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"nobody"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Real-IP", ip)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w.Code
}

// TestLoginRateLimitedPerIP verifies that /login is rate-limited per client IP:
// a single IP is throttled (429) once it exhausts the window, while a different
// IP is unaffected. Regression guard for #491.
func TestLoginRateLimitedPerIP(t *testing.T) {
	router := buildTestRouter(t)

	const limit = 10 // matches httprate.LimitByIP(10, time.Minute) on /login
	const attackerIP = "198.51.100.10"

	// The first `limit` attempts must not be rate-limited (they get the handler's
	// 400 for the missing password, not 429 — see postLogin for why the password
	// is omitted).
	for i := range limit {
		if code := postLogin(t, router, attackerIP); code == http.StatusTooManyRequests {
			t.Fatalf("attempt %d/%d from %s was rate-limited before the limit was reached", i+1, limit, attackerIP)
		}
	}

	// The next attempt from the same IP is over the limit → 429.
	if code := postLogin(t, router, attackerIP); code != http.StatusTooManyRequests {
		t.Errorf("over-limit attempt from %s: status = %d, want %d", attackerIP, code, http.StatusTooManyRequests)
	}

	// A different IP is keyed separately and must still be allowed through.
	if code := postLogin(t, router, "203.0.113.5"); code == http.StatusTooManyRequests {
		t.Error("a fresh IP was rate-limited; the limit is global, not per-IP")
	}
}
