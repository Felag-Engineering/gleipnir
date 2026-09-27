package run

import (
	"context"

	"github.com/felag-engineering/gleipnir/internal/db"
)

// ToolInputServerNameForTest exposes RunsHandler.toolInputServerName so tests
// in external packages can exercise its fallback branch directly against an
// in-memory row, without needing to persist a tool_input_requests row whose
// server_id dangles — server_id is a CASCADE foreign key, so a genuinely
// dangling reference cannot be inserted in the first place.
func ToolInputServerNameForTest(h *RunsHandler, ctx context.Context, row db.ToolInputRequest) string {
	return h.toolInputServerName(ctx, row)
}

// MakeConcurrencyCheckErrorForTest wraps err in a concurrencyCheckError so
// tests in external packages can exercise IsConcurrencyCheckError without
// naming the unexported type.
func MakeConcurrencyCheckErrorForTest(err error) error {
	return &concurrencyCheckError{err: err}
}

// MakeEnqueueErrorForTest wraps err in an enqueueError so tests in external
// packages can exercise IsEnqueueError without naming the unexported type.
func MakeEnqueueErrorForTest(err error) error {
	return &enqueueError{err: err}
}

// DecodeApprovalOptionIDForTest exposes decodeApprovalOptionID so external
// tests can pin its strict approve/reject mapping directly.
func DecodeApprovalOptionIDForTest(optionID string) (bool, error) {
	return decodeApprovalOptionID(optionID)
}
