//go:build substratev2 && unix

package main

// boot_v2_test.go proves the substratev2 build boots the real HTTP server and
// serves /api/v1/health without starting any plugin machinery. assembly.New
// (#948) returns an Assembly with no plugins today, so a missing plugins
// directory — fatal to the v1 build's fsnotify watcher — is the cheapest
// available proof that nothing here needs it. #962 replaces the no-op
// assembly; this test's assertions (server up, health green, clean shutdown)
// must keep passing unchanged once it does.

import (
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/infra/config"
)

// freeLoopbackAddr claims an ephemeral loopback port and immediately releases
// it. The window between release and run()'s own net.Listen is a theoretical
// race, but this test owns the only process opening ports in that window.
func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a free port: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("release reserved port: %v", err)
	}
	return addr
}

// waitForHealth polls GET /api/v1/health until it returns 200 or deadline
// elapses. Polling is unavoidable here: the server comes up in a separate
// goroutine behind a real TCP listener, so there is no in-process event to
// synchronize on instead.
func waitForHealth(t *testing.T, addr string, deadline time.Duration) bool {
	t.Helper()
	client := &http.Client{Timeout: 500 * time.Millisecond}
	url := "http://" + addr + "/api/v1/health"
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		resp, err := client.Get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return true
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func TestBootV2ServesHealthWithNoPluginMachinery(t *testing.T) {
	dir := t.TempDir()
	addr := freeLoopbackAddr(t)

	t.Setenv("GLEIPNIR_ENCRYPTION_KEY", strings.Repeat("ab", 32))
	t.Setenv("GLEIPNIR_DB_PATH", filepath.Join(dir, "gleipnir.db"))
	t.Setenv("GLEIPNIR_PID_FILE", filepath.Join(dir, "gleipnir.pid"))
	// Never created. The default build's fsnotify watcher fails startup when
	// this directory does not exist; the v2 build's no-op assembly must not
	// need it at all.
	t.Setenv("GLEIPNIR_PLUGINS_DIR", filepath.Join(dir, "plugins-does-not-exist"))
	t.Setenv("GLEIPNIR_LISTEN_ADDR", addr)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- run(cfg) }()

	if !waitForHealth(t, addr, 10*time.Second) {
		t.Fatalf("server on %s never became healthy", addr)
	}

	// Shut down the same way an operator does, then confirm run() returns
	// cleanly within the drain budget.
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("signal self: %v", err)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("run() returned error: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("run() did not return after SIGTERM")
	}
}
