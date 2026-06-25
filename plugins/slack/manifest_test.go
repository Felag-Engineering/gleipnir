package main

import (
	"bytes"
	"os"
	"regexp"
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

// TestChannelMessageTextRegexBinding asserts that:
//  1. The reflected binding_schema for channel_message declares text_regex
//     as {type: string, format: regex} — the shape the host binding engine
//     requires to select OpRegex (internal/plugin/binding/binding.go:327).
//  2. A regex derived from that field behaves as documented: `^(?i)recipe:`
//     fires only when the message text *starts* with "Recipe:" or "recipe:",
//     not when the keyword appears mid-string.
//
// The slack module cannot import internal/plugin/binding (separate module,
// internal/ boundary), so we prove the AC by:
//   - Walking the reflected YAML schema to assert the format tag (schema
//     shape is what the host reads at policy-save time).
//   - Compiling and running the regex ourselves (RE2 behavior is stdlib
//     regexp; the host uses the same engine).
func TestChannelMessageTextRegexBinding(t *testing.T) {
	// --- Part 1: assert the reflected schema declares text_regex correctly ---

	node := manifest.MustReflectSchema(SlackChannelMessageBinding{})

	// findMappingValue walks a YAML mapping node looking for the given key and
	// returns its value node. Returns nil when not found.
	findMappingValue := func(mapping *yaml.Node, key string) *yaml.Node {
		// Mapping content is interleaved [key, value, key, value, ...].
		for i := 0; i+1 < len(mapping.Content); i += 2 {
			if mapping.Content[i].Value == key {
				return mapping.Content[i+1]
			}
		}
		return nil
	}

	// Drill into: <root>.properties.text_regex
	properties := findMappingValue(node, "properties")
	if properties == nil {
		t.Fatal("binding schema has no 'properties' key")
	}
	textRegex := findMappingValue(properties, "text_regex")
	if textRegex == nil {
		t.Fatal("binding schema has no 'text_regex' property; was manifest.go updated and manifest.yaml regenerated?")
	}

	typ := findMappingValue(textRegex, "type")
	if typ == nil || typ.Value != "string" {
		t.Errorf("text_regex: want type=string, got %v", typ)
	}
	format := findMappingValue(textRegex, "format")
	if format == nil || format.Value != "regex" {
		t.Errorf("text_regex: want format=regex, got %v", format)
	}

	// --- Part 2: prove the acceptance criteria with the regex itself ---
	//
	// The host selects OpRegex for format:regex and calls regexp.MatchString
	// (RE2). We reproduce that here to document the expected routing behavior
	// without importing the host engine.
	re := regexp.MustCompile(`^(?i)recipe:`)

	should := []string{
		"Recipe: find dinner tonight",
		"recipe: pasta ideas",
	}
	shouldNot := []string{
		"got a great Recipe: idea", // keyword not at start — not anchored
		"Lunch order is in.",       // no keyword at all
	}

	for _, msg := range should {
		if !re.MatchString(msg) {
			t.Errorf("expected regex to match %q but it did not", msg)
		}
	}
	for _, msg := range shouldNot {
		if re.MatchString(msg) {
			t.Errorf("expected regex NOT to match %q but it did", msg)
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
	for _, field := range []string{"text", "text_regex", "user"} {
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
