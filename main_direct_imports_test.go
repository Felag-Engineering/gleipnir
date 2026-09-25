package main

// main_direct_imports_test.go tightens the default-direction half of
// one-substrate enforcement. main_linkage_test.go's TestMainLinkage_NoV2Substrate
// only forbids internal/plugin/assembly from the untagged build's TRANSITIVE
// dependency closure — but the default binary already links reconciler,
// container, and egress transitively (through hostendpoint and loader, both
// of which know about both substrate eras), so that test alone would not
// catch someone starting v2 machinery directly from an untagged package-main
// file. These two tests close that gap:
//
//  1. TestMainDirectImports_NoV2Packages checks package main's own DIRECT
//     imports (not -deps) exclude the v2-only packages outright.
//  2. TestMainAST_NoV2SubstrateStarts is a source-level check: even without a
//     matching import, it parses every untagged package-main file and fails
//     on a reference to one of the specific calls that actually START v2
//     machinery (reconciler.New*, hostendpoint.NewListenerSet, any egress.*
//     call, loader.NewOCIInstaller) — the ones a well-meaning but wrong
//     refactor would add first, often via a helper that re-exports the
//     import rather than adding it to this file directly.
//
// Both were proven to fail against a deliberately injected reference to
// reconciler.New before being finalized (added a temporary
// `var _ = reconciler.New` behind an internal/plugin/reconciler import in a
// scratch file, confirmed both tests turned red, then reverted it) — see the
// #948 security review that asked for this proof.

import (
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestMainDirectImports_NoV2Packages(t *testing.T) {
	cmd := exec.Command("go", "list", "-f", `{{join .Imports "\n"}}`, ".")
	cmd.Env = append(os.Environ(), "GOWORK=off")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf(`go list -f '{{join .Imports "\n"}}' .: %v\n%s`, err, out)
	}
	imports := string(out)

	forbidden := []string{
		"github.com/felag-engineering/gleipnir/internal/plugin/reconciler",
		"github.com/felag-engineering/gleipnir/internal/plugin/container",
		"github.com/felag-engineering/gleipnir/internal/plugin/egress",
		"github.com/felag-engineering/gleipnir/internal/plugin/resources",
		"github.com/felag-engineering/gleipnir/internal/plugin/assembly",
	}
	for _, pkg := range forbidden {
		if containsDep(imports, pkg) {
			t.Errorf("package main must not directly import %s in the default build — v2 machinery must never be started from an untagged file", pkg)
		}
	}
}

// v2StartCall pins one call this test refuses to see reachable from an
// untagged package-main file. Each is a constructor (or, for egress, the
// whole package) that only makes sense once the v2 substrate is actually
// being started — unlike hostendpoint.AssertHostPlane or the plain
// *.Server{} skeleton, which both builds legitimately use.
type v2StartCall struct {
	importPath string
	// selector is the exact function/method name to forbid. Empty means any
	// selector on this import path is forbidden.
	selector string
	// prefix, when true, matches any selector name starting with selector
	// (reconciler.New / reconciler.NewSubnetAllocator / ... are all
	// v2-substrate constructors).
	prefix bool
}

var forbiddenV2Starts = []v2StartCall{
	{importPath: "github.com/felag-engineering/gleipnir/internal/plugin/reconciler", selector: "New", prefix: true},
	{importPath: "github.com/felag-engineering/gleipnir/internal/plugin/hostendpoint", selector: "NewListenerSet"},
	{importPath: "github.com/felag-engineering/gleipnir/internal/plugin/egress"},
	{importPath: "github.com/felag-engineering/gleipnir/internal/plugin/loader", selector: "NewOCIInstaller"},
}

// TestMainAST_NoV2SubstrateStarts parses every source file the untagged
// (default) build actually compiles for package main — resolved via
// go/build so a file gated behind //go:build substratev2 (plugins_v2.go,
// main_linkage_v2_test.go, boot_v2_test.go) is correctly excluded, exactly
// as the real build would exclude it — and fails if any of them references
// one of forbiddenV2Starts.
func TestMainAST_NoV2SubstrateStarts(t *testing.T) {
	pkg, err := build.Default.ImportDir(".", 0)
	if err != nil {
		t.Fatalf("build.ImportDir(.): %v", err)
	}

	var files []string
	files = append(files, pkg.GoFiles...)
	files = append(files, pkg.TestGoFiles...)
	files = append(files, pkg.XTestGoFiles...)

	for _, f := range files {
		checkFileForV2Starts(t, filepath.Join(pkg.Dir, f))
	}
}

func checkFileForV2Starts(t *testing.T, filename string) {
	t.Helper()
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, filename, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}

	// Map each file-local import identifier to its full import path, so an
	// aliased import (`import rec "…/reconciler"`) is still caught.
	importPathByName := map[string]string{}
	for _, imp := range node.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		name := path[strings.LastIndex(path, "/")+1:]
		if imp.Name != nil {
			name = imp.Name.Name
		}
		importPathByName[name] = path
	}

	ast.Inspect(node, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		importPath, ok := importPathByName[ident.Name]
		if !ok {
			return true
		}
		for _, forbidden := range forbiddenV2Starts {
			if importPath != forbidden.importPath {
				continue
			}
			switch {
			case forbidden.selector == "":
				t.Errorf("%s: %s.%s references %s — v2 machinery must never start from an untagged file",
					filename, ident.Name, sel.Sel.Name, importPath)
			case forbidden.prefix && strings.HasPrefix(sel.Sel.Name, forbidden.selector):
				t.Errorf("%s: %s.%s references %s.%s* — v2 machinery must never start from an untagged file",
					filename, ident.Name, sel.Sel.Name, importPath, forbidden.selector)
			case !forbidden.prefix && sel.Sel.Name == forbidden.selector:
				t.Errorf("%s: %s.%s — v2 machinery must never start from an untagged file", filename, ident.Name, sel.Sel.Name)
			}
		}
		return true
	})
}
