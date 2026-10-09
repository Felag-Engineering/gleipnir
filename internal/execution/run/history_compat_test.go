package run_test

// MUST-STAY-GREEN COMPAT TEST for the v1 -> v2 plugin cutover (#963).
//
// Run history recorded by the v1 gRPC plugin runtime (fixtures.SeedGrpcEraRuns)
// must keep reading back through the live handlers after the v1 plugin code is
// deleted. The #1004 purge must re-run this file; a failure here means the
// purge broke existing operators' audit trail, not that the test is stale.
// Do not edit the fixture to make it pass.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/execution/run"
	"github.com/felag-engineering/gleipnir/internal/testutil"
	"github.com/felag-engineering/gleipnir/internal/testutil/fixtures"
)

func getJSON(t *testing.T, router http.Handler, path string, out any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200; body: %s", path, w.Code, w.Body.String())
	}
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&env); err != nil {
		t.Fatalf("GET %s: decode envelope: %v", path, err)
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		t.Fatalf("GET %s: decode data: %v", path, err)
	}
}

func TestGrpcEraRunHistory_StillServes(t *testing.T) {
	store := testutil.NewTestStore(t)
	seeded := fixtures.SeedGrpcEraRuns(t, store)

	// Nothing here launches a run, so there is no RunManager to drain.
	h := run.NewRunsHandler(store, run.NewRunManager(), nil)
	router := chi.NewRouter()
	router.Get("/api/v1/runs/{runID}", h.Get)
	router.Get("/api/v1/runs/{runID}/steps", h.ListSteps)
	router.Get("/api/v1/runs/{runID}/decisions", h.ListDecisions)
	router.Get("/api/v1/runs/{runID}/responders", h.ListResponders)
	base := "/api/v1/runs/" + seeded.RunID

	t.Run("run detail", func(t *testing.T) {
		var got run.RunSummary
		getJSON(t, router, base, &got)
		if got.ID != seeded.RunID || got.PolicyID != seeded.PolicyID || got.Status != "complete" {
			t.Errorf("run = {%s %s %s}, want {%s %s complete}", got.ID, got.PolicyID, got.Status, seeded.RunID, seeded.PolicyID)
		}
	})

	t.Run("steps", func(t *testing.T) {
		var got []run.StepSummary
		getJSON(t, router, base+"/steps", &got)

		gotTypes := make([]string, len(got))
		for i, s := range got {
			gotTypes[i] = s.Type
			if !json.Valid([]byte(s.Content)) {
				t.Errorf("step %d (%s) content is not valid JSON: %q", s.StepNumber, s.Type, s.Content)
			}
		}
		if !slices.Equal(gotTypes, seeded.StepTypes) {
			t.Fatalf("step types = %v, want %v", gotTypes, seeded.StepTypes)
		}

		var snapshot struct {
			Tools []struct {
				ServerName string `json:"server_name"`
				ToolName   string `json:"tool_name"`
				Source     string `json:"source"`
			} `json:"tools"`
		}
		if err := json.Unmarshal([]byte(got[0].Content), &snapshot); err != nil {
			t.Fatalf("decode capability_snapshot: %v", err)
		}
		if len(snapshot.Tools) == 0 || snapshot.Tools[0].Source != "plugin:slack-ops@7" {
			t.Errorf("snapshot tools = %+v, want first tool sourced from plugin:slack-ops@7", snapshot.Tools)
		}

		// Both dispatcher failures are "error" steps distinguished by content.kind.
		var kinds []string
		for _, s := range got {
			if s.Type != "error" {
				continue
			}
			var c struct {
				Kind string `json:"kind"`
				Code string `json:"code"`
			}
			if err := json.Unmarshal([]byte(s.Content), &c); err != nil {
				t.Fatalf("decode error step: %v", err)
			}
			if c.Kind != c.Code {
				t.Errorf("error step kind %q != code %q", c.Kind, c.Code)
			}
			kinds = append(kinds, c.Kind)
		}
		if want := []string{"feedback_dispatch_error", "plugin_request_timeout"}; !slices.Equal(kinds, want) {
			t.Errorf("error kinds = %v, want %v", kinds, want)
		}
	})

	t.Run("decisions skips v1 audit rows", func(t *testing.T) {
		// The run has one run-scoped v1 audit row; it is not a decision record
		// and must be skipped, not rendered and not an error.
		var got []run.DecisionSummary
		getJSON(t, router, base+"/decisions", &got)
		if len(got) != 0 {
			t.Errorf("decisions = %+v, want none", got)
		}
	})

	t.Run("responders", func(t *testing.T) {
		var got []run.ResponderSummary
		getJSON(t, router, base+"/responders", &got)
		byID := map[string]run.ResponderSummary{}
		for _, r := range got {
			byID[r.RequestID] = r
		}
		if a := byID[seeded.ApprovalID]; a.Kind != "approval" || a.Status != "approved" || a.DecidedBy != nil {
			t.Errorf("approval responder = %+v, want approved with no decider", a)
		}
		if f := byID[seeded.FeedbackID]; f.Kind != "feedback" || f.Status != "resolved" || f.DecidedBy != nil {
			t.Errorf("feedback responder = %+v, want resolved with no responder", f)
		}
	})
}

// The admin plugin audit feed has no HTTP route today; its read path is the
// ListRecentPluginAuditEvents query, which is what this checks. A row whose
// instance was deleted (NULL plugin_instance_id) must still read back.
func TestGrpcEraAuditEvents_StillReadable(t *testing.T) {
	store := testutil.NewTestStore(t)
	seeded := fixtures.SeedGrpcEraRuns(t, store)

	rows, err := store.ListRecentPluginAuditEvents(context.Background(), db.ListRecentPluginAuditEventsParams{
		Severity: "",
		Limit:    100,
		Offset:   0,
	})
	if err != nil {
		t.Fatalf("ListRecentPluginAuditEvents: %v", err)
	}
	if len(rows) != seeded.AuditEventCount {
		t.Fatalf("got %d audit events, want %d", len(rows), seeded.AuditEventCount)
	}

	var sawOrphan bool
	runScoped := 0
	for _, row := range rows {
		if !json.Valid([]byte(row.PayloadJson)) {
			t.Errorf("%s payload is not valid JSON: %q", row.EventType, row.PayloadJson)
		}
		if row.RunID != nil {
			runScoped++
		}
		if row.PluginInstanceID == nil {
			sawOrphan = true
			if row.EventType != seeded.OrphanAuditEventType {
				t.Errorf("NULL-instance row type = %q, want %q", row.EventType, seeded.OrphanAuditEventType)
			}
		}
	}
	if !sawOrphan {
		t.Error("no audit row with NULL plugin_instance_id came back")
	}
	if runScoped != seeded.RunScopedAuditEventCount {
		t.Errorf("run-scoped audit rows = %d, want %d", runScoped, seeded.RunScopedAuditEventCount)
	}
}
