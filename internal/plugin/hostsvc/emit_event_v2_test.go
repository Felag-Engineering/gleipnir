package hostsvc_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/plugin/hostsvc"
	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
)

// digestV2 is a syntactically valid 64-hex-char sha256 digest for the
// package.identifier field manifestv2.Validate requires.
const digestV2 = "@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// v1ManifestSnapshot is a v1 (schema_version: v1) manifest. IsV2 must read
// this as "not v2" so v1 plugins are entirely untouched by the #906 check.
const v1ManifestSnapshot = `schema_version: v1
name: myplugin
version: 1.0.0
auth:
  mode: instance_credentials
  strategy: none
services:
  tool: v1
`

// v2EventSourceManifest declares profiles.event_source — the ONLY shape
// EmitEvent must refuse.
const v2EventSourceManifest = `
schema_version: "2"
name: io.github.example/eventy
version: 1.0.0
package:
  registry_type: oci
  identifier: ghcr.io/example/eventy` + digestV2 + `
  transport:
    type: streamable-http
    port: 8080
gleipnir:
  profiles:
    event_source: {}
`

// v2ToolProviderManifest is a v2 manifest that does NOT declare
// profiles.event_source. A v2 manifest alone must not trigger the refusal.
const v2ToolProviderManifest = `
schema_version: "2"
name: io.github.example/toolsonly
version: 1.0.0
package:
  registry_type: oci
  identifier: ghcr.io/example/toolsonly` + digestV2 + `
  transport:
    type: streamable-http
    port: 8080
gleipnir:
  profiles:
    tool_provider: {}
`

// v2UnparseableManifest declares schema_version "2" (so manifestv2.IsV2
// routes it to the v2 pipeline) but fails manifestv2.Parse's validation: no
// profile is declared and the package identifier isn't digest-pinned. This
// is what "unparseable" means for the fail-open branch — a manifest whose
// installation the loader would already have refused.
const v2UnparseableManifest = `
schema_version: "2"
name: io.github.example/broken
version: 1.0.0
package:
  registry_type: oci
  identifier: not-digest-pinned
  transport:
    type: streamable-http
    port: 8080
gleipnir:
  profiles: {}
`

// TestEmitEvent_V2EventSourceRefusal table-tests the manifest-shape
// classification: only a v2 manifest that parses AND declares
// profiles.event_source is refused; every other shape (v1, v2 without
// event_source, unparseable v2) falls through to ordinary EmitEvent
// behavior unchanged.
func TestEmitEvent_V2EventSourceRefusal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		manifestSnapshot string
		wantRefused      bool
	}{
		{
			name:             "v1 manifest is untouched",
			manifestSnapshot: v1ManifestSnapshot,
			wantRefused:      false,
		},
		{
			name:             "empty manifest snapshot (existing test fixtures) is untouched",
			manifestSnapshot: "",
			wantRefused:      false,
		},
		{
			name:             "v2 manifest declaring event_source is refused",
			manifestSnapshot: v2EventSourceManifest,
			wantRefused:      true,
		},
		{
			name:             "v2 manifest without event_source still emits",
			manifestSnapshot: v2ToolProviderManifest,
			wantRefused:      false,
		},
		{
			name:             "unparseable v2 manifest fails open to v1 behavior",
			manifestSnapshot: v2UnparseableManifest,
			wantRefused:      false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			q := &fakeQuerier{
				instance: db.PluginInstance{ID: "iid-v2-" + tt.name, PluginID: "plug-v2"},
				plugin:   db.Plugin{ID: "plug-v2", ManifestSnapshot: tt.manifestSnapshot},
			}
			pub := &fakePublisher{}
			binder := &fakeInstanceBinder{id: q.instance.ID, ok: true}
			srv := hostsvc.NewServer(q, testEncryptionKey, &fakeResolver{}, binder, pub)

			resp, err := srv.EmitEvent(context.Background(), &hostv1.EmitEventRequest{
				EventId:     "evt-1",
				EventKind:   "user.created",
				PayloadJson: `{}`,
			})

			if tt.wantRefused {
				st, ok := status.FromError(err)
				if !ok || st.Code() != codes.FailedPrecondition {
					t.Fatalf("expected FailedPrecondition, got %v", err)
				}
				const wantMsg = "emit_event retired for v2 event-source plugins: this instance's events ride io.gleipnir/events (events/listen)"
				if st.Message() != wantMsg {
					t.Errorf("message = %q, want %q", st.Message(), wantMsg)
				}
				if len(pub.all()) != 0 {
					t.Errorf("refused call must not publish to the SSE bus, got %v", pub.all())
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !resp.GetOk() {
				t.Error("ok = false, want true")
			}
			events := pub.all()
			if len(events) != 1 || events[0] != "plugin.event_emitted" {
				t.Errorf("published events = %v, want [plugin.event_emitted]", events)
			}
		})
	}
}

// TestEmitEvent_V2EventSourceRefusal_AuditShape pins the audit event's type,
// severity, and payload key set for the refusal path.
func TestEmitEvent_V2EventSourceRefusal_AuditShape(t *testing.T) {
	t.Parallel()

	q := &fakeQuerier{
		instance: db.PluginInstance{ID: "iid-audit", PluginID: "plug-audit"},
		plugin:   db.Plugin{ID: "plug-audit", ManifestSnapshot: v2EventSourceManifest},
	}
	pub := &fakePublisher{}
	binder := &fakeInstanceBinder{id: "iid-audit", ok: true}
	srv := hostsvc.NewServer(q, testEncryptionKey, &fakeResolver{}, binder, pub)

	_, err := srv.EmitEvent(context.Background(), &hostv1.EmitEventRequest{
		EventId:     "evt-audit",
		EventKind:   "user.created",
		PayloadJson: `{}`,
	})
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", err)
	}

	events := q.all()
	if len(events) != 1 {
		t.Fatalf("audit event count = %d, want 1", len(events))
	}
	e := events[0]
	if e.EventType != hostsvc.EventTypeEmitEventRetiredProfile {
		t.Errorf("event_type = %q, want %q", e.EventType, hostsvc.EventTypeEmitEventRetiredProfile)
	}
	if e.Severity != "high" {
		t.Errorf("severity = %q, want high", e.Severity)
	}
	if e.PluginInstanceID == nil || *e.PluginInstanceID != "iid-audit" {
		t.Errorf("plugin_instance_id = %v, want iid-audit", e.PluginInstanceID)
	}

	var payload map[string]string
	if unmarshalErr := json.Unmarshal([]byte(e.PayloadJson), &payload); unmarshalErr != nil {
		t.Fatalf("parse audit payload: %v", unmarshalErr)
	}
	wantKeys := map[string]string{
		"plugin_id":  "plug-audit",
		"event_id":   "evt-audit",
		"event_kind": "user.created",
	}
	for k, want := range wantKeys {
		if payload[k] != want {
			t.Errorf("payload[%q] = %q, want %q", k, payload[k], want)
		}
	}
	for _, required := range []string{"drop_count", "window_secs"} {
		if _, ok := payload[required]; !ok {
			t.Errorf("payload missing key %q", required)
		}
	}
	if len(payload) != 5 {
		t.Errorf("payload has %d keys (%v), want exactly 5 (plugin_id, event_id, event_kind, drop_count, window_secs)", len(payload), payload)
	}
}

// TestEmitEvent_V2EventSourceRefusal_Coalesces verifies that repeated
// refusals within the auditFlushInterval window write exactly one audit row,
// mirroring EventTypeEventRateLimited's coalescing (#906 mirrors #577's
// precedent). Every call is still refused — coalescing only governs the
// audit write, never the gRPC response.
//
// Not t.Parallel(): this test swaps the package-level timeNow clock, which
// eventRateLimiter.Allow also reads on every EmitEvent call from any other
// test — running in parallel races (docs/developer/testing-patterns.md).
func TestEmitEvent_V2EventSourceRefusal_Coalesces(t *testing.T) {
	fakeNow := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	restore := hostsvc.SetTimeNowForTest(func() time.Time { return fakeNow })
	t.Cleanup(restore)

	q := &fakeQuerier{
		instance: db.PluginInstance{ID: "iid-coalesce", PluginID: "plug-coalesce"},
		plugin:   db.Plugin{ID: "plug-coalesce", ManifestSnapshot: v2EventSourceManifest},
	}
	pub := &fakePublisher{}
	binder := &fakeInstanceBinder{id: "iid-coalesce", ok: true}
	srv := hostsvc.NewServer(q, testEncryptionKey, &fakeResolver{}, binder, pub)

	// The very first refusal always flushes immediately (zero-value lastFlush,
	// same as eventRateLimiter) — this is the operator's immediate signal.
	// The next two, still inside the flush window, must NOT write new rows.
	for i := 0; i < 3; i++ {
		_, err := srv.EmitEvent(context.Background(), &hostv1.EmitEventRequest{
			EventId:   "evt-repeat",
			EventKind: "user.created",
		})
		st, ok := status.FromError(err)
		if !ok || st.Code() != codes.FailedPrecondition {
			t.Fatalf("call %d: expected FailedPrecondition, got %v", i, err)
		}
	}

	events := q.all()
	if len(events) != 1 {
		t.Fatalf("audit event count = %d, want 1 (only the first refusal flushes inside the window)", len(events))
	}
	var firstPayload map[string]string
	if err := json.Unmarshal([]byte(events[0].PayloadJson), &firstPayload); err != nil {
		t.Fatalf("parse audit payload: %v", err)
	}
	if firstPayload["drop_count"] != "1" {
		t.Errorf("first flush drop_count = %q, want %q", firstPayload["drop_count"], "1")
	}

	// Advance past the flush window: the next refusal flushes the two
	// refusals accumulated since the first flush, plus itself (3 total).
	fakeNow = fakeNow.Add(2 * time.Minute)
	_, err := srv.EmitEvent(context.Background(), &hostv1.EmitEventRequest{
		EventId:   "evt-after-window",
		EventKind: "user.created",
	})
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", err)
	}

	events = q.all()
	if len(events) != 2 {
		t.Fatalf("audit event count after window elapsed = %d, want 2", len(events))
	}
	var secondPayload map[string]string
	if err := json.Unmarshal([]byte(events[1].PayloadJson), &secondPayload); err != nil {
		t.Fatalf("parse audit payload: %v", err)
	}
	if secondPayload["drop_count"] != "3" {
		t.Errorf("second flush drop_count = %q, want %q (2 coalesced + this one)", secondPayload["drop_count"], "3")
	}
}
