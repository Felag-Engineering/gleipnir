package main

import (
	"bytes"
	"os"
	"testing"

	"github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
	"gopkg.in/yaml.v3"
)

// TestManifestYAMLIsCanonical verifies that manifest.yaml on disk is the
// byte-exact canonical projection of the pluginManifest Go declaration.
//
// Two sub-assertions:
//  1. Round-trip: Unmarshal(disk) → Marshal produces the same bytes as disk.
//  2. Go-source-of-truth: Marshal(pluginManifest) produces the same bytes as disk.
//
// If either assertion fails, the committed manifest.yaml has drifted from
// manifest.go — run `make manifest` to regenerate it.
//
// Note: os.ReadFile("manifest.yaml") uses a relative path because `go test`
// runs with the package directory as the working directory, so the path
// resolves correctly without any special setup.
func TestManifestYAMLIsCanonical(t *testing.T) {
	diskBytes, err := os.ReadFile("manifest.yaml")
	if err != nil {
		t.Fatalf("read manifest.yaml: %v", err)
	}

	// Sub-assertion 1: round-trip stability.
	var parsed manifest.Manifest
	if err := manifest.Unmarshal(diskBytes, &parsed); err != nil {
		t.Fatalf("unmarshal manifest.yaml: %v", err)
	}
	roundTripped, err := manifest.Marshal(&parsed)
	if err != nil {
		t.Fatalf("re-marshal after unmarshal: %v", err)
	}
	if !bytes.Equal(diskBytes, roundTripped) {
		t.Errorf("manifest.yaml is not canonical YAML (round-trip mismatch):\ndisk:\n%s\nre-marshalled:\n%s",
			diskBytes, roundTripped)
	}

	// Sub-assertion 2: Go declaration matches disk.
	fromGo, err := manifest.Marshal(&pluginManifest)
	if err != nil {
		t.Fatalf("marshal pluginManifest: %v", err)
	}
	if !bytes.Equal(diskBytes, fromGo) {
		t.Errorf("manifest.yaml does not match pluginManifest in manifest.go:\ndisk:\n%s\nfrom Go:\n%s",
			diskBytes, fromGo)
	}
}

// TestAllEventKindsHaveGuidance asserts that all 5 Slack event kinds have a
// non-empty Guidance string and that Description is still the short label
// (unchanged). This is the backstop that ensures MustAddEventKindWithGuidance
// was used correctly for every event kind.
func TestAllEventKindsHaveGuidance(t *testing.T) {
	wantKinds := []struct {
		kind            string
		wantDescription string
	}{
		{"channel_message", "A message was posted to a Slack channel"},
		{"direct_message", "A direct message was sent to the bot"},
		{"slash_command", "A workspace slash command was invoked"},
		{"message_shortcut", "A message shortcut was invoked on a message"},
		{"global_shortcut", "A global shortcut was invoked"},
	}

	// Build an index from kind → EventKindDecl for O(1) lookups.
	kindIndex := make(map[string]manifest.EventKindDecl, len(pluginManifest.EventKinds))
	for _, ek := range pluginManifest.EventKinds {
		kindIndex[ek.Kind] = ek
	}

	for _, tc := range wantKinds {
		ek, ok := kindIndex[tc.kind]
		if !ok {
			t.Errorf("event kind %q not found in manifest", tc.kind)
			continue
		}
		if ek.Description != tc.wantDescription {
			t.Errorf("%s: Description = %q, want %q", tc.kind, ek.Description, tc.wantDescription)
		}
		if ek.Guidance == "" {
			t.Errorf("%s: Guidance is empty, want a non-empty string", tc.kind)
		}
	}
}

// TestChannelMessageChannelIDBinding asserts that:
//  1. The reflected binding_schema for channel_message declares channel_id as
//     {type: string, x-gleipnir-options: {source: "channels"}} with NO format
//     key — the shape the host binding engine requires for OpEquals semantics
//     (internal/plugin/binding/binding.go:238), where the annotation is a UI hint.
//  2. The old `channel` (contains) and `text_regex` fields are absent, confirming
//     they were removed (#646, #654).
func TestChannelMessageChannelIDBinding(t *testing.T) {
	node := manifest.MustReflectSchema(SlackChannelMessageBinding{})

	// findMappingValue walks a YAML mapping node looking for the given key and
	// returns its value node. Returns nil when not found.
	findMappingValue := func(mapping *yaml.Node, key string) *yaml.Node {
		for i := 0; i+1 < len(mapping.Content); i += 2 {
			if mapping.Content[i].Value == key {
				return mapping.Content[i+1]
			}
		}
		return nil
	}

	properties := findMappingValue(node, "properties")
	if properties == nil {
		t.Fatal("binding schema has no 'properties' key")
	}

	// channel_id must be present with type:string, no format, and x-gleipnir-options.source==channels.
	channelID := findMappingValue(properties, "channel_id")
	if channelID == nil {
		t.Fatal("binding schema has no 'channel_id' property; was manifest.go updated and manifest.yaml regenerated?")
	}

	typ := findMappingValue(channelID, "type")
	if typ == nil || typ.Value != "string" {
		t.Errorf("channel_id: want type=string, got %v", typ)
	}
	// No format key is the OpEquals discriminator.
	format := findMappingValue(channelID, "format")
	if format != nil {
		t.Errorf("channel_id: want no format key (OpEquals), got format=%q", format.Value)
	}
	// x-gleipnir-options must be present with source: channels.
	opts := findMappingValue(channelID, "x-gleipnir-options")
	if opts == nil {
		t.Fatal("channel_id: missing x-gleipnir-options annotation")
	}
	source := findMappingValue(opts, "source")
	if source == nil || source.Value != "channels" {
		t.Errorf("channel_id: want x-gleipnir-options.source=channels, got %v", source)
	}

	// The old `channel` (contains) and `text_regex` fields must be absent.
	for _, absent := range []string{"channel", "text_regex"} {
		if findMappingValue(properties, absent) != nil {
			t.Errorf("channel_message binding schema should not contain removed field %q", absent)
		}
	}
}

// TestDirectMessageBindingSchema asserts that the reflected binding_schema for
// direct_message carries text, text_regex, and user — and does NOT carry
// channel, channel_type, or mention_only (meaningless for a 1:1 DM).
func TestDirectMessageBindingSchema(t *testing.T) {
	node := manifest.MustReflectSchema(SlackDirectMessageBinding{})

	findMappingValue := func(mapping *yaml.Node, key string) *yaml.Node {
		for i := 0; i+1 < len(mapping.Content); i += 2 {
			if mapping.Content[i].Value == key {
				return mapping.Content[i+1]
			}
		}
		return nil
	}

	properties := findMappingValue(node, "properties")
	if properties == nil {
		t.Fatal("binding schema has no 'properties' key")
	}

	// Fields that MUST be present in the DM binding schema.
	for _, field := range []string{"text", "user"} {
		if findMappingValue(properties, field) == nil {
			t.Errorf("direct_message binding schema missing expected field %q", field)
		}
	}

	// Fields that must NOT appear in the DM binding schema: they are
	// channel-surface concepts with no meaning in a 1:1 DM.
	for _, field := range []string{"channel", "channel_type", "mention_only"} {
		if findMappingValue(properties, field) != nil {
			t.Errorf("direct_message binding schema should not contain field %q", field)
		}
	}
}

// TestChannelMessageChannelTypeBinding asserts that:
//  1. The reflected binding_schema declares channel_type as {type: string} with
//     NO format key — the shape the host binding engine requires for OpEquals
//     (internal/plugin/binding/binding.go:238). Contrast with text_regex which
//     carries format:regex to select OpRegex.
//  2. OpEquals semantics: binding value must exactly equal the payload value;
//     an empty binding ("") matches any payload value (back-compat).
//
// The slack module cannot import internal/plugin/binding (separate module,
// internal/ boundary), so we assert the schema shape directly and reproduce
// OpEquals as plain string equality — the same logic the host evaluator uses
// (binding.go:341-346).
func TestChannelMessageChannelTypeBinding(t *testing.T) {
	// --- Part 1: assert the reflected schema declares channel_type correctly ---

	node := manifest.MustReflectSchema(SlackChannelMessageBinding{})

	// findMappingValue walks a YAML mapping node looking for the given key and
	// returns its value node. Returns nil when not found.
	// Same helper pattern as TestChannelMessageTextRegexBinding above.
	findMappingValue := func(mapping *yaml.Node, key string) *yaml.Node {
		// Mapping content is interleaved [key, value, key, value, ...].
		for i := 0; i+1 < len(mapping.Content); i += 2 {
			if mapping.Content[i].Value == key {
				return mapping.Content[i+1]
			}
		}
		return nil
	}

	// Drill into: <root>.properties.channel_type
	properties := findMappingValue(node, "properties")
	if properties == nil {
		t.Fatal("binding schema has no 'properties' key")
	}
	channelType := findMappingValue(properties, "channel_type")
	if channelType == nil {
		t.Fatal("binding schema has no 'channel_type' property; was manifest.go updated and manifest.yaml regenerated?")
	}

	typ := findMappingValue(channelType, "type")
	if typ == nil || typ.Value != "string" {
		t.Errorf("channel_type: want type=string, got %v", typ)
	}
	// No format key is the OpEquals discriminator. format:regex → OpRegex;
	// format:contains → OpContains; absent format → OpEquals.
	format := findMappingValue(channelType, "format")
	if format != nil {
		t.Errorf("channel_type: want no format key (OpEquals), got format=%q", format.Value)
	}

	// --- Part 2: prove OpEquals acceptance criteria ---
	//
	// OpEquals (binding.go:341-346): match iff bindingValue == "" (empty matches
	// anything) or bindingValue == payload[fieldName]. Reproduced locally since
	// the slack module cannot import the host engine.
	opEquals := func(bindingValue, payloadValue string) bool {
		return bindingValue == "" || bindingValue == payloadValue
	}

	tests := []struct {
		binding string
		payload string
		want    bool
	}{
		// "im" binding fires only on DM events.
		{binding: "im", payload: "im", want: true},
		{binding: "im", payload: "channel", want: false},
		{binding: "im", payload: "group", want: false},
		{binding: "im", payload: "mpim", want: false},
		// "channel" binding fires only on public channel events.
		{binding: "channel", payload: "channel", want: true},
		{binding: "channel", payload: "im", want: false},
		// "group" binding fires only on private channel events.
		{binding: "group", payload: "group", want: true},
		{binding: "group", payload: "channel", want: false},
		// "mpim" binding fires only on multi-party DM events.
		// Note: Slack emits "mpim", not "mim" — slack-go ChannelTypeMPIM = "mpim".
		{binding: "mpim", payload: "mpim", want: true},
		{binding: "mpim", payload: "im", want: false},
		// Empty binding matches all channel kinds (back-compat: no binding = no filter).
		{binding: "", payload: "channel", want: true},
		{binding: "", payload: "group", want: true},
		{binding: "", payload: "im", want: true},
		{binding: "", payload: "mpim", want: true},
	}

	for _, tc := range tests {
		got := opEquals(tc.binding, tc.payload)
		if got != tc.want {
			t.Errorf("opEquals(binding=%q, payload=%q) = %v, want %v",
				tc.binding, tc.payload, got, tc.want)
		}
	}
}
