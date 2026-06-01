package manifest_test

import (
	"bytes"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
)

// canonicalManifestYAML is the known-good canonical YAML for a minimal
// manifest. Used to pin byte-identical output across code paths.
const canonicalManifestYAML = `auth:
  mode: instance_credentials
  strategy: static_key
name: testplugin
schema_version: v1
services:
  tool: v1
version: 1.0.0
`

// minimalManifestJSON is the JSON equivalent of canonicalManifestYAML (key
// order intentionally scrambled to verify canonicalization).
const minimalManifestJSON = `{
  "version": "1.0.0",
  "name": "testplugin",
  "schema_version": "v1",
  "services": {"tool": "v1"},
  "auth": {"mode": "instance_credentials", "strategy": "static_key"}
}`

// minimalManifestYAMLUnsorted is YAML with keys in non-canonical order.
const minimalManifestYAMLUnsorted = `version: "1.0.0"
name: testplugin
schema_version: v1
services:
  tool: v1
auth:
  mode: instance_credentials
  strategy: static_key
`

// TestCanonicalize is a table-driven test covering the main Canonicalize paths.
func TestCanonicalize(t *testing.T) {
	tests := []struct {
		name        string
		input       []byte
		wantOutput  []byte // if set, assert byte-equal
		wantContain string // if set, assert output contains this substring
		wantErr     string // if set, assert error message contains this
	}{
		{
			name:       "JSON unordered keys → canonical YAML",
			input:      []byte(minimalManifestJSON),
			wantOutput: []byte(canonicalManifestYAML),
		},
		{
			name:       "YAML unsorted keys → canonical YAML",
			input:      []byte(minimalManifestYAMLUnsorted),
			wantOutput: []byte(canonicalManifestYAML),
		},
		{
			name:       "already-canonical YAML → byte-identical (idempotent)",
			input:      []byte(canonicalManifestYAML),
			wantOutput: []byte(canonicalManifestYAML),
		},
		{
			name: "key-ordering normalization: zebra_tool before alpha_tool → sorted output",
			input: []byte(`schema_version: v1
name: myplugin
version: 2.0.0
services:
  tool: v1
auth:
  mode: instance_credentials
  strategy: static_key
tools:
  - name: zebra_tool
    description: Does zebra things
  - name: alpha_tool
    description: Does alpha things
`),
			// Tools sequence order is preserved; mapping keys within each tool are sorted.
			wantContain: "name: zebra_tool",
		},
		{
			name:    "malformed JSON starting with { → parse error",
			input:   []byte(`{"broken": `),
			wantErr: "parse JSON:",
		},
		{
			name:    "malformed YAML → unmarshal error",
			input:   []byte("key: [\nbad"),
			wantErr: "manifest unmarshal:",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := manifest.Canonicalize(tc.input)

			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tc.wantOutput != nil && !bytes.Equal(got, tc.wantOutput) {
				t.Fatalf("output mismatch:\ngot:\n%s\nwant:\n%s", got, tc.wantOutput)
			}

			if tc.wantContain != "" && !bytes.Contains(got, []byte(tc.wantContain)) {
				t.Fatalf("output does not contain %q:\n%s", tc.wantContain, got)
			}
		})
	}
}

// TestCanonicalizePreservesSchemaNodes verifies that *yaml.Node fields
// (InputSchema on a ToolDecl, ConfigSchema on the manifest) survive the JSON
// path through Canonicalize. This exercises the load-bearing JSON→generic→YAML
// round-trip: without the intermediate yaml.Marshal step, yaml.v3 would leave
// these *yaml.Node fields nil when decoding from a JSON source.
func TestCanonicalizePreservesSchemaNodes(t *testing.T) {
	// A JSON manifest with both config_schema and a tool with input_schema.
	jsonInput := []byte(`{
		"schema_version": "v1",
		"name": "schema-plugin",
		"version": "1.0.0",
		"services": {"tool": "v1"},
		"auth": {"mode": "instance_credentials", "strategy": "static_key"},
		"config_schema": {
			"type": "object",
			"properties": {
				"api_key": {"type": "string"}
			}
		},
		"tools": [
			{
				"name": "do_thing",
				"description": "does a thing",
				"input_schema": {
					"type": "object",
					"properties": {
						"target": {"type": "string"}
					}
				}
			}
		]
	}`)

	out, err := manifest.Canonicalize(jsonInput)
	if err != nil {
		t.Fatalf("Canonicalize: %v", err)
	}

	// Unmarshal the canonical output and check node fields are populated.
	var m manifest.Manifest
	if err := manifest.Unmarshal(out, &m); err != nil {
		t.Fatalf("Unmarshal canonical output: %v", err)
	}

	if m.ConfigSchema == nil {
		t.Error("ConfigSchema is nil after JSON Canonicalize round-trip")
	}
	if len(m.Tools) == 0 {
		t.Fatal("Tools is empty after JSON Canonicalize round-trip")
	}
	if m.Tools[0].InputSchema == nil {
		t.Errorf("Tools[0].InputSchema is nil after JSON Canonicalize round-trip")
	}

	// The canonical bytes must contain the schema content.
	if !bytes.Contains(out, []byte("type: object")) {
		t.Errorf("canonical output missing 'type: object'; output:\n%s", out)
	}
}

// TestCanonicalizeConvergence asserts that JSON and YAML representations of the
// same manifest produce byte-identical canonical output.
func TestCanonicalizeConvergence(t *testing.T) {
	gotJSON, err := manifest.Canonicalize([]byte(minimalManifestJSON))
	if err != nil {
		t.Fatalf("Canonicalize JSON: %v", err)
	}

	gotYAML, err := manifest.Canonicalize([]byte(minimalManifestYAMLUnsorted))
	if err != nil {
		t.Fatalf("Canonicalize YAML: %v", err)
	}

	if !bytes.Equal(gotJSON, gotYAML) {
		t.Fatalf("JSON and YAML paths produced different output:\nJSON path:\n%s\nYAML path:\n%s", gotJSON, gotYAML)
	}
}

// TestCanonicalizeSortedMappingKeys verifies that every mapping in the
// canonical output has lexicographically ordered keys.
func TestCanonicalizeSortedMappingKeys(t *testing.T) {
	out, err := manifest.Canonicalize([]byte(minimalManifestJSON))
	if err != nil {
		t.Fatalf("Canonicalize: %v", err)
	}

	var root yaml.Node
	if err := yaml.Unmarshal(out, &root); err != nil {
		t.Fatalf("yaml.Unmarshal canonical output: %v", err)
	}
	if err := assertSortedMappingKeys(&root); err != nil {
		t.Fatalf("keys not sorted: %v\n\nYAML:\n%s", err, out)
	}
}

// assertSortedMappingKeys is a local copy of the walk helper from yaml_test.go
// to keep this file self-contained without depending on unexported test symbols.
func assertSortedMappingKeys(n *yaml.Node) error {
	switch n.Kind {
	case yaml.DocumentNode:
		for _, c := range n.Content {
			if err := assertSortedMappingKeys(c); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		var prev string
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i].Value
			if key < prev {
				return &unsortedError{prev: prev, cur: key}
			}
			prev = key
			if err := assertSortedMappingKeys(n.Content[i+1]); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for _, c := range n.Content {
			if err := assertSortedMappingKeys(c); err != nil {
				return err
			}
		}
	}
	return nil
}

type unsortedError struct{ prev, cur string }

func (e *unsortedError) Error() string {
	return "key " + e.cur + " comes after " + e.prev + " (want ascending order)"
}
