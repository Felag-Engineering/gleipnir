package main

import (
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/felag-engineering/gleipnir/plugin-sdk/manifestv2"
)

// stubDigest is a placeholder image digest. A real deployment pins the digest
// its own build of the Dockerfile produced; the manifests here are fixtures.
const stubDigest = "0123456789abcdef" + "0123456789abcdef" + "0123456789abcdef" + "0123456789abcdef"

// manifestMode is one shipped manifest variant. Each pairs with the
// STUB_PROFILES / STUB_ASSURANCE values that make the running container agree
// with it, so a conformance run picks a manifest and the matching environment
// together.
type manifestMode struct {
	// File is the canonical YAML under manifests/ projected from this mode.
	File      string
	Profiles  Profiles
	Assurance string // only meaningful when Profiles.HumanChannel
}

var manifestModes = []manifestMode{
	{File: "all.yaml", Profiles: Profiles{ToolProvider: true, EventSource: true, HumanChannel: true}, Assurance: manifestv2.AssuranceAuthenticated},
	{File: "tool-only.yaml", Profiles: Profiles{ToolProvider: true}},
	{File: "event-only.yaml", Profiles: Profiles{EventSource: true}},
	{File: "human-channel.yaml", Profiles: Profiles{HumanChannel: true}, Assurance: manifestv2.AssuranceAuthenticated},
	{File: "human-channel-weak.yaml", Profiles: Profiles{HumanChannel: true}, Assurance: manifestv2.AssuranceWeak},
}

// manifestFor builds the manifest for a mode. Tool and event-kind
// declarations come from the same package-level definitions the running
// server registers (echoTool, eventKinds), so the manifest cannot attest
// something the handler does not serve; manifest_test.go also compares them
// through the live composed server.
func manifestFor(m manifestMode) (manifestv2.Manifest, error) {
	out := manifestv2.Manifest{
		SchemaVersion: manifestv2.SchemaVersion,
		Name:          "io.example/conformance-stub",
		Version:       "0.1.0",
		Description:   "Stub plugin for exercising host conformance across profiles.",
		Package: manifestv2.Package{
			RegistryType: manifestv2.RegistryTypeOCI,
			Identifier:   "ghcr.io/example/conformance-stub@sha256:" + stubDigest,
			Transport: manifestv2.Transport{
				Type: manifestv2.TransportStreamableHTTP,
				Port: 8080,
			},
		},
		Gleipnir: manifestv2.Gleipnir{
			Resources: &manifestv2.Resources{MemoryMB: 64, CPUMillicores: 100},
		},
	}

	if m.Profiles.ToolProvider {
		out.Gleipnir.Profiles.ToolProvider = &manifestv2.ToolProviderProfile{}
		out.Gleipnir.Tools = []manifestv2.ToolDecl{{Name: echoTool.Name}}
	}
	if m.Profiles.HumanChannel {
		out.Gleipnir.Profiles.HumanChannel = &manifestv2.HumanChannelProfile{Assurance: m.Assurance}
	}
	if m.Profiles.EventSource {
		out.Gleipnir.Profiles.EventSource = &manifestv2.EventSourceProfile{}
		for _, k := range eventKinds {
			decl, err := eventKindDecl(k.Kind, k.Guidance, k.BindingSchema, k.Operators)
			if err != nil {
				return manifestv2.Manifest{}, err
			}
			out.Gleipnir.EventKinds = append(out.Gleipnir.EventKinds, decl)
		}
	}
	return out, nil
}

func eventKindDecl(kind, guidance string, bindingSchema any, operators map[string][]string) (manifestv2.EventKindDecl, error) {
	var node yaml.Node
	if err := node.Encode(bindingSchema); err != nil {
		return manifestv2.EventKindDecl{}, fmt.Errorf("encode binding schema for %q: %w", kind, err)
	}
	return manifestv2.EventKindDecl{
		Kind:          kind,
		Description:   "A synthetic event emitted on a fixed schedule.",
		Guidance:      guidance,
		BindingSchema: &node,
		Operators:     operators,
	}, nil
}
