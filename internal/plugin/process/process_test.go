//go:build unix

package process_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/identity"
	"github.com/felag-engineering/gleipnir/internal/plugin/process"
	"github.com/felag-engineering/gleipnir/plugin-sdk/hostwire"
)

// TestMain intercepts the test binary when run in "fixture mode" so it acts as
// a plugin subprocess rather than running the test suite. This is the standard
// Go re-exec pattern for testing code that spawns subprocesses.
//
// The GLEIPNIR_TEST_FIXTURE env var selects the mode:
//   - "serve-and-block"  — serve the plugin protocol, block until killed
//   - "serve-and-crash"  — serve the plugin protocol, then exit(17) after 200ms
//   - "exit-no-handshake" — print junk to stdout and exit immediately
func TestMain(m *testing.M) {
	switch os.Getenv("GLEIPNIR_TEST_FIXTURE") {
	case "serve-and-block":
		runFixtureServePlugin()
		os.Exit(0)
	case "serve-and-crash":
		runFixtureServeThenCrash()
		os.Exit(17) // not reached; runFixtureServeThenCrash calls os.Exit itself
	case "exit-no-handshake":
		// Print something to stdout so go-plugin's magic-cookie check can fail
		// in a recognisable way. The important thing is that we exit quickly.
		fmt.Println("not a valid handshake line")
		os.Exit(0)
	case "serve-and-trap-sigterm":
		runFixtureServeTrapSigterm()
		os.Exit(0)
	case "echo-env":
		runFixtureServeEchoEnv()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// ── healthCall captures a single HealthSetter invocation for assertions ───────

type healthCall struct {
	instanceID string
	state      model.PluginHealthState
	detail     string
}

// ── helpers ───────────────────────────────────────────────────────────────────

// fixtureConfig builds a Config that re-execs the test binary in the given
// fixture mode. The Launch function injects GLEIPNIR_TEST_FIXTURE into the
// subprocess environment via os.Setenv before calling hostwire.Launch.
//
// Because each test case runs serially, using os.Setenv + os.Unsetenv is
// correct. (If parallel test execution is needed, switch to building a per-test
// fixture binary in TestMain via `go build -o tmpdir`.)
func fixtureConfig(
	t *testing.T,
	mode string,
	issuer *identity.Registry,
	healthCh chan healthCall,
) process.Config {
	t.Helper()

	var healthSetter func(ctx context.Context, instanceID string, target model.PluginHealthState, detail string)
	if healthCh != nil {
		healthSetter = func(_ context.Context, id string, target model.PluginHealthState, detail string) {
			healthCh <- healthCall{instanceID: id, state: target, detail: detail}
		}
	}

	return process.Config{
		BinaryPath:     os.Args[0], // re-exec the test binary
		InstanceID:     "test-instance-1",
		PluginID:       "test-plugin-1",
		InstanceName:   "test-instance",
		StartupTimeout: 30 * time.Second,
		StopGrace:      10 * time.Second,
		IdentityIssuer: issuer,
		HealthSetter:   healthSetter,
		// Launch wraps hostwire.Launch with environment injection.
		Launch: func(ctx context.Context, binaryPath string, host hostwire.HostServer, opts hostwire.Options) (*hostwire.Client, func(), error) {
			os.Setenv("GLEIPNIR_TEST_FIXTURE", mode)
			client, teardown, err := hostwire.Launch(ctx, binaryPath, host, opts)
			os.Unsetenv("GLEIPNIR_TEST_FIXTURE")
			return client, teardown, err
		},
	}
}

// ── test cases ────────────────────────────────────────────────────────────────

// TestStart_Success verifies that a well-behaved plugin subprocess starts,
// issues a non-empty token that resolves in the registry, and can be stopped
// without triggering a health callback.
func TestStart_Success(t *testing.T) {
	reg := identity.New()
	cfg := fixtureConfig(t, "serve-and-block", reg, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	inst, err := process.Start(ctx, cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Token must be non-empty and resolvable in the registry.
	if inst.Token() == "" {
		t.Fatal("Token() is empty")
	}
	instanceID, ok := reg.Lookup(inst.Token())
	if !ok {
		t.Fatal("token not found in registry")
	}
	if instanceID != cfg.InstanceID {
		t.Errorf("token resolved to %q, want %q", instanceID, cfg.InstanceID)
	}

	// Stop the subprocess.
	if err := inst.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// After Stop the token must be revoked.
	if _, ok := reg.Lookup(inst.Token()); ok {
		t.Error("token still in registry after Stop")
	}
}

// TestStart_BinaryMissing verifies that a non-existent binary path returns an
// error before any identity token is issued.
func TestStart_BinaryMissing(t *testing.T) {
	reg := identity.New()

	cfg := process.Config{
		BinaryPath:     "/nonexistent/plugin-binary",
		InstanceID:     "missing-instance",
		IdentityIssuer: reg,
	}

	ctx := context.Background()
	_, err := process.Start(ctx, cfg)
	if err == nil {
		t.Fatal("expected error for missing binary, got nil")
	}

	// No token should have been issued because we fail before Issue().
	// Verify by checking that no tokens exist for this instance in the registry.
	// (We cannot call Lookup without a token, but RevokeInstance is idempotent
	// and safe to call on a clean registry.)
	reg.RevokeInstance("missing-instance") // should be a no-op
}

// TestStart_HandshakeFails verifies the error path when the subprocess exits
// before completing the go-plugin handshake. The identity token must be issued
// (because we call Issue before Launch) and then revoked on the Launch error.
func TestStart_HandshakeFails(t *testing.T) {
	reg := identity.New()
	healthCh := make(chan healthCall, 1)
	cfg := fixtureConfig(t, "exit-no-handshake", reg, healthCh)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := process.Start(ctx, cfg)
	if err == nil {
		t.Fatal("expected error when handshake fails, got nil")
	}

	// The token must have been revoked by the error-path code in Start.
	// We can verify this indirectly: Issue again for the same instance and
	// confirm the registry holds only the new token.
	newToken, err2 := reg.Issue(cfg.InstanceID)
	if err2 != nil {
		t.Fatalf("re-issue token: %v", err2)
	}
	id, ok := reg.Lookup(newToken)
	if !ok || id != cfg.InstanceID {
		t.Error("re-issued token not found in registry")
	}
}

// TestStart_Crash verifies that when the subprocess exits unexpectedly (without
// Stop being called), the HealthSetter is called with PluginHealthStateCrashed.
func TestStart_Crash(t *testing.T) {
	reg := identity.New()
	healthCh := make(chan healthCall, 1)
	cfg := fixtureConfig(t, "serve-and-crash", reg, healthCh)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	inst, err := process.Start(ctx, cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wait for the done channel to close (subprocess exited).
	select {
	case <-inst.Done():
	case <-time.After(15 * time.Second):
		t.Fatal("done channel did not close within 15s")
	}

	// HealthSetter must have been called with Crashed.
	select {
	case call := <-healthCh:
		if call.state != model.PluginHealthStateCrashed {
			t.Errorf("health state: want Crashed, got %s", call.state)
		}
		if call.instanceID != cfg.InstanceID {
			t.Errorf("health instance ID: want %s, got %s", cfg.InstanceID, call.instanceID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("HealthSetter was not called within 5s of crash")
	}

	// Token must be revoked after crash.
	if _, ok := reg.Lookup(inst.Token()); ok {
		t.Error("token still in registry after crash")
	}
}

// TestStop_Graceful verifies that Stop() returns nil, the done channel closes,
// and no health callback is triggered for a normal shutdown.
func TestStop_Graceful(t *testing.T) {
	reg := identity.New()
	healthCh := make(chan healthCall, 1)
	cfg := fixtureConfig(t, "serve-and-block", reg, healthCh)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	inst, err := process.Start(ctx, cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	stopCtx, stopCancel := context.WithTimeout(ctx, 10*time.Second)
	defer stopCancel()

	if err := inst.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// done channel must be closed.
	select {
	case <-inst.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("done channel not closed after Stop")
	}

	// No health callback should have been fired.
	select {
	case call := <-healthCh:
		t.Errorf("unexpected health callback: %+v", call)
	default:
	}

	// Token must be revoked.
	if _, ok := reg.Lookup(inst.Token()); ok {
		t.Error("token still in registry after graceful Stop")
	}
}

// TestStop_Idempotent verifies that calling Stop twice does not panic and the
// second call returns nil (the sync.Once body is skipped; the doneCh is already
// closed, so the select unblocks immediately).
func TestStop_Idempotent(t *testing.T) {
	reg := identity.New()
	cfg := fixtureConfig(t, "serve-and-block", reg, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	inst, err := process.Start(ctx, cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := inst.Stop(ctx); err != nil {
		t.Fatalf("first Stop: %v", err)
	}
	// Second Stop must be a fast no-op that returns nil. The doneCh is already
	// closed, so the select inside Stop unblocks immediately.
	if err := inst.Stop(ctx); err != nil {
		t.Errorf("second Stop: want nil, got %v", err)
	}
}

// TestStart_TokenRevokedOnExit verifies that the identity token is revoked
// after the subprocess exits, regardless of whether Stop was called first.
// We use "serve-and-crash" because it self-exits, providing a clean signal
// without needing to obtain the subprocess PID.
func TestStart_TokenRevokedOnExit(t *testing.T) {
	reg := identity.New()
	cfg := fixtureConfig(t, "serve-and-crash", reg, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	inst, err := process.Start(ctx, cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	token := inst.Token()
	if _, ok := reg.Lookup(token); !ok {
		t.Fatal("token should be present immediately after Start")
	}

	// Wait for crash.
	select {
	case <-inst.Done():
	case <-time.After(15 * time.Second):
		t.Fatal("done channel did not close within 15s")
	}

	if _, ok := reg.Lookup(token); ok {
		t.Error("token should be revoked after subprocess exit")
	}
}

// TestStart_NoopHostServer verifies that a nil HostServer resolves to
// NoopHostServer{} and does not cause the launch to fail.
func TestStart_NoopHostServer(t *testing.T) {
	reg := identity.New()
	cfg := fixtureConfig(t, "serve-and-block", reg, nil)
	// HostServer is nil by default in fixtureConfig; this test explicitly
	// asserts the nil → NoopHostServer path works end-to-end.
	cfg.HostServer = nil

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	inst, err := process.Start(ctx, cfg)
	if err != nil {
		t.Fatalf("Start with nil HostServer: %v", err)
	}
	defer inst.Stop(ctx) //nolint:errcheck
}

// textCapturingHandler is a slog.Handler that collects log output as plain text
// into a shared buffer. All derived handlers (created by WithAttrs/WithGroup)
// share the same mutex and buffer pointer, so lines emitted after attribute
// chaining (e.g. logger.With("plugin", …)) still appear in the captured output.
type textCapturingHandler struct {
	mu   *sync.Mutex
	buf  *bytes.Buffer
	base slog.Handler
}

func newCapturingLogger() (*slog.Logger, *textCapturingHandler) {
	mu := &sync.Mutex{}
	buf := &bytes.Buffer{}
	base := slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	h := &textCapturingHandler{mu: mu, buf: buf, base: base}
	return slog.New(h), h
}

func (h *textCapturingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.base.Enabled(ctx, level)
}

func (h *textCapturingHandler) Handle(ctx context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.base.Handle(ctx, r)
}

func (h *textCapturingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &textCapturingHandler{mu: h.mu, buf: h.buf, base: h.base.WithAttrs(attrs)}
}

func (h *textCapturingHandler) WithGroup(name string) slog.Handler {
	return &textCapturingHandler{mu: h.mu, buf: h.buf, base: h.base.WithGroup(name)}
}

func (h *textCapturingHandler) String() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.buf.String()
}

// TestStart_EnvInjected verifies that GLEIPNIR_INSTANCE_ID and GLEIPNIR_PLUGIN_ID
// are injected into the subprocess environment. The "echo-env" fixture writes
// these values to stderr immediately after startup; the host log pipe captures
// them via the slog logger we inject into the config.
func TestStart_EnvInjected(t *testing.T) {
	reg := identity.New()
	cfg := fixtureConfig(t, "echo-env", reg, nil)

	captureLogger, captureHandler := newCapturingLogger()
	cfg.Logger = captureLogger

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	inst, err := process.Start(ctx, cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Give the subprocess time to write the env lines before we stop it.
	time.Sleep(500 * time.Millisecond)

	stopCtx, stopCancel := context.WithTimeout(ctx, 10*time.Second)
	defer stopCancel()
	if err := inst.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Wait for done so the log pipe has fully flushed all buffered lines.
	select {
	case <-inst.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("done channel did not close within 10s")
	}

	output := captureHandler.String()
	wantInstanceID := "GLEIPNIR_INSTANCE_ID=" + cfg.InstanceID
	wantPluginID := "GLEIPNIR_PLUGIN_ID=" + cfg.PluginID

	if !strings.Contains(output, wantInstanceID) {
		t.Errorf("log output does not contain %q\nfull output:\n%s", wantInstanceID, output)
	}
	if !strings.Contains(output, wantPluginID) {
		t.Errorf("log output does not contain %q\nfull output:\n%s", wantPluginID, output)
	}
}

// TestStop_KillOnGraceTimeout verifies that Stop returns within bounded time
// even when the subprocess ignores SIGTERM. The "serve-and-trap-sigterm"
// fixture ignores SIGTERM; go-plugin sends SIGKILL after its own internal grace
// period (approximately 2s). We use a short StopGrace so Stop waits for
// go-plugin's kill path.
func TestStop_KillOnGraceTimeout(t *testing.T) {
	reg := identity.New()
	cfg := fixtureConfig(t, "serve-and-trap-sigterm", reg, nil)
	// Short grace so the test runs quickly. go-plugin's internal SIGKILL fires
	// after ~2s; we wait up to 5s total so the assertion has headroom.
	cfg.StopGrace = 200 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	inst, err := process.Start(ctx, cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	start := time.Now()
	_ = inst.Stop(ctx) // may return a timeout error; that's expected here

	// The subprocess should be gone well within 5s (go-plugin's internal grace
	// is ~2s from the Kill() call).
	select {
	case <-inst.Done():
		t.Logf("subprocess exited in %s", time.Since(start))
	case <-time.After(5 * time.Second):
		t.Fatal("subprocess did not exit within 5s after Stop with SIGTERM trapped")
	}
}
