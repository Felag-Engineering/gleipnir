package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/plugin/oauth"
	sdkmanifest "github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
)

// --- fake OAuthPluginQuerier ---

type fakeOAuthPluginQuerier struct {
	instances map[string]db.PluginInstance
	plugins   map[string]db.Plugin
}

func newFakeOAuthPluginQuerier() *fakeOAuthPluginQuerier {
	return &fakeOAuthPluginQuerier{
		instances: make(map[string]db.PluginInstance),
		plugins:   make(map[string]db.Plugin),
	}
}

func (f *fakeOAuthPluginQuerier) GetPluginInstanceByID(_ context.Context, id string) (db.PluginInstance, error) {
	row, ok := f.instances[id]
	if !ok {
		return db.PluginInstance{}, ErrNotFound
	}
	return row, nil
}

func (f *fakeOAuthPluginQuerier) GetPluginByID(_ context.Context, id string) (db.Plugin, error) {
	row, ok := f.plugins[id]
	if !ok {
		return db.Plugin{}, ErrNotFound
	}
	return row, nil
}

func (f *fakeOAuthPluginQuerier) UpdatePluginInstanceHealth(_ context.Context, _ db.UpdatePluginInstanceHealthParams) (int64, error) {
	return 1, nil
}

// fakeOAuthQuerier from store_test.go also satisfies oauth.OAuthQuerier and
// oauth.HealthSetter, but it is in the same package (admin), so we build a
// minimal one here that satisfies oauth.OAuthQuerier.

type stubOAuthQuerier struct{}

func (s *stubOAuthQuerier) GetPluginInstanceByID(_ context.Context, id string) (db.PluginInstance, error) {
	creds := oauth.StoredCredentials{
		Strategy:     "oauth2_authcode",
		ClientID:     "cid",
		ClientSecret: "csec",
		TokenURL:     "https://provider.example.com/token",
		Scopes:       nil,
	}
	plain, _ := creds.Marshal()
	enc := "enc:" + plain
	return db.PluginInstance{
		ID:                   id,
		PluginID:             "plugin-1",
		HealthState:          "healthy",
		Version:              0,
		CredentialsEncrypted: &enc,
	}, nil
}

func (s *stubOAuthQuerier) UpdatePluginInstanceCredentials(_ context.Context, _ db.UpdatePluginInstanceCredentialsParams) (int64, error) {
	return 1, nil
}

func (s *stubOAuthQuerier) UpdatePluginInstanceHealth(_ context.Context, _ db.UpdatePluginInstanceHealthParams) (int64, error) {
	return 1, nil
}

func (s *stubOAuthQuerier) InsertPluginAuditEvent(_ context.Context, _ db.InsertPluginAuditEventParams) (db.PluginAuditEvent, error) {
	return db.PluginAuditEvent{}, nil
}

func (s *stubOAuthQuerier) ListPluginInstancesWithExpiringCredentials(_ context.Context, _ *string) ([]db.PluginInstance, error) {
	return nil, nil
}

func (s *stubOAuthQuerier) UpdatePluginInstanceOAuthCallback(_ context.Context, _ db.UpdatePluginInstanceOAuthCallbackParams) (int64, error) {
	return 1, nil
}

// --- helpers ---

// buildTestManifest creates a minimal manifest YAML for the given auth strategy.
func buildTestManifest(t *testing.T, strategy string) string {
	t.Helper()
	m := sdkmanifest.Manifest{
		SchemaVersion: "v1",
		Name:          "test-plugin",
		Version:       "1.0.0",
		Services:      sdkmanifest.Services{Tool: "v1"},
		Auth: sdkmanifest.AuthDecl{
			Mode:     "instance_credentials",
			Strategy: strategy,
		},
	}
	if strategy == sdkmanifest.AuthStrategyOAuth2Authcode {
		m.Auth.OAuthDefaults = &sdkmanifest.OAuthDefaultsDecl{
			AuthorizationURL: "https://provider.example.com/authorize",
			TokenURL:         "https://provider.example.com/token",
		}
	}
	raw, err := sdkmanifest.Marshal(&m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	return string(raw)
}

// buildOAuthHandler builds a PluginOAuthHandler with a real Manager backed by
// a stub querier. The public URL and HMAC key are fixed for tests.
func buildOAuthHandler(t *testing.T, q OAuthPluginQuerier, publicURL string) *PluginOAuthHandler {
	t.Helper()
	clock := func() time.Time { return time.Unix(1000000, 0) }

	stubQ := &stubOAuthQuerier{}
	enc := func(p string) (string, error) { return "enc:" + p, nil }
	dec := func(c string) (string, error) {
		if strings.HasPrefix(c, "enc:") {
			return c[4:], nil
		}
		return "", nil
	}
	store := oauth.NewDBStore(stubQ, enc, dec, stubQ, clock)
	nonces := oauth.NewMemoryNonceStore(clock)
	hmacKey := oauth.DeriveHMACKey(make([]byte, 32))
	mgr := oauth.NewManager(store, nonces, clock, hmacKey, func() string { return publicURL })
	return NewPluginOAuthHandler(q, mgr, func() string { return publicURL })
}

func TestPluginOAuthHandler_Begin_NonOAuthStrategy_400(t *testing.T) {
	q := newFakeOAuthPluginQuerier()
	q.instances["inst-1"] = db.PluginInstance{
		ID:          "inst-1",
		PluginID:    "plugin-1",
		HealthState: "healthy",
	}
	q.plugins["plugin-1"] = db.Plugin{
		ID:               "plugin-1",
		ManifestSnapshot: buildTestManifest(t, "none"),
	}

	h := buildOAuthHandler(t, q, "https://gleipnir.example.com")

	body, _ := json.Marshal(map[string]string{"return_url": "https://app.example.com"})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/admin/plugins/plugin-1/instances/inst-1/oauth/begin", bytes.NewReader(body))
	r = withChiParams(r, map[string]string{"id": "plugin-1", "iid": "inst-1"})
	w := httptest.NewRecorder()

	h.Begin(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for non-OAuth strategy, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPluginOAuthHandler_Begin_AuthcodeReturnsAuthorizeURL(t *testing.T) {
	q := newFakeOAuthPluginQuerier()
	q.instances["inst-1"] = db.PluginInstance{
		ID:          "inst-1",
		PluginID:    "plugin-1",
		HealthState: "healthy",
	}
	q.plugins["plugin-1"] = db.Plugin{
		ID:               "plugin-1",
		ManifestSnapshot: buildTestManifest(t, sdkmanifest.AuthStrategyOAuth2Authcode),
	}

	h := buildOAuthHandler(t, q, "https://gleipnir.example.com")

	// Use a same-origin return_url so the new open-redirect validation passes.
	body, _ := json.Marshal(map[string]string{"return_url": "https://gleipnir.example.com/admin/plugins/inst-1"})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/admin/plugins/plugin-1/instances/inst-1/oauth/begin", bytes.NewReader(body))
	r = withChiParams(r, map[string]string{"id": "plugin-1", "iid": "inst-1"})
	w := httptest.NewRecorder()

	h.Begin(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var envelope struct {
		Data beginAuthcodeResponse `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Data.AuthorizeURL == "" {
		t.Error("expected non-empty authorize_url")
	}
}

func TestPluginOAuthHandler_Callback_MissingState_400(t *testing.T) {
	q := newFakeOAuthPluginQuerier()
	h := buildOAuthHandler(t, q, "https://gleipnir.example.com")

	r := httptest.NewRequest(http.MethodGet, "/api/v1/admin/plugins/oauth/callback", nil)
	w := httptest.NewRecorder()

	h.Callback(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestPluginOAuthHandler_Callback_TamperedState_400(t *testing.T) {
	q := newFakeOAuthPluginQuerier()
	h := buildOAuthHandler(t, q, "https://gleipnir.example.com")

	r := httptest.NewRequest(http.MethodGet, "/api/v1/admin/plugins/oauth/callback?state=AAAA&code=someCode", nil)
	w := httptest.NewRecorder()

	h.Callback(w, r)

	// Tampered state should yield 400.
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for tampered state, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPluginOAuthHandler_Begin_EmptyPublicURL_400(t *testing.T) {
	q := newFakeOAuthPluginQuerier()
	q.instances["inst-1"] = db.PluginInstance{
		ID:          "inst-1",
		PluginID:    "plugin-1",
		HealthState: "healthy",
	}
	q.plugins["plugin-1"] = db.Plugin{
		ID:               "plugin-1",
		ManifestSnapshot: buildTestManifest(t, sdkmanifest.AuthStrategyOAuth2Authcode),
	}

	// Build the handler with an empty public_url — BeginAuthcode will return ErrConfigInvalid.
	h := buildOAuthHandler(t, q, "")

	body, _ := json.Marshal(map[string]string{"return_url": "https://app.example.com/settings"})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/admin/plugins/plugin-1/instances/inst-1/oauth/begin", bytes.NewReader(body))
	r = withChiParams(r, map[string]string{"id": "plugin-1", "iid": "inst-1"})
	w := httptest.NewRecorder()

	h.Begin(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty public_url, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "public_url") {
		t.Errorf("expected response body to mention public_url, got: %s", w.Body.String())
	}
}

func TestPluginOAuthHandler_Callback_ProviderError_RedirectsWithError(t *testing.T) {
	// When the provider sends ?error=access_denied, the callback handler should
	// redirect to ReturnURL?oauth_error=access_denied. Since the state is
	// tampered/expired in this test, it falls back to 400 — that is fine because
	// the real flow would have a valid state.
	q := newFakeOAuthPluginQuerier()
	h := buildOAuthHandler(t, q, "https://gleipnir.example.com")

	// Use a real, valid state so the handler can extract ReturnURL.
	clock := func() time.Time { return time.Unix(1000000, 0) }
	hmacKey := oauth.DeriveHMACKey(make([]byte, 32))
	env, nonce, err := oauth.NewStateEnvelope("inst-1", "https://app.example.com/settings", clock)
	if err != nil {
		t.Fatalf("NewStateEnvelope: %v", err)
	}
	_ = nonce
	encoded, err := oauth.EncodeState(env, hmacKey)
	if err != nil {
		t.Fatalf("EncodeState: %v", err)
	}

	target := "/api/v1/admin/plugins/oauth/callback?state=" + encoded + "&error=access_denied"
	r := httptest.NewRequest(http.MethodGet, target, nil)
	w := httptest.NewRecorder()

	h.Callback(w, r)

	// Should redirect (302) to return URL with oauth_error parameter.
	if w.Code != http.StatusFound {
		t.Errorf("expected 302 redirect, got %d: %s", w.Code, w.Body.String())
	}
	location := w.Header().Get("Location")
	if !strings.Contains(location, "oauth_error") {
		t.Errorf("expected oauth_error in redirect location, got: %q", location)
	}
}

// --- validateReturnURL tests (Fix 2: open-redirect hardening) ---

// TestValidateReturnURL covers the full acceptance/rejection matrix for
// return_url validation. Rows with wantErr=false must be accepted; those with
// wantErr=true must be rejected with a non-nil error.
func TestValidateReturnURL(t *testing.T) {
	const publicURL = "https://gleipnir.example.com"

	cases := []struct {
		name      string
		returnURL string
		publicURL string
		wantErr   bool
	}{
		// Relative paths are always accepted.
		{name: "relative path", returnURL: "/admin/plugins/inst-1", wantErr: false, publicURL: publicURL},
		{name: "relative path with query", returnURL: "/admin?tab=oauth", wantErr: false, publicURL: publicURL},
		// Same-origin absolute URLs are accepted.
		{name: "same-origin absolute", returnURL: "https://gleipnir.example.com/admin/plugins", wantErr: false, publicURL: publicURL},
		{name: "same-origin root", returnURL: "https://gleipnir.example.com/", wantErr: false, publicURL: publicURL},
		// Different-origin absolute URLs are rejected.
		{name: "different-origin absolute", returnURL: "https://evil.com/steal", wantErr: true, publicURL: publicURL},
		{name: "different-scheme", returnURL: "http://gleipnir.example.com/admin", wantErr: true, publicURL: publicURL},
		{name: "different host port", returnURL: "https://gleipnir.example.com:9999/admin", wantErr: true, publicURL: publicURL},
		// Protocol-relative URLs must be rejected (browsers treat // as absolute).
		{name: "protocol-relative", returnURL: "//evil.com/steal", wantErr: true, publicURL: publicURL},
		// Malformed / non-URL strings are rejected.
		{name: "bare word", returnURL: "evil.com", wantErr: true, publicURL: publicURL},
		{name: "javascript scheme", returnURL: "javascript:alert(1)", wantErr: true, publicURL: publicURL},
		// When publicURL is empty, only relative paths are accepted.
		{name: "relative path no publicURL", returnURL: "/admin", wantErr: false, publicURL: ""},
		{name: "absolute no publicURL", returnURL: "https://gleipnir.example.com/admin", wantErr: true, publicURL: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateReturnURL(tc.returnURL, tc.publicURL)
			if tc.wantErr && err == nil {
				t.Errorf("validateReturnURL(%q, %q): expected error, got nil", tc.returnURL, tc.publicURL)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("validateReturnURL(%q, %q): unexpected error: %v", tc.returnURL, tc.publicURL, err)
			}
		})
	}
}

// TestPluginOAuthHandler_Begin_InvalidReturnURL_400 exercises the HTTP handler's
// open-redirect guard. Sends a return_url pointing at a different origin and
// expects a 400 response.
func TestPluginOAuthHandler_Begin_InvalidReturnURL_400(t *testing.T) {
	q := newFakeOAuthPluginQuerier()
	q.instances["inst-1"] = db.PluginInstance{
		ID:          "inst-1",
		PluginID:    "plugin-1",
		HealthState: "healthy",
	}
	q.plugins["plugin-1"] = db.Plugin{
		ID:               "plugin-1",
		ManifestSnapshot: buildTestManifest(t, sdkmanifest.AuthStrategyOAuth2Authcode),
	}
	h := buildOAuthHandler(t, q, "https://gleipnir.example.com")

	for _, returnURL := range []string{
		"https://evil.com/steal",
		"//evil.com",
		"javascript:alert(1)",
	} {
		body, _ := json.Marshal(map[string]string{"return_url": returnURL})
		r := httptest.NewRequest(http.MethodPost, "/api/v1/admin/plugins/plugin-1/instances/inst-1/oauth/begin", bytes.NewReader(body))
		r = withChiParams(r, map[string]string{"id": "plugin-1", "iid": "inst-1"})
		w := httptest.NewRecorder()

		h.Begin(w, r)

		if w.Code != http.StatusBadRequest {
			t.Errorf("return_url=%q: expected 400, got %d: %s", returnURL, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "invalid return_url") {
			t.Errorf("return_url=%q: expected body to mention 'invalid return_url', got: %s", returnURL, w.Body.String())
		}
	}
}

// TestPluginOAuthHandler_Begin_RelativeReturnURL_Accepted verifies that a
// relative path return_url passes validation and the flow proceeds normally.
func TestPluginOAuthHandler_Begin_RelativeReturnURL_Accepted(t *testing.T) {
	q := newFakeOAuthPluginQuerier()
	q.instances["inst-1"] = db.PluginInstance{
		ID:          "inst-1",
		PluginID:    "plugin-1",
		HealthState: "healthy",
	}
	q.plugins["plugin-1"] = db.Plugin{
		ID:               "plugin-1",
		ManifestSnapshot: buildTestManifest(t, sdkmanifest.AuthStrategyOAuth2Authcode),
	}
	h := buildOAuthHandler(t, q, "https://gleipnir.example.com")

	body, _ := json.Marshal(map[string]string{"return_url": "/admin/plugins/inst-1"})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/admin/plugins/plugin-1/instances/inst-1/oauth/begin", bytes.NewReader(body))
	r = withChiParams(r, map[string]string{"id": "plugin-1", "iid": "inst-1"})
	w := httptest.NewRecorder()

	h.Begin(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for relative return_url, got %d: %s", w.Code, w.Body.String())
	}
}
