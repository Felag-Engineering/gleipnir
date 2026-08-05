package run

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/felag-engineering/gleipnir/internal/http/httputil"
	"github.com/felag-engineering/gleipnir/internal/plugin/decision"
	"github.com/go-chi/chi/v5"
)

// DecisionSummary is one tool-initiated HITL decision record as the API renders
// it (ADR-055, mcp-realignment-spec.md §6.6).
//
// It is a separate endpoint from /steps rather than extra entries in it, and
// that is the ADR-046 split showing through the API: /steps is the trace the
// model is replayed, and merging oversight evidence into it would make the
// wire shape disagree with what the agent actually saw. A client that wants a
// combined timeline interleaves the two by timestamp, which is a presentation
// decision and belongs on the presentation side.
type DecisionSummary struct {
	RunID     string `json:"run_id"`
	RequestID string `json:"request_id"`

	// Type is the record type — `tool_permission_request` or
	// `tool_input_request`. Deliberately NOT a run-step type; see the decision
	// package doc.
	Type     string `json:"type"`
	Kind     string `json:"kind"`
	Severity string `json:"severity"`
	ToolName string `json:"tool_name,omitempty"`

	// Channel identifies who was asked and how strongly that channel knows the
	// person it names.
	ChannelEntryID    string `json:"channel_entry_id"`
	ChannelInstanceID string `json:"channel_instance_id,omitempty"`
	ChannelAssurance  string `json:"channel_assurance"`

	// Actor is the channel's claim; LinkVerified says whether the host could
	// tie it to a Gleipnir user. A renderer that shows the ID without the flag
	// would present hearsay as identity.
	ActorExternalID string `json:"actor_external_id,omitempty"`
	ActorUserID     string `json:"actor_user_id,omitempty"`
	LinkMethod      string `json:"link_method"`
	LinkVerified    bool   `json:"link_verified"`

	// EffectiveDeadline is the minimum of every clock that governed the wait
	// (spec §6.3), and DeadlineSource names which one won.
	EffectiveDeadline string `json:"effective_deadline,omitempty"`
	DeadlineSource    string `json:"deadline_source,omitempty"`

	Outcome string `json:"outcome"`

	// Considered is every audience entry passed over before the chosen one.
	// "The third channel answered" only means something alongside why the
	// first two did not.
	Considered []decision.Candidate `json:"considered,omitempty"`

	DecidedAt string `json:"decided_at"`
}

// ListDecisions handles GET /api/v1/runs/{runID}/decisions.
//
// Read access matches the rest of the run detail surface. There is no write
// side: a decision record is written by the settlement path that produced it,
// and an endpoint that let anyone add one would make the whole table
// assertions rather than evidence.
func (h *RunsHandler) ListDecisions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	runID := chi.URLParam(r, "runID")

	// Same guard ListSteps uses: an empty list for a run that does not exist
	// would answer "no decisions" to a question about nothing.
	if _, err := h.store.GetRun(ctx, runID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			httputil.WriteError(w, http.StatusNotFound, "run not found", "")
			return
		}
		slog.Error("GetRun query failed", "run_id", runID, "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal server error", "")
		return
	}

	records, err := decision.NewRecorder(h.store.Queries()).ForRun(ctx, runID)
	if err != nil {
		slog.Error("listing run decision records failed", "run_id", runID, "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal server error", "")
		return
	}

	result := make([]DecisionSummary, 0, len(records))
	for _, rec := range records {
		result = append(result, toDecisionSummary(rec))
	}
	httputil.WriteJSON(w, http.StatusOK, result)
}

func toDecisionSummary(rec decision.Record) DecisionSummary {
	summary := DecisionSummary{
		RunID:             rec.RunID,
		RequestID:         rec.RequestID,
		Type:              string(rec.Type()),
		Kind:              string(rec.Kind),
		Severity:          rec.Severity(),
		ToolName:          rec.ToolName,
		ChannelEntryID:    rec.ChannelEntryID,
		ChannelInstanceID: rec.ChannelInstance,
		ChannelAssurance:  rec.ChannelAssurance,
		ActorExternalID:   rec.ActorExternalID,
		ActorUserID:       rec.ActorUserID,
		LinkMethod:        string(rec.LinkMethod),
		LinkVerified:      rec.LinkMethod.Verified(),
		DeadlineSource:    rec.DeadlineSource,
		Outcome:           string(rec.Outcome),
		Considered:        rec.Considered,
		DecidedAt:         rec.DecidedAt.UTC().Format(time.RFC3339Nano),
	}
	if !rec.EffectiveDeadline.IsZero() {
		summary.EffectiveDeadline = rec.EffectiveDeadline.UTC().Format(time.RFC3339Nano)
	}
	return summary
}
