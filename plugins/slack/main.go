package main

import (
	"net/http"

	channelv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/channel/v1"
	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
	toolv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/tool/v1"
	triggerv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/trigger/v1"
	"github.com/felag-engineering/gleipnir/plugin-sdk/serve"
)

func main() {
	serve.Serve(
		serve.WithManifest(pluginManifest),
		serve.WithToolService(func(host hostv1.HostServiceClient) toolv1.ToolServiceServer {
			// Production: use the default HTTP client and Slack's production API URL.
			return NewToolService(host, http.DefaultClient, "")
		}),
		serve.WithTriggerService(func(host hostv1.HostServiceClient) triggerv1.TriggerServiceServer {
			return NewTriggerService(host)
		}),
		serve.WithChannelService(func(host hostv1.HostServiceClient) channelv1.ChannelServiceServer {
			return NewChannelService(host)
		}),
	)
}
