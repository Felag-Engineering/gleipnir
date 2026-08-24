package manifest_test

import (
	"strings"
	"testing"

	pluginmanifest "github.com/felag-engineering/gleipnir/internal/plugin/manifest"
	sdkmanifest "github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
	"github.com/felag-engineering/gleipnir/plugin-sdk/manifestv2"
	"gopkg.in/yaml.v3"
)

// baseManifest returns a minimal valid manifest to use as the baseline for diff tests.
func baseManifest() *sdkmanifest.Manifest {
	return &sdkmanifest.Manifest{
		SchemaVersion: "v1",
		Name:          "test-plugin",
		Version:       "1.0.0",
		Description:   "A plugin",
		Services:      sdkmanifest.Services{Tool: "v1"},
		Auth:          sdkmanifest.AuthDecl{Mode: "instance_credentials", Strategy: "none"},
	}
}

// baseManifestV2 returns a minimal v2 manifest declaring the event_source
// profile, to use as the baseline for DiffV2 tests.
func baseManifestV2() *manifestv2.Manifest {
	return &manifestv2.Manifest{
		SchemaVersion: manifestv2.SchemaVersion,
		Name:          "test-plugin",
		Version:       "1.0.0",
		Gleipnir: manifestv2.Gleipnir{
			Profiles: manifestv2.Profiles{
				EventSource: &manifestv2.EventSourceProfile{},
			},
		},
	}
}

// parseNode builds a *yaml.Node from inline YAML text. Panics on parse error
// (tests call this only with valid YAML literals).
func parseNode(t *testing.T, src string) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatalf("parseNode: %v", err)
	}
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		return doc.Content[0]
	}
	return &doc
}

func TestDiff_MaterialServiceVersionChange(t *testing.T) {
	old := baseManifest()
	new := baseManifest()
	new.Services.Tool = "v2"

	changes := pluginmanifest.Diff(old, new)
	if !pluginmanifest.HasMaterial(changes) {
		t.Error("expected material change for services.tool version bump")
	}
	fields := pluginmanifest.MaterialFields(changes)
	if len(fields) != 1 || fields[0] != "services.tool" {
		t.Errorf("material fields = %v, want [services.tool]", fields)
	}
}

func TestDiff_MaterialTier2Added(t *testing.T) {
	old := baseManifest()
	new := baseManifest()
	new.Tier2 = []string{"host.exec"}

	changes := pluginmanifest.Diff(old, new)
	if !pluginmanifest.HasMaterial(changes) {
		t.Error("expected material change when tier2 capability is added")
	}
	fields := pluginmanifest.MaterialFields(changes)
	if len(fields) != 1 || fields[0] != "tier2" {
		t.Errorf("material fields = %v, want [tier2]", fields)
	}
}

func TestDiff_MaterialOAuthScopesChanged(t *testing.T) {
	old := baseManifest()
	old.Auth.Strategy = sdkmanifest.AuthStrategyOAuth2Authcode
	old.Auth.OAuthDefaults = &sdkmanifest.OAuthDefaultsDecl{
		AuthorizationURL: "https://example.com/auth",
		TokenURL:         "https://example.com/token",
		Scopes:           []string{"read"},
	}

	new := baseManifest()
	new.Auth.Strategy = sdkmanifest.AuthStrategyOAuth2Authcode
	new.Auth.OAuthDefaults = &sdkmanifest.OAuthDefaultsDecl{
		AuthorizationURL: "https://example.com/auth",
		TokenURL:         "https://example.com/token",
		Scopes:           []string{"read", "write"},
	}

	changes := pluginmanifest.Diff(old, new)
	if !pluginmanifest.HasMaterial(changes) {
		t.Error("expected material change for OAuth scope addition")
	}
	mf := pluginmanifest.MaterialFields(changes)
	found := false
	for _, f := range mf {
		if f == "auth.oauth_defaults.scopes" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected auth.oauth_defaults.scopes in material fields, got %v", mf)
	}
}

func TestDiff_MaterialOAuthStrategyChanged(t *testing.T) {
	old := baseManifest()
	old.Auth.Strategy = sdkmanifest.AuthStrategyStaticAPIKey

	new := baseManifest()
	new.Auth.Strategy = sdkmanifest.AuthStrategyOAuth2Authcode

	changes := pluginmanifest.Diff(old, new)
	if !pluginmanifest.HasMaterial(changes) {
		t.Error("expected material change for auth.strategy change")
	}
}

func TestDiff_MaterialToolAdded(t *testing.T) {
	old := baseManifest()
	new := baseManifest()
	new.Tools = []sdkmanifest.ToolDecl{{Name: "new_tool"}}

	changes := pluginmanifest.Diff(old, new)
	if !pluginmanifest.HasMaterial(changes) {
		t.Error("expected material change when a tool is added")
	}
	for _, c := range changes {
		if c.Field == "tools.new_tool" && c.Material {
			return
		}
	}
	t.Errorf("expected change for tools.new_tool, got %v", changes)
}

func TestDiff_MaterialToolRemoved(t *testing.T) {
	old := baseManifest()
	old.Tools = []sdkmanifest.ToolDecl{{Name: "old_tool"}}

	new := baseManifest()

	changes := pluginmanifest.Diff(old, new)
	if !pluginmanifest.HasMaterial(changes) {
		t.Error("expected material change when a tool is removed")
	}
	for _, c := range changes {
		if c.Field == "tools.old_tool" && c.Material {
			return
		}
	}
	t.Errorf("expected change for tools.old_tool, got %v", changes)
}

func TestDiff_MaterialToolApprovalFlipped(t *testing.T) {
	old := baseManifest()
	old.Tools = []sdkmanifest.ToolDecl{{Name: "risky_tool", ApprovalRequired: false}}

	new := baseManifest()
	new.Tools = []sdkmanifest.ToolDecl{{Name: "risky_tool", ApprovalRequired: true}}

	changes := pluginmanifest.Diff(old, new)
	if !pluginmanifest.HasMaterial(changes) {
		t.Error("expected material change when approval_required is flipped")
	}
	for _, c := range changes {
		if c.Field == "tools.risky_tool.approval_required" && c.Material {
			return
		}
	}
	t.Errorf("expected change for tools.risky_tool.approval_required, got %v", changes)
}

func TestDiff_MaterialToolInputSchemaChanged(t *testing.T) {
	schemaA := parseNode(t, `{"type":"object","properties":{"x":{"type":"string"}}}`)
	schemaB := parseNode(t, `{"type":"object","properties":{"x":{"type":"integer"}}}`)

	old := baseManifest()
	old.Tools = []sdkmanifest.ToolDecl{{Name: "my_tool", InputSchema: schemaA}}

	new := baseManifest()
	new.Tools = []sdkmanifest.ToolDecl{{Name: "my_tool", InputSchema: schemaB}}

	changes := pluginmanifest.Diff(old, new)
	if !pluginmanifest.HasMaterial(changes) {
		t.Error("expected material change when tool input_schema type changes")
	}
	for _, c := range changes {
		if c.Field == "tools.my_tool.input_schema" && c.Material {
			return
		}
	}
	t.Errorf("expected change for tools.my_tool.input_schema, got %v", changes)
}

func TestDiff_MaterialConfigSchemaShapeChanged(t *testing.T) {
	schemaA := parseNode(t, `{"type":"object","properties":{"token":{"type":"string"}}}`)
	schemaB := parseNode(t, `{"type":"object","properties":{"token":{"type":"string"},"endpoint":{"type":"string"}}}`)

	old := baseManifest()
	old.ConfigSchema = schemaA

	new := baseManifest()
	new.ConfigSchema = schemaB

	changes := pluginmanifest.Diff(old, new)
	if !pluginmanifest.HasMaterial(changes) {
		t.Error("expected material change when config_schema gains a property")
	}
	for _, c := range changes {
		if c.Field == "config_schema" && c.Material {
			return
		}
	}
	t.Errorf("expected change for config_schema, got %v", changes)
}

func TestDiff_MaterialEventKindBindingSchemaChanged(t *testing.T) {
	schemaA := parseNode(t, `{"type":"object","properties":{"channel":{"type":"string"}}}`)
	schemaB := parseNode(t, `{"type":"object","properties":{"channel":{"type":"string"},"topic":{"type":"string"}}}`)

	old := baseManifest()
	old.EventKinds = []sdkmanifest.EventKindDecl{{Kind: "message", BindingSchema: schemaA}}

	new := baseManifest()
	new.EventKinds = []sdkmanifest.EventKindDecl{{Kind: "message", BindingSchema: schemaB}}

	changes := pluginmanifest.Diff(old, new)
	if !pluginmanifest.HasMaterial(changes) {
		t.Error("expected material change when event_kind binding_schema changes")
	}
	for _, c := range changes {
		if c.Field == "event_kinds.message.binding_schema" && c.Material {
			return
		}
	}
	t.Errorf("expected change for event_kinds.message.binding_schema, got %v", changes)
}

func TestDiffV2_MaterialEventKindBindingSchemaChanged(t *testing.T) {
	schemaA := parseNode(t, `{"type":"object","properties":{"channel":{"type":"string"}}}`)
	schemaB := parseNode(t, `{"type":"object","properties":{"channel":{"type":"string"},"topic":{"type":"string"}}}`)

	old := baseManifestV2()
	old.Gleipnir.EventKinds = []manifestv2.EventKindDecl{{Kind: "message", BindingSchema: schemaA}}

	new := baseManifestV2()
	new.Gleipnir.EventKinds = []manifestv2.EventKindDecl{{Kind: "message", BindingSchema: schemaB}}

	changes := pluginmanifest.DiffV2(old, new)
	if !pluginmanifest.HasMaterial(changes) {
		t.Error("expected material change when v2 event_kind binding_schema changes")
	}
	for _, c := range changes {
		if c.Field == "event_kinds.message.binding_schema" && c.Material {
			return
		}
	}
	t.Errorf("expected change for event_kinds.message.binding_schema, got %v", changes)
}

// guidance is help text rendered in the subscribed-trigger dialog — it does
// not change what a policy binding may express, so it must be cosmetic.
func TestDiffV2_CosmeticEventKindGuidanceChanged(t *testing.T) {
	old := baseManifestV2()
	old.Gleipnir.EventKinds = []manifestv2.EventKindDecl{{Kind: "message", Guidance: "Fires on every post."}}

	new := baseManifestV2()
	new.Gleipnir.EventKinds = []manifestv2.EventKindDecl{{Kind: "message", Guidance: "Fires once per post, batched."}}

	changes := pluginmanifest.DiffV2(old, new)
	if pluginmanifest.HasMaterial(changes) {
		t.Errorf("expected no material changes for guidance-only diff, got %v", pluginmanifest.MaterialFields(changes))
	}
	found := false
	for _, c := range changes {
		if c.Field == "event_kinds.message.guidance" {
			found = true
			if c.Material {
				t.Errorf("event_kinds.message.guidance change must not be material, got %+v", c)
			}
		}
	}
	if !found {
		t.Errorf("expected a change for event_kinds.message.guidance, got %v", changes)
	}
}

// operators widens or narrows what a policy binding may express — exactly
// what the material-change hot-reload block exists to catch (ADR-052).
func TestDiffV2_MaterialEventKindOperatorsChanged(t *testing.T) {
	old := baseManifestV2()
	old.Gleipnir.EventKinds = []manifestv2.EventKindDecl{{
		Kind:      "message",
		Operators: map[string][]string{"channel": {"equals"}},
	}}

	new := baseManifestV2()
	new.Gleipnir.EventKinds = []manifestv2.EventKindDecl{{
		Kind:      "message",
		Operators: map[string][]string{"channel": {"equals", "contains"}},
	}}

	changes := pluginmanifest.DiffV2(old, new)
	if !pluginmanifest.HasMaterial(changes) {
		t.Error("expected material change when event_kind operators widens")
	}
	for _, c := range changes {
		if c.Field == "event_kinds.message.operators" && c.Material {
			return
		}
	}
	t.Errorf("expected change for event_kinds.message.operators, got %v", changes)
}

// Operator list order within a field carries no meaning — it is a set — so
// reordering the same operators must not produce any change.
func TestDiffV2_EventKindOperatorsReordered_NoDiff(t *testing.T) {
	old := baseManifestV2()
	old.Gleipnir.EventKinds = []manifestv2.EventKindDecl{{
		Kind:      "message",
		Operators: map[string][]string{"channel": {"equals", "contains"}},
	}}

	new := baseManifestV2()
	new.Gleipnir.EventKinds = []manifestv2.EventKindDecl{{
		Kind:      "message",
		Operators: map[string][]string{"channel": {"contains", "equals"}},
	}}

	changes := pluginmanifest.DiffV2(old, new)
	for _, c := range changes {
		if c.Field == "event_kinds.message.operators" {
			t.Errorf("unexpected change for reordered operators: %+v", c)
		}
	}
}

// subscription_schema lives on the event_source profile, not on a kind — it
// is the scope events/listen validates an operator's watch scope against, and
// a shape change is material for the same reason config_schema shape changes
// are: stored subscription_scope_json might no longer validate.
func TestDiffV2_MaterialEventSourceSubscriptionSchemaChanged(t *testing.T) {
	schemaA := parseNode(t, `{"type":"object","properties":{"channels":{"type":"array"}}}`)
	schemaB := parseNode(t, `{"type":"object","properties":{"channels":{"type":"array"},"topics":{"type":"array"}}}`)

	old := baseManifestV2()
	old.Gleipnir.Profiles.EventSource.SubscriptionSchema = schemaA

	new := baseManifestV2()
	new.Gleipnir.Profiles.EventSource.SubscriptionSchema = schemaB

	changes := pluginmanifest.DiffV2(old, new)
	if !pluginmanifest.HasMaterial(changes) {
		t.Error("expected material change when profile subscription_schema gains a property")
	}
	for _, c := range changes {
		if c.Field == "profiles.event_source.subscription_schema" && c.Material {
			return
		}
	}
	t.Errorf("expected change for profiles.event_source.subscription_schema, got %v", changes)
}

// The same cosmetic-key stripping applied to config_schema applies here:
// description/default changes inside the schema must not block a hot-reload.
func TestDiffV2_EventSourceSubscriptionSchemaCosmeticOnly_NoMaterial(t *testing.T) {
	schemaA := parseNode(t, `{"type":"object","properties":{"channels":{"type":"array","description":"old"}}}`)
	schemaB := parseNode(t, `{"type":"object","properties":{"channels":{"type":"array","description":"new"}}}`)

	old := baseManifestV2()
	old.Gleipnir.Profiles.EventSource.SubscriptionSchema = schemaA

	new := baseManifestV2()
	new.Gleipnir.Profiles.EventSource.SubscriptionSchema = schemaB

	changes := pluginmanifest.DiffV2(old, new)
	for _, c := range changes {
		if c.Field == "profiles.event_source.subscription_schema" && c.Material {
			t.Errorf("subscription_schema description-only change must not be material, got %+v", c)
		}
	}
}

func TestDiffV2_NoChange_Empty(t *testing.T) {
	m := baseManifestV2()
	changes := pluginmanifest.DiffV2(m, m)
	if len(changes) != 0 {
		t.Errorf("expected no changes for identical v2 manifests, got %v", changes)
	}
}

func TestDiff_MaterialEventKindAdded(t *testing.T) {
	old := baseManifest()
	new := baseManifest()
	new.EventKinds = []sdkmanifest.EventKindDecl{{Kind: "alert"}}

	changes := pluginmanifest.Diff(old, new)
	if !pluginmanifest.HasMaterial(changes) {
		t.Error("expected material change when an event_kind is added")
	}
	for _, c := range changes {
		if c.Field == "event_kinds.alert" && c.Material {
			return
		}
	}
	t.Errorf("expected change for event_kinds.alert, got %v", changes)
}

func TestDiff_CosmeticDescriptionOnly_NoMaterial(t *testing.T) {
	old := baseManifest()
	new := baseManifest()
	new.Description = "An updated description"

	changes := pluginmanifest.Diff(old, new)
	if pluginmanifest.HasMaterial(changes) {
		t.Errorf("expected no material changes for description-only diff, got %v", pluginmanifest.MaterialFields(changes))
	}
	if len(changes) != 1 || changes[0].Field != "description" {
		t.Errorf("expected exactly one cosmetic change (description), got %v", changes)
	}
}

func TestDiff_CosmeticSchemaDescriptionAndDefault_NoMaterial(t *testing.T) {
	// Schemas that differ only in "description" and "default" values must not
	// produce a material change — those keys are stripped before comparison.
	schemaA := parseNode(t, `{"type":"object","properties":{"token":{"type":"string","description":"old desc","default":"x"}}}`)
	schemaB := parseNode(t, `{"type":"object","properties":{"token":{"type":"string","description":"new desc","default":"y"}}}`)

	old := baseManifest()
	old.ConfigSchema = schemaA

	new := baseManifest()
	new.ConfigSchema = schemaB

	changes := pluginmanifest.Diff(old, new)
	if pluginmanifest.HasMaterial(changes) {
		t.Errorf("expected no material changes for schema-description-only diff, got %v", pluginmanifest.MaterialFields(changes))
	}
}

func TestDiff_ConfigSchemaNewlyRequiredFields_ReturnsAddedNames(t *testing.T) {
	schemaOld := parseNode(t, `{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"string"}},"required":["a"]}`)
	schemaNew := parseNode(t, `{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"string"}},"required":["a","b"]}`)

	old := baseManifest()
	old.ConfigSchema = schemaOld

	new := baseManifest()
	new.ConfigSchema = schemaNew

	added, err := pluginmanifest.ConfigSchemaNewlyRequiredFields(old, new)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(added) != 1 || added[0] != "b" {
		t.Errorf("newly required fields = %v, want [b]", added)
	}
}

func TestDiff_ConfigSchemaNewlyRequiredFields_NilOld(t *testing.T) {
	schemaNew := parseNode(t, `{"type":"object","properties":{"token":{"type":"string"}},"required":["token"]}`)

	old := baseManifest()
	new := baseManifest()
	new.ConfigSchema = schemaNew

	added, err := pluginmanifest.ConfigSchemaNewlyRequiredFields(old, new)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(added) != 1 || added[0] != "token" {
		t.Errorf("newly required fields = %v, want [token]", added)
	}
}

func TestDiff_ConfigSchemaNewlyRequiredFields_MalformedRequired(t *testing.T) {
	cases := []struct {
		name      string
		oldYAML   string
		newYAML   string
		wantErrIn string // "old" or "new"
	}{
		{
			name:      "new required is a scalar string",
			oldYAML:   `{"type":"object"}`,
			newYAML:   `{"type":"object","required":"api_key"}`,
			wantErrIn: "new",
		},
		{
			name:      "new required is a map",
			oldYAML:   `{"type":"object"}`,
			newYAML:   `{"type":"object","required":{"api_key":true}}`,
			wantErrIn: "new",
		},
		{
			name:      "new required array contains a non-string element",
			oldYAML:   `{"type":"object"}`,
			newYAML:   `{"type":"object","required":["api_key",42]}`,
			wantErrIn: "new",
		},
		{
			name:      "old required is a scalar string",
			oldYAML:   `{"type":"object","required":"token"}`,
			newYAML:   `{"type":"object","required":["token"]}`,
			wantErrIn: "old",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			old := baseManifest()
			old.ConfigSchema = parseNode(t, tc.oldYAML)

			new := baseManifest()
			new.ConfigSchema = parseNode(t, tc.newYAML)

			_, err := pluginmanifest.ConfigSchemaNewlyRequiredFields(old, new)
			if err == nil {
				t.Fatal("expected error for malformed required, got nil")
			}
			if tc.wantErrIn != "" {
				errStr := err.Error()
				if !strings.Contains(errStr, tc.wantErrIn+" config schema") {
					t.Errorf("error %q should mention %q config schema", errStr, tc.wantErrIn)
				}
			}
		})
	}
}

func TestDiff_MaterialSubscriptionSchemaShapeChanged(t *testing.T) {
	schemaA := parseNode(t, `{"type":"object","properties":{"channels":{"type":"array","items":{"type":"string"}}}}`)
	schemaB := parseNode(t, `{"type":"object","properties":{"channels":{"type":"array","items":{"type":"string"}},"topics":{"type":"array","items":{"type":"string"}}}}`)

	old := baseManifest()
	old.SubscriptionSchema = schemaA

	new := baseManifest()
	new.SubscriptionSchema = schemaB

	changes := pluginmanifest.Diff(old, new)
	if !pluginmanifest.HasMaterial(changes) {
		t.Error("expected material change when subscription_schema gains a property")
	}
	for _, c := range changes {
		if c.Field == "subscription_schema" && c.Material {
			return
		}
	}
	t.Errorf("expected change for subscription_schema, got %v", changes)
}

func TestDiff_SubscriptionSchemaIdentical_NoDiff(t *testing.T) {
	schema := parseNode(t, `{"type":"object","properties":{"channels":{"type":"array"}}}`)

	old := baseManifest()
	old.SubscriptionSchema = schema

	new := baseManifest()
	new.SubscriptionSchema = parseNode(t, `{"type":"object","properties":{"channels":{"type":"array"}}}`)

	changes := pluginmanifest.Diff(old, new)
	for _, c := range changes {
		if c.Field == "subscription_schema" {
			t.Errorf("unexpected change for subscription_schema when schemas are identical: %+v", c)
		}
	}
}

func TestDiff_SubscriptionSchemaCosmeticOnly_NoMaterial(t *testing.T) {
	// description/default changes are stripped → not material.
	schemaA := parseNode(t, `{"type":"object","properties":{"channels":{"type":"array","description":"old"}}}`)
	schemaB := parseNode(t, `{"type":"object","properties":{"channels":{"type":"array","description":"new"}}}`)

	old := baseManifest()
	old.SubscriptionSchema = schemaA

	new := baseManifest()
	new.SubscriptionSchema = schemaB

	changes := pluginmanifest.Diff(old, new)
	for _, c := range changes {
		if c.Field == "subscription_schema" && c.Material {
			t.Errorf("subscription_schema description-only change must not be material, got %+v", c)
		}
	}
}

func TestDiff_NoChange_Empty(t *testing.T) {
	m := baseManifest()
	changes := pluginmanifest.Diff(m, m)
	if len(changes) != 0 {
		t.Errorf("expected no changes for identical manifests, got %v", changes)
	}
}
