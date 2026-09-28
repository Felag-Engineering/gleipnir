package cmd

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
	manifestv2 "github.com/felag-engineering/gleipnir/plugin-sdk/manifestv2"
	"github.com/felag-engineering/gleipnir/plugin-sdk/signing"
)

// ociImageArchiveEntryName is the in-bundle filename for a v2 image archive.
// It MUST match ociImageArchiveName in internal/plugin/loader/ocibundle.go —
// the SDK cannot import internal/* to share the constant, so this comment is
// the tripwire against the two drifting apart. manifest.yaml carries the same
// obligation, but needs no separate constant here: both v1 and v2 already
// agree it is named "manifest.yaml".
const ociImageArchiveEntryName = "image.tar"

// NewPackageCmd returns the cobra.Command for the `package` subcommand.
func NewPackageCmd() *cobra.Command {
	var flagBinary, flagManifest, flagImageArchive, flagImage, flagKey, flagPubkey, flagOutDir, flagSBOM string
	var flagKeyStdin, flagUnsigned bool

	defaultKey := defaultKeyPath()

	cmd := &cobra.Command{
		Use:   "package",
		Short: "Build and sign a plugin release bundle",
		Long: `Build a signed plugin release bundle. The manifest's schema_version picks
the mode:

v1 (gRPC-subprocess plugin, spec §14.5) requires --binary and produces:
  <name>-<version>/
    <manifest.Name>            (mode 0755, the binary)
    manifest.yaml             (mode 0644)
    <manifest.Name>.minisig   (mode 0644)
    signing.pub               (mode 0644)
    sbom.cyclonedx.json       (mode 0644, optional)

v2 (containerized plugin, spec §7) requires exactly one of --image-archive
<path> (a pre-saved image archive) or --image <ref> (shelled out to
"docker save" or "podman save") and produces:
  <name>-<version>/
    image.tar                 (mode 0644, the OCI/Docker image archive)
    manifest.yaml             (mode 0644)
    <manifest.Name>.minisig   (mode 0644)
    signing.pub               (mode 0644)
    sbom.cyclonedx.json       (mode 0644, optional)

--binary and --image-archive/--image are mutually exclusive: a v1 manifest
takes --binary, a v2 manifest takes an image flag, and supplying the wrong
one for the manifest's schema_version is an error.

Package-time digest check (v2 only): the image archive's config digest is
computed and compared against the manifest's package.identifier pin before
anything is signed. A mismatch fails loudly, naming both digests, rather than
producing a bundle that would be rejected at install time.

Signed payload is sha256(archive) || sha256(manifest) per spec §5.2, where
"archive" is the binary for v1 and the image archive for v2 — the same
pairing internal/plugin/loader/verify.go and ocibundle.go verify.

Use --unsigned only when the host has GLEIPNIR_ALLOW_UNSIGNED_PLUGINS=true set.
Unsigned bundles carry no .minisig or signing.pub.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPackage(cmd, flagBinary, flagManifest, flagImageArchive, flagImage, flagKey, flagKeyStdin, flagPubkey, flagOutDir, flagSBOM, flagUnsigned)
		},
	}

	cmd.Flags().StringVar(&flagBinary, "binary", "", "path to plugin binary (v1 manifests only)")
	cmd.Flags().StringVar(&flagManifest, "manifest", "manifest.yaml", "path to manifest.yaml")
	cmd.Flags().StringVar(&flagImageArchive, "image-archive", "", "path to a pre-saved OCI/Docker image archive (v2 manifests only)")
	cmd.Flags().StringVar(&flagImage, "image", "", "image reference to save via docker/podman (v2 manifests only)")
	cmd.Flags().StringVar(&flagKey, "key", defaultKey, "path to .key file")
	cmd.Flags().BoolVar(&flagKeyStdin, "key-stdin", false, "read .key from stdin (CI)")
	cmd.Flags().StringVar(&flagPubkey, "pubkey", "", "path to .pub file (default: sibling of .key)")
	cmd.Flags().StringVar(&flagOutDir, "out-dir", "dist", "output directory for tarball")
	cmd.Flags().StringVar(&flagSBOM, "sbom", "", "optional CycloneDX SBOM JSON path")
	cmd.Flags().BoolVar(&flagUnsigned, "unsigned", false, "produce unsigned bundle (requires GLEIPNIR_ALLOW_UNSIGNED_PLUGINS=true on host)")

	// --binary is not marked required here: which flags are required depends
	// on the manifest's schema_version, known only once runPackage reads it.

	return cmd
}

// runPackage dispatches on the on-disk manifest's schema_version. A manifest
// with no file on disk at all is a v1-only case (the binary's --emit-manifest
// output substitutes for it below), since a v2 plugin has no such fallback: a
// container image cannot be asked to emit its own manifest at package time.
func runPackage(cmd *cobra.Command, binary, manifestPath, imageArchive, imageRef, flagKey string, flagKeyStdin bool, pubkeyPath, outDir, sbomPath string, unsigned bool) error {
	raw, hasManifestFile, err := peekManifestFile(manifestPath)
	if err != nil {
		return fmt.Errorf("package: %w", err)
	}

	if hasManifestFile && manifestv2.IsV2(raw) {
		if binary != "" {
			return fmt.Errorf("package: %s is schema_version %s (a containerized plugin); --binary is a v1 flag — use --image-archive or --image", manifestPath, manifestv2.SchemaVersion)
		}
		return runPackageV2(cmd, raw, imageArchive, imageRef, flagKey, flagKeyStdin, pubkeyPath, outDir, sbomPath, unsigned)
	}

	if imageArchive != "" || imageRef != "" {
		return fmt.Errorf("package: %s is not a schema_version %s manifest; --image-archive/--image only apply to containerized plugins", manifestPath, manifestv2.SchemaVersion)
	}
	if binary == "" {
		return fmt.Errorf("package: --binary is required for a v1 manifest")
	}
	return runPackageV1(cmd, binary, manifestPath, flagKey, flagKeyStdin, pubkeyPath, outDir, sbomPath, unsigned)
}

// peekManifestFile reads manifestPath if it exists. A missing file is not an
// error here — it means "derive the manifest from the binary", the v1
// --emit-manifest fallback runPackageV1 implements.
func peekManifestFile(manifestPath string) (data []byte, ok bool, err error) {
	data, err = os.ReadFile(manifestPath)
	switch {
	case err == nil:
		return data, true, nil
	case os.IsNotExist(err):
		return nil, false, nil
	default:
		return nil, false, fmt.Errorf("read %s: %w", manifestPath, err)
	}
}

// runPackageV1 builds a v1 (gRPC-subprocess) plugin bundle.
func runPackageV1(cmd *cobra.Command, binary, manifestPath, flagKey string, flagKeyStdin bool, pubkeyPath, outDir, sbomPath string, unsigned bool) error {
	stdin := cmd.InOrStdin()

	// Load and canonicalise manifest.
	var manifestData []byte
	var err error
	if _, err = os.Stat(manifestPath); err == nil {
		manifestData, err = loadCanonicalManifest(manifestPath)
		if err != nil {
			return fmt.Errorf("package: %w", err)
		}
	} else {
		// Derive manifest from binary --emit-manifest.
		raw, err := runBinary(binary, []string{"--emit-manifest"})
		if err != nil {
			return fmt.Errorf("package: invoke binary for manifest: %w", err)
		}
		manifestData, err = manifest.Canonicalize(raw)
		if err != nil {
			return fmt.Errorf("package: canonicalise manifest from binary: %w", err)
		}
	}

	m, err := parseManifestFromBytes(manifestData)
	if err != nil {
		return fmt.Errorf("package: parse manifest: %w", err)
	}
	if m.Name == "" || m.Version == "" {
		return fmt.Errorf("package: manifest must have name and version set")
	}
	if err := validateBundleNameComponent("name", m.Name); err != nil {
		return fmt.Errorf("package: %w", err)
	}
	if err := validateBundleNameComponent("version", m.Version); err != nil {
		return fmt.Errorf("package: %w", err)
	}

	binaryData, err := os.ReadFile(binary)
	if err != nil {
		return fmt.Errorf("package: read binary: %w", err)
	}

	sigData, pubData, err := signBundle(cmd, unsigned, flagKey, flagKeyStdin, stdin, pubkeyPath, m.Name, m.Version, binaryData, manifestData)
	if err != nil {
		return fmt.Errorf("package: %w", err)
	}

	sbomData, err := readOptionalFile(sbomPath)
	if err != nil {
		return fmt.Errorf("package: %w", err)
	}

	tarPath, err := bundleTarballPath(outDir, m.Name, m.Version)
	if err != nil {
		return fmt.Errorf("package: %w", err)
	}

	entries := []bundleTarEntry{
		{m.Name, 0o755, binaryData},
		{"manifest.yaml", 0o644, manifestData},
	}
	entries = appendSigningEntries(entries, m.Name, sigData, pubData, sbomData)

	if err := writeBundleTarball(tarPath, m.Name, m.Version, entries); err != nil {
		return fmt.Errorf("package: write tarball: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "wrote bundle: %s\n", tarPath)
	return nil
}

// runPackageV2 builds a v2 (containerized) plugin bundle: a digest-pinned
// image archive plus its manifest, per spec §7's bundle format.
func runPackageV2(cmd *cobra.Command, manifestData []byte, imageArchive, imageRef, flagKey string, flagKeyStdin bool, pubkeyPath, outDir, sbomPath string, unsigned bool) error {
	stdin := cmd.InOrStdin()

	switch {
	case imageArchive == "" && imageRef == "":
		return fmt.Errorf("package: exactly one of --image-archive or --image is required for a schema_version %s manifest", manifestv2.SchemaVersion)
	case imageArchive != "" && imageRef != "":
		return fmt.Errorf("package: --image-archive and --image are mutually exclusive")
	}

	m, err := manifestv2.Parse(manifestData)
	if err != nil {
		return fmt.Errorf("package: parse manifest: %w", err)
	}
	if err := validateBundleNameComponent("name", m.Name); err != nil {
		return fmt.Errorf("package: %w", err)
	}
	if err := validateBundleNameComponent("version", m.Version); err != nil {
		return fmt.Errorf("package: %w", err)
	}

	canonicalManifest, err := manifestv2.Marshal(m)
	if err != nil {
		return fmt.Errorf("package: canonicalise manifest: %w", err)
	}

	archivePath, cleanupArchive, err := resolveImageArchivePath(cmd, imageArchive, imageRef)
	if err != nil {
		return fmt.Errorf("package: %w", err)
	}
	defer cleanupArchive()

	// Read the archive exactly once: info is computed from archiveData, and
	// archiveData — not the path — is what gets signed and bundled below. A
	// digest computed from the path and then re-read from the path to sign
	// would leave a check-then-sign gap if the file changed in between.
	archiveData, err := os.ReadFile(archivePath)
	if err != nil {
		return fmt.Errorf("package: read image archive: %w", err)
	}

	info, err := inspectImageArchive(archiveData)
	if err != nil {
		return fmt.Errorf("package: %w", err)
	}
	pinnedDigest := m.Package.Digest()
	if info.ConfigDigest != pinnedDigest {
		return fmt.Errorf("package: image archive config digest %s does not match manifest package.identifier digest %s",
			info.ConfigDigest, pinnedDigest)
	}
	warnOnUnexpectedTags(cmd, info.Tags, m.Package.Repository())

	sigData, pubData, err := signBundle(cmd, unsigned, flagKey, flagKeyStdin, stdin, pubkeyPath, m.Name, m.Version, archiveData, canonicalManifest)
	if err != nil {
		return fmt.Errorf("package: %w", err)
	}

	sbomData, err := readOptionalFile(sbomPath)
	if err != nil {
		return fmt.Errorf("package: %w", err)
	}

	tarPath, err := bundleTarballPath(outDir, m.Name, m.Version)
	if err != nil {
		return fmt.Errorf("package: %w", err)
	}

	entries := []bundleTarEntry{
		{ociImageArchiveEntryName, 0o644, archiveData},
		{"manifest.yaml", 0o644, canonicalManifest},
	}
	entries = appendSigningEntries(entries, m.Name, sigData, pubData, sbomData)
	warnIfBundleExceedsHostExtractionCap(cmd, entries)

	if err := writeBundleTarball(tarPath, m.Name, m.Version, entries); err != nil {
		return fmt.Errorf("package: write tarball: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "wrote bundle: %s\n", tarPath)
	return nil
}

// signBundle signs payload := PluginPayload(artifact, manifestData) and
// returns the .minisig + signing.pub bytes, or (nil, nil, nil) when unsigned
// is requested (after printing the same warning the v1 path always has).
func signBundle(cmd *cobra.Command, unsigned bool, flagKey string, flagKeyStdin bool, stdin io.Reader, pubkeyPath, name, version string, artifact, manifestData []byte) (sigData, pubData []byte, err error) {
	if unsigned {
		fmt.Fprintln(cmd.ErrOrStderr(), "WARNING: building unsigned bundle — only loads on hosts with GLEIPNIR_ALLOW_UNSIGNED_PLUGINS=true")
		return nil, nil, nil
	}

	raw, keyID, defaultPubPath, err := loadSecretKey(flagKey, flagKeyStdin, stdin)
	if err != nil {
		return nil, nil, err
	}

	trustedComment := fmt.Sprintf("timestamp:%d\tname:%s\tversion:%s", time.Now().Unix(), name, version)
	payload := signing.PluginPayload(artifact, manifestData)
	sig, err := signing.Sign(raw, keyID, payload, trustedComment)
	if err != nil {
		return nil, nil, fmt.Errorf("sign: %w", err)
	}
	sigData = signing.MarshalSignature(sig, fmt.Sprintf("signature for %s %s", name, version))

	resolvedPubPath := pubkeyPath
	if resolvedPubPath == "" {
		resolvedPubPath = defaultPubPath
	}
	if resolvedPubPath != "" {
		pubData, err = os.ReadFile(resolvedPubPath)
		if err != nil {
			return nil, nil, fmt.Errorf("read public key %s: %w", resolvedPubPath, err)
		}
	}
	if len(pubData) == 0 {
		return nil, nil, fmt.Errorf("no public key available; use --pubkey to specify one")
	}
	return sigData, pubData, nil
}

// appendSigningEntries appends the .minisig/signing.pub/sbom entries that are
// common to both v1 and v2 bundles, in the shape writeBundleTarball expects.
// sigData/pubData/sbomData being nil (unsigned, or no --sbom) omits the entry.
func appendSigningEntries(entries []bundleTarEntry, name string, sigData, pubData, sbomData []byte) []bundleTarEntry {
	if sigData != nil {
		// .minisig filename derives from manifest.Name per spec §14.5, for both
		// v1 and v2 bundles.
		entries = append(entries, bundleTarEntry{name + ".minisig", 0o644, sigData})
	}
	if pubData != nil {
		entries = append(entries, bundleTarEntry{"signing.pub", 0o644, pubData})
	}
	if sbomData != nil {
		entries = append(entries, bundleTarEntry{"sbom.cyclonedx.json", 0o644, sbomData})
	}
	return entries
}

// readOptionalFile reads path, returning (nil, nil) when path is empty.
func readOptionalFile(path string) ([]byte, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return data, nil
}

// bundleTarballPath ensures outDir exists and returns the <name>-<version>.tar.gz
// path inside it.
func bundleTarballPath(outDir, name, version string) (string, error) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", fmt.Errorf("create output dir: %w", err)
	}
	return filepath.Join(outDir, fmt.Sprintf("%s-%s.tar.gz", name, version)), nil
}

// validateBundleNameComponent rejects values that could escape a tar path:
// those containing '/', '\', or starting with '.'.
func validateBundleNameComponent(field, value string) error {
	if strings.ContainsAny(value, `/\`) || strings.HasPrefix(value, ".") {
		return fmt.Errorf("manifest %s contains path separator or starts with '.': %q", field, value)
	}
	return nil
}

// bundleTarEntry is one file to place inside a plugin release tarball, named
// relative to the bundle root (the "<name>-<version>/" prefix is added by
// writeBundleTarball).
type bundleTarEntry struct {
	name string
	mode int64
	data []byte
}

// writeBundleTarball writes entries into a gzip'd tar at tarPath, per spec
// §14.5: a single top-level "<name>-<version>/" directory, entries sorted by
// name for determinism.
func writeBundleTarball(tarPath, name, version string, entries []bundleTarEntry) error {
	f, err := os.Create(tarPath)
	if err != nil {
		return fmt.Errorf("create tarball: %w", err)
	}

	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)

	mtime := sourceDateEpoch()
	prefix := fmt.Sprintf("%s-%s", name, version)

	sorted := make([]bundleTarEntry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].name < sorted[j].name })

	for _, e := range sorted {
		hdr := &tar.Header{
			Name:     prefix + "/" + e.name,
			Mode:     e.mode,
			Size:     int64(len(e.data)),
			ModTime:  mtime,
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return fmt.Errorf("write tar header for %s: %w", e.name, err)
		}
		if _, err := tw.Write(e.data); err != nil {
			return fmt.Errorf("write tar entry %s: %w", e.name, err)
		}
	}

	if err := tw.Close(); err != nil {
		return fmt.Errorf("flush tar: %w", err)
	}
	if err := gw.Close(); err != nil {
		return fmt.Errorf("flush gzip: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close output: %w", err)
	}
	return nil
}

// sourceDateEpoch returns the mtime for tarball entries. Honors the
// SOURCE_DATE_EPOCH environment variable (standard for reproducible builds);
// falls back to the current time.
func sourceDateEpoch() time.Time {
	if v := os.Getenv("SOURCE_DATE_EPOCH"); v != "" {
		if sec, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			return time.Unix(sec, 0).UTC()
		}
	}
	return time.Now().UTC()
}
