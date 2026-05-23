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
	return NewPluginOAuthHandler(q, mgr)
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

	body, _ := json.Marshal(map[string]string{"return_url": "https://app.example.com/settings"})
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

