package assembly

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/felag-engineering/gleipnir/internal/admin"
	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/plugin/container"
	"github.com/felag-engineering/gleipnir/internal/plugin/lifecycle"
	"github.com/felag-engineering/gleipnir/internal/plugin/loader"
)

// bundleInstaller is what the admin upload route and the directory watcher
// both install through: the OCI installer behind lifecycle's adapter.
type bundleInstaller interface {
	admin.PluginInstaller
	OnInstalled(func(ctx context.Context, pluginID string))
}

// composeInstaller builds the v2 install path.
//
// It is the v1 Installer's verifier, TOFU pin check, audit writer and
// transaction helper (ADR-045 is unchanged) with the image half added. images
// is the runtime as the assembly holds it, so in manual posture the read-only
// wrapper refuses the image load (container.ErrManualModeWrite) and the install
// is recorded without one, rather than the installer deciding to stay off the
// socket by itself.
func composeInstaller(deps Deps, q *db.Queries, images container.ImageRuntime) bundleInstaller {
	if deps.Config.AllowUnsignedPlugins {
		slog.Warn("GLEIPNIR_ALLOW_UNSIGNED_PLUGINS=true: unsigned plugins will load; "+
			"every load emits a high-severity audit event. Signed plugins are "+
			"still fully verified.",
			"signature_verification", "disabled")
	}
	verifier := &loader.Verifier{AllowUnsigned: deps.Config.AllowUnsignedPlugins}
	base := loader.NewInstaller(verifier, q, deps.Store.DB(), deps.Publisher, deps.Config.PluginsDir)
	return lifecycle.NewOCIInstallerAdapter(loader.NewOCIInstaller(base, images))
}

// startWatcher registers the plugins directory with fsnotify synchronously, so
// a bad directory is an error rather than a silently absent watcher, then runs
// the event loop on the assembly's WaitGroup. The loop ends when a.ctx is
// cancelled, which Shutdown does before joining bg.
func (a *Assembly) startWatcher() error {
	if a.watcher == nil {
		return nil
	}
	fw, err := a.watcher.Setup()
	if err != nil {
		return fmt.Errorf("starting plugin bundle watcher: %w", err)
	}
	a.goBackground(func(ctx context.Context) {
		if err := a.watcher.Run(ctx, fw); err != nil && err != ctx.Err() {
			slog.Error("plugin bundle watcher exited with error", "err", err)
		}
	})
	return nil
}
