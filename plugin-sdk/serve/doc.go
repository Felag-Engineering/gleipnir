// Package serve provides the entry point for plugin binaries.
//
// Plugin authors call serve.Serve() from their main() after declaring their
// services. The implementation wires up go-plugin transport, gRPC server
// registration, health checks, and call-ID propagation.
//
// This package is a placeholder; the full implementation lands in the Phase 3
// loader PR alongside the plugin host.
package serve

// Serve starts the plugin subprocess listener. It is the last call in a plugin
// binary's main() function and does not return under normal operation.
//
// This stub panics so that compilation succeeds while the implementation is
// pending. The Phase 3 loader PR replaces this with the real implementation.
func Serve() {
	panic("serve.Serve: not yet implemented — Phase 3 loader PR")
}
