// Package dood holds the Docker-outside-of-Docker integration suite for the v2
// plugin substrate (mcp-realignment-spec.md §7 and §15; issue #985).
//
// Under rootless Podman a test process on the runner host cannot reach an
// instance gateway or a plugin container's IP: those addresses live inside the
// runtime's own networks. The suite therefore runs its test binary inside a
// container that has the runtime socket mounted, and the assembly self-attaches
// that container to each instance network (#958) exactly as it would attach a
// production Gleipnir. That is the same shape as a production DooD deployment,
// and it is what makes the host endpoint reachable at all.
//
// The suite builds its runtime only through assembly.New, so it exercises the
// composition the binary ships rather than a look-alike. It installs the signed
// conformance-stub bundles (make conformance-stub-bundle) the way an operator's
// upload does, and drives them to a running state through the reconciler.
//
// This package deliberately contains no production code. The suite itself is
// behind the `dood` and `substratev2` build tags and is a no-op without them,
// for the reason given in dood_test.go. Run it with scripts/run-dood-suite.sh;
// that is the one supported way, for CI and for developers alike, because the
// suite refuses to run outside a container.
package dood
