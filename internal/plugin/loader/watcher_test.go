package loader

import (
	"context"
	"os"
	"path/filepath"
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

func TestWatcher_DebouncesBurstWrites(t *testing.T) {
	dir := t.TempDir()
	stub := &stubInstaller{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := NewWatcher(dir, stub.install, WithDebounce(testDebounce))

	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	// Give the watcher time to register the inotify watch.
	time.Sleep(20 * time.Millisecond)

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

	// Drop the tarball BEFORE the watcher starts.
	tarPath := filepath.Join(dir, "pre-existing.tar.gz")
	if err := os.WriteFile(tarPath, []byte("content"), 0o644); err != nil {
		t.Fatalf("write pre-existing tarball: %v", err)
	}

	stub := &stubInstaller{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := NewWatcher(dir, stub.install, WithDebounce(testDebounce))

	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

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

	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	time.Sleep(20 * time.Millisecond)

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

	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	time.Sleep(20 * time.Millisecond)
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

	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	// Give the watcher time to register the inotify watch.
	time.Sleep(20 * time.Millisecond)

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

func TestWatcher_RemoveCancelsPendingTimer(t *testing.T) {
	dir := t.TempDir()
	stub := &stubInstaller{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := NewWatcher(dir, stub.install, WithDebounce(testDebounce))

	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	time.Sleep(20 * time.Millisecond)

	tarPath := filepath.Join(dir, "remove-me.tar.gz")
	if err := os.WriteFile(tarPath, []byte("content"), 0o644); err != nil {
		t.Fatalf("write tarball: %v", err)
	}

	// Remove the file before the debounce window expires.
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
