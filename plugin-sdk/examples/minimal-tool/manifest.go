package main

import "github.com/felag-engineering/gleipnir/plugin-sdk/manifest"

// pluginManifest declares the minimal-tool plugin's metadata and capabilities.
// In a real plugin binary this is passed to serve.EmitManifest(); it is shown
// here for documentation only.
var pluginManifest = manifest.Manifest{
	SchemaVersion: "v1",
	Name:          "minimal-tool",
	Version:       "0.1.0",
	Description:   "Demonstration plugin with a single echo tool.",
	Services: manifest.Services{
		Tool: "v1",
	},
	Auth: manifest.AuthDecl{
		Mode:     "instance_credentials",
		Strategy: "none",
	},
	Tools: []manifest.ToolDecl{
		{
			Name:        "echo",
			Description: "Returns the message it received.",
		},
	},
}
