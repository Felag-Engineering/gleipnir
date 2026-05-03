// Package serve provides the entry point for plugin binaries.
//
// Plugin authors call serve.Serve() from their main() after declaring their
// services. The implementation wires up go-plugin transport, gRPC server
// registration, health checks, and call-ID propagation.
//
// serve.EmitManifest() is called when the binary is invoked with
// --emit-manifest. It writes the plugin's manifest JSON to stdout so that
// gleipnir-plugin gen-manifest can capture and canonicalise it.
//
// Both functions are stubs; the full implementation lands in the Phase 3
// loader PR alongside the plugin host. (#170)
package serve

// Serve starts the plugin subprocess listener. It is the last call in a plugin
// binary's main() function and does not return under normal operation.
//
// This stub panics so that compilation succeeds while the implementation is
// pending. The Phase 3 loader PR replaces this with the real implementation.
// (#170)
func Serve() {
	panic("serve.Serve: not yet implemented — Phase 3 loader PR (#170)")
}

// EmitManifest writes the plugin's manifest as JSON to stdout, then exits.
// It is invoked when the binary is called with the --emit-manifest flag.
// gleipnir-plugin gen-manifest uses this output to produce deterministic YAML.
//
// This stub panics; the real implementation lands with the Phase 3 loader PR.
// (#170)
func EmitManifest() {
	panic("serve.EmitManifest: not yet implemented — Phase 3 loader PR (#170)")
}
