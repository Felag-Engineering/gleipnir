package oauth

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
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

	env, nonce, err := NewStateEnvelope("inst-1", "https://app.example.com/settings", clock)
	if err != nil {
		t.Fatalf("NewStateEnvelope: %v", err)
	}
	nonces.Record(nonce)

	encoded, err := EncodeState(env, hmacKey)
	if err != nil {
		t.Fatalf("EncodeState: %v", err)
	}

	// Attempt HandleCallback — it will fail at code exchange (no real provider),
	// but the nonce is consumed beforehand.
	// We use a deliberate bad code; error from Exchange is expected.
	_, _ = mgr.HandleCallback(context.Background(), encoded, "bad-code")

	// Nonce must be gone now.
	if nonces.Consume(nonce) {
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
	nonces.Record(nonce)
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
