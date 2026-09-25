//go:build substratev2

package main

// plugins_v2.go is the substratev2 build's pluginSubsystem implementation: a
// thin adapter over the importable internal/plugin/assembly package (the v2
// substrate body — see that package's doc comment). Kept thin on purpose: the
// real composition lives in internal/plugin/assembly so test packages (the
// DooD suite, #985) can construct it directly, without linking package main.

import (
	"context"

	"github.com/felag-engineering/gleipnir/internal/db"
	runpkg "github.com/felag-engineering/gleipnir/internal/execution/run"
	"github.com/felag-engineering/gleipnir/internal/infra/config"
	"github.com/felag-engineering/gleipnir/internal/infra/event"
	"github.com/felag-engineering/gleipnir/internal/plugin/assembly"
	"github.com/felag-engineering/gleipnir/internal/settings"
	"github.com/felag-engineering/gleipnir/internal/toolregistry"
)

// startPluginSubsystem brings up the v2 plugin substrate and wraps it to
// satisfy pluginSubsystem.
func startPluginSubsystem(
	ctx context.Context,
	cfg config.Config,
	store *db.Store,
	broadcaster event.Publisher,
	encryptionKey []byte,
	arbiter *toolregistry.Registry,
	systemSettings *settings.Service,
) (pluginSubsystem, error) {
	a, err := assembly.New(ctx, assembly.Deps{
		Config:         cfg,
		Store:          store,
		Publisher:      broadcaster,
		EncryptionKey:  encryptionKey,
		Arbiter:        arbiter,
		SystemSettings: systemSettings,
	})
	if err != nil {
		return nil, err
	}
	return &v2PluginSubsystem{assembly: a}, nil
}

// v2PluginSubsystem adapts *assembly.Assembly to pluginSubsystem, converting
// between assembly's exported dependency types (importable by tests) and
// package main's unexported ones.
type v2PluginSubsystem struct {
	assembly *assembly.Assembly
}

func (s *v2PluginSubsystem) launcherDeps() launcherPluginDeps {
	d := s.assembly.LauncherDeps()
	return launcherPluginDeps{
		Registrar:          d.Registrar,
		Dispatcher:         d.Dispatcher,
		ApprovalDispatcher: d.ApprovalDispatcher,
		FeedbackDispatcher: d.FeedbackDispatcher,
		ToolClassifier:     d.ToolClassifier,
		ToolResolver:       d.ToolResolver,
		Canceller:          d.Canceller,
	}
}

func (s *v2PluginSubsystem) bindLauncher(ctx context.Context, launcher *runpkg.RunLauncher) {
	s.assembly.BindLauncher(ctx, launcher)
}

func (s *v2PluginSubsystem) adminDeps() adminPluginDeps {
	d := s.assembly.AdminDeps()
	return adminPluginDeps{
		Installer:          d.Installer,
		ProcMgr:            d.ProcMgr,
		Trigger:            d.Trigger,
		Inflight:           d.Inflight,
		Evictor:            d.Evictor,
		PluginsDir:         d.PluginsDir,
		RSSAggregator:      d.RSSAggregator,
		Unregistrar:        d.Unregistrar,
		OAuthHandler:       d.OAuthHandler,
		CredentialsHandler: d.CredentialsHandler,
		OptionsHandler:     d.OptionsHandler,
		CredentialSeeder:   d.CredentialSeeder,
		ManifestSnap:       d.ManifestSnap,
		OnPublicURLChanged: d.OnPublicURLChanged,
		CapabilityHealth:   d.CapabilityHealth,
	}
}

func (s *v2PluginSubsystem) quiesce()  { s.assembly.Quiesce() }
func (s *v2PluginSubsystem) shutdown() { s.assembly.Shutdown() }
