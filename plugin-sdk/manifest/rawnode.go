package manifest

import "gopkg.in/yaml.v3"

// rawNode is a decode-only wrapper that concentrates the yaml.v3 "*yaml.Node
// struct fields decode as nil" quirk in one place. When yaml.v3 decodes into a
// struct, pointer fields of type *yaml.Node are never populated — the decoder
// skips them. Using rawNode as the intermediate field type works around this:
// UnmarshalYAML receives the concrete *yaml.Node and stores a copy.
//
// Usage: declare a local plain struct inside each UnmarshalYAML method with
// node-bearing fields typed as rawNode (or []rawNode). After decoding, access
// the node via .Node and assign to the public *yaml.Node field.
//
// rawNode is intentionally unexported and carries no MarshalYAML — it is used
// only inside per-method decode structs and never reaches the encode path.
type rawNode struct {
	Node *yaml.Node
}

// UnmarshalYAML copies the incoming node value into r.Node. The copy (n := *v)
// detaches the node from yaml.v3's internal decode state so the caller can
// safely store a pointer to it.
func (r *rawNode) UnmarshalYAML(v *yaml.Node) error {
	n := *v
	r.Node = &n
	return nil
}
