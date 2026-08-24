// Command host-client is a compile-only example demonstrating plugin-sdk's
// typed host-endpoint client (plugin-sdk/hostclient, #882). It is not a
// runnable plugin binary like minimal-tool — a real plugin calls host/*
// methods from inside its ToolService/ChannelService/TriggerService
// handlers, not from main() — so this example exists solely to show
// construction and a couple of typed calls in context and to keep the
// package's public API compiling and vetting cleanly.
//
// The whole point this example makes: nothing here imports anything under
// plugin-sdk/gen, plugin-sdk/proto, or google.golang.org/grpc. Constructing
// a client and calling host methods needs only plugin-sdk/hostclient, which
// in turn needs only the standard library (ADR-060 Amendment 1's "zero
// protobuf" property, checked mechanically by `go list -deps`).
package main

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/felag-engineering/gleipnir/plugin-sdk/hostclient"
)

func main() {
	// New() reads GLEIPNIR_HOST_ENDPOINT_URL and GLEIPNIR_INSTANCE_TOKEN from
	// the environment — the same two values a plugin subprocess already
	// receives at spawn today (the token) and will receive once the
	// container substrate goes live (the URL, injected by the reconciler at
	// container create). WithBaseURL/WithToken below are only for this
	// example's own demonstration; a real plugin calls New() with no options.
	client, err := hostclient.New(
		hostclient.WithBaseURL("https://host-endpoint.invalid"),
		hostclient.WithToken("example-instance-token"),
	)
	if err != nil {
		log.Fatalf("host-client example: construct client: %v", err)
	}

	// runDemo is gated behind a constant that is always false: this example
	// is compiled and vetted by CI, never executed against a live endpoint,
	// so the calls below only need to type-check and read correctly — not
	// succeed against a real host.
	const runDemo = false
	if runDemo {
		demonstrateCalls(client)
	}
}

// demonstrateCalls shows the typed request/response shape of a Tier-1 call
// and a Tier-2 call, plus the two error types a caller matches on. It is
// dead code by construction (see runDemo above) but must still compile and
// vet, which is what makes it useful as documentation.
func demonstrateCalls(client *hostclient.Client) {
	ctx := context.Background()

	// A call the plugin's own tool handler makes mid-request: attach the
	// call id the host handed it so host/get_run_context and host/log can
	// correlate back to the run.
	ctx = hostclient.WithCallID(ctx, "example-call-id")

	cfg, err := client.GetInstanceConfig(ctx)
	if err != nil {
		reportHostClientError(err)
		return
	}
	fmt.Println("instance config:", cfg.ConfigJSON)

	if _, err := client.Log(ctx, hostclient.LogRequest{
		Level: hostclient.LogLevelInfo,
		Msg:   "host-client example reached the host",
	}); err != nil {
		reportHostClientError(err)
		return
	}

	history, err := client.RunHistoryRead(ctx, hostclient.RunHistoryReadRequest{Limit: 10})
	if err != nil {
		reportHostClientError(err)
		return
	}
	fmt.Println("run history entries:", len(history.Runs))
}

// reportHostClientError shows the two error shapes a caller distinguishes:
// *hostclient.HostError (the tool ran and refused, with a stable machine
// code) versus *hostclient.JSONRPCError (a transport-level fault, the tool
// never ran).
func reportHostClientError(err error) {
	var hostErr *hostclient.HostError
	if errors.As(err, &hostErr) {
		log.Printf("host refused the call: code=%s message=%s", hostErr.Code, hostErr.Message)
		return
	}
	var rpcErr *hostclient.JSONRPCError
	if errors.As(err, &rpcErr) {
		log.Printf("transport-level failure: code=%d message=%s", rpcErr.Code, rpcErr.Message)
		return
	}
	log.Printf("host-client example: unexpected error: %v", err)
}
