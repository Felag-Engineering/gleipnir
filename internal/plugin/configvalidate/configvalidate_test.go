package configvalidate_test

import (
	"strings"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/plugin/configvalidate"
	sdkmanifest "github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
	"gopkg.in/yaml.v3"
)

// parseNode decodes a YAML literal into a *yaml.Node for use in test fixtures.
func parseNode(t *testing.T, src string) *yaml.Node {
	t.Helper()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(src), &node); err != nil {
		t.Fatalf("parseNode: %v", err)
	}
	// yaml.Unmarshal wraps in a document node; unwrap to the content node.
	if node.Kind == yaml.DocumentNode && len(node.Content) == 1 {
		return node.Content[0]
	}
	return &node
}

// channelSchema is a simple channel config schema used across tests.
const channelSchema = `
type: object
additionalProperties: false
required: [channel]
properties:
  channel: { type: string }
  mention: { type: string }
`

// channelManifest builds a *Manifest with a ChannelService and one ChannelDecl
// whose ConfigSchema is parsed from src.
func channelManifest(t *testing.T, schemaSrc string) *sdkmanifest.Manifest {
	t.Helper()
	return &sdkmanifest.Manifest{
		Services: sdkmanifest.Services{Channel: "v1"},
		Channels: []sdkmanifest.ChannelDecl{
			{ConfigSchema: parseNode(t, schemaSrc)},
		},
	}
}

func TestForChannelAudience_HappyPath(t *testing.T) {
	m := channelManifest(t, channelSchema)
	v, err := configvalidate.ForChannelAudience(m)
	if err != nil {
		t.Fatalf("ForChannelAudience: %v", err)
	}
	errs, err := v.Validate(map[string]any{"channel": "#general"})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(errs) != 0 {
		t.Errorf("expected no errors, got %v", errs)
	}
}

func TestForChannelAudience_MissingRequired(t *testing.T) {
	m := channelManifest(t, channelSchema)
	v, err := configvalidate.ForChannelAudience(m)
	if err != nil {
		t.Fatalf("ForChannelAudience: %v", err)
	}
	// Empty object — "channel" is required.
	errs, err := v.Validate(map[string]any{})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(errs) != 1 {
		t.Fatalf("expected 1 error, got %d: %v", len(errs), errs)
	}
	if errs[0].Field != "channel" {
		t.Errorf("Field = %q, want %q", errs[0].Field, "channel")
	}
	if !containsString(errs[0].Message, "missing") {
		t.Errorf("Message = %q, want it to contain %q", errs[0].Message, "missing")
	}
}

func TestForChannelAudience_MissingRequired_MultipleFields(t *testing.T) {
	// Both "channel" and "mention" are required; a single *kind.Required leaf
	// should produce TWO FieldErrors via Path A (type assertion on Missing).
	const schema = `
type: object
additionalProperties: false
required: [channel, mention]
properties:
  channel: { type: string }
  mention: { type: string }
`
	m := channelManifest(t, schema)
	v, err := configvalidate.ForChannelAudience(m)
	if err != nil {
		t.Fatalf("ForChannelAudience: %v", err)
	}
	errs, err := v.Validate(map[string]any{})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(errs) != 2 {
		t.Fatalf("expected 2 errors (one per missing field), got %d: %v", len(errs), errs)
	}
	fields := map[string]bool{}
	for _, e := range errs {
		fields[e.Field] = true
		if !containsString(e.Message, "missing") {
			t.Errorf("Message = %q, want it to contain %q", e.Message, "missing")
		}
	}
	for _, want := range []string{"channel", "mention"} {
		if !fields[want] {
			t.Errorf("expected a FieldError for %q, got fields=%v", want, fields)
		}
	}
}

func TestForChannelAudience_TypeMismatch(t *testing.T) {
	// channel should be a string; supply int — Path B (InstanceLocation).
	m := channelManifest(t, channelSchema)
	v, err := configvalidate.ForChannelAudience(m)
	if err != nil {
		t.Fatalf("ForChannelAudience: %v", err)
	}
	errs, err := v.Validate(map[string]any{"channel": 7})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(errs) != 1 {
		t.Fatalf("expected 1 error, got %d: %v", len(errs), errs)
	}
	if errs[0].Field != "channel" {
		t.Errorf("Field = %q, want %q", errs[0].Field, "channel")
	}
	if !containsString(errs[0].Message, "string") {
		t.Errorf("Message = %q, want it to contain %q", errs[0].Message, "string")
	}
}

func TestForChannelAudience_AdditionalProperties(t *testing.T) {
	// Two unexpected properties — Path A (*kind.AdditionalProperties.Properties)
	// should yield TWO FieldErrors from a single leaf.
	m := channelManifest(t, channelSchema)
	v, err := configvalidate.ForChannelAudience(m)
	if err != nil {
		t.Fatalf("ForChannelAudience: %v", err)
	}
	errs, err := v.Validate(map[string]any{
		"channel": "#x",
		"bogus":   1,
		"other":   2,
	})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(errs) != 2 {
		t.Fatalf("expected 2 errors (bogus + other), got %d: %v", len(errs), errs)
	}
	fields := map[string]bool{}
	for _, e := range errs {
		fields[e.Field] = true
		if !containsString(e.Message, "unexpected") {
			t.Errorf("Message = %q, want it to contain %q", e.Message, "unexpected")
		}
	}
	for _, want := range []string{"bogus", "other"} {
		if !fields[want] {
			t.Errorf("expected a FieldError for %q, got fields=%v", want, fields)
		}
	}
}

func TestForChannelAudience_NestedPath(t *testing.T) {
	// Nested schema: outer.inner must be a string; supply int.
	// FieldError.Field should be "outer.inner" via Path B.
	const schema = `
type: object
properties:
  outer:
    type: object
    properties:
      inner: { type: string }
`
	m := channelManifest(t, schema)
	v, err := configvalidate.ForChannelAudience(m)
	if err != nil {
		t.Fatalf("ForChannelAudience: %v", err)
	}
	errs, err := v.Validate(map[string]any{
		"outer": map[string]any{"inner": 7},
	})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(errs) != 1 {
		t.Fatalf("expected 1 error, got %d: %v", len(errs), errs)
	}
	if errs[0].Field != "outer.inner" {
		t.Errorf("Field = %q, want %q", errs[0].Field, "outer.inner")
	}
}

func TestForChannelAudience_NonChannelPlugin(t *testing.T) {
	m := &sdkmanifest.Manifest{
		Services: sdkmanifest.Services{Tool: "v1"},
	}
	_, err := configvalidate.ForChannelAudience(m)
	if err != configvalidate.ErrNotChannelPlugin {
		t.Errorf("err = %v, want ErrNotChannelPlugin", err)
	}
}

func TestForChannelAudience_NonChannelPlugin_EmptyChannels(t *testing.T) {
	// Services.Channel is set but Channels slice is empty.
	m := &sdkmanifest.Manifest{
		Services: sdkmanifest.Services{Channel: "v1"},
		Channels: []sdkmanifest.ChannelDecl{},
	}
	_, err := configvalidate.ForChannelAudience(m)
	if err != configvalidate.ErrNotChannelPlugin {
		t.Errorf("err = %v, want ErrNotChannelPlugin", err)
	}
}

func TestForTriggerBinding_SelectsByKind(t *testing.T) {
	// Two event kinds with different schemas. ForTriggerBinding("b") must use
	// b's schema, NOT a's.
	const schemaA = `
type: object
required: [fieldA]
properties:
  fieldA: { type: string }
`
	const schemaB = `
type: object
required: [fieldB]
properties:
  fieldB: { type: string }
`
	m := &sdkmanifest.Manifest{
		EventKinds: []sdkmanifest.EventKindDecl{
			{Kind: "a", BindingSchema: parseNode(t, schemaA)},
			{Kind: "b", BindingSchema: parseNode(t, schemaB)},
		},
	}

	v, err := configvalidate.ForTriggerBinding(m, "b")
	if err != nil {
		t.Fatalf("ForTriggerBinding: %v", err)
	}

	// fieldB present → valid.
	if errs, err := v.Validate(map[string]any{"fieldB": "x"}); err != nil || len(errs) != 0 {
		t.Errorf("expected valid for fieldB; errs=%v err=%v", errs, err)
	}

	// fieldA present but fieldB missing → invalid (b's schema requires fieldB).
	errs, err := v.Validate(map[string]any{"fieldA": "x"})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(errs) == 0 {
		t.Error("expected validation errors for missing fieldB, got none")
	}
}

func TestForTriggerBinding_NotFound(t *testing.T) {
	m := &sdkmanifest.Manifest{
		EventKinds: []sdkmanifest.EventKindDecl{
			{Kind: "a"},
		},
	}
	_, err := configvalidate.ForTriggerBinding(m, "missing")
	if err != configvalidate.ErrEventKindNotFound {
		t.Errorf("err = %v, want ErrEventKindNotFound", err)
	}
}

func TestCache_SamePointerForIdenticalSchemas(t *testing.T) {
	configvalidate.ResetCache()
	defer configvalidate.ResetCache()

	m1 := channelManifest(t, channelSchema)
	m2 := channelManifest(t, channelSchema) // semantically identical

	v1, err := configvalidate.ForChannelAudience(m1)
	if err != nil {
		t.Fatalf("ForChannelAudience m1: %v", err)
	}
	v2, err := configvalidate.ForChannelAudience(m2)
	if err != nil {
		t.Fatalf("ForChannelAudience m2: %v", err)
	}

	// Same compiled schema bytes → same *Validator pointer from the cache.
	if v1 != v2 {
		t.Error("expected the same *Validator pointer for identical schemas (cache miss)")
	}
}

func TestValidateChannelCapabilities(t *testing.T) {
	notifyOnly := &sdkmanifest.Manifest{
		Services: sdkmanifest.Services{Channel: "v1"},
		Channels: []sdkmanifest.ChannelDecl{
			{ImplementsNotify: true, ImplementsRequest: false},
		},
	}
	requestOnly := &sdkmanifest.Manifest{
		Services: sdkmanifest.Services{Channel: "v1"},
		Channels: []sdkmanifest.ChannelDecl{
			{ImplementsNotify: false, ImplementsRequest: true},
		},
	}
	noChannel := &sdkmanifest.Manifest{
		Services: sdkmanifest.Services{Tool: "v1"},
	}

	tests := []struct {
		name       string
		manifest   *sdkmanifest.Manifest
		notify     bool
		request    bool
		wantFields []string // expected FieldError.Field values; nil means no errors
	}{
		{
			name:     "notify-only manifest, notify=true request=false",
			manifest: notifyOnly,
			notify:   true, request: false,
			wantFields: nil,
		},
		{
			name:     "notify-only manifest, request=true",
			manifest: notifyOnly,
			notify:   false, request: true,
			wantFields: []string{"request"},
		},
		{
			name:     "request-only manifest, notify=true",
			manifest: requestOnly,
			notify:   true, request: false,
			wantFields: []string{"notify"},
		},
		{
			name:     "both flags off",
			manifest: notifyOnly,
			notify:   false, request: false,
			wantFields: nil,
		},
		{
			name:     "no ChannelService",
			manifest: noChannel,
			notify:   true, request: false,
			wantFields: []string{"plugin_instance_id"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			errs := configvalidate.ValidateChannelCapabilities(tc.manifest, tc.notify, tc.request)
			if len(tc.wantFields) == 0 {
				if len(errs) != 0 {
					t.Errorf("expected no errors, got %v", errs)
				}
				return
			}
			if len(errs) != len(tc.wantFields) {
				t.Fatalf("got %d errors, want %d: %v", len(errs), len(tc.wantFields), errs)
			}
			got := make(map[string]bool, len(errs))
			for _, e := range errs {
				got[e.Field] = true
			}
			for _, f := range tc.wantFields {
				if !got[f] {
					t.Errorf("expected FieldError with Field=%q, got %v", f, errs)
				}
			}
		})
	}
}

// TestForTriggerBinding_AcceptsReflectedSchema is the cross-library
// compatibility test: it builds a binding_schema via manifest.ReflectSchema
// (invopop/jsonschema) and feeds it through ForTriggerBinding
// (santhosh-tekuri/v6). Both compilers must agree on the schema semantics.
func TestForTriggerBinding_AcceptsReflectedSchema(t *testing.T) {
	// reflectedFilterFixture is the filter struct for this test. It uses the
	// typed filter primitives from the SDK to exercise the reflection path.
	type reflectedFilterFixture struct {
		Channel sdkmanifest.EqualsField   `json:"channel"  jsonschema:"required"`
		Keyword sdkmanifest.ContainsField `json:"keyword,omitempty"`
		Pattern sdkmanifest.RegexField    `json:"pattern,omitempty"`
	}

	bindingNode := sdkmanifest.MustReflectSchema(reflectedFilterFixture{})

	m := &sdkmanifest.Manifest{
		EventKinds: []sdkmanifest.EventKindDecl{
			{Kind: "channel_event", BindingSchema: bindingNode},
		},
	}

	v, err := configvalidate.ForTriggerBinding(m, "channel_event")
	if err != nil {
		t.Fatalf("ForTriggerBinding: %v", err)
	}

	// Valid input: required "channel" field present.
	errs, err := v.Validate(map[string]any{"channel": "#general"})
	if err != nil {
		t.Fatalf("Validate (good input): %v", err)
	}
	if len(errs) != 0 {
		t.Errorf("expected no errors for valid input, got %v", errs)
	}

	// Invalid input: "channel" missing → should produce a FieldError.
	errs, err = v.Validate(map[string]any{"keyword": "hello"})
	if err != nil {
		t.Fatalf("Validate (bad input): %v", err)
	}
	if len(errs) == 0 {
		t.Error("expected at least one error for missing required 'channel' field")
	}
	found := false
	for _, e := range errs {
		if e.Field == "channel" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected FieldError with Field='channel', got %v", errs)
	}
}

// containsString reports whether s contains substr.
func containsString(s, substr string) bool {
	return strings.Contains(s, substr)
}

// ── ForSubscriptionScope ──────────────────────────────────────────────────────

const subscriptionSchemaYAML = `
type: object
additionalProperties: false
required: [channels]
properties:
  channels:
    type: array
    items:
      type: string
`

// subscriptionManifest builds a Manifest with a SubscriptionSchema populated
// from raw YAML.
func subscriptionManifest(t *testing.T, schemaYAML string) *sdkmanifest.Manifest {
	t.Helper()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(schemaYAML), &node); err != nil {
		t.Fatalf("parse subscription schema YAML: %v", err)
	}
	inner := &node
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		inner = node.Content[0]
	}
	return &sdkmanifest.Manifest{
		SchemaVersion:      "v1",
		Name:               "test-trigger-plugin",
		Version:            "1.0.0",
		Services:           sdkmanifest.Services{Trigger: "v1"},
		Auth:               sdkmanifest.AuthDecl{Mode: "instance_credentials", Strategy: "none"},
		SubscriptionSchema: inner,
	}
}

func TestForSubscriptionScope_ValidInput(t *testing.T) {
	m := subscriptionManifest(t, subscriptionSchemaYAML)
	v, err := configvalidate.ForSubscriptionScope(m)
	if err != nil {
		t.Fatalf("ForSubscriptionScope: %v", err)
	}

	errs, err := v.Validate(map[string]any{
		"channels": []any{"#incidents", "#ops"},
	})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(errs) != 0 {
		t.Errorf("expected no errors for valid scope, got %v", errs)
	}
}

func TestForSubscriptionScope_MissingRequired(t *testing.T) {
	m := subscriptionManifest(t, subscriptionSchemaYAML)
	v, err := configvalidate.ForSubscriptionScope(m)
	if err != nil {
		t.Fatalf("ForSubscriptionScope: %v", err)
	}

	// Missing required "channels".
	errs, err := v.Validate(map[string]any{})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(errs) == 0 {
		t.Fatal("expected at least one error for missing required 'channels'")
	}
	found := false
	for _, e := range errs {
		if e.Field == "channels" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected FieldError with Field='channels', got %v", errs)
	}
}

func TestForSubscriptionScope_NilSchema_AcceptsAnything(t *testing.T) {
	// A manifest with no SubscriptionSchema produces an empty-schema validator
	// that accepts any value (mirrors ForInstanceConfig with nil ConfigSchema).
	m := &sdkmanifest.Manifest{
		SchemaVersion: "v1",
		Name:          "test",
		Version:       "1.0.0",
		Services:      sdkmanifest.Services{Trigger: "v1"},
		Auth:          sdkmanifest.AuthDecl{Mode: "instance_credentials", Strategy: "none"},
	}
	v, err := configvalidate.ForSubscriptionScope(m)
	if err != nil {
		t.Fatalf("ForSubscriptionScope with nil schema: %v", err)
	}
	errs, err := v.Validate(map[string]any{"anything": "goes"})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(errs) != 0 {
		t.Errorf("expected no errors with nil schema, got %v", errs)
	}
}
