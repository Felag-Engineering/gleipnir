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
	"github.com/felag-engineering/gleipnir/internal/infra/headervalidate"
	"github.com/felag-engineering/gleipnir/internal/plugin/oauth"
	sdkmanifest "github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
)

// Ensure buildCredHandler exposes store for LoadCredentials in tests.
// The field is private; we access via the DBStore in the test by reconstructing it.
// Actually we need to expose the store — let's use a helper that returns the store too.
func buildCredHandlerWithStore(t *testing.T, q OAuthPluginQuerier, fakeStore *fakeCredQuerier) (*PluginCredentialsHandler, *oauth.DBStore) {
	t.Helper()
	clock := func() time.Time { return time.Unix(1000000, 0) }
	enc := func(p string) (string, error) { return "enc:" + p, nil }
	dec := func(c string) (string, error) {
		if strings.HasPrefix(c, "enc:") {
			return c[4:], nil
		}
		return "", nil
	}
	store := oauth.NewDBStore(fakeStore, enc, dec, fakeStore, clock)
	return NewPluginCredentialsHandler(q, store), store
}

// buildCredHandler builds a PluginCredentialsHandler backed by the given fake
// querier and an in-memory DBStore. enc/dec use the noopEncrypt/noopDecrypt
// helpers defined in plugin_oauth_handler_test.go (same package).
func buildCredHandler(t *testing.T, q OAuthPluginQuerier, fakeStore *fakeCredQuerier) *PluginCredentialsHandler {
	t.Helper()
	clock := func() time.Time { return time.Unix(1000000, 0) }
	enc := func(p string) (string, error) { return "enc:" + p, nil }
	dec := func(c string) (string, error) {
		if strings.HasPrefix(c, "enc:") {
			return c[4:], nil
		}
		return "", nil
	}
	store := oauth.NewDBStore(fakeStore, enc, dec, fakeStore, clock)
	return NewPluginCredentialsHandler(q, store)
}

// fakeCredQuerier satisfies both oauth.OAuthQuerier and pluginstate.Querier for
// the credential handler tests. It stores a single instance in memory.
type fakeCredQuerier struct {
	instance      db.PluginInstance
	casFailTimes  int
	auditEvents   []db.InsertPluginAuditEventParams
	healthUpdates []db.UpdatePluginInstanceHealthParams
}

func (f *fakeCredQuerier) GetPluginInstanceByID(_ context.Context, id string) (db.PluginInstance, error) {
	if f.instance.ID == id {
		return f.instance, nil
	}
	return db.PluginInstance{}, ErrNotFound
}

func (f *fakeCredQuerier) UpdatePluginInstanceCredentials(_ context.Context, arg db.UpdatePluginInstanceCredentialsParams) (int64, error) {
	if f.casFailTimes > 0 {
		f.casFailTimes--
		return 0, nil
	}
	f.instance.CredentialsEncrypted = arg.CredentialsEncrypted
	f.instance.CredentialsExpiresAt = arg.CredentialsExpiresAt
	f.instance.Version++
	return 1, nil
}

func (f *fakeCredQuerier) UpdatePluginInstanceHealth(_ context.Context, arg db.UpdatePluginInstanceHealthParams) (int64, error) {
	f.healthUpdates = append(f.healthUpdates, arg)
	f.instance.HealthState = arg.HealthState
	f.instance.Version++
	return 1, nil
}

func (f *fakeCredQuerier) InsertPluginAuditEvent(_ context.Context, arg db.InsertPluginAuditEventParams) (db.PluginAuditEvent, error) {
	f.auditEvents = append(f.auditEvents, arg)
	return db.PluginAuditEvent{}, nil
}

func (f *fakeCredQuerier) ListPluginInstancesWithExpiringCredentials(_ context.Context, _ *string) ([]db.PluginInstance, error) {
	return nil, nil
}

func (f *fakeCredQuerier) UpdatePluginInstanceOAuthCallback(_ context.Context, _ db.UpdatePluginInstanceOAuthCallbackParams) (int64, error) {
	return 1, nil
}

// seedCredentials writes initial credentials into the fake store through the
// DBStore so subsequent handler calls can load them.
func seedCredentials(t *testing.T, store *fakeCredQuerier, creds oauth.StoredCredentials) {
	t.Helper()
	plain, err := creds.Marshal()
	if err != nil {
		t.Fatalf("seed marshal: %v", err)
	}
	enc := "enc:" + plain
	store.instance.CredentialsEncrypted = &enc
}

// --- helpers ---

// newCredRequest builds an http.Request with chi URL params pre-set.
func newCredRequest(method, path string, body any, params map[string]string) *http.Request {
	var bodyReader *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(b)
	} else {
		bodyReader = bytes.NewReader(nil)
	}
	r := httptest.NewRequest(method, path, bodyReader)
	if len(params) > 0 {
		r = withChiParams(r, params)
	}
	return r
}

// buildQuerierWithManifest builds an OAuthPluginQuerier (fakeOAuthPluginQuerier)
// holding one instance and one plugin for the given auth strategy.
func buildQuerierWithManifest(t *testing.T, strategy string) *fakeOAuthPluginQuerier {
	t.Helper()
	pq := newFakeOAuthPluginQuerier()
	pq.instances["inst-1"] = db.PluginInstance{
		ID:          "inst-1",
		PluginID:    "plugin-1",
		HealthState: "healthy",
	}
	pq.plugins["plugin-1"] = db.Plugin{
		ID:               "plugin-1",
		ManifestSnapshot: buildTestManifest(t, strategy),
	}
	return pq
}

// --- GET tests ---

func TestPluginCredentialsHandler_Get_ReturnsRedacted(t *testing.T) {
	pq := buildQuerierWithManifest(t, sdkmanifest.AuthStrategyStaticAPIKey)
	sq := &fakeCredQuerier{
		instance: db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", HealthState: "healthy"},
	}
	seedCredentials(t, sq, oauth.StoredCredentials{
		Strategy: sdkmanifest.AuthStrategyStaticAPIKey,
		StaticAPIKey: &oauth.StaticAPIKeyCreds{
			HeaderName: "X-API-Key",
			Scheme:     "Bearer",
			APIKey:     "secret-value",
		},
	})

	h := buildCredHandler(t, pq, sq)
	r := newCredRequest(http.MethodGet, "/credentials", nil, map[string]string{"id": "plugin-1", "iid": "inst-1"})
	w := httptest.NewRecorder()
	h.Get(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	// Secret values must NOT appear in the response.
	for _, secret := range []string{"secret-value", `"api_key"`} {
		if strings.Contains(body, secret) {
			t.Errorf("GET leaked %q in response: %s", secret, body)
		}
	}
	// has_api_key should be true.
	var envelope struct {
		Data oauth.RedactedCredentials `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !envelope.Data.HasAPIKey {
		t.Error("expected has_api_key=true")
	}
	if envelope.Data.HeaderName != "X-API-Key" {
		t.Errorf("HeaderName: got %q, want X-API-Key", envelope.Data.HeaderName)
	}
}

func TestPluginCredentialsHandler_Get_UnknownInstance_404(t *testing.T) {
	// pq has no instance with ID "inst-missing" → resolveInstance returns 404.
	pq := newFakeOAuthPluginQuerier()
	pq.plugins["plugin-1"] = db.Plugin{
		ID:               "plugin-1",
		ManifestSnapshot: buildTestManifest(t, sdkmanifest.AuthStrategyStaticAPIKey),
	}
	// No instance added to pq.
	sq := &fakeCredQuerier{
		instance: db.PluginInstance{ID: "inst-1", PluginID: "plugin-1"},
	}

	h := buildCredHandler(t, pq, sq)
	r := newCredRequest(http.MethodGet, "/credentials", nil, map[string]string{"id": "plugin-1", "iid": "inst-missing"})
	w := httptest.NewRecorder()
	h.Get(w, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestPluginCredentialsHandler_Get_PluginIDMismatch_404(t *testing.T) {
	// pq returns inst-1 but with PluginID = "plugin-other".
	pq := newFakeOAuthPluginQuerier()
	pq.instances["inst-1"] = db.PluginInstance{
		ID:          "inst-1",
		PluginID:    "plugin-other", // does not match URL param "plugin-1"
		HealthState: "healthy",
	}
	pq.plugins["plugin-1"] = db.Plugin{
		ID:               "plugin-1",
		ManifestSnapshot: buildTestManifest(t, sdkmanifest.AuthStrategyStaticAPIKey),
	}
	sq := &fakeCredQuerier{
		instance: db.PluginInstance{ID: "inst-1", PluginID: "plugin-other"},
	}

	h := buildCredHandler(t, pq, sq)
	r := newCredRequest(http.MethodGet, "/credentials", nil, map[string]string{"id": "plugin-1", "iid": "inst-1"})
	w := httptest.NewRecorder()
	h.Get(w, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// --- DELETE tests ---

func TestPluginCredentialsHandler_Delete_ClearsCredentials(t *testing.T) {
	pq := buildQuerierWithManifest(t, sdkmanifest.AuthStrategyStaticAPIKey)
	sq := &fakeCredQuerier{
		instance: db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", HealthState: "healthy"},
	}
	seedCredentials(t, sq, oauth.StoredCredentials{
		Strategy:     sdkmanifest.AuthStrategyStaticAPIKey,
		StaticAPIKey: &oauth.StaticAPIKeyCreds{HeaderName: "X-Key", APIKey: "secret"},
	})

	h := buildCredHandler(t, pq, sq)
	r := newCredRequest(http.MethodDelete, "/credentials", nil, map[string]string{"id": "plugin-1", "iid": "inst-1"})
	w := httptest.NewRecorder()
	h.Delete(w, r)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d: %s", w.Code, w.Body.String())
	}
}

// --- SetStaticAPIKey tests ---

func TestPluginCredentialsHandler_SetStaticAPIKey_HappyPath(t *testing.T) {
	pq := buildQuerierWithManifest(t, sdkmanifest.AuthStrategyStaticAPIKey)
	sq := &fakeCredQuerier{
		instance: db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", HealthState: "healthy"},
	}
	seedCredentials(t, sq, oauth.StoredCredentials{Strategy: sdkmanifest.AuthStrategyStaticAPIKey})

	h := buildCredHandler(t, pq, sq)
	body := map[string]string{"header_name": "X-API-Key", "scheme": "Bearer", "api_key": "sk-123"}
	r := newCredRequest(http.MethodPut, "/credentials/static-api-key", body, map[string]string{"id": "plugin-1", "iid": "inst-1"})
	w := httptest.NewRecorder()
	h.SetStaticAPIKey(w, r)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPluginCredentialsHandler_SetStaticAPIKey_WrongStrategy_400(t *testing.T) {
	// Manifest declares header_set; endpoint requires static_api_key → 400.
	pq := buildQuerierWithManifest(t, sdkmanifest.AuthStrategyHeaderSet)
	sq := &fakeCredQuerier{
		instance: db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", HealthState: "healthy"},
	}

	h := buildCredHandler(t, pq, sq)
	body := map[string]string{"header_name": "X-Key", "api_key": "val"}
	r := newCredRequest(http.MethodPut, "/credentials/static-api-key", body, map[string]string{"id": "plugin-1", "iid": "inst-1"})
	w := httptest.NewRecorder()
	h.SetStaticAPIKey(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for wrong strategy, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPluginCredentialsHandler_SetStaticAPIKey_EmptyHeaderName_400(t *testing.T) {
	pq := buildQuerierWithManifest(t, sdkmanifest.AuthStrategyStaticAPIKey)
	sq := &fakeCredQuerier{
		instance: db.PluginInstance{ID: "inst-1", PluginID: "plugin-1"},
	}

	h := buildCredHandler(t, pq, sq)
	body := map[string]string{"header_name": "", "api_key": "val"}
	r := newCredRequest(http.MethodPut, "/credentials/static-api-key", body, map[string]string{"id": "plugin-1", "iid": "inst-1"})
	w := httptest.NewRecorder()
	h.SetStaticAPIKey(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty header_name, got %d", w.Code)
	}
}

// --- SetHeader (header_set) tests ---

func TestPluginCredentialsHandler_SetHeader_HappyPath(t *testing.T) {
	pq := buildQuerierWithManifest(t, sdkmanifest.AuthStrategyHeaderSet)
	sq := &fakeCredQuerier{
		instance: db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", HealthState: "healthy"},
	}
	seedCredentials(t, sq, oauth.StoredCredentials{
		Strategy:  sdkmanifest.AuthStrategyHeaderSet,
		HeaderSet: &oauth.HeaderSetCreds{Headers: []oauth.NamedHeader{}},
	})

	h := buildCredHandler(t, pq, sq)
	body := map[string]string{"value": "org-123"}
	params := map[string]string{"id": "plugin-1", "iid": "inst-1", "name": "X-Org-ID"}
	r := newCredRequest(http.MethodPut, "/credentials/headers/X-Org-ID", body, params)
	w := httptest.NewRecorder()
	h.SetHeader(w, r)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPluginCredentialsHandler_SetHeader_ReservedName_400(t *testing.T) {
	pq := buildQuerierWithManifest(t, sdkmanifest.AuthStrategyHeaderSet)
	sq := &fakeCredQuerier{
		instance: db.PluginInstance{ID: "inst-1", PluginID: "plugin-1"},
	}

	h := buildCredHandler(t, pq, sq)
	for _, reserved := range headervalidate.ReservedHeaderNames {
		body := map[string]string{"value": "v"}
		params := map[string]string{"id": "plugin-1", "iid": "inst-1", "name": reserved}
		r := newCredRequest(http.MethodPut, "/credentials/headers/"+reserved, body, params)
		w := httptest.NewRecorder()
		h.SetHeader(w, r)
		if w.Code != http.StatusBadRequest {
			t.Errorf("SetHeader(%q): expected 400, got %d", reserved, w.Code)
		}
	}
}

func TestPluginCredentialsHandler_SetHeader_WrongStrategy_400(t *testing.T) {
	pq := buildQuerierWithManifest(t, sdkmanifest.AuthStrategyStaticAPIKey)
	sq := &fakeCredQuerier{
		instance: db.PluginInstance{ID: "inst-1", PluginID: "plugin-1"},
	}

	h := buildCredHandler(t, pq, sq)
	body := map[string]string{"value": "v"}
	params := map[string]string{"id": "plugin-1", "iid": "inst-1", "name": "X-Custom"}
	r := newCredRequest(http.MethodPut, "/credentials/headers/X-Custom", body, params)
	w := httptest.NewRecorder()
	h.SetHeader(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for wrong strategy, got %d", w.Code)
	}
}

// --- DeleteHeader tests ---

func TestPluginCredentialsHandler_DeleteHeader_IdempotentMissing(t *testing.T) {
	pq := buildQuerierWithManifest(t, sdkmanifest.AuthStrategyHeaderSet)
	sq := &fakeCredQuerier{
		instance: db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", HealthState: "healthy"},
	}
	seedCredentials(t, sq, oauth.StoredCredentials{
		Strategy:  sdkmanifest.AuthStrategyHeaderSet,
		HeaderSet: &oauth.HeaderSetCreds{Headers: []oauth.NamedHeader{}},
	})

	h := buildCredHandler(t, pq, sq)
	params := map[string]string{"id": "plugin-1", "iid": "inst-1", "name": "X-NonExistent"}
	r := newCredRequest(http.MethodDelete, "/credentials/headers/X-NonExistent", nil, params)
	w := httptest.NewRecorder()
	h.DeleteHeader(w, r)

	// Idempotent: no 404 when header is absent.
	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204 for idempotent delete, got %d: %s", w.Code, w.Body.String())
	}
}

// --- SetBasicAuth tests ---

func TestPluginCredentialsHandler_SetBasicAuth_HappyPath(t *testing.T) {
	pq := buildQuerierWithManifest(t, sdkmanifest.AuthStrategyBasicAuth)
	sq := &fakeCredQuerier{
		instance: db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", HealthState: "healthy"},
	}
	seedCredentials(t, sq, oauth.StoredCredentials{Strategy: sdkmanifest.AuthStrategyBasicAuth})

	h := buildCredHandler(t, pq, sq)
	body := map[string]string{"username": "alice", "password": "s3cr3t"}
	r := newCredRequest(http.MethodPut, "/credentials/basic-auth", body, map[string]string{"id": "plugin-1", "iid": "inst-1"})
	w := httptest.NewRecorder()
	h.SetBasicAuth(w, r)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPluginCredentialsHandler_SetBasicAuth_WrongStrategy_400(t *testing.T) {
	pq := buildQuerierWithManifest(t, sdkmanifest.AuthStrategyStaticAPIKey)
	sq := &fakeCredQuerier{
		instance: db.PluginInstance{ID: "inst-1", PluginID: "plugin-1"},
	}

	h := buildCredHandler(t, pq, sq)
	body := map[string]string{"username": "alice", "password": "pw"}
	r := newCredRequest(http.MethodPut, "/credentials/basic-auth", body, map[string]string{"id": "plugin-1", "iid": "inst-1"})
	w := httptest.NewRecorder()
	h.SetBasicAuth(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for wrong strategy, got %d", w.Code)
	}
}

func TestPluginCredentialsHandler_SetBasicAuth_MissingFields_400(t *testing.T) {
	pq := buildQuerierWithManifest(t, sdkmanifest.AuthStrategyBasicAuth)
	sq := &fakeCredQuerier{
		instance: db.PluginInstance{ID: "inst-1", PluginID: "plugin-1"},
	}

	tests := []struct {
		name string
		body map[string]string
	}{
		{"missing username", map[string]string{"username": "", "password": "pw"}},
		{"missing password", map[string]string{"username": "alice", "password": ""}},
	}

	h := buildCredHandler(t, pq, sq)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newCredRequest(http.MethodPut, "/credentials/basic-auth", tt.body, map[string]string{"id": "plugin-1", "iid": "inst-1"})
			w := httptest.NewRecorder()
			h.SetBasicAuth(w, r)
			if w.Code != http.StatusBadRequest {
				t.Errorf("expected 400, got %d", w.Code)
			}
		})
	}
}

// --- SetOAuthToken tests ---

func TestPluginCredentialsHandler_SetOAuthToken_HappyPath_Authcode(t *testing.T) {
	pq := buildQuerierWithManifest(t, sdkmanifest.AuthStrategyOAuth2Authcode)
	sq := &fakeCredQuerier{
		instance: db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", HealthState: "healthy"},
	}

	h, store := buildCredHandlerWithStore(t, pq, sq)
	body := map[string]any{
		"access_token":  "xoxb-access",
		"refresh_token": "xoxb-refresh",
		"expires_at":    "2030-01-01T00:00:00Z",
	}
	r := newCredRequest(http.MethodPut, "/credentials/oauth-token", body, map[string]string{"id": "plugin-1", "iid": "inst-1"})
	w := httptest.NewRecorder()
	h.SetOAuthToken(w, r)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d: %s", w.Code, w.Body.String())
	}

	// Verify credentials were stored.
	loaded, _, err := store.LoadCredentials(context.Background(), "inst-1")
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if loaded.Strategy != sdkmanifest.AuthStrategyOAuth2Authcode {
		t.Errorf("Strategy = %q, want %q", loaded.Strategy, sdkmanifest.AuthStrategyOAuth2Authcode)
	}
	if loaded.Token == nil || loaded.Token.AccessToken != "xoxb-access" {
		t.Errorf("Token.AccessToken = %v, want xoxb-access", loaded.Token)
	}
}

func TestPluginCredentialsHandler_SetOAuthToken_HappyPath_Clientcred(t *testing.T) {
	pq := buildQuerierWithManifest(t, sdkmanifest.AuthStrategyOAuth2Clientcred)
	sq := &fakeCredQuerier{
		instance: db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", HealthState: "healthy"},
	}

	h := buildCredHandler(t, pq, sq)
	body := map[string]any{
		"access_token": "cc-token",
	}
	r := newCredRequest(http.MethodPut, "/credentials/oauth-token", body, map[string]string{"id": "plugin-1", "iid": "inst-1"})
	w := httptest.NewRecorder()
	h.SetOAuthToken(w, r)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPluginCredentialsHandler_SetOAuthToken_WrongStrategy_400(t *testing.T) {
	// Manifest declares static_api_key — not an OAuth strategy.
	pq := buildQuerierWithManifest(t, sdkmanifest.AuthStrategyStaticAPIKey)
	sq := &fakeCredQuerier{
		instance: db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", HealthState: "healthy"},
	}

	h := buildCredHandler(t, pq, sq)
	body := map[string]any{"access_token": "tok"}
	r := newCredRequest(http.MethodPut, "/credentials/oauth-token", body, map[string]string{"id": "plugin-1", "iid": "inst-1"})
	w := httptest.NewRecorder()
	h.SetOAuthToken(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for wrong strategy, got %d: %s", w.Code, w.Body.String())
	}
	// Verify the error mentions both permitted OAuth strategies.
	respBody := w.Body.String()
	if !strings.Contains(respBody, "requires one of") {
		t.Errorf("error message should mention 'requires one of': %s", respBody)
	}
}

func TestPluginCredentialsHandler_SetOAuthToken_EmptyAccessToken_400(t *testing.T) {
	pq := buildQuerierWithManifest(t, sdkmanifest.AuthStrategyOAuth2Authcode)
	sq := &fakeCredQuerier{
		instance: db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", HealthState: "healthy"},
	}

	h := buildCredHandler(t, pq, sq)
	body := map[string]any{"access_token": ""}
	r := newCredRequest(http.MethodPut, "/credentials/oauth-token", body, map[string]string{"id": "plugin-1", "iid": "inst-1"})
	w := httptest.NewRecorder()
	h.SetOAuthToken(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty access_token, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPluginCredentialsHandler_SetOAuthToken_BadExpiresAt_400(t *testing.T) {
	pq := buildQuerierWithManifest(t, sdkmanifest.AuthStrategyOAuth2Authcode)
	sq := &fakeCredQuerier{
		instance: db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", HealthState: "healthy"},
	}

	h := buildCredHandler(t, pq, sq)
	body := map[string]any{"access_token": "tok", "expires_at": "not-a-date"}
	r := newCredRequest(http.MethodPut, "/credentials/oauth-token", body, map[string]string{"id": "plugin-1", "iid": "inst-1"})
	w := httptest.NewRecorder()
	h.SetOAuthToken(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for bad expires_at, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPluginCredentialsHandler_SetOAuthToken_InstanceNotFound_404(t *testing.T) {
	pq := newFakeOAuthPluginQuerier()
	sq := &fakeCredQuerier{
		instance: db.PluginInstance{ID: "inst-1"},
	}

	h := buildCredHandler(t, pq, sq)
	body := map[string]any{"access_token": "tok"}
	r := newCredRequest(http.MethodPut, "/credentials/oauth-token", body, map[string]string{"id": "plugin-1", "iid": "inst-missing"})
	w := httptest.NewRecorder()
	h.SetOAuthToken(w, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for missing instance, got %d", w.Code)
	}
}

func TestPluginCredentialsHandler_SetOAuthToken_PluginIDMismatch_404(t *testing.T) {
	pq := newFakeOAuthPluginQuerier()
	pq.instances["inst-1"] = db.PluginInstance{
		ID:          "inst-1",
		PluginID:    "plugin-other",
		HealthState: "healthy",
	}
	pq.plugins["plugin-1"] = db.Plugin{
		ID:               "plugin-1",
		ManifestSnapshot: buildTestManifest(t, sdkmanifest.AuthStrategyOAuth2Authcode),
	}
	sq := &fakeCredQuerier{
		instance: db.PluginInstance{ID: "inst-1", PluginID: "plugin-other"},
	}

	h := buildCredHandler(t, pq, sq)
	body := map[string]any{"access_token": "tok"}
	r := newCredRequest(http.MethodPut, "/credentials/oauth-token", body, map[string]string{"id": "plugin-1", "iid": "inst-1"})
	w := httptest.NewRecorder()
	h.SetOAuthToken(w, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for plugin ID mismatch, got %d", w.Code)
	}
}

// --- Write-only assertion: GET must never expose raw secrets ---

func TestPluginCredentialsHandler_Get_NoRawSecretFields(t *testing.T) {
	tests := []struct {
		name   string
		creds  oauth.StoredCredentials
		fields []string // JSON field names that must NOT appear
	}{
		{
			name: "static_api_key",
			creds: oauth.StoredCredentials{
				Strategy:     sdkmanifest.AuthStrategyStaticAPIKey,
				StaticAPIKey: &oauth.StaticAPIKeyCreds{HeaderName: "X-Key", APIKey: "secret-api-key"},
			},
			// The raw secret value must not appear; "api_key" is deliberately
			// absent from this list because "has_api_key" is a legitimate
			// presence-flag field that contains the substring.
			fields: []string{"secret-api-key"},
		},
		{
			name: "header_set",
			creds: oauth.StoredCredentials{
				Strategy: sdkmanifest.AuthStrategyHeaderSet,
				HeaderSet: &oauth.HeaderSetCreds{
					Headers: []oauth.NamedHeader{{Name: "X-A", Value: "hdr-secret"}},
				},
			},
			fields: []string{"hdr-secret"},
		},
		{
			name: "basic_auth",
			creds: oauth.StoredCredentials{
				Strategy:  sdkmanifest.AuthStrategyBasicAuth,
				BasicAuth: &oauth.BasicAuthCreds{Username: "alice", Password: "pw-secret"},
			},
			fields: []string{"pw-secret"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pq := buildQuerierWithManifest(t, tt.creds.Strategy)
			sq := &fakeCredQuerier{
				instance: db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", HealthState: "healthy"},
			}
			seedCredentials(t, sq, tt.creds)

			h := buildCredHandler(t, pq, sq)
			r := newCredRequest(http.MethodGet, "/credentials", nil, map[string]string{"id": "plugin-1", "iid": "inst-1"})
			w := httptest.NewRecorder()
			h.Get(w, r)

			body := w.Body.String()
			for _, forbidden := range tt.fields {
				if strings.Contains(body, forbidden) {
					t.Errorf("GET response contains forbidden field/value %q: %s", forbidden, body)
				}
			}
		})
	}
}
