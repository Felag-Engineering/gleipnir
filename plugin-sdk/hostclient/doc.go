// Package hostclient is a typed Go client for the MCP realignment host
// endpoint (ADR-057 as amended, spec §8; internal/plugin/hostendpoint on the
// host side). It carries no protobuf dependency: the host endpoint is an
// ordinary 2026-07-28 streamable-HTTP MCP server, so talking to it needs
// nothing beyond net/http and encoding/json.
//
// ADR-060 Amendment 1 names the point of this package directly: "a plugin
// author in any language now needs exactly one protocol dependency (the MCP
// SDK they already have) for both directions: the plugin serves MCP (tools,
// events, channel) and consumes MCP (host callbacks)." Before this package
// existed, an author needed the MCP SDK for the serving direction AND the
// generated gRPC stubs AND the buf toolchain for the calling direction. After
// it, they need the first only — hostclient is a thin, stdlib-only wrapper
// over the same transport the author is already speaking.
//
// # Construction
//
// New reads the host endpoint URL and instance token from the environment by
// default (HostEndpointURLEnvVar and InstanceTokenEnvVar), the same way a
// plugin subprocess already receives its identity token today. Both can be
// overridden with WithBaseURL / WithToken for tests or unusual deployments.
// Construction fails clearly when neither the environment nor an option
// supplies a required value — a plugin author should learn about a missing
// token at startup, not at the first failed call.
//
// # Method shape
//
// Every host method (spec §8's eleven host/* tools, all reachable through
// tools/call) is a Client method that takes a typed request and returns a
// typed response, mirroring the shape of today's generated gRPC host.* calls
// one-for-one — deliberately, since the Slack plugin's eventual port to this
// client (#19) is meant to be a mechanical rewrite of call sites, not a
// redesign of how the plugin talks to the host.
//
// # Errors
//
// A call can fail two structurally different ways, and callers need to tell
// them apart: a transport/JSON-RPC-level fault (bad headers, unsupported
// protocol version, unknown method) surfaces as *JSONRPCError, while a tool
// that ran and refused surfaces as *HostError carrying one of the stable
// hostendpoint code strings (invalid_argument, permission_denied,
// unauthenticated, …). Match on the Code field, not the error string.
package hostclient
