package main

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/felag-engineering/gleipnir/plugin-sdk/manifestv2"
)

func loadManifest(t *testing.T) *manifestv2.Manifest {
	t.Helper()
	data, err := os.ReadFile("manifest.yaml")
	if err != nil {
		t.Fatalf("read manifest.yaml: %v", err)
	}
	m, err := manifestv2.Parse(data)
	if err != nil {
		t.Fatalf("manifestv2.Parse: %v", err)
	}
	return m
}

func TestManifestDeclaresTheRegisteredTools(t *testing.T) {
	m := loadManifest(t)

	var declared []string
	for _, tool := range m.Gleipnir.Tools {
		declared = append(declared, tool.Name)
	}

	p := &plugin{}
	var registered []string
	for _, tool := range p.allTools() {
		registered = append(registered, tool.Name)
	}

	sort.Strings(declared)
	sort.Strings(registered)
	if !slices.Equal(declared, registered) {
		t.Errorf("manifest tools = %v, registered tools = %v", declared, registered)
	}
}

func TestManifestScopesAreTheUnionOfToolScopes(t *testing.T) {
	m := loadManifest(t)

	want := map[string]bool{}
	for _, table := range []map[string][]string{toolScopes, channelScopes, eventScopes} {
		for _, scopes := range table {
			for _, s := range scopes {
				want[s] = true
			}
		}
	}
	got := map[string]bool{}
	for _, s := range m.Gleipnir.Auth.OAuthDefaults.Scopes {
		got[s] = true
	}
	for s := range want {
		if !got[s] {
			t.Errorf("scope %q is required by a tool but not declared in the manifest", s)
		}
	}
	for s := range got {
		if !want[s] {
			t.Errorf("scope %q is declared in the manifest but no tool needs it", s)
		}
	}
}

func TestManifestShape(t *testing.T) {
	m := loadManifest(t)

	if m.Version != pluginVersion {
		t.Errorf("manifest version = %q, pluginVersion = %q", m.Version, pluginVersion)
	}
	if got := m.Gleipnir.Auth.Strategy; got != manifestv2.AuthStrategyOAuth2Authcode {
		t.Errorf("auth strategy = %q, want %q", got, manifestv2.AuthStrategyOAuth2Authcode)
	}
	if got := m.Gleipnir.Profiles.Declared(); !slices.Equal(got, []string{"tool_provider", "event_source", "human_channel", "identity_provider"}) {
		t.Errorf("profiles = %v, want [tool_provider event_source human_channel identity_provider]", got)
	}
	if got := m.Gleipnir.Profiles.HumanChannel.Assurance; got != channelDeclaration.Assurance {
		t.Errorf("manifest human_channel assurance = %q, served declaration says %q", got, channelDeclaration.Assurance)
	}

	var domains []string
	for _, e := range m.Gleipnir.Egress {
		domains = append(domains, e.Domain)
	}
	if want := []string{"slack.com", "*.slack.com"}; !slices.Equal(domains, want) {
		t.Errorf("egress = %v, want %v", domains, want)
	}

	secrets, err := manifestv2.SecretPropertyNames(m.Gleipnir.ConfigSchema)
	if err != nil {
		t.Fatalf("SecretPropertyNames: %v", err)
	}
	if !secrets["app_token"] {
		t.Errorf("config_schema.app_token is not marked x-gleipnir-secret: %v", secrets)
	}
}

// discoveredKind is one events/discover entry as the host reads it.
type discoveredKind struct {
	Kind          string              `json:"kind"`
	Guidance      string              `json:"guidance"`
	BindingSchema any                 `json:"binding_schema"`
	Operators     map[string][]string `json:"operators"`
}

// TestManifestAttestsTheServedEventKinds is the drift check: what the live
// server discovers must be what the manifest attests, kind for kind, down to
// the binding schemas and attested operators. The host reports a mismatch in
// either direction as a capability fault.
func TestManifestAttestsTheServedEventKinds(t *testing.T) {
	m := loadManifest(t)
	srv := newTestServer(t, &fakeHost{}, "http://unused.invalid/api/")

	res := rpc(t, srv, "events/discover", nil)
	if res.Error != nil {
		t.Fatalf("events/discover: %s", res.Error.Message)
	}
	var discovered struct {
		Kinds []discoveredKind `json:"kinds"`
	}
	if err := json.Unmarshal(res.Result, &discovered); err != nil {
		t.Fatalf("decode events/discover: %v", err)
	}

	attested := map[string]manifestv2.EventKindDecl{}
	for _, k := range m.Gleipnir.EventKinds {
		attested[k.Kind] = k
	}
	if len(discovered.Kinds) != len(attested) {
		t.Errorf("server discovers %d kinds, manifest attests %d", len(discovered.Kinds), len(attested))
	}
	for _, got := range discovered.Kinds {
		want, ok := attested[got.Kind]
		if !ok {
			t.Errorf("kind %q is discovered but not attested", got.Kind)
			continue
		}
		delete(attested, got.Kind)

		if got.Guidance != want.Guidance {
			t.Errorf("kind %q: guidance differs\n served: %s\n manifest: %s", got.Kind, got.Guidance, want.Guidance)
		}
		if !reflect.DeepEqual(got.Operators, want.Operators) {
			t.Errorf("kind %q: operators differ\n served: %v\n manifest: %v", got.Kind, got.Operators, want.Operators)
		}
		var wantSchema any
		if err := want.BindingSchema.Decode(&wantSchema); err != nil {
			t.Fatalf("kind %q: decode manifest binding_schema: %v", got.Kind, err)
		}
		if !reflect.DeepEqual(normalizeJSON(t, got.BindingSchema), normalizeJSON(t, wantSchema)) {
			t.Errorf("kind %q: binding_schema differs\n served: %v\n manifest: %v", got.Kind, got.BindingSchema, wantSchema)
		}
	}
	for kind := range attested {
		t.Errorf("kind %q is attested but not discovered", kind)
	}
}

// normalizeJSON round-trips v through JSON so a YAML-decoded value and a
// served one compare on shape and value, not on Go types.
func normalizeJSON(t *testing.T, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSubscriptionSchemaDescribesEventScope(t *testing.T) {
	m := loadManifest(t)
	schema := m.Gleipnir.Profiles.EventSource.SubscriptionSchema
	if schema == nil {
		t.Fatal("event_source declares no subscription_schema")
	}
	var parsed struct {
		Properties map[string]any `yaml:"properties"`
	}
	if err := schema.Decode(&parsed); err != nil {
		t.Fatalf("decode subscription_schema: %v", err)
	}

	var want []string
	typ := reflect.TypeOf(eventScope{})
	for i := 0; i < typ.NumField(); i++ {
		want = append(want, strings.Split(typ.Field(i).Tag.Get("json"), ",")[0])
	}
	var got []string
	for name := range parsed.Properties {
		got = append(got, name)
	}
	sort.Strings(want)
	sort.Strings(got)
	if !slices.Equal(got, want) {
		t.Errorf("subscription_schema properties = %v, eventScope fields = %v", got, want)
	}
}
