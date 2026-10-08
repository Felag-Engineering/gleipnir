package manifest

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/felag-engineering/gleipnir/plugin-sdk/manifestv2"
	"gopkg.in/yaml.v3"
)

func v2Base() *manifestv2.Manifest {
	return &manifestv2.Manifest{
		SchemaVersion: manifestv2.SchemaVersion,
		Name:          "test-plugin",
		Version:       "1.0.0",
	}
}

func v2WithResources(memMB, cpu int) *manifestv2.Manifest {
	m := v2Base()
	m.Gleipnir.Resources = &manifestv2.Resources{MemoryMB: memMB, CPUMillicores: cpu}
	return m
}

func v2WithEgress(grants ...manifestv2.EgressGrant) *manifestv2.Manifest {
	m := v2Base()
	m.Gleipnir.Egress = grants
	return m
}

func v2WithTools(tools ...manifestv2.ToolDecl) *manifestv2.Manifest {
	m := v2Base()
	m.Gleipnir.Tools = tools
	return m
}

func v2WithChannel(assurance string) *manifestv2.Manifest {
	m := v2Base()
	m.Gleipnir.Profiles.HumanChannel = &manifestv2.HumanChannelProfile{Assurance: assurance}
	return m
}

func v2WithIdentity(methods ...string) *manifestv2.Manifest {
	m := v2Base()
	m.Gleipnir.Profiles.IdentityProvider = &manifestv2.IdentityProviderProfile{LinkMethods: methods}
	return m
}

func yamlNode(t *testing.T, src string) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatalf("yaml: %v", err)
	}
	return doc.Content[0]
}

// TestDiffV2_CapabilityWidening pins which v2 manifest edits are material.
// wantMaterial lists Change.Field values that must be material; wantCosmetic
// lists those that must be present but non-material.
func TestDiffV2_CapabilityWidening(t *testing.T) {
	withSchema := func(src string) *manifestv2.Manifest {
		m := v2Base()
		m.Gleipnir.ConfigSchema = yamlNode(t, src)
		return m
	}
	withUserSchema := func(src string) *manifestv2.Manifest {
		m := v2Base()
		m.Gleipnir.UserConfigSchema = yamlNode(t, src)
		return m
	}
	withImage := func(id string) *manifestv2.Manifest {
		m := v2Base()
		m.Package.Identifier = id
		return m
	}

	tests := []struct {
		name         string
		old, new     *manifestv2.Manifest
		wantMaterial []string
		wantCosmetic []string
	}{
		{name: "cpu increase", old: v2WithResources(512, 500), new: v2WithResources(512, 1000), wantMaterial: []string{"resources.cpu_millicores"}},
		{name: "cpu decrease", old: v2WithResources(512, 1000), new: v2WithResources(512, 500), wantCosmetic: []string{"resources.cpu_millicores"}},
		{name: "memory increase", old: v2WithResources(256, 500), new: v2WithResources(512, 500), wantMaterial: []string{"resources.memory_mb"}},
		{name: "memory decrease", old: v2WithResources(512, 500), new: v2WithResources(256, 500), wantCosmetic: []string{"resources.memory_mb"}},
		{name: "both increase", old: v2WithResources(256, 250), new: v2WithResources(512, 1000), wantMaterial: []string{"resources.memory_mb", "resources.cpu_millicores"}},
		{name: "memory up cpu down", old: v2WithResources(256, 1000), new: v2WithResources(512, 500), wantMaterial: []string{"resources.memory_mb"}, wantCosmetic: []string{"resources.cpu_millicores"}},
		// An absent field means the host default (256 MiB, 500m), not "no limit".
		{name: "memory limit removed above-default declared", old: v2WithResources(64, 500), new: v2Base(), wantMaterial: []string{"resources.memory_mb"}},
		{name: "memory limit removed below-default declared", old: v2WithResources(1024, 500), new: v2Base(), wantCosmetic: []string{"resources.memory_mb"}},
		{name: "cpu limit removed", old: v2WithResources(256, 100), new: v2Base(), wantMaterial: []string{"resources.cpu_millicores"}},
		{name: "zero cpu field equals unset", old: v2WithResources(256, 0), new: v2WithResources(256, 0)},
		{name: "none to declared lower than default", old: v2Base(), new: v2WithResources(128, 250), wantCosmetic: []string{"resources.memory_mb", "resources.cpu_millicores"}},
		{name: "none to declared higher than default", old: v2Base(), new: v2WithResources(4096, 2000), wantMaterial: []string{"resources.memory_mb", "resources.cpu_millicores"}},
		{name: "declared equal to default is no change", old: v2Base(), new: v2WithResources(256, 500)},
		{name: "invalid new resources fail closed", old: v2WithResources(256, 500), new: v2WithResources(1, 500), wantMaterial: []string{"resources"}},

		{name: "egress added", old: v2WithEgress(), new: v2WithEgress(manifestv2.EgressGrant{Domain: "api.example.com"}), wantMaterial: []string{"egress.api.example.com"}},
		{name: "egress wildcard added alongside existing", old: v2WithEgress(manifestv2.EgressGrant{Domain: "a.com"}), new: v2WithEgress(manifestv2.EgressGrant{Domain: "a.com"}, manifestv2.EgressGrant{Domain: "*.a.com"}), wantMaterial: []string{"egress.*.a.com"}},
		{name: "egress removed", old: v2WithEgress(manifestv2.EgressGrant{Domain: "a.com"}), new: v2WithEgress(), wantCosmetic: []string{"egress.a.com"}},
		{name: "egress reason edited", old: v2WithEgress(manifestv2.EgressGrant{Domain: "a.com", Reason: "x"}), new: v2WithEgress(manifestv2.EgressGrant{Domain: "a.com", Reason: "y"}), wantCosmetic: []string{"egress.a.com.reason"}},
		{name: "egress reordered", old: v2WithEgress(manifestv2.EgressGrant{Domain: "a.com"}, manifestv2.EgressGrant{Domain: "b.com"}), new: v2WithEgress(manifestv2.EgressGrant{Domain: "b.com"}, manifestv2.EgressGrant{Domain: "a.com"})},

		{name: "tool kind changed", old: v2WithTools(manifestv2.ToolDecl{Name: "t", ElicitationKind: "permission"}), new: v2WithTools(manifestv2.ToolDecl{Name: "t", ElicitationKind: "information"}), wantMaterial: []string{"tools.t.elicitation_kind"}},
		{name: "tool kind declared", old: v2WithTools(), new: v2WithTools(manifestv2.ToolDecl{Name: "t", ElicitationKind: "information"}), wantMaterial: []string{"tools.t.elicitation_kind"}},
		{name: "tool kind dropped", old: v2WithTools(manifestv2.ToolDecl{Name: "t", ElicitationKind: "permission"}), new: v2WithTools(), wantMaterial: []string{"tools.t.elicitation_kind"}},
		{name: "kindless tool decl is no change", old: v2WithTools(), new: v2WithTools(manifestv2.ToolDecl{Name: "t"})},

		{name: "human channel added", old: v2Base(), new: v2WithChannel("authenticated"), wantMaterial: []string{"profiles.human_channel"}},
		{name: "human channel removed", old: v2WithChannel("authenticated"), new: v2Base(), wantCosmetic: []string{"profiles.human_channel"}},
		{name: "assurance changed", old: v2WithChannel("weak"), new: v2WithChannel("authenticated"), wantMaterial: []string{"profiles.human_channel.assurance"}},
		{name: "event source added", old: v2Base(), new: func() *manifestv2.Manifest {
			m := v2Base()
			m.Gleipnir.Profiles.EventSource = &manifestv2.EventSourceProfile{}
			return m
		}(), wantMaterial: []string{"profiles.event_source"}},
		{name: "identity provider added", old: v2Base(), new: v2WithIdentity("inbound_code"), wantMaterial: []string{"profiles.identity_provider"}},
		{name: "link method added", old: v2WithIdentity("a"), new: v2WithIdentity("a", "b"), wantMaterial: []string{"profiles.identity_provider"}},
		{name: "link method removed", old: v2WithIdentity("a", "b"), new: v2WithIdentity("a"), wantCosmetic: []string{"profiles.identity_provider"}},
		{name: "identity provider removed", old: v2WithIdentity("a"), new: v2Base(), wantCosmetic: []string{"profiles.identity_provider"}},

		{name: "config schema changed", old: withSchema("type: object"), new: withSchema("type: object\nrequired: [x]"), wantMaterial: []string{"config_schema"}},
		{name: "config schema description only", old: withSchema("type: string\ndescription: a"), new: withSchema("type: string\ndescription: b")},
		{name: "user config schema changed", old: withUserSchema("type: object"), new: withUserSchema("type: array"), wantMaterial: []string{"user_config_schema"}},

		{name: "image changed", old: withImage("r/x@sha256:aa"), new: withImage("r/x@sha256:bb"), wantCosmetic: []string{"package.identifier"}},
		{name: "name changed", old: v2Base(), new: func() *manifestv2.Manifest { m := v2Base(); m.Name = "other"; return m }(), wantMaterial: []string{"name"}},
		{name: "version changed", old: v2Base(), new: func() *manifestv2.Manifest { m := v2Base(); m.Version = "2.0.0"; return m }(), wantCosmetic: []string{"version"}},
		{name: "identical", old: v2WithResources(512, 500), new: v2WithResources(512, 500)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			material := map[string]bool{}
			cosmetic := map[string]bool{}
			for _, c := range DiffV2(tc.old, tc.new) {
				if c.Material {
					material[c.Field] = true
				} else {
					cosmetic[c.Field] = true
				}
			}
			assertFields(t, "material", material, tc.wantMaterial)
			assertFields(t, "cosmetic", cosmetic, tc.wantCosmetic)
		})
	}
}

func assertFields(t *testing.T, kind string, got map[string]bool, want []string) {
	t.Helper()
	wantSet := map[string]bool{}
	for _, f := range want {
		wantSet[f] = true
	}
	if !reflect.DeepEqual(got, wantSet) && !(len(got) == 0 && len(wantSet) == 0) {
		t.Errorf("%s fields = %v, want %v", kind, sortedKeys(got), sortedKeys(wantSet))
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestDiffV2ClassifiesEveryManifestField fails when a field is added to any
// struct reachable from manifestv2.Manifest without being classified in
// v2FieldCoverage — the point of the table is that a new capability-bearing
// field cannot be silently missed by the material-change block (#1035).
func TestDiffV2ClassifiesEveryManifestField(t *testing.T) {
	seen := map[reflect.Type]bool{}
	reached := map[string]bool{}
	var walk func(rt reflect.Type)
	walk = func(rt reflect.Type) {
		for rt.Kind() == reflect.Ptr || rt.Kind() == reflect.Slice || rt.Kind() == reflect.Map {
			rt = rt.Elem()
		}
		if rt.Kind() != reflect.Struct || seen[rt] || rt == reflect.TypeOf(yaml.Node{}) {
			return
		}
		seen[rt] = true
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			reached[rt.Name()+"."+f.Name] = true
			walk(f.Type)
		}
	}
	walk(reflect.TypeOf(manifestv2.Manifest{}))

	var missing []string
	for key := range reached {
		reason, ok := v2FieldCoverage[key]
		if !ok {
			missing = append(missing, key)
			continue
		}
		if strings.TrimSpace(reason) == "" {
			t.Errorf("%s is classified with an empty reason", key)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("manifestv2 fields not classified in v2FieldCoverage: %s\n"+
			"Diff the field in DiffV2 (diff_v2_fields.go) if it can widen a capability, "+
			"then add it to v2FieldCoverage with the reason.", strings.Join(missing, ", "))
	}

	for key := range v2FieldCoverage {
		if !reached[key] {
			t.Errorf("v2FieldCoverage names %q, which is not a manifestv2 field", key)
		}
	}
}
