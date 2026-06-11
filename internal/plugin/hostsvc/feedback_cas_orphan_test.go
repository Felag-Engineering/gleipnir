package hostsvc_test

// Residual-race test for #496 (a follow-up to #348).
//
// #348 fixed step_number ordering by wrapping the feedback_response INSERT and
// the guarded UpdateFeedbackRequestStatus CAS in one transaction. A residual
// window remained: WriteAuditStep pre-checks fr.Status == "pending" BEFORE the
// transaction, but if the request is concurrently resolved between that
// pre-check and the commit, the CAS (WHERE status='pending') updates 0 rows
// while the feedback_response step still commits — an orphan audit step with no
// corresponding state change.
//
// The fix captures rows-affected from the CAS; when 0, it rolls back the tx (no
// step committed) and returns the late-callback ok=false envelope. This test
// forces that 0-row CAS by making the pre-check observe "pending" while the real
// DB row is already "resolved", then asserts no feedback_response step landed
// and the caller got ok=false.

import (
	"context"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/hostsvc"
	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
)

// forcedPendingQuerier wraps the real-DB-backed fakeInstanceQuerier but lies in
// GetFeedbackRequest: it always reports the request as "pending" so WriteAuditStep
// clears its fast-path pre-check, even though the underlying DB row has already
// been transitioned to "resolved". This deterministically reproduces the
// concurrent-resolution window that the real guarded CAS must catch.
type forcedPendingQuerier struct {
	*fakeInstanceQuerier
}

func (f *forcedPendingQuerier) GetFeedbackRequest(ctx context.Context, id string) (db.FeedbackRequest, error) {
	fr, err := f.fakeInstanceQuerier.GetFeedbackRequest(ctx, id)
	if err != nil {
		return fr, err
	}
	// Mask the real (possibly already-resolved) status so the pre-check passes
	// and execution proceeds into the transactional CAS.
	fr.Status = "pending"
	return fr, nil
}

// TestWriteAuditStep_CASZeroRowsDoesNotOrphanStep proves that when the guarded
// UpdateFeedbackRequestStatus CAS affects 0 rows (the request was resolved
// concurrently after the fr.Status pre-check), WriteAuditStep:
//   - does NOT commit a feedback_response run_step, and
//   - returns ok=false with the feedback_response_late envelope.
func TestWriteAuditStep_CASZeroRowsDoesNotOrphanStep(t *testing.T) {
	store, q := openConcurrencyStore(t)
	instanceID := "cas-inst-" + model.NewULID()
	instanceName := "cas-plugin"
	runID, feedbackIDs := seedConcurrencyRun(t, q, 1, instanceID, instanceName)
	requestID := feedbackIDs[0]

	// Simulate the concurrent resolver winning: transition the request to
	// "resolved" in the real DB BEFORE WriteAuditStep runs its CAS. The forced
	// pre-check below will still report "pending", so we land in the residual
	// window the fix closes.
	resolvedResp := `{"by":"other-writer"}`
	resolvedAt := "2026-06-10T00:00:00Z"
	rows, err := q.UpdateFeedbackRequestStatus(context.Background(), db.UpdateFeedbackRequestStatusParams{
		Status:     "resolved",
		Response:   &resolvedResp,
		ResolvedAt: &resolvedAt,
		ID:         requestID,
	})
	if err != nil {
		t.Fatalf("pre-resolve UpdateFeedbackRequestStatus: %v", err)
	}
	if rows != 1 {
		t.Fatalf("pre-resolve affected %d rows, want 1", rows)
	}

	binder := &fakeInstanceBinder{id: instanceID, ok: true}
	pub := &fakePublisher{}
	base := &fakeInstanceQuerier{
		store:        store,
		q:            q,
		instanceID:   instanceID,
		instanceName: instanceName,
	}
	fq := &forcedPendingQuerier{fakeInstanceQuerier: base}
	srv := hostsvc.NewServer(fq, store.DB(), testEncryptionKey, &fakeResolver{}, binder, pub, nil)

	resp, err := srv.WriteAuditStep(context.Background(), &hostv1.WriteAuditStepRequest{
		StepType:    "feedback_response",
		RequestId:   requestID,
		PayloadJson: `{"late":true}`,
	})
	if err != nil {
		t.Fatalf("WriteAuditStep returned RPC error: %v", err)
	}

	// The caller must observe a late-callback, not a success.
	if resp.GetOk() {
		t.Fatalf("expected ok=false on 0-row CAS, got ok=true")
	}
	if got := resp.GetError().GetMessage(); got != "feedback_response_late" {
		t.Errorf("error message = %q, want %q", got, "feedback_response_late")
	}

	// No feedback_response step may have been committed — the tx must have rolled
	// back. The run was seeded with no steps, so any step here is the orphan bug.
	steps, err := q.ListRunSteps(context.Background(), db.ListRunStepsParams{
		RunID: runID,
		After: -1,
		Limit: 50,
	})
	if err != nil {
		t.Fatalf("ListRunSteps: %v", err)
	}
	for _, s := range steps {
		if s.Type == "feedback_response" {
			t.Fatalf("orphan feedback_response step committed (step_number=%d) despite 0-row CAS", s.StepNumber)
		}
	}

	// And the original resolver's response must be intact — our late write must
	// not have clobbered it.
	fr, err := q.GetFeedbackRequest(context.Background(), requestID)
	if err != nil {
		t.Fatalf("GetFeedbackRequest: %v", err)
	}
	if fr.Status != "resolved" {
		t.Errorf("status = %q, want resolved", fr.Status)
	}
	if fr.Response == nil || *fr.Response != resolvedResp {
		t.Errorf("response = %v, want %q (original resolver's value preserved)", fr.Response, resolvedResp)
	}
}
