package manifest_test

import (
	"bytes"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
)

// sampleManifest returns a non-trivial Manifest for determinism tests.
func sampleManifest() *manifest.Manifest {
	return &manifest.Manifest{
		SchemaVersion: "v1",
		Name:          "testplugin",
		Version:       "1.0.0",
		Description:   "A test plugin",
		Author:        "Test Author",
		Services: manifest.Services{
			Tool: "v1",
		},
		Auth: manifest.AuthDecl{
			Mode:     "instance_credentials",
			Strategy: "static_api_key",
		},
		Tools: []manifest.ToolDecl{
			{Name: "zebra_tool", Description: "Does zebra things"},
			{Name: "alpha_tool", Description: "Does alpha things"},
		},
	}
}

// TestMarshalTwiceByteEqual verifies that marshalling the same Manifest twice
// produces identical bytes.
func TestMarshalTwiceByteEqual(t *testing.T) {
	m := sampleManifest()

	out1, err := manifest.Marshal(m)
	if err != nil {
		t.Fatalf("first marshal: %v", err)
	}
	out2, err := manifest.Marshal(m)
	if err != nil {
		t.Fatalf("second marshal: %v", err)
	}

	if !bytes.Equal(out1, out2) {
		t.Fatalf("marshal not deterministic:\nfirst:\n%s\nsecond:\n%s", out1, out2)
	}
}

// TestRoundTripStable verifies that Marshal → Unmarshal → Marshal produces
// the same bytes as the first Marshal (round-trip stability).
func TestRoundTripStable(t *testing.T) {
	m := sampleManifest()

	first, err := manifest.Marshal(m)
	if err != nil {
		t.Fatalf("first marshal: %v", err)
	}

	var parsed manifest.Manifest
	if err := manifest.Unmarshal(first, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	second, err := manifest.Marshal(&parsed)
	if err != nil {
		t.Fatalf("second marshal: %v", err)
	}

	if !bytes.Equal(first, second) {
		t.Fatalf("round-trip not stable:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

// TestKeysSorted verifies that the canonical YAML has all mapping keys in
// ascending lexicographic order at every level.
func TestKeysSorted(t *testing.T) {
	m := sampleManifest()

	out, err := manifest.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Decode back as a raw node and check that every mapping has sorted keys.
	var root yaml.Node
	if err := yaml.Unmarshal(out, &root); err != nil {
		t.Fatalf("yaml unmarshal for key check: %v", err)
	}
	if err := assertSortedMapping(&root); err != nil {
		t.Fatalf("keys not sorted:\n%v\n\nYAML:\n%s", err, out)
	}
}

// assertSortedMapping walks a yaml.Node tree and returns an error if any
// mapping node has keys out of lexicographic order.
func assertSortedMapping(n *yaml.Node) error {
	switch n.Kind {
	case yaml.DocumentNode:
		for _, c := range n.Content {
			if err := assertSortedMapping(c); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		var prev string
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i].Value
			if key < prev {
				return &unsortedKeyError{prev: prev, cur: key}
			}
			prev = key
			if err := assertSortedMapping(n.Content[i+1]); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for _, c := range n.Content {
			if err := assertSortedMapping(c); err != nil {
				return err
			}
		}
	}
	return nil
}

type unsortedKeyError struct{ prev, cur string }

func (e *unsortedKeyError) Error() string {
	return "key " + e.cur + " comes after " + e.prev + " (want ascending order)"
}

// TestIndentAndLineEndings verifies exactly 2-space indent and a single
// trailing newline.
func TestIndentAndLineEndings(t *testing.T) {
	m := sampleManifest()

	out, err := manifest.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Must end with exactly one newline.
	if !bytes.HasSuffix(out, []byte("\n")) {
		t.Error("output does not end with newline")
	}
	if bytes.HasSuffix(out, []byte("\n\n")) {
		t.Error("output ends with more than one newline")
	}

	// Every non-empty line that is indented must use spaces (not tabs) and
	// indentation must be a multiple of 2.
	for i, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		trimmed := strings.TrimLeft(line, " ")
		indent := len(line) - len(trimmed)
		if indent%2 != 0 {
			t.Errorf("line %d: indent %d is not a multiple of 2: %q", i+1, indent, line)
		}
		// Ensure no tabs.
		if strings.ContainsRune(line, '\t') {
			t.Errorf("line %d: contains tab: %q", i+1, line)
		}
	}
}

// TestHasTier2 verifies the HasTier2 helper against declared and undeclared
// capability strings, including the tier2_capabilities YAML key round-trip.
func TestHasTier2(t *testing.T) {
	t.Parallel()

	yamlSrc := `schema_version: v1
name: testplugin
version: 1.0.0
auth:
  mode: instance_credentials
  strategy: none
services:
  tool: v1
tier2_capabilities:
  - run_history_read
`
	var m manifest.Manifest
	if err := manifest.Unmarshal([]byte(yamlSrc), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !m.HasTier2(manifest.Tier2RunHistoryRead) {
		t.Error("HasTier2(run_history_read) = false, want true")
	}
	if m.HasTier2(manifest.Tier2UserDirectoryRead) {
		t.Error("HasTier2(user_directory_read) = true, want false (not declared)")
	}
	if m.HasTier2("nonexistent") {
		t.Error("HasTier2(nonexistent) = true, want false")
	}
}

// TestHasTier2_Empty verifies HasTier2 returns false for a manifest with no
// tier2_capabilities declared.
func TestHasTier2_Empty(t *testing.T) {
	t.Parallel()

	m := sampleManifest()
	if m.HasTier2(manifest.Tier2RunHistoryRead) {
		t.Error("HasTier2 = true on manifest with no tier2 capabilities")
	}
}

// TestYAMLNodeFieldsPreserved verifies that *yaml.Node fields (like
// ConfigSchema) survive a Marshal → Unmarshal round-trip intact.
func TestYAMLNodeFieldsPreserved(t *testing.T) {
	schemaYAML := "type: object\nproperties:\n  channel:\n    type: string\n"

	var schemaNode yaml.Node
	if err := yaml.Unmarshal([]byte(schemaYAML), &schemaNode); err != nil {
		t.Fatalf("parse schema yaml: %v", err)
	}
	// yaml.Unmarshal produces a document node; store the actual content node.
	contentNode := schemaNode.Content[0]

	m := sampleManifest()
	m.ConfigSchema = contentNode

	out, err := manifest.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var parsed manifest.Manifest
	if err := manifest.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if parsed.ConfigSchema == nil {
		t.Fatal("ConfigSchema is nil after round-trip")
	}
	// The re-marshalled schema should contain the key "type".
	second, err := manifest.Marshal(&parsed)
	if err != nil {
		t.Fatalf("second marshal: %v", err)
	}
	if !bytes.Contains(second, []byte("type: object")) {
		t.Errorf("config_schema content not preserved; output:\n%s", second)
	}
}
