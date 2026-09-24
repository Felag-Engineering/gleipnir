package agent

import (
	"context"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/infra/logctx"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/audience"
	"github.com/felag-engineering/gleipnir/internal/plugin/decision"
	"github.com/felag-engineering/gleipnir/internal/timeout"
)

// NewToolInputTimeoutDecisionHook builds the timeout.WithOnTerminated hook
// main.go wires onto the tool-input scanner (the same pattern the other
// scanners use their hooks for).
//
// It exists because Route's own timer branch only records a decision when
// THIS process's Route call wins the race against the scanner (operator_timeout.go's
// onWon). The scanner can also win — most notably the restart case: a host
// that restarted while a run was paused has no Route goroutine left at all,
// so the scanner's background claim is the ONLY settlement event that will
// ever happen for that row. Without this hook, a restart-timed-out request
// would settle with no decision record at all, leaving a gap in the audit
// trail for exactly the case an operator is most likely to go looking for one.
//
// The scanner's ExpiredItem carries only (ID, RunID, ToolName) — not the
// elicitation kind or the effective deadline — so this hook re-reads the row
// the CAS just won. That read is safe: the hook fires only after
// ClaimTimeout's conditional UPDATE reports rows==1, so the row is guaranteed
// to exist with every field this needs already written.
func NewToolInputTimeoutDecisionHook(queries *db.Queries) func(ctx context.Context, item timeout.ExpiredItem) {
	return func(ctx context.Context, item timeout.ExpiredItem) {
		row, err := queries.GetToolInputRequest(ctx, item.ID)
		if err != nil {
			logctx.Logger(ctx).WarnContext(ctx, "tool input: reading a scanner-timed-out request for its decision record failed",
				"request_id", item.ID, "run_id", item.RunID, "err", err)
			return
		}

		rec := decision.Record{
			RunID:            item.RunID,
			RequestID:        item.ID,
			Kind:             model.ElicitationKind(row.ElicitationKind),
			ToolName:         item.ToolName,
			ChannelEntryID:   audience.InAppEntryID,
			ChannelAssurance: decision.AssuranceOf(mcp.ChannelAssuranceAuthenticated),
			LinkMethod:       decision.LinkNone,
			Outcome:          decision.OutcomeTimeout,
		}
		if row.DeadlineSource != nil {
			rec.DeadlineSource = *row.DeadlineSource
		}
		if deadline, err := time.Parse(time.RFC3339Nano, row.ExpiresAt); err == nil {
			rec.EffectiveDeadline = deadline
		}

		if err := decision.NewRecorder(queries).Record(ctx, rec); err != nil {
			logctx.Logger(ctx).WarnContext(ctx, "tool input: writing a scanner-timeout decision record failed",
				"request_id", item.ID, "run_id", item.RunID, "err", err)
		}
	}
}
