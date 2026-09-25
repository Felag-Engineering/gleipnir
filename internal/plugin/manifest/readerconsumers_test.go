package manifest_test

import (
	"go/build"
	"path/filepath"
	"runtime"
	"testing"
)

// TestConsumersDoNotImportV1Manifest is the #950 DoD line: internal/plugin/oauth
// (host-side OAuth2 orchestration) and internal/plugin/hostendpoint (the
// Tier-2 gate) — the two subsystems #950 moves onto Read that are ALSO whole,
// single-purpose packages — must not import plugin-sdk/manifest directly in
// production code any more. The whole point of Read is that a call site which
// switches on version itself is a call site that will forget to when the
// next field is added.
//
// internal/admin is deliberately NOT in this list, even though its instance
// config/subscription-scope and OAuth/credentials handlers ARE migrated (see
// instance_config.go, plugin_oauth_handler.go, plugin_credentials_handler.go,
// and the two migrated call sites in plugin_handler.go): it is one package
// spanning both those migrated handlers AND plugin listing/detail display
// (services, SBOM) plus the accept-manifest flow, which calls
// internal/plugin/manifest/diff.go's ConfigSchemaNewlyRequiredFields — a
// do-not-touch, v1-only file per #950's own task list. A package-level import
// check cannot distinguish between files within the same package, so
// internal/admin legitimately keeps the plugin-sdk/manifest import as a
// whole. The migrated instance_config.go/plugin_oauth_handler.go/
// plugin_credentials_handler.go call sites are proven directly by their own
// tests instead (see TestInstanceConfig_PutConfig_V2Manifest and the oauth
// package's v2-manifest strategy-resolution tests).
//
// internal/plugin/configvalidate is also NOT in this list. ForChannelAudience,
// ValidateChannelCapabilities (audience/channel validation) and
// ForTriggerBinding (event-kind binding validation) all stay on
// plugin-sdk/manifest directly: audience/channel has no v2 shape in Snapshot
// yet, and routing event/trigger consumers through Read is #951's scope, not
// #950's. Only that package's SecretPropertyNames/OptionsAnnotations
// (schema-annotation helpers, now forwarding to plugin-sdk/manifestv2) and
// its ForInstanceConfig/ForSubscriptionScope moved onto the version-neutral
// types in #950; the package as a whole keeps the import for the functions
// that did not move.
//
// Checking PRODUCTION imports only (go/build.ImportDir separates Imports
// from TestImports/XTestImports on its own), so a test fixture built from the
// v1 types does not trip this check.
func TestConsumersDoNotImportV1Manifest(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller: could not resolve this file's own path")
	}
	// thisFile is .../internal/plugin/manifest/readerconsumers_test.go;
	// three levels up is the repo root.
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..", "..")

	const v1ManifestImport = "github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
	migratedDirs := []string{
		"internal/plugin/oauth",
		"internal/plugin/hostendpoint",
	}

	for _, rel := range migratedDirs {
		dir := filepath.Join(repoRoot, rel)
		pkg, err := build.ImportDir(dir, 0)
		if err != nil {
			t.Fatalf("build.ImportDir(%s): %v", rel, err)
		}
		for _, imp := range pkg.Imports {
			if imp == v1ManifestImport {
				t.Errorf("%s: production import %q — this subsystem must read manifests through internal/plugin/manifest.Read instead", rel, imp)
			}
		}
	}
}
