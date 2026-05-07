package process

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/felag-engineering/gleipnir/internal/infra/logctx"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/plugin-sdk/hostwire"
)

// defaultStartupTimeout is how long we wait for the go-plugin handshake to
// complete before giving up. Operators can override this via Config.StartupTimeout.
const defaultStartupTimeout = 10 * time.Second

// defaultStopGrace is how long Stop waits for the subprocess to exit after
// calling teardown before reporting a timeout. Operators can override this via
// Config.StopGrace.
const defaultStopGrace = 10 * time.Second

// IdentityIssuer issues and revokes per-instance cryptographic tokens. The
// *identity.Registry from internal/plugin/identity satisfies this interface.
type IdentityIssuer interface {
	Issue(instanceID string) (string, error)
	Revoke(token string)
}

// LaunchFunc is the signature of the function used to start a plugin subprocess.
// The default is hostwire.Launch; tests inject a stub to avoid real subprocess
// spawning.
type LaunchFunc func(ctx context.Context, binaryPath string, host hostwire.HostServer, opts hostwire.Options) (*hostwire.Client, func(), error)

// Config holds all parameters needed to start one plugin subprocess instance.
type Config struct {
	// BinaryPath is the absolute path to the plugin executable. Start will
	// verify the file exists and is executable before calling Launch.
	BinaryPath string

	// InstanceID is the stable, DB-assigned ID for this plugin instance.
	InstanceID string

	// PluginID is used only for log labels; it does not affect behaviour.
	PluginID string

	// InstanceName is used only for log labels.
	InstanceName string

	// StartupTimeout is passed to hostwire.Options.StartupTimeout. If zero,
	// defaults to defaultStartupTimeout.
	StartupTimeout time.Duration

	// StopGrace is how long Stop waits for the subprocess to exit after calling
	// teardown. If zero, defaults to defaultStopGrace.
	StopGrace time.Duration

	// IdentityIssuer mints and revokes tokens. Required.
	IdentityIssuer IdentityIssuer

	// HealthSetter is called when a subprocess exits unexpectedly (crash). May
	// be nil if the caller does not need health-state callbacks.
	HealthSetter func(ctx context.Context, instanceID string, target model.PluginHealthState, detail string)

	// HostServer registers the host-side gRPC service on the broker-allocated
	// server. If nil, NoopHostServer{} is used so the handshake still completes.
	// #292 will inject hostsvc.Server here.
	HostServer hostwire.HostServer

	// Logger is the base logger for this instance. If nil, logctx.Logger(ctx)
	// is used at Start time.
	Logger *slog.Logger

	// Launch is the function used to start the subprocess. If nil, defaults to
	// hostwire.Launch. Tests inject a stub here.
	Launch LaunchFunc
}

// Instance represents a running plugin subprocess. Obtain one via Start.
//
// Stop may be called from any goroutine; it is idempotent (subsequent calls are
// no-ops thanks to sync.Once).
type Instance struct {
	cfg           Config
	token         string
	client        *hostwire.Client
	teardown      func()
	stderrW       io.WriteCloser // closed by Stop (or on Launch error) to signal EOF to scanner
	doneCh        chan struct{}
	stopRequested atomic.Bool
	stopOnce      sync.Once
	logger        *slog.Logger
}

// Start validates cfg, issues an identity token, launches the plugin subprocess
// via hostwire.Launch (or cfg.Launch if set), and starts the goroutine that
// watches for subprocess exit.
//
// On any error the identity token (if already issued) is revoked before
// returning so the registry stays clean.
func Start(ctx context.Context, cfg Config) (*Instance, error) {
	// Validate the binary before issuing a token so we can return early without
	// a registry entry to clean up.
	if err := validateBinary(cfg.BinaryPath); err != nil {
		return nil, fmt.Errorf("plugin %s: %w", cfg.InstanceID, err)
	}

	if cfg.StartupTimeout == 0 {
		cfg.StartupTimeout = defaultStartupTimeout
	}
	if cfg.StopGrace == 0 {
		cfg.StopGrace = defaultStopGrace
	}

	// Issue the identity token. The registry auto-revokes any prior token for
	// this instance so a killed generation cannot impersonate the new one.
	token, err := cfg.IdentityIssuer.Issue(cfg.InstanceID)
	if err != nil {
		return nil, fmt.Errorf("plugin %s: issue identity token: %w", cfg.InstanceID, err)
	}

	// Build the per-instance logger with stable labels so every log line from
	// this instance is identifiable without inspecting message content.
	logger := cfg.Logger
	if logger == nil {
		logger = logctx.Logger(ctx)
	}
	logger = logger.With("plugin", cfg.PluginID, "instance", cfg.InstanceID)

	// go-plugin reserves stdout for the handshake magic-cookie line; piping or
	// reading stdout here would corrupt the protocol. We only pipe stderr, which
	// is the agreed log channel per spec §13.
	stderrW, stderrDone := PipeLines(logger, slog.LevelWarn, "stderr")

	host := cfg.HostServer
	if host == nil {
		host = NoopHostServer{}
	}

	launchFn := cfg.Launch
	if launchFn == nil {
		launchFn = hostwire.Launch
	}

	// onProcessExited closes the write end of the stderr pipe when go-plugin
	// detects that the subprocess has exited and stopped writing. This fires
	// for both graceful stops (after Kill()) and crashes (subprocess self-exit),
	// giving the scanner goroutine an EOF so waitForExit can run.
	onProcessExited := func() { _ = stderrW.Close() }

	client, teardown, err := launchFn(ctx, cfg.BinaryPath, host, hostwire.Options{
		Stderr:         stderrW,
		StartupTimeout: cfg.StartupTimeout,
		Logger:         logger,
		OnProcessExited: onProcessExited,
		// Inject per-instance env vars so the plugin binary knows its own
		// identity. GLEIPNIR_INSTANCE_ID and GLEIPNIR_PLUGIN_ID are the two
		// required vars per the acceptance criteria for #291.
		Env: []string{
			"GLEIPNIR_INSTANCE_ID=" + cfg.InstanceID,
			"GLEIPNIR_PLUGIN_ID=" + cfg.PluginID,
		},
	})
	if err != nil {
		// Revoke the token because no subprocess is running to use it.
		cfg.IdentityIssuer.Revoke(token)
		closeWriterSilently(stderrW)
		return nil, fmt.Errorf("plugin %s: launch subprocess: %w", cfg.InstanceID, err)
	}

	inst := &Instance{
		cfg:      cfg,
		token:    token,
		client:   client,
		teardown: teardown,
		stderrW:  stderrW,
		doneCh:   make(chan struct{}),
		logger:   logger,
	}

	// waitForExit monitors stderr EOF as the crash-detection signal (best-effort
	// for #291; #292 will upgrade to go-plugin's Client.Exited() polling).
	go inst.waitForExit(ctx, stderrDone)

	return inst, nil
}

// Stop shuts down the subprocess. It is safe to call concurrently; subsequent
// calls after the first are no-ops.
//
// Stop sets the stopRequested flag before calling teardown so the exit-watching
// goroutine can distinguish a graceful stop from a crash. teardown calls
// go-plugin's Kill, which sends SIGKILL (go-plugin handles the SIGTERM→SIGKILL
// sequence internally with its own grace period).
//
// After teardown, Stop waits up to cfg.StopGrace for the done channel to close.
// If the timeout expires it returns a descriptive error; the caller is
// responsible for deciding whether to log and continue or escalate.
//
// The identity token is revoked on every Stop call path (both the normal path
// and the timeout path) so the registry does not leak entries.
func (i *Instance) Stop(ctx context.Context) error {
	i.stopOnce.Do(func() {
		// stopRequested must be stored before teardown so waitForExit sees the
		// flag when it re-reads after stderrDone fires (reviewer correction §5).
		i.stopRequested.Store(true)
		i.teardown()
		// Close the write end of the stderr pipe so the scanner goroutine gets
		// EOF and the waitForExit goroutine unblocks. go-plugin copies subprocess
		// stderr via io.Copy but does not call Close on our writer after the
		// subprocess exits; we close it ourselves here. io.PipeWriter.Close is
		// idempotent: a second call returns ErrClosedPipe which we discard.
		_ = i.stderrW.Close()
	})

	// Always revoke; identity.Revoke is idempotent.
	defer i.cfg.IdentityIssuer.Revoke(i.token)

	select {
	case <-i.doneCh:
		return nil
	case <-time.After(i.cfg.StopGrace):
		return fmt.Errorf("plugin %s: did not exit within %s", i.cfg.InstanceID, i.cfg.StopGrace)
	case <-ctx.Done():
		return fmt.Errorf("plugin %s: stop cancelled: %w", i.cfg.InstanceID, ctx.Err())
	}
}

// InstanceID returns the stable instance ID this Instance was started with.
func (i *Instance) InstanceID() string { return i.cfg.InstanceID }

// Token returns the identity token issued at Start time. #292 uses this to
// deliver the token to the plugin via Bootstrap.Bind.
func (i *Instance) Token() string { return i.token }

// Client returns the typed gRPC client set for making host→plugin RPCs.
// #292's dispatcher will call Client() to access Bootstrap.Bind and Tool RPCs.
func (i *Instance) Client() *hostwire.Client { return i.client }

// Done returns a channel that is closed when the subprocess has exited (either
// via Stop or a crash). Callers can select on this channel.
func (i *Instance) Done() <-chan struct{} { return i.doneCh }

// waitForExit blocks until stderrDone fires (signalling that the subprocess
// closed its stderr pipe), then determines whether the exit was requested or a
// crash, and updates health state / revokes token accordingly.
//
// Crash detection via stderr EOF is a documented best-effort signal for #291.
// A plugin that explicitly closes stderr while still running will be
// misclassified as crashed, but the spec contract is that plugins must not
// close stderr. #292 will replace this seam with go-plugin's Client.Exited()
// once we hold the *plugin.Client directly.
func (i *Instance) waitForExit(ctx context.Context, stderrDone <-chan struct{}) {
	<-stderrDone

	// Re-read stopRequested AFTER the channel fires so we see the Store that
	// happened before teardown (reviewer correction §5: happens-before ordering).
	if !i.stopRequested.Load() {
		i.logger.Warn("plugin subprocess exited unexpectedly", "instance_id", i.cfg.InstanceID)

		if i.cfg.HealthSetter != nil {
			i.cfg.HealthSetter(ctx, i.cfg.InstanceID,
				model.PluginHealthStateCrashed,
				"subprocess exited unexpectedly")
		}
	}

	// Revoke the token regardless of whether the exit was requested or a crash.
	// identity.Revoke is idempotent, so this is safe even if Stop already called
	// it via the defer in Stop().
	i.cfg.IdentityIssuer.Revoke(i.token)

	close(i.doneCh)
}

// validateBinary checks that binaryPath exists, is a regular file, and has
// at least one executable bit set. This catches misconfigured paths before we
// issue an identity token.
func validateBinary(binaryPath string) error {
	info, err := os.Stat(binaryPath)
	if err != nil {
		return fmt.Errorf("binary not found at %q: %w", binaryPath, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("binary path %q is not a regular file", binaryPath)
	}
	// Any executable bit (owner, group, or other) is sufficient; the OS will
	// enforce the actual permission check when we exec the file.
	if info.Mode()&0o111 == 0 {
		return fmt.Errorf("binary %q is not executable (mode %v)", binaryPath, info.Mode())
	}
	return nil
}

// closeWriterSilently closes w, discarding any error. Used on the Launch error
// path where we only need to release the pipe resources.
func closeWriterSilently(w io.WriteCloser) {
	_ = w.Close()
}
