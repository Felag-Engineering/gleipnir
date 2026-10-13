package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestRun_ShutdownOrdering pins the order run() tears the plugin subsystem
// down in, for whichever substrate is compiled in: plugin trigger ingress is
// quiesced (stream goroutines stopped and joined) BEFORE the run drain, and
// the rest of the subsystem is shut down only AFTER it.
//
// A trigger event that reaches RunLauncher.Launch after RunManager.Wait has
// returned registers a run nobody awaits, and that run races teardown of what
// it depends on. The assembly's own test proves Quiesce joins in-flight
// launches; this proves run() calls it at the right point.
func TestRun_ShutdownOrdering(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing main.go: %v", err)
	}

	var runFn *ast.FuncDecl
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == "run" {
			runFn = fn
		}
	}
	if runFn == nil {
		t.Fatal("main.go has no func run")
	}

	// first and last record the source offset of the first and last call to
	// recv.method. pluginSys.shutdown is also called on an early-exit path, so
	// the shutdown-after-drain check must look at the last call.
	first := map[string]int{}
	last := map[string]int{}
	ast.Inspect(runFn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		recv, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		key := recv.Name + "." + sel.Sel.Name
		offset := fset.Position(call.Pos()).Offset
		if _, seen := first[key]; !seen {
			first[key] = offset
		}
		last[key] = offset
		return true
	})

	for _, key := range []string{"pluginSys.quiesce", "runManager.CancelAll", "runManager.Wait", "pluginSys.shutdown"} {
		if _, ok := first[key]; !ok {
			t.Fatalf("run() no longer calls %s; update this test if the shutdown sequence was restructured deliberately", key)
		}
	}

	if first["pluginSys.quiesce"] > first["runManager.Wait"] {
		t.Error("run() drains runs (runManager.Wait) before quiescing plugin trigger ingress (pluginSys.quiesce)")
	}
	if first["pluginSys.quiesce"] > first["runManager.CancelAll"] {
		t.Error("run() cancels runs (runManager.CancelAll) before quiescing plugin trigger ingress (pluginSys.quiesce)")
	}
	if last["pluginSys.shutdown"] < first["runManager.Wait"] {
		t.Error("run() shuts the plugin subsystem down (pluginSys.shutdown) before the run drain finished")
	}
}
