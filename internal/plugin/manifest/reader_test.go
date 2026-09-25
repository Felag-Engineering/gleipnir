package manifest_test

import (
	"reflect"
	"testing"

	pluginmanifest "github.com/felag-engineering/gleipnir/internal/plugin/manifest"
	"github.com/felag-engineering/gleipnir/internal/plugin/schemautil"
	"gopkg.in/yaml.v3"
)

const readerTestDigest = "@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// twinV1Manifest and twinV2Manifest declare the same plugin — a trigger
// source with an OAuth2 credential and one Tier-2 capability — by hand, once
// per schema version. They exist to prove Read produces the SAME Snapshot
// fields regardless of which format wrote them; a translation bug that only
// one format's tests would catch is exactly what a reader with two call sites
// (instead of one) would let through.
const twinV1Manifest = `
schema_version: "1.0"
name: acme-notify
version: 1.0.0
services:
  trigger: v1
auth:
  strategy: oauth2_authcode
  oauth_defaults:
    authorization_url: https://acme.example.com/oauth/authorize
    token_url: https://acme.example.com/oauth/token
    scopes: [read, write]
tier2_capabilities:
  - run_history_read
config_schema:
  type: object
  properties:
    workspace:
      type: string
event_kinds:
  - kind: message.posted
    description: A message was posted.
    guidance: Fires once per message posted to a channel this instance watches.
    binding_schema:
      type: object
      properties:
        channel:
          type: string
`

const twinV2Manifest = `
schema_version: "2"
name: acme-notify
version: 1.0.0
package:
  registry_type: oci
  identifier: ghcr.io/acme/notify` + readerTestDigest + `
  transport:
    type: streamable-http
    port: 8080
gleipnir:
  profiles:
    tool_provider: {}
    event_source: {}
  auth:
    strategy: oauth2_authcode
    oauth_defaults:
      authorization_url: https://acme.example.com/oauth/authorize
      token_url: https://acme.example.com/oauth/token
      scopes: [read, write]
  tier2_capabilities:
    - run_history_read
  config_schema:
    type: object
    properties:
      workspace:
        type: string
  event_kinds:
    - kind: message.posted
      description: A message was posted.
      guidance: Fires once per message posted to a channel this instance watches.
      binding_schema:
        type: object
        properties:
          channel:
            type: string
`

func TestRead_DispatchesOnVersion(t *testing.T) {
	v1, err := pluginmanifest.Read([]byte(twinV1Manifest))
	if err != nil {
		t.Fatalf("Read(v1): %v", err)
	}
	if v1.Version != 1 {
		t.Errorf("v1.Version = %d, want 1", v1.Version)
	}

	v2, err := pluginmanifest.Read([]byte(twinV2Manifest))
	if err != nil {
		t.Fatalf("Read(v2): %v", err)
	}
	if v2.Version != 2 {
		t.Errorf("v2.Version = %d, want 2", v2.Version)
	}
}

// The v1/v2 twin pair must yield equal EventKinds, ConfigSchema and Auth —
// the fields both formats declare today. Schema nodes carry line/column
// metadata that differs by source document even when the schema itself is
// identical, so schemas are compared as canonical JSON bytes rather than by
// reflect.DeepEqual on the *yaml.Node tree.
func TestRead_V1V2TwinsAgree(t *testing.T) {
	v1, err := pluginmanifest.Read([]byte(twinV1Manifest))
	if err != nil {
		t.Fatalf("Read(v1): %v", err)
	}
	v2, err := pluginmanifest.Read([]byte(twinV2Manifest))
	if err != nil {
		t.Fatalf("Read(v2): %v", err)
	}

	if !reflect.DeepEqual(v1.Auth, v2.Auth) {
		t.Errorf("Auth differs:\nv1: %+v\nv2: %+v", v1.Auth, v2.Auth)
	}
	if !reflect.DeepEqual(v1.Tier2Capabilities, v2.Tier2Capabilities) {
		t.Errorf("Tier2Capabilities differs: v1=%v v2=%v", v1.Tier2Capabilities, v2.Tier2Capabilities)
	}

	assertSameSchemaBytes(t, "ConfigSchema", v1.ConfigSchema, v2.ConfigSchema)

	if len(v1.EventKinds) != 1 || len(v2.EventKinds) != 1 {
		t.Fatalf("EventKinds length: v1=%d v2=%d, want 1 each", len(v1.EventKinds), len(v2.EventKinds))
	}
	v1Kind, v2Kind := v1.EventKinds[0], v2.EventKinds[0]
	if v1Kind.Kind != v2Kind.Kind {
		t.Errorf("Kind differs: v1=%q v2=%q", v1Kind.Kind, v2Kind.Kind)
	}
	if v1Kind.Description != v2Kind.Description {
		t.Errorf("Description differs: v1=%q v2=%q", v1Kind.Description, v2Kind.Description)
	}
	if v1Kind.Guidance != v2Kind.Guidance {
		t.Errorf("Guidance differs: v1=%q v2=%q", v1Kind.Guidance, v2Kind.Guidance)
	}
	assertSameSchemaBytes(t, "EventKinds[0].BindingSchema", v1Kind.BindingSchema, v2Kind.BindingSchema)
	// v1 declares no operators attestation; v2 declares none in this fixture
	// either, so both sides are expected to be empty — not just equal.
	if len(v1Kind.Operators) != 0 || len(v2Kind.Operators) != 0 {
		t.Errorf("Operators: v1=%v v2=%v, want both empty", v1Kind.Operators, v2Kind.Operators)
	}
	// v2 declares no payload_schema/examples; asserting v1's are unset too
	// keeps this fixture pair a genuine twin rather than one that happens to
	// pass because v1 carries extra data v2 has no way to express.
	if v1Kind.PayloadSchema != nil || len(v1Kind.Examples) != 0 {
		t.Errorf("v1 fixture carries payload_schema/examples the v2 twin cannot express")
	}
}

func assertSameSchemaBytes(t *testing.T, label string, v1Node, v2Node *yaml.Node) {
	t.Helper()
	v1Bytes, err := schemautil.ToJSON(v1Node)
	if err != nil {
		t.Fatalf("%s: ToJSON(v1): %v", label, err)
	}
	v2Bytes, err := schemautil.ToJSON(v2Node)
	if err != nil {
		t.Fatalf("%s: ToJSON(v2): %v", label, err)
	}
	if string(v1Bytes) != string(v2Bytes) {
		t.Errorf("%s differs:\nv1: %s\nv2: %s", label, v1Bytes, v2Bytes)
	}
}

func TestRead_MalformedInputErrors(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{name: "malformed YAML", data: "schema_version: \"2\"\n  name: broken\n:::"},
		{name: "v2 fails validation", data: "schema_version: \"2\"\nname: x\nversion: 1.0.0\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := pluginmanifest.Read([]byte(tc.data)); err == nil {
				t.Fatal("Read succeeded, want an error")
			}
		})
	}
}
