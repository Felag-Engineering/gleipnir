module github.com/felag-engineering/gleipnir/plugins/ntfy

go 1.25.11

require github.com/felag-engineering/gleipnir/plugin-sdk v0.0.0

require (
	github.com/kr/text v0.2.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

// The repository workspace resolves plugin-sdk through the root module's own
// replace; this one is for builds outside it — the image build in Dockerfile
// works from plugin-sdk alone, where nothing else would say where v0.0.0 lives.
replace github.com/felag-engineering/gleipnir/plugin-sdk => ../../plugin-sdk
