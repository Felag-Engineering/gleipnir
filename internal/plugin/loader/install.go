package loader

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/infra/event"
	"github.com/felag-engineering/gleipnir/internal/model"
	pluginmanifest "github.com/felag-engineering/gleipnir/internal/plugin/manifest"
	pluginstate "github.com/felag-engineering/gleipnir/internal/plugin/state"
	manifest "github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
	"github.com/felag-engineering/gleipnir/plugin-sdk/signing"
)

// VerifyOutcome mirrors internal/plugin.VerifyOutcome without creating an import cycle.
type VerifyOutcome int

const (
	OutcomeVerified           VerifyOutcome = iota // bundle signed and valid
	OutcomeUnsignedPermissive                      // unsigned but host allows it
	OutcomeRejected                                // verification failed
)

func (o VerifyOutcome) String() string {
	switch o {
	case OutcomeVerified:
		return "verified"
	case OutcomeUnsignedPermissive:
		return "unsigned_permissive"
	case OutcomeRejected:
		return "rejected"
	default:
		return "unknown"
	}
}

// VerifyResult is the minimal result the installer needs from the verifier.
type VerifyResult struct {
	Outcome VerifyOutcome
	Pubkey  []byte
	Err     error
}

// BundleVerifier is the interface the Installer requires from the host verifier.
// internal/plugin.Verifier satisfies this interface; using an interface here
// avoids the import cycle between internal/plugin and internal/plugin/loader.
type BundleVerifier interface {
	// VerifyBundle verifies the plugin bundle rooted at bundleDir against the
	// binary at binaryPath. Err must be non-nil when Outcome == OutcomeRejected.
	// Pubkey must be non-nil and non-empty when Outcome == OutcomeVerified
	// (TOFU guarantee per ADR-045 — without this, trusted_pubkey could be stored
	// as empty string and silently break the key-pinning path in #188).
	VerifyBundle(bundleDir, binaryPath string) VerifyResult
}

const (
	// maxTarballBytes caps cumulative uncompressed bytes extracted from a plugin
	// tarball. Defends against gzip-bomb payloads (spec §5.1 size guidance).
	maxTarballBytes = 100 << 20 // 100 MiB

	// Plugin status values that mirror the DB CHECK constraint.
	statusPendingReview = "pending_review"
	statusActive        = "active"

	// Audit event types (ADR-046).
	auditSignatureInvalid       = "plugin_signature_invalid"
	auditPluginInstalled        = "plugin_installed"
	auditUpdatePending          = "plugin_update_pending"
	auditPubkeyMismatch         = "plugin_pubkey_mismatch"
	auditManifestMaterialChange = "plugin_manifest_material_change"
	auditManifestCosmeticChange = "plugin_manifest_cosmetic_change"

	// Audit severity levels.
	severityHigh = "high"
	severityInfo = "info"
)

// Installer runs the extract → verify → snapshot-into-DB pipeline for a single
// plugin tarball. One Installer is shared across all tarballs dispatched by the
// Watcher; Install is called sequentially (ADR-003 serialization via the
// watcher's fire channel).
type Installer struct {
	verifier   BundleVerifier
	q          *db.Queries
	publisher  event.Publisher
	pluginsDir string
	// onInstalled is called after every successful install with the plugin ID.
	// Nil-safe: if not set, no callback fires. Wire via OnInstalled().
	onInstalled func(ctx context.Context, pluginID string)
	// clock is injectable so tests can assert on timestamps without racing real time.
	clock func() time.Time
}

// NewInstaller returns an Installer wired to the given verifier, query set,
// event publisher, and plugins directory. publisher may be nil — state change
// events are skipped when nil. pluginsDir is the root under which extracted
// binaries are published to <pluginsDir>/installed/<plugin-name>/; pass an
// empty string to disable binary publishing (useful for tests that don't need
// real binaries on disk).
func NewInstaller(v BundleVerifier, q *db.Queries, publisher event.Publisher, pluginsDir string) *Installer {
	return &Installer{verifier: v, q: q, publisher: publisher, pluginsDir: pluginsDir, clock: time.Now}
}

// OnInstalled registers a callback that fires after every successful Install.
// The callback receives the context and the newly-installed plugin ID.
// This is how main.go wires the post-install subprocess spawn (issue #386):
//
//	installer.OnInstalled(func(ctx context.Context, pluginID string) {
//	    mgr.StartByPluginID(ctx, pluginID)
//	})
//
// Replaces any previously registered callback. Nil-safe: passing nil disables
// the callback.
func (in *Installer) OnInstalled(fn func(ctx context.Context, pluginID string)) {
	in.onInstalled = fn
}

// Install runs the full install pipeline for the tarball at tarPath:
//  1. Extract to a temp directory inside pluginsDir (same filesystem as the
//     publish target, so os.Rename stays on-device — avoids EXDEV in Docker
//     where /tmp and /plugins are on separate filesystems).
//  2. Parse manifest.yaml.
//  3. Verify the bundle signature.
//  4. Snapshot into the plugins table. Commit branches (createPlugin,
//     updatePlugin, updatePluginCosmetic) atomically publish the verified
//     bundle to <pluginsDir>/installed/<name>/ inside the same code path.
//     Rejection branches (pubkey-mismatch, material-change) never publish.
//
// Returns the plugin row ID on success. Returns ("", nil) when verification
// fails — the audit event (not an error) is the operator-visible signal
// (ADR-046). The caller (Watcher) continues after a failed install; nothing is started.
//
// NOTE: The schema CHECK constraint on plugins.status only allows
// pending_review|active|removed. A "signature_invalid" status is not stored
// on the plugin row — #191 owns the per-instance health state machine.
func (in *Installer) Install(ctx context.Context, tarPath string) (string, error) {
	// Create the extraction temp dir inside pluginsDir so the rename to the
	// staging path stays on the same filesystem (avoids EXDEV on Docker where
	// /tmp and /plugins are separate devices). Fall back to os.TempDir() when
	// pluginsDir is empty (test-only path).
	extractParent := os.TempDir()
	if in.pluginsDir != "" {
		if err := os.MkdirAll(in.pluginsDir, 0o755); err != nil {
			return "", fmt.Errorf("ensure plugins dir: %w", err)
		}
		extractParent = in.pluginsDir
	}

	tmpDir, err := os.MkdirTemp(extractParent, "incoming-*")
	if err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	if err := ExtractTarball(tarPath, tmpDir, maxTarballBytes); err != nil {
		return "", fmt.Errorf("extract tarball %q: %w", tarPath, err)
	}

	// Resolve the bundle root: some packagers (e.g. gleipnir-plugin package) wrap
	// everything under a single top-level directory (e.g. slack-0.1.1/). Walk
	// into that directory when present so the rest of the pipeline sees a flat root.
	bundleDir, err := resolveBundleRoot(tmpDir)
	if err != nil {
		return "", fmt.Errorf("resolve bundle root in %q: %w", tarPath, err)
	}

	m, manifestBytes, err := readManifest(bundleDir)
	if err != nil {
		return "", fmt.Errorf("read manifest from %q: %w", tarPath, err)
	}

	binaryPath := filepath.Join(bundleDir, m.Name)
	if rel, err := filepath.Rel(bundleDir, binaryPath); err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("manifest.name %q escapes bundle directory", m.Name)
	}
	result := in.verifier.VerifyBundle(bundleDir, binaryPath)

	if result.Outcome == OutcomeRejected {
		return in.recordSignatureInvalid(ctx, tarPath, m.Name, result.Err)
	}

	// upsertPlugin handles commit vs. rejection branching. Only commit branches
	// (createPlugin, updatePlugin, updatePluginCosmetic) publish the bundle and
	// invoke onInstalled. Rejection branches (pubkey-mismatch, material-change)
	// return committed=false so the disk and hook remain untouched.
	pluginID, committed, err := in.upsertPlugin(ctx, m, manifestBytes, result, bundleDir)
	if err != nil {
		return "", err
	}

	// Notify the post-install hook (e.g. to spawn the subprocess) only when the
	// install actually committed a new DB row or manifest update. Rejection paths
	// (pubkey-mismatch, material-change) must not trigger a spawn.
	if committed && pluginID != "" && in.onInstalled != nil {
		in.onInstalled(ctx, pluginID)
	}
	return pluginID, nil
}

// publishBundle moves the extracted and verified bundle from tmpDir to a stable
// location under <pluginsDir>/installed/<name>/ using a rename swap-aside
// sequence so the directory is replaced atomically from the perspective of
// concurrent readers:
//
//  1. Rename tmpDir → <dest>.new  (moves the whole tree in one syscall).
//  2. If <dest> exists: rename <dest> → <dest>.old (displaces the predecessor).
//  3. Rename <dest>.new → <dest>   (publishes the new bundle).
//  4. RemoveAll <dest>.old          (best-effort cleanup of the predecessor).
//
// Returns the absolute path of the published binary (<dest>/<name>).
//
// The existing "defer os.RemoveAll(tmpDir)" in Install is a no-op once
// tmpDir has been renamed away — os.RemoveAll on a missing path returns nil.
//
// On rename failure mid-sequence this function attempts to roll back by
// renaming <dest>.old back to <dest>, then returns an error.
func (in *Installer) publishBundle(ctx context.Context, tmpDir, name string) (string, error) {
	installedRoot := filepath.Join(in.pluginsDir, "installed")
	if err := os.MkdirAll(installedRoot, 0o755); err != nil {
		return "", fmt.Errorf("create installed dir: %w", err)
	}

	dest := filepath.Join(installedRoot, name)
	staging := dest + ".new"
	old := dest + ".old"

	// Step 1: move tmpDir → staging. After this, tmpDir no longer exists so
	// the deferred RemoveAll in Install is a harmless no-op.
	if err := os.Rename(tmpDir, staging); err != nil {
		return "", fmt.Errorf("move bundle to staging: %w", err)
	}

	// Step 2: displace existing dest if present.
	displaced := false
	if _, err := os.Lstat(dest); err == nil {
		if renameErr := os.Rename(dest, old); renameErr != nil {
			// Roll back: restore staging to tmpDir so Install's defer can clean it.
			_ = os.Rename(staging, tmpDir)
			return "", fmt.Errorf("displace previous bundle: %w", renameErr)
		}
		displaced = true
	}

	// Step 3: publish staging → dest.
	if err := os.Rename(staging, dest); err != nil {
		if displaced {
			// Roll back: restore the old bundle so the plugin keeps running.
			_ = os.Rename(old, dest)
		}
		_ = os.Rename(staging, tmpDir)
		return "", fmt.Errorf("publish bundle: %w", err)
	}

	// Step 4: clean up the displaced predecessor. Best-effort — log on error but
	// don't fail the install; the old binary is no longer referenced.
	if displaced {
		if err := os.RemoveAll(old); err != nil {
			slog.WarnContext(ctx, "publishBundle: could not remove old bundle dir",
				"path", old, "err", err)
		}
	}

	return filepath.Join(dest, name), nil
}

// resolveBundleRoot determines which directory within the extracted tmpDir
// contains manifest.yaml. Two layouts are supported:
//
//   - Flat: manifest.yaml sits directly in tmpDir (legacy / manually-packaged tarballs).
//   - Nested: tmpDir contains exactly one subdirectory, and manifest.yaml lives
//     inside that subdirectory (layout produced by "gleipnir-plugin package").
//
// Standard packaging tools (npm, helm, cargo, git-archive) all wrap their
// payload under a single top-level directory; this mirrors that convention.
// Any other layout returns an error so operators get a clear message instead
// of a confusing "no such file" failure.
func resolveBundleRoot(tmpDir string) (string, error) {
	// Fast path: flat layout.
	if _, err := os.Stat(filepath.Join(tmpDir, "manifest.yaml")); err == nil {
		return tmpDir, nil
	}

	// Check for the single-directory nested layout.
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return "", fmt.Errorf("read extracted dir: %w", err)
	}
	if len(entries) == 1 && entries[0].IsDir() {
		nested := filepath.Join(tmpDir, entries[0].Name())
		if _, err := os.Stat(filepath.Join(nested, "manifest.yaml")); err == nil {
			return nested, nil
		}
	}

	return "", fmt.Errorf("manifest.yaml not found at bundle root or under a single top-level directory")
}

// readManifest parses the manifest.yaml inside the extracted bundle directory.
// Returns the parsed manifest, the raw YAML bytes, and any error.
func readManifest(bundleDir string) (*manifest.Manifest, []byte, error) {
	manifestPath := filepath.Join(bundleDir, "manifest.yaml")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read manifest.yaml: %w", err)
	}

	var m manifest.Manifest
	if err := manifest.Unmarshal(data, &m); err != nil {
		return nil, nil, fmt.Errorf("parse manifest.yaml: %w", err)
	}

	if m.Name == "" {
		return nil, nil, fmt.Errorf("manifest.name is required")
	}
	if strings.ContainsAny(m.Name, `/\`) || m.Name == ".." || m.Name == "." {
		return nil, nil, fmt.Errorf("manifest.name %q must be a plain filename (no path separators)", m.Name)
	}
	if m.Version == "" {
		return nil, nil, fmt.Errorf("manifest.version is required")
	}

	return &m, data, nil
}

// recordSignatureInvalid inserts a plugin_audit_events row for a failed
// signature check and returns ("", nil) — the event is the operator signal.
// An empty plugin ID is returned because no plugin row exists for a rejected bundle.
func (in *Installer) recordSignatureInvalid(ctx context.Context, tarPath, pluginName string, verifyErr error) (string, error) {
	// Guard against a BundleVerifier that returns OutcomeRejected with Err==nil,
	// which would panic on verifyErr.Error(). The interface contract requires
	// Err to be non-nil, but we defend here regardless.
	errMsg := "unknown rejection"
	if verifyErr != nil {
		errMsg = verifyErr.Error()
	}
	if err := in.recordAuditEvent(ctx, auditSignatureInvalid, severityHigh, in.nowStr(), map[string]any{
		"tarball":       tarPath,
		"manifest_name": pluginName,
		"error":         errMsg,
	}); err != nil {
		return "", fmt.Errorf("record signature_invalid audit: %w", err)
	}
	return "", nil
}

// upsertPlugin creates or updates the plugin row and emits an audit event.
// tmpDir is the resolved bundle root containing manifest.yaml — for flat
// tarballs this is the extraction directory itself, for nested tarballs it
// is the single top-level subdirectory inside the extraction directory (see
// resolveBundleRoot). Commit branches (createPlugin, updatePlugin,
// updatePluginCosmetic) call publishBundle to move it to its permanent
// location and record the path in the DB so Manager.StartAllActive can
// re-spawn the subprocess on server restart.
// Rejection branches (pubkey-mismatch, material-change) never publish and
// return committed=false.
// Returns (pluginID, committed, error).
func (in *Installer) upsertPlugin(ctx context.Context, m *manifest.Manifest, manifestBytes []byte, result VerifyResult, tmpDir string) (string, bool, error) {
	existing, err := in.q.GetPluginByName(ctx, m.Name)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", false, fmt.Errorf("get plugin %q: %w", m.Name, err)
	}

	nowStr := in.nowStr()

	if errors.Is(err, sql.ErrNoRows) {
		id, commitErr := in.createPlugin(ctx, m, manifestBytes, result, tmpDir, nowStr)
		return id, commitErr == nil && id != "", commitErr
	}

	if existing.PluginVersion == m.Version {
		// Same version already recorded — idempotent re-install (debounce safety net).
		// If the row has no binary_path yet (legacy row from before #386), backfill it
		// so a re-deploy of the same tarball repairs the gap without a version bump.
		if in.pluginsDir != "" && (existing.BinaryPath == nil || *existing.BinaryPath == "") {
			publishedPath, pubErr := in.publishBundle(ctx, tmpDir, m.Name)
			if pubErr != nil {
				return "", false, fmt.Errorf("publish bundle for %q (backfill): %w", m.Name, pubErr)
			}
			if err := in.updateBinaryPath(ctx, existing.ID, existing.Version, &publishedPath, nowStr); err != nil {
				return "", false, fmt.Errorf("backfill binary_path for %q: %w", m.Name, err)
			}
		}
		return existing.ID, false, nil
	}

	// TOFU pubkey trust check: only applies to verified (signed) bundles.
	if result.Outcome == OutcomeVerified {
		if existing.TrustedPubkey == "" {
			// Delayed TOFU: plugin was first installed under GLEIPNIR_ALLOW_UNSIGNED_PLUGINS=true
			// and a signed update has now arrived. Capture the pubkey silently — no mismatch event.
			rows, updateErr := in.q.UpdatePluginTrustedPubkey(ctx, db.UpdatePluginTrustedPubkeyParams{
				TrustedPubkey:   string(result.Pubkey),
				UpdatedAt:       nowStr,
				ID:              existing.ID,
				ExpectedVersion: existing.Version,
			})
			if updateErr != nil {
				return "", false, fmt.Errorf("capture trusted pubkey for %q (delayed TOFU): %w", m.Name, updateErr)
			}
			if rows == 0 {
				return "", false, fmt.Errorf("capture trusted pubkey for %q: CAS conflict (version mismatch)", m.Name)
			}
			// Re-read so updatePlugin sees the bumped version.
			existing, err = in.q.GetPluginByName(ctx, m.Name)
			if err != nil {
				return "", false, fmt.Errorf("re-read plugin %q after TOFU capture: %w", m.Name, err)
			}
		} else if !bytes.Equal(result.Pubkey, []byte(existing.TrustedPubkey)) {
			// Key mismatch: a different signing key was used for this update.
			// Block the update and transition all instances to pending_key_approval
			// so admins are alerted. Do not publish the bundle.
			id, mismatchErr := in.handlePubkeyMismatch(ctx, existing, result, m.Version, nowStr)
			return id, false, mismatchErr
		}
	}

	// Diff the incoming manifest against the stored snapshot before calling
	// updatePlugin. Material changes block the update and enter pending_manifest_approval.
	// Cosmetic-only changes update the snapshot silently.
	var oldManifest manifest.Manifest
	if parseErr := manifest.Unmarshal([]byte(existing.ManifestSnapshot), &oldManifest); parseErr != nil {
		return "", false, fmt.Errorf("parse stored manifest snapshot for %q: %w", m.Name, parseErr)
	}
	changes := pluginmanifest.Diff(&oldManifest, m)
	if pluginmanifest.HasMaterial(changes) {
		// Rejected path: do not publish the bundle or update binary_path.
		// The running generation continues using the previously-verified binary.
		id, matErr := in.handleManifestMaterialChange(ctx, existing, &oldManifest, m, manifestBytes, changes, nowStr)
		return id, false, matErr
	}
	if len(changes) > 0 {
		id, cosErr := in.updatePluginCosmetic(ctx, existing, m, manifestBytes, changes, tmpDir, nowStr)
		return id, cosErr == nil && id != "", cosErr
	}

	id, upErr := in.updatePlugin(ctx, existing, m, manifestBytes, result, tmpDir, nowStr)
	return id, upErr == nil && id != "", upErr
}

// handlePubkeyMismatch transitions all instances of the plugin to
// pending_key_approval and emits a plugin_pubkey_mismatch audit event.
// Returns the existing plugin ID — the update is blocked but the row still exists.
func (in *Installer) handlePubkeyMismatch(ctx context.Context, existing db.Plugin, result VerifyResult, newVersion, nowStr string) (string, error) {
	in.transitionAllInstances(
		ctx, existing,
		model.PluginHealthStatePendingKeyApproval,
		"signing key changed; admin approval required",
		"pubkey mismatch",
	)

	if err := in.recordAuditEvent(ctx, auditPubkeyMismatch, severityHigh, nowStr, map[string]any{
		"plugin_id":              existing.ID,
		"name":                   existing.Name,
		"old_pubkey_fingerprint": pubkeyFingerprint([]byte(existing.TrustedPubkey)),
		"new_pubkey_fingerprint": pubkeyFingerprint(result.Pubkey),
		"new_pubkey_b64":         base64.StdEncoding.EncodeToString(result.Pubkey),
		"version":                newVersion,
	}); err != nil {
		return "", fmt.Errorf("record pubkey_mismatch audit for %q: %w", existing.Name, err)
	}
	return existing.ID, nil
}

// handleManifestMaterialChange blocks the manifest update and transitions all
// eligible instances to pending_manifest_approval. The candidate manifest is
// persisted in the audit-event payload (as base64) rather than the plugins row;
// the running generation continues serving the existing snapshot until an admin
// calls POST /api/v1/admin/plugins/{id}/accept-manifest.
//
// oldManifest is the already-parsed existing snapshot — passed in to avoid a
// second parse of the same bytes that upsertPlugin already completed.
// Returns the existing plugin ID — the update is blocked but the row still exists.
func (in *Installer) handleManifestMaterialChange(ctx context.Context, existing db.Plugin, oldManifest *manifest.Manifest, m *manifest.Manifest, candidateBytes []byte, changes []pluginmanifest.Change, nowStr string) (string, error) {
	in.transitionAllInstances(
		ctx, existing,
		model.PluginHealthStatePendingManifestApproval,
		"manifest changed materially; admin re-approval required",
		"manifest material change",
	)

	if err := in.recordAuditEvent(ctx, auditManifestMaterialChange, severityHigh, nowStr, map[string]any{
		"plugin_id":                    existing.ID,
		"name":                         existing.Name,
		"old_version":                  existing.PluginVersion,
		"new_version":                  m.Version,
		"material_fields":              pluginmanifest.MaterialFields(changes),
		"cosmetic_fields":              pluginmanifest.CosmeticFields(changes),
		"candidate_manifest_b64":       base64.StdEncoding.EncodeToString(candidateBytes),
		"newly_required_config_fields": pluginmanifest.ConfigSchemaNewlyRequiredFields(oldManifest, m),
	}); err != nil {
		return "", fmt.Errorf("record manifest_material_change audit for %q: %w", existing.Name, err)
	}
	return existing.ID, nil
}

// transitionAllInstances moves every instance of the plugin to target if the
// state-machine graph permits the transition. List failures and per-instance
// CAS conflicts are logged and skipped — the caller's audit event is the
// operator's signal that the transition was attempted. logTag is included in
// the log lines so install-pipeline failures can be grep'd by reason.
func (in *Installer) transitionAllInstances(ctx context.Context, existing db.Plugin, target model.PluginHealthState, detail, logTag string) {
	instances, err := in.q.ListPluginInstancesByPlugin(ctx, existing.ID)
	if err != nil {
		slog.WarnContext(ctx, logTag+": list instances failed; skipping state transitions",
			"plugin", existing.Name, "err", err)
		return
	}
	for _, inst := range instances {
		current := model.PluginHealthState(inst.HealthState)
		if !pluginstate.IsLegalTransition(current, target) {
			continue
		}
		if stateErr := pluginstate.SetHealthState(ctx, in.q, in.publisher, inst.ID, pluginstate.OriginHost, target, detail); stateErr != nil {
			if !errors.Is(stateErr, pluginstate.ErrTransitionConflict) {
				slog.WarnContext(ctx, logTag+": set "+string(target)+" failed",
					"instance_id", inst.ID, "err", stateErr)
			}
		}
	}
}

// recordAuditEvent writes a plugin-level audit row (PluginInstanceID = nil,
// ActorUserID = nil — the install pipeline runs without an operator). Returns
// the InsertPluginAuditEvent error so callers can wrap it with their own context.
func (in *Installer) recordAuditEvent(ctx context.Context, eventType, severity, nowStr string, payload map[string]any) error {
	body, _ := json.Marshal(payload)
	_, err := in.q.InsertPluginAuditEvent(ctx, db.InsertPluginAuditEventParams{
		PluginInstanceID: nil,
		EventType:        eventType,
		Severity:         severity,
		ActorUserID:      nil,
		PayloadJson:      string(body),
		CreatedAt:        nowStr,
	})
	return err
}

// updatePluginCosmetic updates the manifest snapshot for a cosmetic-only change,
// preserving the current plugin status so an already-active plugin is not
// regressed to pending_review for a description tweak. Publishes the verified
// bundle to disk and updates binary_path after the manifest CAS succeeds.
func (in *Installer) updatePluginCosmetic(ctx context.Context, existing db.Plugin, m *manifest.Manifest, manifestBytes []byte, changes []pluginmanifest.Change, tmpDir string, nowStr string) (string, error) {
	// Publish the verified bundle before touching the DB so the subprocess can
	// be re-spawned with the correct binary on the next restart.
	var binaryPathPtr *string
	if in.pluginsDir != "" {
		publishedPath, pubErr := in.publishBundle(ctx, tmpDir, m.Name)
		if pubErr != nil {
			return "", fmt.Errorf("publish bundle for %q (cosmetic): %w", m.Name, pubErr)
		}
		binaryPathPtr = &publishedPath
	}

	rows, err := in.q.UpdatePluginManifest(ctx, db.UpdatePluginManifestParams{
		ManifestSnapshot: string(manifestBytes),
		PluginVersion:    m.Version,
		Status:           existing.Status, // preserve current status — cosmetic change must not regress an active plugin
		UpdatedAt:        nowStr,
		ID:               existing.ID,
		ExpectedVersion:  existing.Version,
	})
	if err != nil {
		return "", fmt.Errorf("update plugin %q manifest (cosmetic): %w", m.Name, err)
	}
	if rows == 0 {
		return "", fmt.Errorf("update plugin %q manifest (cosmetic): CAS conflict (version mismatch)", m.Name)
	}

	// The manifest CAS bumped the version; re-read to get the new version for
	// the binary_path CAS that follows.
	if binaryPathPtr != nil {
		updated, rereadErr := in.q.GetPluginByName(ctx, m.Name)
		if rereadErr != nil {
			return "", fmt.Errorf("re-read plugin %q after cosmetic update: %w", m.Name, rereadErr)
		}
		if err := in.updateBinaryPath(ctx, existing.ID, updated.Version, binaryPathPtr, nowStr); err != nil {
			return "", fmt.Errorf("update binary_path for %q (cosmetic): %w", m.Name, err)
		}
	}

	if err := in.recordAuditEvent(ctx, auditManifestCosmeticChange, severityInfo, nowStr, map[string]any{
		"plugin_id":       existing.ID,
		"name":            existing.Name,
		"old_version":     existing.PluginVersion,
		"new_version":     m.Version,
		"cosmetic_fields": pluginmanifest.CosmeticFields(changes),
	}); err != nil {
		return "", fmt.Errorf("record manifest_cosmetic_change audit for %q: %w", existing.Name, err)
	}
	return existing.ID, nil
}

// pubkeyFingerprint derives a short human-readable fingerprint from the
// Minisign public key bytes. Returns the hex-encoded 8-byte key ID, which is
// already embedded in the wire format and unique per keypair.
// If the bytes cannot be parsed (e.g. empty string for an unsigned plugin),
// a fallback of "(unknown)" is returned.
func pubkeyFingerprint(pubkeyBytes []byte) string {
	if len(pubkeyBytes) == 0 {
		return "(unknown)"
	}
	pk, _, err := signing.ParsePublicKey(pubkeyBytes)
	if err != nil {
		return "(unparseable)"
	}
	return fmt.Sprintf("%x", pk.KeyID)
}

// installStatusFor returns the status value to write for a newly-installed or
// version-bumped plugin row. Verified bundles (and unsigned bundles when the
// operator has explicitly opted in via GLEIPNIR_ALLOW_UNSIGNED_PLUGINS=true)
// are promoted to "active" so the subprocess manager and trigger supervisor
// will pick them up. The "pending_review" status is reserved for paths that
// require explicit operator approval — currently none in this codepath, but
// future review-gated flows can reuse the constant.
func installStatusFor(outcome VerifyOutcome) string {
	switch outcome {
	case OutcomeVerified, OutcomeUnsignedPermissive:
		return statusActive
	default:
		return statusPendingReview
	}
}

// createPlugin inserts a new plugin row with the status returned by
// installStatusFor (typically "active" for verified or unsigned-permissive bundles).
// Publishes the verified bundle to disk first, then stores the binary_path
// in the DB so Manager.StartAllActive can re-spawn the subprocess on server
// restart without re-extracting the tarball.
//
// TODO(plugin): wrap createPlugin + audit insert in a single transaction once
// Installer takes *sql.DB instead of *db.Queries. Today an audit-insert failure
// leaves an orphan plugins row; same-version idempotency masks the retry.
func (in *Installer) createPlugin(ctx context.Context, m *manifest.Manifest, manifestBytes []byte, result VerifyResult, tmpDir string, nowStr string) (string, error) {
	var binaryPathPtr *string
	if in.pluginsDir != "" {
		publishedPath, pubErr := in.publishBundle(ctx, tmpDir, m.Name)
		if pubErr != nil {
			return "", fmt.Errorf("publish bundle for %q: %w", m.Name, pubErr)
		}
		binaryPathPtr = &publishedPath
	}

	pluginID := model.NewULID()
	_, err := in.q.CreatePlugin(ctx, db.CreatePluginParams{
		ID:               pluginID,
		Name:             m.Name,
		PluginVersion:    m.Version,
		ManifestSnapshot: string(manifestBytes),
		TrustedPubkey:    string(result.Pubkey),
		Status:           installStatusFor(result.Outcome),
		BinaryPath:       binaryPathPtr,
		CreatedAt:        nowStr,
		UpdatedAt:        nowStr,
	})
	if err != nil {
		return "", fmt.Errorf("create plugin %q: %w", m.Name, err)
	}

	if err := in.recordAuditEvent(ctx, auditPluginInstalled, severityInfo, nowStr, map[string]any{
		"name":    m.Name,
		"version": m.Version,
		"outcome": result.Outcome.String(),
	}); err != nil {
		return "", fmt.Errorf("record plugin_installed audit: %w", err)
	}
	return pluginID, nil
}

// updatePlugin updates the manifest snapshot on an existing plugin row when the
// version has changed. Publishes the verified bundle to disk first, then updates
// binary_path after the manifest CAS succeeds. Uses the CAS guard (ADR-038).
//
// TODO(plugin): wrap updatePlugin + audit insert in a single transaction once
// Installer takes *sql.DB instead of *db.Queries. Today an audit-insert failure
// leaves the plugins row updated but the event unrecorded; same-version
// idempotency masks the retry.
func (in *Installer) updatePlugin(ctx context.Context, existing db.Plugin, m *manifest.Manifest, manifestBytes []byte, result VerifyResult, tmpDir string, nowStr string) (string, error) {
	// Publish the verified bundle before touching the DB so the subprocess can
	// be re-spawned with the correct binary on the next restart.
	var binaryPathPtr *string
	if in.pluginsDir != "" {
		publishedPath, pubErr := in.publishBundle(ctx, tmpDir, m.Name)
		if pubErr != nil {
			return "", fmt.Errorf("publish bundle for %q: %w", m.Name, pubErr)
		}
		binaryPathPtr = &publishedPath
	}

	rows, err := in.q.UpdatePluginManifest(ctx, db.UpdatePluginManifestParams{
		ManifestSnapshot: string(manifestBytes),
		PluginVersion:    m.Version,
		Status:           installStatusFor(result.Outcome),
		UpdatedAt:        nowStr,
		ID:               existing.ID,
		ExpectedVersion:  existing.Version,
	})
	if err != nil {
		return "", fmt.Errorf("update plugin %q manifest: %w", m.Name, err)
	}
	if rows == 0 {
		// CAS miss: a concurrent writer already advanced the version. This is
		// unlikely in the sequential dispatch model but safe to surface as an error.
		return "", fmt.Errorf("update plugin %q: CAS conflict (version mismatch)", m.Name)
	}

	// The manifest CAS bumped the version; re-read to get the new version for
	// the binary_path CAS that follows.
	if binaryPathPtr != nil {
		updated, rereadErr := in.q.GetPluginByName(ctx, m.Name)
		if rereadErr != nil {
			return "", fmt.Errorf("re-read plugin %q after manifest update: %w", m.Name, rereadErr)
		}
		if err := in.updateBinaryPath(ctx, existing.ID, updated.Version, binaryPathPtr, nowStr); err != nil {
			return "", fmt.Errorf("update binary_path for %q: %w", m.Name, err)
		}
	}

	if err := in.recordAuditEvent(ctx, auditUpdatePending, severityInfo, nowStr, map[string]any{
		"name":        m.Name,
		"old_version": existing.PluginVersion,
		"new_version": m.Version,
	}); err != nil {
		return "", fmt.Errorf("record plugin_update_pending audit: %w", err)
	}
	return existing.ID, nil
}

// updateBinaryPath writes the published binary path to the plugins row using
// a CAS guard. A rows==0 result is a non-fatal CAS conflict — the manifest was
// already bumped by another writer and the binary_path will be set on the next
// install attempt. We log and continue rather than failing the overall install.
func (in *Installer) updateBinaryPath(ctx context.Context, pluginID string, version int64, binaryPathPtr *string, nowStr string) error {
	rows, err := in.q.UpdatePluginBinaryPath(ctx, db.UpdatePluginBinaryPathParams{
		BinaryPath:      binaryPathPtr,
		UpdatedAt:       nowStr,
		ID:              pluginID,
		ExpectedVersion: version,
	})
	if err != nil {
		return fmt.Errorf("db update binary_path: %w", err)
	}
	if rows == 0 {
		slog.WarnContext(ctx, "updateBinaryPath: CAS conflict; binary_path will be set on next install",
			"plugin_id", pluginID)
	}
	return nil
}

// nowStr returns the current time as an RFC3339Nano string via the injectable clock.
func (in *Installer) nowStr() string {
	return in.clock().UTC().Format(time.RFC3339Nano)
}
