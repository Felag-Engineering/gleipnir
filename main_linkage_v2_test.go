//go:build substratev2

package main

// main_linkage_v2_test.go proves the substratev2 binary never links the v1
// go-plugin substrate it replaces. main_linkage_test.go is the untagged
// mirror, proving the opposite direction for the default build.
//
// #1005 (G-59, the flip) deletes plugins_v1.go, pluginruntime.go, and every
// package this test forbids; at that point the tagged half becomes the only
// substrate and this test (like main_linkage_test.go) is deleted with it.
//
// Two packages named in issue #948's own DoD are deliberately NOT asserted
// here yet, because they are pulled in by code this issue does not touch and
// that both substrates still need today:
//
//   - internal/plugin/dispatch, via internal/execution/run's
//     ApprovalChannelAdapter/FeedbackChannelAdapter (approval_adapter.go,
//     feedback_adapter.go), which reference dispatch.RouteContext /
//     RoutingOutcome. RunLauncher itself is substrate-agnostic and required by
//     both builds, so importing internal/execution/run pulls dispatch in
//     regardless of which pluginSubsystem is wired up.
//   - internal/plugin/identity, via internal/plugin/hostendpoint's
//     RegistryResolver (resolver.go) — one of hostendpoint's two
//     TokenResolver implementations, kept for the still-live v1.1 substrate.
//     hostendpoint.AssertHostPlane runs in both builds per this issue's own
//     instruction, so hostendpoint (and therefore identity) is unconditional.
//
// Neither leak is new: both predate this issue and both packages are
// candidates for #1008 (G-61, "delete the v1 host runtime graph incl.
// hostsvc, identity, tools, dispatch, process") to close for good post-flip.
// process, hostsvc, tools, and hashicorp/go-plugin ARE fully enforced below.

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestMainLinkage_NoV1Substrate(t *testing.T) {
	cmd := exec.Command("go", "list", "-tags", "substratev2", "-deps", ".")
	cmd.Env = append(os.Environ(), "GOWORK=off")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -tags substratev2 -deps .: %v\n%s", err, out)
	}
	deps := string(out)

	forbidden := []string{
		"github.com/felag-engineering/gleipnir/internal/plugin/process",
		"github.com/felag-engineering/gleipnir/internal/plugin/hostsvc",
		"github.com/felag-engineering/gleipnir/internal/plugin/tools",
		"github.com/hashicorp/go-plugin",
	}
	for _, pkg := range forbidden {
		if containsDep(deps, pkg) {
			t.Errorf("the substratev2 build must not link %s; it belongs only to the default build", pkg)
		}
	}
}

// TestMainDirectImports_NoDispatchOrIdentity keeps the documented dispatch/
// identity exemption above (they are pulled in TRANSITIVELY, via
// internal/execution/run and internal/plugin/hostendpoint, both of which
// both substrates need) narrow: neither package main's own tagged files nor
// internal/plugin/assembly may import either package DIRECTLY. A direct
// import from either would mean v2 code reaching for v1 dispatch/identity
// machinery itself, which is a materially different (and unwanted) thing
// from inheriting them through a shared, substrate-agnostic dependency.
func TestMainDirectImports_NoDispatchOrIdentity(t *testing.T) {
	checkNoDirectImport(t, []string{"-tags", "substratev2"}, ".")
	checkNoDirectImport(t, nil, "./internal/plugin/assembly")
}

func checkNoDirectImport(t *testing.T, extraArgs []string, dir string) {
	t.Helper()
	args := append([]string{"list"}, extraArgs...)
	args = append(args, "-f", `{{join .Imports "\n"}}`, dir)
	cmd := exec.Command("go", args...)
	cmd.Env = append(os.Environ(), "GOWORK=off")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	imports := string(out)

	forbidden := []string{
		"github.com/felag-engineering/gleipnir/internal/plugin/dispatch",
		"github.com/felag-engineering/gleipnir/internal/plugin/identity",
	}
	for _, pkg := range forbidden {
		if containsDep(imports, pkg) {
			t.Errorf("%s must not directly import %s — it is only inherited transitively, per the exemption documented above", dir, pkg)
		}
	}
}
