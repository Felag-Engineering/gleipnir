// Package serve provides the entry point for plugin binaries.
//
// Plugin authors call serve.Serve() from their main() after declaring their
// services. The implementation wires up go-plugin transport, gRPC server
// registration, health checks, and call-ID propagation.
//
// serve.EmitManifest() is called when the binary is invoked with
// --emit-manifest. It writes the plugin's manifest JSON to stdout so that
// gleipnir-plugin gen-manifest can capture and canonicalise it.
package serve

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"

	goplugin "github.com/hashicorp/go-plugin"

	"github.com/felag-engineering/gleipnir/plugin-sdk/hostwire"
	"github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
)

// Serve starts the plugin subprocess listener. It is the last call in a plugin
// binary's main() function and does not return under normal operation.
//
// If the first argument is "--emit-manifest", Serve delegates to
// emitManifestAndExit and the process exits 0 (or 2 if no manifest was provided
// via WithManifest). This detection happens before any flag.Parse() call, so
// scaffold templates must not call flag.Parse() themselves.
//
// Otherwise, Serve runs go-plugin's server in a goroutine and blocks until
// either the server exits on its own (host disconnects) or a SIGTERM/SIGINT
// signal is received. SIGTERM is handled here because go-plugin v1.6.3 catches
// only SIGINT internally; the OS default for SIGTERM is immediate process kill,
// which would prevent any cleanup.
//
// Passing zero options is valid: Serve responds correctly to the handshake and
// Bootstrap.Bind, but all service RPCs return codes.Unavailable.
func Serve(opts ...Option) {
	cfg := newConfig(opts)

	// Positional flag detection chosen so scaffold templates can drop their
	// own flag.Parse() block (which would consume --emit-manifest before Serve
	// sees it). Using os.Args[1] directly avoids interfering with any flags the
	// plugin author may wish to define for other purposes.
	if len(os.Args) > 1 && os.Args[1] == "--emit-manifest" {
		emitManifestAndExit(cfg)
		return // unreachable: emitManifestAndExit calls os.Exit
	}

	impl := newPluginGRPCPlugin(cfg)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, cfg.stopSignals...)
	defer signal.Stop(quit)

	done := make(chan struct{})
	go func() {
		goplugin.Serve(&goplugin.ServeConfig{
			HandshakeConfig: hostwire.HandshakeConfig,
			Plugins:         goplugin.PluginSet{"gleipnir": impl},
			GRPCServer:      goplugin.DefaultGRPCServer,
		})
		close(done)
	}()

	select {
	case <-quit:
		// Best-effort drain. We own the SIGTERM path because go-plugin's
		// internal handler covers only SIGINT. SIGKILL or a crash bypasses this
		// entirely, so shutdown() must be treated as best-effort, not a
		// correctness guarantee.
	case <-done:
	}
	impl.shutdown()
}

// EmitManifest writes m as JSON to stdout. Plugin authors can call this
// directly when they wire their own --emit-manifest flag, but scaffolded
// templates should rely on Serve's built-in detection instead.
func EmitManifest(m manifest.Manifest) {
	enc := json.NewEncoder(os.Stdout)
	_ = enc.Encode(m)
}

// emitManifestAndExit writes the manifest JSON to stdout and exits. Exits 2
// with an error message if no manifest was registered via WithManifest.
func emitManifestAndExit(cfg *config) {
	if cfg.manifest == nil {
		fmt.Fprintln(os.Stderr, "serve: --emit-manifest requires WithManifest(...) to be passed to Serve")
		os.Exit(2)
	}
	EmitManifest(*cfg.manifest)
	os.Exit(0)
}
