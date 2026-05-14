package oauth

import (
	"context"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
)

// Tests that swap timeNow must NOT call t.Parallel().

// fakeScannerQuerier satisfies ScannerQuerier for scanner tests.
type fakeScannerQuerier struct {
	rows []db.PluginInstance
}

func (f *fakeScannerQuerier) ListPluginInstancesWithExpiringCredentials(_ context.Context, _ *string) ([]db.PluginInstance, error) {
	return f.rows, nil
}

func TestRefreshScanner_NoRows_NoPanic(t *testing.T) {
	baseTime := time.Unix(1000000, 0)
	clock := func() time.Time { return baseTime }

	q := &fakeOAuthQuerier{}
	store := NewDBStore(q, noopEncrypt, noopDecrypt, q, clock)

	sq := &fakeScannerQuerier{rows: nil}
	scanner := NewRefreshScanner(store, sq, func() string { return "https://gleipnir.example.com" }, time.Minute, 15*time.Minute)
	scanner.timeNow = clock

	// scan should complete without panic or error.
	scanner.scan(context.Background())
}

func TestRefreshScanner_SkipsAuthcodeWhenPublicURLEmpty(t *testing.T) {
	baseTime := time.Unix(1000000, 0)
	clock := func() time.Time { return baseTime }

	// Set up an instance with authcode credentials.
	creds := testCreds("oauth2_authcode")
	plain, _ := creds.Marshal()
	enc, _ := noopEncrypt(plain)
	expiresAt := baseTime.Add(5 * time.Minute).UTC().Format(time.RFC3339Nano)
	inst := db.PluginInstance{
		ID:                   "inst-auth",
		HealthState:          "healthy",
		Version:              0,
		CredentialsEncrypted: &enc,
		CredentialsExpiresAt: &expiresAt,
	}
	q := &fakeOAuthQuerier{instance: inst}

	sq := &fakeScannerQuerier{rows: []db.PluginInstance{inst}}
	store := NewDBStore(q, noopEncrypt, noopDecrypt, q, clock)

	// Public URL is empty — authcode rows should be skipped.
	scanner := NewRefreshScanner(store, sq, func() string { return "" }, time.Minute, 15*time.Minute)
	scanner.timeNow = clock

	// The scan should skip the authcode instance (no Token(), no panic).
	// If it did not skip, Token() would fail because there's no real provider.
	// The test succeeds as long as scan does not panic and update count is 0.
	scanner.scan(context.Background())

	if q.updateCalls != 0 {
		t.Errorf("expected 0 update calls for authcode with empty public_url, got %d", q.updateCalls)
	}
}

func TestRefreshScanner_ClientcredProcessedWithoutPublicURL(t *testing.T) {
	// Client credentials do not need a redirect URL — they should still be
	// attempted even when public_url is empty.
	baseTime := time.Unix(1000000, 0)
	clock := func() time.Time { return baseTime }

	creds := testCreds("oauth2_clientcred")
	plain, _ := creds.Marshal()
	enc, _ := noopEncrypt(plain)
	expiresAt := baseTime.Add(5 * time.Minute).UTC().Format(time.RFC3339Nano)
	inst := db.PluginInstance{
		ID:                   "inst-cc",
		HealthState:          "healthy",
		Version:              0,
		CredentialsEncrypted: &enc,
		CredentialsExpiresAt: &expiresAt,
	}
	q := &fakeOAuthQuerier{instance: inst}

	sq := &fakeScannerQuerier{rows: []db.PluginInstance{inst}}
	store := NewDBStore(q, noopEncrypt, noopDecrypt, q, clock)

	scanner := NewRefreshScanner(store, sq, func() string { return "" }, time.Minute, 15*time.Minute)
	scanner.timeNow = clock

	// Token() will fail because the fake provider does not exist, but the
	// important thing is that the scanner tried (MarkRefreshFailed is called).
	scanner.scan(context.Background())

	// MarkRefreshFailed should have emitted an audit event.
	foundFailedAudit := false
	for _, ev := range q.auditEvents {
		if ev.EventType == auditOAuthRefreshFailed {
			foundFailedAudit = true
		}
	}
	if !foundFailedAudit {
		t.Errorf("expected %q audit event for clientcred refresh failure, got: %v", auditOAuthRefreshFailed, q.auditEvents)
	}
}

func TestRefreshScanner_CutoffIsNowPlusLead(t *testing.T) {
	// The scanner should pass now+lead as the cutoff to ListPluginInstancesWithExpiringCredentials.
	baseTime := time.Unix(1000000, 0)
	clock := func() time.Time { return baseTime }

	var receivedCutoff *string
	sq := &capturedCutoffQuerier{capture: &receivedCutoff}

	q := &fakeOAuthQuerier{}
	store := NewDBStore(q, noopEncrypt, noopDecrypt, q, clock)

	lead := 15 * time.Minute
	scanner := NewRefreshScanner(store, sq, func() string { return "https://gleipnir.example.com" }, time.Minute, lead)
	scanner.timeNow = clock

	scanner.scan(context.Background())

	if receivedCutoff == nil {
		t.Fatal("expected cutoff to be passed, got nil")
	}
	expected := baseTime.Add(lead).UTC().Format(time.RFC3339Nano)
	if *receivedCutoff != expected {
		t.Errorf("cutoff: got %q, want %q", *receivedCutoff, expected)
	}
}

// capturedCutoffQuerier captures the cutoff value passed to ListPluginInstancesWithExpiringCredentials.
type capturedCutoffQuerier struct {
	capture **string
}

func (c *capturedCutoffQuerier) ListPluginInstancesWithExpiringCredentials(_ context.Context, cutoff *string) ([]db.PluginInstance, error) {
	*c.capture = cutoff
	return nil, nil
}
