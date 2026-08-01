// Package loader implements the plugin install pipeline: a debounced fsnotify
// watcher dispatches tarballs through extract → verify → snapshot-into-DB.
package loader

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	manifest "github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
)

// maxTarballBytes caps cumulative uncompressed bytes extracted from a plugin
// tarball. Defends against gzip-bomb payloads (spec §5.1 size guidance).
// A var (not a const) solely so TestInstall_TarballTooLarge can lower it —
// materializing a real >100 MiB payload cost ~16s under -race for no extra
// coverage. Production code must never reassign it.
var maxTarballBytes int64 = 100 << 20 // 100 MiB

// maxTarballFiles caps the number of entries (files + directories) extracted
// from a plugin tarball. Defends against inode-exhaustion DoS where a small
// tarball can encode millions of zero-byte entries that pass the byte cap.
const maxTarballFiles = 10_000

// ExtractTarball extracts a gzip-compressed tarball at tarPath into destDir.
//
// Safety guarantees enforced on every entry:
//   - No path traversal: entries with ".." segments or absolute paths are rejected.
//   - Only regular files and directories are accepted; symlinks, hard links,
//     devices, and FIFOs are rejected to prevent privilege escalation.
//   - Cumulative uncompressed bytes across all entries must not exceed maxBytes.
//     This defends against gzip-bomb payloads (see plan §100MB cap note).
//   - Total number of entries (files + directories) must not exceed maxFiles.
//     This defends against inode-exhaustion DoS where a small tarball can encode
//     millions of zero-byte entries that pass the byte cap.
//   - File mode is 0755 if the executable bit is set, otherwise 0644.
func ExtractTarball(tarPath, destDir string, maxBytes int64, maxFiles int) error {
	f, err := os.Open(tarPath)
	if err != nil {
		return fmt.Errorf("open tarball: %w", err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("create gzip reader: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	var totalBytes int64
	var entryCount int

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar entry: %w", err)
		}

		entryCount++
		if entryCount > maxFiles {
			return fmt.Errorf("tarball exceeds %d-entry cap", maxFiles)
		}

		if err := validateTarEntry(hdr, destDir); err != nil {
			return fmt.Errorf("unsafe tar entry %q: %w", hdr.Name, err)
		}

		target := filepath.Join(destDir, filepath.FromSlash(hdr.Name))

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("create directory %q: %w", hdr.Name, err)
			}

		case tar.TypeReg:
			// Ensure parent directories exist (some tarballs omit explicit dir entries).
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("create parent dirs for %q: %w", hdr.Name, err)
			}

			remaining := maxBytes - totalBytes
			// +1 so that an entry exactly at the cap fails with the right error.
			written, writeErr := writeFile(target, tr, remaining+1, fileMode(hdr.Mode))
			totalBytes += written
			if writeErr != nil {
				return writeErr
			}
			if totalBytes > maxBytes {
				return fmt.Errorf("tarball exceeds %d-byte uncompressed size cap", maxBytes)
			}
		}
	}

	return nil
}

// validateTarEntry rejects entries that would escape destDir or use unsafe types.
func validateTarEntry(hdr *tar.Header, destDir string) error {
	name := filepath.FromSlash(hdr.Name)

	if filepath.IsAbs(name) {
		return errors.New("absolute path")
	}

	// filepath.Clean removes ".." but we want to detect it explicitly.
	for _, part := range strings.Split(hdr.Name, "/") {
		if part == ".." {
			return errors.New("path traversal via dot-dot segment")
		}
	}

	// Confirm the resolved path is still inside destDir.
	resolved := filepath.Join(destDir, name)
	rel, err := filepath.Rel(destDir, resolved)
	if err != nil || strings.HasPrefix(rel, "..") {
		return errors.New("path escapes destination directory")
	}

	switch hdr.Typeflag {
	case tar.TypeReg, tar.TypeDir:
		// Allowed.
	default:
		return fmt.Errorf("unsupported entry type %d (only regular files and directories allowed)", hdr.Typeflag)
	}

	return nil
}

// fileMode returns 0755 when the Unix executable bit is set, otherwise 0644.
func fileMode(mode int64) os.FileMode {
	if mode&0o111 != 0 {
		return 0o755
	}
	return 0o644
}

// ReadManifestFromTarball peeks inside a .tar.gz and returns the parsed
// manifest.Manifest without fully extracting the archive to disk. It reads
// only the manifest.yaml entry from the tar stream, applying the same
// maxTarballBytes / maxTarballFiles guards used by full extraction so a
// gzip-bomb cannot stall the startup sweep.
//
// Two tarball layouts are supported (mirroring resolveBundleRoot in install.go):
//   - Flat: manifest.yaml sits at the archive root.
//   - Nested: every file lives under a single top-level directory; manifest.yaml
//     is the only file named "manifest.yaml" under that prefix.
//
// Returns an error (never panics) when the archive is unreadable, has no
// manifest.yaml within the caps, or the YAML cannot be parsed. The caller
// (initial sweep) logs the error and skips the tarball.
func ReadManifestFromTarball(tarPath string) (*manifest.Manifest, error) {
	f, err := os.Open(tarPath)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("gzip reader: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	var totalBytes int64
	var entryCount int

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tar entry: %w", err)
		}

		entryCount++
		if entryCount > maxTarballFiles {
			return nil, fmt.Errorf("tarball exceeds %d-entry cap", maxTarballFiles)
		}

		// Apply the cumulative byte cap to non-manifest entries too so a
		// gzip-bomb stuffed before manifest.yaml does not stall the sweep.
		if hdr.Typeflag != tar.TypeReg {
			continue
		}

		totalBytes += hdr.Size
		if totalBytes > maxTarballBytes {
			return nil, fmt.Errorf("tarball exceeds %d-byte uncompressed size cap", maxTarballBytes)
		}

		name := filepath.ToSlash(hdr.Name)
		// Accept flat layout ("manifest.yaml") or single-top-level-dir layout
		// ("slack-1.0.1/manifest.yaml"). The path must have at most one leading
		// directory component and must end with "manifest.yaml".
		parts := strings.SplitN(name, "/", 3)
		isManifest := (len(parts) == 1 && parts[0] == "manifest.yaml") ||
			(len(parts) == 2 && parts[1] == "manifest.yaml")
		if !isManifest {
			continue
		}

		// Read the manifest bytes from the tar stream (bounded by the remaining cap).
		remaining := maxTarballBytes - totalBytes + hdr.Size // hdr.Size already counted above
		lr := &io.LimitedReader{R: tr, N: remaining + 1}
		data, readErr := io.ReadAll(lr)
		if readErr != nil {
			return nil, fmt.Errorf("read manifest.yaml: %w", readErr)
		}
		if lr.N == 0 {
			return nil, fmt.Errorf("manifest.yaml exceeds size cap")
		}

		var m manifest.Manifest
		if parseErr := manifest.Unmarshal(data, &m); parseErr != nil {
			return nil, fmt.Errorf("parse manifest.yaml: %w", parseErr)
		}
		if m.Name == "" {
			return nil, fmt.Errorf("manifest.name is required")
		}
		if m.Version == "" {
			return nil, fmt.Errorf("manifest.version is required")
		}
		return &m, nil
	}

	return nil, fmt.Errorf("manifest.yaml not found in tarball")
}

// writeFile copies at most limitBytes from src into a new file at path.
// It returns the number of bytes written and any error. If the reader contains
// more than limitBytes the write is interrupted and an error is returned.
func writeFile(path string, src io.Reader, limitBytes int64, mode os.FileMode) (int64, error) {
	out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return 0, fmt.Errorf("create file %q: %w", path, err)
	}
	defer out.Close()

	n, err := io.CopyN(out, src, limitBytes)
	if err != nil && !errors.Is(err, io.EOF) {
		return n, fmt.Errorf("write file %q: %w", path, err)
	}

	// If CopyN stopped because it hit limitBytes without EOF, the entry is too large.
	if n == limitBytes {
		// Check if there's more data in the stream.
		buf := make([]byte, 1)
		extra, _ := src.Read(buf)
		if extra > 0 {
			return n, fmt.Errorf("single entry exceeds size cap")
		}
	}

	return n, nil
}
