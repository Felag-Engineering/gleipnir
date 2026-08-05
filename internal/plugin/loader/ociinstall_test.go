package loader

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/plugin/container"
	"github.com/felag-engineering/gleipnir/plugin-sdk/signing"
)

// v2Manifest renders a minimal valid v2 manifest pinning digest.
func v2Manifest(name, version, digest string) []byte {
	return []byte(fmt.Sprintf(`schema_version: "2"
name: %s
version: %s
description: a containerized test plugin
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

// digestOf renders a plausible sha256 digest from a seed, so a test can name
// "the digest the manifest pins" and "some other digest" without hardcoding
// 64-character strings inline.
func digestOf(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ociBundleOptions describes a v2 bundle to build.
type ociBundleOptions struct {
	name    string
	version string
	digest  string // what the manifest pins

	// unsigned omits signing.pub and the .minisig entirely.
	unsigned bool
	// tamper signs a different archive than the one packaged, modelling a
	// bundle whose image was swapped after signing.
	tamper bool
	// omitArchive leaves out image.tar.
	omitArchive bool
	// manifestOverride replaces the rendered manifest wholesale.
	manifestOverride []byte
}

// buildOCIBundle writes a v2 bundle tarball and returns its path. Each signed
// bundle gets a FRESH keypair, which is what makes the TOFU test's second
// release a genuine key rotation rather than a contrived one.
func buildOCIBundle(t *testing.T, opts ociBundleOptions) string {
	t.Helper()

	manifestBytes := opts.manifestOverride
	if manifestBytes == nil {
		manifestBytes = v2Manifest(opts.name, opts.version, opts.digest)
	}
	archiveBytes := []byte("fake OCI image archive for " + opts.name)

	entries := []tarEntry{{name: ociManifestFilename, content: manifestBytes, mode: 0o644}}
	if !opts.omitArchive {
		entries = append(entries, tarEntry{name: ociImageArchiveName, content: archiveBytes, mode: 0o644})
	}

	if !opts.unsigned {
		pk, sk, err := signing.GenerateKeypair(rand.Reader)
		if err != nil {
			t.Fatalf("generate keypair: %v", err)
		}

		signed := archiveBytes
		if opts.tamper {
			signed = []byte("a different archive entirely")
		}
		sig, err := signing.Sign(sk.SecretKey, sk.KeyID, signing.PluginPayload(signed, manifestBytes), "trusted comment")
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		entries = append(entries,
			tarEntry{name: "signing.pub", content: signing.MarshalPublicKey(pk, "test key"), mode: 0o644},
			tarEntry{name: opts.name + ".minisig", content: signing.MarshalSignature(sig, "test sig"), mode: 0o644},
		)
	}

	tarPath := filepath.Join(t.TempDir(), opts.name+".tar.gz")
	writeTarball(t, tarPath, entries)
	return tarPath
}

// newOCIFixture stands up a store, a v1 Installer, a Fake runtime, and the v2
// installer over both.
func newOCIFixture(t *testing.T, allowUnsigned bool) (*db.Store, *container.Fake, *OCIInstaller) {
	t.Helper()
	store := openTestStore(t)
	base := NewInstaller(&realVerifier{allowUnsigned: allowUnsigned}, store.Queries(), store.DB(), nil, t.TempDir())
	base.clock = func() time.Time { return time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC) }
	rt := container.NewFake()
	return store, rt, NewOCIInstaller(base, rt)
}

// The happy path: a signed bundle whose archive really does contain the pinned
// image installs, records the image, and leaves the plugin awaiting review.
func TestOCIInstall_SignedBundleLoadsAndPins(t *testing.T) {
	ctx := context.Background()
	store, rt, in := newOCIFixture(t, false)

	digest := digestOf("happy")
	tarPath := buildOCIBundle(t, ociBundleOptions{name: "acme-plugin", version: "1.0.0", digest: digest})
	rt.PendingImages = []container.ImageInfo{{ID: digest, SizeBytes: 4096}}

	res, err := in.Install(ctx, tarPath)
	if err != nil {
		t.Fatalf("Install: %v", err)
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
	if rt.Loads != 1 {
		t.Errorf("runtime saw %d loads, want 1", rt.Loads)
	}
	if rt.LoadedBytes == 0 {
		t.Error("the archive was never read; ImageLoad got an empty reader")
	}

	plugin, err := store.Queries().GetPluginByName(ctx, "acme-plugin")
	if err != nil {
		t.Fatalf("GetPluginByName: %v", err)
	}
	if plugin.Status != statusPendingReview {
		t.Errorf("status = %q, want %q — a v2 install is still consent-gated", plugin.Status, statusPendingReview)
	}
	if plugin.BinaryPath != nil {
		t.Errorf("binary_path = %v, want NULL — a containerized plugin has no host binary", *plugin.BinaryPath)
	}
	if !strings.Contains(plugin.ManifestSnapshot, `schema_version: "2"`) {
		t.Error("manifest snapshot is not the v2 manifest that was signed")
	}

	image, err := store.Queries().GetContainerImage(ctx, digest)
	if err != nil {
		t.Fatalf("GetContainerImage: %v", err)
	}
	if image.Reference != "ghcr.io/acme/acme-plugin@"+digest {
		t.Errorf("reference = %q, want the manifest's pinned reference", image.Reference)
	}
	if image.PluginID == nil || *image.PluginID != res.PluginID {
		t.Errorf("image plugin_id = %v, want %q", image.PluginID, res.PluginID)
	}
	if image.SizeBytes == nil || *image.SizeBytes != 4096 {
		t.Errorf("size_bytes = %v, want 4096", image.SizeBytes)
	}
}

// The pin is the whole mechanism by which an admin's consent means anything.
// An archive that yields a different image does not install, and says so
// loudly.
func TestOCIInstall_DigestMismatchRejectsAndLeavesNothing(t *testing.T) {
	ctx := context.Background()
	store, rt, in := newOCIFixture(t, false)

	pinned := digestOf("pinned")
	tarPath := buildOCIBundle(t, ociBundleOptions{name: "sneaky", version: "1.0.0", digest: pinned})
	// The archive contained something else entirely.
	rt.PendingImages = []container.ImageInfo{{ID: digestOf("actually-this")}}

	_, err := in.Install(ctx, tarPath)
	var rejected *InstallRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("Install error = %v, want *InstallRejectedError", err)
	}
	if rejected.Reason != rejectImageDigestMismatch {
		t.Errorf("reason = %q, want %q", rejected.Reason, rejectImageDigestMismatch)
	}

	// #349's lesson: a failed install must leave nothing half-written.
	if _, err := store.Queries().GetPluginByName(ctx, "sneaky"); err == nil {
		t.Error("a plugins row survives a digest mismatch")
	}
	if _, err := store.Queries().GetContainerImage(ctx, pinned); err == nil {
		t.Error("an image row survives a digest mismatch")
	}
	images, err := store.Queries().ListContainerImages(ctx)
	if err != nil {
		t.Fatalf("ListContainerImages: %v", err)
	}
	if len(images) != 0 {
		t.Errorf("%d image rows written for a rejected install, want 0", len(images))
	}

	assertAuditEvent(t, store, auditImageDigestMismatch, severityHigh)
}

// A load that "succeeds" without the pinned image being present is the same
// failure: the expected bytes are not here.
func TestOCIInstall_MissingPinnedImageIsAMismatch(t *testing.T) {
	ctx := context.Background()
	store, rt, in := newOCIFixture(t, false)

	tarPath := buildOCIBundle(t, ociBundleOptions{name: "empty-archive", version: "1.0.0", digest: digestOf("nothing")})
	rt.PendingImages = nil // the load reports success and produces no image

	_, err := in.Install(ctx, tarPath)
	var rejected *InstallRejectedError
	if !errors.As(err, &rejected) || rejected.Reason != rejectImageDigestMismatch {
		t.Fatalf("Install error = %v, want an image_digest_mismatch rejection", err)
	}
	if _, err := store.Queries().GetPluginByName(ctx, "empty-archive"); err == nil {
		t.Error("a plugins row survives an absent image")
	}
}

// A repo-digest match is as good as an ID match: a bundle built from an OCI
// layout on a daemon that preserves the manifest digest must not be spuriously
// rejected.
func TestOCIInstall_RepoDigestMatchIsAccepted(t *testing.T) {
	ctx := context.Background()
	_, rt, in := newOCIFixture(t, false)

	digest := digestOf("repo-digest")
	tarPath := buildOCIBundle(t, ociBundleOptions{name: "layout-plugin", version: "1.0.0", digest: digest})
	rt.PendingImages = []container.ImageInfo{{
		ID:          digestOf("some-config-digest"),
		RepoDigests: []string{"ghcr.io/acme/layout-plugin@" + digest},
	}}
	// The Fake keys images by every reference they answer to, including the
	// repo digest, so an inspect by bare digest would miss — teach it the
	// digest alias the daemon would also answer to.
	rt.AddImage(container.ImageInfo{
		ID:          digest,
		RepoDigests: []string{"ghcr.io/acme/layout-plugin@" + digest},
	})

	res, err := in.Install(ctx, tarPath)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !res.ImageLoaded {
		t.Error("ImageLoaded = false for an image matching by repo digest")
	}
}

// A tampered bundle — archive swapped after signing — fails verification, and
// the audit event is the operator-visible signal rather than an error
// (ADR-046, matching the v1 path exactly).
func TestOCIInstall_TamperedBundleIsRejected(t *testing.T) {
	ctx := context.Background()
	store, rt, in := newOCIFixture(t, false)

	tarPath := buildOCIBundle(t, ociBundleOptions{
		name: "tampered", version: "1.0.0", digest: digestOf("t"), tamper: true,
	})

	res, err := in.Install(ctx, tarPath)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if res.PluginID != "" {
		t.Errorf("PluginID = %q, want empty for a rejected bundle", res.PluginID)
	}
	if rt.Loads != 0 {
		t.Error("the image was loaded despite a failed signature; verification must gate the socket")
	}
	if _, err := store.Queries().GetPluginByName(ctx, "tampered"); err == nil {
		t.Error("a plugins row survives a tampered bundle")
	}
	assertAuditEvent(t, store, auditSignatureInvalid, severityHigh)
}

// The unsigned escape hatch carries over verbatim: refused by default, allowed
// under the operator's global opt-in, and high severity either way so alerting
// catches every load that bypassed verification (ADR-045 §6).
func TestOCIInstall_UnsignedBundle(t *testing.T) {
	t.Run("refused by default", func(t *testing.T) {
		ctx := context.Background()
		store, rt, in := newOCIFixture(t, false)
		tarPath := buildOCIBundle(t, ociBundleOptions{
			name: "unsigned", version: "1.0.0", digest: digestOf("u"), unsigned: true,
		})

		if _, err := in.Install(ctx, tarPath); err != nil {
			t.Fatalf("Install: %v", err)
		}
		if rt.Loads != 0 {
			t.Error("an unsigned bundle reached the socket")
		}
		if _, err := store.Queries().GetPluginByName(ctx, "unsigned"); err == nil {
			t.Error("an unsigned bundle installed with the escape hatch off")
		}
	})

	t.Run("permitted under the escape hatch", func(t *testing.T) {
		ctx := context.Background()
		store, rt, in := newOCIFixture(t, true)
		digest := digestOf("u2")
		tarPath := buildOCIBundle(t, ociBundleOptions{
			name: "unsigned-ok", version: "1.0.0", digest: digest, unsigned: true,
		})
		rt.PendingImages = []container.ImageInfo{{ID: digest}}

		res, err := in.Install(ctx, tarPath)
		if err != nil {
			t.Fatalf("Install: %v", err)
		}
		if res.PluginID == "" {
			t.Fatal("permissive install did not commit")
		}
		assertAuditEvent(t, store, auditPluginInstalled, severityHigh)
	})
}

// TOFU (ADR-045): a re-release signed with a different key does not install,
// it waits for an admin. This is the v1 check reused, which is the only way
// "the trust model is unchanged" can be true.
func TestOCIInstall_RotatedKeyRequiresApproval(t *testing.T) {
	ctx := context.Background()
	store, rt, in := newOCIFixture(t, false)

	digest := digestOf("v1")
	first := buildOCIBundle(t, ociBundleOptions{name: "rotato", version: "1.0.0", digest: digest})
	rt.PendingImages = []container.ImageInfo{{ID: digest}}
	if _, err := in.Install(ctx, first); err != nil {
		t.Fatalf("first Install: %v", err)
	}

	// A second release of the same plugin, signed with a fresh keypair.
	nextDigest := digestOf("v2")
	second := buildOCIBundle(t, ociBundleOptions{name: "rotato", version: "1.1.0", digest: nextDigest})
	rt.PendingImages = []container.ImageInfo{{ID: nextDigest}}

	_, err := in.Install(ctx, second)
	var rejected *InstallRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("Install error = %v, want *InstallRejectedError", err)
	}
	if rejected.Reason != rejectPubkeyMismatch {
		t.Errorf("reason = %q, want %q", rejected.Reason, rejectPubkeyMismatch)
	}

	plugin, err := store.Queries().GetPluginByName(ctx, "rotato")
	if err != nil {
		t.Fatalf("GetPluginByName: %v", err)
	}
	if plugin.PluginVersion != "1.0.0" {
		t.Errorf("version = %q, want the pre-rotation version to be untouched", plugin.PluginVersion)
	}
	assertAuditEvent(t, store, auditPubkeyMismatch, severityHigh)
}

// Manual posture: the operator runs the containers, so the host accepts the
// bundle, never touches the socket, and records that the image is not its
// doing (spec §7).
func TestOCIInstall_ManualPostureSkipsTheLoad(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	base := NewInstaller(&realVerifier{}, store.Queries(), store.DB(), nil, t.TempDir())
	inner := container.NewFake()
	in := NewOCIInstaller(base, container.NewReadOnlyRuntime(inner))

	digest := digestOf("manual")
	tarPath := buildOCIBundle(t, ociBundleOptions{name: "manual-plugin", version: "1.0.0", digest: digest})

	res, err := in.Install(ctx, tarPath)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if res.PluginID == "" {
		t.Fatal("manual-mode install did not commit; the bundle must still be accepted")
	}
	if res.ImageLoaded {
		t.Error("ImageLoaded = true in manual posture; the host must not load the image")
	}
	if inner.Loads != 0 {
		t.Error("manual posture reached the socket")
	}

	// No image row, because the host does not know the image is there.
	if _, err := store.Queries().GetContainerImage(ctx, digest); err == nil {
		t.Error("an image row was written for an image the host never loaded")
	}
	// High severity: an operator who forgets to load it would otherwise only
	// find out when the container fails to start.
	assertAuditEvent(t, store, auditImageLoadSkipped, severityHigh)
}

// A socket failure mid-load is not a half-install.
func TestOCIInstall_LoadFailureLeavesNothing(t *testing.T) {
	ctx := context.Background()
	store, rt, in := newOCIFixture(t, false)

	tarPath := buildOCIBundle(t, ociBundleOptions{name: "flaky", version: "1.0.0", digest: digestOf("f")})
	rt.LoadErr = errors.New("socket closed mid-load")

	if _, err := in.Install(ctx, tarPath); err == nil {
		t.Fatal("Install succeeded despite a failed image load")
	}
	if _, err := store.Queries().GetPluginByName(ctx, "flaky"); err == nil {
		t.Error("a plugins row survives a failed image load")
	}
}

// A v1 bundle arriving through the same directory must be reported as "not a
// v2 bundle", not as a pile of unknown-field parse errors.
func TestOCIInstall_V1BundleIsNotAnOCIBundle(t *testing.T) {
	ctx := context.Background()
	_, _, in := newOCIFixture(t, false)

	tarPath, _ := signedPluginTarball(t, "legacy-plugin", "1.0.0")

	_, err := in.Install(ctx, tarPath)
	if !errors.Is(err, ErrNotOCIBundle) {
		t.Fatalf("Install error = %v, want ErrNotOCIBundle", err)
	}
}

func TestOpenOCIBundle(t *testing.T) {
	tests := []struct {
		name     string
		write    func(t *testing.T, dir string)
		wantErr  error // errors.Is target; nil means "expect some error"
		wantOK   bool
		contains string
	}{
		{
			name: "valid bundle",
			write: func(t *testing.T, dir string) {
				writeBundleFile(t, dir, ociManifestFilename, v2Manifest("ok", "1.0.0", digestOf("ok")))
				writeBundleFile(t, dir, ociImageArchiveName, []byte("archive"))
			},
			wantOK: true,
		},
		{
			name:    "no manifest",
			write:   func(t *testing.T, dir string) { writeBundleFile(t, dir, ociImageArchiveName, []byte("archive")) },
			wantErr: ErrNotOCIBundle,
		},
		{
			name: "v1 manifest",
			write: func(t *testing.T, dir string) {
				writeBundleFile(t, dir, ociManifestFilename, []byte("schema_version: v1\nname: old\n"))
				writeBundleFile(t, dir, ociImageArchiveName, []byte("archive"))
			},
			wantErr: ErrNotOCIBundle,
		},
		{
			name: "no image archive",
			write: func(t *testing.T, dir string) {
				writeBundleFile(t, dir, ociManifestFilename, v2Manifest("noimg", "1.0.0", digestOf("x")))
			},
			wantErr: ErrNotOCIBundle,
		},
		{
			// Claims v2 and does not parse: a BROKEN v2 bundle, which is an
			// operator problem worth reporting, not a v1 bundle to route away.
			name: "v2 manifest that does not validate",
			write: func(t *testing.T, dir string) {
				writeBundleFile(t, dir, ociManifestFilename, []byte("schema_version: \"2\"\nname: broken\n"))
				writeBundleFile(t, dir, ociImageArchiveName, []byte("archive"))
			},
			contains: "parse v2 manifest",
		},
		{
			// A tag is a mutable pointer; consenting to one is not consent.
			name: "tag instead of a digest",
			write: func(t *testing.T, dir string) {
				writeBundleFile(t, dir, ociManifestFilename, []byte(`schema_version: "2"
name: tagged
version: 1.0.0
package:
  registry_type: oci
  identifier: ghcr.io/acme/tagged:latest
  transport:
    type: streamable-http
gleipnir:
  profiles:
    tool_provider: {}
`))
				writeBundleFile(t, dir, ociImageArchiveName, []byte("archive"))
			},
			contains: "digest-pinned",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.write(t, dir)

			bundle, err := OpenOCIBundle(dir)
			switch {
			case tc.wantOK:
				if err != nil {
					t.Fatalf("OpenOCIBundle: %v", err)
				}
				if bundle.ImageDigest() != digestOf("ok") {
					t.Errorf("ImageDigest = %q, want the manifest pin", bundle.ImageDigest())
				}
				if bundle.ImageReference() != "ghcr.io/acme/ok@"+digestOf("ok") {
					t.Errorf("ImageReference = %q", bundle.ImageReference())
				}
			case tc.wantErr != nil:
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error = %v, want %v", err, tc.wantErr)
				}
			default:
				if err == nil {
					t.Fatal("expected an error")
				}
				if !strings.Contains(err.Error(), tc.contains) {
					t.Errorf("error = %q, want it to mention %q", err, tc.contains)
				}
			}
		})
	}
}

// writeBundleFile drops one file into a bundle directory.
func writeBundleFile(t *testing.T, dir, name string, content []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// assertAuditEvent fails unless an audit row of the given type and severity
// exists.
func assertAuditEvent(t *testing.T, store *db.Store, eventType, severity string) {
	t.Helper()
	rows, err := store.Queries().ListPluginAuditEventsByType(context.Background(), db.ListPluginAuditEventsByTypeParams{
		EventType: eventType,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("ListPluginAuditEventsByType(%s): %v", eventType, err)
	}
	if len(rows) == 0 {
		t.Errorf("no %s audit event was written", eventType)
		return
	}
	if rows[0].Severity != severity {
		t.Errorf("%s severity = %q, want %q", eventType, rows[0].Severity, severity)
	}
}
