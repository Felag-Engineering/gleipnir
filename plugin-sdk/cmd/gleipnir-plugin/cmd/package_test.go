package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/felag-engineering/gleipnir/plugin-sdk/signing"
)

func TestRunPackageSignedBundle(t *testing.T) {
	orig := runBinary
	defer func() { runBinary = orig }()
	runBinary = func(_ string, _ []string) ([]byte, error) {
		return []byte(sampleManifestJSON), nil
	}

	dir := t.TempDir()
	outDir := filepath.Join(dir, "dist")

	pk := writeTestKey(t, dir)
	binaryPath := writeTestBinary(t, dir)
	manifestPath := writeTestManifest(t, dir)

	fakeCmd := &cobra.Command{}
	fakeCmd.SetOut(&bytes.Buffer{})
	fakeCmd.SetErr(&bytes.Buffer{})
	fakeCmd.SetIn(strings.NewReader(""))

	keyPath := filepath.Join(dir, "signing.key")
	pubPath := filepath.Join(dir, "signing.pub")

	if err := runPackage(fakeCmd, binaryPath, manifestPath, "", "", keyPath, false, pubPath, outDir, "", false); err != nil {
		t.Fatalf("runPackage: %v", err)
	}

	// Locate the tarball.
	entries, err := os.ReadDir(outDir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("no tarball in output dir: %v", err)
	}

	tarPath := filepath.Join(outDir, entries[0].Name())
	contents := readTarball(t, tarPath)

	// Verify required files exist. The binary is stored under manifest.Name
	// ("testplugin"), NOT the source binary basename ("myplugin"), because the
	// host locates it at <bundle>/<manifest.Name> to hash and verify it.
	if _, ok := contents["testplugin"]; !ok {
		t.Error("binary (testplugin, from manifest.Name) not in bundle; keys:", mapKeys(contents))
	}
	if _, ok := contents["myplugin"]; ok {
		t.Error("binary stored under source basename 'myplugin'; must use manifest.Name 'testplugin'")
	}
	if _, ok := contents["manifest.yaml"]; !ok {
		t.Error("manifest.yaml not in bundle")
	}

	// .minisig filename must derive from manifest.Name, not binary basename.
	sigKey := "testplugin.minisig"
	sigData, ok := contents[sigKey]
	if !ok {
		t.Errorf("expected %s in bundle; keys: %v", sigKey, mapKeys(contents))
	}
	if _, ok := contents["signing.pub"]; !ok {
		t.Error("signing.pub not in bundle")
	}

	// Verify the signature.
	if ok && len(sigData) > 0 {
		sig, _, err := signing.ParseSignature(sigData)
		if err != nil {
			t.Fatalf("parse signature: %v", err)
		}
		payload := signing.PluginPayload(contents["testplugin"], contents["manifest.yaml"])
		if err := signing.Verify(pk, payload, sig, sig.TrustedComment); err != nil {
			t.Errorf("verify bundle signature: %v", err)
		}
	}
}

// TestRunPackageEncryptedKeyProducesVerifiableSignature is the regression test
// for the bug where DecryptSecretKey discarded the plaintext KeyID, causing the
// bundled .minisig to embed the still-encrypted KeyID and fail Verify.
func TestRunPackageEncryptedKeyProducesVerifiableSignature(t *testing.T) {
	const passphrase = "test-passphrase-for-package"
	dir := t.TempDir()
	outDir := filepath.Join(dir, "dist")

	pk := writeEncryptedTestKey(t, dir, passphrase)
	binaryPath := writeTestBinary(t, dir)
	manifestPath := writeTestManifest(t, dir)

	// Provide passphrase via env var (CI mode).
	t.Setenv("GLEIPNIR_PLUGIN_SIGNING_KEY_PASSPHRASE", passphrase)

	fakeCmd := &cobra.Command{}
	fakeCmd.SetOut(&bytes.Buffer{})
	fakeCmd.SetErr(&bytes.Buffer{})
	fakeCmd.SetIn(strings.NewReader(""))

	keyPath := filepath.Join(dir, "signing.key")
	pubPath := filepath.Join(dir, "signing.pub")

	if err := runPackage(fakeCmd, binaryPath, manifestPath, "", "", keyPath, false, pubPath, outDir, "", false); err != nil {
		t.Fatalf("runPackage with encrypted key: %v", err)
	}

	entries, err := os.ReadDir(outDir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("no tarball in output dir: %v", err)
	}

	tarPath := filepath.Join(outDir, entries[0].Name())
	contents := readTarball(t, tarPath)

	sigData, ok := contents["testplugin.minisig"]
	if !ok {
		t.Fatalf("testplugin.minisig not in bundle; keys: %v", mapKeys(contents))
	}

	sig, _, err := signing.ParseSignature(sigData)
	if err != nil {
		t.Fatalf("parse signature: %v", err)
	}
	payload := signing.PluginPayload(contents["testplugin"], contents["manifest.yaml"])
	if err := signing.Verify(pk, payload, sig, sig.TrustedComment); err != nil {
		t.Errorf("verify encrypted-key bundle signature: %v", err)
	}
}

func TestRunPackageUnsignedRequiresFlag(t *testing.T) {
	dir := t.TempDir()
	binaryPath := writeTestBinary(t, dir)
	manifestPath := writeTestManifest(t, dir)

	fakeCmd := &cobra.Command{}
	fakeCmd.SetOut(&bytes.Buffer{})
	fakeCmd.SetErr(&bytes.Buffer{})
	fakeCmd.SetIn(strings.NewReader(""))

	// Without --unsigned and no key that works → should fail trying to load key.
	err := runPackage(fakeCmd, binaryPath, manifestPath, "", "", "/nonexistent.key", false, "", filepath.Join(dir, "dist"), "", false)
	if err == nil {
		t.Error("expected error when key is missing, got nil")
	}
}

func TestRunPackageUnsignedBundle(t *testing.T) {
	dir := t.TempDir()
	outDir := filepath.Join(dir, "dist")

	binaryPath := writeTestBinary(t, dir)
	manifestPath := writeTestManifest(t, dir)

	fakeCmd := &cobra.Command{}
	fakeCmd.SetOut(&bytes.Buffer{})
	var errOut bytes.Buffer
	fakeCmd.SetErr(&errOut)
	fakeCmd.SetIn(strings.NewReader(""))

	if err := runPackage(fakeCmd, binaryPath, manifestPath, "", "", "", false, "", outDir, "", true); err != nil {
		t.Fatalf("runPackage --unsigned: %v", err)
	}

	// Warning should be on stderr.
	if !strings.Contains(errOut.String(), "unsigned") {
		t.Errorf("expected unsigned warning on stderr, got: %q", errOut.String())
	}

	entries, _ := os.ReadDir(outDir)
	tarPath := filepath.Join(outDir, entries[0].Name())
	contents := readTarball(t, tarPath)

	if _, ok := contents["testplugin.minisig"]; ok {
		t.Error("unsigned bundle should not contain .minisig")
	}
	if _, ok := contents["signing.pub"]; ok {
		t.Error("unsigned bundle should not contain signing.pub")
	}
	if _, ok := contents["manifest.yaml"]; !ok {
		t.Error("unsigned bundle should contain manifest.yaml")
	}
}

func TestRunPackageSBOMIncluded(t *testing.T) {
	dir := t.TempDir()
	outDir := filepath.Join(dir, "dist")

	pk := writeTestKey(t, dir)
	_ = pk
	binaryPath := writeTestBinary(t, dir)
	manifestPath := writeTestManifest(t, dir)
	sbomPath := filepath.Join(dir, "sbom.cyclonedx.json")
	sbomContent := []byte(`{"bomFormat":"CycloneDX","specVersion":"1.4"}`)
	if err := os.WriteFile(sbomPath, sbomContent, 0o644); err != nil {
		t.Fatalf("write sbom: %v", err)
	}

	fakeCmd := &cobra.Command{}
	fakeCmd.SetOut(&bytes.Buffer{})
	fakeCmd.SetErr(&bytes.Buffer{})
	fakeCmd.SetIn(strings.NewReader(""))

	keyPath := filepath.Join(dir, "signing.key")
	pubPath := filepath.Join(dir, "signing.pub")

	if err := runPackage(fakeCmd, binaryPath, manifestPath, "", "", keyPath, false, pubPath, outDir, sbomPath, false); err != nil {
		t.Fatalf("runPackage with sbom: %v", err)
	}

	entries, _ := os.ReadDir(outDir)
	tarPath := filepath.Join(outDir, entries[0].Name())
	contents := readTarball(t, tarPath)

	if data, ok := contents["sbom.cyclonedx.json"]; !ok || !bytes.Equal(data, sbomContent) {
		t.Errorf("sbom.cyclonedx.json not in bundle or content mismatch")
	}
}

func TestRunPackageMinisigFilenameFromManifestName(t *testing.T) {
	dir := t.TempDir()
	outDir := filepath.Join(dir, "dist")

	writeTestKey(t, dir)
	binaryPath := writeTestBinary(t, dir)
	manifestPath := writeTestManifest(t, dir)

	fakeCmd := &cobra.Command{}
	fakeCmd.SetOut(&bytes.Buffer{})
	fakeCmd.SetErr(&bytes.Buffer{})
	fakeCmd.SetIn(strings.NewReader(""))

	keyPath := filepath.Join(dir, "signing.key")
	pubPath := filepath.Join(dir, "signing.pub")

	if err := runPackage(fakeCmd, binaryPath, manifestPath, "", "", keyPath, false, pubPath, outDir, "", false); err != nil {
		t.Fatalf("runPackage: %v", err)
	}

	entries, _ := os.ReadDir(outDir)
	tarPath := filepath.Join(outDir, entries[0].Name())
	contents := readTarball(t, tarPath)

	// manifest.Name = "testplugin", binary basename = "myplugin"
	// .minisig must derive from manifest.Name.
	if _, ok := contents["testplugin.minisig"]; !ok {
		t.Errorf("expected testplugin.minisig (from manifest.Name), not binary name; keys: %v", mapKeys(contents))
	}
	if _, ok := contents["myplugin.minisig"]; ok {
		t.Error("found myplugin.minisig — .minisig should use manifest.Name, not binary basename")
	}
}

func TestRunPackageRejectsPathTraversalInName(t *testing.T) {
	dir := t.TempDir()
	binaryPath := writeTestBinary(t, dir)

	// Write a manifest with a path-traversal name.
	evilManifest := []byte("name: \"../evil\"\nversion: \"1.0.0\"\nkind: tool\n")
	manifestPath := filepath.Join(dir, "manifest.yaml")
	if err := os.WriteFile(manifestPath, evilManifest, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	fakeCmd := &cobra.Command{}
	fakeCmd.SetOut(&bytes.Buffer{})
	fakeCmd.SetErr(&bytes.Buffer{})
	fakeCmd.SetIn(strings.NewReader(""))

	err := runPackage(fakeCmd, binaryPath, manifestPath, "", "", "", false, "", filepath.Join(dir, "dist"), "", true)
	if err == nil {
		t.Fatal("expected error for path-traversal name, got nil")
	}
	if !strings.Contains(err.Error(), "path separator") && !strings.Contains(err.Error(), "starts with '.'") {
		t.Errorf("expected path-traversal error, got: %v", err)
	}
}

// writeTestBinary writes a fake executable and returns its path.
func writeTestBinary(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "myplugin")
	if err := os.WriteFile(p, []byte("fake binary data"), 0o755); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	return p
}

// writeTestManifest writes canonicalManifestYAML to manifest.yaml and returns
// its path.
func writeTestManifest(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "manifest.yaml")
	if err := os.WriteFile(p, []byte(canonicalManifestYAML), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return p
}

// readTarball reads a .tar.gz and returns a map of basename → content for all
// files (stripping the top-level directory prefix).
func readTarball(t *testing.T, path string) map[string][]byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open tarball: %v", err)
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	contents := map[string][]byte{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar next: %v", err)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("tar read: %v", err)
		}
		// Strip top-level directory.
		parts := strings.SplitN(hdr.Name, "/", 2)
		if len(parts) == 2 {
			contents[parts[1]] = data
		} else {
			contents[hdr.Name] = data
		}
	}
	return contents
}

func mapKeys(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// --- v2 (containerized) packaging tests ---

// v2ManifestYAML renders a minimal valid v2 manifest pinning digest.
func v2ManifestYAML(name, version, digest string) []byte {
	return []byte(fmt.Sprintf(`schema_version: "2"
name: %s
version: %s
package:
  registry_type: oci
  identifier: ghcr.io/acme/%s@%s
  transport:
    type: streamable-http
    port: 8080
gleipnir:
  profiles:
    tool_provider: {}
`, name, version, name, digest))
}

// writeArchiveTarFile writes one entry into an in-progress tar.Writer.
func writeArchiveTarFile(t *testing.T, tw *tar.Writer, name string, data []byte) {
	t.Helper()
	hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(data))}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("write tar header %s: %v", name, err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatalf("write tar entry %s: %v", name, err)
	}
}

// buildDockerImageArchive writes a minimal Docker-legacy-format (`docker
// save`) image archive: manifest.json plus its referenced config blob. It
// carries no layers — nothing package-time reads needs one.
func buildDockerImageArchive(t *testing.T, dir string) (archivePath, digest string) {
	t.Helper()

	configData := []byte(`{"config":{},"rootfs":{"type":"layers","diff_ids":[]}}`)
	sum := sha256.Sum256(configData)
	digest = "sha256:" + hex.EncodeToString(sum[:])
	configName := hex.EncodeToString(sum[:]) + ".json"

	manifestEntries := []map[string]any{{
		"Config":   configName,
		"RepoTags": []string{"example/test:latest"},
		"Layers":   []string{},
	}}
	manifestJSON, err := json.Marshal(manifestEntries)
	if err != nil {
		t.Fatalf("marshal manifest.json: %v", err)
	}

	buf := &bytes.Buffer{}
	tw := tar.NewWriter(buf)
	writeArchiveTarFile(t, tw, "manifest.json", manifestJSON)
	writeArchiveTarFile(t, tw, configName, configData)
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}

	archivePath = filepath.Join(dir, "image.tar")
	if err := os.WriteFile(archivePath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write image archive: %v", err)
	}
	return archivePath, digest
}

// buildOCILayoutImageArchive writes a minimal OCI-layout (`docker save
// --format oci-archive` / `podman save --format oci-archive`) image archive:
// index.json pointing at a manifest blob, which in turn names the config
// blob's digest.
func buildOCILayoutImageArchive(t *testing.T, dir string) (archivePath, digest string) {
	t.Helper()

	configData := []byte(`{"config":{},"rootfs":{"type":"layers","diff_ids":[]}}`)
	configSum := sha256.Sum256(configData)
	configDigest := "sha256:" + hex.EncodeToString(configSum[:])

	imageManifest := map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"config": map[string]any{
			"mediaType": "application/vnd.oci.image.config.v1+json",
			"digest":    configDigest,
			"size":      len(configData),
		},
		"layers": []any{},
	}
	manifestData, err := json.Marshal(imageManifest)
	if err != nil {
		t.Fatalf("marshal image manifest: %v", err)
	}
	manifestSum := sha256.Sum256(manifestData)

	index := map[string]any{
		"schemaVersion": 2,
		"manifests": []map[string]any{{
			"mediaType": "application/vnd.oci.image.manifest.v1+json",
			"digest":    "sha256:" + hex.EncodeToString(manifestSum[:]),
			"size":      len(manifestData),
		}},
	}
	indexData, err := json.Marshal(index)
	if err != nil {
		t.Fatalf("marshal index.json: %v", err)
	}

	buf := &bytes.Buffer{}
	tw := tar.NewWriter(buf)
	writeArchiveTarFile(t, tw, "index.json", indexData)
	writeArchiveTarFile(t, tw, "blobs/sha256/"+hex.EncodeToString(manifestSum[:]), manifestData)
	writeArchiveTarFile(t, tw, "blobs/sha256/"+hex.EncodeToString(configSum[:]), configData)
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}

	archivePath = filepath.Join(dir, "image.tar")
	if err := os.WriteFile(archivePath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write image archive: %v", err)
	}
	return archivePath, configDigest
}

func TestRunPackageV2SignedBundle(t *testing.T) {
	dir := t.TempDir()
	outDir := filepath.Join(dir, "dist")

	archivePath, digest := buildDockerImageArchive(t, dir)
	manifestPath := filepath.Join(dir, "manifest.yaml")
	if err := os.WriteFile(manifestPath, v2ManifestYAML("v2plugin", "1.0.0", digest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	pk := writeTestKey(t, dir)
	keyPath := filepath.Join(dir, "signing.key")
	pubPath := filepath.Join(dir, "signing.pub")

	fakeCmd := &cobra.Command{}
	fakeCmd.SetOut(&bytes.Buffer{})
	fakeCmd.SetErr(&bytes.Buffer{})
	fakeCmd.SetIn(strings.NewReader(""))

	if err := runPackage(fakeCmd, "", manifestPath, archivePath, "", keyPath, false, pubPath, outDir, "", false); err != nil {
		t.Fatalf("runPackage: %v", err)
	}

	entries, err := os.ReadDir(outDir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("no tarball in output dir: %v", err)
	}
	contents := readTarball(t, filepath.Join(outDir, entries[0].Name()))

	if _, ok := contents["image.tar"]; !ok {
		t.Errorf("image.tar not in bundle; keys: %v", mapKeys(contents))
	}
	if !bytes.Equal(contents["image.tar"], mustReadFile(t, archivePath)) {
		t.Error("bundled image.tar does not match the source archive")
	}
	if _, ok := contents["manifest.yaml"]; !ok {
		t.Error("manifest.yaml not in bundle")
	}
	sigData, ok := contents["v2plugin.minisig"]
	if !ok {
		t.Fatalf("v2plugin.minisig not in bundle; keys: %v", mapKeys(contents))
	}

	sig, _, err := signing.ParseSignature(sigData)
	if err != nil {
		t.Fatalf("parse signature: %v", err)
	}
	payload := signing.PluginPayload(contents["image.tar"], contents["manifest.yaml"])
	if err := signing.Verify(pk, payload, sig, sig.TrustedComment); err != nil {
		t.Errorf("verify bundle signature: %v", err)
	}
}

func TestRunPackageV2OCILayoutArchive(t *testing.T) {
	dir := t.TempDir()
	outDir := filepath.Join(dir, "dist")

	archivePath, digest := buildOCILayoutImageArchive(t, dir)
	manifestPath := filepath.Join(dir, "manifest.yaml")
	if err := os.WriteFile(manifestPath, v2ManifestYAML("ocilayout", "1.0.0", digest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	fakeCmd := &cobra.Command{}
	fakeCmd.SetOut(&bytes.Buffer{})
	fakeCmd.SetErr(&bytes.Buffer{})
	fakeCmd.SetIn(strings.NewReader(""))

	if err := runPackage(fakeCmd, "", manifestPath, archivePath, "", "", false, "", outDir, "", true); err != nil {
		t.Fatalf("runPackage: %v", err)
	}

	entries, err := os.ReadDir(outDir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("no tarball in output dir: %v", err)
	}
	contents := readTarball(t, filepath.Join(outDir, entries[0].Name()))
	if _, ok := contents["image.tar"]; !ok {
		t.Errorf("image.tar not in bundle; keys: %v", mapKeys(contents))
	}
}

func TestRunPackageV2DigestMismatchFailsNamingBothDigests(t *testing.T) {
	dir := t.TempDir()
	archivePath, _ := buildDockerImageArchive(t, dir)

	pinnedDigest := "sha256:" + strings.Repeat("a", 64)
	manifestPath := filepath.Join(dir, "manifest.yaml")
	if err := os.WriteFile(manifestPath, v2ManifestYAML("mismatched", "1.0.0", pinnedDigest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	fakeCmd := &cobra.Command{}
	fakeCmd.SetOut(&bytes.Buffer{})
	fakeCmd.SetErr(&bytes.Buffer{})
	fakeCmd.SetIn(strings.NewReader(""))

	// --unsigned so the mismatch (checked before signing) is what fails this
	// call, not an absent key.
	err := runPackage(fakeCmd, "", manifestPath, archivePath, "", "", false, "", filepath.Join(dir, "dist"), "", true)
	if err == nil {
		t.Fatal("expected a digest mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), pinnedDigest) {
		t.Errorf("error does not name the pinned digest %s: %v", pinnedDigest, err)
	}
	if strings.Count(err.Error(), "sha256:") < 2 {
		t.Errorf("error does not name both digests: %v", err)
	}
}

func TestRunPackageV2ManifestWithBinaryIsAnError(t *testing.T) {
	dir := t.TempDir()
	archivePath, digest := buildDockerImageArchive(t, dir)
	manifestPath := filepath.Join(dir, "manifest.yaml")
	if err := os.WriteFile(manifestPath, v2ManifestYAML("wrongmode", "1.0.0", digest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	binaryPath := writeTestBinary(t, dir)

	fakeCmd := &cobra.Command{}
	fakeCmd.SetOut(&bytes.Buffer{})
	fakeCmd.SetErr(&bytes.Buffer{})
	fakeCmd.SetIn(strings.NewReader(""))

	err := runPackage(fakeCmd, binaryPath, manifestPath, archivePath, "", "", false, "", filepath.Join(dir, "dist"), "", true)
	if err == nil {
		t.Fatal("expected an error for a v2 manifest with --binary set, got nil")
	}
	if !strings.Contains(err.Error(), "--binary") {
		t.Errorf("error does not mention --binary: %v", err)
	}
}

func TestRunPackageV1ManifestWithImageFlagIsAnError(t *testing.T) {
	dir := t.TempDir()
	binaryPath := writeTestBinary(t, dir)
	manifestPath := writeTestManifest(t, dir) // v1 manifest
	archivePath, _ := buildDockerImageArchive(t, dir)

	fakeCmd := &cobra.Command{}
	fakeCmd.SetOut(&bytes.Buffer{})
	fakeCmd.SetErr(&bytes.Buffer{})
	fakeCmd.SetIn(strings.NewReader(""))

	err := runPackage(fakeCmd, binaryPath, manifestPath, archivePath, "", "", false, "", filepath.Join(dir, "dist"), "", true)
	if err == nil {
		t.Fatal("expected an error for a v1 manifest with --image-archive set, got nil")
	}
	if !strings.Contains(err.Error(), "--image-archive") {
		t.Errorf("error does not mention --image-archive: %v", err)
	}
}

func TestRunPackageV2RequiresExactlyOneImageFlag(t *testing.T) {
	dir := t.TempDir()
	_, digest := buildDockerImageArchive(t, dir)
	manifestPath := filepath.Join(dir, "manifest.yaml")
	if err := os.WriteFile(manifestPath, v2ManifestYAML("needsimage", "1.0.0", digest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	fakeCmd := &cobra.Command{}
	fakeCmd.SetOut(&bytes.Buffer{})
	fakeCmd.SetErr(&bytes.Buffer{})
	fakeCmd.SetIn(strings.NewReader(""))

	if err := runPackage(fakeCmd, "", manifestPath, "", "", "", false, "", filepath.Join(dir, "dist"), "", true); err == nil {
		t.Error("expected an error when neither --image-archive nor --image is set")
	}
	if err := runPackage(fakeCmd, "", manifestPath, "archive1.tar", "docker.io/x/y", "", false, "", filepath.Join(dir, "dist"), "", true); err == nil {
		t.Error("expected an error when both --image-archive and --image are set")
	}
}

func TestRunPackageV2RejectsDashPrefixedImageRef(t *testing.T) {
	dir := t.TempDir()
	_, digest := buildDockerImageArchive(t, dir)
	manifestPath := filepath.Join(dir, "manifest.yaml")
	if err := os.WriteFile(manifestPath, v2ManifestYAML("dashref", "1.0.0", digest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	fakeCmd := &cobra.Command{}
	fakeCmd.SetOut(&bytes.Buffer{})
	fakeCmd.SetErr(&bytes.Buffer{})
	fakeCmd.SetIn(strings.NewReader(""))

	err := runPackage(fakeCmd, "", manifestPath, "", "--rm", "", false, "", filepath.Join(dir, "dist"), "", true)
	if err == nil {
		t.Fatal("expected an error for a dash-prefixed --image ref, got nil")
	}
	if !strings.Contains(err.Error(), "looks like a flag") {
		t.Errorf("expected a leading-dash rejection, got: %v", err)
	}
}

func TestRunPackageV2WarnsOnUnexpectedTag(t *testing.T) {
	dir := t.TempDir()
	outDir := filepath.Join(dir, "dist")

	archivePath, digest := buildDockerImageArchive(t, dir) // tagged "example/test:latest"
	manifestPath := filepath.Join(dir, "manifest.yaml")
	// v2ManifestYAML's identifier is "ghcr.io/acme/<name>@<digest>", a
	// different repository than the archive's own "example/test" tag.
	if err := os.WriteFile(manifestPath, v2ManifestYAML("tagmismatch", "1.0.0", digest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	fakeCmd := &cobra.Command{}
	fakeCmd.SetOut(&bytes.Buffer{})
	var errOut bytes.Buffer
	fakeCmd.SetErr(&errOut)
	fakeCmd.SetIn(strings.NewReader(""))

	if err := runPackage(fakeCmd, "", manifestPath, archivePath, "", "", false, "", outDir, "", true); err != nil {
		t.Fatalf("runPackage: %v", err)
	}
	if !strings.Contains(errOut.String(), "example/test:latest") {
		t.Errorf("expected a tag-mismatch warning naming the archive's tag, got: %q", errOut.String())
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}
