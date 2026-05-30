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
// # Ergonomic seam (recommended)
//
// Authors implement tool.Service, channel.Service, or trigger.Service from the
// corresponding sub-packages and register them via the WithXHandler options:
//
//	serve.Serve(
//	    serve.WithManifest(pluginManifest),
//	    serve.WithToolHandler(func(host hostv1.HostServiceClient) tool.Service {
//	        return NewToolService(host)
//	    }),
//	)
//
// The ergonomic seam keeps proto types, ErrorEnvelope construction, and
// []byte↔string JSON conversion inside the serve package. Authors deal only in
// plain Go types and pluginerr codes. See plugin-sdk/examples/minimal-tool and
// plugins/ntfy for working examples.
//
// # Raw gRPC seam (advanced)
//
// Authors who need full control over the generated proto types can implement
// toolv1.ToolServiceServer / channelv1.ChannelServiceServer /
// triggerv1.TriggerServiceServer directly and register them via WithToolService
// / WithChannelService / WithTriggerService. plugins/slack is the canonical
// example of this path (see ADR-050).
//
// # Choosing an event_id for TriggerService plugins
//
// Every StartResponse message must carry a stable event_id that the host uses
// for deduplication within a 1-hour rolling window (spec §4.3). Two patterns
// are supported:
//
// ## Preferred: ULID-encoded substrate sequence number
//
// When the substrate provides a stable monotonic identifier — a Slack event_id,
// a GitHub webhook delivery GUID, a Kafka offset — pass it through verbatim if
// it is already ULID-shaped, or wrap it in a ULID using oklog/ulid/v2:
//
//	import "github.com/oklog/ulid/v2"
//
//	// Substrate provides a monotonic offset; encode as ULID with event time.
//	id := ulid.MustNew(ulid.Timestamp(eventTime), ulid.DefaultEntropy())
//	resp.EventId = id.String()
//
// ULIDs are lexicographically time-sortable, which allows the dedup store
// (#215) to use primary-key range cleanup rather than full-table scans.
// github.com/oklog/ulid/v2 is already in the main go.mod.
//
// ## Fallback: SHA-256 of canonical payload
//
// When the substrate provides no stable per-event identifier, compute a
// SHA-256 over the canonicalised JSON payload:
//
//	import (
//	    "crypto/sha256"
//	    "encoding/hex"
//	    "encoding/json"
//	)
//
//	// Marshal the payload in a canonical (sorted-key) form first so the hash
//	// is deterministic across equivalent payloads with different key orders.
//	b, _ := json.Marshal(payload)
//	sum := sha256.Sum256(b)
//	resp.EventId = hex.EncodeToString(sum[:])
//
// With this approach, deduplication works within the 1-hour window; events
// separated by more than the window may fire more than once (best-effort only,
// per spec §4.3).
//
// ## Host contract
//
// The host treats event_id as an opaque string. Any stable, per-event string
// works. ULID is the SDK-recommended default because the dedup store can use
// its time-sortable property for efficient cleanup; SHA-256 is the correct
// fallback when no substrate-provided stable ID exists.
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
