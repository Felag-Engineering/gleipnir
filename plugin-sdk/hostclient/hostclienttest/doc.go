// Package hostclienttest is a contract-faithful fake of the MCP realignment
// host endpoint (internal/plugin/hostendpoint on the host side; see
// docs/developer/host-endpoint-contract.md §6 for the normative method
// inventory this fake mirrors). It exists so a plugin author can write tests
// against a server that speaks the real wire contract, rather than each
// author's own ad-hoc httptest handler drifting from it independently — the
// "ad-hoc fake host endpoint" the M7 issue notes describe popping up
// repeatedly before this package existed.
//
// Like plugin-sdk/hostclient itself, this package imports nothing under
// internal/ — it is a separate Go module (plugin-sdk/go.mod) and reproduces
// the wire shapes (JSON-RPC envelope, header/_meta validation, error-code
// vocabulary) from the ground-truth handlers rather than importing them.
//
// # Naming
//
// hostclienttest pairs with hostclient the way net/http/httptest pairs with
// net/http: New builds a bare http.Handler, and NewServer is the convenience
// constructor that also wraps it in an httptest.Server and wires the
// environment variables hostclient.New reads by default
// (GLEIPNIR_HOST_ENDPOINT_URL, GLEIPNIR_INSTANCE_TOKEN), so a test using
// hostclient.New() with no options talks to the fake automatically.
//
// # Minimal usage
//
//	srv := hostclienttest.NewServer(t, hostclienttest.WithInstanceConfigJSON(`{"channel":"#ops"}`))
//	client, err := hostclient.New()
//	out, err := client.GetInstanceConfig(context.Background())
//	srv.WaitForCall(context.Background(), "host/log") // signal-don't-poll
//
// # Known fake-vs-real divergences
//
//   - No manifest or database: host/run_history_read and
//     host/user_directory_read serve whatever WithRunHistory /
//     WithUserDirectory configured, with no Tier-2 manifest-capability gate
//     — that gate is host-side authorization policy the real endpoint's own
//     tests (internal/plugin/hostendpoint/tier2_test.go) cover, not a wire
//     shape a plugin author's test needs to reproduce.
//   - No ADR-047 cardinality cap on host/emit_metric: every emission is
//     recorded verbatim via Metrics(), unlimited.
//   - host/get_run_context performs no cross-instance ownership check (the
//     real endpoint's high-severity unauthorized_request_id audit event) —
//     this fake models exactly one instance, so there is no "another
//     instance" to leak into.
package hostclienttest
