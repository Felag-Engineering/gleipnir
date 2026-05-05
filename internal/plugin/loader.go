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
//
// The fsnotify watcher (#187), material-change detection (#189), and the
// generation manager (#190-impl, #193-impl) land in follow-up PRs and consume
// the Verifier built here.
package plugin

import (
	"context"
	"log/slog"

	"github.com/felag-engineering/gleipnir/internal/infra/config"
)

// Loader owns plugin-subsystem initialization. It is the host's entry point
// to anything plugin-related; everything else in this package is reached
// through it (or through types it constructs).
type Loader struct {
	verifier *Verifier
}

func NewLoader() *Loader { return &Loader{} }

// Verifier returns the verifier configured at Init time. nil when the plugin
// subsystem is disabled. Callers downstream of Init (the fsnotify watcher,
// the install handler) read it to verify bundles.
func (l *Loader) Verifier() *Verifier { return l.verifier }

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
