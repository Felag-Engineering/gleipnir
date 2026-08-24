package events_test

import (
	"go/build"
	"strings"
	"testing"
)

// TestNoForbiddenImports enforces the dependency-direction inversion this
// package's doc describes: internal/plugin/events declares its own Event and
// Sink types and must never import internal/plugin/trigger,
// internal/plugin/hostsvc, or any plugin-sdk/gen package — the adapter that
// bridges the two directions (trigger/listen_sink.go) lives on the trigger
// side precisely so this package never needs to know trigger exists.
//
// Mirrors internal/plugin/caphealth's TestNoMCPImport in shape
// (build.ImportDir over production imports only, so a _test.go file's fakes
// are free to satisfy interfaces from a forbidden package without this
// package acquiring the real dependency).
func TestNoForbiddenImports(t *testing.T) {
	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatalf("build.ImportDir: %v", err)
	}

	forbidden := []string{
		"github.com/felag-engineering/gleipnir/internal/plugin/trigger",
		"github.com/felag-engineering/gleipnir/internal/plugin/hostsvc",
		"github.com/felag-engineering/gleipnir/plugin-sdk/gen",
	}

	for _, imp := range pkg.Imports {
		for _, forbid := range forbidden {
			if imp == forbid || strings.HasPrefix(imp, forbid+"/") {
				t.Errorf("production import %q: internal/plugin/events must not import %q", imp, forbid)
			}
		}
	}
}
