package loader

import (
	"testing"
)

func TestIsValidSemver(t *testing.T) {
	cases := []struct {
		v    string
		want bool
	}{
		{"1.0.0", true},
		{"1.9", true},
		{"1.10.0", true},
		{"0.1.0-alpha", true},
		{"v1.0.0", true},  // already has "v" prefix
		{"", false},
		{"not-semver", false},
		{"1.9-final", false},
		{"invalid-b", false},
	}
	for _, tc := range cases {
		t.Run(tc.v, func(t *testing.T) {
			got := isValidSemver(tc.v)
			if got != tc.want {
				t.Errorf("isValidSemver(%q) = %v, want %v", tc.v, got, tc.want)
			}
		})
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		name    string
		a, b    string
		wantCmp int // sign: -1, 0, +1
	}{
		{
			name:    "semver lexical trap: 1.9 < 1.10",
			a:       "1.9",
			b:       "1.10",
			wantCmp: -1,
		},
		{
			name:    "equal versions",
			a:       "1.0.0",
			b:       "1.0.0",
			wantCmp: 0,
		},
		{
			name:    "2.0.0 > 1.9.9",
			a:       "2.0.0",
			b:       "1.9.9",
			wantCmp: 1,
		},
		{
			name:    "valid beats invalid",
			a:       "1.0.0",
			b:       "not-a-semver",
			wantCmp: 1,
		},
		{
			name:    "invalid loses to valid",
			a:       "not-a-semver",
			b:       "1.0.0",
			wantCmp: -1,
		},
		{
			// Both invalid: result must be the strings.Compare result, NOT 0.
			// A naive semver.Compare wrapper would return 0 for two invalid inputs
			// which would break determinism in the sweep.
			name:    "both invalid: strings.Compare fallback (b < a lexically)",
			a:       "invalid-b",
			b:       "invalid-a",
			wantCmp: 1, // "invalid-b" > "invalid-a" lexicographically
		},
		{
			name:    "both invalid: strings.Compare fallback (a < b lexically)",
			a:       "invalid-a",
			b:       "invalid-b",
			wantCmp: -1,
		},
		{
			name:    "both invalid equal strings",
			a:       "hand-edited",
			b:       "hand-edited",
			wantCmp: 0,
		},
		{
			name:    "v-prefixed vs bare: same version",
			a:       "v1.2.3",
			b:       "1.2.3",
			wantCmp: 0,
		},
		{
			name:    "1.0 < 1.1",
			a:       "1.0",
			b:       "1.1",
			wantCmp: -1,
		},
	}

	sign := func(n int) int {
		if n < 0 {
			return -1
		}
		if n > 0 {
			return 1
		}
		return 0
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sign(compareVersions(tc.a, tc.b))
			if got != tc.wantCmp {
				t.Errorf("compareVersions(%q, %q) sign = %d, want %d", tc.a, tc.b, got, tc.wantCmp)
			}
		})
	}
}
