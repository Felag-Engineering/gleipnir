package trigger_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/plugin/dedup"
	plugintrigger "github.com/felag-engineering/gleipnir/internal/plugin/trigger"
)

const v2TestDigest = "@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// v2ManifestWithMessageKind is a valid schema_version "2" manifest attesting
// exactly one event kind, message.posted, whose binding schema has a single
// string field "channel".
const v2ManifestWithMessageKind = `
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
      binding_schema:
        type: object
        properties:
          channel:
            type: string
`

// auditRecordingQuerier wraps fakeQuerier with an InsertPluginAuditEvent that
// records rows, so tests can assert on the plugin_audit_events writes.
type auditRecordingQuerier struct {
	*fakeQuerier
	mu   sync.Mutex
	rows []db.InsertPluginAuditEventParams
}

func (q *auditRecordingQuerier) InsertPluginAuditEvent(_ context.Context, arg db.InsertPluginAuditEventParams) (db.PluginAuditEvent, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.rows = append(q.rows, arg)
	return db.PluginAuditEvent{}, nil
}

func (q *auditRecordingQuerier) auditRows() []db.InsertPluginAuditEventParams {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]db.InsertPluginAuditEventParams(nil), q.rows...)
}

func newV2Querier(manifest, eventKind string, binding map[string]any) *auditRecordingQuerier {
	return &auditRecordingQuerier{fakeQuerier: &fakeQuerier{
		policies: []db.Policy{{
			ID:        "pol-1",
			Yaml:      policyYAML("my-instance", eventKind, binding),
			UpdatedAt: "t1",
		}},
		instances: map[string]db.PluginInstance{
			"inst-1": {ID: "inst-1", PluginID: "plug-1", InstanceName: "my-instance"},
		},
		plugins: map[string]db.Plugin{
			"plug-1": {ID: "plug-1", ManifestSnapshot: manifest},
		},
	}}
}

func v2Event(eventKind string, payload map[string]any) plugintrigger.Event {
	b, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return plugintrigger.Event{
		InstanceID:  "inst-1",
		PluginID:    "plug-1",
		EventKind:   eventKind,
		EventID:     "event-1",
		PayloadJSON: b,
		ObservedAt:  time.Now(),
	}
}

// TestDispatcher_V2Manifest_AttestedKindBindingEvaluated verifies that a v2
// kind's binding_schema is read and honored: a matching payload launches, a
// non-matching one does not.
func TestDispatcher_V2Manifest_AttestedKindBindingEvaluated(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		payload    map[string]any
		wantLaunch int
	}{
		{"matching payload launches", map[string]any{"channel": "general"}, 1},
		{"non-matching payload does not launch", map[string]any{"channel": "random"}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			q := newV2Querier(v2ManifestWithMessageKind, "message.posted", map[string]any{"channel": "general"})
			launcher := &fakeLauncher{}
			d := newDispatcher(q, dedup.Noop{}, launcher, &fakePublisher{})

			if err := d.Handle(context.Background(), v2Event("message.posted", tc.payload)); err != nil {
				t.Fatalf("Handle: %v", err)
			}
			if got := launcher.callCount(); got != tc.wantLaunch {
				t.Errorf("launches = %d, want %d", got, tc.wantLaunch)
			}
			if rows := q.auditRows(); len(rows) != 0 {
				t.Errorf("attested kind wrote %d audit rows, want 0", len(rows))
			}
		})
	}
}

// TestDispatcher_V2Manifest_UnattestedKindMatchesNothing verifies the manifest
// is the consent record: a v2 event of a kind the manifest does not attest
// launches nothing (even for a policy with an empty binding, which would
// otherwise match everything) and leaves an event_kind_unattested audit row.
func TestDispatcher_V2Manifest_UnattestedKindMatchesNothing(t *testing.T) {
	t.Parallel()

	q := newV2Querier(v2ManifestWithMessageKind, "reaction.added", nil)
	launcher := &fakeLauncher{}
	d := newDispatcher(q, dedup.Noop{}, launcher, &fakePublisher{})

	if err := d.Handle(context.Background(), v2Event("reaction.added", map[string]any{"emoji": "tada"})); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := launcher.callCount(); got != 0 {
		t.Errorf("launches = %d, want 0 for an unattested v2 kind", got)
	}

	rows := q.auditRows()
	if len(rows) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(rows))
	}
	if rows[0].EventType != "event_kind_unattested" {
		t.Errorf("audit event_type = %q, want event_kind_unattested", rows[0].EventType)
	}
	if rows[0].PluginInstanceID == nil || *rows[0].PluginInstanceID != "inst-1" {
		t.Errorf("audit plugin_instance_id = %v, want inst-1", rows[0].PluginInstanceID)
	}
}

// TestDispatcher_V2Manifest_UnparseableFailsClosed verifies a v2 snapshot that
// no longer validates never degrades to match-everything.
func TestDispatcher_V2Manifest_UnparseableFailsClosed(t *testing.T) {
	t.Parallel()

	broken := `
schema_version: "2"
name: acme-notify
version: 1.0.0
gleipnir:
  event_kinds:
    - kind: message.posted
`
	q := newV2Querier(broken, "message.posted", nil)
	launcher := &fakeLauncher{}
	d := newDispatcher(q, dedup.Noop{}, launcher, &fakePublisher{})

	if err := d.Handle(context.Background(), v2Event("message.posted", map[string]any{"channel": "general"})); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := launcher.callCount(); got != 0 {
		t.Errorf("launches = %d, want 0 for an unparseable v2 snapshot", got)
	}
}
