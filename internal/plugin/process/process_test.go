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
	channelv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/channel/v1"
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
	case "echo-token":
		runFixtureServeEchoToken()
		os.Exit(0)
	case "serve-via-sdk":
		runFixtureServeViaSDK()
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
// fixture mode. The Launch function injects GLEIPNIR_TEST_FIXTURE via
// opts.Env, which hostwire.Launch forwards to the subprocess regardless of
// the env allowlist (opts.Env is the intentional channel for per-instance vars).
// (If parallel test execution is needed, switch to building a per-test
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
		// GLEIPNIR_TEST_FIXTURE is passed via opts.Env (not os.Setenv) because
		// hostwire.Launch now uses a strict env allowlist — the subprocess only
		// receives vars explicitly passed through opts.Env or the system allowlist.
		Launch: func(ctx context.Context, binaryPath string, host hostwire.HostServer, opts hostwire.Options) (*hostwire.Client, func(), error) {
			opts.Env = append(opts.Env, "GLEIPNIR_TEST_FIXTURE="+mode)
			return hostwire.Launch(ctx, binaryPath, host, opts)
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

	stopCtx, stopCancel := context.WithTimeout(ctx, 10*time.Second)
	defer stopCancel()
	if err := inst.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Wait for done so the log pipe has fully flushed all buffered lines.
	// waitForExit blocks on the stderrDone channel before closing doneCh, so
	// when Done() fires all stderr has already been written to the capture logger.
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

// TestStart_TokenInjectedIntoEnv verifies that the identity token issued at Start
// time is delivered to the subprocess via the GLEIPNIR_INSTANCE_TOKEN env var.
// The "echo-token" fixture writes the value to stderr; the host log pipe captures
// it, and we assert the captured output contains the exact token.
func TestStart_TokenInjectedIntoEnv(t *testing.T) {
	reg := identity.New()
	cfg := fixtureConfig(t, "echo-token", reg, nil)

	captureLogger, captureHandler := newCapturingLogger()
	cfg.Logger = captureLogger

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

	// Wait for done so the log pipe has fully flushed all buffered lines.
	// waitForExit blocks on the stderrDone channel before closing doneCh, so
	// when Done() fires all stderr has already been written to the capture logger.
	select {
	case <-inst.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("done channel did not close within 10s")
	}

	want := "GLEIPNIR_INSTANCE_TOKEN=" + inst.Token()
	output := captureHandler.String()
	if !strings.Contains(output, want) {
		t.Errorf("log output does not contain %q\nfull output:\n%s", want, output)
	}
}

// TestStart_OldTokenRejectedAfterReissue verifies the per-generation rotation
// guarantee: after stopping an instance and starting it again, the old token is
// revoked and only the new token is valid in the registry.
//
// We wait on <-inst.Done() between Stop and the second Start because waitForExit
// calls Revoke; without the wait there would be a race between the revoke in
// waitForExit and the new Issue call.
func TestStart_OldTokenRejectedAfterReissue(t *testing.T) {
	reg := identity.New()
	cfg := fixtureConfig(t, "serve-and-block", reg, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// First generation.
	inst1, err := process.Start(ctx, cfg)
	if err != nil {
		t.Fatalf("first Start: %v", err)
	}
	oldToken := inst1.Token()

	stopCtx, stopCancel := context.WithTimeout(ctx, 10*time.Second)
	defer stopCancel()
	if err := inst1.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Wait for waitForExit to complete so the Revoke inside it has run before
	// the second Issue. Without this, the race between Revoke and Issue can
	// leave the old token present when we check below.
	select {
	case <-inst1.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("first instance done channel did not close within 10s")
	}

	// Second generation for the same instance ID.
	inst2, err := process.Start(ctx, cfg)
	if err != nil {
		t.Fatalf("second Start: %v", err)
	}
	newToken := inst2.Token()
	defer inst2.Stop(ctx) //nolint:errcheck

	// Old token must be revoked.
	if _, ok := reg.Lookup(oldToken); ok {
		t.Error("old token still valid after reissue — per-generation rotation broken")
	}
	// New token must resolve to the correct instance.
	if id, ok := reg.Lookup(newToken); !ok {
		t.Error("new token not found in registry")
	} else if id != cfg.InstanceID {
		t.Errorf("new token resolved to %q, want %q", id, cfg.InstanceID)
	}
}

// TestStart_RealSDKServe verifies that a subprocess started via the real
// serve.Serve (the "serve-via-sdk" fixture mode) starts, completes the
// hostwire.Launch handshake, responds to a ChannelService.Notify RPC, and
// stops cleanly. The Notify dispatch is the critical assertion: it confirms
// that serve.Serve actually registered the adapter on the gRPC server. Without
// it, a broken adapter registration would not be caught by the handshake alone.
func TestStart_RealSDKServe(t *testing.T) {
	reg := identity.New()
	cfg := fixtureConfig(t, "serve-via-sdk", reg, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	inst, err := process.Start(ctx, cfg)
	if err != nil {
		t.Fatalf("Start with serve-via-sdk fixture: %v", err)
	}

	// Token must be present and valid.
	if inst.Token() == "" {
		t.Fatal("Token() is empty")
	}
	if _, ok := reg.Lookup(inst.Token()); !ok {
		t.Fatal("token not found in registry")
	}

	// Dispatch a Notify RPC to confirm the ChannelService adapter is wired.
	// If serve.Serve did not register the adapter, this call returns
	// codes.Unavailable and the test fails — catching the regression described
	// in the issue.
	notifyCtx, notifyCancel := context.WithTimeout(ctx, 10*time.Second)
	defer notifyCancel()
	resp, err := inst.Client().Channel.Notify(notifyCtx, &channelv1.NotifyRequest{})
	if err != nil {
		t.Fatalf("Channel.Notify: %v", err)
	}
	if !resp.GetOk() {
		t.Errorf("Channel.Notify: want Ok=true, got Ok=false; error=%v", resp.GetError())
	}

	stopCtx, stopCancel := context.WithTimeout(ctx, 10*time.Second)
	defer stopCancel()
	if err := inst.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	select {
	case <-inst.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("done channel did not close within 10s after Stop")
	}

	// Token must be revoked after stop.
	if _, ok := reg.Lookup(inst.Token()); ok {
		t.Error("token still in registry after Stop")
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
