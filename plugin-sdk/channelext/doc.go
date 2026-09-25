// Package channelext is the plugin-author helper for the io.gleipnir/channel
// MCP extension (ADR-055 Amendment 1,
// docs/developer/extension-io-gleipnir-channel.md — that document is the
// normative contract; this package is one conforming implementation of the
// server side of it).
//
// Amendment 1 states the rule this package exists to satisfy: host-initiated
// ⇒ not a tool. Delivering a message to a human is something the host
// decides to do, never something a model asks for, so it must never be
// reachable through tools/call. channelext is built on top of
// plugin-sdk/mcpserver's Extension seam for exactly that reason: mounting it
// via Server.Mount puts channel/notify behind server/discover negotiation,
// alongside a server's tools/list and tools/call, but structurally outside
// the set of things a policy can grant.
//
// # Shape
//
//   - Declaration is this server's io.gleipnir/channel capability entry
//     (contract §4.1): version, assurance, and the delivery targets it
//     supports. New validates it up front — a broken declaration is a
//     startup bug to fail loudly on, the same discipline
//     mcpserver.RegisterTool and Server.Mount already apply to a tool or an
//     extension ID.
//   - Target and Notification are the channel/notify payload (contract §7):
//     where a message goes and what it says. Decoding is strict about the
//     vocabulary it knows (an unrecognized delivery is refused) and tolerant
//     of anything else, mirroring internal/mcp's own posture on the wire
//     shapes both sides of this contract share.
//   - NotifyFunc is the plugin author's own delivery logic. channelext knows
//     nothing about what a "space" or a "person" is for any given channel;
//     it decodes the target and message and hands them to the author's
//     handler, whose error return is what turns "unknown address" into a
//     JSON-RPC error rather than a silent, false acknowledgement.
//   - New(decl, notify) *Extension is what a plugin's main() passes to
//     Server.Mount. Until #967 adds a request handler, Methods() serves
//     channel/notify only — channel/request is absent, and the composed
//     server's own dispatch answers it with method-not-found. That is the
//     contract's worked example C (a notify-only channel with no input
//     surface), not a gap: a server declaring the extension without a
//     request path is a documented, first-class shape.
//
// This package is proto-free and imports nothing from the host module
// (github.com/felag-engineering/gleipnir/internal/...): plugin-sdk has its
// own go.mod, and a plugin author's dependency tree must never need the host
// binary's own packages just to serve this extension.
package channelext
