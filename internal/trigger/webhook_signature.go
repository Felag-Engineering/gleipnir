package trigger

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

// SignatureHeader is the HTTP header name that carries the HMAC-SHA256 signature.
const SignatureHeader = "X-Gleipnir-Signature"

const signaturePrefix = "sha256="

var (
	errMissingSignature   = errors.New("missing signature header")
	errMalformedSignature = errors.New("malformed signature: expected sha256= prefix")
	errInvalidSignature   = errors.New("invalid signature")
)

// ValidateSignature verifies that signatureHeader matches the HMAC-SHA256 of body
// computed with secret. Returns nil on success.
//
// The expected header format is "sha256=<hex>". Comparison is timing-safe.
func ValidateSignature(secret string, body []byte, signatureHeader string) error {
	if signatureHeader == "" {
		return errMissingSignature
	}
	if !strings.HasPrefix(signatureHeader, signaturePrefix) {
		return errMalformedSignature
	}

	received := signatureHeader[len(signaturePrefix):]

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(expected), []byte(received)) {
		return errInvalidSignature
	}
	return nil
}
