package manifestv2

import (
	"errors"
	"strings"
	"testing"
)

const digest = "@sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// minimalManifest is the smallest thing that installs: a plain ecosystem MCP
// server wrapped with a manifest and a signature. Keeping this case first is
// deliberate — the profile system's whole claim is that a tool-only server
// needs no Gleipnir-specific code, and this is what that looks like.
const minimalManifest = `
schema_version: "2"
name: io.github.example/weather
version: 1.2.0
description: Weather lookups.
package:
  registry_type: oci
  identifier: ghcr.io/example/weather` + digest + `
  transport:
    type: streamable-http
    port: 8080
gleipnir:
  profiles:
    tool_provider: {}
`

// fullManifest declares all four profiles and every extension field.
const fullManifest = `
schema_version: "2"
name: io.github.example/everything
version: 2.0.0
description: All four profiles.
repository:
  url: https://github.com/example/everything
  source: github
package:
  registry_type: oci
  identifier: ghcr.io/example/everything` + digest + `
  version: 2.0.0
  transport:
    type: streamable-http
    port: 9000
gleipnir:
  profiles:
    tool_provider: {}
    event_source:
      subscription_schema:
        type: object
        properties:
          channels:
            type: array
            items:
              type: string
    human_channel:
      assurance: authenticated
    identity_provider:
      link_methods: [oauth, code]
  egress:
    - domain: api.example.com
      reason: the vendor API this plugin wraps
    - domain: "*.cdn.example.com"
  resources:
    memory_mb: 512
    cpu_millicores: 1500
  tools:
    - name: deploy
      elicitation_kind: permission
    - name: lookup_ticket
      elicitation_kind: information
  event_kinds:
    - kind: message.posted
      description: A message was posted.
      guidance: Fires once per message posted to a channel this instance watches.
      binding_schema:
        type: object
        properties:
          channel:
            type: string
      operators:
        channel: [equals, contains]
  config_schema:
    type: object
    properties:
      api_key:
        type: string
        x-gleipnir-secret: true
  user_config_schema:
    type: object
    properties:
      handle:
        type: string
  sbom: sbom.cdx.json
`

func TestParse_Minimal(t *testing.T) {
	m, err := Parse([]byte(minimalManifest))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.Name != "io.github.example/weather" {
		t.Errorf("name = %q", m.Name)
	}
	if got := m.Gleipnir.Profiles.Declared(); len(got) != 1 || got[0] != "tool_provider" {
		t.Errorf("profiles = %v, want [tool_provider]", got)
	}
	// Default deny: no grants means the container reaches nothing.
	if len(m.Gleipnir.Egress) != 0 {
		t.Errorf("egress = %v, want none", m.Gleipnir.Egress)
	}
}

func TestParse_Full(t *testing.T) {
	m, err := Parse([]byte(fullManifest))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	want := []string{"tool_provider", "event_source", "human_channel", "identity_provider"}
	got := m.Gleipnir.Profiles.Declared()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("profiles = %v, want %v", got, want)
	}
	if m.Gleipnir.Profiles.HumanChannel.Assurance != AssuranceAuthenticated {
		t.Errorf("assurance = %q", m.Gleipnir.Profiles.HumanChannel.Assurance)
	}
	if len(m.Gleipnir.Egress) != 2 || m.Gleipnir.Egress[1].Domain != "*.cdn.example.com" {
		t.Errorf("egress = %+v", m.Gleipnir.Egress)
	}
	if m.Gleipnir.Resources == nil || m.Gleipnir.Resources.MemoryMB != 512 {
		t.Errorf("resources = %+v", m.Gleipnir.Resources)
	}
	if len(m.Gleipnir.Tools) != 2 || m.Gleipnir.Tools[0].ElicitationKind != ElicitationKindPermission {
		t.Errorf("tools = %+v", m.Gleipnir.Tools)
	}
	// The raw-node fields are the ones a decoder quirk silently drops; assert
	// each actually arrived.
	if m.Gleipnir.ConfigSchema == nil {
		t.Error("config_schema is nil")
	}
	if m.Gleipnir.UserConfigSchema == nil {
		t.Error("user_config_schema is nil")
	}
	if len(m.Gleipnir.EventKinds) != 1 || m.Gleipnir.EventKinds[0].BindingSchema == nil {
		t.Errorf("event_kinds = %+v, want one kind with a binding schema", m.Gleipnir.EventKinds)
	}
	ek := m.Gleipnir.EventKinds[0]
	if ek.Guidance == "" {
		t.Error("event_kinds[0].guidance is empty")
	}
	if want := []string{"equals", "contains"}; len(ek.Operators["channel"]) != 2 ||
		ek.Operators["channel"][0] != want[0] || ek.Operators["channel"][1] != want[1] {
		t.Errorf("event_kinds[0].operators[channel] = %v, want %v", ek.Operators["channel"], want)
	}
	if m.Gleipnir.Profiles.EventSource == nil || m.Gleipnir.Profiles.EventSource.SubscriptionSchema == nil {
		t.Error("profiles.event_source.subscription_schema is nil")
	}
}

// Round-trip must be lossless: signing hashes manifest bytes, so a
// re-serialized manifest that differs would fail its own signature.
func TestRoundTrip_IsLossless(t *testing.T) {
	for _, src := range []string{minimalManifest, fullManifest} {
		first, err := Parse([]byte(src))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		encoded, err := Marshal(first)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		second, err := Parse(encoded)
		if err != nil {
			t.Fatalf("re-Parse of marshalled output: %v\n%s", err, encoded)
		}
		reEncoded, err := Marshal(second)
		if err != nil {
			t.Fatalf("re-Marshal: %v", err)
		}
		if string(encoded) != string(reEncoded) {
			t.Errorf("round trip is not stable:\nfirst:\n%s\nsecond:\n%s", encoded, reEncoded)
		}
	}
}

func TestMarshal_IsCanonical(t *testing.T) {
	m, err := Parse([]byte(minimalManifest))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	out, err := Marshal(m)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.HasSuffix(string(out), "\n") || strings.HasSuffix(string(out), "\n\n") {
		t.Error("output does not end with exactly one newline")
	}

	// Top-level keys sorted: gleipnir, name, package, schema_version, version.
	var topLevel []string
	for _, line := range strings.Split(string(out), "\n") {
		if line != "" && !strings.HasPrefix(line, " ") && strings.Contains(line, ":") {
			topLevel = append(topLevel, strings.SplitN(line, ":", 2)[0])
		}
	}
	for i := 1; i < len(topLevel); i++ {
		if topLevel[i-1] > topLevel[i] {
			t.Errorf("top-level keys are not sorted: %v", topLevel)
			break
		}
	}
}

// Every rejection path. The manifest is a consent surface, so each of these is
// something an admin would otherwise have approved without it being true.
func TestParse_Rejections(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(string) string
		wantField string
	}{
		{
			name:      "wrong schema version",
			mutate:    func(s string) string { return strings.Replace(s, `schema_version: "2"`, `schema_version: "3"`, 1) },
			wantField: "schema_version",
		},
		{
			name:      "missing name",
			mutate:    func(s string) string { return strings.Replace(s, "name: io.github.example/weather\n", "", 1) },
			wantField: "name",
		},
		{
			name:      "missing version",
			mutate:    func(s string) string { return strings.Replace(s, "version: 1.2.0\n", "", 1) },
			wantField: "version",
		},
		{
			// A tag is a mutable pointer: consenting to it is consenting to
			// whatever it points at tomorrow.
			name:      "image not digest-pinned",
			mutate:    func(s string) string { return strings.Replace(s, digest, ":v1.2.0", 1) },
			wantField: "package.identifier",
		},
		{
			name:      "malformed digest",
			mutate:    func(s string) string { return strings.Replace(s, digest, "@sha256:notahexdigest", 1) },
			wantField: "package.identifier",
		},
		{
			name:      "non-OCI registry type",
			mutate:    func(s string) string { return strings.Replace(s, "registry_type: oci", "registry_type: npm", 1) },
			wantField: "package.registry_type",
		},
		{
			name:      "unknown transport",
			mutate:    func(s string) string { return strings.Replace(s, "type: streamable-http", "type: stdio", 1) },
			wantField: "package.transport.type",
		},
		{
			name:      "no profiles declared",
			mutate:    func(s string) string { return strings.Replace(s, "    tool_provider: {}\n", "", 1) },
			wantField: "gleipnir.profiles",
		},
		{
			name: "egress with a scheme",
			mutate: func(s string) string {
				return s + "  egress:\n    - domain: https://api.example.com\n"
			},
			wantField: "gleipnir.egress[0].domain",
		},
		{
			name: "egress with a port",
			mutate: func(s string) string {
				return s + "  egress:\n    - domain: api.example.com:443\n"
			},
			wantField: "gleipnir.egress[0].domain",
		},
		{
			name: "egress with an interior wildcard",
			mutate: func(s string) string {
				return s + "  egress:\n    - domain: api.*.example.com\n"
			},
			wantField: "gleipnir.egress[0].domain",
		},
		{
			name: "duplicate egress grant",
			mutate: func(s string) string {
				return s + "  egress:\n    - domain: api.example.com\n    - domain: api.example.com\n"
			},
			wantField: "gleipnir.egress[1].domain",
		},
		{
			name: "negative resource limit",
			mutate: func(s string) string {
				return s + "  resources:\n    memory_mb: -1\n"
			},
			wantField: "gleipnir.resources.memory_mb",
		},
		{
			name: "unknown elicitation kind",
			mutate: func(s string) string {
				return s + "  tools:\n    - name: deploy\n      elicitation_kind: urgent\n"
			},
			wantField: "gleipnir.tools[0].elicitation_kind",
		},
		{
			name: "duplicate tool declaration",
			mutate: func(s string) string {
				return s + "  tools:\n    - name: deploy\n    - name: deploy\n"
			},
			wantField: "gleipnir.tools[1].name",
		},
		{
			name: "duplicate event kind",
			mutate: func(s string) string {
				return s + "  event_kinds:\n    - kind: message.posted\n    - kind: message.posted\n"
			},
			wantField: "gleipnir.event_kinds[1].kind",
		},
		{
			name: "human channel without an assurance level",
			mutate: func(s string) string {
				return strings.Replace(s, "    tool_provider: {}\n", "    tool_provider: {}\n    human_channel: {}\n", 1)
			},
			wantField: "gleipnir.profiles.human_channel.assurance",
		},
		{
			name: "human channel with an unknown assurance level",
			mutate: func(s string) string {
				return strings.Replace(s, "    tool_provider: {}\n",
					"    tool_provider: {}\n    human_channel:\n      assurance: probably\n", 1)
			},
			wantField: "gleipnir.profiles.human_channel.assurance",
		},
		{
			name: "identity provider with no link methods",
			mutate: func(s string) string {
				return strings.Replace(s, "    tool_provider: {}\n",
					"    tool_provider: {}\n    identity_provider:\n      link_methods: []\n", 1)
			},
			wantField: "gleipnir.profiles.identity_provider.link_methods",
		},
		{
			// event_kinds attests what the (absent) event_source profile emits —
			// the manifest disagreeing with itself.
			name: "event_kinds without an event_source profile",
			mutate: func(s string) string {
				return s + "  event_kinds:\n    - kind: message.posted\n"
			},
			wantField: "gleipnir.event_kinds",
		},
		{
			name: "operators naming a field absent from binding_schema",
			mutate: func(s string) string {
				return strings.Replace(s, "    tool_provider: {}\n", "    tool_provider: {}\n    event_source: {}\n", 1) +
					"  event_kinds:\n    - kind: message.posted\n" +
					"      binding_schema:\n        type: object\n        properties:\n          channel:\n            type: string\n" +
					"      operators:\n        topic: [equals]\n"
			},
			wantField: "gleipnir.event_kinds[0].operators",
		},
		{
			name: "operators naming an unknown operator",
			mutate: func(s string) string {
				return strings.Replace(s, "    tool_provider: {}\n", "    tool_provider: {}\n    event_source: {}\n", 1) +
					"  event_kinds:\n    - kind: message.posted\n" +
					"      binding_schema:\n        type: object\n        properties:\n          channel:\n            type: string\n" +
					"      operators:\n        channel: [glob]\n"
			},
			wantField: "gleipnir.event_kinds[0].operators",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.mutate(minimalManifest)))
			if err == nil {
				t.Fatal("Parse succeeded, want a validation error")
			}
			var verr *ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("error = %v, want *ValidationError", err)
			}
			found := false
			for _, issue := range verr.Issues {
				if issue.Field == tc.wantField {
					found = true
				}
			}
			if !found {
				t.Errorf("issues = %v, want one tagged %q", verr.Issues, tc.wantField)
			}
		})
	}
}

// An unknown profile — or any unknown key — is an error, not something to
// ignore. A field the host silently drops is a claim the admin read and the
// runtime never enforced.
func TestParse_UnknownFieldsAreRejected(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(string) string
	}{
		{
			name: "unknown profile",
			mutate: func(s string) string {
				return strings.Replace(s, "    tool_provider: {}\n", "    tool_provider: {}\n    time_traveller: {}\n", 1)
			},
		},
		{
			name:   "misspelled top-level key",
			mutate: func(s string) string { return strings.Replace(s, "description:", "descriptoin:", 1) },
		},
		{
			name:   "unknown gleipnir key",
			mutate: func(s string) string { return s + "  unknown_future_field: true\n" },
		},
		{
			name: "unknown package key",
			mutate: func(s string) string {
				return strings.Replace(s, "  registry_type: oci", "  registry_type: oci\n  runtime_hint: docker", 1)
			},
		},
		{
			name: "unknown key under event_source",
			mutate: func(s string) string {
				return strings.Replace(s, "    tool_provider: {}\n",
					"    tool_provider: {}\n    event_source:\n      bogus_field: true\n", 1)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse([]byte(tc.mutate(minimalManifest))); err == nil {
				t.Fatal("Parse accepted an unknown field, want an error")
			}
		})
	}
}

// The unknown-key rejection must carry assertKnownKeys' specific shape
// ("line N: field X not found in type Y"), not just any error — that shape is
// what points a plugin author at the offending line rather than a generic
// decode failure.
func TestParse_EventSourceUnknownKeyErrorShape(t *testing.T) {
	src := strings.Replace(minimalManifest, "    tool_provider: {}\n",
		"    tool_provider: {}\n    event_source:\n      bogus_field: true\n", 1)
	_, err := Parse([]byte(src))
	if err == nil {
		t.Fatal("Parse accepted an unknown event_source key, want an error")
	}
	if !strings.Contains(err.Error(), "field bogus_field not found in type event_source") {
		t.Errorf("error = %q, want the assertKnownKeys shape naming event_source", err)
	}
}

// An unknown operator's error must list the accepted set so an author can fix
// it without cross-referencing the binding evaluator's source.
func TestParse_UnknownOperatorErrorListsAcceptedSet(t *testing.T) {
	src := strings.Replace(minimalManifest, "    tool_provider: {}\n", "    tool_provider: {}\n    event_source: {}\n", 1) +
		"  event_kinds:\n    - kind: message.posted\n" +
		"      binding_schema:\n        type: object\n        properties:\n          channel:\n            type: string\n" +
		"      operators:\n        channel: [glob]\n"

	_, err := Parse([]byte(src))
	if err == nil {
		t.Fatal("Parse accepted an unknown operator, want an error")
	}
	for _, want := range []string{"equals", "contains", "regex", "mention_only"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to list accepted operator %q", err, want)
		}
	}
}

// Unparseable YAML fails closed rather than yielding a partial manifest.
func TestParse_MalformedYAMLFailsClosed(t *testing.T) {
	if _, err := Parse([]byte("schema_version: \"2\"\n  name: broken\n:::")); err == nil {
		t.Fatal("Parse accepted malformed YAML, want an error")
	}
}

// Validation reports every problem at once; an author fixing a manifest should
// not discover its faults one round trip at a time.
func TestValidate_ReportsEveryIssue(t *testing.T) {
	m := &Manifest{} // empty: schema version, name, version, package, profiles all wrong

	err := Validate(m)
	if err == nil {
		t.Fatal("Validate accepted an empty manifest")
	}
	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("error = %v, want *ValidationError", err)
	}
	if len(verr.Issues) < 5 {
		t.Errorf("got %d issues, want at least 5 (schema_version, name, version, package, profiles): %v",
			len(verr.Issues), verr.Issues)
	}
	if !strings.Contains(err.Error(), "schema_version") {
		t.Errorf("error text %q does not name the offending fields", err)
	}
}
