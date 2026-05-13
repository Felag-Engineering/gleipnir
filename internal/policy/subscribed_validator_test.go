package policy

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	sdkmanifest "github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/configvalidate"
	"gopkg.in/yaml.v3"
)

// fakeResolver implements InstanceManifestResolver for tests.
type fakeResolver struct {
	instances map[string]string // name → instanceID
}

func (f *fakeResolver) ResolveInstanceByName(_ context.Context, name string) (string, error) {
	id, ok := f.instances[name]
	if !ok {
		return "", fmt.Errorf("instance not found: %q", name)
	}
	return id, nil
}

// fakeManifestQuerier implements configvalidate.ManifestQuerier for tests.
type fakeManifestQuerier struct {
	plugins   map[string]db.Plugin         // pluginID → Plugin row
	instances map[string]db.PluginInstance // instanceID → PluginInstance row
}

func (q *fakeManifestQuerier) GetPluginByID(_ context.Context, id string) (db.Plugin, error) {
	p, ok := q.plugins[id]
	if !ok {
		return db.Plugin{}, sql.ErrNoRows
	}
	return p, nil
}

func (q *fakeManifestQuerier) GetPluginInstanceByID(_ context.Context, id string) (db.PluginInstance, error) {
	inst, ok := q.instances[id]
	if !ok {
		return db.PluginInstance{}, sql.ErrNoRows
	}
	return inst, nil
}

// buildManifestYAML returns raw manifest YAML for a TriggerService plugin with
// the given event kinds and optional binding schemas.
func buildManifestYAML(t *testing.T, eventKinds []sdkmanifest.EventKindDecl) string {
	t.Helper()
	m := sdkmanifest.Manifest{
		SchemaVersion: "1",
		Name:          "test-plugin",
		Version:       "1.0.0",
		Services:      sdkmanifest.Services{Trigger: "v1"},
		EventKinds:    eventKinds,
	}
	raw, err := yaml.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	return string(raw)
}

// buildBindingSchema returns a *yaml.Node for a simple JSON Schema that
// requires a string field named "filter".
func buildBindingSchema(t *testing.T) *yaml.Node {
	t.Helper()
	schemaYAML := `
type: object
properties:
  filter:
    type: string
required:
  - filter
additionalProperties: false
`
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(schemaYAML), &node); err != nil {
		t.Fatalf("unmarshal binding schema: %v", err)
	}
	// yaml.Unmarshal produces a document node; unwrap to the content node.
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		return node.Content[0]
	}
	return &node
}

// newTestValidator wires up a SubscribedBindingValidator with fakes. It returns
// the validator plus the underlying Snapshotter so tests can call ResetCache.
func newTestValidator(t *testing.T, instanceName, instanceID, manifestYAML string) (*SubscribedBindingValidator, *configvalidate.Snapshotter) {
	t.Helper()

	resolver := &fakeResolver{
		instances: map[string]string{instanceName: instanceID},
	}
	querier := &fakeManifestQuerier{
		plugins: map[string]db.Plugin{
			"plugin-1": {ID: "plugin-1", ManifestSnapshot: manifestYAML},
		},
		instances: map[string]db.PluginInstance{
			instanceID: {ID: instanceID, PluginID: "plugin-1"},
		},
	}
	snap := configvalidate.NewSnapshotter(querier)
	return NewSubscribedBindingValidator(resolver, snap), snap
}

func subscribedTrigger(source, eventKind string, binding map[string]any) model.TriggerConfig {
	return model.TriggerConfig{
		Type:      model.TriggerTypeSubscribed,
		Source:    source,
		EventKind: eventKind,
		Binding:   binding,
	}
}

func TestSubscribedValidator_NonSubscribedTrigger(t *testing.T) {
	// Non-subscribed triggers must be skipped immediately (nil return).
	v, _ := newTestValidator(t, "inst", "inst-id", buildManifestYAML(t, nil))
	issues := v.Validate(context.Background(), model.TriggerConfig{Type: model.TriggerTypeWebhook})
	if issues != nil {
		t.Errorf("expected nil for non-subscribed trigger, got %v", issues)
	}
}

func TestSubscribedValidator_UnknownSource(t *testing.T) {
	manifestYAML := buildManifestYAML(t, []sdkmanifest.EventKindDecl{{Kind: "heartbeat"}})
	v, snap := newTestValidator(t, "known-instance", "inst-1", manifestYAML)
	defer snap.ResetCache()

	issues := v.Validate(context.Background(), subscribedTrigger("unknown-instance", "heartbeat", nil))
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d: %v", len(issues), issues)
	}
	if issues[0].Field != "trigger.source" {
		t.Errorf("field = %q, want trigger.source", issues[0].Field)
	}
	if !containsString(issues[0].Message, "unknown-instance") {
		t.Errorf("message %q should mention the unknown instance name", issues[0].Message)
	}
}

func TestSubscribedValidator_NoTriggerService(t *testing.T) {
	// Manifest without Services.Trigger set.
	noTriggerManifest := `
schema_version: "1"
name: test-plugin
version: 1.0.0
services: {}
`
	querier := &fakeManifestQuerier{
		plugins:   map[string]db.Plugin{"p1": {ID: "p1", ManifestSnapshot: noTriggerManifest}},
		instances: map[string]db.PluginInstance{"i1": {ID: "i1", PluginID: "p1"}},
	}
	snap := configvalidate.NewSnapshotter(querier)
	defer snap.ResetCache()
	resolver := &fakeResolver{instances: map[string]string{"my-inst": "i1"}}
	v := NewSubscribedBindingValidator(resolver, snap)

	issues := v.Validate(context.Background(), subscribedTrigger("my-inst", "heartbeat", nil))
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d: %v", len(issues), issues)
	}
	if issues[0].Field != "trigger.source" {
		t.Errorf("field = %q, want trigger.source", issues[0].Field)
	}
	if !containsString(issues[0].Message, "TriggerService") {
		t.Errorf("message %q should mention TriggerService", issues[0].Message)
	}
}

func TestSubscribedValidator_UnknownEventKind(t *testing.T) {
	manifestYAML := buildManifestYAML(t, []sdkmanifest.EventKindDecl{{Kind: "heartbeat"}})
	v, snap := newTestValidator(t, "inst", "i1", manifestYAML)
	defer snap.ResetCache()

	issues := v.Validate(context.Background(), subscribedTrigger("inst", "no_such_kind", nil))
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d: %v", len(issues), issues)
	}
	if issues[0].Field != "trigger.event_kind" {
		t.Errorf("field = %q, want trigger.event_kind", issues[0].Field)
	}
	if !containsString(issues[0].Message, "no_such_kind") {
		t.Errorf("message %q should mention the unknown event kind", issues[0].Message)
	}
}

func TestSubscribedValidator_ValidNoBinding(t *testing.T) {
	manifestYAML := buildManifestYAML(t, []sdkmanifest.EventKindDecl{{Kind: "heartbeat"}})
	v, snap := newTestValidator(t, "inst", "i1", manifestYAML)
	defer snap.ResetCache()

	issues := v.Validate(context.Background(), subscribedTrigger("inst", "heartbeat", nil))
	if len(issues) != 0 {
		t.Errorf("expected no issues, got %v", issues)
	}
}

func TestSubscribedValidator_BindingSchemaViolation(t *testing.T) {
	schema := buildBindingSchema(t) // requires "filter" string field
	manifestYAML := buildManifestYAML(t, []sdkmanifest.EventKindDecl{
		{Kind: "channel_message", BindingSchema: schema},
	})
	v, snap := newTestValidator(t, "inst", "i1", manifestYAML)
	defer snap.ResetCache()

	// Provide a binding that's missing the required "filter" field.
	issues := v.Validate(context.Background(), subscribedTrigger("inst", "channel_message", map[string]any{
		"unknown_field": "bad",
	}))
	if len(issues) == 0 {
		t.Fatal("expected binding schema violation issues, got none")
	}
	for _, iss := range issues {
		if !containsString(iss.Field, "trigger.binding") {
			t.Errorf("field %q should start with trigger.binding", iss.Field)
		}
	}
}

func TestSubscribedValidator_ValidBinding(t *testing.T) {
	schema := buildBindingSchema(t) // requires "filter" string field
	manifestYAML := buildManifestYAML(t, []sdkmanifest.EventKindDecl{
		{Kind: "channel_message", BindingSchema: schema},
	})
	v, snap := newTestValidator(t, "inst", "i1", manifestYAML)
	defer snap.ResetCache()

	issues := v.Validate(context.Background(), subscribedTrigger("inst", "channel_message", map[string]any{
		"filter": ".*",
	}))
	if len(issues) != 0 {
		t.Errorf("expected no issues for valid binding, got %v", issues)
	}
}

func TestSubscribedValidator_CacheHit(t *testing.T) {
	// Repeated calls with the same manifest+eventKind must compile the binding
	// validator schema only once. We verify this by ensuring ForTriggerBinding
	// is idempotent: two Validate calls succeed and produce no issues. The
	// underlying cachedCompile function is covered in configvalidate tests; here
	// we just confirm the integration is wired correctly.
	schema := buildBindingSchema(t)
	manifestYAML := buildManifestYAML(t, []sdkmanifest.EventKindDecl{
		{Kind: "channel_message", BindingSchema: schema},
	})
	v, snap := newTestValidator(t, "inst", "i1", manifestYAML)
	defer snap.ResetCache()

	tc := subscribedTrigger("inst", "channel_message", map[string]any{"filter": "x"})

	for i := range 3 {
		issues := v.Validate(context.Background(), tc)
		if len(issues) != 0 {
			t.Fatalf("call %d: unexpected issues %v", i+1, issues)
		}
	}
}

// containsString is a local test helper.
func containsString(s, substr string) bool {
	return strings.Contains(s, substr)
}
