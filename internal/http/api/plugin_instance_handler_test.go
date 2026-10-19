package api_test

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/configvalidate"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

const identityV2ManifestYAML = `schema_version: "2"
name: chat-v2
version: 1.0.0
description: A containerized chat plugin
package:
  registry_type: oci
  identifier: ghcr.io/acme/chat-v2@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
  version: 1.0.0
  transport:
    type: streamable-http
    port: 8080
gleipnir:
  profiles:
    identity_provider:
      link_methods: [admin_set, inbound_code]
  auth:
    strategy: none
`

func TestListPluginInstances_IdentityLinkMethods(t *testing.T) {
	store := testutil.NewTestStore(t)
	snap := configvalidate.NewSnapshotter(store.Queries())
	t.Cleanup(func() { snap.ResetCache(); configvalidate.ResetCache() })

	v2Plugin := seedPlugin(t, store, "chat-v2", identityV2ManifestYAML)
	seedPluginInstance(t, store, "chat-v2-inst", v2Plugin)
	v1Plugin := seedPlugin(t, store, "notify-v1", notifyOnlyManifestYAML)
	seedPluginInstance(t, store, "notify-v1-inst", v1Plugin)

	w := do(t, newPluginInstancesRouter(store, snap), http.MethodGet, "/", nil, model.RoleAdmin)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", w.Code, w.Body.String())
	}
	var items []struct {
		ID                  string   `json:"id"`
		IdentityLinkMethods []string `json:"identity_link_methods"`
	}
	parseData(t, w, &items)

	got := map[string][]string{}
	for _, it := range items {
		if it.IdentityLinkMethods == nil {
			t.Errorf("%s: identity_link_methods is null, want an array", it.ID)
		}
		got[it.ID] = it.IdentityLinkMethods
	}
	if want := []string{"admin_set", "inbound_code"}; !reflect.DeepEqual(got["chat-v2-inst"], want) {
		t.Errorf("v2 methods = %v, want %v", got["chat-v2-inst"], want)
	}
	if len(got["notify-v1-inst"]) != 0 {
		t.Errorf("v1 methods = %v, want empty", got["notify-v1-inst"])
	}
}
