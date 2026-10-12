package events_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/db"
)

const v2TestDigest = "@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// v2EventManifest is a minimal valid schema_version "2" manifest attesting
// the event kinds message.posted and reaction.added.
const v2EventManifest = `
schema_version: "2"
name: acme-notify
version: 1.0.0
package:
  registry_type: oci
  identifier: ghcr.io/acme/notify` + v2TestDigest + `
  transport:
    type: streamable-http
    port: 8080
gleipnir:
  profiles:
    event_source: {}
  event_kinds:
    - kind: message.posted
      description: A message was posted.
    - kind: reaction.added
      description: A reaction was added.
`

// TestSupervisor_V2Manifest_ResolvesAttestedKinds verifies the supervisor
// reads the attested kinds out of a v2 manifest_snapshot rather than
// treating it as an unreadable (or empty) v1 manifest.
func TestSupervisor_V2Manifest_ResolvesAttestedKinds(t *testing.T) {
	t.Parallel()

	q := newFakeSupervisorQuerier()
	q.setInstance(db.PluginInstance{ID: "inst-1", PluginID: "plug-1", SubscriptionScopeJson: `{}`})
	q.setPlugin(db.Plugin{ID: "plug-1", ManifestSnapshot: v2EventManifest})

	opener := newFakeOpener(func(int) *fakeStream { return &fakeStream{} })
	sup := newTestSupervisor(t, q, &fakeResolver{opener: opener}, nil, newFakeCursorStore(), newFakeSink(), nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup.Start(ctx, "inst-1")
	t.Cleanup(sup.StopAll)

	params := waitOnChan(t, opener.opened, testTimeout, "stream to open for a v2 manifest")
	want := []string{"message.posted", "reaction.added"}
	if !reflect.DeepEqual(params.Kinds, want) {
		t.Errorf("listen kinds = %v, want %v", params.Kinds, want)
	}
}
