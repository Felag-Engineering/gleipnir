package trigger

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
)

// computeSignature is a test helper that produces the correct sha256= header value
// for the given secret and body.
func computeSignature(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return signaturePrefix + hex.EncodeToString(mac.Sum(nil))
}

func TestValidateSignature(t *testing.T) {
	secret := "test-secret-key-must-be-at-least-32-bytes-long"
	body := []byte(`{"event":"push"}`)
	validSig := computeSignature(secret, body)

	cases := []struct {
		name            string
		secret          string
		body            []byte
		signatureHeader string
		wantErr         error
	}{
		{
			name:            "valid signature",
			secret:          secret,
			body:            body,
			signatureHeader: validSig,
			wantErr:         nil,
		},
		{
			name:            "invalid signature (wrong hex)",
			secret:          secret,
			body:            body,
			signatureHeader: "sha256=deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
			wantErr:         errInvalidSignature,
		},
		{
			name:            "missing header",
			secret:          secret,
			body:            body,
			signatureHeader: "",
			wantErr:         errMissingSignature,
		},
		{
			name:            "malformed header (no sha256= prefix)",
			secret:          secret,
			body:            body,
			signatureHeader: "abcdef1234567890",
			wantErr:         errMalformedSignature,
		},
		{
			name:            "wrong secret",
			secret:          "wrong-secret-key-must-be-at-least-32-bytes-long",
			body:            body,
			signatureHeader: validSig,
			wantErr:         errInvalidSignature,
		},
		{
			name:            "empty body with valid signature",
			secret:          secret,
			body:            []byte{},
			signatureHeader: computeSignature(secret, []byte{}),
			wantErr:         nil,
		},
		{
			name:            "wrong hex length (shorter than expected)",
			secret:          secret,
			body:            body,
			signatureHeader: "sha256=deadbeef",
			wantErr:         errInvalidSignature,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateSignature(tc.secret, tc.body, tc.signatureHeader)
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("ValidateSignature() = %v, want %v", err, tc.wantErr)
			}
		})
	}
}
