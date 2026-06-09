package main

import (
	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
	"github.com/felag-engineering/gleipnir/plugin-sdk/serve"
	"github.com/felag-engineering/gleipnir/plugin-sdk/tool"
)

func main() {
	// serve.Serve wires up the go-plugin transport, registers ToolService, and
	// blocks until the host disconnects or a signal is received.
	//
	// Passing --emit-manifest as the first argument causes it to write the
	// manifest JSON to stdout and exit (used by gleipnir-plugin gen-manifest).
	//
	// WithToolHandler is the ergonomic seam: authors implement tool.Service
	// and do not touch proto types directly. The raw WithToolService option
	// remains available for authors who need full proto control; plugins/slack
	// is the canonical example of that path.
	serve.Serve(
		serve.WithManifest(pluginManifest),
		serve.WithToolHandler(func(host hostv1.HostServiceClient) tool.Service {
			return NewToolService(host)
		}),
	)
}
