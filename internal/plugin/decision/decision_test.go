package decision

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

func newStore(t *testing.T) *db.Store {
	t.Helper()
	store, err := db.Open(filepath.Join(t.TempDir(), "decision.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store
}

func fixture(t *testing.T, runID string) (*db.Store, *Recorder) {
	t.Helper()
	store := newStore(t)
	testutil.InsertPolicy(t, store, "p-"+runID, "policy-"+runID, "webhook", testutil.MinimalWebhookPolicy)
	testutil.InsertRun(t, store, runID, "p-"+runID, model.RunStatusRunning)
	// actor_user_id is a real FK: a record naming a Gleipnir user must name one
	// that exists, which is part of what makes it evidence rather than a string.
	for _, id := range []string{"user-1", "user-2", "user-7"} {
		insertUser(t, store, id)
	}
	return store, NewRecorder(store.Queries())
}

func answeredRecord(runID, requestID string) Record {
	return Record{
		RunID:             runID,
		RequestID:         requestID,
		Kind:              model.ElicitationKindPermission,
		ToolName:          "deploy.release",
		ChannelEntryID:    "gleipnir.in-app",
		ChannelAssurance:  string(mcp.ChannelAssuranceAuthenticated),
		ActorExternalID:   "user-7",
		ActorUserID:       "user-7",
		LinkMethod:        LinkSession,
		EffectiveDeadline: time.Date(2026, 8, 5, 12, 30, 0, 0, time.UTC),
		DeadlineSource:    "policy_timeout",
		Outcome:           OutcomeAnswered,
		DecidedAt:         time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC),
	}
}

// --- the ADR-046 split ------------------------------------------------------

// The structural guard behind the context-replay purity claim: these record
// types are not run-step types, so nothing can write them into the trace the
// model is replayed even by accident. If someone later adds them to StepType,
// this fails and makes them read the reasoning first.
func TestRecordTypesAreNotStepTypes(t *testing.T) {
	for _, recordType := range []RecordType{RecordToolPermissionRequest, RecordToolInputRequest} {
		if model.StepType(recordType).Valid() {
			t.Errorf("%q is a valid model.StepType; decision records must never be eligible for context replay (ADR-046)", recordType)
		}
	}
}

// A run whose decisions are recorded still has a clean trace: the records land
// in plugin_audit_events and nothing appears in run_steps.
func TestRecord_WritesOperationalRecordOnly(t *testing.T) {
	ctx := context.Background()
	store, recorder := fixture(t, "r-split")

	// The agent-visible trace of a paused-and-resumed call: the call and its
	// result, exactly as if the server had answered on the first round trip.
	insertStep(t, store, "r-split", 0, model.StepTypeToolCall, `{"tool_name":"deploy.release"}`)
	insertStep(t, store, "r-split", 1, model.StepTypeToolResult, `{"tool_name":"deploy.release","output":"deployed"}`)

	if err := recorder.Record(ctx, answeredRecord("r-split", "req-1")); err != nil {
		t.Fatalf("Record: %v", err)
	}

	steps, err := store.Queries().ListRunSteps(ctx, db.ListRunStepsParams{RunID: "r-split", After: -1, Limit: 100})
	if err != nil {
		t.Fatalf("ListRunSteps: %v", err)
	}
	if len(steps) != 2 {
		t.Fatalf("run has %d steps, want exactly the tool_call/tool_result pair", len(steps))
	}
	for _, step := range steps {
		if RecordType(step.Type).Valid() {
			t.Errorf("run_steps carries a decision record type %q", step.Type)
		}
	}

	records, err := recorder.ForRun(ctx, "r-split")
	if err != nil {
		t.Fatalf("ForRun: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d decision records, want 1", len(records))
	}
	if records[0].ActorUserID != "user-7" || records[0].Outcome != OutcomeAnswered {
		t.Errorf("decision record round-tripped as %+v", records[0])
	}
}

// --- the evidence a record carries ------------------------------------------

// Every field the spec names survives a round trip. The point of the assertion
// is that "an approval happened" and "this person, through this channel, at
// this assurance, before this deadline" must not collapse into the same row.
func TestRecord_RoundTripsTheWholeEvidence(t *testing.T) {
	ctx := context.Background()
	store, recorder := fixture(t, "r-evidence")

	want := Record{
		RunID:             "r-evidence",
		RequestID:         "req-ev",
		Kind:              model.ElicitationKindInformation,
		ToolName:          "infra.scale",
		ChannelEntryID:    "entry-2",
		ChannelInstance:   "inst-9",
		ChannelAssurance:  string(mcp.ChannelAssuranceWeak),
		ActorExternalID:   "ops@example.com",
		LinkMethod:        LinkUnverified,
		EffectiveDeadline: time.Date(2026, 8, 5, 13, 0, 0, 0, time.UTC),
		DeadlineSource:    "server_task_ttl",
		Outcome:           OutcomeAnswered,
		Considered: []Candidate{
			{EntryID: "entry-1", InstanceID: "inst-1", Reason: "assurance_too_weak"},
		},
		DecidedAt: time.Date(2026, 8, 5, 12, 15, 0, 0, time.UTC),
	}
	// An instance the record can reference; the column is a real FK.
	insertInstance(t, store, "plug-1", "inst-9", "ops-channel")

	if err := recorder.Record(ctx, want); err != nil {
		t.Fatalf("Record: %v", err)
	}

	got, err := recorder.ForRun(ctx, "r-evidence")
	if err != nil {
		t.Fatalf("ForRun: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d records, want 1", len(got))
	}
	if diff := compareRecords(want, got[0]); diff != "" {
		t.Error(diff)
	}

	// The classification decides the record type, so an auditor can filter on
	// "consent" versus "data entry" without parsing payloads.
	rows, err := store.Queries().ListPluginAuditEventsByRun(ctx, strPtr("r-evidence"))
	if err != nil {
		t.Fatalf("ListPluginAuditEventsByRun: %v", err)
	}
	if len(rows) != 1 || rows[0].EventType != string(RecordToolInputRequest) {
		t.Fatalf("event_type = %v, want %q", rows, RecordToolInputRequest)
	}
	// The instance is on the indexed column too, so "everything this channel
	// settled" is a query rather than a JSON scan.
	if rows[0].PluginInstanceID == nil || *rows[0].PluginInstanceID != "inst-9" {
		t.Errorf("plugin_instance_id = %v, want inst-9", rows[0].PluginInstanceID)
	}
}

// A permission granted by an actor nobody could tie to a Gleipnir user is the
// shape a forged approval takes. It is raised above info so an operator
// scanning the feed does not have to open every record to find it.
func TestRecord_Severity(t *testing.T) {
	tests := []struct {
		name string
		rec  Record
		want string
	}{
		{
			name: "verified permission is routine",
			rec:  Record{Kind: model.ElicitationKindPermission, LinkMethod: LinkSession, Outcome: OutcomeAnswered},
			want: "info",
		},
		{
			name: "unverified permission is worth a look",
			rec:  Record{Kind: model.ElicitationKindPermission, LinkMethod: LinkUnverified, Outcome: OutcomeAnswered},
			want: "warning",
		},
		{
			name: "unverified rejection too — a forged refusal also decides something",
			rec:  Record{Kind: model.ElicitationKindPermission, LinkMethod: LinkUnverified, Outcome: OutcomeRejected},
			want: "warning",
		},
		{
			name: "unverified information is only data entry",
			rec:  Record{Kind: model.ElicitationKindInformation, LinkMethod: LinkUnverified, Outcome: OutcomeAnswered},
			want: "info",
		},
		{
			name: "a run stalled on people",
			rec:  Record{Kind: model.ElicitationKindPermission, LinkMethod: LinkNone, Outcome: OutcomeTimeout},
			want: "warning",
		},
		{
			name: "cancelled",
			rec:  Record{Kind: model.ElicitationKindInformation, LinkMethod: LinkNone, Outcome: OutcomeCancelled},
			want: "warning",
		},
		{
			name: "a replay troubled nobody",
			rec:  Record{Kind: model.ElicitationKindPermission, LinkMethod: LinkNone, Outcome: OutcomeReplayedAfterTTL},
			want: "info",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.rec.Severity(); got != tc.want {
				t.Errorf("Severity() = %q, want %q", got, tc.want)
			}
		})
	}
}

// --- one record per outcome path --------------------------------------------

// Each terminal path produces exactly one record, and the outcome is what
// distinguishes them. A refusal and a silence both leave the request
// unfulfilled; only one of them involved a human, and the record must say which.
func TestRecord_EachOutcomePathProducesExactlyOne(t *testing.T) {
	ctx := context.Background()
	store, recorder := fixture(t, "r-outcomes")

	cases := []struct {
		outcome Outcome
		actor   string
		link    LinkMethod
	}{
		{OutcomeAnswered, "user-1", LinkSession},
		{OutcomeRejected, "user-2", LinkSession},
		{OutcomeTimeout, "", LinkNone},
		{OutcomeCancelled, "", LinkNone},
		{OutcomeReplayedAfterTTL, "", LinkNone},
	}

	for i, tc := range cases {
		rec := answeredRecord("r-outcomes", fmt.Sprintf("req-%d", i))
		rec.Outcome = tc.outcome
		rec.ActorExternalID = tc.actor
		rec.ActorUserID = tc.actor
		rec.LinkMethod = tc.link
		if err := recorder.Record(ctx, rec); err != nil {
			t.Fatalf("Record(%s): %v", tc.outcome, err)
		}
	}

	rows, err := store.Queries().ListPluginAuditEventsByRun(ctx, strPtr("r-outcomes"))
	if err != nil {
		t.Fatalf("ListPluginAuditEventsByRun: %v", err)
	}
	if len(rows) != len(cases) {
		t.Fatalf("got %d rows for %d outcomes, want one each", len(rows), len(cases))
	}

	seen := map[Outcome]int{}
	for _, row := range rows {
		rec, ok := Decode(row)
		if !ok {
			t.Fatalf("row %d did not decode as a decision record", row.ID)
		}
		seen[rec.Outcome]++
	}
	for _, tc := range cases {
		if seen[tc.outcome] != 1 {
			t.Errorf("outcome %q produced %d records, want exactly 1", tc.outcome, seen[tc.outcome])
		}
	}
}

// A request settled twice inside one process writes one record, not two — a
// second row would double-count an approval in any export that counts them.
func TestRecord_SecondSettlementIsRefused(t *testing.T) {
	ctx := context.Background()
	store, recorder := fixture(t, "r-once")

	if err := recorder.Record(ctx, answeredRecord("r-once", "req-1")); err != nil {
		t.Fatalf("Record: %v", err)
	}
	second := answeredRecord("r-once", "req-1")
	second.Outcome = OutcomeRejected
	if err := recorder.Record(ctx, second); !errors.Is(err, ErrAlreadyRecorded) {
		t.Fatalf("second Record error = %v, want ErrAlreadyRecorded", err)
	}

	rows, err := store.Queries().ListPluginAuditEventsByRun(ctx, strPtr("r-once"))
	if err != nil {
		t.Fatalf("ListPluginAuditEventsByRun: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
}

// A failed write releases the claim, so a retry can still record the decision.
// Refusing the retry would lose the record entirely — the opposite of what the
// duplicate guard is for.
func TestRecord_FailedWriteLeavesTheRequestRecordable(t *testing.T) {
	ctx := context.Background()
	failing := &flakyStore{failNext: true}
	recorder := NewRecorder(failing)

	if err := recorder.Record(ctx, answeredRecord("r-flaky", "req-1")); err == nil {
		t.Fatal("Record succeeded against a failing store")
	}
	if err := recorder.Record(ctx, answeredRecord("r-flaky", "req-1")); err != nil {
		t.Fatalf("retry after a failed write: %v", err)
	}
	if failing.writes != 1 {
		t.Errorf("store saw %d successful writes, want 1", failing.writes)
	}
}

// Concurrent settlements: distinct requests all land exactly once, and
// contenders for the SAME request produce one winner. The audit table is
// written from several goroutines in a live host — the poller, the API
// handler, the timeout scanner — so this is the ordinary case, not an edge one.
func TestRecord_ConcurrentWrites(t *testing.T) {
	ctx := context.Background()
	store, recorder := fixture(t, "r-concurrent")

	const (
		requests   = 12
		contenders = 4
	)
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		conflicts int
		winners   int
	)
	for i := 0; i < requests; i++ {
		for c := 0; c < contenders; c++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				err := recorder.Record(ctx, answeredRecord("r-concurrent", fmt.Sprintf("req-%d", i)))
				mu.Lock()
				defer mu.Unlock()
				switch {
				case err == nil:
					winners++
				case errors.Is(err, ErrAlreadyRecorded):
					conflicts++
				default:
					t.Errorf("unexpected Record error: %v", err)
				}
			}(i)
		}
	}
	wg.Wait()

	if winners != requests {
		t.Errorf("got %d winners, want one per request (%d)", winners, requests)
	}
	if conflicts != requests*(contenders-1) {
		t.Errorf("got %d conflicts, want %d", conflicts, requests*(contenders-1))
	}

	rows, err := store.Queries().ListPluginAuditEventsByRun(ctx, strPtr("r-concurrent"))
	if err != nil {
		t.Fatalf("ListPluginAuditEventsByRun: %v", err)
	}
	if len(rows) != requests {
		t.Fatalf("got %d rows, want %d — one decision record per request", len(rows), requests)
	}
	// Every row is intact: a torn payload would decode as something other than
	// the record that was written.
	for _, row := range rows {
		rec, ok := Decode(row)
		if !ok {
			t.Fatalf("row %d did not decode", row.ID)
		}
		if rec.Outcome != OutcomeAnswered || rec.ActorUserID != "user-7" {
			t.Errorf("row %d decoded as %+v", row.ID, rec)
		}
	}
}

// --- validation -------------------------------------------------------------

// The rejected records are the self-contradictory ones: a record that reads
// back as evidence of something that did not happen is worse than no record.
func TestRecord_Validate(t *testing.T) {
	valid := answeredRecord("r", "req")

	tests := []struct {
		name   string
		mutate func(*Record)
		wantOK bool
	}{
		{"valid", func(*Record) {}, true},
		{"no run", func(r *Record) { r.RunID = "" }, false},
		{"no request", func(r *Record) { r.RequestID = "" }, false},
		{"unknown kind", func(r *Record) { r.Kind = "urgent" }, false},
		{"unknown outcome", func(r *Record) { r.Outcome = "shrugged" }, false},
		{"unknown link method", func(r *Record) { r.LinkMethod = "vibes" }, false},
		{
			name: "a timeout that names an actor",
			mutate: func(r *Record) {
				r.Outcome = OutcomeTimeout
			},
			wantOK: false,
		},
		{
			name: "a verified link with nobody linked",
			mutate: func(r *Record) {
				r.ActorUserID = ""
			},
			wantOK: false,
		},
		{
			name: "an unverified link that names a user anyway",
			mutate: func(r *Record) {
				r.LinkMethod = LinkUnverified
			},
			wantOK: false,
		},
		{
			name: "an unverified actor with no linked user is fine",
			mutate: func(r *Record) {
				r.LinkMethod = LinkUnverified
				r.ActorUserID = ""
			},
			wantOK: true,
		},
		{
			name: "a timeout with nobody named is fine",
			mutate: func(r *Record) {
				r.Outcome = OutcomeTimeout
				r.ActorExternalID = ""
				r.ActorUserID = ""
				r.LinkMethod = LinkNone
			},
			wantOK: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := valid
			tc.mutate(&rec)
			err := rec.Validate()
			if tc.wantOK && err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
			if !tc.wantOK && err == nil {
				t.Error("Validate() accepted a self-contradictory record")
			}
		})
	}
}

func TestRecordTypeFor(t *testing.T) {
	if got := RecordTypeFor(model.ElicitationKindPermission); got != RecordToolPermissionRequest {
		t.Errorf("permission → %q", got)
	}
	if got := RecordTypeFor(model.ElicitationKindInformation); got != RecordToolInputRequest {
		t.Errorf("information → %q", got)
	}
}

// "The server declared something this host does not recognize" and "no
// assurance was recorded" are different facts. An empty field cannot say which.
func TestAssuranceOf(t *testing.T) {
	tests := []struct {
		in   mcp.ChannelAssurance
		want string
	}{
		{mcp.ChannelAssuranceAuthenticated, "authenticated"},
		{mcp.ChannelAssuranceWeak, "weak"},
		{"", "undeclared"},
		{"biometric", "unrecognized"},
	}
	for _, tc := range tests {
		if got := AssuranceOf(tc.in); got != tc.want {
			t.Errorf("AssuranceOf(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// An unreadable record must never come back as an empty one: an empty record
// reads as "nobody acted", which is a specific and wrong claim.
func TestDecode_RejectsUnreadableRows(t *testing.T) {
	tests := []struct {
		name string
		row  db.PluginAuditEvent
	}{
		{"not a decision record", db.PluginAuditEvent{EventType: "plugin_installed", PayloadJson: "{}"}},
		{"payload does not parse", db.PluginAuditEvent{EventType: string(RecordToolInputRequest), PayloadJson: "{"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := Decode(tc.row); ok {
				t.Error("Decode accepted an unreadable row")
			}
		})
	}
}

// ForRun skips whatever else grows a run association later, rather than
// surfacing it as a malformed decision.
func TestForRun_SkipsNonDecisionRows(t *testing.T) {
	ctx := context.Background()
	store, recorder := fixture(t, "r-mixed")

	runID := "r-mixed"
	if _, err := store.Queries().InsertPluginAuditEvent(ctx, db.InsertPluginAuditEventParams{
		EventType:   "plugin_instance_activated",
		Severity:    "info",
		PayloadJson: `{"instance":"inst-1"}`,
		CreatedAt:   "2026-08-05T11:00:00Z",
		RunID:       &runID,
	}); err != nil {
		t.Fatalf("InsertPluginAuditEvent: %v", err)
	}
	if err := recorder.Record(ctx, answeredRecord(runID, "req-1")); err != nil {
		t.Fatalf("Record: %v", err)
	}

	records, err := recorder.ForRun(ctx, runID)
	if err != nil {
		t.Fatalf("ForRun: %v", err)
	}
	if len(records) != 1 || records[0].RequestID != "req-1" {
		t.Fatalf("ForRun returned %+v, want only the decision record", records)
	}
}

// The clock is injectable, and an unset DecidedAt is stamped rather than left
// zero — a decision record with no time on it is not evidence of anything.
func TestRecord_StampsDecidedAt(t *testing.T) {
	ctx := context.Background()
	frozen := time.Date(2026, 8, 5, 9, 30, 0, 0, time.UTC)
	timeNow = func() time.Time { return frozen }
	t.Cleanup(func() { timeNow = func() time.Time { return time.Now() } })

	_, recorder := fixture(t, "r-clock")
	rec := answeredRecord("r-clock", "req-1")
	rec.DecidedAt = time.Time{}
	if err := recorder.Record(ctx, rec); err != nil {
		t.Fatalf("Record: %v", err)
	}

	records, err := recorder.ForRun(ctx, "r-clock")
	if err != nil {
		t.Fatalf("ForRun: %v", err)
	}
	if !records[0].DecidedAt.Equal(frozen) {
		t.Errorf("DecidedAt = %s, want %s", records[0].DecidedAt, frozen)
	}
}

// --- helpers ----------------------------------------------------------------

func strPtr(s string) *string { return &s }

func insertUser(t *testing.T, store *db.Store, id string) {
	t.Helper()
	if _, err := store.Queries().CreateUser(context.Background(), db.CreateUserParams{
		ID:           id,
		Username:     id,
		PasswordHash: "x",
		CreatedAt:    time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("CreateUser %s: %v", id, err)
	}
}

func insertStep(t *testing.T, store *db.Store, runID string, number int64, stepType model.StepType, content string) {
	t.Helper()
	if _, err := store.Queries().CreateRunStep(context.Background(), db.CreateRunStepParams{
		ID:         fmt.Sprintf("%s-step-%d", runID, number),
		RunID:      runID,
		StepNumber: number,
		Type:       string(stepType),
		Content:    content,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("CreateRunStep: %v", err)
	}
}

func insertInstance(t *testing.T, store *db.Store, pluginID, instanceID, name string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.Queries().CreatePlugin(context.Background(), db.CreatePluginParams{
		ID:               pluginID,
		Name:             name,
		PluginVersion:    "1.0.0",
		ManifestSnapshot: "{}",
		TrustedPubkey:    "pk",
		Status:           "active",
		CreatedAt:        now,
		UpdatedAt:        now,
	}); err != nil {
		t.Fatalf("CreatePlugin: %v", err)
	}
	if _, err := store.Queries().CreatePluginInstance(context.Background(), db.CreatePluginInstanceParams{
		ID:                    instanceID,
		PluginID:              pluginID,
		InstanceName:          name,
		ConfigJson:            "{}",
		SubscriptionScopeJson: "{}",
		HandshakeVersions:     "{}",
		HealthState:           "healthy",
		HealthDetail:          strPtr(""),
		CreatedAt:             now,
		UpdatedAt:             now,
	}); err != nil {
		t.Fatalf("CreatePluginInstance: %v", err)
	}
}

// flakyStore fails its first insert, then succeeds.
type flakyStore struct {
	failNext bool
	writes   int
}

func (f *flakyStore) InsertPluginAuditEvent(_ context.Context, _ db.InsertPluginAuditEventParams) (db.PluginAuditEvent, error) {
	if f.failNext {
		f.failNext = false
		return db.PluginAuditEvent{}, errors.New("disk on fire")
	}
	f.writes++
	return db.PluginAuditEvent{}, nil
}

func (f *flakyStore) ListPluginAuditEventsByRun(context.Context, *string) ([]db.PluginAuditEvent, error) {
	return nil, nil
}

// compareRecords reports the first field that differs, as a readable message.
// A hand-rolled comparison rather than reflect.DeepEqual so the failure names
// the field instead of printing two large structs.
func compareRecords(want, got Record) string {
	wantJSON, _ := json.Marshal(want)
	gotJSON, _ := json.Marshal(got)
	if string(wantJSON) != string(gotJSON) {
		return fmt.Sprintf("record round-tripped as\n  %s\nwant\n  %s", gotJSON, wantJSON)
	}
	if want.RunID != got.RunID {
		return fmt.Sprintf("run ID = %q, want %q", got.RunID, want.RunID)
	}
	return ""
}
