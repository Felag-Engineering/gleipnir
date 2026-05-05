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
	"strings"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
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
	errUnsigned   = os.ErrNotExist // matches expected behaviour for unsigned check
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

	// 101 MiB is intentional: it sits just over the 100 MiB cumulative cap to exercise that limit.
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
	inst := NewInstaller(v, q, nil)
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

	// Generate a single keypair so both installs are signed by the same key.
	// Using the same key avoids triggering the pubkey-mismatch path.
	pk, sk, err := signing.GenerateKeypair(rand.Reader)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	pubkeyBytes := signing.MarshalPublicKey(pk, "test key")

	buildVersionedTarball := func(t *testing.T, version string) string {
		t.Helper()
		manifestBytes := []byte("schema_version: v1\nname: bump-plugin\nversion: " + version + "\nservices:\n  tool: v1\nauth:\n  mode: instance_credentials\n  strategy: none\n")
		binaryBytes := []byte("binary for bump-plugin " + version)
		payload := signing.PluginPayload(binaryBytes, manifestBytes)
		sig, err := signing.Sign(sk.SecretKey, sk.KeyID, payload, "trusted comment")
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		sigBytes := signing.MarshalSignature(sig, "test sig")
		tarPath := filepath.Join(t.TempDir(), "bump-plugin-"+version+".tar.gz")
		writeTarball(t, tarPath, []tarEntry{
			{name: "manifest.yaml", content: manifestBytes, mode: 0o644},
			{name: "bump-plugin", content: binaryBytes, mode: 0o755},
			{name: "signing.pub", content: pubkeyBytes, mode: 0o644},
			{name: "bump-plugin.minisig", content: sigBytes, mode: 0o644},
		})
		return tarPath
	}

	// First install — v1.0.0.
	tarPath1 := buildVersionedTarball(t, "1.0.0")
	if err := inst.Install(context.Background(), tarPath1); err != nil {
		t.Fatalf("initial Install: %v", err)
	}

	// Second install — v1.1.0 (version bump, same key).
	tarPath2 := buildVersionedTarball(t, "1.1.0")
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

// seedPluginInstance creates a plugin_instances row for testing TOFU transitions.
func seedPluginInstance(t *testing.T, q *db.Queries, pluginID, instanceID string, state model.PluginHealthState) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := q.CreatePluginInstance(context.Background(), db.CreatePluginInstanceParams{
		ID:                instanceID,
		PluginID:          pluginID,
		InstanceName:      "test",
		ConfigJson:        "{}",
		HandshakeVersions: "{}",
		HealthState:       string(state),
		CreatedAt:         now,
		UpdatedAt:         now,
	})
	if err != nil {
		t.Fatalf("CreatePluginInstance: %v", err)
	}
}

func TestInstall_TOFUFirstInstall_CapturesPubkey(t *testing.T) {
	q := openTestDB(t)
	inst := newTestInstaller(t, q, false)

	tarPath, _ := signedPluginTarball(t, "tofu-plugin", "1.0.0")
	if err := inst.Install(context.Background(), tarPath); err != nil {
		t.Fatalf("Install: %v", err)
	}

	row, err := q.GetPluginByName(context.Background(), "tofu-plugin")
	if err != nil {
		t.Fatalf("GetPluginByName: %v", err)
	}
	if row.TrustedPubkey == "" {
		t.Error("trusted_pubkey: want non-empty after first install (TOFU capture)")
	}
}

func TestInstall_SamePubkeyUpdate_PassesThrough(t *testing.T) {
	// Install v1 with key A, then install v2 built from the same key A.
	// The update should proceed without a mismatch event.
	q := openTestDB(t)
	inst := newTestInstaller(t, q, false)

	// Generate one keypair and build two tarballs with it.
	pk, sk, err := signing.GenerateKeypair(rand.Reader)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	pubkeyBytes := signing.MarshalPublicKey(pk, "test key")

	buildTar := func(t *testing.T, version string) string {
		t.Helper()
		manifestBytes := []byte("schema_version: v1\nname: same-key-plugin\nversion: " + version + "\nservices:\n  tool: v1\nauth:\n  mode: instance_credentials\n  strategy: none\n")
		binaryBytes := []byte("binary for " + version)
		payload := signing.PluginPayload(binaryBytes, manifestBytes)
		sig, err := signing.Sign(sk.SecretKey, sk.KeyID, payload, "trusted comment")
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		sigBytes := signing.MarshalSignature(sig, "test sig")
		tarPath := filepath.Join(t.TempDir(), "same-key-plugin-"+version+".tar.gz")
		writeTarball(t, tarPath, []tarEntry{
			{name: "manifest.yaml", content: manifestBytes, mode: 0o644},
			{name: "same-key-plugin", content: binaryBytes, mode: 0o755},
			{name: "signing.pub", content: pubkeyBytes, mode: 0o644},
			{name: "same-key-plugin.minisig", content: sigBytes, mode: 0o644},
		})
		return tarPath
	}

	if err := inst.Install(context.Background(), buildTar(t, "1.0.0")); err != nil {
		t.Fatalf("Install v1: %v", err)
	}
	if err := inst.Install(context.Background(), buildTar(t, "1.1.0")); err != nil {
		t.Fatalf("Install v1.1.0 (same key): %v", err)
	}

	row, err := q.GetPluginByName(context.Background(), "same-key-plugin")
	if err != nil {
		t.Fatalf("GetPluginByName: %v", err)
	}
	if row.PluginVersion != "1.1.0" {
		t.Errorf("plugin_version = %q, want 1.1.0", row.PluginVersion)
	}

	// No mismatch event should have been recorded.
	events, err := q.ListPluginAuditEventsByType(context.Background(), db.ListPluginAuditEventsByTypeParams{
		EventType: auditPubkeyMismatch,
		Offset:    0,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	if len(events) > 0 {
		t.Errorf("got %d pubkey_mismatch events, want 0 (same key update)", len(events))
	}
}

func TestInstall_DifferentPubkeyUpdate_BlocksAndAudits(t *testing.T) {
	// Install v1 with key A, then attempt v2 signed by key B.
	// Expect: no UpdatePluginManifest call (version stays at v1), mismatch audit event,
	// and any healthy instance transitions to pending_key_approval.
	q := openTestDB(t)
	inst := newTestInstaller(t, q, false)

	// First install with key A.
	tarPath1, _ := signedPluginTarball(t, "mismatch-plugin", "1.0.0")
	if err := inst.Install(context.Background(), tarPath1); err != nil {
		t.Fatalf("Install v1: %v", err)
	}

	pluginRow, err := q.GetPluginByName(context.Background(), "mismatch-plugin")
	if err != nil {
		t.Fatalf("GetPluginByName after v1: %v", err)
	}

	// Seed a healthy instance for the plugin.
	seedPluginInstance(t, q, pluginRow.ID, "inst-1", model.PluginHealthStateHealthy)

	// Second tarball signed by a different key (signedPluginTarball generates a fresh keypair).
	tarPath2, _ := signedPluginTarball(t, "mismatch-plugin", "2.0.0")
	if err := inst.Install(context.Background(), tarPath2); err != nil {
		t.Fatalf("Install v2 (different key): %v", err)
	}

	// Plugin version must remain at 1.0.0 (blocked).
	pluginRow2, err := q.GetPluginByName(context.Background(), "mismatch-plugin")
	if err != nil {
		t.Fatalf("GetPluginByName after v2: %v", err)
	}
	if pluginRow2.PluginVersion != "1.0.0" {
		t.Errorf("plugin_version = %q, want 1.0.0 (mismatch should block update)", pluginRow2.PluginVersion)
	}

	// A plugin_pubkey_mismatch audit event must have been recorded.
	events, err := q.ListPluginAuditEventsByType(context.Background(), db.ListPluginAuditEventsByTypeParams{
		EventType: auditPubkeyMismatch,
		Offset:    0,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("list mismatch events: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("expected a plugin_pubkey_mismatch audit event, got none")
	}
	if events[0].Severity != "high" {
		t.Errorf("audit severity = %q, want high", events[0].Severity)
	}
	var payload map[string]string
	if err := json.Unmarshal([]byte(events[0].PayloadJson), &payload); err != nil {
		t.Fatalf("parse mismatch audit payload: %v", err)
	}
	if payload["new_pubkey_b64"] == "" {
		t.Error("mismatch audit payload: new_pubkey_b64 must be non-empty")
	}
	if payload["name"] != "mismatch-plugin" {
		t.Errorf("mismatch audit payload: name = %q, want mismatch-plugin", payload["name"])
	}

	// The instance must be in pending_key_approval now.
	inst1, err := q.GetPluginInstanceByID(context.Background(), "inst-1")
	if err != nil {
		t.Fatalf("GetPluginInstanceByID: %v", err)
	}
	if inst1.HealthState != string(model.PluginHealthStatePendingKeyApproval) {
		t.Errorf("instance health_state = %q, want pending_key_approval", inst1.HealthState)
	}
}

func TestInstall_DelayedTOFU_CapturesSilently(t *testing.T) {
	// First install unsigned (empty trusted_pubkey), then signed update arrives.
	// Expect: pubkey is captured via UpdatePluginTrustedPubkey, no mismatch event.
	q := openTestDB(t)
	inst := newTestInstaller(t, q, true) // allowUnsigned to accept the first install

	tarPath1 := unsignedPluginTarball(t, "delayed-tofu", "1.0.0")
	if err := inst.Install(context.Background(), tarPath1); err != nil {
		t.Fatalf("Install v1 (unsigned): %v", err)
	}

	row1, err := q.GetPluginByName(context.Background(), "delayed-tofu")
	if err != nil {
		t.Fatalf("GetPluginByName after v1: %v", err)
	}
	if row1.TrustedPubkey != "" {
		t.Errorf("trusted_pubkey after unsigned install = %q, want empty", row1.TrustedPubkey)
	}

	// Install signed v2 — should capture pubkey, not mismatch.
	tarPath2, _ := signedPluginTarball(t, "delayed-tofu", "2.0.0")
	if err := inst.Install(context.Background(), tarPath2); err != nil {
		t.Fatalf("Install v2 (signed): %v", err)
	}

	row2, err := q.GetPluginByName(context.Background(), "delayed-tofu")
	if err != nil {
		t.Fatalf("GetPluginByName after v2: %v", err)
	}
	if row2.TrustedPubkey == "" {
		t.Error("trusted_pubkey: want non-empty after delayed TOFU capture")
	}
	if row2.PluginVersion != "2.0.0" {
		t.Errorf("plugin_version = %q, want 2.0.0 after delayed TOFU", row2.PluginVersion)
	}

	// No mismatch events.
	events, err := q.ListPluginAuditEventsByType(context.Background(), db.ListPluginAuditEventsByTypeParams{
		EventType: auditPubkeyMismatch,
		Offset:    0,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("list mismatch events: %v", err)
	}
	if len(events) > 0 {
		t.Errorf("got %d pubkey_mismatch events after delayed TOFU, want 0", len(events))
	}
}

// traversalTarball builds a tarball whose manifest.yaml contains the given name.
// The tarball itself uses "binary" as the actual file entry to avoid OS-level
// path issues when creating the archive; the manifest is what triggers the check.
func traversalTarball(t *testing.T, manifestName string) string {
	t.Helper()

	manifestBytes := []byte("schema_version: v1\nname: " + manifestName + "\nversion: 1.0.0\nservices:\n  tool: v1\nauth:\n  mode: instance_credentials\n  strategy: none\n")
	binaryBytes := []byte("fake binary")

	tarPath := filepath.Join(t.TempDir(), "traversal.tar.gz")
	writeTarball(t, tarPath, []tarEntry{
		{name: "manifest.yaml", content: manifestBytes, mode: 0o644},
		{name: "binary", content: binaryBytes, mode: 0o755},
	})
	return tarPath
}

// TestInstall_ManifestNamePathTraversal_Rejected verifies that a manifest whose
// name field contains path separators (e.g. "../escape") is rejected before any
// DB writes occur.
func TestInstall_ManifestNamePathTraversal_Rejected(t *testing.T) {
	q := openTestDB(t)
	inst := newTestInstaller(t, q, true) // allowUnsigned so verifier is not the rejection path

	tarPath := traversalTarball(t, "../escape")

	err := inst.Install(context.Background(), tarPath)
	if err == nil {
		t.Fatal("Install: expected error for path-traversal manifest name, got nil")
	}

	// No plugin row must have been created.
	_, dbErr := q.GetPluginByName(context.Background(), "../escape")
	if dbErr == nil {
		t.Error("expected no plugin row for traversal name, but found one")
	}
}

// nilErrVerifier is a stub BundleVerifier that returns OutcomeRejected with
// Err==nil, violating the interface contract, to confirm Install doesn't panic.
type nilErrVerifier struct{}

func (nilErrVerifier) VerifyBundle(_, _ string) VerifyResult {
	return VerifyResult{Outcome: OutcomeRejected, Err: nil}
}

// TestInstall_RejectedWithNilErr_NoPanic confirms that Install does not panic
// when a BundleVerifier returns OutcomeRejected with Err==nil.
func TestInstall_RejectedWithNilErr_NoPanic(t *testing.T) {
	q := openTestDB(t)

	// Build a valid signed tarball so we get past manifest parsing.
	tarPath, _ := signedPluginTarball(t, "nil-err-plugin", "1.0.0")

	inst := &Installer{verifier: nilErrVerifier{}, q: q, publisher: nil, clock: time.Now}

	// Must not panic; Install should record an audit event and return nil.
	if err := inst.Install(context.Background(), tarPath); err != nil {
		t.Fatalf("Install returned unexpected error: %v", err)
	}

	// No plugin row — only an audit event.
	_, dbErr := q.GetPluginByName(context.Background(), "nil-err-plugin")
	if dbErr == nil {
		t.Error("expected no plugin row for rejected install, but found one")
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
		t.Error("expected a signature_invalid audit event, got none")
	}
}

func TestReadManifest_Validation(t *testing.T) {
	base := "schema_version: v1\nservices:\n  tool: v1\nauth:\n  mode: instance_credentials\n  strategy: none\n"

	cases := []struct {
		name        string
		manifest    string
		wantErrFrag string
	}{
		{
			name:        "empty name",
			manifest:    base + "version: 1.0.0\n",
			wantErrFrag: "manifest.name is required",
		},
		{
			name:        "empty version",
			manifest:    base + "name: my-plugin\n",
			wantErrFrag: "manifest.version is required",
		},
		{
			name:        "valid manifest",
			manifest:    base + "name: my-plugin\nversion: 1.0.0\n",
			wantErrFrag: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte(tc.manifest), 0o644); err != nil {
				t.Fatalf("write manifest: %v", err)
			}
			_, _, err := readManifest(dir)
			if tc.wantErrFrag == "" {
				if err != nil {
					t.Errorf("readManifest: unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("readManifest: expected error containing %q, got nil", tc.wantErrFrag)
			}
			if !strings.Contains(err.Error(), tc.wantErrFrag) {
				t.Errorf("readManifest error = %q, want substring %q", err.Error(), tc.wantErrFrag)
			}
		})
	}
}
