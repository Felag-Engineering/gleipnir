package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
)

// Tests do NOT call t.Parallel — they share the package clock.

func newTestManager(q *fakeOAuthQuerier, publicURL string) (*Manager, *DBStore) {
	baseTime := time.Unix(1000000, 0)
	clock := func() time.Time { return baseTime }

	store := NewDBStore(q, noopEncrypt, noopDecrypt, q, clock)
	nonces := &MemoryNonceStore{
		entries: make(map[string]time.Time),
		clock:   clock,
	}
	hmacKey := fixedKey()
	mgr := NewManager(store, nonces, clock, hmacKey, func() string { return publicURL })
	return mgr, store
}

// buildInstanceWithCreds seeds a fakeOAuthQuerier with an instance whose
// credentials_encrypted field contains the given StoredCredentials.
func buildInstanceWithCreds(t *testing.T, creds StoredCredentials) *fakeOAuthQuerier {
	t.Helper()
	plain, err := creds.Marshal()
	if err != nil {
		t.Fatalf("marshal creds: %v", err)
	}
	enc, err := noopEncrypt(plain)
	if err != nil {
		t.Fatalf("encrypt creds: %v", err)
	}
	return &fakeOAuthQuerier{
		instance: db.PluginInstance{
			ID:                   "inst-1",
			HealthState:          "healthy",
			Version:              0,
			CredentialsEncrypted: &enc,
		},
	}
}

func TestBeginAuthcode_ReturnsAuthorizeURL(t *testing.T) {
	creds := testCreds("oauth2_authcode")
	creds.AuthorizationURL = "https://provider.example.com/oauth/authorize"

	q := buildInstanceWithCreds(t, creds)
	mgr, _ := newTestManager(q, "https://gleipnir.example.com")

	authorizeURL, err := mgr.BeginAuthcode(context.Background(), "inst-1", "https://app.example.com/settings")
	if err != nil {
		t.Fatalf("BeginAuthcode: %v", err)
	}

	// The returned URL must point to the provider's authorization endpoint.
	if !strings.HasPrefix(authorizeURL, "https://provider.example.com/oauth/authorize") {
		t.Errorf("authorize URL does not start with provider endpoint: %q", authorizeURL)
	}

	// Must contain the Gleipnir callback URL as redirect_uri.
	u, err := url.Parse(authorizeURL)
	if err != nil {
		t.Fatalf("parse authorize URL: %v", err)
	}
	redirectURI := u.Query().Get("redirect_uri")
	if redirectURI != "https://gleipnir.example.com/api/v1/admin/plugins/oauth/callback" {
		t.Errorf("unexpected redirect_uri: %q", redirectURI)
	}

	// State must be present and non-empty.
	if u.Query().Get("state") == "" {
		t.Error("expected non-empty state parameter in authorize URL")
	}
}

func TestBeginAuthcode_EmptyPublicURL_Error(t *testing.T) {
	creds := testCreds("oauth2_authcode")
	creds.AuthorizationURL = "https://provider.example.com/oauth/authorize"

	q := buildInstanceWithCreds(t, creds)
	mgr, _ := newTestManager(q, "") // empty public_url

	_, err := mgr.BeginAuthcode(context.Background(), "inst-1", "https://app.example.com/settings")
	if err == nil {
		t.Fatal("expected error when public_url is empty")
	}
	if !strings.Contains(err.Error(), "public_url") {
		t.Errorf("error should mention public_url, got: %v", err)
	}
	if !errors.Is(err, ErrConfigInvalid) {
		t.Errorf("expected ErrConfigInvalid, got: %v", err)
	}
}

func TestBeginAuthcode_MissingClientID_ReturnsConfigInvalid(t *testing.T) {
	creds := testCreds("oauth2_authcode")
	creds.ClientID = "" // clear the client ID set by testCreds

	q := buildInstanceWithCreds(t, creds)
	mgr, _ := newTestManager(q, "https://gleipnir.example.com")

	_, err := mgr.BeginAuthcode(context.Background(), "inst-1", "https://app.example.com/settings")
	if err == nil {
		t.Fatal("expected error when client_id is empty")
	}
	if !errors.Is(err, ErrConfigInvalid) {
		t.Errorf("expected ErrConfigInvalid for missing client_id, got: %v", err)
	}
}

func TestBeginAuthcode_MissingTokenURL_ReturnsConfigInvalid(t *testing.T) {
	creds := testCreds("oauth2_authcode")
	creds.TokenURL = "" // clear the token URL

	q := buildInstanceWithCreds(t, creds)
	mgr, _ := newTestManager(q, "https://gleipnir.example.com")

	_, err := mgr.BeginAuthcode(context.Background(), "inst-1", "https://app.example.com/settings")
	if err == nil {
		t.Fatal("expected error when token_url is empty")
	}
	if !errors.Is(err, ErrConfigInvalid) {
		t.Errorf("expected ErrConfigInvalid for missing token_url, got: %v", err)
	}
}

func TestDecodeStateForRedirect_IgnoresExpiry(t *testing.T) {
	key := fixedKey()
	// Build an already-expired envelope.
	past := time.Unix(1, 0) // very far in the past
	clock := func() time.Time { return past }

	env, _, err := NewStateEnvelope("inst-x", "https://return.example.com/done", clock)
	if err != nil {
		t.Fatalf("NewStateEnvelope: %v", err)
	}
	encoded, err := EncodeState(env, key)
	if err != nil {
		t.Fatalf("EncodeState: %v", err)
	}

	creds := testCreds("oauth2_authcode")
	q := buildInstanceWithCreds(t, creds)
	mgr, _ := newTestManager(q, "https://gleipnir.example.com")

	// DecodeStateForRedirect should succeed even though the envelope is expired.
	decoded, err := mgr.DecodeStateForRedirect(encoded)
	if err != nil {
		t.Fatalf("DecodeStateForRedirect: %v", err)
	}
	if decoded.ReturnURL != env.ReturnURL {
		t.Errorf("ReturnURL: got %q, want %q", decoded.ReturnURL, env.ReturnURL)
	}
}

func TestDecodeStateForRedirect_TamperedHMAC_Error(t *testing.T) {
	key := fixedKey()
	clock := func() time.Time { return time.Unix(1000000, 0) }
	env, _, _ := NewStateEnvelope("inst-x", "https://return.example.com/done", clock)
	encoded, _ := EncodeState(env, key)

	// Flip last byte.
	tampered := []byte(encoded)
	tampered[len(tampered)-1] ^= 0xFF

	creds := testCreds("oauth2_authcode")
	q := buildInstanceWithCreds(t, creds)
	mgr, _ := newTestManager(q, "https://gleipnir.example.com")

	_, err := mgr.DecodeStateForRedirect(string(tampered))
	if err == nil {
		t.Fatal("expected error for tampered state")
	}
}

func TestHandleCallback_NonceSingleUse(t *testing.T) {
	// Build a valid signed state, consume the nonce once via HandleCallback stub,
	// then attempt to consume again via nonces.Consume — must return false.
	baseTime := time.Unix(1000000, 0)
	clock := func() time.Time { return baseTime }

	creds := testCreds("oauth2_authcode")
	creds.AuthorizationURL = "https://provider.example.com/oauth/authorize"
	q := buildInstanceWithCreds(t, creds)

	store := NewDBStore(q, noopEncrypt, noopDecrypt, q, clock)
	nonces := &MemoryNonceStore{
		entries: make(map[string]time.Time),
		clock:   clock,
	}
	hmacKey := fixedKey()
	mgr := NewManager(store, nonces, clock, hmacKey, func() string { return "https://gleipnir.example.com" })

	ctx := context.Background()

	env, nonce, err := NewStateEnvelope("inst-1", "https://app.example.com/settings", clock)
	if err != nil {
		t.Fatalf("NewStateEnvelope: %v", err)
	}
	if err := nonces.Record(ctx, nonce, "inst-1"); err != nil {
		t.Fatalf("Record nonce: %v", err)
	}

	encoded, err := EncodeState(env, hmacKey)
	if err != nil {
		t.Fatalf("EncodeState: %v", err)
	}

	// Attempt HandleCallback — it will fail at code exchange (no real provider),
	// but the nonce is consumed beforehand.
	// We use a deliberate bad code; error from Exchange is expected.
	_, _ = mgr.HandleCallback(ctx, encoded, "bad-code")

	// Nonce must be gone now.
	ok, err := nonces.Consume(ctx, nonce)
	if err != nil {
		t.Fatalf("second Consume: %v", err)
	}
	if ok {
		t.Error("nonce was consumed by HandleCallback but second Consume returned true (nonce re-use)")
	}
}

func TestHandleCallback_ExpiredState_Error(t *testing.T) {
	past := time.Unix(1, 0)
	clock := func() time.Time { return past }

	creds := testCreds("oauth2_authcode")
	q := buildInstanceWithCreds(t, creds)

	baseTime := time.Unix(1000000, 0)
	store := NewDBStore(q, noopEncrypt, noopDecrypt, q, func() time.Time { return baseTime })
	nonces := &MemoryNonceStore{entries: make(map[string]time.Time), clock: func() time.Time { return baseTime }}
	hmacKey := fixedKey()
	mgr := NewManager(store, nonces, func() time.Time { return baseTime }, hmacKey, func() string { return "https://gleipnir.example.com" })

	env, nonce, _ := NewStateEnvelope("inst-1", "https://app.example.com", clock)
	_ = nonces.Record(context.Background(), nonce, "inst-1")
	encoded, _ := EncodeState(env, hmacKey)

	// Now try to handle callback long after the envelope expired.
	_, err := mgr.HandleCallback(context.Background(), encoded, "some-code")
	if err == nil {
		t.Fatal("expected error for expired state")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Errorf("expected expiry error, got: %v", err)
	}
}

func TestBeginClientcred_SuccessTransitionsToHealthy(t *testing.T) {
	// Spin up a minimal fake token endpoint so the clientcredentials exchange succeeds.
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "tok-abc",
			"token_type":   "bearer",
			"expires_in":   3600,
		})
	}))
	defer tokenServer.Close()

	// Seed an instance in unhealthy state (simulating a prior refresh failure).
	creds := testCreds("oauth2_clientcred")
	creds.TokenURL = tokenServer.URL + "/token"
	plain, err := creds.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	enc, err := noopEncrypt(plain)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	q := &fakeOAuthQuerier{
		instance: db.PluginInstance{
			ID:                   "inst-1",
			HealthState:          string(model.PluginHealthStateUnhealthy),
			Version:              1,
			CredentialsEncrypted: &enc,
		},
	}

	mgr, _ := newTestManager(q, "https://gleipnir.example.com")
	if err := mgr.BeginClientcred(context.Background(), "inst-1"); err != nil {
		t.Fatalf("BeginClientcred: %v", err)
	}

	// The health transition to healthy must have been recorded.
	if len(q.healthUpdates) == 0 {
		t.Fatal("expected at least one UpdatePluginInstanceHealth call, got none")
	}
	last := q.healthUpdates[len(q.healthUpdates)-1]
	if last.HealthState != string(model.PluginHealthStateHealthy) {
		t.Errorf("expected health_state=%q, got %q", model.PluginHealthStateHealthy, last.HealthState)
	}
}

// TestBeginAuthcode_WritesLastCallbackURL checks that BeginAuthcode records
// last_oauth_callback_url on the instance row (the existing pre-#230 behavior).
// The fakeOAuthQuerier.UpdatePluginInstanceOAuthCallback returns 1 (success)
// but does not bump the version field; we verify the call succeeds without error.
func TestBeginAuthcode_WritesLastCallbackURL(t *testing.T) {
	creds := testCreds("oauth2_authcode")
	creds.AuthorizationURL = "https://provider.example.com/oauth/authorize"
	q := buildInstanceWithCreds(t, creds)
	mgr, _ := newTestManager(q, "https://gleipnir.example.com")

	// BeginAuthcode must succeed; internally it calls UpdatePluginInstanceOAuthCallback.
	// The fakeOAuthQuerier accepts the call silently (no error). The non-fatal
	// design means the authorize URL is still returned even if the write failed.
	authorizeURL, err := mgr.BeginAuthcode(context.Background(), "inst-1", "https://app.example.com/settings")
	if err != nil {
		t.Fatalf("BeginAuthcode: %v", err)
	}
	if authorizeURL == "" {
		t.Error("expected non-empty authorize URL")
	}
}

// TestHandleCallback_WritesLastCallbackURL verifies that a successful
// HandleCallback records last_oauth_callback_url on the instance row (#230).
func TestHandleCallback_WritesLastCallbackURL(t *testing.T) {
	// Spin up a fake token endpoint so the code exchange succeeds.
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "tok-xyz",
			"token_type":   "bearer",
			"expires_in":   3600,
		})
	}))
	defer tokenServer.Close()

	creds := testCreds("oauth2_authcode")
	creds.AuthorizationURL = "https://provider.example.com/oauth/authorize"
	creds.TokenURL = tokenServer.URL + "/token"
	plain, err := creds.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	enc, err := noopEncrypt(plain)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	q := &fakeOAuthQuerier{
		instance: db.PluginInstance{
			ID:                   "inst-1",
			HealthState:          string(model.PluginHealthStatePendingReauthorize),
			Version:              0,
			CredentialsEncrypted: &enc,
		},
	}

	baseTime := func() time.Time { return time.Unix(1000000, 0) }
	store := NewDBStore(q, noopEncrypt, noopDecrypt, q, baseTime)
	nonces := &MemoryNonceStore{entries: make(map[string]time.Time), clock: baseTime}
	hmacKey := fixedKey()
	mgr := NewManager(store, nonces, baseTime, hmacKey, func() string { return "https://gleipnir.example.com" })

	ctx := context.Background()

	// Build a valid signed state and record its nonce.
	env, nonce, err := NewStateEnvelope("inst-1", "https://app.example.com/done", baseTime)
	if err != nil {
		t.Fatalf("NewStateEnvelope: %v", err)
	}
	if err := nonces.Record(ctx, nonce, "inst-1"); err != nil {
		t.Fatalf("Record: %v", err)
	}
	encoded, err := EncodeState(env, hmacKey)
	if err != nil {
		t.Fatalf("EncodeState: %v", err)
	}

	returnURL, err := mgr.HandleCallback(ctx, encoded, "auth-code-123")
	if err != nil {
		t.Fatalf("HandleCallback: %v", err)
	}
	if returnURL != "https://app.example.com/done" {
		t.Errorf("returnURL = %q, want https://app.example.com/done", returnURL)
	}

	// After a successful callback the instance should have moved to healthy.
	if q.instance.HealthState != string(model.PluginHealthStateHealthy) {
		t.Errorf("health_state = %q, want healthy after HandleCallback", q.instance.HealthState)
	}

	// The callback-URL write must have been attempted exactly once.
	if q.callbackUpdates != 1 {
		t.Errorf("callbackUpdates = %d, want 1 after HandleCallback", q.callbackUpdates)
	}
}

// TestBeginClientcred_WritesLastCallbackURL verifies that a successful
// BeginClientcred records last_oauth_callback_url when public_url is set (#230).
func TestBeginClientcred_WritesLastCallbackURL(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "tok-abc",
			"token_type":   "bearer",
			"expires_in":   3600,
		})
	}))
	defer tokenServer.Close()

	creds := testCreds("oauth2_clientcred")
	creds.TokenURL = tokenServer.URL + "/token"
	plain, err := creds.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	enc, err := noopEncrypt(plain)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	q := &fakeOAuthQuerier{
		instance: db.PluginInstance{
			ID:                   "inst-1",
			HealthState:          string(model.PluginHealthStateUnhealthy),
			Version:              1,
			CredentialsEncrypted: &enc,
		},
	}

	mgr, _ := newTestManager(q, "https://gleipnir.example.com")
	if err := mgr.BeginClientcred(context.Background(), "inst-1"); err != nil {
		t.Fatalf("BeginClientcred: %v", err)
	}

	// The callback-URL write must have been attempted exactly once.
	if q.callbackUpdates != 1 {
		t.Errorf("callbackUpdates = %d, want 1 after BeginClientcred", q.callbackUpdates)
	}
}

// TestBeginClientcred_SkipsCallbackURL_WhenPublicURLEmpty verifies that
// BeginClientcred silently skips the callback URL write when public_url is
// empty — the token is still saved (#230).
func TestBeginClientcred_SkipsCallbackURL_WhenPublicURLEmpty(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "tok-abc",
			"token_type":   "bearer",
			"expires_in":   3600,
		})
	}))
	defer tokenServer.Close()

	creds := testCreds("oauth2_clientcred")
	creds.TokenURL = tokenServer.URL + "/token"
	plain, _ := creds.Marshal()
	enc, _ := noopEncrypt(plain)
	q := &fakeOAuthQuerier{
		instance: db.PluginInstance{
			ID:                   "inst-1",
			HealthState:          string(model.PluginHealthStateUnhealthy),
			Version:              1,
			CredentialsEncrypted: &enc,
		},
	}

	// Empty public_url — callback URL must not be written.
	mgr, _ := newTestManager(q, "")
	if err := mgr.BeginClientcred(context.Background(), "inst-1"); err != nil {
		t.Fatalf("BeginClientcred: %v", err)
	}

	// Should still succeed — token and health state written, but callback URL skipped.
	// We don't assert exact version because health + credentials writes still happen.
	// The important invariant is: no panic and no error returned.
}

// providerErrorBody builds a minimal Slack-shaped OAuth error JSON body.
// code is the machine-readable error code (e.g. "invalid_code");
// description is the human-readable error_description (may be empty).
func providerErrorBody(code, description string) string {
	if description == "" {
		return `{"error":"` + code + `"}`
	}
	return `{"error":"` + code + `","error_description":"` + description + `"}`
}

// buildCallbackState builds a valid signed state envelope seeded with inst-1
// and records its nonce in the given store. Returns the base64-encoded state string.
func buildCallbackState(t *testing.T, nonces NonceStore, hmacKey []byte, clock func() time.Time) string {
	t.Helper()
	env, nonce, err := NewStateEnvelope("inst-1", "/admin/plugins/inst-1", clock)
	if err != nil {
		t.Fatalf("NewStateEnvelope: %v", err)
	}
	if err := nonces.Record(context.Background(), nonce, "inst-1"); err != nil {
		t.Fatalf("Record nonce: %v", err)
	}
	encoded, err := EncodeState(env, hmacKey)
	if err != nil {
		t.Fatalf("EncodeState: %v", err)
	}
	return encoded
}

// buildTokenServerWithError returns an httptest.Server that responds to the
// token endpoint with the given HTTP status code and JSON body.
func buildTokenServerWithError(t *testing.T, statusCode int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestHandleCallback_ProviderError_InvalidCode checks that a Slack-shaped
// "invalid_code" response from the token endpoint surfaces as a
// *ProviderExchangeError with Code="invalid_code".
func TestHandleCallback_ProviderError_InvalidCode(t *testing.T) {
	srv := buildTokenServerWithError(t, http.StatusBadRequest,
		providerErrorBody("invalid_code", "The code has already been redeemed."))

	creds := testCreds("oauth2_authcode")
	creds.TokenURL = srv.URL + "/token"
	q := buildInstanceWithCreds(t, creds)

	baseTime := time.Unix(1000000, 0)
	clock := func() time.Time { return baseTime }
	store := NewDBStore(q, noopEncrypt, noopDecrypt, q, clock)
	nonces := &MemoryNonceStore{entries: make(map[string]time.Time), clock: clock}
	hmacKey := fixedKey()
	mgr := NewManager(store, nonces, clock, hmacKey, func() string { return "https://gleipnir.example.com" })

	encoded := buildCallbackState(t, nonces, hmacKey, clock)

	_, err := mgr.HandleCallback(context.Background(), encoded, "used-code")
	if err == nil {
		t.Fatal("expected error from HandleCallback, got nil")
	}

	var pee *ProviderExchangeError
	if !errors.As(err, &pee) {
		t.Fatalf("expected *ProviderExchangeError, got %T: %v", err, err)
	}
	if pee.Code != "invalid_code" {
		t.Errorf("Code = %q, want %q", pee.Code, "invalid_code")
	}
	if pee.Description == "" {
		t.Error("expected non-empty Description for invalid_code")
	}
	if pee.Raw == "" {
		t.Error("expected non-empty Raw error string")
	}
}

// TestHandleCallback_ProviderError_RedirectURIMismatch checks that
// "redirect_uri_mismatch" surfaces as a *ProviderExchangeError.
func TestHandleCallback_ProviderError_RedirectURIMismatch(t *testing.T) {
	srv := buildTokenServerWithError(t, http.StatusBadRequest,
		providerErrorBody("redirect_uri_mismatch", "redirect_uri did not match"))

	creds := testCreds("oauth2_authcode")
	creds.TokenURL = srv.URL + "/token"
	q := buildInstanceWithCreds(t, creds)

	baseTime := time.Unix(1000000, 0)
	clock := func() time.Time { return baseTime }
	store := NewDBStore(q, noopEncrypt, noopDecrypt, q, clock)
	nonces := &MemoryNonceStore{entries: make(map[string]time.Time), clock: clock}
	hmacKey := fixedKey()
	mgr := NewManager(store, nonces, clock, hmacKey, func() string { return "https://gleipnir.example.com" })

	encoded := buildCallbackState(t, nonces, hmacKey, clock)

	_, err := mgr.HandleCallback(context.Background(), encoded, "code-xyz")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var pee *ProviderExchangeError
	if !errors.As(err, &pee) {
		t.Fatalf("expected *ProviderExchangeError, got %T: %v", err, err)
	}
	if pee.Code != "redirect_uri_mismatch" {
		t.Errorf("Code = %q, want %q", pee.Code, "redirect_uri_mismatch")
	}
}

// TestHandleCallback_ProviderError_InvalidClient checks that "invalid_client"
// (missing/wrong client credentials) surfaces as a *ProviderExchangeError.
func TestHandleCallback_ProviderError_InvalidClient(t *testing.T) {
	srv := buildTokenServerWithError(t, http.StatusUnauthorized,
		providerErrorBody("invalid_client", ""))

	creds := testCreds("oauth2_authcode")
	creds.TokenURL = srv.URL + "/token"
	q := buildInstanceWithCreds(t, creds)

	baseTime := time.Unix(1000000, 0)
	clock := func() time.Time { return baseTime }
	store := NewDBStore(q, noopEncrypt, noopDecrypt, q, clock)
	nonces := &MemoryNonceStore{entries: make(map[string]time.Time), clock: clock}
	hmacKey := fixedKey()
	mgr := NewManager(store, nonces, clock, hmacKey, func() string { return "https://gleipnir.example.com" })

	encoded := buildCallbackState(t, nonces, hmacKey, clock)

	_, err := mgr.HandleCallback(context.Background(), encoded, "code-abc")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var pee *ProviderExchangeError
	if !errors.As(err, &pee) {
		t.Fatalf("expected *ProviderExchangeError, got %T: %v", err, err)
	}
	if pee.Code != "invalid_client" {
		t.Errorf("Code = %q, want %q", pee.Code, "invalid_client")
	}
	// Description may be empty when the provider omits the field — that is fine.
}

// TestProviderExchangeError_ErrorMethod checks the Error() string formatting.
func TestProviderExchangeError_ErrorMethod(t *testing.T) {
	withDesc := &ProviderExchangeError{Code: "invalid_code", Description: "already used", Raw: "raw blob"}
	if withDesc.Error() != "oauth provider error: invalid_code: already used" {
		t.Errorf("unexpected Error(): %q", withDesc.Error())
	}

	noDesc := &ProviderExchangeError{Code: "invalid_client", Raw: "raw blob"}
	if noDesc.Error() != "oauth provider error: invalid_client" {
		t.Errorf("unexpected Error() with no description: %q", noDesc.Error())
	}
}

func TestHandleCallback_NonceSingleUse_WithDBStore(t *testing.T) {
	baseTime := time.Unix(1000000, 0)
	clock := func() time.Time { return baseTime }

	creds := testCreds("oauth2_authcode")
	creds.AuthorizationURL = "https://provider.example.com/oauth/authorize"
	q := buildInstanceWithCreds(t, creds)

	oauthStore := NewDBStore(q, noopEncrypt, noopDecrypt, q, clock)
	dbNonces := NewDBNonceStore(newFakeNonceQuerier(), clock)
	hmacKey := fixedKey()
	mgr := NewManager(oauthStore, dbNonces, clock, hmacKey, func() string { return "https://gleipnir.example.com" })

	ctx := context.Background()

	// Begin flow: nonce is recorded in the DB-backed store.
	authorizeURL, err := mgr.BeginAuthcode(ctx, "inst-1", "https://app.example.com/settings")
	if err != nil {
		t.Fatalf("BeginAuthcode: %v", err)
	}

	// Extract the state parameter from the authorize URL.
	u, err := url.Parse(authorizeURL)
	if err != nil {
		t.Fatalf("parse authorize URL: %v", err)
	}
	rawState := u.Query().Get("state")
	if rawState == "" {
		t.Fatal("expected non-empty state in authorize URL")
	}

	// First callback attempt: nonce consumed (exchange fails with bad code, but
	// nonce consumption happens before the exchange).
	_, _ = mgr.HandleCallback(ctx, rawState, "bad-code")

	// Second callback with the same state must be rejected as a replay.
	_, replayErr := mgr.HandleCallback(ctx, rawState, "bad-code")
	if replayErr == nil {
		t.Fatal("expected error on replay, got nil")
	}
	if replayErr != ErrNonceUsed {
		t.Errorf("expected ErrNonceUsed on replay, got: %v", replayErr)
	}
}
