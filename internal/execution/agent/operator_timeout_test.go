// Package agent — operator_timeout_test.go covers claimRequestTimeout, the
// shared two-writer timeout race extracted in #505. Both branches are tested
// once here through the single helper interface: the rows==1 win path (this
// caller owns the error step) and the rows==0 scanner-wins path (no error step,
// sentinel error). The claim seam is a fake closure so the test asserts the
// helper's branching independent of any approval/feedback query.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

func TestClaimRequestTimeout(t *testing.T) {
	tests := []struct {
		name          string
		claimRows     int64
		claimErr      error
		wantErrMsg    string
		wantErrorStep bool
		wantStepCode  model.ErrorCode
		wantStepMsg   string
	}{
		{
			name:          "handler wins the race",
			claimRows:     1,
			wantErrMsg:    "feedback timeout: won",
			wantErrorStep: true,
			wantStepCode:  model.ErrorCodeFeedbackTimeout,
			wantStepMsg:   "feedback timeout: won",
		},
		{
			name:          "scanner already claimed the row",
			claimRows:     0,
			wantErrMsg:    "feedback timeout: lost",
			wantErrorStep: false,
		},
		{
			name:          "claim write error falls through to sentinel",
			claimRows:     0,
			claimErr:      errors.New("db stalled"),
			wantErrMsg:    "feedback timeout: lost",
			wantErrorStep: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			const runID = "run1"
			s := testutil.NewTestStore(t)
			testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
			testutil.InsertRun(t, s, runID, "p1", model.RunStatusRunning)

			w := NewAuditWriter(s.Queries())

			err := claimRequestTimeout(context.Background(), w, timeoutClaim{
				name:      "feedback",
				runID:     runID,
				requestID: "fb-1",
				claim: func(_ context.Context, _ string) (int64, error) {
					return tc.claimRows, tc.claimErr
				},
				errorCode:   model.ErrorCodeFeedbackTimeout,
				wonMessage:  "feedback timeout: won",
				lostMessage: "feedback timeout: lost",
			})

			if err == nil || err.Error() != tc.wantErrMsg {
				t.Fatalf("claimRequestTimeout error = %v, want %q", err, tc.wantErrMsg)
			}

			// Flush the audit writer so any enqueued error step has landed.
			if cerr := w.Close(); cerr != nil {
				t.Fatalf("AuditWriter.Close: %v", cerr)
			}

			steps, lerr := s.ListRunSteps(context.Background(), db.ListRunStepsParams{RunID: runID, After: -1, Limit: listAll})
			if lerr != nil {
				t.Fatalf("ListRunSteps: %v", lerr)
			}

			var errorSteps []db.RunStep
			for _, step := range steps {
				if step.Type == string(model.StepTypeError) {
					errorSteps = append(errorSteps, step)
				}
			}

			if !tc.wantErrorStep {
				if len(errorSteps) != 0 {
					t.Fatalf("expected no error step (scanner owns it), got %d", len(errorSteps))
				}
				return
			}

			if len(errorSteps) != 1 {
				t.Fatalf("expected exactly 1 error step, got %d", len(errorSteps))
			}
			var content model.ErrorStepContent
			if uerr := json.Unmarshal([]byte(errorSteps[0].Content), &content); uerr != nil {
				t.Fatalf("unmarshal error step content: %v", uerr)
			}
			if content.Code != tc.wantStepCode {
				t.Errorf("error step code = %q, want %q", content.Code, tc.wantStepCode)
			}
			if content.Message != tc.wantStepMsg {
				t.Errorf("error step message = %q, want %q", content.Message, tc.wantStepMsg)
			}
		})
	}
}
