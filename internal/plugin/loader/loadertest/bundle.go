// Package loadertest builds signed v2 plugin bundles for tests outside the
// loader package, which cannot reach the loader's own unexported fixtures.
// Tests only: it imports "testing" and must not be linked into a binary.
package loadertest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/felag-engineering/gleipnir/plugin-sdk/signing"
)

// Options describes a bundle to build.
type Options struct {
	Name    string
	Version string

	// Seed is the image config content. The archive's image digest is
	// DigestOf(Seed), which is also what the default manifest pins, so a
	// bundle built from one Options value always agrees with itself.
	Seed string

	// Manifest renders the manifest for a digest. Empty yields Manifest's
	// default.
	Manifest func(digest string) []byte

	// Unsigned omits signing.pub and the .minisig.
	Unsigned bool
}

// Bundle is a built tarball.
type Bundle struct {
	// Path is the .tar.gz.
	Path string
	// Digest is the image digest the archive yields and the manifest pins.
	Digest string
}

// DigestOf returns the image digest of an archive whose config is seed.
func DigestOf(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Manifest renders a minimal valid v2 manifest pinning digest.
func Manifest(name, version, digest string) []byte {
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

// Build writes a bundle tarball into a fresh temp dir. Each signed bundle gets
// a fresh keypair, so building the same plugin twice models a key rotation.
func Build(t testing.TB, o Options) Bundle {
	t.Helper()

	digest := DigestOf(o.Seed)
	manifest := Manifest(o.Name, o.Version, digest)
	if o.Manifest != nil {
		manifest = o.Manifest(digest)
	}
	archive := dockerArchive(t, []byte(o.Seed))

	entries := []entry{
		{name: "manifest.yaml", content: manifest},
		{name: "image.tar", content: archive},
	}
	if !o.Unsigned {
		pk, sk, err := signing.GenerateKeypair(rand.Reader)
		if err != nil {
			t.Fatalf("generate keypair: %v", err)
		}
		sig, err := signing.Sign(sk.SecretKey, sk.KeyID, signing.PluginPayload(archive, manifest), "trusted comment")
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		entries = append(entries,
			entry{name: "signing.pub", content: signing.MarshalPublicKey(pk, "test key")},
			entry{name: o.Name + ".minisig", content: signing.MarshalSignature(sig, "test sig")},
		)
	}

	path := filepath.Join(t.TempDir(), o.Name+"-"+o.Version+".tar.gz")
	writeTarGz(t, path, entries)
	return Bundle{Path: path, Digest: digest}
}

type entry struct {
	name    string
	content []byte
}

// dockerArchive is a single-image `docker save` layout whose config digest is
// sha256(config).
func dockerArchive(t testing.TB, config []byte) []byte {
	t.Helper()
	sum := sha256.Sum256(config)
	configName := hex.EncodeToString(sum[:]) + ".json"
	manifest, err := json.Marshal([]map[string]any{{"Config": configName, "RepoTags": nil, "Layers": []string{}}})
	if err != nil {
		t.Fatalf("marshal docker manifest: %v", err)
	}
	var buf bytes.Buffer
	writeTar(t, &buf, []entry{{name: "manifest.json", content: manifest}, {name: configName, content: config}})
	return buf.Bytes()
}

func writeTar(t testing.TB, buf *bytes.Buffer, entries []entry) {
	t.Helper()
	tw := tar.NewWriter(buf)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(e.content))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write tar header %q: %v", e.name, err)
		}
		if _, err := tw.Write(e.content); err != nil {
			t.Fatalf("write tar body %q: %v", e.name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
}

func writeTarGz(t testing.TB, path string, entries []entry) {
	t.Helper()
	var raw bytes.Buffer
	writeTar(t, &raw, entries)

	var zipped bytes.Buffer
	gw := gzip.NewWriter(&zipped)
	if _, err := gw.Write(raw.Bytes()); err != nil {
		t.Fatalf("gzip: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	if err := os.WriteFile(path, zipped.Bytes(), 0o644); err != nil {
		t.Fatalf("write tarball: %v", err)
	}
}
