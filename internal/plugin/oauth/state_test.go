package oauth

import (
	"testing"
	"time"
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

	store.Record("nonce-abc")

	if !store.Consume("nonce-abc") {
		t.Fatal("expected first Consume to return true")
	}
	if store.Consume("nonce-abc") {
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

	if store.Consume("unknown-nonce") {
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
	store.Record("old-nonce")

	// Advance past expiry before consuming.
	store.clock = func() time.Time { return baseTime.Add(stateHMACExpiry + time.Second) }

	if store.Consume("old-nonce") {
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
