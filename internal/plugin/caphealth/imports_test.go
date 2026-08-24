package caphealth_test

import (
	"go/build"
	"strings"
	"testing"
)

// TestNoMCPImport enforces the package boundary internal/plugin/events'
// discoverprobe.go exists to preserve: internal/plugin/caphealth models
// manifest-vs-discovery drift as a capability fault (DriftDetail,
// applyEventDrift, the DiscoverProbe interface) without ever needing to know
// how a `server/discover` round trip is actually made. internal/mcp is that
// how, and internal/plugin/events -- not this package -- is where the client
// that talks to it lives. Checking PRODUCTION imports only (go/build.ImportDir
// separates Imports from TestImports/XTestImports on its own), so a fake
// DiscoverProbe in a _test.go file is free to satisfy the interface without
// this package acquiring the real dependency.
//
// Mirrors internal/schemanorm's TestNoInternalImports in shape (build.ImportDir
// over production imports), scoped to the one import this package must never
// acquire rather than to "no internal imports at all" -- caphealth is not a
// leaf package the way schemanorm is; it legitimately imports internal/model
// and internal/plugin/state.
func TestNoMCPImport(t *testing.T) {
	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatalf("build.ImportDir: %v", err)
	}

	const mcpImport = "github.com/felag-engineering/gleipnir/internal/mcp"
	for _, imp := range pkg.Imports {
		if imp == mcpImport || strings.HasPrefix(imp, mcpImport+"/") {
			t.Errorf("production import %q: internal/plugin/caphealth must not import internal/mcp", imp)
		}
	}
}
