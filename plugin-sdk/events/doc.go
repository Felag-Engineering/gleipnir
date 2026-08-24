// Package events is the plugin-author helper for the io.gleipnir/events MCP
// extension (ADR-054, docs/developer/extension-io-gleipnir-events.md — that
// document is the normative contract; this package is one conforming
// implementation of the server side of it).
//
// A plugin that emits events implements the "event_source" profile
// (plugin-sdk/manifestv2) and serves the wire protocol this package
// implements: negotiation via server/discover, kind discovery via
// events/discover, and delivery via events/listen (SSE-framed JSON-RPC
// notifications, a mandatory heartbeat, and a resumable cursor).
//
// # Shape
//
//   - Kind declares one event kind a server may emit. A plugin author
//     builds a []Kind once and hands it to NewHandler; the same slice is
//     what events/discover renders, so discovery cannot drift from what the
//     handler actually serves. Keeping the plugin's manifest event_kinds in
//     agreement with this slice is the author's job (see
//     examples/minimal-event-source for the pattern) — the manifest attests
//     what an admin consented to, and this package cannot see the manifest
//     from inside a separate build.
//   - Event is what an author publishes through Handler.Publish. It is
//     deliberately not a CloudEvents envelope: source, specversion, and the
//     gleipnirseq sequence number are the SDK's to assign, never the
//     author's — an author who could set gleipnirseq could forge the
//     host's own resume-cursor bookkeeping.
//   - Buffer assigns sequence numbers and answers resume requests. The
//     zero-cost default (NewBuffer) is a bounded in-memory ring: honest
//     about restart (it holds nothing across one), and a resume cursor it
//     cannot satisfy comes back as ErrCursorUnknown rather than a silent
//     gap. Implement Store and use NewBufferWithStore for real durability.
//   - Handler is the http.Handler a plugin's main() serves: it wires
//     server/discover, events/discover, and events/listen together over
//     one Buffer.
package events
