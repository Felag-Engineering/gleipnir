package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
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

	if err := runPackage(fakeCmd, binaryPath, manifestPath, keyPath, false, pubPath, outDir, "", false); err != nil {
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

	if err := runPackage(fakeCmd, binaryPath, manifestPath, keyPath, false, pubPath, outDir, "", false); err != nil {
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
	err := runPackage(fakeCmd, binaryPath, manifestPath, "/nonexistent.key", false, "", filepath.Join(dir, "dist"), "", false)
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

	if err := runPackage(fakeCmd, binaryPath, manifestPath, "", false, "", outDir, "", true); err != nil {
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

	if err := runPackage(fakeCmd, binaryPath, manifestPath, keyPath, false, pubPath, outDir, sbomPath, false); err != nil {
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

	if err := runPackage(fakeCmd, binaryPath, manifestPath, keyPath, false, pubPath, outDir, "", false); err != nil {
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

	err := runPackage(fakeCmd, binaryPath, manifestPath, "", false, "", filepath.Join(dir, "dist"), "", true)
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
