package oauth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/crypto/hkdf"
)

// ErrStateExpired is returned when an OAuth callback state envelope has passed
// its 10-minute TTL.
var ErrStateExpired = errors.New("oauth state: envelope expired")

// ErrStateTampered is returned when the HMAC signature on the state envelope
// does not match.
var ErrStateTampered = errors.New("oauth state: HMAC verification failed")

// ErrNonceUsed is returned when a nonce has already been consumed — either a
// replay attack or the flow was submitted twice.
var ErrNonceUsed = errors.New("oauth state: nonce already used or expired")

// stateHMACExpiry is the maximum lifetime of a signed state envelope.
const stateHMACExpiry = 10 * time.Minute

// StateEnvelope is the signed payload attached to the OAuth2 authorization URL
// as the ?state= query parameter (spec §9.2). The HMAC signature prevents CSRF
// and ensures integrity; the nonce prevents replay.
type StateEnvelope struct {
	InstanceID string `json:"instance_id"`
	Nonce      string `json:"nonce"`
	ExpiresAt  int64  `json:"expires_at"` // Unix seconds
	ReturnURL  string `json:"return_url"`
}

// EncodeState serialises and HMAC-signs the envelope. The returned string is
// safe to embed in a URL query parameter (base64-URL encoded).
func EncodeState(env StateEnvelope, key []byte) (string, error) {
	payload, err := json.Marshal(env)
	if err != nil {
		return "", fmt.Errorf("oauth state: marshal: %w", err)
	}
	sig := computeHMAC(payload, key)
	combined := append(payload, '|') //nolint:gocritic // intentional separator
	combined = append(combined, []byte(sig)...)
	return base64.URLEncoding.EncodeToString(combined), nil
}

// DecodeState decodes and verifies a state string produced by EncodeState.
// Returns ErrStateTampered when the HMAC does not match, ErrStateExpired when
// the envelope is past its TTL.
func DecodeState(encoded string, key []byte, clock func() time.Time) (StateEnvelope, error) {
	raw, err := base64.URLEncoding.DecodeString(encoded)
	if err != nil {
		return StateEnvelope{}, fmt.Errorf("oauth state: base64 decode: %w", err)
	}

	// Split at the last '|' to separate JSON payload from hex HMAC.
	sep := lastIndexByte(raw, '|')
	if sep < 0 {
		return StateEnvelope{}, ErrStateTampered
	}
	payload := raw[:sep]
	receivedSig := string(raw[sep+1:])

	expected := computeHMAC(payload, key)
	if !hmac.Equal([]byte(receivedSig), []byte(expected)) {
		return StateEnvelope{}, ErrStateTampered
	}

	var env StateEnvelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return StateEnvelope{}, fmt.Errorf("oauth state: unmarshal: %w", err)
	}

	if clock().Unix() > env.ExpiresAt {
		return StateEnvelope{}, ErrStateExpired
	}

	return env, nil
}

// computeHMAC returns a lowercase hex-encoded HMAC-SHA256 of payload.
func computeHMAC(payload, key []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

// lastIndexByte returns the index of the last occurrence of b in s, or -1.
func lastIndexByte(s []byte, b byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// DeriveHMACKey derives a 32-byte HMAC key from the host's AES-256 encryption
// key using HKDF-SHA256 with the info string "gleipnir-oauth-state-v1". This
// ensures the OAuth state key is independent of the AES-GCM key even though
// both are derived from the same master secret.
func DeriveHMACKey(encKey []byte) []byte {
	r := hkdf.New(sha256.New, encKey, nil, []byte("gleipnir-oauth-state-v1"))
	out := make([]byte, 32)
	if _, err := r.Read(out); err != nil {
		// hkdf.Read over sha256 always succeeds for reasonable output lengths.
		panic(fmt.Sprintf("oauth: HKDF derive failed: %v", err))
	}
	return out
}

// generateNonce returns a 32-byte random nonce encoded as base64-URL.
func generateNonce() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("oauth state: generate nonce: %w", err)
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

// MemoryNonceStore is an in-memory nonce registry. Each nonce is single-use:
// Consume removes it so replayed callbacks are rejected. A background janitor
// prunes expired entries every minute so the map does not grow without bound
// across many flows.
//
// Server restart mid-dance invalidates in-progress flows; this is documented
// as acceptable (operators simply re-click "Authorize").
type MemoryNonceStore struct {
	mu      sync.Mutex
	entries map[string]time.Time // nonce → expiry
	clock   func() time.Time
}

// NewMemoryNonceStore returns a started MemoryNonceStore. The caller must not
// call Stop on the returned store — the janitor runs until the process exits.
// Pass time.Now as clock in production; tests may substitute a fake clock.
func NewMemoryNonceStore(clock func() time.Time) *MemoryNonceStore {
	s := &MemoryNonceStore{
		entries: make(map[string]time.Time),
		clock:   clock,
	}
	go s.janitor()
	return s
}

// Record registers nonce with a 10-minute TTL.
func (s *MemoryNonceStore) Record(nonce string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[nonce] = s.clock().Add(stateHMACExpiry)
}

// Consume atomically checks and removes nonce. Returns true if the nonce was
// present and not yet expired; false otherwise (expired or already consumed).
func (s *MemoryNonceStore) Consume(nonce string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.entries[nonce]
	if !ok {
		return false
	}
	delete(s.entries, nonce)
	return s.clock().Before(exp)
}

// janitor prunes expired entries every minute. Runs forever; expected to be
// started as a goroutine.
func (s *MemoryNonceStore) janitor() {
	for {
		// Sleep outside the lock so other operations can proceed.
		time.Sleep(time.Minute)
		now := s.clock()
		s.mu.Lock()
		for nonce, exp := range s.entries {
			if now.After(exp) {
				delete(s.entries, nonce)
			}
		}
		s.mu.Unlock()
	}
}

// NewStateEnvelope constructs a StateEnvelope with a fresh nonce and 10-minute
// expiry. The returned nonce should be passed to MemoryNonceStore.Record before
// the authorize URL is returned to the caller.
func NewStateEnvelope(instanceID, returnURL string, clock func() time.Time) (StateEnvelope, string, error) {
	nonce, err := generateNonce()
	if err != nil {
		return StateEnvelope{}, "", err
	}
	env := StateEnvelope{
		InstanceID: instanceID,
		Nonce:      nonce,
		ExpiresAt:  clock().Add(stateHMACExpiry).Unix(),
		ReturnURL:  returnURL,
	}
	return env, nonce, nil
}

