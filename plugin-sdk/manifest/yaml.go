package manifest

import (
	"bytes"
	"fmt"
	"sort"

	"gopkg.in/yaml.v3"
)

// Marshal serialises m into deterministic YAML: sorted mapping keys, 2-space
// indent, and Unix line endings. Because signing hashes the raw manifest bytes,
// the emitter must produce byte-identical output for the same Go declarations.
//
// The canonical rules are:
//   - Mapping keys are sorted lexicographically at every level.
//   - Sequences preserve declaration order (tools, event_kinds, etc.).
//   - Indent width is exactly 2 spaces.
//   - Output ends with a single newline character.
func Marshal(m *Manifest) ([]byte, error) {
	// First marshal through yaml.v3 to get a *yaml.Node tree, then normalise
	// the tree in-place before encoding to bytes.
	var root yaml.Node
	if err := root.Encode(m); err != nil {
		return nil, fmt.Errorf("manifest marshal: encode to node: %w", err)
	}

	// root is a document node; normalise its content.
	sortMappingNode(&root)

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&root); err != nil {
		return nil, fmt.Errorf("manifest marshal: encode to bytes: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("manifest marshal: close encoder: %w", err)
	}

	// yaml.Encoder always ends with a newline; ensure exactly one.
	out := bytes.TrimRight(buf.Bytes(), "\n")
	out = append(out, '\n')
	return out, nil
}

// Unmarshal parses canonical (or non-canonical) YAML into m.
func Unmarshal(data []byte, m *Manifest) error {
	if err := yaml.Unmarshal(data, m); err != nil {
		return fmt.Errorf("manifest unmarshal: %w", err)
	}
	return nil
}

// sortMappingNode recursively sorts all mapping nodes in the YAML node tree
// lexicographically by key. Sequence nodes are not reordered (declaration
// order is meaningful for tools, event_kinds, etc.).
func sortMappingNode(n *yaml.Node) {
	switch n.Kind {
	case yaml.DocumentNode:
		for _, child := range n.Content {
			sortMappingNode(child)
		}

	case yaml.MappingNode:
		// Mapping content is stored as alternating key/value pairs.
		// Group into (key, value) pairs, sort by key, then flatten back.
		type kv struct {
			key *yaml.Node
			val *yaml.Node
		}
		pairs := make([]kv, 0, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			pairs = append(pairs, kv{n.Content[i], n.Content[i+1]})
		}
		sort.SliceStable(pairs, func(i, j int) bool {
			return pairs[i].key.Value < pairs[j].key.Value
		})
		for i, p := range pairs {
			n.Content[i*2] = p.key
			n.Content[i*2+1] = p.val
			sortMappingNode(p.val)
		}

	case yaml.SequenceNode:
		for _, child := range n.Content {
			sortMappingNode(child)
		}
	}
}
