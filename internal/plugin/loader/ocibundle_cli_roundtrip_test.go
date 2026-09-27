package loader

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/plugin/container"
	plugincmd "github.com/felag-engineering/gleipnir/plugin-sdk/cmd/gleipnir-plugin/cmd"
	"github.com/felag-engineering/gleipnir/plugin-sdk/signing"
)

// TestOCIBundleCLIRoundTrip proves the CLI and the host loader agree on the
// v2 bundle format end to end: a bundle built by `gleipnir-plugin package`
// opens as a structurally valid OCI bundle, its signature verifies against
// the pairing internal/plugin/loader's own verifier expects, and it installs
// clean through OCIInstaller over a fake container runtime. Each half of this
// (the CLI writer, the loader reader) is otherwise only tested against its
// own hand-built fixtures — this is the seam where a filename or a payload
// pairing could drift between the two without either side's own tests
// noticing.
//
// The signing key is generated fresh for this run and never touches disk
// outside t.TempDir(); it is discarded when the test exits.
func TestOCIBundleCLIRoundTrip(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	pk, sk, err := signing.GenerateKeypair(nil)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	keyPath := filepath.Join(dir, "signing.key")
	pubPath := filepath.Join(dir, "signing.pub")
	if err := os.WriteFile(keyPath, signing.MarshalSecretKey(sk, "roundtrip test key"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	if err := os.WriteFile(pubPath, signing.MarshalPublicKey(pk, "roundtrip test pub"), 0o644); err != nil {
		t.Fatalf("write pub: %v", err)
	}

	archivePath, digest := buildDockerLayoutArchive(t, dir)

	manifestPath := filepath.Join(dir, "manifest.yaml")
	manifestYAML := v2Manifest("roundtrip-plugin", "1.0.0", digest)
	if err := os.WriteFile(manifestPath, manifestYAML, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	outDir := filepath.Join(dir, "dist")
	pkg := plugincmd.NewPackageCmd()
	pkg.SetArgs([]string{
		"--manifest", manifestPath,
		"--image-archive", archivePath,
		"--key", keyPath,
		"--pubkey", pubPath,
		"--out-dir", outDir,
	})
	pkg.SetOut(&bytes.Buffer{})
	pkg.SetErr(&bytes.Buffer{})
	if err := pkg.Execute(); err != nil {
		t.Fatalf("gleipnir-plugin package: %v", err)
	}

	entries, err := os.ReadDir(outDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected exactly one bundle in %s, got %v (err=%v)", outDir, entries, err)
	}
	tarPath := filepath.Join(outDir, entries[0].Name())

	// Step 1: the bundle is structurally a v2 bundle, and its signature
	// verifies with the SAME pairing (archive, manifest) the loader's own
	// verifier checks — proving the CLI and the loader agree on both the
	// filenames and the signed payload, not just that each independently
	// produces something self-consistent.
	extractDir := t.TempDir()
	if err := ExtractTarball(tarPath, extractDir, maxTarballBytes, maxTarballFiles); err != nil {
		t.Fatalf("extract bundle: %v", err)
	}
	bundleDir, err := resolveBundleRoot(extractDir)
	if err != nil {
		t.Fatalf("resolve bundle root: %v", err)
	}
	bundle, err := OpenOCIBundle(bundleDir)
	if err != nil {
		t.Fatalf("OpenOCIBundle: %v", err)
	}
	if bundle.ImageDigest() != digest {
		t.Errorf("bundle.ImageDigest() = %q, want %q", bundle.ImageDigest(), digest)
	}

	verifier := &realVerifier{}
	result := verifier.VerifyBundle(bundleDir, bundle.ArchivePath)
	if result.Outcome != OutcomeVerified {
		t.Fatalf("VerifyBundle outcome = %v, want OutcomeVerified (err=%v)", result.Outcome, result.Err)
	}

	// Step 2: the same tarball installs clean through the real install
	// pipeline — signature re-verified, image "loaded", digest matched — and
	// leaves a plugin row awaiting review.
	store, rt, in := newOCIFixture(t, false)
	rt.PendingImages = []container.ImageInfo{{ID: digest}}

	res, err := in.Install(ctx, tarPath)
	if err != nil {
		t.Fatalf("OCIInstaller.Install: %v", err)
	}
	if res.PluginID == "" {
		t.Fatal("no plugin row created")
	}
	if !res.ImageLoaded {
		t.Error("ImageLoaded = false, want true")
	}
	if res.ImageDigest != digest {
		t.Errorf("ImageDigest = %q, want %q", res.ImageDigest, digest)
	}

	plugin, err := store.Queries().GetPluginByName(ctx, "roundtrip-plugin")
	if err != nil {
		t.Fatalf("GetPluginByName: %v", err)
	}
	if plugin.Status != statusPendingReview {
		t.Errorf("status = %q, want %q", plugin.Status, statusPendingReview)
	}
}

// buildDockerLayoutArchive writes a minimal Docker-legacy-format (`docker
// save`) image archive at dir/image.tar: manifest.json plus its referenced
// config blob, with no layers — nothing the CLI's digest check or this test
// needs one for.
func buildDockerLayoutArchive(t *testing.T, dir string) (archivePath, digest string) {
	t.Helper()

	configData := []byte(`{"config":{},"rootfs":{"type":"layers","diff_ids":[]}}`)
	sum := sha256.Sum256(configData)
	digest = "sha256:" + hex.EncodeToString(sum[:])
	configName := hex.EncodeToString(sum[:]) + ".json"

	manifestEntries := []map[string]any{{
		"Config":   configName,
		"RepoTags": []string{"example/roundtrip:latest"},
		"Layers":   []string{},
	}}
	manifestJSON, err := json.Marshal(manifestEntries)
	if err != nil {
		t.Fatalf("marshal manifest.json: %v", err)
	}

	buf := &bytes.Buffer{}
	tw := tar.NewWriter(buf)
	for _, f := range []struct {
		name string
		data []byte
	}{
		{"manifest.json", manifestJSON},
		{configName, configData},
	} {
		hdr := &tar.Header{Name: f.name, Mode: 0o644, Size: int64(len(f.data))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write tar header %s: %v", f.name, err)
		}
		if _, err := tw.Write(f.data); err != nil {
			t.Fatalf("write tar entry %s: %v", f.name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}

	archivePath = filepath.Join(dir, "image.tar")
	if err := os.WriteFile(archivePath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write image archive: %v", err)
	}
	return archivePath, digest
}
