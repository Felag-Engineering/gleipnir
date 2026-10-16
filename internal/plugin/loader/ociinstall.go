package loader

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/container"
	pluginmanifest "github.com/felag-engineering/gleipnir/internal/plugin/manifest"
	"github.com/felag-engineering/gleipnir/plugin-sdk/manifestv2"
)

// Audit event types specific to the v2 install path (ADR-046: operational, not
// LLM-visible).
const (
	auditImageLoaded         = "plugin_image_loaded"
	auditImageDigestMismatch = "plugin_image_digest_mismatch"
	auditImageLoadSkipped    = "plugin_image_load_skipped"
	auditImageArchiveRefused = "plugin_image_archive_rejected"
)

// rejectImageDigestMismatch is the InstallRejectedError reason for a bundle
// whose loaded image is not the one its manifest pinned.
const rejectImageDigestMismatch = "image_digest_mismatch"

// rejectImageArchiveInvalid is the InstallRejectedError reason for a bundle
// whose image archive failed inspection before it was offered to the runtime.
const rejectImageArchiveInvalid = "image_archive_invalid"

// ErrImageDigestMismatch reports that the image the runtime ended up holding is
// not the image the manifest pinned.
//
// This is a hard install failure and not a warning, because the digest pin is
// the entire mechanism by which an admin's consent means anything: they
// approved a specific set of bytes, and something else is present. Whether the
// cause is a mis-assembled bundle or an attacker does not change the response.
var ErrImageDigestMismatch = errors.New("loaded image digest does not match the manifest pin")

// OCIInstallResult describes what one v2 install did.
type OCIInstallResult struct {
	// PluginID is the plugins row. Empty when the install did not commit
	// (signature rejected).
	PluginID string

	// ImageDigest is the manifest's pin, which — when ImageLoaded is true —
	// has been confirmed present in the runtime.
	ImageDigest string

	// ImageLoaded is false when the host did not put the image into the
	// runtime itself. Today that means manual posture: the operator manages
	// containers via their own compose file and loads the image themselves
	// (spec §7), so the host records the bundle and stays off the socket.
	ImageLoaded bool
}

// OCIInstaller runs the v2 install pipeline: extract → verify signature → load
// image → verify digest → snapshot into the DB.
//
// It reuses the v1 Installer for everything ADR-045 already settled — the
// Minisign verifier, TOFU pubkey pinning, the audit writer, the transaction
// helper — rather than reimplementing those flows against a new manifest type.
// "The trust model is unchanged" is only true if it is literally the same code.
type OCIInstaller struct {
	base *Installer

	// images is the narrow image half of the runtime. An OCIInstaller cannot
	// start a container even by mistake: it was never handed the ability.
	images container.ImageRuntime
}

// NewOCIInstaller wires a v2 installer over an existing v1 Installer and the
// container runtime's image operations. images may be nil, in which case the
// image is never loaded and every install is recorded as image-pending —
// the same outcome as manual posture, for a host with no runtime configured.
func NewOCIInstaller(base *Installer, images container.ImageRuntime) *OCIInstaller {
	return &OCIInstaller{base: base, images: images}
}

// OnInstalled registers the post-install hook on the underlying Installer, so
// both pipelines share one callback slot. As in v1 the hook fires only for an
// install that commits an update to an already-active plugin: a fresh v2
// install always lands pending_review and never triggers it.
func (in *OCIInstaller) OnInstalled(fn func(ctx context.Context, pluginID string)) {
	in.base.OnInstalled(fn)
}

// Install runs the full v2 pipeline for the tarball at tarPath.
//
// Ordering is the load-bearing part. The image load and its digest check
// happen BEFORE any row is written, so a bundle that fails either one leaves
// the database exactly as it was — no plugins row pointing at an image that
// was never loaded, no image row for an install that did not happen (#349's
// lesson: a half-installed plugin is worse than a failed install, because
// nothing reports it as broken).
func (in *OCIInstaller) Install(ctx context.Context, tarPath string) (OCIInstallResult, error) {
	tmpDir, err := in.base.extractIncoming(tarPath)
	if err != nil {
		return OCIInstallResult{}, err
	}
	defer os.RemoveAll(tmpDir)

	bundleDir, err := resolveBundleRoot(tmpDir)
	if err != nil {
		return OCIInstallResult{}, fmt.Errorf("resolve bundle root in %q: %w", tarPath, err)
	}

	bundle, err := OpenOCIBundle(bundleDir)
	if err != nil {
		return OCIInstallResult{}, fmt.Errorf("open bundle %q: %w", tarPath, err)
	}

	// The signature covers (image archive, manifest) — the same pairing v1 used
	// for (binary, manifest). Editing the manifest to point at another image
	// breaks it, which is what makes the digest pin below meaningful rather
	// than self-asserted.
	result := in.base.verifier.VerifyBundle(bundleDir, bundle.ArchivePath)
	if result.Outcome == OutcomeRejected {
		id, err := in.base.recordSignatureInvalid(ctx, tarPath, bundle.Manifest.Name, result.Err)
		return OCIInstallResult{PluginID: id}, err
	}

	nowStr := in.base.nowStr()

	existing, lookupErr := in.base.q.GetPluginByName(ctx, bundle.Manifest.Name)
	switch {
	case lookupErr == nil:
		return in.installOverExisting(ctx, bundle, result, existing, nowStr)
	case errors.Is(lookupErr, sql.ErrNoRows):
		return in.installFresh(ctx, bundle, result, nowStr)
	default:
		return OCIInstallResult{}, fmt.Errorf("look up plugin %q: %w", bundle.Manifest.Name, lookupErr)
	}
}

// installFresh is the first install of a plugin name: nothing is pinned yet,
// so the image is loaded and verified and then everything is written at once.
func (in *OCIInstaller) installFresh(ctx context.Context, bundle *OCIBundle, result VerifyResult, nowStr string) (OCIInstallResult, error) {
	loaded, err := in.loadOrReject(ctx, bundle, nowStr)
	if err != nil {
		return OCIInstallResult{}, err
	}

	pluginID, err := in.snapshot(ctx, bundle, result, loaded, nowStr)
	if err != nil {
		return OCIInstallResult{}, err
	}

	return OCIInstallResult{
		PluginID:    pluginID,
		ImageDigest: bundle.ImageDigest(),
		ImageLoaded: loaded.present,
	}, nil
}

// installOverExisting handles a bundle for a plugin name that already has a
// row, with the same TOFU and material-change rules the v1 Installer applies:
// an identical re-drop is a no-op, a cosmetic change hot-applies, a material
// change is blocked for admin review, and a different signing key waits for
// admin approval.
//
// Ordering is the same load-bearing rule as a fresh install. Everything that
// can refuse the bundle runs first, the image is loaded and its digest
// verified next, and only then is a row written, so a rejected or blocked
// bundle leaves the consented snapshot, the pinned key and the desired image
// exactly as they were. The only writes before the image load are the audit
// records of a refusal.
//
// One deliberate difference from v1: v1 ignores any same-version re-drop,
// because a binary it cannot inspect might have changed under the same
// version string. The v2 manifest pins the image digest, so a same-version
// bundle with different content is diffed like any other and only a
// byte-identical manifest is a no-op.
func (in *OCIInstaller) installOverExisting(ctx context.Context, bundle *OCIBundle, result VerifyResult, existing db.Plugin, nowStr string) (OCIInstallResult, error) {
	name := bundle.Manifest.Name
	out := OCIInstallResult{PluginID: existing.ID, ImageDigest: bundle.ImageDigest()}

	// A v1 row and a v2 bundle are different substrates under one name.
	// Letting one replace the other would swap a subprocess for a container
	// (or back) under an old consent, so it is refused outright.
	if !manifestv2.IsV2([]byte(existing.ManifestSnapshot)) {
		return OCIInstallResult{}, in.base.refuseSchemaCrossover(ctx, existing, schemaLabelV2, bundle.Manifest.Version, nowStr)
	}

	if bytes.Equal([]byte(existing.ManifestSnapshot), bundle.ManifestBytes) {
		// Byte-identical manifest means the same digest, so there is nothing
		// to load or change. Recorded so a re-submitted bundle leaves a trail.
		if err := in.base.recordAuditEvent(ctx, auditReinstallIgnored, severityInfo, nowStr, map[string]any{
			"name":    name,
			"version": bundle.Manifest.Version,
		}); err != nil {
			return OCIInstallResult{}, fmt.Errorf("record reinstall_ignored audit for %q: %w", name, err)
		}
		return out, nil
	}

	if isValidSemver(bundle.Manifest.Version) && isValidSemver(existing.PluginVersion) &&
		compareVersions(bundle.Manifest.Version, existing.PluginVersion) < 0 {
		slog.WarnContext(ctx, "plugin downgrade refused",
			"name", name, "installed_version", existing.PluginVersion, "rejected_version", bundle.Manifest.Version)
		if err := in.base.recordAuditEvent(ctx, auditDowngradeRefused, severityHigh, nowStr, map[string]any{
			"name":              name,
			"installed_version": existing.PluginVersion,
			"rejected_version":  bundle.Manifest.Version,
		}); err != nil {
			return OCIInstallResult{}, fmt.Errorf("record downgrade_refused audit for %q: %w", name, err)
		}
		return OCIInstallResult{}, &InstallRejectedError{
			Reason:  rejectDowngrade,
			Message: fmt.Sprintf("version %s is older than the installed version %s; downgrades are refused", bundle.Manifest.Version, existing.PluginVersion),
		}
	}

	// TOFU (ADR-045): a bundle signed with a different key than the one pinned
	// at first install does not install, it waits for an admin. Decided from
	// the row alone so a mismatching bundle never reaches the runtime.
	if pubkeyDiffers(existing, result) {
		return OCIInstallResult{}, in.rejectPubkeyMismatch(ctx, existing, result, bundle.Manifest.Version, nowStr)
	}

	oldManifest, err := manifestv2.Parse([]byte(existing.ManifestSnapshot))
	if err != nil {
		return OCIInstallResult{}, fmt.Errorf("parse stored manifest snapshot for %q: %w", name, err)
	}
	changes := diffCandidate(oldManifest, bundle.Manifest)

	loaded, err := in.loadOrReject(ctx, bundle, nowStr)
	if err != nil {
		return OCIInstallResult{}, err
	}
	out.ImageLoaded = loaded.present

	// Delayed TOFU: a signed bundle arriving for a plugin first installed
	// unsigned pins its key now. Run after the image checks so a bundle that
	// fails them pins nothing; it can still report a mismatch if the row moved
	// since the read above.
	existing, mismatch, err := in.base.checkPubkeyPin(ctx, existing, result, nowStr)
	if err != nil {
		return OCIInstallResult{}, err
	}
	if mismatch {
		return OCIInstallResult{}, in.rejectPubkeyMismatch(ctx, existing, result, bundle.Manifest.Version, nowStr)
	}

	if pluginmanifest.HasMaterial(changes) {
		if err := in.blockMaterialChange(ctx, existing, oldManifest, bundle, loaded, changes, nowStr); err != nil {
			return OCIInstallResult{}, err
		}
		return out, &InstallRejectedError{
			Reason:  rejectMaterialChange,
			Message: "manifest has material changes that require admin review; approve them via the \"Accept manifest\" admin flow",
		}
	}

	if err := in.applyCosmeticChange(ctx, existing, bundle, result, loaded, changes, nowStr); err != nil {
		return OCIInstallResult{}, err
	}

	// Same gate as v1: only an update to an already-active plugin notifies.
	if existing.Status == statusActive && in.base.onInstalled != nil {
		in.base.onInstalled(ctx, existing.ID)
	}
	return out, nil
}

// pubkeyDiffers reports whether a signed bundle's key differs from the one
// pinned on the row. It is the read-only half of checkPubkeyPin: an unpinned
// row (delayed TOFU) is not a mismatch.
func pubkeyDiffers(existing db.Plugin, result VerifyResult) bool {
	return result.Outcome == OutcomeVerified &&
		existing.TrustedPubkey != "" &&
		!bytes.Equal(result.Pubkey, []byte(existing.TrustedPubkey))
}

func (in *OCIInstaller) rejectPubkeyMismatch(ctx context.Context, existing db.Plugin, result VerifyResult, newVersion, nowStr string) error {
	if _, err := in.base.handlePubkeyMismatch(ctx, existing, result, newVersion, nowStr); err != nil {
		return err
	}
	return &InstallRejectedError{
		Reason:  rejectPubkeyMismatch,
		Message: "bundle is signed with a different key than the one pinned at first install; an admin must approve the rotation",
	}
}

// diffCandidate is manifest.DiffV2 plus the image digest. DiffV2 classifies
// the image identifier as cosmetic because the signature and the digest check
// protect its integrity, but a different digest is a different set of bytes
// than the ones the admin consented to run, so it is material here.
func diffCandidate(old, candidate *manifestv2.Manifest) []pluginmanifest.Change {
	changes := pluginmanifest.DiffV2(old, candidate)
	if oldDigest, newDigest := old.Package.Digest(), candidate.Package.Digest(); oldDigest != newDigest {
		changes = append(changes, pluginmanifest.Change{Field: "package.digest", Material: true, From: oldDigest, To: newDigest})
	}
	return changes
}

// loadOrReject loads and verifies the bundle's image, turning a digest
// mismatch or a malformed archive into the audited InstallRejectedError.
func (in *OCIInstaller) loadOrReject(ctx context.Context, bundle *OCIBundle, nowStr string) (loadedImage, error) {
	loaded, err := in.loadAndVerifyImage(ctx, bundle)
	if err == nil {
		return loaded, nil
	}

	var mismatch *imageDigestMismatchError
	if errors.As(err, &mismatch) {
		if auditErr := insertAuditRow(ctx, in.base.q, auditImageDigestMismatch, severityHigh, nowStr, map[string]any{
			"name":     bundle.Manifest.Name,
			"version":  bundle.Manifest.Version,
			"expected": mismatch.Expected,
			"observed": mismatch.Observed,
		}); auditErr != nil {
			slog.ErrorContext(ctx, "record plugin_image_digest_mismatch audit", "err", auditErr)
		}
		return loadedImage{}, &InstallRejectedError{
			Reason:  rejectImageDigestMismatch,
			Message: "the image loaded from this bundle is not the image its manifest pins; the bundle was not installed",
		}
	}
	if errors.Is(err, ErrImageArchiveInvalid) {
		if auditErr := insertAuditRow(ctx, in.base.q, auditImageArchiveRefused, severityHigh, nowStr, map[string]any{
			"name":    bundle.Manifest.Name,
			"version": bundle.Manifest.Version,
			"reason":  err.Error(),
		}); auditErr != nil {
			slog.ErrorContext(ctx, "record plugin_image_archive_rejected audit", "err", auditErr)
		}
		return loadedImage{}, &InstallRejectedError{
			Reason:  rejectImageArchiveInvalid,
			Message: "the image archive in this bundle failed inspection; the bundle was not installed",
		}
	}
	return loadedImage{}, err
}

// blockMaterialChange records a material change for admin review and leaves
// the consented snapshot untouched. The candidate manifest is persisted so
// accept-manifest can commit it without the bundle, which is also why its
// image was loaded above: accepting must not depend on a second upload.
//
// The loaded image is recorded against the plugin so the accounting matches
// the runtime, but no live generation pins it, so image GC reclaims it if the
// change is never accepted.
func (in *OCIInstaller) blockMaterialChange(ctx context.Context, existing db.Plugin, oldManifest *manifestv2.Manifest, bundle *OCIBundle, loaded loadedImage, changes []pluginmanifest.Change, nowStr string) error {
	newlyRequired, err := pluginmanifest.ConfigSchemaNewlyRequiredFieldsV2(oldManifest, bundle.Manifest)
	if err != nil {
		slog.WarnContext(ctx, "manifest material change: cannot determine newly-required config fields; schema malformed",
			"plugin", existing.Name, "err", err)
		newlyRequired = nil
	}

	err = in.base.inTx(ctx, func(q *db.Queries) error {
		if err := insertAuditRow(ctx, q, auditManifestMaterialChange, severityHigh, nowStr, map[string]any{
			"plugin_id":                    existing.ID,
			"name":                         existing.Name,
			"old_version":                  existing.PluginVersion,
			"new_version":                  bundle.Manifest.Version,
			"old_image_digest":             oldManifest.Package.Digest(),
			"new_image_digest":             bundle.ImageDigest(),
			"image_loaded":                 loaded.present,
			"material_fields":              pluginmanifest.MaterialFields(changes),
			"cosmetic_fields":              pluginmanifest.CosmeticFields(changes),
			"candidate_manifest_b64":       base64.StdEncoding.EncodeToString(bundle.ManifestBytes),
			"newly_required_config_fields": newlyRequired,
		}); err != nil {
			return fmt.Errorf("record manifest_material_change audit for %q: %w", existing.Name, err)
		}
		if err := q.UpsertPluginPendingManifest(ctx, db.UpsertPluginPendingManifestParams{
			PluginID:          existing.ID,
			CandidateManifest: string(bundle.ManifestBytes),
			OldVersion:        existing.PluginVersion,
			NewVersion:        bundle.Manifest.Version,
			CreatedAt:         nowStr,
			UpdatedAt:         nowStr,
		}); err != nil {
			return fmt.Errorf("upsert pending manifest for %q: %w", existing.Name, err)
		}
		return recordLoadedImage(ctx, q, bundle, loaded, existing.ID, nowStr)
	})
	if err != nil {
		return err
	}

	// After the commit, so instances are never held for a change that was not
	// recorded. Best effort, like v1.
	in.base.transitionAllInstances(
		ctx, existing,
		model.PluginHealthStatePendingManifestApproval,
		"manifest changed materially; admin re-approval required",
		"manifest material change",
	)
	return nil
}

// applyCosmeticChange commits a non-material difference: the snapshot is
// updated under the row's version CAS with the plugin's status preserved, so
// a description tweak cannot regress an active plugin.
func (in *OCIInstaller) applyCosmeticChange(ctx context.Context, existing db.Plugin, bundle *OCIBundle, result VerifyResult, loaded loadedImage, changes []pluginmanifest.Change, nowStr string) error {
	return in.base.inTx(ctx, func(q *db.Queries) error {
		rows, err := q.UpdatePluginManifest(ctx, db.UpdatePluginManifestParams{
			ManifestSnapshot: string(bundle.ManifestBytes),
			PluginVersion:    bundle.Manifest.Version,
			Status:           existing.Status,
			UpdatedAt:        nowStr,
			ID:               existing.ID,
			ExpectedVersion:  existing.Version,
		})
		if err != nil {
			return fmt.Errorf("update plugin %q manifest (cosmetic): %w", existing.Name, err)
		}
		if rows == 0 {
			return fmt.Errorf("update plugin %q manifest (cosmetic): CAS conflict (version mismatch)", existing.Name)
		}

		if err := recordLoadedImage(ctx, q, bundle, loaded, existing.ID, nowStr); err != nil {
			return err
		}

		severity := severityInfo
		if result.Outcome == OutcomeUnsignedPermissive {
			severity = severityHigh
		}
		if err := insertAuditRow(ctx, q, auditManifestCosmeticChange, severity, nowStr, map[string]any{
			"plugin_id":       existing.ID,
			"name":            existing.Name,
			"old_version":     existing.PluginVersion,
			"new_version":     bundle.Manifest.Version,
			"cosmetic_fields": pluginmanifest.CosmeticFields(changes),
		}); err != nil {
			return fmt.Errorf("record manifest_cosmetic_change audit for %q: %w", existing.Name, err)
		}
		return nil
	})
}

// recordLoadedImage writes the image accounting row, but only when the image
// is actually present. Recording an image the host never loaded would make
// plugin_container_images a record of intentions rather than of what the
// runtime holds.
func recordLoadedImage(ctx context.Context, q *db.Queries, bundle *OCIBundle, loaded loadedImage, pluginID, nowStr string) error {
	if !loaded.present {
		return nil
	}
	var sizePtr *int64
	if size := loaded.info.SizeBytes; size > 0 {
		sizePtr = &size
	}
	// The ID the runtime itself reports for the image that matched the signed
	// archive. Containers are created from it: the pin is the config digest,
	// which a containerd-store daemon cannot resolve, so the reconciler needs
	// the name the daemon actually answers to.
	var localIDPtr *string
	if localID := loaded.info.ID; localID != "" {
		localIDPtr = &localID
	}
	if _, err := q.UpsertContainerImage(ctx, db.UpsertContainerImageParams{
		Digest:       bundle.ImageDigest(),
		Reference:    bundle.ImageReference(),
		PluginID:     &pluginID,
		SizeBytes:    sizePtr,
		LoadedAt:     nowStr,
		LastUsedAt:   &nowStr,
		LocalImageID: localIDPtr,
	}); err != nil {
		return fmt.Errorf("record loaded image %s: %w", bundle.ImageDigest(), err)
	}
	return nil
}

// loadedImage is what the runtime holds after a load attempt.
type loadedImage struct {
	// present is false when the image is not in the runtime: manual posture,
	// or no runtime configured. The install still commits — the operator loads
	// it — and the row records that the host did not.
	present bool
	info    container.ImageInfo
}

// imageDigestMismatchError carries both digests so the audit event can name
// what was expected and what turned up.
type imageDigestMismatchError struct {
	Expected string
	Observed string
}

func (e *imageDigestMismatchError) Error() string {
	return fmt.Sprintf("%s: expected %s, runtime holds %s", ErrImageDigestMismatch, e.Expected, e.Observed)
}

func (e *imageDigestMismatchError) Unwrap() error { return ErrImageDigestMismatch }

// loadAndVerifyImage loads the bundle's archive into the runtime and confirms
// the runtime then holds the digest the manifest pins.
//
// Verification is by INSPECTING the expected digest rather than by parsing what
// the daemon said it loaded. The load response is a progress stream whose
// human-readable lines are not part of any API contract, and scraping an
// identity out of it would make a security check depend on a log format. Asking
// "is the image I expect present?" has a yes/no answer and no parser.
func (in *OCIInstaller) loadAndVerifyImage(ctx context.Context, bundle *OCIBundle) (loadedImage, error) {
	// Inspected before the runtime is consulted at all, and even when there is
	// none: the archive is part of what the admin is asked to approve, so a
	// malformed one is refused whatever would later have loaded it.
	verified, err := inspectImageArchive(bundle.ArchivePath, bundle.Manifest.Package)
	if err != nil {
		return loadedImage{}, err
	}

	if in.images == nil {
		return loadedImage{}, nil
	}

	archive, err := os.Open(bundle.ArchivePath)
	if err != nil {
		return loadedImage{}, fmt.Errorf("open image archive: %w", err)
	}
	defer archive.Close()

	switch err := in.images.ImageLoad(ctx, archive); {
	case err == nil:
	case errors.Is(err, container.ErrManualModeWrite):
		// Manual posture: the operator loads the image with their own tooling
		// (spec §7). The bundle is still accepted and recorded — refusing it
		// would make manual mode unusable — but the host records that it did
		// not put the image there, so nothing later assumes it is present.
		slog.InfoContext(ctx, "manual posture: image load skipped, operator loads the image",
			"plugin", bundle.Manifest.Name, "digest", bundle.ImageDigest())
		return loadedImage{}, nil
	default:
		return loadedImage{}, fmt.Errorf("load image for %q: %w", bundle.Manifest.Name, err)
	}

	digest := bundle.ImageDigest()
	info, err := in.images.ImageInspect(ctx, digest)
	if errors.Is(err, container.ErrImageNotFound) && verified.ManifestDigest != "" {
		// The containerd image store does not index an image by its config
		// digest; it answers to the manifest digest. That digest is the one
		// the verified archive hashed to, not a value the daemon supplied.
		info, err = in.images.ImageInspect(ctx, verified.ManifestDigest)
	}
	if err != nil {
		if errors.Is(err, container.ErrImageNotFound) {
			// The load reported success and the pinned digest is not there, so
			// the archive contained something else. Reported as a mismatch
			// with an empty observed digest: what matters is that the expected
			// image is absent, and naming whatever else the archive held would
			// mean trusting the archive to describe itself.
			return loadedImage{}, &imageDigestMismatchError{Expected: digest, Observed: ""}
		}
		return loadedImage{}, fmt.Errorf("inspect loaded image %s: %w", digest, err)
	}

	// The runtime found something under that digest; confirm it is the same
	// image by its own reckoning. See imageMatchesArchive for the accepted forms.
	if !imageMatchesArchive(info, verified, bundle.ImageReference()) {
		return loadedImage{}, &imageDigestMismatchError{Expected: digest, Observed: info.ID}
	}

	return loadedImage{present: true, info: info}, nil
}

// snapshot writes the plugins row, the image row, and the audit event in one
// transaction, so an install is either fully recorded or not recorded at all.
func (in *OCIInstaller) snapshot(ctx context.Context, bundle *OCIBundle, result VerifyResult, loaded loadedImage, nowStr string) (string, error) {
	pluginID := model.NewULID()

	err := in.base.inTx(ctx, func(q *db.Queries) error {
		// A v2 plugin has no binary the host executes, so binary_path stays
		// NULL. What runs is the image, recorded below.
		//
		// Leaving it NULL is deliberate rather than incidental: the v1
		// subprocess manager re-spawns from binary_path on restart, so setting
		// it to the image archive to make some path-derived lookup work would
		// hand a tarball to exec(). That is also why the SBOM endpoint does
		// not yet serve v2 rows — it locates the file relative to binary_path,
		// and giving it a real answer needs a bundle-path column, which
		// belongs to the plugins-row cutover rather than here.
		if _, err := q.CreatePlugin(ctx, db.CreatePluginParams{
			ID:               pluginID,
			Name:             bundle.Manifest.Name,
			PluginVersion:    bundle.Manifest.Version,
			ManifestSnapshot: string(bundle.ManifestBytes),
			TrustedPubkey:    string(result.Pubkey),
			Status:           statusPendingReview,
			BinaryPath:       nil,
			CreatedAt:        nowStr,
			UpdatedAt:        nowStr,
		}); err != nil {
			return fmt.Errorf("create plugin %q: %w", bundle.Manifest.Name, err)
		}

		if err := recordLoadedImage(ctx, q, bundle, loaded, pluginID, nowStr); err != nil {
			return err
		}

		// ADR-045 §6: an unsigned-permissive install is high severity even
		// though it succeeded, so severity-based alerting catches every load
		// that bypassed signature verification.
		installSeverity := severityInfo
		if result.Outcome == OutcomeUnsignedPermissive {
			installSeverity = severityHigh
		}
		if err := insertAuditRow(ctx, q, auditPluginInstalled, installSeverity, nowStr, map[string]any{
			"name":         bundle.Manifest.Name,
			"version":      bundle.Manifest.Version,
			"outcome":      result.Outcome.String(),
			"image_digest": bundle.ImageDigest(),
			"image_loaded": loaded.present,
		}); err != nil {
			return fmt.Errorf("record plugin_installed audit: %w", err)
		}

		imageEvent, severity := auditImageLoaded, severityInfo
		if !loaded.present {
			// Not a failure, but not silent either: an operator who forgets to
			// load the image will otherwise find out when the container fails
			// to start, with nothing pointing back at the install.
			imageEvent, severity = auditImageLoadSkipped, severityHigh
		}
		if err := insertAuditRow(ctx, q, imageEvent, severity, nowStr, map[string]any{
			"name":      bundle.Manifest.Name,
			"digest":    bundle.ImageDigest(),
			"reference": bundle.ImageReference(),
		}); err != nil {
			return fmt.Errorf("record %s audit: %w", imageEvent, err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return pluginID, nil
}
