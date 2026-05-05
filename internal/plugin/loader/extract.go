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
)

// ExtractTarball extracts a gzip-compressed tarball at tarPath into destDir.
//
// Safety guarantees enforced on every entry:
//   - No path traversal: entries with ".." segments or absolute paths are rejected.
//   - Only regular files and directories are accepted; symlinks, hard links,
//     devices, and FIFOs are rejected to prevent privilege escalation.
//   - Cumulative uncompressed bytes across all entries must not exceed maxBytes.
//     This defends against gzip-bomb payloads (see plan §100MB cap note).
//   - File mode is 0755 if the executable bit is set, otherwise 0644.
func ExtractTarball(tarPath, destDir string, maxBytes int64) error {
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

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar entry: %w", err)
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
			return errors.New("path traversal via ..")
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
