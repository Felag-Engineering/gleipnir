package hostsvc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/dispatch"
	commonv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/common/v1"
	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
)

// maxPayloadJSONBytes is the per-RPC hard cap on payload_json fields in
// WriteAuditStep and EmitEvent. 64 KiB is generous for a feedback_response or
// event payload; anything larger is a sign of misuse or a bug in the plugin.
const maxPayloadJSONBytes = 64 * 1024

// WriteAuditStep accepts only `feedback_response`. It MUST carry a request_id.
// Authorization is by request-ownership (spec §8.5 exemption): the request_id
// row's plugin_instance_id (plugin substrate path) or the policy's tool grants
// matching `<instance_name>.` (native path) must match the calling instance.
// The detached-context check does not apply — request-ownership is the §8.5
// alternative auth primitive.
//
// NATIVE PATH ORDERING: ownership is resolved BEFORE the fr.Status check so a
// non-owner cannot probe whether a request_id exists or what state it is in —
// nonexistent, foreign-pending, and foreign-resolved all collapse to a single
// codes.PermissionDenied "unauthorized_request_id".
func (s *Server) WriteAuditStep(ctx context.Context, req *hostv1.WriteAuditStepRequest) (*hostv1.WriteAuditStepResponse, error) {
	inst, err := s.resolveInstance(ctx)
	if err != nil {
		return nil, err
	}

	const rpcMethod = "/gleipnir.plugin.host.v1.HostService/WriteAuditStep"

	if len(req.GetPayloadJson()) > maxPayloadJSONBytes {
		return nil, status.Errorf(codes.InvalidArgument,
			"payload_json exceeds maximum size of %d bytes", maxPayloadJSONBytes)
	}

	if req.GetStepType() != "feedback_response" {
		s.writeAuditEvent(ctx, inst.ID, "unauthorized_step_type", "high", map[string]string{
			"rpc_method": rpcMethod,
			"step_type":  req.GetStepType(),
		})
		return nil, status.Errorf(codes.PermissionDenied,
			"step_type %q is not permitted in v1; only feedback_response is allowed", req.GetStepType())
	}

	if req.GetRequestId() == "" {
		return nil, status.Error(codes.InvalidArgument, "request_id must be set for feedback_response steps")
	}

	requestID := req.GetRequestId()
	payloadJSON := req.GetPayloadJson()

	// Plugin-substrate path: look up plugin_pending_requests first. On
	// sql.ErrNoRows, fall through to the native feedback_requests path below.
	pendingReq, perr := s.q.GetPluginPendingRequest(ctx, requestID)
	if perr == nil {
		// Row exists — this is a plugin-substrate feedback_response.
		if pendingReq.PluginInstanceID != inst.ID {
			// Wrong instance: ownership violation.
			s.writeAuditEvent(ctx, inst.ID, EventTypeUnauthorizedRequestID, "high", map[string]string{
				"request_id": requestID,
				"run_id":     pendingReq.RunID,
				"rpc_method": rpcMethod,
			})
			return nil, status.Error(codes.PermissionDenied, "unauthorized_request_id")
		}

		if s.channels == nil {
			// Resolver not wired — treat as late callback to avoid silent drop.
			slog.WarnContext(ctx, "plugin channel resolver not wired; treating as late callback",
				"request_id", requestID,
			)
			s.writeAuditEvent(ctx, inst.ID, EventTypeFeedbackResponseLate, "warning", map[string]string{
				"request_id": requestID,
				"reason":     "resolver_unwired",
				"substrate":  "plugin",
				"status":     pendingReq.Status,
			})
			return &hostv1.WriteAuditStepResponse{
				Ok: false,
				Error: &commonv1.ErrorEnvelope{
					Code:    commonv1.ErrorCode_ERROR_CODE_INVALID_ARG,
					Message: EventTypeFeedbackResponseLate,
				},
			}, nil
		}

		resolved, rerr := s.channels.Resolve(ctx, requestID, payloadJSON)
		if rerr != nil && !errors.Is(rerr, dispatch.ErrUnknownRequestID) {
			return nil, status.Errorf(codes.Internal, "resolve plugin request: %v", rerr)
		}

		if !resolved {
			reason := "late"
			if errors.Is(rerr, dispatch.ErrUnknownRequestID) {
				// The in-memory waiter was evicted (server restart or scanner race)
				// but the DB row exists, so the request is no longer actionable.
				reason = "evicted_waiter"
			}
			s.writeAuditEvent(ctx, inst.ID, EventTypeFeedbackResponseLate, "warning", map[string]string{
				"request_id": requestID,
				"reason":     reason,
				"substrate":  "plugin",
				"status":     pendingReq.Status,
			})
			return &hostv1.WriteAuditStepResponse{
				Ok: false,
				Error: &commonv1.ErrorEnvelope{
					Code:    commonv1.ErrorCode_ERROR_CODE_INVALID_ARG,
					Message: EventTypeFeedbackResponseLate,
				},
			}, nil
		}

		// CAS won: publish SSE event and return ok=true. The agent loop's
		// Dispatcher.Wait writes its own audit step; we do not duplicate it here.
		if eventData, marshalErr := json.Marshal(map[string]string{
			"run_id":      pendingReq.RunID,
			"request_id":  requestID,
			"instance_id": inst.ID,
			"step_type":   "feedback_response",
		}); marshalErr == nil {
			s.publisher.Publish("plugin.feedback_response_written", eventData)
		}
		return &hostv1.WriteAuditStepResponse{Ok: true}, nil
	}
	if !errors.Is(perr, sql.ErrNoRows) {
		return nil, status.Errorf(codes.Internal, "get plugin pending request: %v", perr)
	}
	// sql.ErrNoRows: not a plugin-substrate request — fall through to native path.

	// Look up the feedback request. Ownership is resolved before the status
	// check so a non-owner cannot probe whether a request_id exists or what
	// state it is in (existence oracle / state oracle closure).
	fr, err := s.q.GetFeedbackRequest(ctx, requestID)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.Internal, "get feedback request: %v", err)
		}
		// Unknown request_id: ownership is undeterminable — deny by default
		// (ADR-001). Return the same codes.PermissionDenied a foreign request
		// would get so the response is identical and the request_id existence is
		// not revealed.
		//
		// No audit event is written here: there is no run_id to attribute it to,
		// and writing a high-severity event on every probe of a nonexistent id
		// would flood plugin_audit_events. The foreign-but-existing case below
		// DOES write an audit event — that asymmetry is intentional and must be
		// preserved (do not add an unconditional audit write to this branch).
		return nil, status.Error(codes.PermissionDenied, "unauthorized_request_id")
	}

	// Verify ownership BEFORE the status check so a non-owner cannot distinguish
	// a pending request from a resolved one. This is the heuristic available
	// today; audience-based routing (audiences.plugin_instance per spec §4.2/§6)
	// is a follow-up — no audiences DB schema exists yet.
	run, err := s.q.GetRun(ctx, fr.RunID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get run for request_id scope check: %v", err)
	}
	policy, err := s.q.GetPolicy(ctx, run.PolicyID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get policy for request_id scope check: %v", err)
	}
	if !policyGrantsInstance(policy.Yaml, inst.InstanceName) {
		s.writeAuditEvent(ctx, inst.ID, EventTypeUnauthorizedRequestID, "high", map[string]string{
			"request_id": requestID,
			"run_id":     fr.RunID,
			"rpc_method": rpcMethod,
		})
		return nil, status.Error(codes.PermissionDenied, "unauthorized_request_id")
	}

	// Ownership confirmed. Now check whether the request is still actionable.
	if fr.Status != "pending" {
		// Spec §4.2 line 106: record the late attempt and return ok=false.
		s.writeAuditEvent(ctx, inst.ID, EventTypeFeedbackResponseLate, "warning", map[string]string{
			"request_id": requestID,
			"status":     fr.Status,
		})
		return &hostv1.WriteAuditStepResponse{
			Ok: false,
			Error: &commonv1.ErrorEnvelope{
				Code:    commonv1.ErrorCode_ERROR_CODE_INVALID_ARG,
				Message: EventTypeFeedbackResponseLate,
			},
		}, nil
	}

	// Insert the run step and resolve the feedback request atomically.
	// Without a transaction, two concurrent writers can observe the same
	// MAX(step_number) and attempt to insert with the same (run_id, step_number),
	// violating the unique constraint (fixes #348, ADR-038 discipline).
	resolved, err := s.writeFeedbackResponseStep(ctx, fr.RunID, requestID, payloadJSON)
	if err != nil {
		return nil, err
	}
	if !resolved {
		// The guarded CAS affected 0 rows: the request was resolved by another
		// writer in the residual window between the fr.Status pre-check above and
		// the commit. The transaction has been rolled back, so NO feedback_response
		// step was committed — without this guard we would write a spurious audit
		// step with no corresponding state change (#496, residual of #348).
		s.writeAuditEvent(ctx, inst.ID, EventTypeFeedbackResponseLate, "warning", map[string]string{
			"request_id": requestID,
			"reason":     "cas_lost",
			"status":     fr.Status,
		})
		return &hostv1.WriteAuditStepResponse{
			Ok: false,
			Error: &commonv1.ErrorEnvelope{
				Code:    commonv1.ErrorCode_ERROR_CODE_INVALID_ARG,
				Message: EventTypeFeedbackResponseLate,
			},
		}, nil
	}

	// Publish so SSE subscribers and tests can observe the step.
	if eventData, marshalErr := json.Marshal(map[string]string{
		"run_id":      fr.RunID,
		"request_id":  requestID,
		"instance_id": inst.ID,
		"step_type":   "feedback_response",
	}); marshalErr == nil {
		s.publisher.Publish("plugin.feedback_response_written", eventData)
	}

	return &hostv1.WriteAuditStepResponse{Ok: true}, nil
}

// writeFeedbackResponseStep inserts a feedback_response run_step and resolves
// the feedback_request atomically inside a single transaction. This prevents
// the TOCTOU race where two concurrent writers observe the same MAX(step_number)
// and attempt to insert with the same (run_id, step_number) value (fixes #348).
//
// The returned bool reports whether the guarded CAS
// (UpdateFeedbackRequestStatus, WHERE status='pending') actually transitioned
// the request. When it affects 0 rows another writer resolved the request in
// the residual window after the caller's fr.Status pre-check, so the step
// INSERT is rolled back (never committed) and (false, nil) is returned — the
// caller then emits the late-callback ok=false envelope. This closes the
// orphan-step window left open by #348 (#496).
//
// When s.sqlDB is nil (unit tests using a fake Querier), the operations run
// without a transaction — acceptable because fake Queriers are single-threaded
// and the race only manifests against a real SQLite connection pool. The
// rows-affected guard is still honored so the contract is uniform across paths.
func (s *Server) writeFeedbackResponseStep(ctx context.Context, runID, requestID, payloadJSON string) (resolved bool, err error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	stepID := model.NewULID()

	if s.sqlDB == nil {
		// Test path: no real DB, run without transaction. Resolve the request
		// FIRST so a 0-row CAS short-circuits before the step INSERT — there is no
		// transaction to roll back here, so we must not write the step on CAS loss.
		rows, err := s.q.UpdateFeedbackRequestStatus(ctx, db.UpdateFeedbackRequestStatusParams{
			Status:     "resolved",
			Response:   &payloadJSON,
			ResolvedAt: &now,
			ID:         requestID,
		})
		if err != nil {
			return false, status.Errorf(codes.Internal, "update feedback request status: %v", err)
		}
		if rows == 0 {
			return false, nil
		}
		latestStep, err := s.latestStepNumber(ctx, runID)
		if err != nil {
			return false, status.Errorf(codes.Internal, "resolve step index: %v", err)
		}
		_, err = s.q.CreateRunStep(ctx, db.CreateRunStepParams{
			ID:         stepID,
			RunID:      runID,
			StepNumber: latestStep + 1,
			Type:       "feedback_response",
			Content:    payloadJSON,
			TokenCost:  0,
			CreatedAt:  now,
		})
		if err != nil {
			return false, status.Errorf(codes.Internal, "create run step: %v", err)
		}
		return true, nil
	}

	tx, err := s.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return false, status.Errorf(codes.Internal, "begin feedback response tx: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck — Rollback is a no-op after Commit

	qtx := db.New(tx)

	latestStep, err := qtx.GetLatestRunStep(ctx, runID)
	var nextStep int64
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return false, status.Errorf(codes.Internal, "resolve step index in tx: %v", err)
		}
		// No steps yet — first step is 0.
		nextStep = 0
	} else {
		nextStep = latestStep.StepNumber + 1
	}

	_, err = qtx.CreateRunStep(ctx, db.CreateRunStepParams{
		ID:         stepID,
		RunID:      runID,
		StepNumber: nextStep,
		Type:       "feedback_response",
		Content:    payloadJSON,
		TokenCost:  0,
		CreatedAt:  now,
	})
	if err != nil {
		return false, status.Errorf(codes.Internal, "create run step in tx: %v", err)
	}

	rows, err := qtx.UpdateFeedbackRequestStatus(ctx, db.UpdateFeedbackRequestStatusParams{
		Status:     "resolved",
		Response:   &payloadJSON,
		ResolvedAt: &now,
		ID:         requestID,
	})
	if err != nil {
		return false, status.Errorf(codes.Internal, "update feedback request status in tx: %v", err)
	}
	if rows == 0 {
		// Guarded CAS lost: the request was resolved concurrently after the
		// caller's pre-check. Return without committing so the deferred Rollback
		// discards the feedback_response step we just staged (#496). Returning
		// (false, nil) — not an error — lets the caller emit the late envelope.
		return false, nil
	}

	if err := tx.Commit(); err != nil {
		return false, status.Errorf(codes.Internal, "commit feedback response tx: %v", err)
	}
	return true, nil
}
