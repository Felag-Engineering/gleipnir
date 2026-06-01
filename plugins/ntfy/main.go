package main

import (
	"net/http"

	"github.com/felag-engineering/gleipnir/plugin-sdk/channel"
	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
	"github.com/felag-engineering/gleipnir/plugin-sdk/serve"
)

func main() {
	serve.Serve(
		serve.WithManifest(pluginManifest),
		serve.WithChannelHandler(func(host hostv1.HostServiceClient) channel.Service {
			return NewChannelService(host, http.DefaultClient)
		}),
	)
}
