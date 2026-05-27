package hostwire

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// TestSafeEnv_ExcludesSecrets verifies that safeEnv never passes host secrets
// to plugin subprocesses. This is a regression test for the vulnerability where
// os.Environ() was passed directly, leaking GLEIPNIR_ENCRYPTION_KEY.
func TestSafeEnv_ExcludesSecrets(t *testing.T) {
	secrets := []string{
		"GLEIPNIR_ENCRYPTION_KEY",
		"GLEIPNIR_DB_PATH",
		"ANTHROPIC_API_KEY",
		"OPENAI_API_KEY",
		"GOOGLE_API_KEY",
	}

	// Set each secret in the process environment, then verify safeEnv omits it.
	for _, key := range secrets {
		t.Setenv(key, "should-not-be-passed-to-plugin")
	}

	env := safeEnv()

	for _, entry := range env {
		for _, secret := range secrets {
			if strings.HasPrefix(entry, secret+"=") {
				t.Errorf("safeEnv included secret var %s; plugin subprocess would receive the host encryption key", secret)
			}
		}
	}
}

// TestSafeEnv_IncludesAllowlistedVars verifies that safeEnv passes through the
// system vars a plugin needs to function (PATH, HOME, etc.).
func TestSafeEnv_IncludesAllowlistedVars(t *testing.T) {
	t.Setenv("PATH", "/usr/local/bin:/usr/bin:/bin")
	t.Setenv("HOME", "/home/testuser")
	t.Setenv("TZ", "UTC")

	env := safeEnv()

	check := func(key, want string) {
		t.Helper()
		prefix := key + "="
		for _, entry := range env {
			if strings.HasPrefix(entry, prefix) {
				got := strings.TrimPrefix(entry, prefix)
				if got != want {
					t.Errorf("safeEnv: %s = %q, want %q", key, got, want)
				}
				return
			}
		}
		t.Errorf("safeEnv: %s not present in result; plugin subprocess would not have it", key)
	}

	check("PATH", "/usr/local/bin:/usr/bin:/bin")
	check("HOME", "/home/testuser")
	check("TZ", "UTC")
}

// TestSafeEnv_SkipsMissingKeys verifies that safeEnv silently omits allowlisted
// keys that are not set in the host environment rather than adding them as
// empty-value entries or panicking. A key that does not appear in os.Environ()
// at all should not appear in the result.
func TestSafeEnv_SkipsMissingKeys(t *testing.T) {
	// Verify that LC_ALL — an allowlisted key that is rarely set in CI — is
	// absent from the result when it is absent from the process environment.
	// We look at the raw safeEnv result and check no entry has a key that
	// was not actually set in the process environment.
	env := safeEnv()

	// Build a set of keys actually present in the process environment.
	processKeys := make(map[string]bool)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		processKeys[key] = true
	}

	// Every key in the safeEnv result must have come from the actual process env.
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if !processKeys[key] {
			t.Errorf("safeEnv included key %q that was not in os.Environ()", key)
		}
	}
}

// TestSafeEnv_NoDuplicates verifies that safeEnv doesn't produce duplicate
// entries even if os.Environ() contains duplicates (which is technically
// possible on some platforms).
func TestSafeEnv_NoDuplicates(t *testing.T) {
	t.Setenv("PATH", "/bin")

	env := safeEnv()

	seen := make(map[string]bool)
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if seen[key] {
			t.Errorf("safeEnv produced duplicate entry for key %q", key)
		}
		seen[key] = true
	}
}

// TestSafeEnvKeys_AllowlistIsComplete verifies that the allowlist contains
// only well-known system vars and has no accidental GLEIPNIR_ entries.
func TestSafeEnvKeys_AllowlistIsComplete(t *testing.T) {
	for _, key := range safeEnvKeys {
		if strings.HasPrefix(key, "GLEIPNIR_") {
			t.Errorf("safeEnvKeys contains %q — Gleipnir vars must be injected via opts.Env, not the allowlist", key)
		}
		// Sanity: every entry should be a non-empty identifier.
		if key == "" {
			t.Error("safeEnvKeys contains an empty string")
		}
		if strings.Contains(key, "=") {
			t.Errorf("safeEnvKeys entry %q contains '=' — should be just the key name", key)
		}
	}

	// PATH must always be present (fundamental for subprocess execution).
	if !slices.Contains(safeEnvKeys, "PATH") {
		t.Error("safeEnvKeys does not contain PATH; plugin subprocesses could not resolve tool binaries")
	}
}
