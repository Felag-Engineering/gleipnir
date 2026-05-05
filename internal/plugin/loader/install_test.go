package loader

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/plugin-sdk/signing"
	_ "modernc.org/sqlite"
)

// realVerifier wraps the signing package to produce a loader.VerifyResult
// without importing internal/plugin (which would create an import cycle since
// internal/plugin imports internal/plugin/loader).
//
// It reimplements just enough of the verification path for tests: locate
// signing.pub + *.minisig, parse, and call signing.Verify.
type realVerifier struct {
	allowUnsigned bool
}

func (v *realVerifier) VerifyBundle(bundleDir, binaryPath string) VerifyResult {
	manifestBytes, err := os.ReadFile(bundleDir + "/manifest.yaml")
	if err != nil {
		return VerifyResult{Outcome: OutcomeRejected, Err: err}
	}
	binaryBytes, err := os.ReadFile(binaryPath)
	if err != nil {
		return VerifyResult{Outcome: OutcomeRejected, Err: err}
	}
	pubkeyBytes, pkErr := os.ReadFile(bundleDir + "/signing.pub")
	matches, _ := filepath.Glob(bundleDir + "/*.minisig")
	missingSig := len(matches) == 0

	if os.IsNotExist(pkErr) && missingSig {
		if v.allowUnsigned {
			return VerifyResult{Outcome: OutcomeUnsignedPermissive}
		}
		return VerifyResult{Outcome: OutcomeRejected, Err: errUnsigned}
	}
	if pkErr != nil {
		return VerifyResult{Outcome: OutcomeRejected, Err: pkErr}
	}
	if missingSig {
		return VerifyResult{Outcome: OutcomeRejected, Err: errHalfSigned}
	}

	pk, _, err := signing.ParsePublicKey(pubkeyBytes)
	if err != nil {
		return VerifyResult{Outcome: OutcomeRejected, Err: err}
	}
	sigBytes, err := os.ReadFile(matches[0])
	if err != nil {
		return VerifyResult{Outcome: OutcomeRejected, Err: err}
	}
	sig, _, err := signing.ParseSignature(sigBytes)
	if err != nil {
		return VerifyResult{Outcome: OutcomeRejected, Err: err}
	}
	payload := signing.PluginPayload(binaryBytes, manifestBytes)
	if err := signing.Verify(pk, payload, sig, sig.TrustedComment); err != nil {
		return VerifyResult{Outcome: OutcomeRejected, Err: err}
	}
	return VerifyResult{Outcome: OutcomeVerified, Pubkey: pubkeyBytes}
}

// sentinel errors for the test verifier stubs.
var (
	errUnsigned  = os.ErrNotExist // matches expected behaviour for unsigned check
	errHalfSigned = os.ErrInvalid
)

// openTestDB opens a temporary SQLite DB, applies the full schema, and returns
// a *db.Queries ready for use. The store is closed at the end of the test.
func openTestDB(t *testing.T) *db.Queries {
	t.Helper()
	s, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return s.Queries()
}

// signedPluginTarball builds a fully signed plugin tarball at a temp path and
// returns the tarball path plus the raw manifest bytes.
//
// Uses signing.GenerateKeypair(rand.Reader) for consistency with
// internal/plugin/verify_test.go.
func signedPluginTarball(t *testing.T, name, version string) (tarPath string, manifestBytes []byte) {
	t.Helper()

	manifestBytes = []byte("schema_version: v1\nname: " + name + "\nversion: " + version + "\nservices:\n  tool: v1\nauth:\n  mode: instance_credentials\n  strategy: none\n")
	binaryBytes := []byte("fake binary content for " + name)

	pk, sk, err := signing.GenerateKeypair(rand.Reader)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}

	payload := signing.PluginPayload(binaryBytes, manifestBytes)
	sig, err := signing.Sign(sk.SecretKey, sk.KeyID, payload, "trusted comment")
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	pubkeyBytes := signing.MarshalPublicKey(pk, "test key")
	sigBytes := signing.MarshalSignature(sig, "test sig")

	tarPath = filepath.Join(t.TempDir(), name+".tar.gz")
	writeTarball(t, tarPath, []tarEntry{
		{name: "manifest.yaml", content: manifestBytes, mode: 0o644},
		{name: name, content: binaryBytes, mode: 0o755},
		{name: "signing.pub", content: pubkeyBytes, mode: 0o644},
		{name: name + ".minisig", content: sigBytes, mode: 0o644},
	})

	return tarPath, manifestBytes
}

// unsignedPluginTarball builds a plugin tarball without any signing artifacts.
func unsignedPluginTarball(t *testing.T, name, version string) string {
	t.Helper()

	manifestBytes := []byte("schema_version: v1\nname: " + name + "\nversion: " + version + "\nservices:\n  tool: v1\nauth:\n  mode: instance_credentials\n  strategy: none\n")
	binaryBytes := []byte("fake binary content for " + name)

	tarPath := filepath.Join(t.TempDir(), name+".tar.gz")
	writeTarball(t, tarPath, []tarEntry{
		{name: "manifest.yaml", content: manifestBytes, mode: 0o644},
		{name: name, content: binaryBytes, mode: 0o755},
	})
	return tarPath
}

// badSignatureTarball builds a tarball with a signing.pub and .minisig that
// don't match the binary content.
func badSignatureTarball(t *testing.T, name, version string) string {
	t.Helper()

	manifestBytes := []byte("schema_version: v1\nname: " + name + "\nversion: " + version + "\nservices:\n  tool: v1\nauth:\n  mode: instance_credentials\n  strategy: none\n")
	binaryBytes := []byte("tampered binary content")
	originalBinaryBytes := []byte("original binary content")

	pk, sk, err := signing.GenerateKeypair(rand.Reader)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}

	// Sign the original bytes, not the tampered bytes — produces an invalid sig.
	payload := signing.PluginPayload(originalBinaryBytes, manifestBytes)
	sig, err := signing.Sign(sk.SecretKey, sk.KeyID, payload, "trusted comment")
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	pubkeyBytes := signing.MarshalPublicKey(pk, "test key")
	sigBytes := signing.MarshalSignature(sig, "test sig")

	tarPath := filepath.Join(t.TempDir(), name+"-bad.tar.gz")
	writeTarball(t, tarPath, []tarEntry{
		{name: "manifest.yaml", content: manifestBytes, mode: 0o644},
		{name: name, content: binaryBytes, mode: 0o755},
		{name: "signing.pub", content: pubkeyBytes, mode: 0o644},
		{name: name + ".minisig", content: sigBytes, mode: 0o644},
	})
	return tarPath
}

// oversizedTarball builds a tarball whose uncompressed total exceeds maxTarballBytes.
func oversizedTarball(t *testing.T, name string) string {
	t.Helper()

	// Write 101 MiB of content (just over the 100 MiB cap).
	huge := bytes.Repeat([]byte("X"), 101<<20)

	tarPath := filepath.Join(t.TempDir(), name+"-huge.tar.gz")
	writeTarball(t, tarPath, []tarEntry{
		{name: "huge.bin", content: huge, mode: 0o644},
	})
	return tarPath
}

// writeTarball serialises entries into a .tar.gz at path.
func writeTarball(t *testing.T, path string, entries []tarEntry) {
	t.Helper()

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	for _, e := range entries {
		tf := e.typeflag
		if tf == 0 {
			tf = tar.TypeReg
		}
		hdr := &tar.Header{
			Name:     e.name,
			Typeflag: tf,
			Mode:     e.mode,
			Size:     int64(len(e.content)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header %q: %v", e.name, err)
		}
		if len(e.content) > 0 {
			if _, err := tw.Write(e.content); err != nil {
				t.Fatalf("write body %q: %v", e.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write tarball %q: %v", path, err)
	}
}

// newTestInstaller builds an Installer with a fixed clock for timestamp assertions.
func newTestInstaller(t *testing.T, q *db.Queries, allowUnsigned bool) *Installer {
	t.Helper()
	v := &realVerifier{allowUnsigned: allowUnsigned}
	inst := NewInstaller(v, q)
	inst.clock = func() time.Time { return time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC) }
	return inst
}

func TestInstall_NewSignedPlugin_PendingReview(t *testing.T) {
	q := openTestDB(t)
	inst := newTestInstaller(t, q, false)

	tarPath, _ := signedPluginTarball(t, "test-plugin", "1.0.0")

	if err := inst.Install(context.Background(), tarPath); err != nil {
		t.Fatalf("Install: %v", err)
	}

	row, err := q.GetPluginByName(context.Background(), "test-plugin")
	if err != nil {
		t.Fatalf("GetPluginByName: %v", err)
	}

	if row.Status != "pending_review" {
		t.Errorf("status = %q, want %q", row.Status, "pending_review")
	}
	if row.PluginVersion != "1.0.0" {
		t.Errorf("plugin_version = %q, want %q", row.PluginVersion, "1.0.0")
	}
	if row.TrustedPubkey == "" {
		t.Error("trusted_pubkey: want non-empty (TOFU-captured pubkey)")
	}
}

func TestInstall_BadSignature_AuditOnly(t *testing.T) {
	q := openTestDB(t)
	inst := newTestInstaller(t, q, false)

	tarPath := badSignatureTarball(t, "bad-plugin", "1.0.0")

	if err := inst.Install(context.Background(), tarPath); err != nil {
		t.Fatalf("Install with bad sig returned unexpected error: %v", err)
	}

	// No plugin row must exist — only an audit event.
	_, err := q.GetPluginByName(context.Background(), "bad-plugin")
	if err == nil {
		t.Error("expected no plugin row for bad-signature install, got one")
	}

	events, err := q.ListPluginAuditEventsByType(context.Background(), db.ListPluginAuditEventsByTypeParams{
		EventType: auditSignatureInvalid,
		Offset:    0,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	if len(events) == 0 {
		t.Error("expected at least one signature_invalid audit event")
	}

	var payload map[string]string
	if err := json.Unmarshal([]byte(events[0].PayloadJson), &payload); err != nil {
		t.Fatalf("parse audit payload: %v", err)
	}
	if payload["manifest_name"] != "bad-plugin" {
		t.Errorf("audit payload manifest_name = %q, want %q", payload["manifest_name"], "bad-plugin")
	}
	if payload["error"] == "" {
		t.Error("audit payload error: want non-empty")
	}
	if events[0].Severity != "high" {
		t.Errorf("audit severity = %q, want %q", events[0].Severity, "high")
	}
}

func TestInstall_VersionBump_PendingReview(t *testing.T) {
	q := openTestDB(t)
	inst := newTestInstaller(t, q, false)

	// First install — v1.0.0.
	tarPath1, _ := signedPluginTarball(t, "bump-plugin", "1.0.0")
	if err := inst.Install(context.Background(), tarPath1); err != nil {
		t.Fatalf("initial Install: %v", err)
	}

	// Second install — v1.1.0 (version bump).
	tarPath2, _ := signedPluginTarball(t, "bump-plugin", "1.1.0")
	if err := inst.Install(context.Background(), tarPath2); err != nil {
		t.Fatalf("version-bump Install: %v", err)
	}

	row, err := q.GetPluginByName(context.Background(), "bump-plugin")
	if err != nil {
		t.Fatalf("GetPluginByName: %v", err)
	}
	if row.PluginVersion != "1.1.0" {
		t.Errorf("plugin_version = %q, want %q", row.PluginVersion, "1.1.0")
	}
	if row.Status != "pending_review" {
		t.Errorf("status = %q, want %q", row.Status, "pending_review")
	}

	// Expect a plugin_update_pending audit event.
	events, err := q.ListPluginAuditEventsByType(context.Background(), db.ListPluginAuditEventsByTypeParams{
		EventType: auditUpdatePending,
		Offset:    0,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("list update_pending events: %v", err)
	}
	if len(events) == 0 {
		t.Error("expected plugin_update_pending audit event after version bump")
	}
}

func TestInstall_SameVersion_NoOp(t *testing.T) {
	q := openTestDB(t)
	inst := newTestInstaller(t, q, false)

	tarPath, _ := signedPluginTarball(t, "stable-plugin", "2.0.0")

	// Install twice with the same version.
	if err := inst.Install(context.Background(), tarPath); err != nil {
		t.Fatalf("first Install: %v", err)
	}
	if err := inst.Install(context.Background(), tarPath); err != nil {
		t.Fatalf("second Install (same version): %v", err)
	}

	row, err := q.GetPluginByName(context.Background(), "stable-plugin")
	if err != nil {
		t.Fatalf("GetPluginByName: %v", err)
	}
	// Version counter stays at 0 (no update was issued).
	if row.Version != 0 {
		t.Errorf("version = %d, want 0 (same-version should be a no-op)", row.Version)
	}
}

func TestInstall_TarballTooLarge(t *testing.T) {
	q := openTestDB(t)
	inst := newTestInstaller(t, q, false)

	tarPath := oversizedTarball(t, "huge-plugin")

	err := inst.Install(context.Background(), tarPath)
	if err == nil {
		t.Fatal("expected error for oversized tarball, got nil")
	}
}

func TestInstall_UnsignedPermissive_PendingReview(t *testing.T) {
	q := openTestDB(t)
	// AllowUnsigned=true so unsigned bundles are accepted.
	inst := newTestInstaller(t, q, true)

	tarPath := unsignedPluginTarball(t, "unsigned-plugin", "1.0.0")

	if err := inst.Install(context.Background(), tarPath); err != nil {
		t.Fatalf("Install (unsigned permissive): %v", err)
	}

	row, err := q.GetPluginByName(context.Background(), "unsigned-plugin")
	if err != nil {
		t.Fatalf("GetPluginByName: %v", err)
	}
	if row.Status != "pending_review" {
		t.Errorf("status = %q, want pending_review", row.Status)
	}
}
