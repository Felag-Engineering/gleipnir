// Package loader — this file implements the v2 bundle format (ADR-056, spec §7
// "bundle format"; ADR-045's trust model intact).
//
// The drop-in UX does not change: an operator still copies a Minisign-signed
// tarball into GLEIPNIR_PLUGINS_DIR. What changed is what is inside it. A v1
// bundle carried a Go binary the host executed; a v2 bundle carries an OCI
// image archive the host loads into the container runtime and never executes
// itself. The tarball is still fully offline — no registry, no cosign, no
// network reachability required at install time — which is the property that
// makes a homelab install work on a machine with no outbound access at all.
//
// The signature covers the same two things it always did: the artifact and the
// manifest. For v2 the artifact is the image archive, so a bundle whose
// manifest was edited to point at a different image fails verification exactly
// as a v1 bundle with a swapped binary did.
package loader

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	manifestv2 "github.com/felag-engineering/gleipnir/plugin-sdk/manifestv2"
)

const (
	// ociManifestFilename is the v2 manifest inside a bundle. It shares the v1
	// filename deliberately: the verifier signs "the manifest" by path, and a
	// bundle is identified as v2 by its schema_version field, not by a
	// filename an attacker could pick.
	ociManifestFilename = "manifest.yaml"

	// ociImageArchiveName is the OCI/Docker image archive inside a v2 bundle.
	// One fixed name rather than a manifest-declared path: a path from the
	// manifest is a path an unverified manifest gets to choose, and the
	// manifest is only trustworthy AFTER the signature over this very file has
	// been checked.
	ociImageArchiveName = "image.tar"
)

// ErrNotOCIBundle reports that a bundle directory does not hold a v2 bundle:
// no manifest, a manifest that is not schema_version 2, or no image archive.
// It is a sentinel so the watcher can tell "this is a v1 bundle" from "this is
// a broken v2 bundle" — the first is routine, the second is an operator
// problem worth reporting.
var ErrNotOCIBundle = errors.New("not an OCI plugin bundle")

// OCIBundle is an extracted, structurally-valid v2 bundle. Structurally valid
// is all it claims: the signature has NOT been checked at this point, so
// nothing read out of Manifest may be acted on until it has been.
type OCIBundle struct {
	// Dir is the bundle root on disk.
	Dir string

	// Manifest is the parsed, validated v2 manifest.
	Manifest *manifestv2.Manifest

	// ManifestBytes is the raw YAML, kept for the manifest snapshot column so
	// what is stored is what was signed rather than a re-serialization of it.
	ManifestBytes []byte

	// ArchivePath is the OCI image archive. This is the artifact the Minisign
	// signature covers.
	ArchivePath string
}

// ImageDigest is the digest the manifest pins ("sha256:..."), which the loaded
// image must match. Parse succeeded, so this is non-empty.
func (b *OCIBundle) ImageDigest() string { return b.Manifest.Package.Digest() }

// ImageReference is the digest-pinned reference in full.
func (b *OCIBundle) ImageReference() string { return b.Manifest.Package.Identifier }

// OpenOCIBundle reads the v2 bundle rooted at dir.
//
// Every failure here is reported before any signature check, which sounds
// backwards and is not: these checks answer "is this even a v2 bundle", and a
// bundle that is not one has no signature worth checking. Nothing read here is
// TRUSTED — it is only used to decide which pipeline the tarball belongs to
// and where the signed artifact sits.
func OpenOCIBundle(dir string) (*OCIBundle, error) {
	manifestPath := filepath.Join(dir, ociManifestFilename)
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: no %s in bundle", ErrNotOCIBundle, ociManifestFilename)
		}
		return nil, fmt.Errorf("read %s: %w", ociManifestFilename, err)
	}

	if !manifestv2.IsV2(raw) {
		return nil, fmt.Errorf("%w: manifest is not schema_version %s", ErrNotOCIBundle, manifestv2.SchemaVersion)
	}

	m, err := manifestv2.Parse(raw)
	if err != nil {
		// A manifest that CLAIMS to be v2 and then does not parse is a broken
		// v2 bundle, not a v1 one — so this is deliberately not ErrNotOCIBundle.
		return nil, fmt.Errorf("parse v2 manifest: %w", err)
	}

	archivePath := filepath.Join(dir, ociImageArchiveName)
	info, err := os.Stat(archivePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: no %s in bundle", ErrNotOCIBundle, ociImageArchiveName)
		}
		return nil, fmt.Errorf("stat %s: %w", ociImageArchiveName, err)
	}
	if !info.Mode().IsRegular() {
		// ExtractTarball already refuses symlinks and devices, so this cannot
		// happen through the drop-in path. It is checked anyway because the
		// next thing that happens to this path is "read it and sign-verify it",
		// and a non-regular file there is worth failing on rather than
		// discovering through a confusing read error.
		return nil, fmt.Errorf("%s is not a regular file", ociImageArchiveName)
	}

	return &OCIBundle{
		Dir:           dir,
		Manifest:      m,
		ManifestBytes: raw,
		ArchivePath:   archivePath,
	}, nil
}
