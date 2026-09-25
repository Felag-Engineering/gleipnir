package run

import (
	"go/parser"
	"go/token"
	"testing"
)

// TestTaskChannelFiles_DoNotImportDispatch pins the #961 boundary: the new
// v2 task-backed channel adapters must not import internal/plugin/dispatch,
// the v1 gRPC channel package these files exist to route around.
//
// This checks source files rather than shelling out to `go list`: go list
// reports imports per PACKAGE, and approval_adapter.go / feedback_adapter.go
// (the v1 adapters, staying in this same package until the #22 cutover)
// legitimately import dispatch — so `go list -deps` on this package will
// always report it as a dependency regardless of what these three files do.
// The boundary #961 asks for is per FILE, which only source-level parsing
// can see.
func TestTaskChannelFiles_DoNotImportDispatch(t *testing.T) {
	const forbidden = `"github.com/felag-engineering/gleipnir/internal/plugin/dispatch"`
	files := []string{"task_waiter.go", "channel_adapter.go", "hitl_entries.go"}

	fset := token.NewFileSet()
	for _, name := range files {
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, imp := range f.Imports {
			if imp.Path.Value == forbidden {
				t.Errorf("%s imports internal/plugin/dispatch, which #961 exists to route around", name)
			}
		}
	}
}
