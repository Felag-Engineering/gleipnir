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
	return openTestStore(t).Queries()
}

// openTestStore is the same as openTestDB but returns the underlying *db.Store
// so callers can reach the raw *sql.DB for tx-aware installer construction.
func openTestStore(t *testing.T) *db.Store {
	t.Helper()
	s, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return s
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
// pluginsDir is typically t.TempDir() for tests that assert on binary publishing,
// or "" for tests that only care about DB state.
func newTestInstaller(t *testing.T, q *db.Queries, allowUnsigned bool) *Installer {
	t.Helper()
	v := &realVerifier{allowUnsigned: allowUnsigned}
	inst := NewInstaller(v, q, nil, nil, t.TempDir())
	inst.clock = func() time.Time { return time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC) }
	return inst
}

// TestInstall_NewSignedPlugin_Active verifies that a verified install promotes
// the plugin row to status=active so the subprocess manager and trigger
// supervisor (both of which filter by status='active') will pick it up. Before
// this change a fresh install sat in pending_review forever with no automated
// path to advance it (no UpdatePluginStatus caller existed).
func TestInstall_NewSignedPlugin_Active(t *testing.T) {
	q := openTestDB(t)
	inst := newTestInstaller(t, q, false)

	tarPath, _ := signedPluginTarball(t, "test-plugin", "1.0.0")

	if _, err := inst.Install(context.Background(), tarPath); err != nil {
		t.Fatalf("Install: %v", err)
	}

	row, err := q.GetPluginByName(context.Background(), "test-plugin")
	if err != nil {
		t.Fatalf("GetPluginByName: %v", err)
	}

	if row.Status != "active" {
		t.Errorf("status = %q, want %q", row.Status, "active")
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

	if _, err := inst.Install(context.Background(), tarPath); err != nil {
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

func TestInstall_VersionBump_Active(t *testing.T) {
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
	if _, err := inst.Install(context.Background(), tarPath1); err != nil {
		t.Fatalf("initial Install: %v", err)
	}

	// Second install — v1.1.0 (version bump, same key).
	tarPath2 := buildVersionedTarball(t, "1.1.0")
	if _, err := inst.Install(context.Background(), tarPath2); err != nil {
		t.Fatalf("version-bump Install: %v", err)
	}

	row, err := q.GetPluginByName(context.Background(), "bump-plugin")
	if err != nil {
		t.Fatalf("GetPluginByName: %v", err)
	}
	if row.PluginVersion != "1.1.0" {
		t.Errorf("plugin_version = %q, want %q", row.PluginVersion, "1.1.0")
	}
	if row.Status != "active" {
		t.Errorf("status = %q, want %q", row.Status, "active")
	}

	// A version-string-only bump produces a cosmetic diff (version is a cosmetic field),
	// so we now expect plugin_manifest_cosmetic_change rather than plugin_update_pending.
	events, err := q.ListPluginAuditEventsByType(context.Background(), db.ListPluginAuditEventsByTypeParams{
		EventType: auditManifestCosmeticChange,
		Offset:    0,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("list cosmetic_change events: %v", err)
	}
	if len(events) == 0 {
		t.Error("expected plugin_manifest_cosmetic_change audit event after version-only bump")
	}
}

func TestInstall_SameVersion_NoOp(t *testing.T) {
	q := openTestDB(t)
	inst := newTestInstaller(t, q, false)

	tarPath, _ := signedPluginTarball(t, "stable-plugin", "2.0.0")

	// Install twice with the same version.
	if _, err := inst.Install(context.Background(), tarPath); err != nil {
		t.Fatalf("first Install: %v", err)
	}
	if _, err := inst.Install(context.Background(), tarPath); err != nil {
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

	_, err := inst.Install(context.Background(), tarPath)
	if err == nil {
		t.Fatal("expected error for oversized tarball, got nil")
	}
}

// TestInstall_UnsignedPermissive_Active verifies that an unsigned-permissive
// install also promotes to status=active. The operator's explicit opt-in via
// GLEIPNIR_ALLOW_UNSIGNED_PLUGINS=true is the trust signal — staying in
// pending_review would defeat the opt-in (instances would never spawn).
func TestInstall_UnsignedPermissive_Active(t *testing.T) {
	q := openTestDB(t)
	// AllowUnsigned=true so unsigned bundles are accepted.
	inst := newTestInstaller(t, q, true)

	tarPath := unsignedPluginTarball(t, "unsigned-plugin", "1.0.0")

	if _, err := inst.Install(context.Background(), tarPath); err != nil {
		t.Fatalf("Install (unsigned permissive): %v", err)
	}

	row, err := q.GetPluginByName(context.Background(), "unsigned-plugin")
	if err != nil {
		t.Fatalf("GetPluginByName: %v", err)
	}
	if row.Status != "active" {
		t.Errorf("status = %q, want active", row.Status)
	}
}

// seedPluginInstance creates a plugin_instances row with instance_name="test".
// Use seedPluginInstanceNamed when seeding multiple instances for the same plugin.
func seedPluginInstance(t *testing.T, q *db.Queries, pluginID, instanceID string, state model.PluginHealthState) {
	t.Helper()
	seedPluginInstanceNamed(t, q, pluginID, instanceID, "test", state)
}

// seedPluginInstanceNamed creates a plugin_instances row with a specified instance name.
// Required when seeding multiple instances for the same plugin to satisfy the
// (plugin_id, instance_name) UNIQUE constraint.
func seedPluginInstanceNamed(t *testing.T, q *db.Queries, pluginID, instanceID, instanceName string, state model.PluginHealthState) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := q.CreatePluginInstance(context.Background(), db.CreatePluginInstanceParams{
		ID:                    instanceID,
		PluginID:              pluginID,
		InstanceName:          instanceName,
		ConfigJson:            "{}",
		SubscriptionScopeJson: "{}",
		HandshakeVersions:     "{}",
		HealthState:           string(state),
		CreatedAt:             now,
		UpdatedAt:             now,
	})
	if err != nil {
		t.Fatalf("CreatePluginInstance: %v", err)
	}
}

func TestInstall_TOFUFirstInstall_CapturesPubkey(t *testing.T) {
	q := openTestDB(t)
	inst := newTestInstaller(t, q, false)

	tarPath, _ := signedPluginTarball(t, "tofu-plugin", "1.0.0")
	if _, err := inst.Install(context.Background(), tarPath); err != nil {
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

	if _, err := inst.Install(context.Background(), buildTar(t, "1.0.0")); err != nil {
		t.Fatalf("Install v1: %v", err)
	}
	if _, err := inst.Install(context.Background(), buildTar(t, "1.1.0")); err != nil {
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
	if _, err := inst.Install(context.Background(), tarPath1); err != nil {
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
	if _, err := inst.Install(context.Background(), tarPath2); err != nil {
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
	if _, err := inst.Install(context.Background(), tarPath1); err != nil {
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
	if _, err := inst.Install(context.Background(), tarPath2); err != nil {
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

	_, err := inst.Install(context.Background(), tarPath)
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

	inst := &Installer{verifier: nilErrVerifier{}, q: q, publisher: nil, pluginsDir: t.TempDir(), clock: time.Now}

	// Must not panic; Install should record an audit event and return nil.
	if _, err := inst.Install(context.Background(), tarPath); err != nil {
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

// TestInstall_TransactionalRollback drops the plugin_audit_events table after
// schema migration so the audit insert inside createPlugin's transaction
// fails. The tx must roll back so neither the plugins row nor any audit row
// is visible afterwards. Install must return an error rather than swallow it.
func TestInstall_TransactionalRollback(t *testing.T) {
	store := openTestStore(t)
	q := store.Queries()

	// Sabotage the audit table so InsertPluginAuditEvent fails inside the tx.
	if _, err := store.DB().Exec("DROP TABLE plugin_audit_events"); err != nil {
		t.Fatalf("drop audit table: %v", err)
	}

	v := &realVerifier{allowUnsigned: false}
	inst := NewInstaller(v, q, store.DB(), nil, t.TempDir())
	inst.clock = func() time.Time { return time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC) }

	tarPath, _ := signedPluginTarball(t, "rollback-plugin", "1.0.0")
	id, err := inst.Install(context.Background(), tarPath)
	if err == nil {
		t.Fatalf("expected install to fail when audit insert errors; got id=%q nil err", id)
	}
	if id != "" {
		t.Errorf("expected empty id on rollback; got %q", id)
	}

	// The plugins row must not be visible — the tx rolled back.
	if _, getErr := q.GetPluginByName(context.Background(), "rollback-plugin"); getErr == nil {
		t.Error("plugins row visible after tx rollback; expected sql.ErrNoRows")
	}
}

// TestInstall_TransactionalCreate confirms that when an Installer is wired
// with a *sql.DB, createPlugin's row insert and audit insert land in the same
// transaction — i.e. both rows are visible after a successful install.
func TestInstall_TransactionalCreate(t *testing.T) {
	store := openTestStore(t)
	q := store.Queries()

	v := &realVerifier{allowUnsigned: false}
	inst := NewInstaller(v, q, store.DB(), nil, t.TempDir())
	inst.clock = func() time.Time { return time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC) }

	tarPath, _ := signedPluginTarball(t, "tx-plugin", "1.0.0")
	id, err := inst.Install(context.Background(), tarPath)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty plugin id")
	}

	if _, err := q.GetPluginByName(context.Background(), "tx-plugin"); err != nil {
		t.Fatalf("plugin row not visible after tx commit: %v", err)
	}
	events, err := q.ListPluginAuditEventsByType(context.Background(), db.ListPluginAuditEventsByTypeParams{
		EventType: auditPluginInstalled,
		Offset:    0,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("expected plugin_installed audit event after tx commit, got none")
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

// buildSignedTarWithContent builds a tarball signed by a given keypair with specific
// manifest content and binary content. Returns the tarball path.
func buildSignedTarWithContent(t *testing.T, name string, manifestContent, binaryContent, pubkeyBytes []byte, skSecret [64]byte, skKeyID [8]byte) string {
	t.Helper()

	payload := signing.PluginPayload(binaryContent, manifestContent)
	sig, err := signing.Sign(skSecret, skKeyID, payload, "trusted comment")
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	sigBytes := signing.MarshalSignature(sig, "test sig")
	tarPath := filepath.Join(t.TempDir(), name+".tar.gz")
	writeTarball(t, tarPath, []tarEntry{
		{name: "manifest.yaml", content: manifestContent, mode: 0o644},
		{name: name, content: binaryContent, mode: 0o755},
		{name: "signing.pub", content: pubkeyBytes, mode: 0o644},
		{name: name + ".minisig", content: sigBytes, mode: 0o644},
	})
	return tarPath
}

// TestInstall_HotReload_MaterialChange_DoesNotUpdateSnapshot verifies that a hot-reload
// tarball with a material manifest change does not update the plugin row's manifest_snapshot.
func TestInstall_HotReload_MaterialChange_DoesNotUpdateSnapshot(t *testing.T) {
	q := openTestDB(t)
	inst := newTestInstaller(t, q, false)

	pk, sk, err := signing.GenerateKeypair(rand.Reader)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	pubkeyBytes := signing.MarshalPublicKey(pk, "test key")

	v1Manifest := []byte("schema_version: v1\nname: mat-plugin\nversion: 1.0.0\nservices:\n  tool: v1\nauth:\n  mode: instance_credentials\n  strategy: none\n")
	tarPath1 := buildSignedTarWithContent(t, "mat-plugin", v1Manifest, []byte("binary v1"), pubkeyBytes, sk.SecretKey, sk.KeyID)
	if _, err := inst.Install(context.Background(), tarPath1); err != nil {
		t.Fatalf("Install v1: %v", err)
	}

	row1, err := q.GetPluginByName(context.Background(), "mat-plugin")
	if err != nil {
		t.Fatalf("GetPluginByName after v1: %v", err)
	}

	// v2 adds a new tool — material change.
	v2Manifest := []byte("schema_version: v1\nname: mat-plugin\nversion: 2.0.0\nservices:\n  tool: v1\nauth:\n  mode: instance_credentials\n  strategy: none\ntools:\n- name: my_tool\n  description: a tool\n")
	tarPath2 := buildSignedTarWithContent(t, "mat-plugin", v2Manifest, []byte("binary v2"), pubkeyBytes, sk.SecretKey, sk.KeyID)
	if _, err := inst.Install(context.Background(), tarPath2); err != nil {
		t.Fatalf("Install v2 (material change): %v", err)
	}

	row2, err := q.GetPluginByName(context.Background(), "mat-plugin")
	if err != nil {
		t.Fatalf("GetPluginByName after v2: %v", err)
	}
	// Snapshot must remain at v1 — material change must NOT update the row.
	if row2.ManifestSnapshot != row1.ManifestSnapshot {
		t.Errorf("manifest_snapshot was updated despite material change; want unchanged v1 snapshot")
	}
	if row2.PluginVersion != "1.0.0" {
		t.Errorf("plugin_version = %q, want 1.0.0 (blocked by material change)", row2.PluginVersion)
	}
}

// TestInstall_HotReload_MaterialChange_TransitionsInstancesToPendingManifestApproval
// verifies that healthy and unsigned_permissive instances are transitioned to
// pending_manifest_approval on a material change; terminal-state instances are skipped.
func TestInstall_HotReload_MaterialChange_TransitionsInstancesToPendingManifestApproval(t *testing.T) {
	q := openTestDB(t)
	inst := newTestInstaller(t, q, false)

	pk, sk, err := signing.GenerateKeypair(rand.Reader)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	pubkeyBytes := signing.MarshalPublicKey(pk, "test key")

	v1Manifest := []byte("schema_version: v1\nname: trans-plugin\nversion: 1.0.0\nservices:\n  tool: v1\nauth:\n  mode: instance_credentials\n  strategy: none\n")
	tarPath1 := buildSignedTarWithContent(t, "trans-plugin", v1Manifest, []byte("binary v1"), pubkeyBytes, sk.SecretKey, sk.KeyID)
	if _, err := inst.Install(context.Background(), tarPath1); err != nil {
		t.Fatalf("Install v1: %v", err)
	}

	pluginRow, err := q.GetPluginByName(context.Background(), "trans-plugin")
	if err != nil {
		t.Fatalf("GetPluginByName: %v", err)
	}

	// Use the instanceID as the instance_name to satisfy the (plugin_id, instance_name) UNIQUE constraint.
	seedPluginInstanceNamed(t, q, pluginRow.ID, "inst-healthy", "inst-healthy", model.PluginHealthStateHealthy)
	seedPluginInstanceNamed(t, q, pluginRow.ID, "inst-crashed", "inst-crashed", model.PluginHealthStateCrashed) // terminal — should be skipped

	v2Manifest := []byte("schema_version: v1\nname: trans-plugin\nversion: 2.0.0\nservices:\n  tool: v2\nauth:\n  mode: instance_credentials\n  strategy: none\n")
	tarPath2 := buildSignedTarWithContent(t, "trans-plugin", v2Manifest, []byte("binary v2"), pubkeyBytes, sk.SecretKey, sk.KeyID)
	if _, err := inst.Install(context.Background(), tarPath2); err != nil {
		t.Fatalf("Install v2 (material change): %v", err)
	}

	instHealthy, err := q.GetPluginInstanceByID(context.Background(), "inst-healthy")
	if err != nil {
		t.Fatalf("GetPluginInstanceByID inst-healthy: %v", err)
	}
	if instHealthy.HealthState != string(model.PluginHealthStatePendingManifestApproval) {
		t.Errorf("inst-healthy health_state = %q, want pending_manifest_approval", instHealthy.HealthState)
	}

	instCrashed, err := q.GetPluginInstanceByID(context.Background(), "inst-crashed")
	if err != nil {
		t.Fatalf("GetPluginInstanceByID inst-crashed: %v", err)
	}
	if instCrashed.HealthState != string(model.PluginHealthStateCrashed) {
		t.Errorf("inst-crashed health_state = %q, want crashed (unchanged)", instCrashed.HealthState)
	}
}

// TestInstall_HotReload_MaterialChange_EmitsHighSeverityAuditEvent verifies that a
// plugin_manifest_material_change audit event with high severity is emitted.
func TestInstall_HotReload_MaterialChange_EmitsHighSeverityAuditEvent(t *testing.T) {
	q := openTestDB(t)
	inst := newTestInstaller(t, q, false)

	pk, sk, err := signing.GenerateKeypair(rand.Reader)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	pubkeyBytes := signing.MarshalPublicKey(pk, "test key")

	v1Manifest := []byte("schema_version: v1\nname: audit-plugin\nversion: 1.0.0\nservices:\n  tool: v1\nauth:\n  mode: instance_credentials\n  strategy: none\n")
	tarPath1 := buildSignedTarWithContent(t, "audit-plugin", v1Manifest, []byte("binary v1"), pubkeyBytes, sk.SecretKey, sk.KeyID)
	if _, err := inst.Install(context.Background(), tarPath1); err != nil {
		t.Fatalf("Install v1: %v", err)
	}

	v2Manifest := []byte("schema_version: v1\nname: audit-plugin\nversion: 2.0.0\nservices:\n  tool: v2\nauth:\n  mode: instance_credentials\n  strategy: none\n")
	tarPath2 := buildSignedTarWithContent(t, "audit-plugin", v2Manifest, []byte("binary v2"), pubkeyBytes, sk.SecretKey, sk.KeyID)
	if _, err := inst.Install(context.Background(), tarPath2); err != nil {
		t.Fatalf("Install v2 (material change): %v", err)
	}

	events, err := q.ListPluginAuditEventsByType(context.Background(), db.ListPluginAuditEventsByTypeParams{
		EventType: auditManifestMaterialChange,
		Offset:    0,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("expected a plugin_manifest_material_change audit event, got none")
	}
	if events[0].Severity != "high" {
		t.Errorf("audit severity = %q, want high", events[0].Severity)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(events[0].PayloadJson), &payload); err != nil {
		t.Fatalf("parse audit payload: %v", err)
	}
	if payload["candidate_manifest_b64"] == "" {
		t.Error("audit payload: candidate_manifest_b64 must be non-empty")
	}
	materialFields, ok := payload["material_fields"].([]any)
	if !ok || len(materialFields) == 0 {
		t.Errorf("audit payload: material_fields must be non-empty, got %v", payload["material_fields"])
	}
}

// TestInstall_HotReload_MaterialChange_NewlyRequiredConfigField_PayloadIncludesField
// verifies that the audit event payload includes newly-required config fields when
// the new manifest's config_schema gains a required property.
func TestInstall_HotReload_MaterialChange_NewlyRequiredConfigField_PayloadIncludesField(t *testing.T) {
	q := openTestDB(t)
	inst := newTestInstaller(t, q, false)

	pk, sk, err := signing.GenerateKeypair(rand.Reader)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	pubkeyBytes := signing.MarshalPublicKey(pk, "test key")

	// v1 has no config_schema.
	v1Manifest := []byte("schema_version: v1\nname: config-plugin\nversion: 1.0.0\nservices:\n  tool: v1\nauth:\n  mode: instance_credentials\n  strategy: none\n")
	tarPath1 := buildSignedTarWithContent(t, "config-plugin", v1Manifest, []byte("binary v1"), pubkeyBytes, sk.SecretKey, sk.KeyID)
	if _, err := inst.Install(context.Background(), tarPath1); err != nil {
		t.Fatalf("Install v1: %v", err)
	}

	// v2 adds a required config field AND changes services (to ensure it's material).
	v2Manifest := []byte("schema_version: v1\nname: config-plugin\nversion: 2.0.0\nservices:\n  tool: v2\nauth:\n  mode: instance_credentials\n  strategy: none\nconfig_schema:\n  type: object\n  properties:\n    api_key:\n      type: string\n  required:\n    - api_key\n")
	tarPath2 := buildSignedTarWithContent(t, "config-plugin", v2Manifest, []byte("binary v2"), pubkeyBytes, sk.SecretKey, sk.KeyID)
	if _, err := inst.Install(context.Background(), tarPath2); err != nil {
		t.Fatalf("Install v2 (with newly required config field): %v", err)
	}

	events, err := q.ListPluginAuditEventsByType(context.Background(), db.ListPluginAuditEventsByTypeParams{
		EventType: auditManifestMaterialChange,
		Offset:    0,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("expected a plugin_manifest_material_change audit event, got none")
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(events[0].PayloadJson), &payload); err != nil {
		t.Fatalf("parse audit payload: %v", err)
	}
	newlyRequired, ok := payload["newly_required_config_fields"].([]any)
	if !ok || len(newlyRequired) == 0 {
		t.Errorf("expected newly_required_config_fields to contain api_key, got %v", payload["newly_required_config_fields"])
		return
	}
	if newlyRequired[0] != "api_key" {
		t.Errorf("newly_required_config_fields[0] = %v, want api_key", newlyRequired[0])
	}
}

// TestInstall_HotReload_CosmeticChange_UpdatesSnapshot_EmitsInfoAuditEvent verifies
// that a cosmetic-only change (description update) updates the snapshot and emits
// a plugin_manifest_cosmetic_change info-severity audit event.
func TestInstall_HotReload_CosmeticChange_UpdatesSnapshot_EmitsInfoAuditEvent(t *testing.T) {
	q := openTestDB(t)
	inst := newTestInstaller(t, q, false)

	pk, sk, err := signing.GenerateKeypair(rand.Reader)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	pubkeyBytes := signing.MarshalPublicKey(pk, "test key")

	v1Manifest := []byte("schema_version: v1\nname: cosm-plugin\nversion: 1.0.0\ndescription: old description\nservices:\n  tool: v1\nauth:\n  mode: instance_credentials\n  strategy: none\n")
	tarPath1 := buildSignedTarWithContent(t, "cosm-plugin", v1Manifest, []byte("binary v1"), pubkeyBytes, sk.SecretKey, sk.KeyID)
	if _, err := inst.Install(context.Background(), tarPath1); err != nil {
		t.Fatalf("Install v1: %v", err)
	}

	// v2 changes only the description and version — both cosmetic.
	v2Manifest := []byte("schema_version: v1\nname: cosm-plugin\nversion: 1.0.1\ndescription: new description\nservices:\n  tool: v1\nauth:\n  mode: instance_credentials\n  strategy: none\n")
	tarPath2 := buildSignedTarWithContent(t, "cosm-plugin", v2Manifest, []byte("binary v2"), pubkeyBytes, sk.SecretKey, sk.KeyID)
	if _, err := inst.Install(context.Background(), tarPath2); err != nil {
		t.Fatalf("Install v2 (cosmetic change): %v", err)
	}

	row2, err := q.GetPluginByName(context.Background(), "cosm-plugin")
	if err != nil {
		t.Fatalf("GetPluginByName after v2: %v", err)
	}
	if row2.PluginVersion != "1.0.1" {
		t.Errorf("plugin_version = %q, want 1.0.1 (cosmetic update must advance version)", row2.PluginVersion)
	}
	if !strings.Contains(row2.ManifestSnapshot, "new description") {
		t.Error("manifest_snapshot must be updated to v2 content on cosmetic change")
	}

	events, err := q.ListPluginAuditEventsByType(context.Background(), db.ListPluginAuditEventsByTypeParams{
		EventType: auditManifestCosmeticChange,
		Offset:    0,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("list cosmetic audit events: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("expected a plugin_manifest_cosmetic_change audit event, got none")
	}
	if events[0].Severity != "info" {
		t.Errorf("audit severity = %q, want info", events[0].Severity)
	}
}

// TestInstall_HotReload_NoChange_NoOp verifies that a tarball with a new version but
// structurally identical manifest content (differing only in version field) uses the
// cosmetic path and does not emit a material-change event.
func TestInstall_HotReload_NoChange_NoOp(t *testing.T) {
	q := openTestDB(t)
	inst := newTestInstaller(t, q, false)

	pk, sk, err := signing.GenerateKeypair(rand.Reader)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	pubkeyBytes := signing.MarshalPublicKey(pk, "test key")

	v1Manifest := []byte("schema_version: v1\nname: noop-plugin\nversion: 1.0.0\nservices:\n  tool: v1\nauth:\n  mode: instance_credentials\n  strategy: none\n")
	tarPath1 := buildSignedTarWithContent(t, "noop-plugin", v1Manifest, []byte("binary v1"), pubkeyBytes, sk.SecretKey, sk.KeyID)
	if _, err := inst.Install(context.Background(), tarPath1); err != nil {
		t.Fatalf("Install v1: %v", err)
	}

	// v2 only bumps the version — cosmetic, no structural change.
	v2Manifest := []byte("schema_version: v1\nname: noop-plugin\nversion: 2.0.0\nservices:\n  tool: v1\nauth:\n  mode: instance_credentials\n  strategy: none\n")
	tarPath2 := buildSignedTarWithContent(t, "noop-plugin", v2Manifest, []byte("binary v2"), pubkeyBytes, sk.SecretKey, sk.KeyID)
	if _, err := inst.Install(context.Background(), tarPath2); err != nil {
		t.Fatalf("Install v2 (version-only bump): %v", err)
	}

	// No material-change event must have been emitted.
	events, err := q.ListPluginAuditEventsByType(context.Background(), db.ListPluginAuditEventsByTypeParams{
		EventType: auditManifestMaterialChange,
		Offset:    0,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("list material events: %v", err)
	}
	if len(events) > 0 {
		t.Errorf("got %d material-change events for version-only bump, want 0", len(events))
	}
}

// ── Binary-path persistence tests (#386) ─────────────────────────────────────

// TestInstall_PersistsBinaryPath verifies that a fresh install writes
// binary_path = <pluginsDir>/installed/<name>/<name> to the plugins row and
// that the file is present on disk.
func TestInstall_PersistsBinaryPath(t *testing.T) {
	q := openTestDB(t)
	inst := newTestInstaller(t, q, false)

	tarPath, _ := signedPluginTarball(t, "path-plugin", "1.0.0")

	if _, err := inst.Install(context.Background(), tarPath); err != nil {
		t.Fatalf("Install: %v", err)
	}

	row, err := q.GetPluginByName(context.Background(), "path-plugin")
	if err != nil {
		t.Fatalf("GetPluginByName: %v", err)
	}

	if row.BinaryPath == nil || *row.BinaryPath == "" {
		t.Fatal("binary_path: want non-empty after first install, got nil/empty")
	}

	wantSuffix := filepath.Join("installed", "path-plugin", "path-plugin")
	if !strings.HasSuffix(*row.BinaryPath, wantSuffix) {
		t.Errorf("binary_path = %q, want suffix %q", *row.BinaryPath, wantSuffix)
	}

	if _, err := os.Stat(*row.BinaryPath); err != nil {
		t.Errorf("binary at persisted path not accessible: %v", err)
	}
}

// TestInstall_VersionBumpReplacesBinary verifies that installing a version bump
// over an existing plugin replaces the binary under installed/ and updates
// binary_path in the DB to the new file.
func TestInstall_VersionBumpReplacesBinary(t *testing.T) {
	q := openTestDB(t)
	inst := newTestInstaller(t, q, false)

	pk, sk, err := signing.GenerateKeypair(rand.Reader)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	pubkeyBytes := signing.MarshalPublicKey(pk, "test key")

	buildTar := func(t *testing.T, version string, binaryContent []byte) string {
		t.Helper()
		manifestBytes := []byte("schema_version: v1\nname: bump2-plugin\nversion: " + version + "\nservices:\n  tool: v1\nauth:\n  mode: instance_credentials\n  strategy: none\n")
		payload := signing.PluginPayload(binaryContent, manifestBytes)
		sig, signErr := signing.Sign(sk.SecretKey, sk.KeyID, payload, "trusted comment")
		if signErr != nil {
			t.Fatalf("sign: %v", signErr)
		}
		sigBytes := signing.MarshalSignature(sig, "test sig")
		tarPath := filepath.Join(t.TempDir(), "bump2-plugin-"+version+".tar.gz")
		writeTarball(t, tarPath, []tarEntry{
			{name: "manifest.yaml", content: manifestBytes, mode: 0o644},
			{name: "bump2-plugin", content: binaryContent, mode: 0o755},
			{name: "signing.pub", content: pubkeyBytes, mode: 0o644},
			{name: "bump2-plugin.minisig", content: sigBytes, mode: 0o644},
		})
		return tarPath
	}

	tarPath1 := buildTar(t, "1.0.0", []byte("binary content v1"))
	if _, err := inst.Install(context.Background(), tarPath1); err != nil {
		t.Fatalf("Install v1: %v", err)
	}

	tarPath2 := buildTar(t, "2.0.0", []byte("binary content v2"))
	if _, err := inst.Install(context.Background(), tarPath2); err != nil {
		t.Fatalf("Install v2: %v", err)
	}

	row, err := q.GetPluginByName(context.Background(), "bump2-plugin")
	if err != nil {
		t.Fatalf("GetPluginByName after v2: %v", err)
	}

	if row.BinaryPath == nil {
		t.Fatal("binary_path: nil after version bump")
	}

	// The binary at the persisted path should contain v2 content.
	content, readErr := os.ReadFile(*row.BinaryPath)
	if readErr != nil {
		t.Fatalf("read binary at persisted path: %v", readErr)
	}
	if string(content) != "binary content v2" {
		t.Errorf("binary content = %q, want v2", string(content))
	}

	// The .old directory must be cleaned up.
	oldDir := filepath.Join(inst.pluginsDir, "installed", "bump2-plugin.old")
	if _, err := os.Stat(oldDir); err == nil {
		t.Error("expected .old dir to be removed after version bump, but it exists")
	}
}

// TestInstall_RejectedBundleDoesNotPersist verifies that a bad-signature install
// does not create any directory under <pluginsDir>/installed/.
func TestInstall_RejectedBundleDoesNotPersist(t *testing.T) {
	q := openTestDB(t)
	inst := newTestInstaller(t, q, false)

	tarPath := badSignatureTarball(t, "reject-plugin", "1.0.0")

	if _, err := inst.Install(context.Background(), tarPath); err != nil {
		t.Fatalf("Install (bad sig): %v", err)
	}

	installedDir := filepath.Join(inst.pluginsDir, "installed", "reject-plugin")
	if _, err := os.Stat(installedDir); err == nil {
		t.Error("expected no installed dir for rejected bundle, but it exists")
	}
}

// TestInstall_LegacyRowBackfill verifies that installing the same version over a
// row that has binary_path=NULL backfills the column without a version bump.
func TestInstall_LegacyRowBackfill(t *testing.T) {
	q := openTestDB(t)
	inst := newTestInstaller(t, q, false)

	tarPath, _ := signedPluginTarball(t, "backfill-plugin", "1.0.0")

	// First install to create the row.
	if _, err := inst.Install(context.Background(), tarPath); err != nil {
		t.Fatalf("Install v1: %v", err)
	}

	// Manually clear binary_path to simulate a legacy row from before #386.
	row, err := q.GetPluginByName(context.Background(), "backfill-plugin")
	if err != nil {
		t.Fatalf("GetPluginByName: %v", err)
	}
	if _, err := q.UpdatePluginBinaryPath(context.Background(), db.UpdatePluginBinaryPathParams{
		BinaryPath:      nil,
		UpdatedAt:       row.UpdatedAt,
		ID:              row.ID,
		ExpectedVersion: row.Version,
	}); err != nil {
		t.Fatalf("clear binary_path: %v", err)
	}

	// Re-install the same version — should backfill binary_path.
	if _, err := inst.Install(context.Background(), tarPath); err != nil {
		t.Fatalf("Install (same version, backfill): %v", err)
	}

	row2, err := q.GetPluginByName(context.Background(), "backfill-plugin")
	if err != nil {
		t.Fatalf("GetPluginByName after backfill: %v", err)
	}

	if row2.BinaryPath == nil || *row2.BinaryPath == "" {
		t.Error("binary_path: still nil after backfill re-install, expected non-empty")
	}
}

// TestInstall_OnInstalled_CalledAfterSuccessfulInstall verifies that the
// OnInstalled callback fires with the correct plugin ID after a successful install
// and does not fire for rejected bundles.
func TestInstall_OnInstalled_CalledAfterSuccessfulInstall(t *testing.T) {
	q := openTestDB(t)
	inst := newTestInstaller(t, q, false)

	var calledWith []string
	inst.OnInstalled(func(_ context.Context, pluginID string) {
		calledWith = append(calledWith, pluginID)
	})

	tarPath, _ := signedPluginTarball(t, "hook-plugin", "1.0.0")

	pluginID, err := inst.Install(context.Background(), tarPath)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	if len(calledWith) != 1 {
		t.Fatalf("OnInstalled called %d times, want 1", len(calledWith))
	}
	if calledWith[0] != pluginID {
		t.Errorf("OnInstalled plugin ID = %q, want %q", calledWith[0], pluginID)
	}

	// Rejected install must not fire the callback.
	badTarPath := badSignatureTarball(t, "hook-plugin", "2.0.0")
	if _, err := inst.Install(context.Background(), badTarPath); err != nil {
		t.Fatalf("Install (bad sig): %v", err)
	}
	if len(calledWith) != 1 {
		t.Errorf("OnInstalled called %d times after rejected install, want 1 (no new call)", len(calledWith))
	}
}

// TestInstall_PubkeyMismatch_DoesNotOverwriteBinary is a regression test for the
// TOFU bypass described in cycle-1 review feedback: a pubkey-mismatch install must
// NOT overwrite the on-disk binary or update binary_path in the DB. Before the fix,
// publishBundle ran unconditionally before upsertPlugin, so the file at the
// previously-verified binary_path was silently replaced by the untrusted binary.
// After a server restart, StartAllActive would spawn the unaccepted binary.
func TestInstall_PubkeyMismatch_DoesNotOverwriteBinary(t *testing.T) {
	q := openTestDB(t)
	inst := newTestInstaller(t, q, false)

	// Install v1 with key A.
	tarPath1, _ := signedPluginTarball(t, "tofu-guard-plugin", "1.0.0")
	if _, err := inst.Install(context.Background(), tarPath1); err != nil {
		t.Fatalf("Install v1: %v", err)
	}

	row1, err := q.GetPluginByName(context.Background(), "tofu-guard-plugin")
	if err != nil {
		t.Fatalf("GetPluginByName after v1: %v", err)
	}
	if row1.BinaryPath == nil {
		t.Fatal("binary_path nil after v1 install")
	}
	v1Path := *row1.BinaryPath
	v1Content, err := os.ReadFile(v1Path)
	if err != nil {
		t.Fatalf("read v1 binary: %v", err)
	}

	// Attempt v2 signed by a different key (signedPluginTarball generates a fresh keypair each call).
	tarPath2, _ := signedPluginTarball(t, "tofu-guard-plugin", "2.0.0")
	if _, err := inst.Install(context.Background(), tarPath2); err != nil {
		t.Fatalf("Install v2 (different key): %v", err)
	}

	row2, err := q.GetPluginByName(context.Background(), "tofu-guard-plugin")
	if err != nil {
		t.Fatalf("GetPluginByName after v2 mismatch: %v", err)
	}

	// binary_path must be unchanged.
	if row2.BinaryPath == nil || *row2.BinaryPath != v1Path {
		got := "<nil>"
		if row2.BinaryPath != nil {
			got = *row2.BinaryPath
		}
		t.Errorf("binary_path = %q after pubkey-mismatch install, want %q (unchanged)", got, v1Path)
	}

	// The file at the persisted path must still contain v1 content.
	afterContent, err := os.ReadFile(v1Path)
	if err != nil {
		t.Fatalf("read binary after mismatch install: %v", err)
	}
	if string(afterContent) != string(v1Content) {
		t.Error("binary content changed after pubkey-mismatch install; TOFU bypass regression")
	}

	// The installed dir must not contain a .new or .old artefact (no partial publish).
	installedDir := filepath.Join(inst.pluginsDir, "installed", "tofu-guard-plugin")
	for _, suffix := range []string{".new", ".old"} {
		if _, statErr := os.Stat(installedDir + suffix); statErr == nil {
			t.Errorf("unexpected artefact %s after pubkey-mismatch install", installedDir+suffix)
		}
	}
}

// TestInstall_MaterialChange_DoesNotOverwriteBinary verifies that a material-change
// install does not overwrite the on-disk binary or update binary_path in the DB.
// Mirrors TestInstall_PubkeyMismatch_DoesNotOverwriteBinary for the material-change path.
func TestInstall_MaterialChange_DoesNotOverwriteBinary(t *testing.T) {
	q := openTestDB(t)
	inst := newTestInstaller(t, q, false)

	pk, sk, err := signing.GenerateKeypair(rand.Reader)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	pubkeyBytes := signing.MarshalPublicKey(pk, "test key")

	buildTar := func(t *testing.T, version string, manifestContent, binaryContent []byte) string {
		t.Helper()
		return buildSignedTarWithContent(t, "matguard-plugin", manifestContent, binaryContent, pubkeyBytes, sk.SecretKey, sk.KeyID)
	}

	v1Manifest := []byte("schema_version: v1\nname: matguard-plugin\nversion: 1.0.0\nservices:\n  tool: v1\nauth:\n  mode: instance_credentials\n  strategy: none\n")
	tarPath1 := buildTar(t, "1.0.0", v1Manifest, []byte("binary content v1"))
	if _, err := inst.Install(context.Background(), tarPath1); err != nil {
		t.Fatalf("Install v1: %v", err)
	}

	row1, err := q.GetPluginByName(context.Background(), "matguard-plugin")
	if err != nil {
		t.Fatalf("GetPluginByName after v1: %v", err)
	}
	if row1.BinaryPath == nil {
		t.Fatal("binary_path nil after v1 install")
	}
	v1Path := *row1.BinaryPath

	// v2 adds a new tool — material change that must be blocked.
	v2Manifest := []byte("schema_version: v1\nname: matguard-plugin\nversion: 2.0.0\nservices:\n  tool: v2\nauth:\n  mode: instance_credentials\n  strategy: none\ntools:\n- name: my_tool\n  description: new tool\n")
	tarPath2 := buildTar(t, "2.0.0", v2Manifest, []byte("binary content v2"))
	if _, err := inst.Install(context.Background(), tarPath2); err != nil {
		t.Fatalf("Install v2 (material change): %v", err)
	}

	row2, err := q.GetPluginByName(context.Background(), "matguard-plugin")
	if err != nil {
		t.Fatalf("GetPluginByName after v2: %v", err)
	}

	// binary_path must be unchanged.
	if row2.BinaryPath == nil || *row2.BinaryPath != v1Path {
		got := "<nil>"
		if row2.BinaryPath != nil {
			got = *row2.BinaryPath
		}
		t.Errorf("binary_path = %q after material-change install, want %q (unchanged)", got, v1Path)
	}

	// The file at the persisted path must still contain v1 content.
	afterContent, err := os.ReadFile(v1Path)
	if err != nil {
		t.Fatalf("read binary after material-change install: %v", err)
	}
	if string(afterContent) != "binary content v1" {
		t.Errorf("binary content = %q after material-change install, want v1 content", string(afterContent))
	}
}

// TestInstall_OnInstalled_NotCalledForPubkeyMismatch verifies that the OnInstalled
// callback does not fire when an install is blocked by a pubkey mismatch.
// This is the hook counterpart of TestInstall_PubkeyMismatch_DoesNotOverwriteBinary.
func TestInstall_OnInstalled_NotCalledForPubkeyMismatch(t *testing.T) {
	q := openTestDB(t)
	inst := newTestInstaller(t, q, false)

	callCount := 0
	inst.OnInstalled(func(_ context.Context, _ string) { callCount++ })

	// Install v1 — callback fires once.
	tarPath1, _ := signedPluginTarball(t, "hook-mismatch-plugin", "1.0.0")
	if _, err := inst.Install(context.Background(), tarPath1); err != nil {
		t.Fatalf("Install v1: %v", err)
	}
	if callCount != 1 {
		t.Fatalf("after v1 install: callback count = %d, want 1", callCount)
	}

	// Install v2 with a different key — pubkey mismatch, callback must NOT fire.
	tarPath2, _ := signedPluginTarball(t, "hook-mismatch-plugin", "2.0.0")
	if _, err := inst.Install(context.Background(), tarPath2); err != nil {
		t.Fatalf("Install v2 (different key): %v", err)
	}
	if callCount != 1 {
		t.Errorf("after pubkey-mismatch install: callback count = %d, want 1 (no new call)", callCount)
	}
}

// nestedSignedPluginTarball builds a fully signed tarball in the nested layout
// that "gleipnir-plugin package" produces: every file lives under a single
// top-level directory named "<name>-<version>/".
func nestedSignedPluginTarball(t *testing.T, name, version string) string {
	t.Helper()

	prefix := name + "-" + version + "/"
	manifestBytes := []byte("schema_version: v1\nname: " + name + "\nversion: " + version + "\nservices:\n  tool: v1\nauth:\n  mode: instance_credentials\n  strategy: none\n")
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

	tarPath := filepath.Join(t.TempDir(), name+".tar.gz")
	writeTarball(t, tarPath, []tarEntry{
		{name: prefix, typeflag: tar.TypeDir, mode: 0o755},
		{name: prefix + "manifest.yaml", content: manifestBytes, mode: 0o644},
		{name: prefix + name, content: binaryBytes, mode: 0o755},
		{name: prefix + "signing.pub", content: pubkeyBytes, mode: 0o644},
		{name: prefix + name + ".minisig", content: sigBytes, mode: 0o644},
	})
	return tarPath
}

// TestInstall_NestedLayout_Installs verifies that a tarball where every file
// lives under a single top-level "<name>-<version>/" directory (the layout
// produced by "gleipnir-plugin package") installs successfully. This is the
// primary regression test for issue #387.
func TestInstall_NestedLayout_Installs(t *testing.T) {
	q := openTestDB(t)
	inst := newTestInstaller(t, q, false)

	tarPath := nestedSignedPluginTarball(t, "nested-plugin", "1.0.0")

	if _, err := inst.Install(context.Background(), tarPath); err != nil {
		t.Fatalf("Install (nested layout): %v", err)
	}

	row, err := q.GetPluginByName(context.Background(), "nested-plugin")
	if err != nil {
		t.Fatalf("GetPluginByName: %v", err)
	}
	if row.Status != "active" {
		t.Errorf("status = %q, want active", row.Status)
	}
	if row.PluginVersion != "1.0.0" {
		t.Errorf("plugin_version = %q, want 1.0.0", row.PluginVersion)
	}
	if row.BinaryPath == nil || *row.BinaryPath == "" {
		t.Error("binary_path: want non-empty after nested-layout install")
	}
	// The published binary must be accessible at the persisted path.
	if _, err := os.Stat(*row.BinaryPath); err != nil {
		t.Errorf("binary at persisted path not accessible: %v", err)
	}
}

// TestInstall_FlatLayout_BackwardCompat verifies that a tarball with a flat
// layout (manifest.yaml at the tarball root) continues to install successfully.
// This is an explicit contract test so any future change that breaks flat
// tarballs is caught immediately.
func TestInstall_FlatLayout_BackwardCompat(t *testing.T) {
	q := openTestDB(t)
	inst := newTestInstaller(t, q, false)

	tarPath, _ := signedPluginTarball(t, "flat-plugin", "1.0.0")

	if _, err := inst.Install(context.Background(), tarPath); err != nil {
		t.Fatalf("Install (flat layout): %v", err)
	}

	row, err := q.GetPluginByName(context.Background(), "flat-plugin")
	if err != nil {
		t.Fatalf("GetPluginByName: %v", err)
	}
	if row.Status != "active" {
		t.Errorf("status = %q, want active", row.Status)
	}
	if row.BinaryPath == nil || *row.BinaryPath == "" {
		t.Error("binary_path: want non-empty after flat-layout install")
	}
}

// TestInstall_InvalidLayout_ReturnsError verifies that a tarball with neither a
// flat nor a single-nested-directory layout returns a clear error mentioning the
// bundle layout, rather than a confusing "no such file" error.
func TestInstall_InvalidLayout_ReturnsError(t *testing.T) {
	q := openTestDB(t)
	inst := newTestInstaller(t, q, true) // allowUnsigned so verifier is not the rejection path

	// Build a tarball with two top-level directories — neither flat nor single-nested.
	manifestBytes := []byte("schema_version: v1\nname: broken-plugin\nversion: 1.0.0\nservices:\n  tool: v1\nauth:\n  mode: instance_credentials\n  strategy: none\n")
	tarPath := filepath.Join(t.TempDir(), "broken.tar.gz")
	writeTarball(t, tarPath, []tarEntry{
		{name: "dir-a/", typeflag: tar.TypeDir, mode: 0o755},
		{name: "dir-a/manifest.yaml", content: manifestBytes, mode: 0o644},
		{name: "dir-b/", typeflag: tar.TypeDir, mode: 0o755},
		{name: "dir-b/other.txt", content: []byte("irrelevant"), mode: 0o644},
	})

	_, err := inst.Install(context.Background(), tarPath)
	if err == nil {
		t.Fatal("Install: expected error for invalid layout, got nil")
	}
	if !strings.Contains(err.Error(), "bundle root") {
		t.Errorf("Install error = %q; want it to mention bundle layout (\"bundle root\")", err.Error())
	}
}

// TestInstall_TmpDirUnderPluginsDir verifies that the extraction temp directory is
// created inside pluginsDir (not os.TempDir()), ensuring the rename to the staging
// path stays on the same filesystem and avoids EXDEV on Docker where /tmp and
// /plugins are separate devices.
func TestInstall_TmpDirUnderPluginsDir(t *testing.T) {
	q := openTestDB(t)
	pluginsDir := t.TempDir()
	v := &realVerifier{allowUnsigned: false}
	inst := NewInstaller(v, q, nil, nil, pluginsDir)
	inst.clock = func() time.Time { return time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC) }

	tarPath, _ := signedPluginTarball(t, "fsdev-plugin", "1.0.0")

	if _, err := inst.Install(context.Background(), tarPath); err != nil {
		t.Fatalf("Install: %v", err)
	}

	// The staging path used by publishBundle is <pluginsDir>/installed/<name>.new —
	// verify it was cleaned up (renamed to dest), confirming the rename stayed on-device.
	stagingPath := filepath.Join(pluginsDir, "installed", "fsdev-plugin.new")
	if _, err := os.Stat(stagingPath); err == nil {
		t.Error("staging path still exists after successful install; rename may have failed")
	}

	// The final published binary must be under pluginsDir, not under os.TempDir().
	row, err := q.GetPluginByName(context.Background(), "fsdev-plugin")
	if err != nil {
		t.Fatalf("GetPluginByName: %v", err)
	}
	if row.BinaryPath == nil {
		t.Fatal("binary_path: nil after install")
	}
	if !strings.HasPrefix(*row.BinaryPath, pluginsDir) {
		t.Errorf("binary_path = %q; want prefix %q (must be under pluginsDir, not /tmp)", *row.BinaryPath, pluginsDir)
	}
}
