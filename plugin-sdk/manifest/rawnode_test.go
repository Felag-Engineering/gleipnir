package manifest_test

import (
	"bytes"
	"testing"

	"github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
)

// TestAllNodeFieldsRoundTrip exercises every *yaml.Node field across all four
// types that carry them (Manifest, ToolDecl, EventKindDecl, ChannelDecl).
// This closes the OutputSchema and PayloadSchema coverage gap present before
// the rawNode refactor, and validates that the new decode path is equivalent
// to the old alias-struct approach for each field.
//
// The test:
//  1. Unmarshals a YAML literal that sets all seven node-bearing fields.
//  2. Asserts each field is non-nil (and Examples has the right length).
//  3. Marshals back to YAML and asserts each schema's distinctive content is
//     present.
//  4. Unmarshals the marshal output again and re-marshals, asserting byte
//     equality (round-trip stability).
func TestAllNodeFieldsRoundTrip(t *testing.T) {
	// This YAML sets every *yaml.Node field in the manifest type hierarchy:
	//   Manifest:      config_schema, subscription_schema
	//   ToolDecl:      input_schema, output_schema
	//   EventKindDecl: binding_schema, payload_schema, examples (2 entries)
	//   ChannelDecl:   config_schema
	const src = `schema_version: v1
name: allfields-plugin
version: 0.1.0
services:
  channel: v1
  tool: v1
  trigger: v1
auth:
  mode: instance_credentials
  strategy: none
config_schema:
  description: manifest-config
  type: object
subscription_schema:
  description: manifest-subscription
  type: object
tools:
  - name: do_it
    description: a tool
    input_schema:
      description: tool-input
      type: object
    output_schema:
      description: tool-output
      type: object
event_kinds:
  - kind: thing_happened
    description: something happened
    binding_schema:
      description: event-binding
      type: object
    payload_schema:
      description: event-payload
      type: object
    examples:
      - name: alpha
        payload: {}
      - name: beta
        payload: {}
channels:
  - implements_notify: true
    config_schema:
      description: channel-config
      type: object
`

	var m manifest.Manifest
	if err := manifest.Unmarshal([]byte(src), &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	// --- assert all node fields are non-nil after unmarshal ---

	if m.ConfigSchema == nil {
		t.Error("Manifest.ConfigSchema is nil")
	}
	if m.SubscriptionSchema == nil {
		t.Error("Manifest.SubscriptionSchema is nil")
	}

	if len(m.Tools) == 0 {
		t.Fatal("Tools is empty")
	}
	tool := m.Tools[0]
	if tool.InputSchema == nil {
		t.Error("ToolDecl.InputSchema is nil")
	}
	if tool.OutputSchema == nil {
		t.Error("ToolDecl.OutputSchema is nil")
	}

	if len(m.EventKinds) == 0 {
		t.Fatal("EventKinds is empty")
	}
	ek := m.EventKinds[0]
	if ek.BindingSchema == nil {
		t.Error("EventKindDecl.BindingSchema is nil")
	}
	if ek.PayloadSchema == nil {
		t.Error("EventKindDecl.PayloadSchema is nil")
	}
	if len(ek.Examples) != 2 {
		t.Errorf("len(EventKindDecl.Examples) = %d, want 2", len(ek.Examples))
	}

	if len(m.Channels) == 0 {
		t.Fatal("Channels is empty")
	}
	if m.Channels[0].ConfigSchema == nil {
		t.Error("ChannelDecl.ConfigSchema is nil")
	}

	// --- marshal and assert each schema's distinctive content survives ---

	out1, err := manifest.Marshal(&m)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	checks := []struct {
		desc    string
		snippet string
	}{
		{"Manifest.ConfigSchema", "manifest-config"},
		{"Manifest.SubscriptionSchema", "manifest-subscription"},
		{"ToolDecl.InputSchema", "tool-input"},
		{"ToolDecl.OutputSchema", "tool-output"},
		{"EventKindDecl.BindingSchema", "event-binding"},
		{"EventKindDecl.PayloadSchema", "event-payload"},
		{"ChannelDecl.ConfigSchema", "channel-config"},
	}
	for _, c := range checks {
		if !bytes.Contains(out1, []byte(c.snippet)) {
			t.Errorf("%s: content %q missing from marshal output:\n%s", c.desc, c.snippet, out1)
		}
	}

	// --- Marshal → Unmarshal → Marshal is bytes.Equal (round-trip stability) ---

	var m2 manifest.Manifest
	if err := manifest.Unmarshal(out1, &m2); err != nil {
		t.Fatalf("second Unmarshal: %v", err)
	}
	out2, err := manifest.Marshal(&m2)
	if err != nil {
		t.Fatalf("second Marshal: %v", err)
	}
	if !bytes.Equal(out1, out2) {
		t.Fatalf("round-trip not stable:\nfirst marshal:\n%s\nsecond marshal:\n%s", out1, out2)
	}
}
