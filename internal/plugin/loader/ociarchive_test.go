package loader

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/plugin/container"
	"github.com/felag-engineering/gleipnir/plugin-sdk/imagearchive"
	"github.com/felag-engineering/gleipnir/plugin-sdk/manifestv2"
)

type archiveFile struct {
	name     string
	data     []byte
	typeflag byte
	linkname string
}

func tarBytes(t *testing.T, files []archiveFile) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range files {
		hdr := &tar.Header{Name: f.name, Mode: 0o644, Typeflag: f.typeflag, Linkname: f.linkname}
		if hdr.Typeflag == 0 {
			hdr.Typeflag = tar.TypeReg
		}
		if hdr.Typeflag == tar.TypeReg {
			hdr.Size = int64(len(f.data))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write tar header %q: %v", f.name, err)
		}
		if hdr.Typeflag == tar.TypeReg {
			if _, err := tw.Write(f.data); err != nil {
				t.Fatalf("write tar body %q: %v", f.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	return buf.Bytes()
}

func hexOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func dockerLayoutFiles(t *testing.T, config []byte, repoTags []string) []archiveFile {
	t.Helper()
	configName := hexOf(config) + ".json"
	manifest := mustJSON(t, []map[string]any{{"Config": configName, "RepoTags": repoTags, "Layers": []string{}}})
	return []archiveFile{
		{name: "manifest.json", data: manifest},
		{name: configName, data: config},
	}
}

// dockerArchive builds a single-image `docker save` style archive whose config
// digest is sha256(config).
func dockerArchive(t *testing.T, config []byte, repoTags []string) []byte {
	t.Helper()
	return tarBytes(t, dockerLayoutFiles(t, config, repoTags))
}

// ociLayoutFiles builds an OCI-layout archive with one manifest descriptor per
// config, so a test can model a multi-image index.
func ociLayoutFiles(t *testing.T, configs [][]byte, refName string) []archiveFile {
	t.Helper()
	var files []archiveFile
	var descriptors []map[string]any
	for _, config := range configs {
		manifest := mustJSON(t, map[string]any{
			"schemaVersion": 2,
			"config":        map[string]any{"digest": "sha256:" + hexOf(config), "size": len(config)},
			"layers":        []any{},
		})
		descriptor := map[string]any{"digest": "sha256:" + hexOf(manifest), "size": len(manifest)}
		if refName != "" {
			descriptor["annotations"] = map[string]string{"org.opencontainers.image.ref.name": refName}
		}
		descriptors = append(descriptors, descriptor)
		files = append(files,
			archiveFile{name: "blobs/sha256/" + hexOf(manifest), data: manifest},
			archiveFile{name: "blobs/sha256/" + hexOf(config), data: config},
		)
	}
	index := mustJSON(t, map[string]any{"schemaVersion": 2, "manifests": descriptors})
	return append([]archiveFile{{name: "index.json", data: index}}, files...)
}

// bothLayouts is one image described by both an OCI index (bare ref name) and
// a Docker manifest.json (qualified RepoTag), the shape newer `docker save`
// produces. The config blob is shared, as it is in a real archive.
func bothLayouts(t *testing.T, config []byte, ociRef, dockerTag string) []archiveFile {
	t.Helper()
	files := ociLayoutFiles(t, [][]byte{config}, ociRef)
	for _, f := range dockerLayoutFiles(t, config, []string{dockerTag}) {
		if f.name == "manifest.json" {
			files = append(files, f)
		}
	}
	// The Docker layout names its config at <hex>.json.
	return append(files, archiveFile{name: hexOf(config) + ".json", data: config})
}

// with returns files plus extra without aliasing files' backing array.
func with(files []archiveFile, extra ...archiveFile) []archiveFile {
	return append(append([]archiveFile{}, files...), extra...)
}

const archiveTestRepo = "ghcr.io/acme/archived"

func TestInspectImageArchive(t *testing.T) {
	config := []byte("the-pinned-config")
	otherConfig := []byte("some-other-config")
	pin := "sha256:" + hexOf(config)
	pkg := manifestv2.Package{Identifier: archiveTestRepo + "@" + pin}

	dockerFiles := dockerLayoutFiles(t, config, nil)
	dockerTagged := func(tags ...string) []byte { return tarBytes(t, dockerLayoutFiles(t, config, tags)) }
	ociTagged := func(ref string) []byte { return tarBytes(t, ociLayoutFiles(t, [][]byte{config}, ref)) }

	secondDockerConfig := hexOf(otherConfig) + ".json"
	twoDockerImages := mustJSON(t, []map[string]any{
		{"Config": hexOf(config) + ".json", "Layers": []string{}},
		{"Config": secondDockerConfig, "Layers": []string{}},
	})

	swappedConfig := ociLayoutFiles(t, [][]byte{config}, "")
	for i := range swappedConfig {
		if swappedConfig[i].name == "blobs/sha256/"+hexOf(config) {
			swappedConfig[i].data = []byte("swapped bytes")
		}
	}

	tests := []struct {
		name         string
		archive      []byte
		wantErr      error // nil = accepted
		wantMismatch bool
	}{
		{name: "docker layout, untagged", archive: tarBytes(t, dockerFiles)},
		{name: "docker layout, own repository tag", archive: dockerTagged(archiveTestRepo + ":1.0")},
		{name: "oci layout, own repository", archive: ociTagged(archiveTestRepo + ":1.0")},
		{name: "oci layout, untagged", archive: ociTagged("")},
		{
			name:    "bare tag matching the tag of a qualified own-repository tag",
			archive: tarBytes(t, bothLayouts(t, config, "1.0", archiveTestRepo+":1.0")),
		},
		{
			name:    "bare tag unrelated to the qualified own-repository tag",
			archive: tarBytes(t, bothLayouts(t, config, "postgres", archiveTestRepo+":1.0")),
			wantErr: ErrImageArchiveInvalid,
		},
		{name: "foreign docker tag", archive: dockerTagged("postgres:16"), wantErr: ErrImageArchiveInvalid},
		{name: "foreign tag beside own tag", archive: dockerTagged(archiveTestRepo+":1.0", "postgres:latest"), wantErr: ErrImageArchiveInvalid},
		{name: "bare name that resolves to a library image", archive: dockerTagged("postgres"), wantErr: ErrImageArchiveInvalid},
		{name: "bare oci ref name with nothing confirming it", archive: ociTagged("latest"), wantErr: ErrImageArchiveInvalid},
		{name: "foreign oci ref name", archive: ociTagged("docker.io/library/postgres:16"), wantErr: ErrImageArchiveInvalid},
		{name: "repository prefix is not the repository", archive: dockerTagged(archiveTestRepo + "-evil:1.0"), wantErr: ErrImageArchiveInvalid},
		{
			name:    "two images in one oci index",
			archive: tarBytes(t, ociLayoutFiles(t, [][]byte{config, otherConfig}, "")),
			wantErr: ErrImageArchiveInvalid,
		},
		{
			name: "two images in docker manifest.json",
			archive: tarBytes(t, []archiveFile{
				{name: "manifest.json", data: twoDockerImages},
				{name: hexOf(config) + ".json", data: config},
				{name: secondDockerConfig, data: otherConfig},
			}),
			wantErr: ErrImageArchiveInvalid,
		},
		{
			name:    "index.json and manifest.json name different images",
			archive: tarBytes(t, with(ociLayoutFiles(t, [][]byte{config}, ""), dockerLayoutFiles(t, otherConfig, nil)...)),
			wantErr: ErrImageArchiveInvalid,
		},
		{name: "config blob does not hash to its digest", archive: tarBytes(t, swappedConfig), wantErr: ErrImageArchiveInvalid},
		{name: "duplicate tar entry", archive: tarBytes(t, with(dockerFiles, dockerFiles[1])), wantErr: ErrImageArchiveInvalid},
		{
			name:    "path traversal entry",
			archive: tarBytes(t, with(dockerFiles, archiveFile{name: "../escape", data: []byte("x")})),
			wantErr: ErrImageArchiveInvalid,
		},
		{
			name:    "absolute path entry",
			archive: tarBytes(t, with(dockerFiles, archiveFile{name: "/etc/passwd", data: []byte("x")})),
			wantErr: ErrImageArchiveInvalid,
		},
		{
			name:    "symlink entry",
			archive: tarBytes(t, with(dockerFiles, archiveFile{name: "link", typeflag: tar.TypeSymlink, linkname: "/etc/passwd"})),
			wantErr: ErrImageArchiveInvalid,
		},
		{
			name:    "hardlink entry",
			archive: tarBytes(t, with(dockerFiles, archiveFile{name: "link", typeflag: tar.TypeLink, linkname: "manifest.json"})),
			wantErr: ErrImageArchiveInvalid,
		},
		{
			name:    "oversized manifest entry",
			archive: tarBytes(t, []archiveFile{{name: "manifest.json", data: bytes.Repeat([]byte(" "), 5<<20)}}),
			wantErr: ErrImageArchiveInvalid,
		},
		{name: "not an image archive", archive: []byte("fake OCI image archive"), wantErr: ErrImageArchiveInvalid},
		{name: "config digest differs from the manifest pin", archive: dockerArchive(t, otherConfig, nil), wantMismatch: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "image.tar")
			if err := os.WriteFile(path, tt.archive, 0o644); err != nil {
				t.Fatalf("write archive: %v", err)
			}

			_, err := inspectImageArchive(path, pkg)
			switch {
			case tt.wantMismatch:
				var mismatch *imageDigestMismatchError
				if !errors.As(err, &mismatch) {
					t.Fatalf("error = %v, want *imageDigestMismatchError", err)
				}
				if mismatch.Expected != pin {
					t.Errorf("Expected = %q, want %q", mismatch.Expected, pin)
				}
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error = %v, want %v", err, tt.wantErr)
				}
			case err != nil:
				t.Fatalf("archive rejected: %v", err)
			}
		})
	}
}

// A bad archive must be refused before the daemon is asked to load anything,
// and must leave nothing recorded.
func TestOCIInstall_BadArchiveIsRefusedBeforeLoad(t *testing.T) {
	config := []byte("install-config")
	otherConfig := []byte("install-other-config")
	pin := "sha256:" + hexOf(config)

	tests := []struct {
		name       string
		archive    []byte
		wantReason string
	}{
		{
			name:       "second image",
			archive:    tarBytes(t, ociLayoutFiles(t, [][]byte{config, otherConfig}, "")),
			wantReason: rejectImageArchiveInvalid,
		},
		{
			name:       "foreign tag",
			archive:    dockerArchive(t, config, []string{"postgres:latest"}),
			wantReason: rejectImageArchiveInvalid,
		},
		{
			name:       "index and manifest disagree",
			archive:    tarBytes(t, with(ociLayoutFiles(t, [][]byte{config}, ""), dockerLayoutFiles(t, otherConfig, nil)...)),
			wantReason: rejectImageArchiveInvalid,
		},
		{
			name:       "config digest is not the pin",
			archive:    dockerArchive(t, otherConfig, nil),
			wantReason: rejectImageDigestMismatch,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			store, rt, in := newOCIFixture(t, false)

			tarPath := buildOCIBundle(t, ociBundleOptions{
				name: "hostile", version: "1.0.0", digest: pin, archive: tt.archive,
			})

			_, err := in.Install(ctx, tarPath)
			var rejected *InstallRejectedError
			if !errors.As(err, &rejected) {
				t.Fatalf("Install error = %v, want *InstallRejectedError", err)
			}
			if rejected.Reason != tt.wantReason {
				t.Errorf("reason = %q, want %q", rejected.Reason, tt.wantReason)
			}
			if rt.Loads != 0 || rt.LoadedBytes != 0 {
				t.Errorf("ImageLoad was reached (loads=%d, bytes=%d); inspection must gate the daemon", rt.Loads, rt.LoadedBytes)
			}
			if _, err := store.Queries().GetPluginByName(ctx, "hostile"); err == nil {
				t.Error("a plugins row survives a refused archive")
			}
			if tt.wantReason == rejectImageArchiveInvalid {
				assertAuditEvent(t, store, auditImageArchiveRefused, severityHigh)
			}
		})
	}
}

// Inspection does not depend on a runtime being able to load: manual posture
// still refuses a malformed archive.
func TestOCIInstall_BadArchiveIsRefusedInManualPosture(t *testing.T) {
	store := openTestStore(t)
	base := NewInstaller(&realVerifier{}, store.Queries(), store.DB(), nil, t.TempDir())
	in := NewOCIInstaller(base, container.NewReadOnlyRuntime(container.NewFake()))

	config := []byte("manual-config")
	tarPath := buildOCIBundle(t, ociBundleOptions{
		name: "manual-hostile", version: "1.0.0", digest: "sha256:" + hexOf(config),
		archive: dockerArchive(t, config, []string{"postgres:latest"}),
	})

	_, err := in.Install(context.Background(), tarPath)
	var rejected *InstallRejectedError
	if !errors.As(err, &rejected) || rejected.Reason != rejectImageArchiveInvalid {
		t.Fatalf("Install error = %v, want an image_archive_invalid rejection", err)
	}
}

func TestImageMatchesArchive(t *testing.T) {
	const reference = "ghcr.io/acme/plugin@sha256:cfg"
	verified := imagearchive.Info{ConfigDigest: "sha256:cfg", ManifestDigest: "sha256:manifest"}
	dockerLayout := imagearchive.Info{ConfigDigest: "sha256:cfg"}

	tests := []struct {
		name      string
		inspected container.ImageInfo
		verified  imagearchive.Info
		want      bool
	}{
		{name: "classic store: ID is the config digest", inspected: container.ImageInfo{ID: "sha256:cfg"}, verified: verified, want: true},
		{name: "containerd store: ID is the manifest digest", inspected: container.ImageInfo{ID: "sha256:manifest"}, verified: verified, want: true},
		{name: "repo digest of the pinned reference", inspected: container.ImageInfo{ID: "sha256:other", RepoDigests: []string{reference}}, verified: verified, want: true},
		{name: "unrelated ID", inspected: container.ImageInfo{ID: "sha256:evil"}, verified: verified, want: false},
		{name: "unrelated repo digest", inspected: container.ImageInfo{ID: "sha256:evil", RepoDigests: []string{"ghcr.io/acme/plugin@sha256:evil"}}, verified: verified, want: false},
		{name: "docker-layout archive has no manifest digest to accept", inspected: container.ImageInfo{ID: "sha256:manifest"}, verified: dockerLayout, want: false},
		{name: "empty ID never matches an empty manifest digest", inspected: container.ImageInfo{}, verified: dockerLayout, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := imageMatchesArchive(tt.inspected, tt.verified, reference); got != tt.want {
				t.Errorf("imageMatchesArchive = %v, want %v", got, tt.want)
			}
		})
	}
}

// Under the containerd image store the daemon indexes the loaded image by its
// manifest digest and reports that as the ID, so inspecting the config digest
// finds nothing. The install must still succeed, because the manifest digest
// is one the verified archive hashes to.
func TestOCIInstall_ContainerdStoreManifestDigestID(t *testing.T) {
	ctx := context.Background()

	config := []byte("containerd-config")
	archive := tarBytes(t, ociLayoutFiles(t, [][]byte{config}, "ghcr.io/acme/containerd-plugin:1.0.0"))
	archivePath := filepath.Join(t.TempDir(), "image.tar")
	if err := os.WriteFile(archivePath, archive, 0o644); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	pin := "sha256:" + hexOf(config)
	verified, err := inspectImageArchive(archivePath, manifestv2.Package{Identifier: "ghcr.io/acme/containerd-plugin@" + pin})
	if err != nil {
		t.Fatalf("inspect fixture archive: %v", err)
	}
	if verified.ManifestDigest == "" {
		t.Fatal("fixture archive has no manifest digest; the test would prove nothing")
	}

	tests := []struct {
		name     string
		loadedID string
		wantOK   bool
	}{
		{name: "ID is the verified manifest digest", loadedID: verified.ManifestDigest, wantOK: true},
		{name: "ID is a digest the archive never contained", loadedID: digestOf("not-in-archive"), wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, rt, in := newOCIFixture(t, false)
			tarPath := buildOCIBundle(t, ociBundleOptions{
				name: "containerd-plugin", version: "1.0.0", digest: pin, archive: archive,
			})
			rt.PendingImages = []container.ImageInfo{{ID: tt.loadedID}}

			res, err := in.Install(ctx, tarPath)
			if tt.wantOK {
				if err != nil {
					t.Fatalf("Install: %v", err)
				}
				if !res.ImageLoaded {
					t.Error("ImageLoaded = false for a containerd-store ID matching the manifest digest")
				}
				return
			}
			if res.ImageLoaded {
				t.Error("ImageLoaded = true for an image the archive does not prove")
			}
		})
	}
}
