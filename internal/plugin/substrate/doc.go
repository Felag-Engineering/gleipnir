// Package substrate holds the container substrate's real-daemon integration
// test (ADR-056, mcp-realignment-spec.md §7 and §15; issue #820).
//
// Every other test in this milestone runs against container.Fake, which runs
// the SAME ValidateCreate/ValidateCreateNetwork self-constraint the real
// runtime does — so a Fake-based assertion about a rejection is an assertion
// about the actual rule. What a Fake cannot tell you is whether the daemon
// agrees: whether a network created with Internal:true really has no route
// out, whether a subnet the allocator carved is one the daemon accepts,
// whether an image the GC believes is unreferenced is one the daemon will
// actually delete. Those are properties of the runtime, and only a runtime can
// answer them.
//
// This package deliberately contains no production code. It exists so the
// integration test has a package to live in, so `go list ./...` can see it,
// and so scripts/ci-local-scope.sh can map the substrate packages onto the
// lane that runs it.
//
// The test itself is behind the `substrate` build tag and is a no-op without
// it. See substrate_test.go for why it is opt-in rather than skipped at
// runtime.
package substrate
