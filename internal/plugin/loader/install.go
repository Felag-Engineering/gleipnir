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
	verifier  BundleVerifier
	q         *db.Queries
	publisher event.Publisher
	// clock is injectable so tests can assert on timestamps without racing real time.
	clock func() time.Time
}

// NewInstaller returns an Installer wired to the given verifier, query set, and
// event publisher. publisher may be nil — state change events are skipped when nil.
func NewInstaller(v BundleVerifier, q *db.Queries, publisher event.Publisher) *Installer {
	return &Installer{verifier: v, q: q, publisher: publisher, clock: time.Now}
}

// Install runs the full install pipeline for the tarball at tarPath:
//  1. Extract to a temp directory.
//  2. Parse manifest.yaml.
//  3. Verify the bundle signature.
//  4. Snapshot into the plugins table with status=pending_review (new plugin),
//     or update the manifest (version bump), or skip (same version).
//
// Failed verification is recorded in plugin_audit_events and Install returns
// nil — the event is the operator-visible signal (ADR-046). The caller
// (Watcher) continues after a failed install; nothing is started.
//
// NOTE: The schema CHECK constraint on plugins.status only allows
// pending_review|active|removed. A "signature_invalid" status is not stored
// on the plugin row — #191 owns the per-instance health state machine.
func (in *Installer) Install(ctx context.Context, tarPath string) error {
	tmpDir, err := os.MkdirTemp("", "gleipnir-plugin-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	if err := ExtractTarball(tarPath, tmpDir, maxTarballBytes); err != nil {
		return fmt.Errorf("extract tarball %q: %w", tarPath, err)
	}

	m, manifestBytes, err := readManifest(tmpDir)
	if err != nil {
		return fmt.Errorf("read manifest from %q: %w", tarPath, err)
	}

	binaryPath := filepath.Join(tmpDir, m.Name)
	if rel, err := filepath.Rel(tmpDir, binaryPath); err != nil || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("manifest.name %q escapes bundle directory", m.Name)
	}
	result := in.verifier.VerifyBundle(tmpDir, binaryPath)

	if result.Outcome == OutcomeRejected {
		return in.recordSignatureInvalid(ctx, tarPath, m.Name, result.Err)
	}

	return in.upsertPlugin(ctx, m, manifestBytes, result)
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
// signature check and returns nil (the event is the operator signal).
func (in *Installer) recordSignatureInvalid(ctx context.Context, tarPath, pluginName string, verifyErr error) error {
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
		return fmt.Errorf("record signature_invalid audit: %w", err)
	}
	return nil
}

// upsertPlugin creates or updates the plugin row and emits an audit event.
func (in *Installer) upsertPlugin(ctx context.Context, m *manifest.Manifest, manifestBytes []byte, result VerifyResult) error {
	existing, err := in.q.GetPluginByName(ctx, m.Name)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("get plugin %q: %w", m.Name, err)
	}

	nowStr := in.nowStr()

	if errors.Is(err, sql.ErrNoRows) {
		return in.createPlugin(ctx, m, manifestBytes, result, nowStr)
	}

	if existing.PluginVersion == m.Version {
		// Same version already recorded — idempotent no-op (debounce safety net).
		return nil
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
				return fmt.Errorf("capture trusted pubkey for %q (delayed TOFU): %w", m.Name, updateErr)
			}
			if rows == 0 {
				return fmt.Errorf("capture trusted pubkey for %q: CAS conflict (version mismatch)", m.Name)
			}
			// Re-read so updatePlugin sees the bumped version.
			existing, err = in.q.GetPluginByName(ctx, m.Name)
			if err != nil {
				return fmt.Errorf("re-read plugin %q after TOFU capture: %w", m.Name, err)
			}
		} else if !bytes.Equal(result.Pubkey, []byte(existing.TrustedPubkey)) {
			// Key mismatch: a different signing key was used for this update.
			// Block the update and transition all instances to pending_key_approval
			// so admins are alerted. Do not call updatePlugin.
			return in.handlePubkeyMismatch(ctx, existing, result, m.Version, nowStr)
		}
	}

	// Diff the incoming manifest against the stored snapshot before calling
	// updatePlugin. Material changes block the update and enter pending_manifest_approval.
	// Cosmetic-only changes update the snapshot silently.
	var oldManifest manifest.Manifest
	if parseErr := manifest.Unmarshal([]byte(existing.ManifestSnapshot), &oldManifest); parseErr != nil {
		return fmt.Errorf("parse stored manifest snapshot for %q: %w", m.Name, parseErr)
	}
	changes := pluginmanifest.Diff(&oldManifest, m)
	if pluginmanifest.HasMaterial(changes) {
		return in.handleManifestMaterialChange(ctx, existing, &oldManifest, m, manifestBytes, changes, nowStr)
	}
	if len(changes) > 0 {
		return in.updatePluginCosmetic(ctx, existing, m, manifestBytes, changes, nowStr)
	}

	return in.updatePlugin(ctx, existing, m, manifestBytes, nowStr)
}

// handlePubkeyMismatch transitions all instances of the plugin to
// pending_key_approval and emits a plugin_pubkey_mismatch audit event.
// It returns nil so the watcher continues — the audit event is the operator signal.
func (in *Installer) handlePubkeyMismatch(ctx context.Context, existing db.Plugin, result VerifyResult, newVersion, nowStr string) error {
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
		return fmt.Errorf("record pubkey_mismatch audit for %q: %w", existing.Name, err)
	}
	return nil
}

// handleManifestMaterialChange blocks the manifest update and transitions all
// eligible instances to pending_manifest_approval. The candidate manifest is
// persisted in the audit-event payload (as base64) rather than the plugins row;
// the running generation continues serving the existing snapshot until an admin
// calls POST /api/v1/admin/plugins/{id}/accept-manifest.
//
// oldManifest is the already-parsed existing snapshot — passed in to avoid a
// second parse of the same bytes that upsertPlugin already completed.
func (in *Installer) handleManifestMaterialChange(ctx context.Context, existing db.Plugin, oldManifest *manifest.Manifest, m *manifest.Manifest, candidateBytes []byte, changes []pluginmanifest.Change, nowStr string) error {
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
		return fmt.Errorf("record manifest_material_change audit for %q: %w", existing.Name, err)
	}
	return nil
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
// regressed to pending_review for a description tweak.
func (in *Installer) updatePluginCosmetic(ctx context.Context, existing db.Plugin, m *manifest.Manifest, manifestBytes []byte, changes []pluginmanifest.Change, nowStr string) error {
	rows, err := in.q.UpdatePluginManifest(ctx, db.UpdatePluginManifestParams{
		ManifestSnapshot: string(manifestBytes),
		PluginVersion:    m.Version,
		Status:           existing.Status, // preserve current status — cosmetic change must not regress an active plugin
		UpdatedAt:        nowStr,
		ID:               existing.ID,
		ExpectedVersion:  existing.Version,
	})
	if err != nil {
		return fmt.Errorf("update plugin %q manifest (cosmetic): %w", m.Name, err)
	}
	if rows == 0 {
		return fmt.Errorf("update plugin %q manifest (cosmetic): CAS conflict (version mismatch)", m.Name)
	}

	if err := in.recordAuditEvent(ctx, auditManifestCosmeticChange, severityInfo, nowStr, map[string]any{
		"plugin_id":       existing.ID,
		"name":            existing.Name,
		"old_version":     existing.PluginVersion,
		"new_version":     m.Version,
		"cosmetic_fields": pluginmanifest.CosmeticFields(changes),
	}); err != nil {
		return fmt.Errorf("record manifest_cosmetic_change audit for %q: %w", existing.Name, err)
	}
	return nil
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

// createPlugin inserts a new plugin row with status=pending_review.
//
// TODO(plugin): wrap createPlugin + audit insert in a single transaction once
// Installer takes *sql.DB instead of *db.Queries. Today an audit-insert failure
// leaves an orphan plugins row; same-version idempotency masks the retry.
func (in *Installer) createPlugin(ctx context.Context, m *manifest.Manifest, manifestBytes []byte, result VerifyResult, nowStr string) error {
	_, err := in.q.CreatePlugin(ctx, db.CreatePluginParams{
		ID:               model.NewULID(),
		Name:             m.Name,
		PluginVersion:    m.Version,
		ManifestSnapshot: string(manifestBytes),
		TrustedPubkey:    string(result.Pubkey),
		Status:           statusPendingReview,
		CreatedAt:        nowStr,
		UpdatedAt:        nowStr,
	})
	if err != nil {
		return fmt.Errorf("create plugin %q: %w", m.Name, err)
	}

	if err := in.recordAuditEvent(ctx, auditPluginInstalled, severityInfo, nowStr, map[string]any{
		"name":    m.Name,
		"version": m.Version,
		"outcome": result.Outcome.String(),
	}); err != nil {
		return fmt.Errorf("record plugin_installed audit: %w", err)
	}
	// TODO(plugin-instance-provision): when CreatePluginInstance is wired into
	// the install flow (#159 follow-up), call internal/plugin/oauth.BuildSeedCredentials
	// here for manifests with Auth.Strategy in {oauth2_authcode, oauth2_clientcred}
	// and write the encrypted seed into the new instance row so the OAuth dance
	// has client_id/client_secret/endpoints available to start from.
	return nil
}

// updatePlugin updates the manifest snapshot on an existing plugin row when the
// version has changed. Uses the CAS guard (ADR-038).
//
// TODO(plugin): wrap updatePlugin + audit insert in a single transaction once
// Installer takes *sql.DB instead of *db.Queries. Today an audit-insert failure
// leaves the plugins row updated but the event unrecorded; same-version
// idempotency masks the retry.
func (in *Installer) updatePlugin(ctx context.Context, existing db.Plugin, m *manifest.Manifest, manifestBytes []byte, nowStr string) error {
	rows, err := in.q.UpdatePluginManifest(ctx, db.UpdatePluginManifestParams{
		ManifestSnapshot: string(manifestBytes),
		PluginVersion:    m.Version,
		Status:           statusPendingReview,
		UpdatedAt:        nowStr,
		ID:               existing.ID,
		ExpectedVersion:  existing.Version,
	})
	if err != nil {
		return fmt.Errorf("update plugin %q manifest: %w", m.Name, err)
	}
	if rows == 0 {
		// CAS miss: a concurrent writer already advanced the version. This is
		// unlikely in the sequential dispatch model but safe to surface as an error.
		return fmt.Errorf("update plugin %q: CAS conflict (version mismatch)", m.Name)
	}

	if err := in.recordAuditEvent(ctx, auditUpdatePending, severityInfo, nowStr, map[string]any{
		"name":        m.Name,
		"old_version": existing.PluginVersion,
		"new_version": m.Version,
	}); err != nil {
		return fmt.Errorf("record plugin_update_pending audit: %w", err)
	}
	return nil
}

// nowStr returns the current time as an RFC3339Nano string via the injectable clock.
func (in *Installer) nowStr() string {
	return in.clock().UTC().Format(time.RFC3339Nano)
}
