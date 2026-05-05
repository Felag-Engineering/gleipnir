package plugin

import (
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/felag-engineering/gleipnir/plugin-sdk/signing"
)

// signedBundle writes a signed plugin bundle to a fresh temp directory and
// returns (bundleDir, binaryPath). The binary is the byte slice "binary
// content"; the manifest is "manifest content". The bundle is signed with
// a freshly-generated keypair.
//
// The test helper exists to keep individual tests focused on what they
// mutate (binary, manifest, files present). Each call generates a fresh
// keypair so tests cannot leak state through it.
func signedBundle(t *testing.T) (bundleDir, binaryPath string) {
	t.Helper()
	bundleDir = t.TempDir()
	binaryPath = filepath.Join(bundleDir, "plugin-bin")

	binaryBytes := []byte("binary content")
	manifestBytes := []byte("manifest content")

	if err := os.WriteFile(binaryPath, binaryBytes, 0o755); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bundleDir, "manifest.yaml"), manifestBytes, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	pk, sk, err := signing.GenerateKeypair(rand.Reader)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}

	if err := os.WriteFile(filepath.Join(bundleDir, "signing.pub"),
		signing.MarshalPublicKey(pk, "test pubkey"), 0o644); err != nil {
		t.Fatalf("write signing.pub: %v", err)
	}

	payload := signing.PluginPayload(binaryBytes, manifestBytes)
	sig, err := signing.Sign(sk.SecretKey, sk.KeyID, payload, "trusted comment")
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	if err := os.WriteFile(filepath.Join(bundleDir, "plugin.minisig"),
		signing.MarshalSignature(sig, "test sig"), 0o644); err != nil {
		t.Fatalf("write .minisig: %v", err)
	}

	return bundleDir, binaryPath
}

func TestVerifier_Signed_Verifies(t *testing.T) {
	bundleDir, binaryPath := signedBundle(t)
	v := &Verifier{}

	got := v.VerifyBundle(bundleDir, binaryPath)
	if got.Outcome != OutcomeVerified {
		t.Fatalf("Outcome = %v (err=%v), want %v", got.Outcome, got.Err, OutcomeVerified)
	}
	if len(got.Pubkey) == 0 {
		t.Errorf("Pubkey: got empty, want signing.pub bytes")
	}
}

func TestVerifier_MutatedBinary_Rejected(t *testing.T) {
	bundleDir, binaryPath := signedBundle(t)

	if err := os.WriteFile(binaryPath, []byte("tampered"), 0o755); err != nil {
		t.Fatalf("rewrite binary: %v", err)
	}

	v := &Verifier{}
	got := v.VerifyBundle(bundleDir, binaryPath)
	if got.Outcome != OutcomeRejected {
		t.Fatalf("Outcome = %v, want %v", got.Outcome, OutcomeRejected)
	}
	if got.Err == nil {
		t.Errorf("Err: got nil, want explanation")
	}
}

func TestVerifier_MutatedManifest_Rejected(t *testing.T) {
	bundleDir, binaryPath := signedBundle(t)

	if err := os.WriteFile(filepath.Join(bundleDir, "manifest.yaml"), []byte("tampered"), 0o644); err != nil {
		t.Fatalf("rewrite manifest: %v", err)
	}

	v := &Verifier{}
	got := v.VerifyBundle(bundleDir, binaryPath)
	if got.Outcome != OutcomeRejected {
		t.Fatalf("Outcome = %v, want %v", got.Outcome, OutcomeRejected)
	}
}

func TestVerifier_Unsigned_RejectedByDefault(t *testing.T) {
	bundleDir, binaryPath := signedBundle(t)

	// Strip both signing artifacts.
	if err := os.Remove(filepath.Join(bundleDir, "signing.pub")); err != nil {
		t.Fatalf("rm signing.pub: %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(bundleDir, "*.minisig"))
	for _, m := range matches {
		_ = os.Remove(m)
	}

	v := &Verifier{}
	got := v.VerifyBundle(bundleDir, binaryPath)
	if got.Outcome != OutcomeRejected {
		t.Fatalf("Outcome = %v, want %v", got.Outcome, OutcomeRejected)
	}
}

func TestVerifier_Unsigned_AllowedInPermissiveMode(t *testing.T) {
	bundleDir, binaryPath := signedBundle(t)

	if err := os.Remove(filepath.Join(bundleDir, "signing.pub")); err != nil {
		t.Fatalf("rm signing.pub: %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(bundleDir, "*.minisig"))
	for _, m := range matches {
		_ = os.Remove(m)
	}

	v := &Verifier{AllowUnsigned: true}
	got := v.VerifyBundle(bundleDir, binaryPath)
	if got.Outcome != OutcomeUnsignedPermissive {
		t.Fatalf("Outcome = %v (err=%v), want %v", got.Outcome, got.Err, OutcomeUnsignedPermissive)
	}
}

// Permissive mode does NOT relax verification of bundles that DO carry a
// signature. A tampered signed bundle is still rejected (ADR-045 §6 last bullet).
func TestVerifier_Permissive_DoesNotSkipVerificationOfSigned(t *testing.T) {
	bundleDir, binaryPath := signedBundle(t)

	if err := os.WriteFile(binaryPath, []byte("tampered"), 0o755); err != nil {
		t.Fatalf("rewrite binary: %v", err)
	}

	v := &Verifier{AllowUnsigned: true}
	got := v.VerifyBundle(bundleDir, binaryPath)
	if got.Outcome != OutcomeRejected {
		t.Fatalf("Outcome = %v, want %v (signed bundles must verify even in permissive mode)", got.Outcome, OutcomeRejected)
	}
}

func TestVerifier_HalfSigned_OnlyPubkey_Rejected(t *testing.T) {
	bundleDir, binaryPath := signedBundle(t)

	matches, _ := filepath.Glob(filepath.Join(bundleDir, "*.minisig"))
	for _, m := range matches {
		_ = os.Remove(m)
	}

	v := &Verifier{AllowUnsigned: true} // even permissive: half-signed is malformed
	got := v.VerifyBundle(bundleDir, binaryPath)
	if got.Outcome != OutcomeRejected {
		t.Fatalf("Outcome = %v, want %v", got.Outcome, OutcomeRejected)
	}
}

func TestVerifier_HalfSigned_OnlyMinisig_Rejected(t *testing.T) {
	bundleDir, binaryPath := signedBundle(t)

	if err := os.Remove(filepath.Join(bundleDir, "signing.pub")); err != nil {
		t.Fatalf("rm signing.pub: %v", err)
	}

	v := &Verifier{AllowUnsigned: true}
	got := v.VerifyBundle(bundleDir, binaryPath)
	if got.Outcome != OutcomeRejected {
		t.Fatalf("Outcome = %v, want %v", got.Outcome, OutcomeRejected)
	}
}

func TestVerifier_MissingManifest_Rejected(t *testing.T) {
	bundleDir, binaryPath := signedBundle(t)

	if err := os.Remove(filepath.Join(bundleDir, "manifest.yaml")); err != nil {
		t.Fatalf("rm manifest: %v", err)
	}

	v := &Verifier{}
	got := v.VerifyBundle(bundleDir, binaryPath)
	if got.Outcome != OutcomeRejected {
		t.Fatalf("Outcome = %v, want %v", got.Outcome, OutcomeRejected)
	}
}

func TestVerifier_TwoMinisigFiles_Rejected(t *testing.T) {
	bundleDir, binaryPath := signedBundle(t)

	// Add a stray .minisig — bundle layout guarantees exactly one.
	if err := os.WriteFile(filepath.Join(bundleDir, "extra.minisig"), []byte("stray"), 0o644); err != nil {
		t.Fatalf("write extra minisig: %v", err)
	}

	v := &Verifier{}
	got := v.VerifyBundle(bundleDir, binaryPath)
	if got.Outcome != OutcomeRejected {
		t.Fatalf("Outcome = %v, want %v", got.Outcome, OutcomeRejected)
	}
}

// Sanity check: the host verifier consumes the same signing-package
// fixtures that prove upstream-Minisign-CLI compatibility (see
// plugin-sdk/signing/interop_test.go). If this test ever fails while the
// signing-package suite passes, the host verifier has drifted from the
// shared format.
func TestVerifier_SmokeAgainstSigningPackage(t *testing.T) {
	// Build a bundle and verify it round-trips through both signing.Verify
	// (which the host verifier ultimately calls) and a direct call —
	// cheap insurance against future refactors that bypass signing.Verify.
	bundleDir, binaryPath := signedBundle(t)

	v := &Verifier{}
	got := v.VerifyBundle(bundleDir, binaryPath)
	if got.Outcome != OutcomeVerified {
		t.Fatalf("verifier rejected fresh bundle: %v", got.Err)
	}

	pubBytes, err := os.ReadFile(filepath.Join(bundleDir, "signing.pub"))
	if err != nil {
		t.Fatalf("read pubkey: %v", err)
	}
	pk, _, err := signing.ParsePublicKey(pubBytes)
	if err != nil {
		t.Fatalf("parse pubkey: %v", err)
	}

	matches, _ := filepath.Glob(filepath.Join(bundleDir, "*.minisig"))
	if len(matches) != 1 {
		t.Fatalf("expected 1 minisig file, got %d", len(matches))
	}
	sigBytes, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read sig: %v", err)
	}
	sig, _, err := signing.ParseSignature(sigBytes)
	if err != nil {
		t.Fatalf("parse sig: %v", err)
	}

	binaryBytes, _ := os.ReadFile(binaryPath)
	manifestBytes, _ := os.ReadFile(filepath.Join(bundleDir, "manifest.yaml"))
	payload := signing.PluginPayload(binaryBytes, manifestBytes)
	if err := signing.Verify(pk, payload, sig, sig.TrustedComment); err != nil {
		t.Fatalf("direct signing.Verify: %v", err)
	}
}

// Ensure os.ErrNotExist propagates when a caller passes a binary path that
// doesn't exist; the verifier should reject cleanly rather than panic.
func TestVerifier_MissingBinary_Rejected(t *testing.T) {
	bundleDir, _ := signedBundle(t)

	v := &Verifier{}
	got := v.VerifyBundle(bundleDir, filepath.Join(bundleDir, "does-not-exist"))
	if got.Outcome != OutcomeRejected {
		t.Fatalf("Outcome = %v, want %v", got.Outcome, OutcomeRejected)
	}
	if !errors.Is(got.Err, os.ErrNotExist) {
		t.Errorf("Err = %v; want errors.Is(.., os.ErrNotExist) true", got.Err)
	}
}
