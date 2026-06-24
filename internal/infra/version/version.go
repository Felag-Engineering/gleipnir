// Package version exposes the build version of Gleipnir as a single source
// of truth. Bump the constant in the commit immediately preceding a release
// tag, then create the matching git tag (e.g. v1.0.0).
package version

// Version is the current Gleipnir release. It is reported through the API
// metadata and sent to MCP servers in the initialize handshake's clientInfo.
const Version = "1.1.0"
