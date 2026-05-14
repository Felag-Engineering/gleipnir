package oauth

import (
	"context"
	"errors"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
	pluginstate "github.com/felag-engineering/gleipnir/internal/plugin/state"
)

// Tests that swap timeNow must NOT call t.Parallel().

// --- fake querier ---

type fakeOAuthQuerier struct {
	instance       db.PluginInstance
	updateCalls    int
	casFailTimes   int // fail the first N UpdatePluginInstanceCredentials calls
	healthUpdates  []db.UpdatePluginInstanceHealthParams
	auditEvents    []db.InsertPluginAuditEventParams
	expiringRows   []db.PluginInstance
}

func (f *fakeOAuthQuerier) GetPluginInstanceByID(_ context.Context, id string) (db.PluginInstance, error) {
	if f.instance.ID == id {
		return f.instance, nil
	}
	return db.PluginInstance{}, errors.New("not found")
}

func (f *fakeOAuthQuerier) UpdatePluginInstanceCredentials(_ context.Context, arg db.UpdatePluginInstanceCredentialsParams) (int64, error) {
	if f.casFailTimes > 0 {
		f.casFailTimes--
		return 0, nil // CAS conflict
	}
	f.instance.CredentialsEncrypted = arg.CredentialsEncrypted
	f.instance.CredentialsExpiresAt = arg.CredentialsExpiresAt
	f.instance.Version++
	f.updateCalls++
	return 1, nil
}

func (f *fakeOAuthQuerier) UpdatePluginInstanceHealth(_ context.Context, arg db.UpdatePluginInstanceHealthParams) (int64, error) {
	f.healthUpdates = append(f.healthUpdates, arg)
	f.instance.HealthState = arg.HealthState
	f.instance.Version++
	return 1, nil
}

func (f *fakeOAuthQuerier) InsertPluginAuditEvent(_ context.Context, arg db.InsertPluginAuditEventParams) (db.PluginAuditEvent, error) {
	f.auditEvents = append(f.auditEvents, arg)
	return db.PluginAuditEvent{}, nil
}

func (f *fakeOAuthQuerier) ListPluginInstancesWithExpiringCredentials(_ context.Context, _ *string) ([]db.PluginInstance, error) {
	return f.expiringRows, nil
}

func (f *fakeOAuthQuerier) UpdatePluginInstanceOAuthCallback(_ context.Context, _ db.UpdatePluginInstanceOAuthCallbackParams) (int64, error) {
	return 1, nil
}

// fakeOAuthQuerier also satisfies pluginstate.Querier.
var _ pluginstate.Querier = (*fakeOAuthQuerier)(nil)
var _ OAuthQuerier = (*fakeOAuthQuerier)(nil)

// --- helpers ---

func noopEncrypt(p string) (string, error) { return "enc:" + p, nil }
func noopDecrypt(c string) (string, error) {
	if len(c) > 4 && c[:4] == "enc:" {
		return c[4:], nil
	}
	return "", errors.New("not encrypted")
}

func testCreds(strategy string) StoredCredentials {
	return StoredCredentials{
		Strategy:     strategy,
		ClientID:     "client-1",
		ClientSecret: "secret-1",
		TokenURL:     "https://example.com/token",
		Scopes:       []string{"read"},
	}
}

func testToken(exp time.Time) *oauth2.Token {
	return &oauth2.Token{
		AccessToken: "access-token",
		TokenType:   "Bearer",
		Expiry:      exp,
	}
}

// --- tests ---

func TestDBStore_SaveAndLoadCredentials(t *testing.T) {
	q := &fakeOAuthQuerier{
		instance: db.PluginInstance{ID: "inst-1", HealthState: "healthy", Version: 0},
	}
	baseTime := time.Unix(1000000, 0)
	store := NewDBStore(q, noopEncrypt, noopDecrypt, q, func() time.Time { return baseTime })

	creds := testCreds("oauth2_authcode")
	if err := store.SaveCredentials(context.Background(), "inst-1", creds, 0); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}

	loaded, _, err := store.LoadCredentials(context.Background(), "inst-1")
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if loaded.ClientID != creds.ClientID {
		t.Errorf("ClientID: got %q, want %q", loaded.ClientID, creds.ClientID)
	}
	if loaded.Strategy != creds.Strategy {
		t.Errorf("Strategy: got %q, want %q", loaded.Strategy, creds.Strategy)
	}
}

func TestDBStore_SaveToken_SuccessFirstAttempt(t *testing.T) {
	baseTime := time.Unix(1000000, 0)
	creds := testCreds("oauth2_authcode")
	creds.Token = testToken(baseTime.Add(time.Hour))

	plain, _ := creds.Marshal()
	enc, _ := noopEncrypt(plain)
	q := &fakeOAuthQuerier{
		instance: db.PluginInstance{
			ID:                   "inst-1",
			HealthState:          "healthy",
			Version:              0,
			CredentialsEncrypted: &enc,
		},
	}
	store := NewDBStore(q, noopEncrypt, noopDecrypt, q, func() time.Time { return baseTime })

	newTok := testToken(baseTime.Add(2 * time.Hour))
	if err := store.SaveToken(context.Background(), "inst-1", newTok); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}
	if q.updateCalls != 1 {
		t.Errorf("expected 1 update call, got %d", q.updateCalls)
	}
}

func TestDBStore_SaveToken_CASRetrySuccess(t *testing.T) {
	baseTime := time.Unix(1000000, 0)
	creds := testCreds("oauth2_authcode")
	creds.Token = testToken(baseTime.Add(time.Hour))

	plain, _ := creds.Marshal()
	enc, _ := noopEncrypt(plain)
	q := &fakeOAuthQuerier{
		instance: db.PluginInstance{
			ID:                   "inst-1",
			HealthState:          "healthy",
			Version:              0,
			CredentialsEncrypted: &enc,
		},
		casFailTimes: 1, // fail once, then succeed
	}
	store := NewDBStore(q, noopEncrypt, noopDecrypt, q, func() time.Time { return baseTime })

	newTok := testToken(baseTime.Add(2 * time.Hour))
	if err := store.SaveToken(context.Background(), "inst-1", newTok); err != nil {
		t.Fatalf("SaveToken after 1 CAS retry: %v", err)
	}
}

func TestDBStore_SaveToken_SkipIfReloadedTokenFresher(t *testing.T) {
	baseTime := time.Unix(1000000, 0)
	// Existing token expires 3h from now — fresher than the 1h token we're trying to save.
	creds := testCreds("oauth2_authcode")
	creds.Token = testToken(baseTime.Add(3 * time.Hour))

	plain, _ := creds.Marshal()
	enc, _ := noopEncrypt(plain)
	q := &fakeOAuthQuerier{
		instance: db.PluginInstance{
			ID:                   "inst-1",
			HealthState:          "healthy",
			Version:              0,
			CredentialsEncrypted: &enc,
		},
	}
	store := NewDBStore(q, noopEncrypt, noopDecrypt, q, func() time.Time { return baseTime })

	// Try to save a token expiring only 1h from now — should be skipped.
	olderTok := testToken(baseTime.Add(time.Hour))
	if err := store.SaveToken(context.Background(), "inst-1", olderTok); err != nil {
		t.Fatalf("SaveToken: unexpected error: %v", err)
	}
	if q.updateCalls != 0 {
		t.Errorf("expected 0 update calls (fresher token already stored), got %d", q.updateCalls)
	}
}

func TestDBStore_SaveToken_FailAfterMaxAttempts(t *testing.T) {
	baseTime := time.Unix(1000000, 0)
	creds := testCreds("oauth2_authcode")
	creds.Token = testToken(baseTime.Add(time.Hour))

	plain, _ := creds.Marshal()
	enc, _ := noopEncrypt(plain)
	q := &fakeOAuthQuerier{
		instance: db.PluginInstance{
			ID:                   "inst-1",
			HealthState:          "healthy",
			Version:              0,
			CredentialsEncrypted: &enc,
		},
		casFailTimes: 5, // always fail
	}
	store := NewDBStore(q, noopEncrypt, noopDecrypt, q, func() time.Time { return baseTime })

	newTok := testToken(baseTime.Add(2 * time.Hour))
	err := store.SaveToken(context.Background(), "inst-1", newTok)
	if err == nil {
		t.Fatal("expected error after 3 CAS conflicts, got nil")
	}
}

func TestDBStore_MarkRefreshFailed_WritesAuditAndUnhealthy(t *testing.T) {
	q := &fakeOAuthQuerier{
		instance: db.PluginInstance{
			ID:          "inst-1",
			HealthState: "healthy",
			Version:     0,
		},
	}
	baseTime := time.Unix(1000000, 0)
	store := NewDBStore(q, noopEncrypt, noopDecrypt, q, func() time.Time { return baseTime })

	if err := store.MarkRefreshFailed(context.Background(), "inst-1", errors.New("token expired")); err != nil {
		t.Fatalf("MarkRefreshFailed: %v", err)
	}

	// Should have emitted a refresh_failed audit event.
	foundAudit := false
	for _, ev := range q.auditEvents {
		if ev.EventType == auditOAuthRefreshFailed {
			foundAudit = true
		}
	}
	if !foundAudit {
		t.Errorf("expected %q audit event, none found in %v", auditOAuthRefreshFailed, q.auditEvents)
	}

	// Should have driven the instance to unhealthy.
	if q.instance.HealthState != string(model.PluginHealthStateUnhealthy) {
		t.Errorf("expected instance health_state=unhealthy, got %q", q.instance.HealthState)
	}
}
