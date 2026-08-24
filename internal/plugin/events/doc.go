// Package events is the host-side events/listen ingestion path for the
// `io.gleipnir/events` extension (ADR-054, mcp-realignment-spec.md §5,
// docs/developer/extension-io-gleipnir-events.md §7).
//
// Three pieces, landed across three issues, live here:
//
//   - cursor.go (#901): Store, the durable events/listen resume-point
//     persistence. There is no in-band ack on this extension — the cursor
//     sent on the next (re)connect IS the ack — so Store is the entire
//     acknowledgement mechanism, not a cache in front of one.
//   - discoverprobe.go (#903): the real caphealth.DiscoverProbe
//     implementation, resolving a plugin instance to its managed MCP
//     endpoint and calling server/discover + events/discover so
//     caphealth can report manifest-vs-discovery drift on the event_source
//     profile as a capability fault.
//   - supervisor.go (#902, this issue): Supervisor, which turns the
//     events/listen stream client (internal/mcp) into a running ingestion
//     path. Per instance it opens one long-lived events/listen connection,
//     hands each delivered event to a Sink synchronously, and advances the
//     cursor only after the sink has consumed it.
//
// # Alongside the v1.1 trigger supervisor, never replacing it
//
// internal/plugin/trigger.Supervisor is the live v1.1 gRPC TriggerService
// stream supervisor. This package's Supervisor is the MCP-realignment
// replacement for the SAME role, built the same way milestone #17 built
// hostendpoint/ beside hostsvc/: a parallel implementation that compiles,
// tests, and is fully wired for injection, but is NOT started by main.go.
// Do not edit trigger/supervisor.go, trigger/dispatcher.go, or dedup/ to
// accommodate this package — the one intentional touch point on the v1.1
// side is trigger/listen_sink.go, a new adapter (mirroring trigger/sink.go's
// existing SinkAdapter) that converts this package's Event into what
// *trigger.Dispatcher.Handle consumes, so BOTH ingestion paths — the v1.1
// gRPC stream and this package's events/listen stream — end at the exact
// same dedup → GetSubscribedActivePolicies → binding evaluate →
// RunLauncher.LaunchWithConcurrency pipeline (spec §5's pipeline
// commitment).
//
// The dependency direction is inverted from what that adapter might
// suggest: this package declares its own Event and Sink types and MUST NOT
// import internal/plugin/trigger, internal/plugin/hostsvc, or any
// plugin-sdk/gen package (enforced by imports_test.go). trigger/listen_sink.go
// is free to import this package because trigger already depends on
// concepts this package does not need to know about — the boundary keeps
// the direction of dependency correct as the realignment continues.
//
// # Wired but not started
//
// Nothing in main.go constructs or starts Supervisor yet, matching the
// posture of the reconciler (internal/plugin/reconciler) and the host
// endpoint (internal/plugin/hostendpoint): built, tested, and ready for
// injection, but the live system is still the v1.1 substrate until the
// ADR-053 cutover. Starting it is future work, not this issue's.
//
// # Rate-limit columns are the other path's problem, not this one's
//
// The v1.1 EmitEvent push path is host self-protection against a plugin that
// pushes too fast: plugin_instances.host_event_rate_per_sec/host_event_burst
// (issue #577) throttle the caller. This package's pull model has no
// equivalent columns because there is nothing to throttle the same way — the
// host is the one calling events/listen, so it paces the stream itself by
// choosing when to read. Those columns and their admin endpoint
// (PUT .../event-rate-limit) are on the milestone #22 deletion list, removed
// together with hostsvc.EmitEvent once the cutover lands (issue #906).
//
// # A note on this package's name
//
// This package is named "events" (plural) and imports
// internal/infra/event (singular) for its Publisher interface. The two are
// unrelated packages that happen to share most of their name — the Go
// import graph does not actually collide here the way
// internal/plugin/trigger's own package-doc caveat describes for
// internal/trigger vs internal/plugin/trigger (both literally named
// "trigger"), but a reader skimming an import block for "event" vs "events"
// should not assume they are the same package, or that a change to one
// implies anything about the other.
package events
