package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/execution/agent"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

// TestNewDispatchStepWriter asserts the production WriteRunStep adapter (#573)
// persists a dispatcher error step into the run reasoning trace. The dispatcher
// emits non-canonical step strings ("feedback_dispatch_error",
// "plugin_request_timeout") that the run_steps.type CHECK constraint rejects, so
// the adapter records them as the canonical "error" step type while preserving
// the original kind in payload["kind"] (ADR-046 audit completeness).
func TestNewDispatchStepWriter(t *testing.T) {
	cases := []struct {
		name     string
		stepType string
		payload  map[string]interface{}
	}{
		{
			name:     "feedback dispatch error",
			stepType: "feedback_dispatch_error",
			payload:  map[string]interface{}{"reason": "no route", "instance": "slack-prod"},
		},
		{
			name:     "plugin request timeout",
			stepType: "plugin_request_timeout",
			payload:  map[string]interface{}{"request_id": "req-123"},
		},
		{
			name:     "standard error type control",
			stepType: "error",
			payload:  map[string]interface{}{"message": "boom"},
		},
		{
			name:     "nil payload still records kind",
			stepType: "feedback_dispatch_error",
			payload:  nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := testutil.NewTestStore(t)
			const policyID, runID = "pol-1", "run-1"
			testutil.InsertPolicy(t, store, policyID, "p", "webhook", testutil.MinimalWebhookPolicy)
			testutil.InsertRun(t, store, runID, policyID, model.RunStatusRunning)

			// Build the EXACT production adapter, not a copy, so the test guards
			// against drift in the conversion shape.
			aw := agent.NewAuditWriter(store.Queries())
			write := newDispatchStepWriter(aw)

			if err := write(context.Background(), runID, tc.stepType, tc.payload); err != nil {
				t.Fatalf("write step: %v", err)
			}
			// Close flushes the serialised queue and joins the writer goroutine.
			if err := aw.Close(); err != nil {
				t.Fatalf("audit writer close: %v", err)
			}

			steps, err := store.ListRunSteps(context.Background(), db.ListRunStepsParams{
				RunID: runID,
				After: -1,
				Limit: 10,
			})
			if err != nil {
				t.Fatalf("list run steps: %v", err)
			}
			if len(steps) != 1 {
				t.Fatalf("expected exactly 1 run step, got %d", len(steps))
			}

			got := steps[0]
			// Every dispatcher error is recorded as the canonical "error" step
			// type (the run_steps CHECK constraint rejects the raw dispatcher
			// strings); the original kind survives in payload["kind"].
			if got.Type != model.StepTypeError.String() {
				t.Errorf("step type = %q, want %q", got.Type, model.StepTypeError.String())
			}

			// Content must round-trip the payload JSON the dispatcher handed us,
			// plus the injected "kind" carrying the original dispatcher step type.
			var content map[string]interface{}
			if err := json.Unmarshal([]byte(got.Content), &content); err != nil {
				t.Fatalf("unmarshal step content %q: %v", got.Content, err)
			}
			if content["kind"] != tc.stepType {
				t.Errorf("content[kind] = %v, want %q", content["kind"], tc.stepType)
			}
			for k, want := range tc.payload {
				if content[k] != want {
					t.Errorf("content[%q] = %v, want %v", k, content[k], want)
				}
			}
		})
	}
}
