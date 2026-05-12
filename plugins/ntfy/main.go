package main

import (
	"net/http"

	channelv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/channel/v1"
	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
	"github.com/felag-engineering/gleipnir/plugin-sdk/serve"
)

func main() {
	serve.Serve(
		serve.WithManifest(pluginManifest),
		serve.WithChannelService(func(host hostv1.HostServiceClient) channelv1.ChannelServiceServer {
			return NewChannelService(host, http.DefaultClient)
		}),
	)
}
