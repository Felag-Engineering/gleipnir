// Package lifecycle is the v2 install path's seam between what the loader
// verified and what the container substrate needs to converge toward
// (ADR-056, mcp-realignment-spec.md §7; issue #952).
//
// Two halves, one package because they are the two ends of the same wire:
//
//   - install.go adapts *loader.OCIInstaller (v1's Installer reused for
//     everything ADR-045 already settled, plus the OCI image half) to the
//     narrow BundleInstaller shape the fsnotify Watcher and the admin Install
//     handler both already speak — a bundle drop still ends in "a plugin row
//     exists or it does not", regardless of which substrate installed it.
//   - desired.go turns an installed v2 plugin plus a freshly created instance
//     into a plugin_containers desired-state row: the reconciler (built, not
//     yet wired to main.go) reads that table on its own schedule and never
//     sees an install or an instance directly.
//
// Both halves are reachable from the live v1 admin/loader code today (a nil
// InstanceProvisioner and the untouched v1 Installer keep v1 behaviour
// unchanged), but nothing here talks to a container runtime directly — that
// stays the reconciler's job once it is wired in.
package lifecycle

import (
	"context"

	"github.com/felag-engineering/gleipnir/internal/plugin/loader"
)

// ociInstallerAdapter narrows *loader.OCIInstaller's richer OCIInstallResult
// down to the plain (pluginID, error) shape that loader.BundleInstaller and
// admin.PluginInstaller both already declare. Its only job is discarding the
// digest/image-loaded detail those two callers never asked for — v1's
// *loader.Installer needs no such adapter because its Install method already
// has this exact signature.
type ociInstallerAdapter struct {
	inner *loader.OCIInstaller
}

// NewOCIInstallerAdapter wraps inner so it satisfies loader.BundleInstaller
// (for the watcher) and admin.PluginInstaller (for the manual "install via
// API" path) without either of those packages needing to know OCIInstaller
// exists.
func NewOCIInstallerAdapter(inner *loader.OCIInstaller) *ociInstallerAdapter {
	return &ociInstallerAdapter{inner: inner}
}

// Install runs the v2 pipeline and returns just the plugin ID, matching the
// BundleInstaller / admin.PluginInstaller contract.
func (a *ociInstallerAdapter) Install(ctx context.Context, tarPath string) (string, error) {
	result, err := a.inner.Install(ctx, tarPath)
	return result.PluginID, err
}
