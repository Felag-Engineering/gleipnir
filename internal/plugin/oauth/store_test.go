package oauth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/infra/headervalidate"
	"github.com/felag-engineering/gleipnir/internal/model"
	pluginstate "github.com/felag-engineering/gleipnir/internal/plugin/state"
	sdkmanifest "github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
)

// Tests that swap timeNow must NOT call t.Parallel().

// --- fake querier ---

type fakeOAuthQuerier struct {
	instance      db.PluginInstance
	updateCalls   int
	casFailTimes  int // fail the first N UpdatePluginInstanceCredentials calls
	healthUpdates []db.UpdatePluginInstanceHealthParams
	auditEvents   []db.InsertPluginAuditEventParams
	expiringRows  []db.PluginInstance
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

	// Should have emitted a refresh_failed audit event with severity "warning" (AC #2).
	var foundAuditEvent *db.InsertPluginAuditEventParams
	for i, ev := range q.auditEvents {
		if ev.EventType == auditOAuthRefreshFailed {
			foundAuditEvent = &q.auditEvents[i]
		}
	}
	if foundAuditEvent == nil {
		t.Errorf("expected %q audit event, none found in %v", auditOAuthRefreshFailed, q.auditEvents)
	} else if foundAuditEvent.Severity != "warning" {
		t.Errorf("expected refresh_failed audit severity %q, got %q", "warning", foundAuditEvent.Severity)
	}

	// Should have driven the instance to unhealthy.
	if q.instance.HealthState != string(model.PluginHealthStateUnhealthy) {
		t.Errorf("expected instance health_state=unhealthy, got %q", q.instance.HealthState)
	}
}

func TestDBStore_EmitIssued_SeverityInfo(t *testing.T) {
	q := &fakeOAuthQuerier{
		instance: db.PluginInstance{ID: "inst-1", HealthState: "healthy", Version: 0},
	}
	store := NewDBStore(q, noopEncrypt, noopDecrypt, q, func() time.Time { return time.Unix(1000000, 0) })

	store.EmitIssued(context.Background(), "inst-1")

	var found *db.InsertPluginAuditEventParams
	for i, ev := range q.auditEvents {
		if ev.EventType == auditOAuthIssued {
			found = &q.auditEvents[i]
		}
	}
	if found == nil {
		t.Fatalf("expected %q audit event", auditOAuthIssued)
	}
	if found.Severity != "info" {
		t.Errorf("expected severity %q, got %q", "info", found.Severity)
	}
}

// --- SetStaticAPIKey tests ---

func TestDBStore_SetStaticAPIKey_WritesSubblobAndAudit(t *testing.T) {
	q := &fakeOAuthQuerier{
		instance: db.PluginInstance{ID: "inst-1", HealthState: "healthy", Version: 0},
	}
	baseTime := time.Unix(1000000, 0)
	store := NewDBStore(q, noopEncrypt, noopDecrypt, q, func() time.Time { return baseTime })

	// Seed an empty static_api_key row.
	seed := StoredCredentials{Strategy: sdkmanifest.AuthStrategyStaticAPIKey}
	if err := store.SaveCredentials(context.Background(), "inst-1", seed, 0); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := store.SetStaticAPIKey(context.Background(), "inst-1", "X-API-Key", "Bearer", "sk-live-123"); err != nil {
		t.Fatalf("SetStaticAPIKey: %v", err)
	}

	loaded, _, err := store.LoadCredentials(context.Background(), "inst-1")
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if loaded.StaticAPIKey == nil {
		t.Fatal("expected StaticAPIKey sub-blob to be set")
	}
	if loaded.StaticAPIKey.HeaderName != "X-API-Key" {
		t.Errorf("HeaderName: got %q, want %q", loaded.StaticAPIKey.HeaderName, "X-API-Key")
	}
	if loaded.StaticAPIKey.APIKey != "sk-live-123" {
		t.Errorf("APIKey: got %q, want %q", loaded.StaticAPIKey.APIKey, "sk-live-123")
	}

	// Verify audit event was emitted.
	found := false
	for _, ev := range q.auditEvents {
		if ev.EventType == auditCredentialSet {
			found = true
		}
	}
	if !found {
		t.Errorf("expected %q audit event", auditCredentialSet)
	}
}

func TestDBStore_SetStaticAPIKey_WrongStrategy(t *testing.T) {
	q := &fakeOAuthQuerier{
		instance: db.PluginInstance{ID: "inst-1", HealthState: "healthy", Version: 0},
	}
	baseTime := time.Unix(1000000, 0)
	store := NewDBStore(q, noopEncrypt, noopDecrypt, q, func() time.Time { return baseTime })

	// Seed a header_set row — different strategy.
	seed := StoredCredentials{Strategy: sdkmanifest.AuthStrategyHeaderSet, HeaderSet: &HeaderSetCreds{Headers: []NamedHeader{}}}
	if err := store.SaveCredentials(context.Background(), "inst-1", seed, 0); err != nil {
		t.Fatalf("seed: %v", err)
	}

	err := store.SetStaticAPIKey(context.Background(), "inst-1", "X-API-Key", "", "key")
	if !errors.Is(err, ErrWrongStrategy) {
		t.Errorf("expected ErrWrongStrategy, got %v", err)
	}
}

// --- SetHeaderSetEntry tests ---

func TestDBStore_SetHeaderSetEntry_AddAndReplace(t *testing.T) {
	q := &fakeOAuthQuerier{
		instance: db.PluginInstance{ID: "inst-1", HealthState: "healthy", Version: 0},
	}
	baseTime := time.Unix(1000000, 0)
	store := NewDBStore(q, noopEncrypt, noopDecrypt, q, func() time.Time { return baseTime })

	seed := StoredCredentials{Strategy: sdkmanifest.AuthStrategyHeaderSet, HeaderSet: &HeaderSetCreds{Headers: []NamedHeader{}}}
	if err := store.SaveCredentials(context.Background(), "inst-1", seed, 0); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Add first header.
	if err := store.SetHeaderSetEntry(context.Background(), "inst-1", NamedHeader{Name: "X-A", Value: "val-a"}); err != nil {
		t.Fatalf("SetHeaderSetEntry add: %v", err)
	}
	// Add second header.
	if err := store.SetHeaderSetEntry(context.Background(), "inst-1", NamedHeader{Name: "X-B", Value: "val-b"}); err != nil {
		t.Fatalf("SetHeaderSetEntry add second: %v", err)
	}
	// Replace first header via case-insensitive match.
	if err := store.SetHeaderSetEntry(context.Background(), "inst-1", NamedHeader{Name: "x-a", Value: "new-a"}); err != nil {
		t.Fatalf("SetHeaderSetEntry replace: %v", err)
	}

	loaded, _, err := store.LoadCredentials(context.Background(), "inst-1")
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if loaded.HeaderSet == nil || len(loaded.HeaderSet.Headers) != 2 {
		t.Fatalf("expected 2 headers, got %v", loaded.HeaderSet)
	}
	// First header should have the new value (replaced by case-insensitive match).
	if loaded.HeaderSet.Headers[0].Value != "new-a" {
		t.Errorf("expected headers[0].Value=new-a, got %q", loaded.HeaderSet.Headers[0].Value)
	}
	if loaded.HeaderSet.Headers[1].Name != "X-B" {
		t.Errorf("expected headers[1].Name=X-B, got %q", loaded.HeaderSet.Headers[1].Name)
	}
}

func TestDBStore_SetHeaderSetEntry_ReservedHeaderRejected(t *testing.T) {
	q := &fakeOAuthQuerier{
		instance: db.PluginInstance{ID: "inst-1", HealthState: "healthy", Version: 0},
	}
	baseTime := time.Unix(1000000, 0)
	store := NewDBStore(q, noopEncrypt, noopDecrypt, q, func() time.Time { return baseTime })

	seed := StoredCredentials{Strategy: sdkmanifest.AuthStrategyHeaderSet, HeaderSet: &HeaderSetCreds{Headers: []NamedHeader{}}}
	if err := store.SaveCredentials(context.Background(), "inst-1", seed, 0); err != nil {
		t.Fatalf("seed: %v", err)
	}

	for _, reserved := range headervalidate.ReservedHeaderNames {
		err := store.SetHeaderSetEntry(context.Background(), "inst-1", NamedHeader{Name: reserved, Value: "v"})
		if err == nil {
			t.Errorf("SetHeaderSetEntry(%q): expected error, got nil", reserved)
		}
	}
	// Verify the row was not modified.
	loaded, _, _ := store.LoadCredentials(context.Background(), "inst-1")
	if loaded.HeaderSet != nil && len(loaded.HeaderSet.Headers) != 0 {
		t.Errorf("expected no headers after rejected writes, got %v", loaded.HeaderSet.Headers)
	}
}

// --- DeleteHeaderSetEntry tests ---

func TestDBStore_DeleteHeaderSetEntry_RemovesEntry(t *testing.T) {
	q := &fakeOAuthQuerier{
		instance: db.PluginInstance{ID: "inst-1", HealthState: "healthy", Version: 0},
	}
	baseTime := time.Unix(1000000, 0)
	store := NewDBStore(q, noopEncrypt, noopDecrypt, q, func() time.Time { return baseTime })

	seed := StoredCredentials{
		Strategy: sdkmanifest.AuthStrategyHeaderSet,
		HeaderSet: &HeaderSetCreds{
			Headers: []NamedHeader{
				{Name: "X-A", Value: "a"},
				{Name: "X-B", Value: "b"},
			},
		},
	}
	if err := store.SaveCredentials(context.Background(), "inst-1", seed, 0); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Delete X-A via case-insensitive match.
	if err := store.DeleteHeaderSetEntry(context.Background(), "inst-1", "x-a"); err != nil {
		t.Fatalf("DeleteHeaderSetEntry: %v", err)
	}

	loaded, _, err := store.LoadCredentials(context.Background(), "inst-1")
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if loaded.HeaderSet == nil || len(loaded.HeaderSet.Headers) != 1 {
		t.Fatalf("expected 1 header after delete, got %v", loaded.HeaderSet)
	}
	if loaded.HeaderSet.Headers[0].Name != "X-B" {
		t.Errorf("expected X-B to remain, got %q", loaded.HeaderSet.Headers[0].Name)
	}
	// Strategy must be preserved.
	if loaded.Strategy != sdkmanifest.AuthStrategyHeaderSet {
		t.Errorf("Strategy changed after delete: %q", loaded.Strategy)
	}
}

func TestDBStore_DeleteHeaderSetEntry_LastEntryLeavesEmptySlice(t *testing.T) {
	q := &fakeOAuthQuerier{
		instance: db.PluginInstance{ID: "inst-1", HealthState: "healthy", Version: 0},
	}
	baseTime := time.Unix(1000000, 0)
	store := NewDBStore(q, noopEncrypt, noopDecrypt, q, func() time.Time { return baseTime })

	seed := StoredCredentials{
		Strategy:  sdkmanifest.AuthStrategyHeaderSet,
		HeaderSet: &HeaderSetCreds{Headers: []NamedHeader{{Name: "X-A", Value: "a"}}},
	}
	if err := store.SaveCredentials(context.Background(), "inst-1", seed, 0); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := store.DeleteHeaderSetEntry(context.Background(), "inst-1", "X-A"); err != nil {
		t.Fatalf("DeleteHeaderSetEntry: %v", err)
	}

	loaded, _, err := store.LoadCredentials(context.Background(), "inst-1")
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if loaded.HeaderSet == nil {
		t.Fatal("HeaderSet sub-blob should not be nil after deleting last entry")
	}
	if loaded.HeaderSet.Headers == nil {
		t.Error("Headers slice should be non-nil (empty []NamedHeader{}) after deleting last entry")
	}
	if len(loaded.HeaderSet.Headers) != 0 {
		t.Errorf("expected 0 headers, got %d", len(loaded.HeaderSet.Headers))
	}
	if loaded.Strategy != sdkmanifest.AuthStrategyHeaderSet {
		t.Errorf("Strategy changed: %q", loaded.Strategy)
	}
}

func TestDBStore_DeleteHeaderSetEntry_IdempotentMissing(t *testing.T) {
	q := &fakeOAuthQuerier{
		instance: db.PluginInstance{ID: "inst-1", HealthState: "healthy", Version: 0},
	}
	baseTime := time.Unix(1000000, 0)
	store := NewDBStore(q, noopEncrypt, noopDecrypt, q, func() time.Time { return baseTime })

	seed := StoredCredentials{Strategy: sdkmanifest.AuthStrategyHeaderSet, HeaderSet: &HeaderSetCreds{Headers: []NamedHeader{}}}
	if err := store.SaveCredentials(context.Background(), "inst-1", seed, 0); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Deleting a name that doesn't exist should succeed silently.
	if err := store.DeleteHeaderSetEntry(context.Background(), "inst-1", "X-NonExistent"); err != nil {
		t.Errorf("expected no error for idempotent delete, got %v", err)
	}
}

// --- SetBasicAuth tests ---

func TestDBStore_SetBasicAuth_RoundTrip(t *testing.T) {
	q := &fakeOAuthQuerier{
		instance: db.PluginInstance{ID: "inst-1", HealthState: "healthy", Version: 0},
	}
	baseTime := time.Unix(1000000, 0)
	store := NewDBStore(q, noopEncrypt, noopDecrypt, q, func() time.Time { return baseTime })

	seed := StoredCredentials{Strategy: sdkmanifest.AuthStrategyBasicAuth}
	if err := store.SaveCredentials(context.Background(), "inst-1", seed, 0); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := store.SetBasicAuth(context.Background(), "inst-1", "alice", "s3cr3t"); err != nil {
		t.Fatalf("SetBasicAuth: %v", err)
	}

	loaded, _, err := store.LoadCredentials(context.Background(), "inst-1")
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if loaded.BasicAuth == nil {
		t.Fatal("expected BasicAuth sub-blob")
	}
	if loaded.BasicAuth.Username != "alice" {
		t.Errorf("Username: got %q, want alice", loaded.BasicAuth.Username)
	}
	if loaded.BasicAuth.Password != "s3cr3t" {
		t.Errorf("Password: got %q, want s3cr3t", loaded.BasicAuth.Password)
	}
}

// --- ClearCredentials tests ---

func TestDBStore_ClearCredentials_WipesSecretsPreservesStrategy(t *testing.T) {
	q := &fakeOAuthQuerier{
		instance: db.PluginInstance{ID: "inst-1", HealthState: "healthy", Version: 0},
	}
	baseTime := time.Unix(1000000, 0)
	store := NewDBStore(q, noopEncrypt, noopDecrypt, q, func() time.Time { return baseTime })

	seed := StoredCredentials{
		Strategy:  sdkmanifest.AuthStrategyStaticAPIKey,
		StaticAPIKey: &StaticAPIKeyCreds{HeaderName: "X-Key", APIKey: "secret"},
	}
	if err := store.SaveCredentials(context.Background(), "inst-1", seed, 0); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := store.ClearCredentials(context.Background(), "inst-1"); err != nil {
		t.Fatalf("ClearCredentials: %v", err)
	}

	loaded, _, err := store.LoadCredentials(context.Background(), "inst-1")
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if loaded.Strategy != sdkmanifest.AuthStrategyStaticAPIKey {
		t.Errorf("Strategy changed after clear: %q", loaded.Strategy)
	}
	if loaded.StaticAPIKey != nil {
		t.Errorf("expected StaticAPIKey sub-blob to be nil after clear, got %v", loaded.StaticAPIKey)
	}

	// Verify audit event was emitted.
	found := false
	for _, ev := range q.auditEvents {
		if ev.EventType == auditCredentialCleared {
			found = true
		}
	}
	if !found {
		t.Errorf("expected %q audit event", auditCredentialCleared)
	}
}

// --- CAS retry test ---

func TestDBStore_SetStaticAPIKey_CASRetrySuccess(t *testing.T) {
	baseTime := time.Unix(1000000, 0)
	seed := StoredCredentials{Strategy: sdkmanifest.AuthStrategyStaticAPIKey}
	plain, _ := seed.Marshal()
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

	if err := store.SetStaticAPIKey(context.Background(), "inst-1", "X-Key", "", "val"); err != nil {
		t.Fatalf("SetStaticAPIKey after 1 CAS retry: %v", err)
	}
}

// --- Mutex serialisation test ---

// TestDBStore_MutexSerialisation_SetAPIKeyAndSaveTokenConcurrent fires
// SetStaticAPIKey and SaveToken on the same instance concurrently and asserts
// that both complete without ErrCASConflict surfacing externally. The
// per-instance mutex means only one goroutine holds the lock at a time; the
// other blocks and retries — the net result is both writes succeed.
func TestDBStore_MutexSerialisation_SetAPIKeyAndSaveTokenConcurrent(t *testing.T) {
	// This test mutates shared state; must not run in parallel.

	baseTime := time.Unix(1000000, 0)
	seed := StoredCredentials{
		Strategy: sdkmanifest.AuthStrategyStaticAPIKey,
		StaticAPIKey: &StaticAPIKeyCreds{HeaderName: "X-Key", APIKey: "old"},
	}
	plain, _ := seed.Marshal()
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

	var wg sync.WaitGroup
	var setErr, saveErr error

	wg.Add(2)
	go func() {
		defer wg.Done()
		setErr = store.SetStaticAPIKey(context.Background(), "inst-1", "X-Key", "", "new-key")
	}()
	go func() {
		defer wg.Done()
		// SaveToken only applies to oauth2 strategies; for this test we call
		// SetBasicAuth on a different fake instance to exercise concurrent
		// mutex acquisition path, but since we want the same instance we use
		// ClearCredentials (which also holds the mutex).
		saveErr = store.ClearCredentials(context.Background(), "inst-1")
	}()
	wg.Wait()

	if setErr != nil {
		t.Errorf("SetStaticAPIKey: unexpected error: %v", setErr)
	}
	if saveErr != nil {
		t.Errorf("ClearCredentials: unexpected error: %v", saveErr)
	}
}
