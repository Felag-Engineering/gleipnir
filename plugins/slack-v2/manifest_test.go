package main

import (
	"os"
	"slices"
	"sort"
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
	for _, scopes := range toolScopes {
		for _, s := range scopes {
			want[s] = true
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
	if got := m.Gleipnir.Profiles.Declared(); !slices.Equal(got, []string{"tool_provider"}) {
		t.Errorf("profiles = %v, want [tool_provider]", got)
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
