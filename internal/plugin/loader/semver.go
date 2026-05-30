package loader

import (
	"strings"

	"golang.org/x/mod/semver"
)

// isValidSemver reports whether v is a valid semantic version string.
// Manifest versions are bare (e.g. "1.9", "0.1.0"); we normalise by prepending
// "v" because golang.org/x/mod/semver requires the leading "v" prefix.
func isValidSemver(v string) bool {
	return semver.IsValid(normSemver(v))
}

// compareVersions returns -1, 0, or +1 comparing version string a to b.
// It implements a total order across both valid and invalid semver strings:
//
//  1. Both valid semver → semver.Compare (defeats the lexical trap where
//     "1.10" < "1.9" under plain string comparison).
//  2. Exactly one valid → the valid one is greater.
//  3. Both invalid → strings.Compare on the original strings, giving a
//     deterministic fallback for hand-renamed / non-semver tarballs.
//
// Step 3 uses the original (un-normalised) strings so the order is stable
// across repeated sweeps even when semver.Compare would return 0 for two
// different invalid inputs (which would break determinism).
func compareVersions(a, b string) int {
	aValid := isValidSemver(a)
	bValid := isValidSemver(b)

	switch {
	case aValid && bValid:
		return semver.Compare(normSemver(a), normSemver(b))
	case aValid && !bValid:
		return 1
	case !aValid && bValid:
		return -1
	default:
		// Both invalid: fall back to a simple lexical compare so the result is
		// total and deterministic rather than the 0 that semver.Compare would
		// return for two invalid strings.
		return strings.Compare(a, b)
	}
}

// normSemver prepends "v" if v does not already start with one, which is what
// golang.org/x/mod/semver requires. Manifest versions are typically bare like
// "1.9" or "0.1.0".
func normSemver(v string) string {
	if strings.HasPrefix(v, "v") {
		return v
	}
	return "v" + v
}
