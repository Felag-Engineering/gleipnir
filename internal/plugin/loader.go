// Package plugin is the host-side plugin loader. The real loader lands in
// Phase 3 of the plugin system rollout (see docs/developer/plugin-system-spec.md).
// For this release the package exists only so the GLEIPNIR_PLUGINS_ENABLED
// flag has a concrete consumer; Init is a no-op when disabled and a logged
// stub when enabled.
package plugin

import (
	"context"
	"log/slog"

	"github.com/felag-engineering/gleipnir/internal/infra/config"
)

type Loader struct{}

func NewLoader() *Loader { return &Loader{} }

// Init wires up the plugin subsystem. It is a no-op stub today: when
// cfg.PluginsEnabled is false it returns nil immediately; when true it logs
// that the loader is enabled but does not actually discover or launch any
// plugins. The Phase 3 loader PR replaces this with the real implementation.
//
// TODO(#186): import github.com/felag-engineering/gleipnir/plugin-sdk/signing
// here for signature verification. plugin-sdk/signing is the single source of
// truth for the Minisign format — do not add a second implementation.
func (l *Loader) Init(ctx context.Context, cfg config.Config) error {
	if !cfg.PluginsEnabled {
		slog.Default().Debug("plugin loader disabled")
		return nil
	}
	slog.Default().Info("plugin loader enabled (stub — real loader pending)")
	return nil
}
