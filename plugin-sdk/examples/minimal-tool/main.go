package main

import (
	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
	toolv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/tool/v1"
	"github.com/felag-engineering/gleipnir/plugin-sdk/serve"
)

func main() {
	// serve.Serve wires up the go-plugin transport, registers ToolService, and
	// blocks until the host disconnects or a signal is received.
	//
	// Passing --emit-manifest as the first argument causes it to write the
	// manifest JSON to stdout and exit (used by gleipnir-plugin gen-manifest).
	//
	// A bare serve.Serve() with no options is valid: the plugin responds to
	// the handshake and Bootstrap.Bind but all service RPCs return
	// codes.Unavailable, which is the correct documented behaviour.
	serve.Serve(
		serve.WithManifest(pluginManifest),
		serve.WithToolService(func(host hostv1.HostServiceClient) toolv1.ToolServiceServer {
			return NewToolService(host)
		}),
	)
}
