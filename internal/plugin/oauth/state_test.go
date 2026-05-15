package oauth

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
)

// Tests that swap the clock variable must NOT run with t.Parallel().
// See CLAUDE.md: "tests that mutate a shared package-level clock variable".

func fixedKey() []byte { return DeriveHMACKey(make([]byte, 32)) }

func TestEncodeDecodeState_RoundTrip(t *testing.T) {
	key := fixedKey()
	clock := func() time.Time { return time.Unix(1000000, 0) }

	env, nonce, err := NewStateEnvelope("inst-1", "https://example.com/return", clock)
	if err != nil {
		t.Fatalf("NewStateEnvelope: %v", err)
	}
	if nonce == "" {
		t.Fatal("expected non-empty nonce")
	}

	encoded, err := EncodeState(env, key)
	if err != nil {
		t.Fatalf("EncodeState: %v", err)
	}

	decoded, err := DecodeState(encoded, key, clock)
	if err != nil {
		t.Fatalf("DecodeState: %v", err)
	}

	if decoded.InstanceID != env.InstanceID {
		t.Errorf("InstanceID: got %q, want %q", decoded.InstanceID, env.InstanceID)
	}
	if decoded.Nonce != env.Nonce {
		t.Errorf("Nonce: got %q, want %q", decoded.Nonce, env.Nonce)
	}
	if decoded.ReturnURL != env.ReturnURL {
		t.Errorf("ReturnURL: got %q, want %q", decoded.ReturnURL, env.ReturnURL)
	}
}

func TestDecodeState_ExpiredEnvelope(t *testing.T) {
	key := fixedKey()
	baseTime := time.Unix(1000000, 0)
	clock := func() time.Time { return baseTime }

	env, _, err := NewStateEnvelope("inst-1", "https://example.com/return", clock)
	if err != nil {
		t.Fatalf("NewStateEnvelope: %v", err)
	}

	encoded, err := EncodeState(env, key)
	if err != nil {
		t.Fatalf("EncodeState: %v", err)
	}

	// Advance clock past expiry (10 minutes + 1 second).
	futureClock := func() time.Time { return baseTime.Add(stateHMACExpiry + time.Second) }

	_, err = DecodeState(encoded, key, futureClock)
	if err != ErrStateExpired {
		t.Errorf("expected ErrStateExpired, got %v", err)
	}
}

func TestDecodeState_TamperedHMAC(t *testing.T) {
	key := fixedKey()
	clock := func() time.Time { return time.Unix(1000000, 0) }

	env, _, err := NewStateEnvelope("inst-1", "https://example.com/return", clock)
	if err != nil {
		t.Fatalf("NewStateEnvelope: %v", err)
	}

	encoded, err := EncodeState(env, key)
	if err != nil {
		t.Fatalf("EncodeState: %v", err)
	}

	// Flip the last character of the encoded state to tamper the HMAC.
	tampered := []byte(encoded)
	tampered[len(tampered)-1] ^= 0xFF
	_, err = DecodeState(string(tampered), key, clock)
	if err == nil {
		t.Fatal("expected error for tampered state, got nil")
	}
	// Could be base64 error or HMAC mismatch.
}

func TestDecodeState_WrongKey(t *testing.T) {
	key := fixedKey()
	wrongKey := DeriveHMACKey(make([]byte, 32))
	wrongKey[0] ^= 0xFF // differentiate from key

	clock := func() time.Time { return time.Unix(1000000, 0) }

	env, _, err := NewStateEnvelope("inst-1", "https://example.com/return", clock)
	if err != nil {
		t.Fatalf("NewStateEnvelope: %v", err)
	}

	encoded, err := EncodeState(env, key)
	if err != nil {
		t.Fatalf("EncodeState: %v", err)
	}

	_, err = DecodeState(encoded, wrongKey, clock)
	if err != ErrStateTampered {
		t.Errorf("expected ErrStateTampered with wrong key, got %v", err)
	}
}

func TestMemoryNonceStore_SingleUse(t *testing.T) {
	baseTime := time.Unix(1000000, 0)
	clock := func() time.Time { return baseTime }

	store := &MemoryNonceStore{
		entries: make(map[string]time.Time),
		clock:   clock,
	}
	// Do not use NewMemoryNonceStore here — it starts a goroutine that
	// interferes with test cleanup.

	ctx := context.Background()
	if err := store.Record(ctx, "nonce-abc", "inst-1"); err != nil {
		t.Fatalf("Record: %v", err)
	}

	ok, err := store.Consume(ctx, "nonce-abc")
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if !ok {
		t.Fatal("expected first Consume to return true")
	}
	ok, err = store.Consume(ctx, "nonce-abc")
	if err != nil {
		t.Fatalf("second Consume: %v", err)
	}
	if ok {
		t.Fatal("expected second Consume to return false (already consumed)")
	}
}

func TestMemoryNonceStore_UnknownNonce(t *testing.T) {
	baseTime := time.Unix(1000000, 0)
	clock := func() time.Time { return baseTime }

	store := &MemoryNonceStore{
		entries: make(map[string]time.Time),
		clock:   clock,
	}

	ok, err := store.Consume(context.Background(), "unknown-nonce")
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if ok {
		t.Fatal("expected Consume of unknown nonce to return false")
	}
}

func TestMemoryNonceStore_ExpiredNonce(t *testing.T) {
	baseTime := time.Unix(1000000, 0)
	clock := func() time.Time { return baseTime }

	store := &MemoryNonceStore{
		entries: make(map[string]time.Time),
		clock:   clock,
	}
	ctx := context.Background()
	if err := store.Record(ctx, "old-nonce", "inst-1"); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// Advance past expiry before consuming.
	store.clock = func() time.Time { return baseTime.Add(stateHMACExpiry + time.Second) }

	ok, err := store.Consume(ctx, "old-nonce")
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if ok {
		t.Fatal("expected expired nonce Consume to return false")
	}
}

func TestDeriveHMACKey_Deterministic(t *testing.T) {
	enc := make([]byte, 32)
	k1 := DeriveHMACKey(enc)
	k2 := DeriveHMACKey(enc)
	if len(k1) != 32 {
		t.Errorf("expected 32-byte key, got %d", len(k1))
	}
	for i := range k1 {
		if k1[i] != k2[i] {
			t.Errorf("key not deterministic at byte %d", i)
		}
	}
}

func TestDeriveHMACKey_DifferentFromInput(t *testing.T) {
	enc := make([]byte, 32)
	k := DeriveHMACKey(enc)
	// Should not be all zeros even though input is all zeros.
	allZero := true
	for _, b := range k {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Error("derived key is all zeros; HKDF may not be working")
	}
}

// --- fakeNonceQuerier ---

// fakeNonceQuerier is a map-backed in-memory implementation of NonceQuerier
// for use in DBNonceStore unit tests.
type fakeNonceQuerier struct {
	mu   sync.Mutex
	rows map[string]db.ConsumePluginOAuthNonceRow // nonce → (instance_id, expires_at)
}

func newFakeNonceQuerier() *fakeNonceQuerier {
	return &fakeNonceQuerier{rows: make(map[string]db.ConsumePluginOAuthNonceRow)}
}

func (f *fakeNonceQuerier) InsertPluginOAuthNonce(_ context.Context, arg db.InsertPluginOAuthNonceParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows[arg.Nonce] = db.ConsumePluginOAuthNonceRow{InstanceID: arg.InstanceID, ExpiresAt: arg.ExpiresAt}
	return nil
}

func (f *fakeNonceQuerier) ConsumePluginOAuthNonce(_ context.Context, nonce string) (db.ConsumePluginOAuthNonceRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[nonce]
	if !ok {
		return db.ConsumePluginOAuthNonceRow{}, sql.ErrNoRows
	}
	delete(f.rows, nonce)
	return row, nil
}

func (f *fakeNonceQuerier) PrunePluginOAuthNonces(_ context.Context, cutoff string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for nonce, row := range f.rows {
		if row.ExpiresAt < cutoff {
			delete(f.rows, nonce)
		}
	}
	return nil
}

// --- DBNonceStore tests ---

func TestDBNonceStore_RecordAndConsume_Once(t *testing.T) {
	baseTime := time.Unix(1000000, 0)
	clock := func() time.Time { return baseTime }

	store := NewDBNonceStore(newFakeNonceQuerier(), clock)
	ctx := context.Background()

	if err := store.Record(ctx, "nonce-1", "inst-1"); err != nil {
		t.Fatalf("Record: %v", err)
	}

	ok, err := store.Consume(ctx, "nonce-1")
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if !ok {
		t.Fatal("expected first Consume to return true")
	}

	// Second consume must return false (nonce already deleted).
	ok, err = store.Consume(ctx, "nonce-1")
	if err != nil {
		t.Fatalf("second Consume: %v", err)
	}
	if ok {
		t.Fatal("expected second Consume to return false (already consumed)")
	}
}

func TestDBNonceStore_Consume_Unknown_ReturnsFalse(t *testing.T) {
	baseTime := time.Unix(1000000, 0)
	clock := func() time.Time { return baseTime }

	store := NewDBNonceStore(newFakeNonceQuerier(), clock)
	ctx := context.Background()

	ok, err := store.Consume(ctx, "no-such-nonce")
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if ok {
		t.Fatal("expected false for unknown nonce")
	}
}

func TestDBNonceStore_Consume_Expired_ReturnsFalse(t *testing.T) {
	baseTime := time.Unix(1000000, 0)
	clock := func() time.Time { return baseTime }

	store := NewDBNonceStore(newFakeNonceQuerier(), clock)
	ctx := context.Background()

	if err := store.Record(ctx, "old-nonce", "inst-1"); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// Advance clock past expiry.
	store.clock = func() time.Time { return baseTime.Add(stateHMACExpiry + time.Second) }

	ok, err := store.Consume(ctx, "old-nonce")
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if ok {
		t.Fatal("expected false for expired nonce")
	}
}

func TestDBNonceStore_Prune_RemovesExpiredOnly(t *testing.T) {
	baseTime := time.Unix(1000000, 0)

	q := newFakeNonceQuerier()

	// Insert one fresh and one already-expired nonce directly into the fake querier.
	expiredAt := baseTime.Add(-time.Second).UTC().Format(time.RFC3339Nano)
	freshAt := baseTime.Add(stateHMACExpiry).UTC().Format(time.RFC3339Nano)
	_ = q.InsertPluginOAuthNonce(context.Background(), db.InsertPluginOAuthNonceParams{
		Nonce: "expired-nonce", InstanceID: "inst-1", ExpiresAt: expiredAt, CreatedAt: expiredAt,
	})
	_ = q.InsertPluginOAuthNonce(context.Background(), db.InsertPluginOAuthNonceParams{
		Nonce: "fresh-nonce", InstanceID: "inst-1", ExpiresAt: freshAt, CreatedAt: freshAt,
	})

	store := NewDBNonceStore(q, func() time.Time { return baseTime })
	if err := store.Prune(context.Background()); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	// Expired nonce must be gone.
	ok, err := store.Consume(context.Background(), "expired-nonce")
	if err != nil {
		t.Fatalf("Consume expired: %v", err)
	}
	if ok {
		t.Error("expected expired nonce to be pruned")
	}

	// Fresh nonce must still be present.
	ok, err = store.Consume(context.Background(), "fresh-nonce")
	if err != nil {
		t.Fatalf("Consume fresh: %v", err)
	}
	if !ok {
		t.Error("expected fresh nonce to survive prune")
	}
}
