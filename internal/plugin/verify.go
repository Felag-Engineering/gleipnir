package plugin

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/felag-engineering/gleipnir/internal/plugin/loader"
	"github.com/felag-engineering/gleipnir/plugin-sdk/signing"
)

// Honest framing (ADR-045 §1):
//
// Minisign verification proves a plugin bundle was signed by the holder of
// the captured Ed25519 private key. It does NOT prove the holder is the
// named author, that the binary does what the manifest says it does, or
// that the author is who they claim to be. v1 buys *tamper-evidence between
// admin install and admin run* — a meaningful security property, but a
// narrower one than a curated registry with an identity layer would
// provide.

// Bundle layout convention (spec §5.1, §14.5):
//
// A plugin bundle on disk is a directory containing:
//   - the plugin binary (executable, name from manifest)
//   - manifest.yaml
//   - <manifest.Name>.minisig            — signature; absent => unsigned
//   - signing.pub                        — embedded pubkey; absent => unsigned
//   - sbom.cyclonedx.json (optional, not consulted here)
//
// In v1 the binary file name is taken from the manifest's `name` field. To
// avoid coupling this verifier to YAML parsing (manifest schema lives in
// plugin-sdk/manifest, which has its own evolution), the caller passes the
// resolved binary path explicitly. The verifier reads bytes from disk, hashes
// them per spec §5.2 (sha256(binary) || sha256(manifest)), and runs the
// Minisign check.

const (
	manifestFilename = "manifest.yaml"
	pubkeyFilename   = "signing.pub"
)

// Verifier verifies plugin bundles against their embedded Minisign signature.
//
// AllowUnsigned controls behaviour when a bundle has no .minisig or signing.pub:
// when true, the verifier returns loader.OutcomeUnsignedPermissive (caller is
// expected to honour the warning surface in ADR-045 §6); when false, the
// verifier returns loader.OutcomeRejected.
//
// Bundles that DO carry a signature are verified strictly regardless of
// AllowUnsigned — permissive mode never relaxes verification of signed
// bundles. (ADR-045 §6, last bullet.)
//
// Verifier implements loader.BundleVerifier directly — no adapter required.
type Verifier struct {
	AllowUnsigned bool
}

// VerifyBundle reads the bundle at bundleDir, expects the binary at
// binaryPath (which may live inside bundleDir or be passed as an absolute
// path — both are supported), and returns the verification outcome.
//
// VerifyBundle never panics. Filesystem errors and parse errors all surface
// as loader.OutcomeRejected with a wrapped error.
func (v *Verifier) VerifyBundle(bundleDir, binaryPath string) loader.VerifyResult {
	manifestPath := filepath.Join(bundleDir, manifestFilename)
	pubkeyPath := filepath.Join(bundleDir, pubkeyFilename)
	sigPath, sigErr := findMinisig(bundleDir)

	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return reject(fmt.Errorf("read manifest: %w", err))
	}

	binaryBytes, err := os.ReadFile(binaryPath)
	if err != nil {
		return reject(fmt.Errorf("read binary: %w", err))
	}

	pubkeyBytes, pkErr := os.ReadFile(pubkeyPath)
	missingPubkey := errors.Is(pkErr, os.ErrNotExist)
	missingSig := errors.Is(sigErr, os.ErrNotExist)

	switch {
	case missingPubkey && missingSig:
		// Unsigned. Decide based on AllowUnsigned.
		if v.AllowUnsigned {
			return loader.VerifyResult{Outcome: loader.OutcomeUnsignedPermissive}
		}
		return reject(errors.New("bundle is unsigned (no signing.pub or .minisig); set GLEIPNIR_ALLOW_UNSIGNED_PLUGINS=true to allow"))

	case missingPubkey != missingSig:
		// Half-signed bundles are always rejected — never silently treated
		// as either signed or unsigned. A bundle with a .minisig but no
		// signing.pub is malformed; same for the reverse.
		return reject(errors.New("bundle has only one of signing.pub/.minisig; both required for a signed bundle"))

	case pkErr != nil:
		return reject(fmt.Errorf("read signing.pub: %w", pkErr))

	case sigErr != nil:
		return reject(fmt.Errorf("read .minisig: %w", sigErr))
	}

	pk, _, err := signing.ParsePublicKey(pubkeyBytes)
	if err != nil {
		return reject(fmt.Errorf("parse signing.pub: %w", err))
	}

	sigBytes, err := os.ReadFile(sigPath)
	if err != nil {
		return reject(fmt.Errorf("read .minisig: %w", err))
	}
	sig, _, err := signing.ParseSignature(sigBytes)
	if err != nil {
		return reject(fmt.Errorf("parse .minisig: %w", err))
	}

	payload := signing.PluginPayload(binaryBytes, manifestBytes)
	if err := signing.Verify(pk, payload, sig, sig.TrustedComment); err != nil {
		return reject(fmt.Errorf("signature verification failed: %w", err))
	}

	return loader.VerifyResult{Outcome: loader.OutcomeVerified, Pubkey: pubkeyBytes}
}

// findMinisig looks for a single *.minisig file inside bundleDir. The plugin
// system spec writes it as <manifest.Name>.minisig — exactly one is expected.
// Returns os.ErrNotExist if zero exist; an explanatory error if more than one.
func findMinisig(bundleDir string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(bundleDir, "*.minisig"))
	if err != nil {
		return "", fmt.Errorf("scan for .minisig: %w", err)
	}
	switch len(matches) {
	case 0:
		return "", os.ErrNotExist
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("found %d .minisig files in bundle, expected exactly one", len(matches))
	}
}

func reject(err error) loader.VerifyResult {
	return loader.VerifyResult{Outcome: loader.OutcomeRejected, Err: err}
}
