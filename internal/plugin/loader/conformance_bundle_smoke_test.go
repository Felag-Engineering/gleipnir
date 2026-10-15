//go:build conformance_bundle

package loader

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/felag-engineering/gleipnir/plugin-sdk/imagearchive"
)

// TestConformanceStubBundles opens every bundle scripts/build-conformance-stub.sh
// produced the way the host does, so a bundle the script can write but the
// loader would refuse fails here rather than in the DooD suite.
//
// It lives behind a build tag because the bundles come from a container build:
// plain `go test ./...` must not need one. The Plugin SDK build job in CI runs
// the script and then `go test -tags conformance_bundle -run
// TestConformanceStubBundles ./internal/plugin/loader`. Bundles are read from
// $CONFORMANCE_DIST_DIR, defaulting to <repo>/dist.
func TestConformanceStubBundles(t *testing.T) {
	distDir := os.Getenv("CONFORMANCE_DIST_DIR")
	if distDir == "" {
		distDir = filepath.Join("..", "..", "..", "dist")
	}

	modes := []string{"all", "tool-only", "event-only", "human-channel", "human-channel-weak"}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			tarPath := filepath.Join(distDir, "conformance-stub-"+mode+".tgz")
			if _, err := os.Stat(tarPath); err != nil {
				t.Fatalf("bundle missing (run scripts/build-conformance-stub.sh): %v", err)
			}

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

			if want := "conformance-stub-" + mode; bundle.Manifest.Name != want {
				t.Errorf("manifest name = %q, want %q", bundle.Manifest.Name, want)
			}

			// The pin must equal the digest of the image inside the archive:
			// that is what the loaded image's ID is compared against.
			archive, err := os.ReadFile(bundle.ArchivePath)
			if err != nil {
				t.Fatalf("read archive: %v", err)
			}
			info, err := imagearchive.InspectBytes(archive)
			if err != nil {
				t.Fatalf("inspect archive: %v", err)
			}
			if bundle.ImageDigest() != info.ConfigDigest {
				t.Errorf("manifest pins %s, archive image is %s", bundle.ImageDigest(), info.ConfigDigest)
			}

			result := (&realVerifier{}).VerifyBundle(bundleDir, bundle.ArchivePath)
			if result.Outcome != OutcomeVerified {
				t.Fatalf("VerifyBundle outcome = %v, want OutcomeVerified (err=%v)", result.Outcome, result.Err)
			}
		})
	}
}
