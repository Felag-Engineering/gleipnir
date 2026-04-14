package trigger

import (
	"crypto/hmac"
	"errors"
	"strings"
)

// BearerPrefix is the expected prefix of Authorization header values for bearer auth.
const BearerPrefix = "Bearer "

var (
	errMissingBearer = errors.New("missing or malformed Authorization: Bearer header")
	errInvalidBearer = errors.New("invalid bearer token")
)

// ValidateBearer verifies that headerValue is an Authorization: Bearer header
// whose token matches expected. Comparison is timing-safe.
//
// Returns errMissingBearer when the header is absent or lacks the "Bearer "
// prefix. Returns errInvalidBearer when the token does not match.
func ValidateBearer(expected, headerValue string) error {
	if headerValue == "" || !strings.HasPrefix(headerValue, BearerPrefix) {
		return errMissingBearer
	}
	token := headerValue[len(BearerPrefix):]
	// hmac.Equal performs constant-time comparison; it requires equal-length
	// slices. When lengths differ it returns false without short-circuiting on
	// the mismatch, so timing is not leaked even for length-mismatched tokens.
	if !hmac.Equal([]byte(token), []byte(expected)) {
		return errInvalidBearer
	}
	return nil
}
