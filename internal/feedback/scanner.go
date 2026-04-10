// Package feedback provides the background timeout scanner for pending feedback
// requests. When a run is waiting_for_feedback and the feedback window expires,
// the scanner marks the feedback request as timed_out and the run as failed.
//
// Only feedback requests with a non-NULL expires_at are considered for timeout.
// Requests without expires_at were created without a timeout configured and
// block indefinitely until the operator responds or the run is cancelled.
package feedback

import (
	"context"
	"fmt"
	"time"

	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/timeout"
)

// ScannerOption is a functional option for the feedback scanner.
type ScannerOption = timeout.ScannerOption

// WithPublisher sets an optional SSE publisher so the scanner can emit
// run.status_changed and feedback.timed_out events to connected clients.
var WithPublisher = timeout.WithPublisher

// NewScanner creates a Scanner that checks for expired feedback requests on the
// given interval. Pass functional options to configure optional behaviour.
// The returned *timeout.Scanner exposes Start and Scan methods.
func NewScanner(store *db.Store, interval time.Duration, opts ...ScannerOption) *timeout.Scanner {
	cfg := timeout.Config{
		Name: "feedback",
		ListExpired: func(ctx context.Context, cutoff string) ([]timeout.ExpiredItem, error) {
			// The query accepts *string so it can bind NULL, but here we always
			// pass a non-nil cutoff; the NULL-expires_at exclusion is in the SQL.
			rows, err := store.Queries().ListExpiredFeedbackRequests(ctx, &cutoff)
			if err != nil {
				return nil, err
			}
			items := make([]timeout.ExpiredItem, len(rows))
			for i, r := range rows {
				items[i] = timeout.ExpiredItem{ID: r.ID, RunID: r.RunID, ToolName: r.ToolName}
			}
			return items, nil
		},
		ClaimTimeout: func(ctx context.Context, id string, now string) (int64, error) {
			return store.Queries().UpdateFeedbackRequestStatus(ctx, db.UpdateFeedbackRequestStatusParams{
				Status:     "timed_out",
				Response:   nil,
				ResolvedAt: &now,
				ID:         id,
			})
		},
		WaitingRunStatus: model.RunStatusWaitingForFeedback,
		ErrorCode:        string(model.ErrorCodeFeedbackTimeout),
		ErrorMessage: func(toolName string) string {
			return fmt.Sprintf("feedback timeout: operator did not respond within the configured timeout for %s", toolName)
		},
		SSEEventName: "feedback.timed_out",
		SSEPayload: func(id, runID string) map[string]string {
			return map[string]string{
				"feedback_id": id,
				"run_id":      runID,
				"status":      "timed_out",
			}
		},
	}
	return timeout.NewScanner(store, interval, cfg, opts...)
}
