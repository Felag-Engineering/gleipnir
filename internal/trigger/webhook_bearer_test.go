package trigger

import (
	"errors"
	"testing"
)

func TestValidateBearer(t *testing.T) {
	const secret = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	cases := []struct {
		name        string
		expected    string
		headerValue string
		wantErr     error
	}{
		{
			name:        "valid token",
			expected:    secret,
			headerValue: "Bearer " + secret,
			wantErr:     nil,
		},
		{
			name:        "wrong token",
			expected:    secret,
			headerValue: "Bearer wrongtoken",
			wantErr:     errInvalidBearer,
		},
		{
			name:        "missing header",
			expected:    secret,
			headerValue: "",
			wantErr:     errMissingBearer,
		},
		{
			name:        "no Bearer prefix",
			expected:    secret,
			headerValue: "Token " + secret,
			wantErr:     errMissingBearer,
		},
		{
			name:        "Basic auth header",
			expected:    secret,
			headerValue: "Basic dXNlcjpwYXNz",
			wantErr:     errMissingBearer,
		},
		{
			name:        "empty token after prefix",
			expected:    secret,
			headerValue: "Bearer ",
			wantErr:     errInvalidBearer,
		},
		{
			name:        "Bearer with different case prefix",
			expected:    secret,
			headerValue: "bearer " + secret,
			wantErr:     errMissingBearer, // case-sensitive
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateBearer(tc.expected, tc.headerValue)
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("ValidateBearer() = %v, want %v", err, tc.wantErr)
			}
		})
	}
}
