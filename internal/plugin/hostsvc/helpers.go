package hostsvc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felag-engineering/gleipnir/internal/db"
)

// resolveInstance resolves the calling plugin instance ID from the connection
// context and fetches the corresponding DB row. Returns Unauthenticated when
// the binder reports no identity, Internal on DB errors.
func (s *Server) resolveInstance(ctx context.Context) (db.PluginInstance, error) {
	iid, ok := s.binder.InstanceIDFromContext(ctx)
	if !ok {
		return db.PluginInstance{}, status.Error(codes.Unauthenticated, "no plugin instance identity on connection")
	}
	inst, err := s.q.GetPluginInstanceByID(ctx, iid)
	if err != nil {
		return db.PluginInstance{}, status.Errorf(codes.Internal, "fetch instance: %v", err)
	}
	return inst, nil
}

// latestStepNumber returns the step_number of the most recently inserted step
// for runID, or -1 when there are no steps (sql.ErrNoRows treated as 0 steps).
func (s *Server) latestStepNumber(ctx context.Context, runID string) (int64, error) {
	step, err := s.q.GetLatestRunStep(ctx, runID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// No steps yet — the next step to be inserted is step 0.
			return -1, nil
		}
		return 0, fmt.Errorf("get latest run step: %w", err)
	}
	return step.StepNumber, nil
}

// writeAuditEvent inserts a plugin_audit_events row. Non-fatal: logs at Warn
// on failure (mirrors the pattern in audit_guard.go).
func (s *Server) writeAuditEvent(ctx context.Context, iid, eventType, severity string, payload map[string]string) {
	p, err := json.Marshal(payload)
	if err != nil {
		p = []byte("{}")
	}
	_, insertErr := s.q.InsertPluginAuditEvent(ctx, db.InsertPluginAuditEventParams{
		PluginInstanceID: &iid,
		EventType:        eventType,
		Severity:         severity,
		PayloadJson:      string(p),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
	})
	if insertErr != nil {
		slog.WarnContext(ctx, "audit event insert failed",
			"event_type", eventType,
			"instance_id", iid,
			"err", insertErr,
		)
	}
}
