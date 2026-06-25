package main

import (
	"context"
	"net/http"

	channelv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/channel/v1"
	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
	toolv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/tool/v1"
	triggerv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/trigger/v1"
	"github.com/felag-engineering/gleipnir/plugin-sdk/serve"
)

func main() {
	// One hub registry per process: TriggerService and ChannelService share the
	// same Socket Mode WebSocket connection for the same xapp-token.
	registry := newHubRegistry(defaultSocketModeFactory)

	serve.Serve(
		serve.WithManifest(pluginManifest),
		serve.WithToolService(func(host hostv1.HostServiceClient) toolv1.ToolServiceServer {
			// Production: use the default HTTP client and Slack's production API URL.
			return NewToolService(host, http.DefaultClient, "")
		}),
		serve.WithTriggerService(func(host hostv1.HostServiceClient) triggerv1.TriggerServiceServer {
			// Production: use the default HTTP client and Slack's production API URL.
			return NewTriggerService(host, registry, http.DefaultClient, "")
		}),
		serve.WithChannelService(func(host hostv1.HostServiceClient) channelv1.ChannelServiceServer {
			cs := NewChannelService(host, registry, http.DefaultClient)
			// Maintainer goroutine keeps the interactive handler registered
			// across late-config and hub-death. Bound to context.Background
			// because the plugin process has no explicit shutdown hook —
			// goroutine ends when the process exits.
			cs.Start(context.Background())
			return cs
		}),
	)
}
