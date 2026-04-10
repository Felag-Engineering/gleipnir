// Package approval provides the background timeout scanner for pending approval
// requests. When a run is waiting_for_approval and the approval window expires,
// the scanner marks the approval as timeout and the run as failed.
package approval

import (
	"context"
	"fmt"
	"time"

	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/timeout"
)

// ScannerOption is a functional option for the approval scanner.
type ScannerOption = timeout.ScannerOption

// WithPublisher sets an optional SSE publisher so the scanner can emit
// run.status_changed and approval.resolved events to connected clients.
var WithPublisher = timeout.WithPublisher

// NewScanner creates a Scanner that checks for expired approval requests on the
// given interval. Pass functional options to configure optional behaviour.
// The returned *timeout.Scanner exposes Start and Scan methods.
func NewScanner(store *db.Store, interval time.Duration, opts ...ScannerOption) *timeout.Scanner {
	cfg := timeout.Config{
		Name: "approval",
		ListExpired: func(ctx context.Context, cutoff string) ([]timeout.ExpiredItem, error) {
			rows, err := store.Queries().ListExpiredApprovalRequests(ctx, cutoff)
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
			return store.Queries().UpdateApprovalRequestStatus(ctx, db.UpdateApprovalRequestStatusParams{
				Status:    string(model.ApprovalStatusTimeout),
				DecidedAt: &now,
				Note:      nil,
				ID:        id,
			})
		},
		WaitingRunStatus: model.RunStatusWaitingForApproval,
		ErrorCode:        "approval_timeout",
		ErrorMessage: func(toolName string) string {
			return fmt.Sprintf("approval timeout: %s not approved within timeout window", toolName)
		},
		SSEEventName: "approval.resolved",
		SSEPayload: func(id, runID string) map[string]string {
			return map[string]string{
				"approval_id": id,
				"run_id":      runID,
				"status":      string(model.ApprovalStatusTimeout),
			}
		},
	}
	return timeout.NewScanner(store, interval, cfg, opts...)
}
