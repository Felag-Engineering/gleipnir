package main

// main_linkage_test.go proves the untagged (default) build never links the v2
// plugin substrate. main_linkage_v2_test.go is the tagged mirror, proving the
// opposite direction for the -tags substratev2 build.
//
// This file carries no build constraint on purpose: the "go list -deps ."
// invocation below never passes -tags, so the assertion it makes — the
// default dependency closure excludes internal/plugin/assembly — holds
// regardless of which tag set the CURRENT test binary happens to be compiled
// with. Running it again from inside the substratev2 test binary (see
// main_linkage_v2_test.go, which compiles alongside this file under that tag)
// is redundant, not wrong.
//
// #1005 (G-59, the flip) deletes this file along with plugins_v1.go,
// pluginruntime.go, and the v1-only adapters — at that point the tagged half
// becomes the only substrate and needs no linkage test of its own kind.

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestMainLinkage_NoV2Substrate(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", ".")
	cmd.Env = append(os.Environ(), "GOWORK=off")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps .: %v\n%s", err, out)
	}

	const forbidden = "github.com/felag-engineering/gleipnir/internal/plugin/assembly"
	if containsDep(string(out), forbidden) {
		t.Errorf("the untagged build must not link %s; it belongs only to -tags substratev2", forbidden)
	}
}

// containsDep reports whether pkg appears as a whole line in go list's
// newline-separated output — a substring match alone would also flag an
// unrelated deeper subpackage sharing the same prefix. Shared with
// main_linkage_v2_test.go's assertion, which compiles alongside this file
// under the substratev2 build.
func containsDep(deps, pkg string) bool {
	for _, line := range strings.Split(deps, "\n") {
		if line == pkg {
			return true
		}
	}
	return false
}
