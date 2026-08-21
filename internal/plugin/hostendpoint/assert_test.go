package hostendpoint

import (
	"strings"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/toolregistry"
)

func TestAssertHostPlane(t *testing.T) {
	pluginSrc := toolregistry.Source{Kind: toolregistry.KindPlugin, Name: "slack-prod"}
	mcpSrc := toolregistry.Source{Kind: toolregistry.KindMCP, Name: "github"}

	t.Run("a registry of ordinary tools passes", func(t *testing.T) {
		reg := toolregistry.New()
		for _, name := range []string{"slack-prod.post_message", "github.list_repos"} {
			if err := reg.Reserve(name, pluginSrc); err != nil {
				t.Fatalf("Reserve(%s): %v", name, err)
			}
		}
		if err := AssertHostPlane(reg); err != nil {
			t.Fatalf("AssertHostPlane = %v, want nil", err)
		}
	})

	t.Run("a bare plugin tool that shares a host method's short name passes", func(t *testing.T) {
		// The reason ToolNamePrefix exists: `log` and `get_credentials` are
		// plausible legitimate plugin tool names, and the assertion must not
		// fail startup on an innocent plugin (tools.go).
		reg := toolregistry.New()
		for _, name := range []string{"slack-prod.log", "vault.get_credentials"} {
			if err := reg.Reserve(name, pluginSrc); err != nil {
				t.Fatalf("Reserve(%s): %v", name, err)
			}
		}
		if err := AssertHostPlane(reg); err != nil {
			t.Fatalf("AssertHostPlane = %v, want nil — bare short names are not host tools", err)
		}
	})

	t.Run("an exact host tool name in the registry fails startup", func(t *testing.T) {
		reg := toolregistry.New()
		if err := reg.Reserve(ToolLog, mcpSrc); err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		err := AssertHostPlane(reg)
		if err == nil {
			t.Fatal("AssertHostPlane = nil for a directly registered host tool — the invariant is not enforced")
		}
		if !strings.Contains(err.Error(), ToolLog) {
			t.Errorf("error does not name the offender: %v", err)
		}
	})

	t.Run("a source offering a host tool under its own prefix fails startup", func(t *testing.T) {
		// `slack-prod.host/log` is a plugin claiming to serve a host method;
		// granting it would hand an agent a tool that impersonates the host
		// plane. The dot-name's tool part matches, so it is a leak.
		reg := toolregistry.New()
		leaked := "slack-prod." + ToolLog
		if err := reg.Reserve(leaked, pluginSrc); err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		err := AssertHostPlane(reg)
		if err == nil {
			t.Fatal("AssertHostPlane = nil for a source-prefixed host tool")
		}
		if !strings.Contains(err.Error(), leaked) || !strings.Contains(err.Error(), "slack-prod") {
			t.Errorf("error does not name the offender and its source: %v", err)
		}
	})

	t.Run("a name merely containing 'host' is not a leak", func(t *testing.T) {
		reg := toolregistry.New()
		if err := reg.Reserve("net-tools.hostname_lookup", pluginSrc); err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		if err := AssertHostPlane(reg); err != nil {
			t.Fatalf("AssertHostPlane = %v, want nil", err)
		}
	})

	t.Run("nil registry passes — nothing is discoverable", func(t *testing.T) {
		if err := AssertHostPlane(nil); err != nil {
			t.Fatalf("AssertHostPlane(nil) = %v, want nil", err)
		}
	})
}

// TestToolNamesAllCarryThePrefix guards the property the assertion's
// exactness rests on: every host tool name starts with ToolNamePrefix, whose
// `/` cannot appear in a legitimate dot-name tool part. A prefix-less name
// added to the inventory would silently widen the assertion onto ordinary
// plugin tool names.
func TestToolNamesAllCarryThePrefix(t *testing.T) {
	names := ToolNames()
	if len(names) == 0 {
		t.Fatal("empty host tool inventory")
	}
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if !strings.HasPrefix(name, ToolNamePrefix) {
			t.Errorf("host tool %q does not carry the %q prefix", name, ToolNamePrefix)
		}
		if seen[name] {
			t.Errorf("duplicate host tool name %q", name)
		}
		seen[name] = true
	}
}
