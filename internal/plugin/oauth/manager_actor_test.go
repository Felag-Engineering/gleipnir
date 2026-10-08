package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
)

func newTokenServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "tok", "token_type": "bearer", "expires_in": 3600,
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func querierWithCreds(t *testing.T, creds StoredCredentials) *fakeOAuthQuerier {
	t.Helper()
	plain, err := creds.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	enc, _ := noopEncrypt(plain)
	return &fakeOAuthQuerier{instance: db.PluginInstance{
		ID: "inst-1", HealthState: "unhealthy", CredentialsEncrypted: &enc,
	}}
}

func issuedEvent(t *testing.T, q *fakeOAuthQuerier) db.InsertPluginAuditEventParams {
	t.Helper()
	for _, ev := range q.auditEvents {
		if ev.EventType == auditOAuthIssued {
			return ev
		}
	}
	t.Fatalf("no %s event in %v", auditOAuthIssued, q.auditEvents)
	return db.InsertPluginAuditEventParams{}
}

func requireActor(t *testing.T, ev db.InsertPluginAuditEventParams, want *string) {
	t.Helper()
	switch {
	case want == nil && ev.ActorUserID != nil:
		t.Errorf("actor = %q, want NULL", *ev.ActorUserID)
	case want != nil && (ev.ActorUserID == nil || *ev.ActorUserID != *want):
		t.Errorf("actor = %v, want %q", ev.ActorUserID, *want)
	}
}

func TestBeginClientcred_ActorAttribution(t *testing.T) {
	adminID := "admin-1"
	tests := []struct {
		name string
		ctx  context.Context
		want *string
	}{
		{"admin caller", WithActor(context.Background(), adminID), &adminID},
		{"no caller", context.Background(), nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTokenServer(t)
			creds := testCreds("oauth2_clientcred")
			creds.TokenURL = srv.URL + "/token"
			q := querierWithCreds(t, creds)
			mgr, _ := newTestManager(q, "https://gleipnir.example.com")

			if err := mgr.BeginClientcred(tc.ctx, "inst-1"); err != nil {
				t.Fatalf("BeginClientcred: %v", err)
			}
			requireActor(t, issuedEvent(t, q), tc.want)
		})
	}
}

func TestAuthcode_BeginToCallbackActorAttribution(t *testing.T) {
	adminID := "admin-1"
	tests := []struct {
		name     string
		beginCtx context.Context
		want     *string
	}{
		{"begun by admin", WithActor(context.Background(), adminID), &adminID},
		{"begun without actor", context.Background(), nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTokenServer(t)
			creds := testCreds("oauth2_authcode")
			creds.AuthorizationURL = "https://provider.example.com/oauth/authorize"
			creds.TokenURL = srv.URL + "/token"
			q := querierWithCreds(t, creds)
			mgr, _ := newTestManager(q, "https://gleipnir.example.com")

			authorizeURL, err := mgr.BeginAuthcode(tc.beginCtx, "inst-1", "/done")
			if err != nil {
				t.Fatalf("BeginAuthcode: %v", err)
			}
			u, err := url.Parse(authorizeURL)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}

			// The callback runs with a bare context, as the unauthenticated route does.
			if _, err := mgr.HandleCallback(context.Background(), u.Query().Get("state"), "code"); err != nil {
				t.Fatalf("HandleCallback: %v", err)
			}
			requireActor(t, issuedEvent(t, mgr.store.q.(*fakeOAuthQuerier)), tc.want)
		})
	}
}

func TestHandleCallback_LegacyEnvelopeWithoutActor_NullActor(t *testing.T) {
	srv := newTokenServer(t)
	creds := testCreds("oauth2_authcode")
	creds.AuthorizationURL = "https://provider.example.com/oauth/authorize"
	creds.TokenURL = srv.URL + "/token"
	q := querierWithCreds(t, creds)

	baseTime := func() time.Time { return time.Unix(1000000, 0) }
	store := NewDBStore(q, noopEncrypt, noopDecrypt, q, baseTime)
	nonces := &MemoryNonceStore{entries: make(map[string]time.Time), clock: baseTime}
	key := fixedKey()
	mgr := NewManager(store, nonces, baseTime, key, func() string { return "https://gleipnir.example.com" })

	// An envelope minted before ActorUserID existed has no such field at all.
	env, nonce, err := NewStateEnvelope("inst-1", "/done", baseTime)
	if err != nil {
		t.Fatalf("NewStateEnvelope: %v", err)
	}
	if err := nonces.Record(context.Background(), nonce, "inst-1"); err != nil {
		t.Fatalf("Record: %v", err)
	}
	encoded, err := EncodeState(env, key)
	if err != nil {
		t.Fatalf("EncodeState: %v", err)
	}
	if _, err := mgr.HandleCallback(context.Background(), encoded, "code"); err != nil {
		t.Fatalf("HandleCallback: %v", err)
	}
	requireActor(t, issuedEvent(t, q), nil)
}

// A forged actor can only be injected by editing the signed payload, which the
// HMAC check rejects before the nonce is touched.
func TestHandleCallback_ForgedActorRejected(t *testing.T) {
	q := querierWithCreds(t, testCreds("oauth2_authcode"))
	baseTime := func() time.Time { return time.Unix(1000000, 0) }
	store := NewDBStore(q, noopEncrypt, noopDecrypt, q, baseTime)
	nonces := &MemoryNonceStore{entries: make(map[string]time.Time), clock: baseTime}
	mgr := NewManager(store, nonces, baseTime, fixedKey(), func() string { return "https://gleipnir.example.com" })

	env, nonce, _ := NewStateEnvelope("inst-1", "/done", baseTime)
	env.ActorUserID = "victim-admin"
	_ = nonces.Record(context.Background(), nonce, "inst-1")
	forged, err := EncodeState(env, []byte("attacker-key-attacker-key-attack"))
	if err != nil {
		t.Fatalf("EncodeState: %v", err)
	}

	_, err = mgr.HandleCallback(context.Background(), forged, "code")
	if !errors.Is(err, ErrStateTampered) {
		t.Fatalf("err = %v, want ErrStateTampered", err)
	}
	if _, err := mgr.HandleCallback(context.Background(), "not-a-state", "code"); err == nil {
		t.Fatal("garbage state accepted")
	}
	for _, ev := range q.auditEvents {
		if ev.EventType == auditOAuthIssued {
			t.Fatal("issued event written for rejected callback")
		}
	}
}
