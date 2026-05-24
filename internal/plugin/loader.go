// Package plugin is the host-side plugin loader.
//
// The Phase 3 loader is being assembled across several PRs. Today's surface:
//
//   - GLEIPNIR_PLUGINS_ENABLED gates the subsystem on/off; when off, Init is a
//     fast no-op and nothing else in this package is touched at runtime.
//   - When enabled, Init constructs a Verifier configured by the
//     GLEIPNIR_ALLOW_UNSIGNED_PLUGINS toggle and logs a permissive-mode banner
//     if applicable. The verifier itself (verify.go) is fully wired into
//     plugin-sdk/signing — single source of truth for the Minisign format.
//   - StartWatcher (#187, this PR) sets up the fsnotify watcher and runs the
//     debounced install loop. Material-change detection (#189) and the
//     generation/shutdown manager (#190/#193 implementations) are follow-up PRs.
//   - StartManager (#291, #295) constructs the process.Manager and calls
//     StartAllActive so existing plugin instances are re-spawned on server
//     restart. No-op when GLEIPNIR_PLUGINS_ENABLED=false.
package plugin

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"google.golang.org/grpc"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/infra/config"
	"github.com/felag-engineering/gleipnir/internal/infra/event"
	"github.com/felag-engineering/gleipnir/internal/plugin/generation"
	"github.com/felag-engineering/gleipnir/internal/plugin/identity"
	"github.com/felag-engineering/gleipnir/internal/plugin/loader"
	"github.com/felag-engineering/gleipnir/internal/plugin/process"
	"github.com/felag-engineering/gleipnir/plugin-sdk/hostwire"
)

// Loader owns plugin-subsystem initialization. It is the host's entry point
// to anything plugin-related; everything else in this package is reached
// through it (or through types it constructs).
type Loader struct {
	verifier  *Verifier
	installer *loader.Installer
	watcher   *loader.Watcher
	manager   *process.Manager
}

func NewLoader() *Loader { return &Loader{} }

// Manager returns the process.Manager constructed by StartManager. Returns nil
// when the plugin subsystem is disabled or StartManager has not been called.
func (l *Loader) Manager() *process.Manager { return l.manager }

// Verifier returns the verifier configured at Init time. nil when the plugin
// subsystem is disabled. Callers downstream of Init (the fsnotify watcher,
// the install handler) read it to verify bundles.
func (l *Loader) Verifier() *Verifier { return l.verifier }

// Installer returns the shared Installer created by StartWatcher. Returns nil
// when the plugin subsystem is disabled (Init was not called or StartWatcher
// has not run). Mirrors the Verifier() nil-return pattern.
func (l *Loader) Installer() *loader.Installer { return l.installer }

// Init wires up the plugin subsystem. When cfg.PluginsEnabled is false it
// returns nil immediately and leaves Verifier() nil. When true it builds the
// Verifier and logs the permissive-mode warning if
// GLEIPNIR_ALLOW_UNSIGNED_PLUGINS is set.
func (l *Loader) Init(_ context.Context, cfg config.Config) error {
	if !cfg.PluginsEnabled {
		slog.Default().Debug("plugin loader disabled")
		return nil
	}

	l.verifier = &Verifier{AllowUnsigned: cfg.AllowUnsignedPlugins}

	if cfg.AllowUnsignedPlugins {
		// Loud, persistent warning at startup. The companion surfaces
		// (admin UI banner, /api/v1/health field) ensure the operator
		// also sees this at runtime.
		slog.Default().Warn(
			"GLEIPNIR_ALLOW_UNSIGNED_PLUGINS=true: unsigned plugins will load; "+
				"every load emits a high-severity audit event. Signed plugins are "+
				"still fully verified. See ADR-045.",
			"signature_verification", "disabled",
		)
	} else {
		slog.Default().Info("plugin loader enabled (signature verification active)")
	}
	return nil
}

// StartWatcher wires the fsnotify watcher for the given plugins directory and
// starts the event loop in a goroutine. q must be non-nil; dir is the watch
// path (typically cfg.PluginsDir).
//
// The pre-flight — creating the watch directory and registering it with
// fsnotify — runs synchronously before spawning any goroutine. If pre-flight
// fails, StartWatcher returns an error so main.go can treat it as fatal,
// matching the pattern used by other subsystem starts (scheduler, poller,
// cronRunner).
//
// StartWatcher must be called after Init and after store.Migrate so the DB
// schema is ready for plugin rows and audit events. It is a no-op (returns nil)
// when Init was not called (i.e. when GLEIPNIR_PLUGINS_ENABLED=false) —
// l.verifier will be nil in that case and we return early.
//
// publisher is used to emit plugin.health_changed events when a pubkey mismatch
// transitions instances to pending_key_approval. It may be nil — events are
// skipped when nil.
//
// sqlDB is the raw handle the Installer uses to wrap row inserts and audit
// events in a single transaction (see internal/plugin/loader.NewInstaller).
// Required when plugins are enabled.
func (l *Loader) StartWatcher(ctx context.Context, q *db.Queries, sqlDB *sql.DB, dir string, publisher event.Publisher) error {
	if l.verifier == nil {
		return nil
	}

	l.installer = loader.NewInstaller(&verifierAdapter{v: l.verifier}, q, sqlDB, publisher, dir)
	// The watcher's callback type is func(context.Context, string) error, but
	// Install now returns (string, error). The adapter discards the plugin ID —
	// the watcher only needs to know whether install succeeded.
	l.watcher = loader.NewWatcher(dir, func(ctx context.Context, p string) error {
		_, err := l.installer.Install(ctx, p)
		return err
	})

	fw, err := l.watcher.Setup()
	if err != nil {
		return err
	}

	go func() {
		if err := l.watcher.Run(ctx, fw); err != nil && err != ctx.Err() {
			slog.Default().Error("plugin watcher exited with error", "dir", dir, "err", err)
		} else {
			slog.Default().Debug("plugin watcher stopped", "dir", dir)
		}
	}()
	return nil
}

// StartManagerConfig wires Loader.StartManager to the host-side plugin substrate
// constructed in main.go. All fields are required when plugins are enabled.
type StartManagerConfig struct {
	Querier              *db.Queries
	Publisher            event.Publisher
	HostServer           hostwire.HostServer // shared *hostsvc.Server
	IdentityRegistry     *identity.Registry
	GenerationController *generation.Controller
	ServerInterceptors   []grpc.UnaryServerInterceptor
}

// StartManager constructs a process.Manager wired to the host-side substrate
// supplied in cfg and calls StartAllActive so that any plugin instances whose
// plugin row has status="active" are spawned on server startup.
//
// Returns nil immediately when the plugin subsystem is disabled (Init was not
// called or GLEIPNIR_PLUGINS_ENABLED=false). Any per-instance start errors are
// logged at Warn inside Manager.StartAllActive and do not propagate here.
//
// The IdentityRegistry must be the same registry backing the token interceptor
// so that the per-generation rotation guarantee (one registry, one source of
// truth) holds. main.go owns this registry and passes it to both the interceptor
// chain and StartManager.
func (l *Loader) StartManager(ctx context.Context, cfg StartManagerConfig) error {
	if l.verifier == nil {
		return nil
	}

	if cfg.IdentityRegistry == nil {
		return fmt.Errorf("StartManager: IdentityRegistry must not be nil")
	}
	if cfg.GenerationController == nil {
		return fmt.Errorf("StartManager: GenerationController must not be nil")
	}
	if cfg.HostServer == nil {
		return fmt.Errorf("StartManager: HostServer must not be nil")
	}
	if cfg.ServerInterceptors == nil {
		return fmt.Errorf("StartManager: ServerInterceptors must not be nil")
	}

	l.manager = process.NewManager(process.ManagerConfig{
		Querier:              cfg.Querier,
		Publisher:            cfg.Publisher,
		IdentityIssuer:       cfg.IdentityRegistry,
		GenerationController: cfg.GenerationController,
		// One shared server per host; per-instance routing via token interceptor.
		HostServerFor:      func(_ string) hostwire.HostServer { return cfg.HostServer },
		ServerInterceptors: cfg.ServerInterceptors,
	})

	return l.manager.StartAllActive(ctx)
}

// verifierAdapter bridges *Verifier (returns plugin.VerifyResult) to
// loader.BundleVerifier (returns loader.VerifyResult). The explicit switch
// insulates callers from any future iota reordering in either type.
type verifierAdapter struct{ v *Verifier }

func (a *verifierAdapter) VerifyBundle(bundleDir, binaryPath string) loader.VerifyResult {
	r := a.v.VerifyBundle(bundleDir, binaryPath)

	var outcome loader.VerifyOutcome
	switch r.Outcome {
	case OutcomeVerified:
		outcome = loader.OutcomeVerified
	case OutcomeUnsignedPermissive:
		outcome = loader.OutcomeUnsignedPermissive
	default: // OutcomeRejected or any future value
		outcome = loader.OutcomeRejected
	}

	return loader.VerifyResult{
		Outcome: outcome,
		Pubkey:  r.Pubkey,
		Err:     r.Err,
	}
}
