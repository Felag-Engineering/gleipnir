module github.com/felag-engineering/gleipnir/plugins/slack-v2

go 1.25.11

require (
	github.com/felag-engineering/gleipnir/plugin-sdk v0.0.0
	github.com/invopop/jsonschema v0.13.0
	github.com/slack-go/slack v0.26.0
)

require (
	github.com/bahlo/generic-list-go v0.2.0 // indirect
	github.com/buger/jsonparser v1.1.2 // indirect
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/mailru/easyjson v0.7.7 // indirect
	github.com/wk8/go-ordered-map/v2 v2.1.8 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

// The repository workspace resolves plugin-sdk through go.work; this one is for
// builds outside it, such as the image build in Dockerfile.
replace github.com/felag-engineering/gleipnir/plugin-sdk => ../../plugin-sdk
