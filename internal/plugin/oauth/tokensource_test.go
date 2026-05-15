package oauth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/felag-engineering/gleipnir/internal/db"
)

// fakeInnerSource is a stub oauth2.TokenSource. It returns tokens from a queue
// in order, then returns the last token forever — matching the upstream
// ReuseTokenSource semantics (cache the latest token until refresh is needed).
// If `err` is non-nil, Token() returns it instead.
type fakeInnerSource struct {
	mu      sync.Mutex
	tokens  []*oauth2.Token
	idx     int
	err     error
	callCnt int
}

func (f *fakeInnerSource) Token() (*oauth2.Token, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callCnt++
	if f.err != nil {
		return nil, f.err
	}
	if f.idx >= len(f.tokens) {
		return f.tokens[len(f.tokens)-1], nil
	}
	tok := f.tokens[f.idx]
	f.idx++
	return tok, nil
}

func newPersistingTS(t *testing.T, store *DBStore, instanceID string, inner oauth2.TokenSource, seed *oauth2.Token) *persistingTokenSource {
	t.Helper()
	return &persistingTokenSource{
		ctx:        context.Background(),
		store:      store,
		instanceID: instanceID,
		inner:      inner,
		lastSeen:   seed,
		clock:      func() time.Time { return time.Unix(1000000, 0) },
	}
}

func TestTokenChanged(t *testing.T) {
	exp := time.Unix(2000000, 0)
	cases := []struct {
		name string
		prev *oauth2.Token
		next *oauth2.Token
		want bool
	}{
		{"both nil", nil, nil, false},
		{"prev nil, next set", nil, &oauth2.Token{AccessToken: "a"}, true},
		{"prev set, next nil", &oauth2.Token{AccessToken: "a"}, nil, false},
		{"same access + expiry", &oauth2.Token{AccessToken: "a", Expiry: exp}, &oauth2.Token{AccessToken: "a", Expiry: exp}, false},
		{"different access", &oauth2.Token{AccessToken: "a"}, &oauth2.Token{AccessToken: "b"}, true},
		{"different expiry", &oauth2.Token{AccessToken: "a", Expiry: exp}, &oauth2.Token{AccessToken: "a", Expiry: exp.Add(time.Hour)}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tokenChanged(tc.prev, tc.next); got != tc.want {
				t.Errorf("tokenChanged(%+v, %+v) = %v, want %v", tc.prev, tc.next, got, tc.want)
			}
		})
	}
}

func TestPersistingTokenSource_NoRotation_NoSaveNoAudit(t *testing.T) {
	seed := testToken(time.Unix(2000000, 0))
	q := &fakeOAuthQuerier{
		instance: db.PluginInstance{ID: "inst-1", HealthState: "healthy", Version: 0},
	}
	store := NewDBStore(q, noopEncrypt, noopDecrypt, q, func() time.Time { return time.Unix(1000000, 0) })
	// Seed the row with credentials so SaveToken's CAS retry has a payload.
	creds := testCreds("oauth2_authcode")
	creds.Token = seed
	if err := store.SaveCredentials(context.Background(), "inst-1", creds, 0); err != nil {
		t.Fatalf("seed: %v", err)
	}
	q.auditEvents = nil // clear baseline (none expected anyway)
	priorUpdates := q.updateCalls

	inner := &fakeInnerSource{tokens: []*oauth2.Token{seed}}
	ts := newPersistingTS(t, store, "inst-1", inner, seed)

	if _, err := ts.Token(); err != nil {
		t.Fatalf("Token: %v", err)
	}

	if q.updateCalls != priorUpdates {
		t.Errorf("expected no UpdatePluginInstanceCredentials calls when token unchanged, got %d new", q.updateCalls-priorUpdates)
	}
	for _, ev := range q.auditEvents {
		if ev.EventType == auditOAuthRefreshed {
			t.Errorf("expected no plugin_oauth_refreshed event when token unchanged")
		}
	}
}

func TestPersistingTokenSource_RotationCallsSaveAndEmitsRefreshed(t *testing.T) {
	oldTok := testToken(time.Unix(2000000, 0))
	newTok := &oauth2.Token{AccessToken: "rotated", Expiry: time.Unix(3000000, 0)}

	q := &fakeOAuthQuerier{
		instance: db.PluginInstance{ID: "inst-1", HealthState: "healthy", Version: 0},
	}
	store := NewDBStore(q, noopEncrypt, noopDecrypt, q, func() time.Time { return time.Unix(1000000, 0) })
	creds := testCreds("oauth2_authcode")
	creds.Token = oldTok
	if err := store.SaveCredentials(context.Background(), "inst-1", creds, 0); err != nil {
		t.Fatalf("seed: %v", err)
	}
	q.auditEvents = nil

	inner := &fakeInnerSource{tokens: []*oauth2.Token{newTok}}
	ts := newPersistingTS(t, store, "inst-1", inner, oldTok)

	if _, err := ts.Token(); err != nil {
		t.Fatalf("Token: %v", err)
	}

	if q.updateCalls == 0 {
		t.Error("expected SaveToken to call UpdatePluginInstanceCredentials on rotation")
	}
	saw := false
	for _, ev := range q.auditEvents {
		if ev.EventType == auditOAuthRefreshed {
			saw = true
		}
	}
	if !saw {
		t.Error("expected plugin_oauth_refreshed audit event on rotation")
	}
}

func TestPersistingTokenSource_InnerErrorMarksRefreshFailed(t *testing.T) {
	q := &fakeOAuthQuerier{
		instance: db.PluginInstance{ID: "inst-1", HealthState: "healthy", Version: 0},
	}
	store := NewDBStore(q, noopEncrypt, noopDecrypt, q, func() time.Time { return time.Unix(1000000, 0) })

	wantErr := errors.New("refresh blew up")
	inner := &fakeInnerSource{err: wantErr}
	ts := newPersistingTS(t, store, "inst-1", inner, nil)

	_, err := ts.Token()
	if err == nil || !errors.Is(err, wantErr) {
		t.Fatalf("expected wrapped wantErr, got %v", err)
	}

	// MarkRefreshFailed should have written an audit event and transitioned health.
	sawAudit := false
	for _, ev := range q.auditEvents {
		if ev.EventType == auditOAuthRefreshFailed {
			sawAudit = true
		}
	}
	if !sawAudit {
		t.Error("expected plugin_oauth_refresh_failed audit event on refresh error")
	}
	if len(q.healthUpdates) == 0 {
		t.Error("expected health update on refresh error")
	}
}

func TestInstanceLocks_PerInstanceMutex(t *testing.T) {
	locks := &instanceLocks{}
	m1a := locks.Get("inst-1")
	m1b := locks.Get("inst-1")
	m2 := locks.Get("inst-2")

	if m1a != m1b {
		t.Error("expected the same mutex for repeated lookups on inst-1")
	}
	if m1a == m2 {
		t.Error("expected distinct mutexes for inst-1 and inst-2")
	}

	// Sanity: concurrent acquisitions on different instances do not block.
	var wg sync.WaitGroup
	wg.Add(2)
	// Each goroutine acquires and releases its own instance lock. The point is
	// that they can run concurrently (different mutexes) — the test would hang
	// if Get returned the same mutex.
	go func() {
		defer wg.Done()
		m1a.Lock()
		_ = "held" //nolint:staticcheck // intentional empty critical section in test
		m1a.Unlock()
	}()
	go func() {
		defer wg.Done()
		m2.Lock()
		_ = "held" //nolint:staticcheck
		m2.Unlock()
	}()
	wg.Wait()
}
