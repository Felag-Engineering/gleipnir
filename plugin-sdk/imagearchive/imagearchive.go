package imagearchive

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"
)

// maxEntryBytes bounds how much of any single JSON/config tar entry this
// package will read into memory: index.json, manifest.json, an OCI manifest
// blob, and an image config blob. None of these legitimately approach this
// size — an image's actual layers are never read by this package at all — so
// a producer whose entry declares more is either broken or hostile.
const maxEntryBytes = 4 << 20 // 4 MiB

// MaxBundleBytes is the most uncompressed data the host will extract from one
// plugin bundle, and so the most an image archive inside it can be. It sits
// here because the packaging CLI and the host both import this package, so
// the CLI's too-large warning cannot drift from the limit the host enforces.
//
// Sized for real images: a `docker save` archive of an ordinary service image
// is hundreds of MiB, since layers are stored uncompressed. Every read stays
// bounded (see maxEntryBytes and maxDecompressedBytes); this raises the ceiling,
// not the amount ever held in memory.
const MaxBundleBytes int64 = 1 << 30 // 1 GiB

// maxDecompressedBytes bounds total bytes read while scanning an archive,
// gzip-decompressed if the archive is compressed. This is independent of any
// one entry's declared size: it defends against a gzip bomb — a tiny
// compressed file that expands to gigabytes as it is decompressed — rather
// than against an oversized entry.
//
// A var (not a const) solely so tests can lower it: materializing a real
// >1 GiB decompressed payload to exercise this path would cost real time
// for no additional coverage. Production code must never reassign it.
var maxDecompressedBytes = MaxBundleBytes

// Info is what Inspect learns about an image archive.
type Info struct {
	// ConfigDigest is "sha256:<hex>" of the image config blob — the classic
	// image ID (what `docker inspect --format '{{.Id}}'` reports), and the
	// value manifestv2's Package.Digest() pins.
	ConfigDigest string

	// ManifestDigest is "sha256:<hex>" of the image manifest blob, set only
	// for an OCI-layout archive, where the blob was located and re-hashed.
	// Docker's containerd image store reports this, not ConfigDigest, as the
	// loaded image's ID. Empty for a Docker-legacy archive, which has no
	// manifest blob to hash.
	ManifestDigest string

	// Tags are every repository tag embedded in the archive itself: a
	// Docker-legacy manifest.json entry's RepoTags, and/or an OCI-layout
	// manifest descriptor's org.opencontainers.image.ref.name annotation.
	// Exposed so a caller can police them (#1032 does this on the host side):
	// an archive tagged with anything other than the manifest's own
	// repository is a mislabeled-or-swapped image, worth surfacing even
	// though inspection itself does not judge tags.
	Tags []string
}

// Inspect reads an OCI or Docker-legacy image archive out of r (size bytes
// long) and returns its image config digest and embedded tags. r need only
// support io.ReaderAt — a *bytes.Reader (see InspectBytes) or an *os.File
// both do — because resolving an OCI-layout archive's config blob requires
// two independent passes over the same bytes (index.json, then the blob path
// it names), and re-reading via ReaderAt is simpler and cheaper than trying
// to make a single streaming pass do both jobs.
func Inspect(r io.ReaderAt, size int64) (Info, error) {
	top, err := scanEntries(r, size, map[string]bool{"index.json": true, "manifest.json": true})
	if err != nil {
		return Info{}, fmt.Errorf("read image archive: %w", err)
	}

	indexData, hasIndex := top["index.json"]
	manifestData, hasManifest := top["manifest.json"]

	switch {
	case hasIndex && hasManifest:
		// Some producers emit both layouts in one archive for broader
		// compatibility. Resolving only one and ignoring the other would let
		// a manifest that describes two different images (one per layout)
		// pick whichever this package happened to prefer — so both are
		// resolved, and required to agree.
		ociInfo, err := ociLayoutInfo(r, size, indexData)
		if err != nil {
			return Info{}, err
		}
		dockerInfo, err := dockerLayoutInfo(r, size, manifestData)
		if err != nil {
			return Info{}, err
		}
		if ociInfo.ConfigDigest != dockerInfo.ConfigDigest {
			return Info{}, fmt.Errorf("image archive carries both index.json and manifest.json, but they name different config digests (oci=%s docker=%s)",
				ociInfo.ConfigDigest, dockerInfo.ConfigDigest)
		}
		return Info{ConfigDigest: ociInfo.ConfigDigest, ManifestDigest: ociInfo.ManifestDigest, Tags: mergeTags(ociInfo.Tags, dockerInfo.Tags)}, nil
	case hasIndex:
		return ociLayoutInfo(r, size, indexData)
	case hasManifest:
		return dockerLayoutInfo(r, size, manifestData)
	default:
		return Info{}, fmt.Errorf("image archive contains neither index.json nor manifest.json; not a recognized OCI or Docker image archive")
	}
}

// InspectBytes is Inspect over an archive already fully read into memory —
// the shape a caller that must also sign those exact bytes wants, so the
// digest it checks and the bytes it signs are provably the same read.
func InspectBytes(data []byte) (Info, error) {
	return Inspect(bytes.NewReader(data), int64(len(data)))
}

// ociLayoutInfo resolves the config digest and tag out of an OCI-layout
// archive. index.json names the (single) image manifest's digest; that
// manifest blob, once located and re-hashed against the digest that named it,
// itself names the config's digest; the config blob, once located and
// re-hashed against THAT digest, is the archive's real image ID. Nothing here
// trusts a declared digest without hashing the bytes it supposedly names.
func ociLayoutInfo(r io.ReaderAt, size int64, indexData []byte) (Info, error) {
	var idx struct {
		Manifests []struct {
			MediaType   string            `json:"mediaType"`
			Digest      string            `json:"digest"`
			Annotations map[string]string `json:"annotations"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(indexData, &idx); err != nil {
		return Info{}, fmt.Errorf("parse index.json: %w", err)
	}
	if len(idx.Manifests) != 1 {
		return Info{}, fmt.Errorf("index.json declares %d manifests; only a single-platform, single-image archive is supported", len(idx.Manifests))
	}
	descriptor := idx.Manifests[0]

	manifestAlgo, manifestHex, ok := splitDigest(descriptor.Digest)
	if !ok {
		return Info{}, fmt.Errorf("index.json manifest digest %q is not of the form algo:hex", descriptor.Digest)
	}
	manifestPath := path.Join("blobs", manifestAlgo, manifestHex)

	manifestBlobs, err := scanEntries(r, size, map[string]bool{manifestPath: true})
	if err != nil {
		return Info{}, fmt.Errorf("read image archive: %w", err)
	}
	manifestBlob, ok := manifestBlobs[manifestPath]
	if !ok {
		return Info{}, fmt.Errorf("image archive is missing the manifest blob %s referenced by index.json", manifestPath)
	}
	if err := verifyDigest(manifestAlgo, manifestHex, manifestBlob, fmt.Sprintf("manifest blob %s", manifestPath)); err != nil {
		return Info{}, err
	}

	var imageManifest struct {
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
	}
	if err := json.Unmarshal(manifestBlob, &imageManifest); err != nil {
		return Info{}, fmt.Errorf("parse manifest blob %s: %w", manifestPath, err)
	}
	configAlgo, configHex, ok := splitDigest(imageManifest.Config.Digest)
	if !ok {
		return Info{}, fmt.Errorf("manifest blob %s declares config digest %q, not of the form algo:hex", manifestPath, imageManifest.Config.Digest)
	}
	configPath := path.Join("blobs", configAlgo, configHex)

	configBlobs, err := scanEntries(r, size, map[string]bool{configPath: true})
	if err != nil {
		return Info{}, fmt.Errorf("read image archive: %w", err)
	}
	configBlob, ok := configBlobs[configPath]
	if !ok {
		return Info{}, fmt.Errorf("image archive is missing the config blob %s referenced by manifest blob %s", configPath, manifestPath)
	}
	if err := verifyDigest(configAlgo, configHex, configBlob, fmt.Sprintf("config blob %s", configPath)); err != nil {
		return Info{}, err
	}

	var tags []string
	// io.containerd.image.name carries the full reference (Docker 25+ and
	// podman both populate it via containerd); org.opencontainers.image.ref.name
	// is frequently just a bare tag like "latest" with no repository at all.
	// The full name is reported first so a caller checking tags against a
	// repository has it to prefer.
	if name := descriptor.Annotations["io.containerd.image.name"]; name != "" {
		tags = append(tags, name)
	}
	if ref := descriptor.Annotations["org.opencontainers.image.ref.name"]; ref != "" {
		tags = append(tags, ref)
	}
	return Info{ConfigDigest: configAlgo + ":" + configHex, ManifestDigest: manifestAlgo + ":" + manifestHex, Tags: tags}, nil
}

// dockerLayoutInfo resolves the config digest and tags out of a Docker
// legacy (`docker save`) archive: manifest.json names the config blob's path
// and its embedded RepoTags; the config digest is computed from the blob's
// own bytes — the classical definition of a Docker image ID — since
// manifest.json carries no digest field of its own to check a hash against.
func dockerLayoutInfo(r io.ReaderAt, size int64, manifestData []byte) (Info, error) {
	var entries []struct {
		Config   string   `json:"Config"`
		RepoTags []string `json:"RepoTags"`
		Layers   []string `json:"Layers"`
	}
	if err := json.Unmarshal(manifestData, &entries); err != nil {
		return Info{}, fmt.Errorf("parse manifest.json: %w", err)
	}
	if len(entries) != 1 {
		return Info{}, fmt.Errorf("manifest.json declares %d images; only a single-image archive is supported", len(entries))
	}

	// Every path manifest.json names must resolve to exactly the canonical
	// tar entry scanEntries would accept — checked before any lookup, so a
	// path like "a/../cfg.json" is refused outright rather than silently
	// treated as "cfg.json" by one layer of this package and not another.
	configPath, err := validateArchivePath(entries[0].Config, "manifest.json Config path")
	if err != nil {
		return Info{}, err
	}
	for _, layer := range entries[0].Layers {
		if _, err := validateArchivePath(layer, "manifest.json Layers path"); err != nil {
			return Info{}, err
		}
	}

	blobs, err := scanEntries(r, size, map[string]bool{configPath: true})
	if err != nil {
		return Info{}, fmt.Errorf("read image archive: %w", err)
	}
	configData, ok := blobs[configPath]
	if !ok {
		return Info{}, fmt.Errorf("image archive is missing the config blob %s referenced by manifest.json", configPath)
	}

	sum := sha256.Sum256(configData)
	return Info{
		ConfigDigest: "sha256:" + hex.EncodeToString(sum[:]),
		Tags:         entries[0].RepoTags,
	}, nil
}

// mergeTags dedupes and concatenates two tag lists, preserving first-seen
// order, for the case where both an OCI and a Docker layout are present in
// the same archive and each names a tag.
func mergeTags(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	var merged []string
	for _, tags := range [][]string{a, b} {
		for _, t := range tags {
			if t == "" || seen[t] {
				continue
			}
			seen[t] = true
			merged = append(merged, t)
		}
	}
	return merged
}

// splitDigest splits a "algo:hex" digest string. It does not validate algo or
// hex further than requiring both halves be non-empty — that judgment belongs
// to verifyDigest, which knows what it is prepared to check.
func splitDigest(digest string) (algo, hexDigest string, ok bool) {
	algo, hexDigest, found := strings.Cut(digest, ":")
	if !found || algo == "" || hexDigest == "" {
		return "", "", false
	}
	return algo, hexDigest, true
}

// verifyDigest hashes data with algo and requires the result to equal
// hexDigest, naming what failed in errWhat. Only sha256 is supported — the
// only algorithm any archive this package has ever been asked to open uses —
// so any other algorithm is refused rather than silently trusted.
func verifyDigest(algo, hexDigest string, data []byte, errWhat string) error {
	if algo != "sha256" {
		return fmt.Errorf("%s: unsupported digest algorithm %q", errWhat, algo)
	}
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if got != hexDigest {
		return fmt.Errorf("%s: computed digest sha256:%s does not match declared digest sha256:%s", errWhat, got, hexDigest)
	}
	return nil
}

// scanEntries walks every entry of the tar archive at r (size bytes, from
// offset 0) exactly once, canonicalising each entry's name and rejecting a
// duplicate anywhere in the archive — not just among the names in want —
// since a producer that names two different bodies "manifest.json" is handing
// whoever reads it an ambiguity a signature-verified bundle must never
// contain, regardless of which name this particular call happens to care
// about. It returns the bounded contents of every entry whose canonical name
// is in want; every other entry's data is discarded without being
// materialized.
func scanEntries(r io.ReaderAt, size int64, want map[string]bool) (map[string][]byte, error) {
	tr, closeReader, err := tarReaderFor(io.NewSectionReader(r, 0, size))
	if err != nil {
		return nil, err
	}
	defer closeReader()

	seen := make(map[string]bool)
	found := make(map[string][]byte, len(want))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tar entry: %w", err)
		}

		name, err := canonicalEntryName(hdr.Name)
		if err != nil {
			return nil, fmt.Errorf("image archive: %w", err)
		}
		if seen[name] {
			return nil, fmt.Errorf("image archive contains duplicate entry %q", name)
		}
		seen[name] = true

		// A symlink (or hardlink, device, FIFO, ...) has no real body: tar
		// stores none, so reading one back yields zero bytes — which hashes
		// to a perfectly ordinary-looking, pinnable sha256 digest
		// (e3b0c442...) rather than to whatever the link's target actually
		// contains. Every entry this package might read must be data it
		// itself carries.
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeDir {
			return nil, fmt.Errorf("image archive entry %q is not a regular file or directory (type %q)", name, string(hdr.Typeflag))
		}

		if !want[name] {
			continue
		}
		data, err := readBoundedEntry(tr, hdr.Size)
		if err != nil {
			return nil, fmt.Errorf("read tar entry %s: %w", name, err)
		}
		found[name] = data
	}
	return found, nil
}

// canonicalEntryName strips a "./" prefix (the shape most archive tools emit
// for a root-relative entry) and requires what remains to already be in
// path.Clean's canonical form — allowing only the one difference Clean itself
// introduces for a directory entry's trailing slash. Docker and containerd
// both clean paths internally and let the last write win on a collision;
// this package refuses to guess which of two non-canonical spellings of "the
// same" path a real consumer would resolve to, so anything that is not
// already canonical is rejected outright rather than normalized.
func canonicalEntryName(rawName string) (string, error) {
	stripped := strings.TrimPrefix(rawName, "./")
	if path.IsAbs(stripped) {
		return "", fmt.Errorf("entry name %q is an absolute path", rawName)
	}
	for _, segment := range strings.Split(stripped, "/") {
		if segment == ".." {
			return "", fmt.Errorf("entry name %q contains a '..' segment", rawName)
		}
	}
	cleaned := path.Clean(stripped)
	if cleaned != stripped && cleaned+"/" != stripped {
		return "", fmt.Errorf("entry name %q is not canonical (path.Clean gives %q)", rawName, cleaned)
	}
	return cleaned, nil
}

// validateArchivePath applies canonicalEntryName's rules to a path a JSON
// document (manifest.json's Config or Layers) declares rather than to an
// actual tar header — the two must agree on what a given string names, or the
// "same" blob could mean something different depending on which layer of the
// archive is asked.
func validateArchivePath(rawPath, what string) (string, error) {
	if rawPath == "" {
		return "", fmt.Errorf("%s is empty", what)
	}
	name, err := canonicalEntryName(rawPath)
	if err != nil {
		return "", fmt.Errorf("%s %q is not a safe archive path: %w", what, rawPath, err)
	}
	return name, nil
}

// readBoundedEntry reads one tar entry's data, refusing anything over
// maxEntryBytes. declaredSize is checked first so an entry that lies about
// being huge is rejected without reading it at all; the actual bytes read are
// bounded too, since a tar header's declared size is exactly that — declared,
// not verified by the format itself.
func readBoundedEntry(r io.Reader, declaredSize int64) ([]byte, error) {
	if declaredSize > maxEntryBytes {
		return nil, fmt.Errorf("declares size %d bytes, over the %d byte limit for this kind of entry", declaredSize, maxEntryBytes)
	}
	data, err := io.ReadAll(io.LimitReader(r, maxEntryBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxEntryBytes {
		return nil, fmt.Errorf("exceeds the %d byte limit for this kind of entry", maxEntryBytes)
	}
	return data, nil
}

// tarReaderFor sniffs whether r is gzip-compressed (docker save's output is
// plain tar; some producers gzip it) and returns a tar.Reader over either
// form — wrapped in a boundedReader so a gzip bomb cannot be decompressed
// without limit — plus a close func for the gzip reader (a no-op for plain
// tar).
func tarReaderFor(r io.Reader) (*tar.Reader, func() error, error) {
	br := bufio.NewReader(r)
	magic, err := br.Peek(2)
	if err != nil && err != io.EOF {
		return nil, nil, fmt.Errorf("peek archive header: %w", err)
	}
	if len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return nil, nil, fmt.Errorf("open gzip reader: %w", err)
		}
		return tar.NewReader(&boundedReader{r: gz, limit: maxDecompressedBytes}), gz.Close, nil
	}
	return tar.NewReader(&boundedReader{r: br, limit: maxDecompressedBytes}), func() error { return nil }, nil
}

// boundedReader errors once more than limit bytes have been read through it,
// independent of what any tar header or gzip stream claims about its own
// size. This is the defense a zip-bomb-shaped image archive needs: the risk
// is in the decompression itself, not in any one entry's declared size.
type boundedReader struct {
	r     io.Reader
	limit int64
	total int64
}

func (b *boundedReader) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	b.total += int64(n)
	if b.total > b.limit {
		return n, fmt.Errorf("image archive exceeds %d decompressed bytes; refusing to read further", b.limit)
	}
	return n, err
}
