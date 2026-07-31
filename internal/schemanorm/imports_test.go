package schemanorm_test

import (
	"go/build"
	"strings"
	"testing"
)

// TestNoInternalImports enforces the leaf-package boundary at CI time: this
// package's PRODUCTION imports (not test imports -- go/build.ImportDir
// separates Imports from TestImports/XTestImports on its own) may ONLY be
// the Go standard library, matching the package doc's "Scope" section
// verbatim: "stdlib only, no exceptions". It must never import any
// github.com/felag-engineering/gleipnir/internal/* package, and it must
// never import github.com/santhosh-tekuri/jsonschema/v6 either -- that
// dependency belongs solely to the randomized property test
// (property_test.go), which compiles both the raw and normalized form of
// generated schemas and validates probe instances against both, to prove
// Normalize never changes what a schema accepts. That test lives in this
// same external "schemanorm_test" package, so its import shows up in
// go/build.ImportDir's XTestImports, never in the Imports this test checks;
// this test would fail the moment jsonschema/v6 (or anything else non-stdlib)
// leaked into a production .go file.
func TestNoInternalImports(t *testing.T) {
	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatalf("build.ImportDir: %v", err)
	}

	const internalPrefix = "github.com/felag-engineering/gleipnir/"

	for _, imp := range pkg.Imports {
		if strings.HasPrefix(imp, internalPrefix) {
			t.Errorf("production import %q: internal/schemanorm must not import any github.com/felag-engineering/gleipnir package", imp)
			continue
		}

		isStdlib := !strings.Contains(strings.SplitN(imp, "/", 2)[0], ".")
		if !isStdlib {
			t.Errorf("production import %q: internal/schemanorm may only import the Go standard library", imp)
		}
	}
}
