package oauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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
	baseTime := func() time.Time { return time.Unix(1000000, 0) }
	nonceStores := map[string]func() NonceStore{
		"memory": func() NonceStore {
			return &MemoryNonceStore{entries: make(map[string]time.Time), clock: baseTime}
		},
		"db": func() NonceStore { return NewDBNonceStore(newFakeNonceQuerier(), baseTime) },
	}
	tests := []struct {
		name     string
		beginCtx context.Context
		want     *string
	}{
		{"begun by admin", WithActor(context.Background(), adminID), &adminID},
		{"begun without actor", context.Background(), nil},
	}
	for storeName, newNonces := range nonceStores {
		for _, tc := range tests {
			t.Run(storeName+"/"+tc.name, func(t *testing.T) {
				srv := newTokenServer(t)
				creds := testCreds("oauth2_authcode")
				creds.AuthorizationURL = "https://provider.example.com/oauth/authorize"
				creds.TokenURL = srv.URL + "/token"
				q := querierWithCreds(t, creds)
				store := NewDBStore(q, noopEncrypt, noopDecrypt, q, baseTime)
				mgr := NewManager(store, newNonces(), baseTime, fixedKey(), func() string { return "https://gleipnir.example.com" })

				authorizeURL, err := mgr.BeginAuthcode(tc.beginCtx, "inst-1", "/done")
				if err != nil {
					t.Fatalf("BeginAuthcode: %v", err)
				}
				u, err := url.Parse(authorizeURL)
				if err != nil {
					t.Fatalf("parse: %v", err)
				}
				state := u.Query().Get("state")

				// The user id must not travel to the provider or browser history,
				// in the clear or inside the base64 payload.
				decoded, _ := base64.URLEncoding.DecodeString(state)
				if strings.Contains(authorizeURL, adminID) || strings.Contains(string(decoded), adminID) {
					t.Errorf("authorize URL leaks the actor user id: %s", authorizeURL)
				}

				// The callback runs with a bare context, as the unauthenticated route does.
				if _, err := mgr.HandleCallback(context.Background(), state, "code"); err != nil {
					t.Fatalf("HandleCallback: %v", err)
				}
				requireActor(t, issuedEvent(t, q), tc.want)
			})
		}
	}
}

func TestHandleCallback_LegacyNonceWithoutActor_NullActor(t *testing.T) {
	srv := newTokenServer(t)
	creds := testCreds("oauth2_authcode")
	creds.AuthorizationURL = "https://provider.example.com/oauth/authorize"
	creds.TokenURL = srv.URL + "/token"
	q := querierWithCreds(t, creds)

	baseTime := func() time.Time { return time.Unix(1000000, 0) }
	store := NewDBStore(q, noopEncrypt, noopDecrypt, q, baseTime)
	nonces := NewDBNonceStore(newFakeNonceQuerier(), baseTime)
	key := fixedKey()
	mgr := NewManager(store, nonces, baseTime, key, func() string { return "https://gleipnir.example.com" })

	// A nonce row written before actor_user_id existed reads back as NULL.
	env, nonce, err := NewStateEnvelope("inst-1", "/done", baseTime)
	if err != nil {
		t.Fatalf("NewStateEnvelope: %v", err)
	}
	if err := nonces.Record(context.Background(), nonce, "inst-1", nil); err != nil {
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

// A state signed with the wrong key is rejected before the nonce is touched.
func TestHandleCallback_ForgedStateRejected(t *testing.T) {
	q := querierWithCreds(t, testCreds("oauth2_authcode"))
	baseTime := func() time.Time { return time.Unix(1000000, 0) }
	store := NewDBStore(q, noopEncrypt, noopDecrypt, q, baseTime)
	nonces := &MemoryNonceStore{entries: make(map[string]time.Time), clock: baseTime}
	mgr := NewManager(store, nonces, baseTime, fixedKey(), func() string { return "https://gleipnir.example.com" })

	env, nonce, _ := NewStateEnvelope("inst-1", "/done", baseTime)
	_ = nonces.Record(context.Background(), nonce, "inst-1", nil)
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
