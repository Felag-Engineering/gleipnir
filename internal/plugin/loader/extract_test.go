package loader

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

// makeTarball builds an in-memory .tar.gz from the provided entries and writes
// it to a temp file, returning the file path. Each entry is (name, content,
// typeflag, mode). An empty typeflag defaults to tar.TypeReg.
func makeTarball(t *testing.T, entries []tarEntry) string {
	t.Helper()

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	for _, e := range entries {
		typeflag := e.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		hdr := &tar.Header{
			Name:     e.name,
			Typeflag: typeflag,
			Mode:     e.mode,
			Size:     int64(len(e.content)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write tar header %q: %v", e.name, err)
		}
		if len(e.content) > 0 {
			if _, err := tw.Write(e.content); err != nil {
				t.Fatalf("write tar body %q: %v", e.name, err)
			}
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}

	tmp := filepath.Join(t.TempDir(), "test.tar.gz")
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write tarball: %v", err)
	}
	return tmp
}

type tarEntry struct {
	name     string
	content  []byte
	typeflag byte
	mode     int64
}

func TestExtractTarball_HappyPath(t *testing.T) {
	tarPath := makeTarball(t, []tarEntry{
		{name: "manifest.yaml", content: []byte("name: test-plugin\n"), mode: 0o644},
		{name: "plugin-bin", content: []byte("binary content"), mode: 0o755},
	})

	destDir := t.TempDir()
	if err := ExtractTarball(tarPath, destDir, 100<<20, 10_000); err != nil {
		t.Fatalf("ExtractTarball: %v", err)
	}

	// Both files must be present.
	for _, name := range []string{"manifest.yaml", "plugin-bin"} {
		if _, err := os.Stat(filepath.Join(destDir, name)); err != nil {
			t.Errorf("expected %q to exist after extraction: %v", name, err)
		}
	}

	// Executable bit preserved.
	info, err := os.Stat(filepath.Join(destDir, "plugin-bin"))
	if err != nil {
		t.Fatalf("stat plugin-bin: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Errorf("plugin-bin: mode %v, want executable bit set", info.Mode())
	}
}

func TestExtractTarball_AbsolutePathRejected(t *testing.T) {
	tarPath := makeTarball(t, []tarEntry{
		{name: "/etc/passwd", content: []byte("should not land here"), mode: 0o644},
	})

	err := ExtractTarball(tarPath, t.TempDir(), 100<<20, 10_000)
	if err == nil {
		t.Fatal("expected error for absolute path, got nil")
	}
}

func TestExtractTarball_PathTraversalRejected(t *testing.T) {
	tarPath := makeTarball(t, []tarEntry{
		{name: "../escape.txt", content: []byte("escaped"), mode: 0o644},
	})

	err := ExtractTarball(tarPath, t.TempDir(), 100<<20, 10_000)
	if err == nil {
		t.Fatal("expected error for path traversal, got nil")
	}
}

func TestExtractTarball_NestedTraversalRejected(t *testing.T) {
	tarPath := makeTarball(t, []tarEntry{
		{name: "subdir/../../escape.txt", content: []byte("escaped"), mode: 0o644},
	})

	err := ExtractTarball(tarPath, t.TempDir(), 100<<20, 10_000)
	if err == nil {
		t.Fatal("expected error for nested path traversal, got nil")
	}
}

func TestExtractTarball_SymlinkRejected(t *testing.T) {
	tarPath := makeTarball(t, []tarEntry{
		{name: "link", content: nil, typeflag: tar.TypeSymlink, mode: 0o644},
	})

	err := ExtractTarball(tarPath, t.TempDir(), 100<<20, 10_000)
	if err == nil {
		t.Fatal("expected error for symlink entry, got nil")
	}
}

func TestExtractTarball_HardLinkRejected(t *testing.T) {
	tarPath := makeTarball(t, []tarEntry{
		{name: "hardlink", content: nil, typeflag: tar.TypeLink, mode: 0o644},
	})

	err := ExtractTarball(tarPath, t.TempDir(), 100<<20, 10_000)
	if err == nil {
		t.Fatal("expected error for hard link entry, got nil")
	}
}

func TestExtractTarball_OversizedTotalRejected(t *testing.T) {
	// Two 6-byte files; cap at 10 bytes total.
	tarPath := makeTarball(t, []tarEntry{
		{name: "a.bin", content: []byte("AAAAAA"), mode: 0o644},
		{name: "b.bin", content: []byte("BBBBBB"), mode: 0o644},
	})

	err := ExtractTarball(tarPath, t.TempDir(), 10, 10_000)
	if err == nil {
		t.Fatal("expected error for oversized total payload, got nil")
	}
}

func TestExtractTarball_OversizedSingleEntryRejected(t *testing.T) {
	// One 20-byte file; cap at 10 bytes total.
	tarPath := makeTarball(t, []tarEntry{
		{name: "big.bin", content: bytes.Repeat([]byte("X"), 20), mode: 0o644},
	})

	err := ExtractTarball(tarPath, t.TempDir(), 10, 10_000)
	if err == nil {
		t.Fatal("expected error for single oversized entry, got nil")
	}
}

func TestExtractTarball_SubdirectoryCreated(t *testing.T) {
	tarPath := makeTarball(t, []tarEntry{
		{name: "subdir/", content: nil, typeflag: tar.TypeDir, mode: 0o755},
		{name: "subdir/file.txt", content: []byte("hello"), mode: 0o644},
	})

	destDir := t.TempDir()
	if err := ExtractTarball(tarPath, destDir, 100<<20, 10_000); err != nil {
		t.Fatalf("ExtractTarball: %v", err)
	}

	if _, err := os.Stat(filepath.Join(destDir, "subdir", "file.txt")); err != nil {
		t.Errorf("expected subdir/file.txt to exist: %v", err)
	}
}

// TestExtractTarball_FileCountCap defends against inode-exhaustion DoS via
// tarballs that pack many tiny entries past the configured maxFiles limit.
func TestExtractTarball_FileCountCap(t *testing.T) {
	tests := []struct {
		name     string
		nEntries int
		maxFiles int
		wantErr  bool
	}{
		{name: "under cap", nEntries: 5, maxFiles: 10, wantErr: false},
		{name: "exactly at cap", nEntries: 10, maxFiles: 10, wantErr: false},
		{name: "one over cap", nEntries: 11, maxFiles: 10, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entries := make([]tarEntry, tt.nEntries)
			for i := range entries {
				entries[i] = tarEntry{
					name:    filepath.Join("f", filepath.Base(t.Name())+"-"+string(rune('a'+i))+".txt"),
					content: []byte("x"),
					mode:    0o644,
				}
			}
			tarPath := makeTarball(t, entries)
			err := ExtractTarball(tarPath, t.TempDir(), 100<<20, tt.maxFiles)
			if tt.wantErr && err == nil {
				t.Fatalf("expected file-count cap error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
