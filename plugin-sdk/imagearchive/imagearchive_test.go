package imagearchive

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

// goodConfig is a minimal, valid image config blob used by most tests. Its
// content does not matter beyond being stable and hashable.
var goodConfig = []byte(`{"config":{},"rootfs":{"type":"layers","diff_ids":[]}}`)

type tarFile struct {
	name string
	data []byte

	// typeflag overrides the tar entry type; the zero value means
	// tar.TypeReg. Set to tar.TypeSymlink (with linkname) to build a
	// malicious non-regular entry.
	typeflag byte
	linkname string
}

// buildTarArchive writes files into a plain (uncompressed) tar archive.
func buildTarArchive(t *testing.T, files []tarFile) []byte {
	t.Helper()
	buf := &bytes.Buffer{}
	tw := tar.NewWriter(buf)
	for _, f := range files {
		typeflag := f.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		hdr := &tar.Header{
			Name:     f.name,
			Mode:     0o644,
			Size:     int64(len(f.data)),
			Typeflag: typeflag,
			Linkname: f.linkname,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header %s: %v", f.name, err)
		}
		if _, err := tw.Write(f.data); err != nil {
			t.Fatalf("write data %s: %v", f.name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	return buf.Bytes()
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// dockerLayoutFiles returns the manifest.json + config blob entries for a
// minimal single-image Docker-legacy archive, plus the config's digest.
func dockerLayoutFiles(configData []byte, repoTags []string) ([]tarFile, string) {
	configHex := sha256Hex(configData)
	configName := configHex + ".json"
	entries := []map[string]any{{
		"Config":   configName,
		"RepoTags": repoTags,
		"Layers":   []string{},
	}}
	manifestJSON, err := json.Marshal(entries)
	if err != nil {
		panic(err)
	}
	return []tarFile{
		{name: "manifest.json", data: manifestJSON},
		{name: configName, data: configData},
	}, "sha256:" + configHex
}

// ociLayoutFiles returns the index.json + manifest blob + config blob entries
// for a minimal single-manifest OCI-layout archive, plus the config's digest.
// tamperManifestBlob/tamperConfigBlob, when non-nil, replace a blob's on-disk
// bytes with a different payload while leaving index.json's declared digest
// (and the manifest blob's own config-digest field) pointing at the ORIGINAL
// hash — simulating a producer that names a blob correctly without its
// contents actually matching. containerdImageName, when non-empty, sets the
// io.containerd.image.name annotation alongside org.opencontainers.image.ref.name.
func ociLayoutFiles(configData []byte, refName, containerdImageName string, tamperManifestBlob, tamperConfigBlob []byte) ([]tarFile, string) {
	configHex := sha256Hex(configData)
	configDigest := "sha256:" + configHex

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
		panic(err)
	}
	manifestHex := sha256Hex(manifestData)

	descriptor := map[string]any{
		"mediaType": "application/vnd.oci.image.manifest.v1+json",
		"digest":    "sha256:" + manifestHex,
		"size":      len(manifestData),
	}
	annotations := map[string]string{}
	if refName != "" {
		annotations["org.opencontainers.image.ref.name"] = refName
	}
	if containerdImageName != "" {
		annotations["io.containerd.image.name"] = containerdImageName
	}
	if len(annotations) > 0 {
		descriptor["annotations"] = annotations
	}
	index := map[string]any{
		"schemaVersion": 2,
		"manifests":     []map[string]any{descriptor},
	}
	indexData, err := json.Marshal(index)
	if err != nil {
		panic(err)
	}

	manifestBlobData := manifestData
	if tamperManifestBlob != nil {
		manifestBlobData = tamperManifestBlob
	}
	configBlobData := configData
	if tamperConfigBlob != nil {
		configBlobData = tamperConfigBlob
	}

	return []tarFile{
		{name: "index.json", data: indexData},
		{name: "blobs/sha256/" + manifestHex, data: manifestBlobData},
		{name: "blobs/sha256/" + configHex, data: configBlobData},
	}, configDigest
}

func TestInspect_DockerLayout(t *testing.T) {
	files, wantDigest := dockerLayoutFiles(goodConfig, []string{"example/test:latest"})
	info, err := InspectBytes(buildTarArchive(t, files))
	if err != nil {
		t.Fatalf("InspectBytes: %v", err)
	}
	if info.ConfigDigest != wantDigest {
		t.Errorf("ConfigDigest = %q, want %q", info.ConfigDigest, wantDigest)
	}
	if len(info.Tags) != 1 || info.Tags[0] != "example/test:latest" {
		t.Errorf("Tags = %v, want [example/test:latest]", info.Tags)
	}
}

func TestInspect_OCILayout(t *testing.T) {
	files, wantDigest := ociLayoutFiles(goodConfig, "ghcr.io/acme/plugin:1.0.0", "", nil, nil)
	info, err := InspectBytes(buildTarArchive(t, files))
	if err != nil {
		t.Fatalf("InspectBytes: %v", err)
	}
	if info.ConfigDigest != wantDigest {
		t.Errorf("ConfigDigest = %q, want %q", info.ConfigDigest, wantDigest)
	}
	if len(info.Tags) != 1 || info.Tags[0] != "ghcr.io/acme/plugin:1.0.0" {
		t.Errorf("Tags = %v, want [ghcr.io/acme/plugin:1.0.0]", info.Tags)
	}
	if !strings.HasPrefix(info.ManifestDigest, "sha256:") || info.ManifestDigest == info.ConfigDigest {
		t.Errorf("ManifestDigest = %q, want a sha256 digest distinct from the config digest", info.ManifestDigest)
	}
}

// TestInspect_OCILayout_ContainerdImageNameAnnotation proves the
// io.containerd.image.name annotation (Docker 25+/containerd) is reported
// alongside org.opencontainers.image.ref.name, since the latter is frequently
// just a bare tag with no repository at all.
func TestInspect_OCILayout_ContainerdImageNameAnnotation(t *testing.T) {
	files, wantDigest := ociLayoutFiles(goodConfig, "latest", "ghcr.io/acme/plugin:1.0.0", nil, nil)
	info, err := InspectBytes(buildTarArchive(t, files))
	if err != nil {
		t.Fatalf("InspectBytes: %v", err)
	}
	if info.ConfigDigest != wantDigest {
		t.Errorf("ConfigDigest = %q, want %q", info.ConfigDigest, wantDigest)
	}
	wantTags := map[string]bool{"latest": true, "ghcr.io/acme/plugin:1.0.0": true}
	if len(info.Tags) != len(wantTags) {
		t.Fatalf("Tags = %v, want both annotations reported", info.Tags)
	}
	for _, tag := range info.Tags {
		if !wantTags[tag] {
			t.Errorf("unexpected tag %q", tag)
		}
	}
}

func TestInspect_BothLayoutsPresentAndAgreeing(t *testing.T) {
	oci, wantDigest := ociLayoutFiles(goodConfig, "ghcr.io/acme/plugin:1.0.0", "", nil, nil)
	docker, _ := dockerLayoutFiles(goodConfig, []string{"example/test:latest"})

	info, err := InspectBytes(buildTarArchive(t, append(oci, docker...)))
	if err != nil {
		t.Fatalf("InspectBytes: %v", err)
	}
	if info.ConfigDigest != wantDigest {
		t.Errorf("ConfigDigest = %q, want %q", info.ConfigDigest, wantDigest)
	}
	wantTags := map[string]bool{"ghcr.io/acme/plugin:1.0.0": true, "example/test:latest": true}
	if len(info.Tags) != len(wantTags) {
		t.Fatalf("Tags = %v, want both layouts' tags merged", info.Tags)
	}
	for _, tag := range info.Tags {
		if !wantTags[tag] {
			t.Errorf("unexpected tag %q", tag)
		}
	}
}

func TestInspect_Rejections(t *testing.T) {
	tests := []struct {
		name    string
		archive func(t *testing.T) []byte
		wantErr string
	}{
		{
			name: "neither index.json nor manifest.json",
			archive: func(t *testing.T) []byte {
				return buildTarArchive(t, []tarFile{{name: "README.txt", data: []byte("hi")}})
			},
			wantErr: "neither index.json nor manifest.json",
		},
		{
			// (a) more than one image in a Docker-legacy archive.
			name: "docker manifest.json describes more than one image",
			archive: func(t *testing.T) []byte {
				files, _ := dockerLayoutFiles(goodConfig, nil)
				manifestJSON, err := json.Marshal([]map[string]any{
					{"Config": "a.json", "Layers": []string{}},
					{"Config": "b.json", "Layers": []string{}},
				})
				if err != nil {
					t.Fatal(err)
				}
				files[0] = tarFile{name: "manifest.json", data: manifestJSON}
				return buildTarArchive(t, files)
			},
			wantErr: "declares 2 images",
		},
		{
			// (a) more than one manifest in an OCI-layout archive.
			name: "oci index.json describes more than one manifest",
			archive: func(t *testing.T) []byte {
				index := map[string]any{
					"schemaVersion": 2,
					"manifests": []map[string]any{
						{"mediaType": "x", "digest": "sha256:" + strings.Repeat("a", 64), "size": 1},
						{"mediaType": "x", "digest": "sha256:" + strings.Repeat("b", 64), "size": 1},
					},
				}
				indexData, err := json.Marshal(index)
				if err != nil {
					t.Fatal(err)
				}
				return buildTarArchive(t, []tarFile{{name: "index.json", data: indexData}})
			},
			wantErr: "declares 2 manifests",
		},
		{
			// (b) both layouts present, but naming different images.
			name: "both layouts present with disagreeing config digests",
			archive: func(t *testing.T) []byte {
				oci, _ := ociLayoutFiles(goodConfig, "", "", nil, nil)
				docker, _ := dockerLayoutFiles([]byte(`{"different":true}`), nil)
				return buildTarArchive(t, append(oci, docker...))
			},
			wantErr: "different config digests",
		},
		{
			// (c) index.json's descriptor digest doesn't match the manifest
			// blob's actual bytes.
			name: "oci manifest blob tampered",
			archive: func(t *testing.T) []byte {
				files, _ := ociLayoutFiles(goodConfig, "", "", []byte(`{"tampered":true}`), nil)
				return buildTarArchive(t, files)
			},
			wantErr: "does not match declared digest",
		},
		{
			// (c) the manifest blob's config digest doesn't match the config
			// blob's actual bytes.
			name: "oci config blob tampered",
			archive: func(t *testing.T) []byte {
				files, _ := ociLayoutFiles(goodConfig, "", "", nil, []byte(`{"tampered":true}`))
				return buildTarArchive(t, files)
			},
			wantErr: "does not match declared digest",
		},
		{
			name: "oci manifest blob missing",
			archive: func(t *testing.T) []byte {
				files, _ := ociLayoutFiles(goodConfig, "", "", nil, nil)
				return buildTarArchive(t, files[:1]) // index.json only
			},
			wantErr: "missing the manifest blob",
		},
		{
			// (d) duplicate tar entry names.
			name: "duplicate tar entry",
			archive: func(t *testing.T) []byte {
				files, _ := dockerLayoutFiles(goodConfig, nil)
				files = append(files, tarFile{name: "manifest.json", data: []byte("[]")})
				return buildTarArchive(t, files)
			},
			wantErr: "duplicate entry",
		},
		{
			name: "malformed digest has no colon",
			archive: func(t *testing.T) []byte {
				index := map[string]any{
					"schemaVersion": 2,
					"manifests": []map[string]any{
						{"mediaType": "x", "digest": "notadigest", "size": 1},
					},
				}
				indexData, err := json.Marshal(index)
				if err != nil {
					t.Fatal(err)
				}
				return buildTarArchive(t, []tarFile{{name: "index.json", data: indexData}})
			},
			wantErr: "not of the form algo:hex",
		},
		{
			name: "unsupported digest algorithm",
			archive: func(t *testing.T) []byte {
				manifestHex := strings.Repeat("a", 128)
				index := map[string]any{
					"schemaVersion": 2,
					"manifests": []map[string]any{
						{"mediaType": "x", "digest": "sha512:" + manifestHex, "size": 1},
					},
				}
				indexData, err := json.Marshal(index)
				if err != nil {
					t.Fatal(err)
				}
				return buildTarArchive(t, []tarFile{
					{name: "index.json", data: indexData},
					{name: "blobs/sha512/" + manifestHex, data: []byte("anything")},
				})
			},
			wantErr: "unsupported digest algorithm",
		},
		{
			// (e) an entry declaring more than the 4 MiB per-entry bound.
			name: "manifest.json over the per-entry size limit",
			archive: func(t *testing.T) []byte {
				oversized := bytes.Repeat([]byte("x"), maxEntryBytes+1)
				return buildTarArchive(t, []tarFile{{name: "manifest.json", data: oversized}})
			},
			wantErr: "over the",
		},
		{
			// Entry-name canonicalisation (a): a non-canonical spelling of
			// "manifest.json" must not be treated as a harmless extra entry —
			// Docker itself would clean the path and let the last write win,
			// which is exactly the ambiguity this package must refuse rather
			// than resolve one particular way.
			name: "manifest.json followed by a '..'-escaping alias of itself",
			archive: func(t *testing.T) []byte {
				files, _ := dockerLayoutFiles(goodConfig, nil)
				files = append(files, tarFile{name: "sub/../manifest.json", data: []byte("[]")})
				return buildTarArchive(t, files)
			},
			wantErr: "'..' segment",
		},
		{
			// Entry-name canonicalisation (b): the exact scenario a
			// mismatched verifier/runtime view of "the same" blob enables —
			// a literal "a/../cfg.json" tar entry (one payload) sits
			// alongside a "cfg.json" entry (a different payload); Docker
			// cleans paths internally and "last write wins" on extraction, so
			// which payload a real runtime ends up loading as the config
			// depends on extraction order, not on this package's view of it.
			// The whole archive must be refused rather than let either
			// payload be silently chosen.
			name: "Config path escapes with a '..' segment (literal entry present)",
			archive: func(t *testing.T) []byte {
				configData := goodConfig
				entries := []map[string]any{{
					"Config":   "a/../cfg.json",
					"RepoTags": []string{},
					"Layers":   []string{},
				}}
				manifestJSON, err := json.Marshal(entries)
				if err != nil {
					t.Fatal(err)
				}
				return buildTarArchive(t, []tarFile{
					{name: "manifest.json", data: manifestJSON},
					{name: "a/../cfg.json", data: configData}, // the literal (also invalid) name
					{name: "cfg.json", data: configData},      // the real target
				})
			},
			wantErr: "'..' segment",
		},
		{
			// Entry-name canonicalisation (c): an absolute path anywhere in
			// the archive fails the whole inspection, even when it plays no
			// part in either JSON document — canonicalisation applies to
			// every entry scanEntries walks, not only to the ones it wants.
			name: "an unrelated absolute-path entry poisons the whole archive",
			archive: func(t *testing.T) []byte {
				files, _ := dockerLayoutFiles(goodConfig, nil)
				files = append(files,
					tarFile{name: "x.json", data: []byte("x")},
					tarFile{name: "/x.json", data: []byte("x")},
				)
				return buildTarArchive(t, files)
			},
			wantErr: "absolute path",
		},
		{
			// (2) a symlink's body is empty, which hashes to a perfectly
			// ordinary-looking (and pinnable) sha256 digest rather than to
			// whatever the link's target actually contains.
			name: "symlink entry rejected instead of hashed as empty",
			archive: func(t *testing.T) []byte {
				entries := []map[string]any{{
					"Config":   "x.json",
					"RepoTags": []string{},
					"Layers":   []string{},
				}}
				manifestJSON, err := json.Marshal(entries)
				if err != nil {
					t.Fatal(err)
				}
				return buildTarArchive(t, []tarFile{
					{name: "manifest.json", data: manifestJSON},
					{name: "x.json", typeflag: tar.TypeSymlink, linkname: "y.json"},
				})
			},
			wantErr: "not a regular file or directory",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := InspectBytes(tt.archive(t))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

// TestInspect_ConfigPathValidatedEvenWithoutAMatchingLiteralEntry isolates
// dockerLayoutInfo's own Config-path check from the generic entry-name
// canonicalisation in TestInspect_Rejections: the archive here contains no
// tar entry literally named "a/../cfg.json" at all — only a plainly-named
// "cfg.json" — so the only thing that can catch the malicious Config value is
// validating the JSON-declared path itself, before it is ever used to look
// anything up.
func TestInspect_ConfigPathValidatedEvenWithoutAMatchingLiteralEntry(t *testing.T) {
	entries := []map[string]any{{
		"Config":   "a/../cfg.json",
		"RepoTags": []string{},
		"Layers":   []string{},
	}}
	manifestJSON, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	archive := buildTarArchive(t, []tarFile{
		{name: "manifest.json", data: manifestJSON},
		{name: "cfg.json", data: goodConfig},
	})

	_, err = InspectBytes(archive)
	if err == nil {
		t.Fatal("expected an error for a Config path containing a '..' segment, got nil")
	}
	if !strings.Contains(err.Error(), "Config path") || !strings.Contains(err.Error(), "'..' segment") {
		t.Errorf("error = %q, want it to name the Config path and a '..' segment", err.Error())
	}
}

// TestInspect_LayersPathValidated proves manifest.json's Layers paths are
// checked the same way as Config, even though this package never reads a
// layer's bytes.
func TestInspect_LayersPathValidated(t *testing.T) {
	entries := []map[string]any{{
		"Config":   "cfg.json",
		"RepoTags": []string{},
		"Layers":   []string{"/etc/passwd"},
	}}
	manifestJSON, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	archive := buildTarArchive(t, []tarFile{
		{name: "manifest.json", data: manifestJSON},
		{name: "cfg.json", data: goodConfig},
	})

	_, err = InspectBytes(archive)
	if err == nil {
		t.Fatal("expected an error for an absolute Layers path, got nil")
	}
	if !strings.Contains(err.Error(), "Layers path") {
		t.Errorf("error = %q, want it to name the Layers path", err.Error())
	}
}

// TestInspect_DecompressedSizeBound exercises (e)'s gzip-bomb defense: a
// small, highly-compressible gzip stream that would decompress to more than
// the bound. maxDecompressedBytes is lowered for the duration of this test so
// it does not need to actually materialize hundreds of megabytes to prove the
// bound is enforced.
func TestInspect_DecompressedSizeBound(t *testing.T) {
	orig := maxDecompressedBytes
	maxDecompressedBytes = 1 << 20 // 1 MiB
	t.Cleanup(func() { maxDecompressedBytes = orig })

	// A tar entry whose declared size is within the per-entry bound (4 MiB)
	// but decompresses past the (lowered, 1 MiB) total bound on its own —
	// all-zero content compresses enormously, so the compressed archive stays
	// tiny.
	payload := bytes.Repeat([]byte{0}, maxEntryBytes)
	tarBytes := buildTarArchive(t, []tarFile{{name: "manifest.json", data: payload}})

	var gzBuf bytes.Buffer
	gw := gzip.NewWriter(&gzBuf)
	if _, err := gw.Write(tarBytes); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	_, err := InspectBytes(gzBuf.Bytes())
	if err == nil {
		t.Fatal("expected an error from exceeding the decompressed-size bound, got nil")
	}
	if !strings.Contains(err.Error(), "decompressed bytes") {
		t.Errorf("error = %q, want it to mention the decompressed-size bound", err.Error())
	}
}
