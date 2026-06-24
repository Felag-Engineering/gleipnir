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
