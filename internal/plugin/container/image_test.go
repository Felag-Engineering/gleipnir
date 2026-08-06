package container

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The Fake must exercise the archive reader the same way a real daemon does —
// a caller that hands over an already-consumed file finds out here rather than
// after an install has "succeeded" with nothing loaded.
func TestFakeImageLoad_ReadsTheArchive(t *testing.T) {
	f := NewFake()
	f.PendingImages = []ImageInfo{{ID: "sha256:abc", SizeBytes: 99}}

	if err := f.ImageLoad(context.Background(), strings.NewReader("archive bytes")); err != nil {
		t.Fatalf("ImageLoad: %v", err)
	}
	if f.Loads != 1 {
		t.Errorf("Loads = %d, want 1", f.Loads)
	}
	if f.LoadedBytes != int64(len("archive bytes")) {
		t.Errorf("LoadedBytes = %d, want %d", f.LoadedBytes, len("archive bytes"))
	}

	info, err := f.ImageInspect(context.Background(), "sha256:abc")
	if err != nil {
		t.Fatalf("ImageInspect: %v", err)
	}
	if info.SizeBytes != 99 {
		t.Errorf("SizeBytes = %d, want 99", info.SizeBytes)
	}
}

// Every reference an image answers to resolves to the same image, because a
// caller verifying a manifest pin may hold any one of them.
func TestFakeImageInspect_ResolvesEveryReference(t *testing.T) {
	f := NewFake()
	f.AddImage(ImageInfo{
		ID:          "sha256:aaa",
		RepoTags:    []string{"acme/plugin:1.0"},
		RepoDigests: []string{"acme/plugin@sha256:bbb"},
	})

	for _, ref := range []string{"sha256:aaa", "acme/plugin:1.0", "acme/plugin@sha256:bbb"} {
		if _, err := f.ImageInspect(context.Background(), ref); err != nil {
			t.Errorf("ImageInspect(%q): %v", ref, err)
		}
	}
}

func TestFakeImageInspect_NotFound(t *testing.T) {
	f := NewFake()
	_, err := f.ImageInspect(context.Background(), "sha256:missing")
	if !errors.Is(err, ErrImageNotFound) {
		t.Errorf("error = %v, want ErrImageNotFound", err)
	}
}

func TestFakeImageLoad_ErrorSurfaces(t *testing.T) {
	f := NewFake()
	f.LoadErr = errors.New("socket closed")
	f.PendingImages = []ImageInfo{{ID: "sha256:abc"}}

	if err := f.ImageLoad(context.Background(), strings.NewReader("x")); err == nil {
		t.Fatal("ImageLoad succeeded despite LoadErr")
	}
	if _, err := f.ImageInspect(context.Background(), "sha256:abc"); !errors.Is(err, ErrImageNotFound) {
		t.Error("a failed load still made the image present")
	}
}

// Manual posture is discovery-only: loading an image is putting bytes into the
// daemon, which is a write, and writes fail closed (spec §7).
func TestReadOnlyRuntime_ImageLoadIsAWrite(t *testing.T) {
	inner := NewFake()
	ro := NewReadOnlyRuntime(inner)

	if err := ro.ImageLoad(context.Background(), strings.NewReader("archive")); !errors.Is(err, ErrManualModeWrite) {
		t.Errorf("ImageLoad error = %v, want ErrManualModeWrite", err)
	}
	if inner.Loads != 0 {
		t.Error("the wrapped runtime saw a load; the wrapper must not reach inner's write path")
	}
}

// ImageInspect is a read, so manual mode still gets it: without it nothing can
// verify the image an operator loaded against the manifest's pin.
func TestReadOnlyRuntime_ImageInspectDelegates(t *testing.T) {
	inner := NewFake()
	inner.AddImage(ImageInfo{ID: "sha256:abc"})
	ro := NewReadOnlyRuntime(inner)

	if _, err := ro.ImageInspect(context.Background(), "sha256:abc"); err != nil {
		t.Errorf("ImageInspect: %v", err)
	}
}

// ImageRemove drops the image under every reference it answered to. A removal
// that cleared only the digest would leave the repo:tag resolving to bytes that
// are gone.
func TestFakeImageRemove_ClearsEveryReference(t *testing.T) {
	f := NewFake()
	f.AddImage(ImageInfo{
		ID:          "sha256:abc",
		RepoTags:    []string{"ghcr.io/example/plugin:1.0.0"},
		RepoDigests: []string{"ghcr.io/example/plugin@sha256:abc"},
	})

	if err := f.ImageRemove(context.Background(), "sha256:abc"); err != nil {
		t.Fatalf("ImageRemove: %v", err)
	}
	for _, ref := range []string{"sha256:abc", "ghcr.io/example/plugin:1.0.0", "ghcr.io/example/plugin@sha256:abc"} {
		if _, err := f.ImageInspect(context.Background(), ref); !errors.Is(err, ErrImageNotFound) {
			t.Errorf("%s still resolves after removal", ref)
		}
	}
}

// Removing an image that is not there reports it rather than succeeding. GC
// treats that as reclaimed, but the distinction is the caller's to make — this
// layer answering "sure, done" would erase it.
func TestFakeImageRemove_MissingImageIsReported(t *testing.T) {
	if err := NewFake().ImageRemove(context.Background(), "sha256:nope"); !errors.Is(err, ErrImageNotFound) {
		t.Errorf("ImageRemove error = %v, want ErrImageNotFound", err)
	}
}

// In manual posture the operator loaded the image, so reclaiming it is theirs
// to decide. GC "reports orphans and removes nothing" only holds if this
// refuses rather than obliging.
func TestReadOnlyRuntime_ImageRemoveIsAWrite(t *testing.T) {
	inner := NewFake()
	inner.AddImage(ImageInfo{ID: "sha256:abc"})
	ro := NewReadOnlyRuntime(inner)

	if err := ro.ImageRemove(context.Background(), "sha256:abc"); !errors.Is(err, ErrManualModeWrite) {
		t.Errorf("ImageRemove error = %v, want ErrManualModeWrite", err)
	}
	if len(inner.ImageRemovals) != 0 {
		t.Error("the wrapped runtime saw a removal; the wrapper must not reach inner's write path")
	}
	if _, err := inner.ImageInspect(context.Background(), "sha256:abc"); err != nil {
		t.Errorf("the image was removed anyway: %v", err)
	}
}
