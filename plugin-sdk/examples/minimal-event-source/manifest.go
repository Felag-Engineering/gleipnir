package main

import "github.com/felag-engineering/gleipnir/plugin-sdk/manifestv2"

// exampleDigest is a placeholder image digest — this example is never built
// into a real container image, so nothing ever pulls it. A real plugin's
// digest is produced by its own build pipeline and pinned here for real.
const exampleDigest = "0123456789abcdef" + "0123456789abcdef" + "0123456789abcdef" + "0123456789abcdef"

// pluginManifest declares minimal-event-source's metadata and its single
// event_source profile. It is the source of truth for manifest.yaml (see
// manifest_test.go): declared in Go here, projected to canonical YAML there.
//
// Gleipnir.EventKinds below is a separate declaration from the eventKinds
// slice in main.go — this package cannot generalize the two into one
// definition, because manifestv2.EventKindDecl and events.Kind describe
// different things (a signed admin-facing attestation vs. a live discovery
// rendering) for two different build artifacts (this manifest.yaml file vs.
// the handler running inside the container). manifest_test.go's
// TestManifestEventKindsMatchHandlerKinds is what keeps them from drifting
// apart instead.
var pluginManifest = manifestv2.Manifest{
	SchemaVersion: manifestv2.SchemaVersion,
	Name:          "io.github.example/minimal-event-source",
	Version:       "0.1.0",
	Description:   "Demonstration plugin emitting a single event kind via io.gleipnir/events.",
	Package: manifestv2.Package{
		RegistryType: manifestv2.RegistryTypeOCI,
		Identifier:   "ghcr.io/example/minimal-event-source@sha256:" + exampleDigest,
		Transport: manifestv2.Transport{
			Type: manifestv2.TransportStreamableHTTP,
			Port: 8080,
		},
	},
	Gleipnir: manifestv2.Gleipnir{
		Profiles: manifestv2.Profiles{
			EventSource: &manifestv2.EventSourceProfile{},
		},
		EventKinds: []manifestv2.EventKindDecl{
			{
				Kind:        "example.ping",
				Description: "A demonstration event.",
				Guidance:    "Fires once per publish, on this example's own ticker loop.",
			},
		},
	},
}
