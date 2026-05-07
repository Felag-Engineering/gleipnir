//go:build runfixture

// Command runfixture is an inline plugin binary used only by the run_test.go
// e2e tests. It is compiled with -tags runfixture and never shipped.
//
// It implements the minimum plugin surface required to test the `run` command:
//   - HandshakeService.Negotiate — declares Tool + Trigger capabilities
//   - BootstrapService.Bind — dials HostService on the provided broker ID
//   - ToolService.ListTools — returns one echo tool
//   - ToolService.Call — echoes the input back
//   - TriggerService.Start — emits one synthetic event via EmitEvent then closes
//   - --replay-event — reads event JSON from stdin (io.ReadAll), prints JSON, exits 0
//
// This binary intentionally does NOT use plugin-sdk/serve (which is still a
// stub). Instead it calls plugin.Serve directly from hashicorp/go-plugin with
// the same HandshakeConfig and PluginMap that hostwire exports, guaranteeing
// behavioral parity with production.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	bootstrapv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/bootstrap/v1"
	handshakev1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/handshake/v1"
	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
	toolv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/tool/v1"
	triggerv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/trigger/v1"
	"github.com/felag-engineering/gleipnir/plugin-sdk/hostwire"
)

func main() {
	// --replay-event mode: read the event JSON from stdin, print a summary, exit.
	// The payload arrives via stdin rather than as a CLI arg to avoid ARG_MAX
	// (~2MB on Linux) truncating large webhook payloads.
	if len(os.Args) >= 2 && os.Args[1] == "--replay-event" {
		raw, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "replay-event: read stdin: %v\n", err)
			os.Exit(1)
		}
		handleReplayEvent(string(raw))
		return
	}

	// Normal mode: serve as a go-plugin subprocess.
	impl := &fixturePlugin{}
	goplugin.Serve(&goplugin.ServeConfig{
		HandshakeConfig: hostwire.HandshakeConfig,
		Plugins: goplugin.PluginSet{
			"gleipnir": &fixtureGRPCPlugin{impl: impl},
		},
		GRPCServer: goplugin.DefaultGRPCServer,
	})
}

// handleReplayEvent parses the JSON event (read from stdin by the caller),
// prints a result line, and exits 0 on success or 1 on parse failure. This
// implements the --replay-event convention documented in plugin-spec §14.4 and
// README §run.
func handleReplayEvent(eventJSON string) {
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(eventJSON), &parsed); err != nil {
		fmt.Fprintf(os.Stderr, "replay-event: invalid JSON: %v\n", err)
		os.Exit(1)
	}

	kind, _ := parsed["event_kind"].(string)
	result := map[string]interface{}{
		"parsed_kind": kind,
		"ok":          true,
	}
	b, _ := json.Marshal(result)
	fmt.Println(string(b))
}

// ── go-plugin GRPCPlugin implementation ─────────────────────────────────────

// fixtureGRPCPlugin adapts fixturePlugin for go-plugin's GRPCPlugin interface.
type fixtureGRPCPlugin struct {
	goplugin.Plugin
	impl *fixturePlugin
}

func (p *fixtureGRPCPlugin) GRPCServer(broker *goplugin.GRPCBroker, s *grpc.Server) error {
	// Stash the broker so Bootstrap.Bind can pair the received broker ID with
	// the broker needed to Dial the host's HostService in Trigger.Start.
	p.impl.broker = broker

	handshakev1.RegisterHandshakeServiceServer(s, p.impl)
	bootstrapv1.RegisterBootstrapServiceServer(s, p.impl)
	toolv1.RegisterToolServiceServer(s, p.impl)
	triggerv1.RegisterTriggerServiceServer(s, p.impl)
	return nil
}

func (p *fixtureGRPCPlugin) GRPCClient(_ context.Context, _ *goplugin.GRPCBroker, _ *grpc.ClientConn) (interface{}, error) {
	return nil, status.Error(codes.Unimplemented, "GRPCClient not used on plugin side")
}

// ── fixture implementations ──────────────────────────────────────────────────

// fixturePlugin implements all services used by the e2e tests.
type fixturePlugin struct {
	handshakev1.UnimplementedHandshakeServiceServer
	bootstrapv1.UnimplementedBootstrapServiceServer
	toolv1.UnimplementedToolServiceServer
	triggerv1.UnimplementedTriggerServiceServer

	// broker is stored after Bootstrap.Bind so Trigger.Start can call EmitEvent.
	broker   *goplugin.GRPCBroker
	brokerID uint32
}

// Negotiate declares Tool + Trigger capabilities.
func (f *fixturePlugin) Negotiate(_ context.Context, req *handshakev1.NegotiateRequest) (*handshakev1.NegotiateResponse, error) {
	return &handshakev1.NegotiateResponse{
		SdkVersion:    "0.0.0-runfixture",
		PluginVersion: "0.1.0",
		Ok:            true,
		ActualCapabilities: []handshakev1.ServiceCapability{
			handshakev1.ServiceCapability_SERVICE_CAPABILITY_TOOL,
			handshakev1.ServiceCapability_SERVICE_CAPABILITY_TRIGGER,
		},
	}, nil
}

// Bind records the broker ID so Trigger.Start can dial HostService.
func (f *fixturePlugin) Bind(_ context.Context, req *bootstrapv1.BindRequest) (*bootstrapv1.BindResponse, error) {
	f.brokerID = req.GetHostBrokerId()
	return &bootstrapv1.BindResponse{Ok: true}, nil
}

// ListTools returns one echo tool.
func (f *fixturePlugin) ListTools(_ context.Context, _ *toolv1.ListToolsRequest) (*toolv1.ListToolsResponse, error) {
	return &toolv1.ListToolsResponse{
		Tools: []*toolv1.ToolSchema{
			{
				Name:        "echo",
				Description: "Echoes the input back as output",
				InputSchema: `{"type":"object","properties":{"text":{"type":"string"}}}`,
			},
		},
	}, nil
}

// Call echoes the input_json back as output_json.
func (f *fixturePlugin) Call(_ context.Context, req *toolv1.CallRequest) (*toolv1.CallResponse, error) {
	return &toolv1.CallResponse{
		OutputJson: req.GetInputJson(),
	}, nil
}

// Start emits one synthetic "fixture.event" via HostService.EmitEvent, then
// closes the stream. This exercises the full capture pipeline.
func (f *fixturePlugin) Start(req *triggerv1.StartRequest, stream triggerv1.TriggerService_StartServer) error {
	ctx := stream.Context()

	// Dial the host's HostService on the broker ID we received in Bind.
	// If Bind was never called (e.g. the test doesn't send Bootstrap.Bind),
	// we skip the EmitEvent call and just close the stream.
	if f.brokerID != 0 && f.broker != nil {
		conn, err := f.broker.Dial(f.brokerID)
		if err == nil {
			defer conn.Close()
			hostClient := hostv1.NewHostServiceClient(conn)

			watchScope := strings.TrimSpace(req.GetWatchScopeJson())
			if watchScope == "" {
				watchScope = "{}"
			}
			payload, _ := json.Marshal(map[string]interface{}{
				"source":      "runfixture",
				"watch_scope": json.RawMessage(watchScope),
			})
			_, _ = hostClient.EmitEvent(ctx, &hostv1.EmitEventRequest{
				EventId:     "fixture-evt-1",
				EventKind:   "fixture.event",
				PayloadJson: string(payload),
			})
		}
	}

	// Close the stream — one event is enough for the e2e test.
	return nil
}
