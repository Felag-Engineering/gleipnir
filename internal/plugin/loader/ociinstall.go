package loader

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/container"
)

// Audit event types specific to the v2 install path (ADR-046: operational, not
// LLM-visible).
const (
	auditImageLoaded         = "plugin_image_loaded"
	auditImageDigestMismatch = "plugin_image_digest_mismatch"
	auditImageLoadSkipped    = "plugin_image_load_skipped"
)

// rejectImageDigestMismatch is the InstallRejectedError reason for a bundle
// whose loaded image is not the one its manifest pinned.
const rejectImageDigestMismatch = "image_digest_mismatch"

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

	// TOFU (ADR-045): a bundle signed with a different key than the one pinned
	// at first install does not install, it waits for an admin. Reusing the v1
	// check verbatim is the point — a second implementation of key pinning is a
	// second place for it to be subtly wrong.
	existing, lookupErr := in.base.q.GetPluginByName(ctx, bundle.Manifest.Name)
	switch {
	case lookupErr == nil:
		refreshed, mismatch, pinErr := in.base.checkPubkeyPin(ctx, existing, result, nowStr)
		if pinErr != nil {
			return OCIInstallResult{}, pinErr
		}
		if mismatch {
			if _, mmErr := in.base.handlePubkeyMismatch(ctx, refreshed, result, bundle.Manifest.Version, nowStr); mmErr != nil {
				return OCIInstallResult{}, mmErr
			}
			return OCIInstallResult{}, &InstallRejectedError{
				Reason:  rejectPubkeyMismatch,
				Message: "bundle is signed with a different key than the one pinned at first install; an admin must approve the rotation",
			}
		}
	case errors.Is(lookupErr, sql.ErrNoRows):
		// First install of this plugin; nothing pinned yet.
	default:
		return OCIInstallResult{}, fmt.Errorf("look up plugin %q: %w", bundle.Manifest.Name, lookupErr)
	}

	// Everything above this line touched the DB only to READ or to record a
	// refusal. Everything below can still fail, and must leave nothing behind
	// when it does.
	loaded, err := in.loadAndVerifyImage(ctx, bundle)
	if err != nil {
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
			return OCIInstallResult{}, &InstallRejectedError{
				Reason:  rejectImageDigestMismatch,
				Message: "the image loaded from this bundle is not the image its manifest pins; the bundle was not installed",
			}
		}
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
	// image by its own reckoning. An offline load preserves the image ID, while
	// RepoDigests are usually empty because no registry was involved — so both
	// are accepted, and a match on either is proof the pinned bytes are here.
	if info.ID != digest && !slices.Contains(info.RepoDigests, bundle.ImageReference()) {
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

		// The image row is written only when the image is actually present.
		// Recording an image the host never loaded would make the GC accounting
		// in plugin_container_images a record of intentions rather than of what
		// the runtime holds.
		if loaded.present {
			size := loaded.info.SizeBytes
			var sizePtr *int64
			if size > 0 {
				sizePtr = &size
			}
			if _, err := q.UpsertContainerImage(ctx, db.UpsertContainerImageParams{
				Digest:     bundle.ImageDigest(),
				Reference:  bundle.ImageReference(),
				PluginID:   &pluginID,
				SizeBytes:  sizePtr,
				LoadedAt:   nowStr,
				LastUsedAt: &nowStr,
			}); err != nil {
				return fmt.Errorf("record loaded image %s: %w", bundle.ImageDigest(), err)
			}
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
