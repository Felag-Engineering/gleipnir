//go:build relaysmoke

// Package relaysmoke is the cross-product smoke suite that drives Gleipnir's
// real MCP-registration, RunLauncher, and tool-input HTTP stack against a
// live, compose-started Relay demo fleet (issue #931, part of #927).
//
// It proves: CA + bearer registration against Relay's control plane, that
// discovery finds all eight of Relay's tools and their schemas canonicalize,
// the negotiated MCP protocol version (printed and asserted, so a silent
// downgrade cannot stay green), the reader flow (list_nodes + a read-class
// run_operation fanned out across the fleet), a gated mutate that exercises
// MRTR when Relay supports it and skips cleanly (naming the reason) when it
// does not, a raw_exec every Node Policy refuses, and that a broken CA or
// bearer token fails with a message naming the cause.
//
// Every file in this package carries the `relaysmoke` build tag, so this
// package does not exist as far as `go build/vet/test/list ./...` is
// concerned — it needs Docker and a multi-container Relay fleet, which
// ci-local and per-PR CI must never require. Run it with
// `make relaysmoke RELAY_DIR=<gleipnir-relay checkout>`; see
// docs/developer/relay-smoke.md for what it proves, how to read a failure,
// and why it runs nightly/on-demand rather than per PR.
package relaysmoke
