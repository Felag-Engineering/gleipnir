module github.com/felag-engineering/gleipnir/plugin-sdk/examples/conformance-stub

go 1.25.10

require (
	github.com/felag-engineering/gleipnir/plugin-sdk v0.0.0
	gopkg.in/yaml.v3 v3.0.1
)

// The repository workspace resolves plugin-sdk through the root module's own
// replace; this one is for builds outside it — the image build in Dockerfile
// works from plugin-sdk alone, where nothing else would say where v0.0.0 lives.
replace github.com/felag-engineering/gleipnir/plugin-sdk => ../..
