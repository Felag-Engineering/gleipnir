// Package mcpserver provides a composable, modern-transport (2026-07-28) MCP
// server for plugin authors to embed. A managed plugin is one MCP server
// that may serve tools, io.gleipnir/channel, and io.gleipnir/events
// together (ADR-053…ADR-060, mcp-realignment-spec.md §4/§5/§8); Server
// exists so a plugin author writes that composition once, rather than every
// plugin — including the conformance stub — hand-merging
// capabilities.extensions and re-implementing the transport.
//
// One Server answers server/discover, tools/list, and tools/call, and
// composes any number of extensions (Mount) and streaming methods
// (MountStreaming) behind that one discover:
//
//	srv := mcpserver.NewServer("my-plugin", "1.0.0")
//	if err := srv.RegisterTool(mcpserver.Tool{
//		Name:    "say_hello",
//		Handler: sayHello,
//	}); err != nil {
//		log.Fatal(err)
//	}
//	if err := srv.Mount(myChannelExtension); err != nil {
//		log.Fatal(err)
//	}
//	http.ListenAndServe(addr, srv)
//
// Server is modern-only by construction: it mirrors the A1 (_meta) and A4
// (header) validation rules internal/plugin/hostendpoint.Server enforces on
// the host side of this same transport, and the legacy `initialize`
// handshake is method-not-found rather than a dialect to negotiate down to.
//
// Server implements http.Handler and answers on every path — the host dials
// http://<ip>:<port>/ with no further routing (see
// docs/developer/plugin-manifest-v2.md, "Transport"). A caller must not rely
// on any particular URL path reaching it.
//
// This package is proto-free and imports nothing from the host module
// (github.com/felag-engineering/gleipnir/internal/...): plugin-sdk has its
// own go.mod, and a plugin author's dependency tree must never need the host
// binary's own packages just to embed an MCP server. The _meta keys and
// error codes declared here mirror internal/mcp's byte for byte because both
// sides implement the same wire contract — see the comments on those
// constants.
package mcpserver
