package loader

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stubInstaller counts Install calls and records which paths were installed.
type stubInstaller struct {
	mu    sync.Mutex
	calls []string
}

func (s *stubInstaller) install(ctx context.Context, tarPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, tarPath)
	return nil
}

func (s *stubInstaller) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

// waitForCount blocks until the stubInstaller has accumulated at least n calls
// or deadline is reached. Returns the call count at deadline.
func (s *stubInstaller) waitForCount(n int, deadline time.Duration) int {
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		if s.count() >= n {
			return s.count()
		}
		time.Sleep(5 * time.Millisecond)
	}
	return s.count()
}

const testDebounce = 50 * time.Millisecond

// runWatcher calls Setup, starts Run in a goroutine, and returns a done channel.
// The test is fatally failed if Setup returns an error.
func runWatcher(t *testing.T, ctx context.Context, w *Watcher) <-chan error {
	t.Helper()
	fw, err := w.Setup()
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx, fw) }()
	return done
}

func TestWatcher_DebouncesBurstWrites(t *testing.T) {
	dir := t.TempDir()
	stub := &stubInstaller{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := NewWatcher(dir, stub.install, WithDebounce(testDebounce))
	done := runWatcher(t, ctx, w)

	// fw.Add in Setup registers the watch synchronously with the kernel, so
	// events are captured immediately. No sleep needed to wait for registration.

	tarPath := filepath.Join(dir, "test-plugin.tar.gz")

	// Simulate 4 write chunks — the watcher should collapse them to 1 install.
	for i := 0; i < 4; i++ {
		if err := os.WriteFile(tarPath, []byte("chunk"), 0o644); err != nil {
			t.Fatalf("write chunk %d: %v", i, err)
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Wait for the debounce window to expire and one install to fire.
	n := stub.waitForCount(1, testDebounce*10)
	if n != 1 {
		t.Errorf("install call count = %d, want exactly 1 (debounce should collapse burst)", n)
	}

	cancel()
	<-done
}

func TestWatcher_InitialSweep(t *testing.T) {
	dir := t.TempDir()

	// Drop a real tarball BEFORE the watcher starts. The sweep now calls
	// ReadManifestFromTarball so garbage bytes no longer schedule anything —
	// we need a parseable manifest.yaml inside the archive.
	tarPath := filepath.Join(dir, "pre-existing.tar.gz")
	manifestBytes := []byte("schema_version: v1\nname: pre-existing\nversion: 1.0.0\nservices:\n  tool: v1\nauth:\n  mode: instance_credentials\n  strategy: none\n")
	writeTarball(t, tarPath, []tarEntry{
		{name: "manifest.yaml", content: manifestBytes, mode: 0o644},
		{name: "pre-existing", content: []byte("fake binary"), mode: 0o755},
	})

	stub := &stubInstaller{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := NewWatcher(dir, stub.install, WithDebounce(testDebounce))
	done := runWatcher(t, ctx, w)

	// The initial sweep enqueues the file immediately, then the debounce window fires.
	n := stub.waitForCount(1, testDebounce*10)
	if n < 1 {
		t.Errorf("install count = %d after initial sweep, want >= 1", n)
	}

	cancel()
	<-done
}

func TestWatcher_IgnoresNonTarball(t *testing.T) {
	dir := t.TempDir()
	stub := &stubInstaller{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := NewWatcher(dir, stub.install, WithDebounce(testDebounce))
	done := runWatcher(t, ctx, w)

	// Drop a non-tarball file — must not trigger an install.
	if err := os.WriteFile(filepath.Join(dir, "README.txt"), []byte("notes"), 0o644); err != nil {
		t.Fatalf("write README.txt: %v", err)
	}

	// Wait longer than the debounce window and confirm no install fired.
	time.Sleep(testDebounce * 4)
	if n := stub.count(); n != 0 {
		t.Errorf("install count = %d, want 0 (non-tarball should be ignored)", n)
	}

	cancel()
	<-done
}

func TestWatcher_ContextCancel_StopsCleanly(t *testing.T) {
	dir := t.TempDir()

	var installCount atomic.Int64
	install := func(ctx context.Context, tarPath string) error {
		installCount.Add(1)
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())

	w := NewWatcher(dir, install, WithDebounce(testDebounce))
	done := runWatcher(t, ctx, w)

	cancel()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Errorf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancel within 2s")
	}
}

// TestWatcher_CancelDuringDebounce_NoRace is a regression test for the shutdown
// race where time.Timer.Stop() returns false (callback already queued), then
// close(fire) is called before the callback sends on fire — causing a panic.
// The fix: drop close(fire) and have the dispatch goroutine select on ctx.Done().
func TestWatcher_CancelDuringDebounce_NoRace(t *testing.T) {
	dir := t.TempDir()
	stub := &stubInstaller{}

	ctx, cancel := context.WithCancel(context.Background())

	const debounce = 50 * time.Millisecond
	w := NewWatcher(dir, stub.install, WithDebounce(debounce))
	done := runWatcher(t, ctx, w)

	// Drop a tarball to arm the debounce timer.
	tarPath := filepath.Join(dir, "race-check.tar.gz")
	if err := os.WriteFile(tarPath, []byte("content"), 0o644); err != nil {
		t.Fatalf("write tarball: %v", err)
	}

	// Cancel right in the middle of the debounce window, while the timer may
	// be queued or about to fire. This is the window that previously panicked.
	time.Sleep(debounce / 2)
	cancel()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Errorf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after context cancel within 3s")
	}
	// If we reach here without panic the race is not triggered. install may or
	// may not have fired once depending on timing — either is acceptable.
}

// TestWatcher_FsnotifySetupFailure_NoGoroutineLeak verifies that when Setup
// fails (os.MkdirAll receives a path that is a regular file rather than a
// directory, which returns ENOTDIR), it returns an error without leaking the
// dispatch goroutine. The dispatch goroutine is spawned inside Run, which is
// never reached when Setup fails.
func TestWatcher_FsnotifySetupFailure_NoGoroutineLeak(t *testing.T) {
	// Create a regular file where the watcher expects a directory.
	// os.MkdirAll on a path that is a regular file returns ENOTDIR.
	dir := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(dir, []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}

	stub := &stubInstaller{}
	w := NewWatcher(dir, stub.install, WithDebounce(20*time.Millisecond))

	before := runtime.NumGoroutine()

	_, err := w.Setup()
	if err == nil {
		t.Fatal("expected Setup to return an error when dir is a regular file")
	}

	// Poll until the goroutine count stabilises (or a deadline elapses). A
	// fixed sleep is flaky under high scheduler load; polling with a deadline
	// correctly handles both fast and slow runtimes.
	var after int
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		after = runtime.NumGoroutine()
		if after <= before+2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Allow a small delta for unrelated runtime goroutines. The dispatch goroutine
	// must not have been started, so the count should not have grown.
	if after > before+2 {
		t.Errorf("goroutine count grew from %d to %d after failed Setup — possible leak", before, after)
	}
}

func TestWatcher_RemoveCancelsPendingTimer(t *testing.T) {
	dir := t.TempDir()
	stub := &stubInstaller{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := NewWatcher(dir, stub.install, WithDebounce(testDebounce))
	done := runWatcher(t, ctx, w)

	tarPath := filepath.Join(dir, "remove-me.tar.gz")
	if err := os.WriteFile(tarPath, []byte("content"), 0o644); err != nil {
		t.Fatalf("write tarball: %v", err)
	}

	// Remove the file before the debounce window expires. A brief sleep here
	// ensures the Create event has been dispatched to the watcher's event loop
	// so the debounce timer is armed before the Remove event cancels it.
	// This is unavoidably time-based because the OS event delivery has no
	// synchronous acknowledgement we can observe from outside the watcher.
	time.Sleep(10 * time.Millisecond)
	if err := os.Remove(tarPath); err != nil {
		t.Fatalf("remove tarball: %v", err)
	}

	// Wait for the debounce window; the install should NOT fire (file was removed).
	time.Sleep(testDebounce * 4)
	if n := stub.count(); n != 0 {
		t.Errorf("install count = %d, want 0 (removed file timer should be cancelled)", n)
	}

	cancel()
	<-done
}

// makeManifestTarball writes a minimal (unsigned) tarball with the given
// name and version into dir, returning the absolute path.
func makeManifestTarball(t *testing.T, dir, filename, name, version string) string {
	t.Helper()
	manifestBytes := []byte("schema_version: v1\nname: " + name + "\nversion: " + version + "\nservices:\n  tool: v1\nauth:\n  mode: instance_credentials\n  strategy: none\n")
	tarPath := filepath.Join(dir, filename)
	writeTarball(t, tarPath, []tarEntry{
		{name: "manifest.yaml", content: manifestBytes, mode: 0o644},
		{name: name, content: []byte("fake binary for " + name), mode: 0o755},
	})
	return tarPath
}

// sweepPaths runs the watcher over a pre-populated directory and collects the
// set of absolute paths that were scheduled for install. It cancels after the
// debounce window has elapsed for all expected installs.
func sweepPaths(t *testing.T, dir string, expectedCount int) []string {
	t.Helper()
	stub := &stubInstaller{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := NewWatcher(dir, stub.install, WithDebounce(testDebounce))
	done := runWatcher(t, ctx, w)

	// Wait long enough for all debounce timers to fire.
	stub.waitForCount(expectedCount, testDebounce*20)

	cancel()
	<-done

	stub.mu.Lock()
	defer stub.mu.Unlock()
	return append([]string(nil), stub.calls...)
}

// TestWatcher_Sweep_TwoVersions verifies that when two tarballs for the same
// plugin name are present, only the higher version is scheduled.
func TestWatcher_Sweep_TwoVersions(t *testing.T) {
	dir := t.TempDir()
	makeManifestTarball(t, dir, "myplugin-1.0.tar.gz", "myplugin", "1.0")
	makeManifestTarball(t, dir, "myplugin-1.1.tar.gz", "myplugin", "1.1")

	paths := sweepPaths(t, dir, 1)
	if len(paths) != 1 {
		t.Fatalf("sweep scheduled %d tarballs, want 1", len(paths))
	}
	if !strings.Contains(paths[0], "1.1") {
		t.Errorf("sweep scheduled %q; want the 1.1 tarball", paths[0])
	}
}

// TestWatcher_Sweep_SemverLexicalTrap verifies that 1.10 beats 1.9 even though
// "1.10" < "1.9" under plain lexical ordering.
func TestWatcher_Sweep_SemverLexicalTrap(t *testing.T) {
	dir := t.TempDir()
	makeManifestTarball(t, dir, "slack-1.9.tar.gz", "slack", "1.9")
	makeManifestTarball(t, dir, "slack-1.10.tar.gz", "slack", "1.10")

	paths := sweepPaths(t, dir, 1)
	if len(paths) != 1 {
		t.Fatalf("sweep scheduled %d tarballs, want 1", len(paths))
	}
	if !strings.Contains(paths[0], "1.10") {
		t.Errorf("sweep scheduled %q; want the 1.10 tarball (semver ordering, not lexical)", paths[0])
	}
}

// TestWatcher_Sweep_FilenameOrderIndependence verifies that the sweep picks the
// highest version regardless of which filename sorts first under os.ReadDir.
// Here "slack-1.10.tar.gz" < "slack-1.9.tar.gz" lexically, so without version
// selection the last-processed file would be the wrong choice.
func TestWatcher_Sweep_FilenameOrderIndependence(t *testing.T) {
	dir := t.TempDir()
	// Filenames sort as: "slack-1.10.tar.gz" < "slack-1.9.tar.gz" alphabetically.
	// Without version selection the sweep would install 1.9 last, overwriting 1.10.
	makeManifestTarball(t, dir, "slack-1.10.tar.gz", "slack", "1.10")
	makeManifestTarball(t, dir, "slack-1.9.tar.gz", "slack", "1.9")

	paths := sweepPaths(t, dir, 1)
	if len(paths) != 1 {
		t.Fatalf("sweep scheduled %d tarballs, want 1", len(paths))
	}
	if !strings.Contains(paths[0], "1.10") {
		t.Errorf("sweep scheduled %q; want the 1.10 tarball (filename order must not matter)", paths[0])
	}
}

// TestWatcher_Sweep_GarbageSkipped verifies that an unreadable (garbage) tarball
// alongside a valid one does not abort the sweep: the valid tarball is scheduled
// and the garbage file is skipped.
func TestWatcher_Sweep_GarbageSkipped(t *testing.T) {
	dir := t.TempDir()
	makeManifestTarball(t, dir, "valid-1.0.tar.gz", "valid", "1.0")

	// Write garbage bytes that will fail gzip decoding.
	garbagePath := filepath.Join(dir, "garbage.tar.gz")
	if err := os.WriteFile(garbagePath, []byte("not a tarball"), 0o644); err != nil {
		t.Fatalf("write garbage: %v", err)
	}

	paths := sweepPaths(t, dir, 1)
	if len(paths) != 1 {
		t.Fatalf("sweep scheduled %d tarballs, want 1 (garbage should be skipped)", len(paths))
	}
	if !strings.Contains(paths[0], "valid") {
		t.Errorf("sweep scheduled %q; want the valid tarball", paths[0])
	}
}
