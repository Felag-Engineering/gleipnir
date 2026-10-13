package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

// TestFeedbackHandler_Wait_PluginResponseStep pins who writes the
// feedback_response run step on the plugin path: nobody here for a dispatcher
// that leaves WriteResponseStep unset (v1: hostsvc wrote it), exactly one step
// carrying the parsed answer when the dispatcher asks for it, and none when the
// answer lost the race with the timeout scanner (it never took effect).
func TestFeedbackHandler_Wait_PluginResponseStep(t *testing.T) {
	tests := []struct {
		name              string
		writeResponseStep bool
		scannerWinsFirst  bool
		wantSteps         int
		wantErr           bool
	}{
		{name: "dispatcher keeps the step to itself", writeResponseStep: false, wantSteps: 0},
		{name: "dispatcher asks the handler to write it", writeResponseStep: true, wantSteps: 1},
		{name: "answer that lost the CAS writes nothing", writeResponseStep: true, scannerWinsFirst: true, wantSteps: 0, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := testutil.NewTestStore(t)
			testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
			testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

			sm := NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries())
			w := NewAuditWriter(s.Queries())
			defer w.Close() //nolint:errcheck

			dispatcher := funcFeedbackDispatcher(func(ctx context.Context, _ FeedbackDispatchRequest) (FeedbackSettlement, error) {
				if tt.scannerWinsFirst {
					pending, err := s.GetPendingFeedbackRequestsByRun(ctx, "run1")
					if err != nil || len(pending) != 1 {
						t.Fatalf("pending feedback rows = %d, err %v; want exactly one", len(pending), err)
					}
					now := time.Now().UTC().Format(time.RFC3339Nano)
					if _, err := s.UpdateFeedbackRequestStatus(ctx, db.UpdateFeedbackRequestStatusParams{
						Status: "timed_out", ResolvedAt: &now, ID: pending[0].ID,
					}); err != nil {
						t.Fatalf("simulating the timeout scanner: %v", err)
					}
				}
				return FeedbackSettlement{
					Response:          `{"text":"operator reply"}`,
					WriteResponseStep: tt.writeResponseStep,
				}, nil
			})
			h := NewFeedbackHandler(w, sm, time.Minute, WithFeedbackChannelDispatch(dispatcher, "aud-1", "pol-1"))

			text, err := h.Wait(context.Background(), "run1", AskOperatorToolName, "{}", "please answer", time.Minute)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Wait error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && text != "operator reply" {
				t.Errorf("response = %q, want %q", text, "operator reply")
			}

			if err := w.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			steps, err := s.ListRunSteps(context.Background(), db.ListRunStepsParams{RunID: "run1", After: -1, Limit: listAll})
			if err != nil {
				t.Fatalf("ListRunSteps: %v", err)
			}
			var responseSteps []db.RunStep
			for _, step := range steps {
				if step.Type == string(model.StepTypeFeedbackResponse) {
					responseSteps = append(responseSteps, step)
				}
			}
			if len(responseSteps) != tt.wantSteps {
				t.Fatalf("feedback_response steps = %d, want %d", len(responseSteps), tt.wantSteps)
			}
			if tt.wantSteps == 1 && !strings.Contains(responseSteps[0].Content, `"response":"operator reply"`) {
				t.Errorf("feedback_response content = %s, want the parsed answer", responseSteps[0].Content)
			}
		})
	}
}
