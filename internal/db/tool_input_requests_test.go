package db

import (
	"context"
	"testing"
	"time"
)

func createTestToolInputRequest(t *testing.T, s *Store, id, runID, serverID, status string) ToolInputRequest {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	req, err := s.Queries().CreateToolInputRequest(context.Background(), CreateToolInputRequestParams{
		ID:              id,
		RunID:           runID,
		ServerID:        serverID,
		ToolName:        "send_invoice",
		CallArgs:        `{"amount":100}`,
		RequestState:    "opaque-state",
		RequestPayload:  `{"message":"confirm?"}`,
		ElicitationKind: "permission",
		ExpiresAt:       now,
		CreatedAt:       now,
	})
	if err != nil {
		t.Fatalf("CreateToolInputRequest %s: %v", id, err)
	}
	if status == "pending" {
		return req
	}

	resolvedAt := time.Now().UTC().Format(time.RFC3339Nano)
	switch status {
	case "resolved":
		response := "approved"
		if _, err := s.Queries().ResolveToolInputRequest(context.Background(), ResolveToolInputRequestParams{
			Response:   &response,
			ResolvedAt: &resolvedAt,
			ID:         id,
		}); err != nil {
			t.Fatalf("ResolveToolInputRequest %s: %v", id, err)
		}
	case "timed_out":
		if _, err := s.Queries().ExpireToolInputRequest(context.Background(), ExpireToolInputRequestParams{
			ResolvedAt: &resolvedAt,
			ID:         id,
		}); err != nil {
			t.Fatalf("ExpireToolInputRequest %s: %v", id, err)
		}
	default:
		t.Fatalf("createTestToolInputRequest: unsupported status %q", status)
	}
	got, err := s.Queries().GetToolInputRequest(context.Background(), id)
	if err != nil {
		t.Fatalf("GetToolInputRequest %s: %v", id, err)
	}
	return got
}

func TestCreateAndGetToolInputRequest(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	insertRun(t, s, "r1", "p1", "running")
	insertMcpServer(t, s, "srv1")

	now := time.Now().UTC().Format(time.RFC3339Nano)
	created, err := s.Queries().CreateToolInputRequest(ctx, CreateToolInputRequestParams{
		ID:              "tir1",
		RunID:           "r1",
		ServerID:        "srv1",
		ToolName:        "send_invoice",
		CallArgs:        `{"amount":100}`,
		RequestState:    "opaque-state",
		RequestPayload:  `{"message":"confirm?"}`,
		ElicitationKind: "permission",
		ExpiresAt:       now,
		CreatedAt:       now,
	})
	if err != nil {
		t.Fatalf("CreateToolInputRequest: %v", err)
	}
	if created.Status != "pending" {
		t.Errorf("created status = %q, want pending", created.Status)
	}
	if created.ElicitationKind != "permission" {
		t.Errorf("created elicitation_kind = %q, want permission", created.ElicitationKind)
	}

	got, err := s.Queries().GetToolInputRequest(ctx, "tir1")
	if err != nil {
		t.Fatalf("GetToolInputRequest: %v", err)
	}
	if got.ToolName != "send_invoice" {
		t.Errorf("got tool_name = %q, want send_invoice", got.ToolName)
	}
	if got.RequestState != "opaque-state" {
		t.Errorf("got request_state = %q, want opaque-state", got.RequestState)
	}
	if got.Response != nil {
		t.Errorf("got response = %v, want nil", got.Response)
	}
}

func TestResolveToolInputRequest(t *testing.T) {
	ctx := context.Background()

	t.Run("pending request resolves and is no longer resumable", func(t *testing.T) {
		s := newTestStore(t)
		insertPolicy(t, s, "p1")
		insertRun(t, s, "r1", "p1", "running")
		insertMcpServer(t, s, "srv1")
		createTestToolInputRequest(t, s, "tir1", "r1", "srv1", "pending")

		response := "operator says yes"
		resolvedAt := time.Now().UTC().Format(time.RFC3339Nano)
		rows, err := s.Queries().ResolveToolInputRequest(ctx, ResolveToolInputRequestParams{
			Response:   &response,
			ResolvedAt: &resolvedAt,
			ID:         "tir1",
		})
		if err != nil {
			t.Fatalf("ResolveToolInputRequest: %v", err)
		}
		if rows != 1 {
			t.Fatalf("rows affected = %d, want 1", rows)
		}

		got, err := s.Queries().GetToolInputRequest(ctx, "tir1")
		if err != nil {
			t.Fatalf("GetToolInputRequest: %v", err)
		}
		if got.Status != "resolved" {
			t.Errorf("status = %q, want resolved", got.Status)
		}
		if got.Response == nil || *got.Response != response {
			t.Errorf("response = %v, want %q", got.Response, response)
		}

		resumable, err := s.Queries().ListResumableToolInputRequests(ctx)
		if err != nil {
			t.Fatalf("ListResumableToolInputRequests: %v", err)
		}
		if len(resumable) != 0 {
			t.Errorf("resumable count = %d, want 0 after resolve", len(resumable))
		}
	})

	t.Run("double resolve is a no-op on the second call", func(t *testing.T) {
		s := newTestStore(t)
		insertPolicy(t, s, "p1")
		insertRun(t, s, "r1", "p1", "running")
		insertMcpServer(t, s, "srv1")
		createTestToolInputRequest(t, s, "tir1", "r1", "srv1", "pending")

		response := "first"
		resolvedAt := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err := s.Queries().ResolveToolInputRequest(ctx, ResolveToolInputRequestParams{
			Response:   &response,
			ResolvedAt: &resolvedAt,
			ID:         "tir1",
		}); err != nil {
			t.Fatalf("first ResolveToolInputRequest: %v", err)
		}

		second := "second"
		rows, err := s.Queries().ResolveToolInputRequest(ctx, ResolveToolInputRequestParams{
			Response:   &second,
			ResolvedAt: &resolvedAt,
			ID:         "tir1",
		})
		if err != nil {
			t.Fatalf("second ResolveToolInputRequest: %v", err)
		}
		if rows != 0 {
			t.Errorf("rows affected on second resolve = %d, want 0", rows)
		}

		got, err := s.Queries().GetToolInputRequest(ctx, "tir1")
		if err != nil {
			t.Fatalf("GetToolInputRequest: %v", err)
		}
		if got.Response == nil || *got.Response != response {
			t.Errorf("response after double resolve = %v, want first write %q preserved", got.Response, response)
		}
	})
}

func TestExpireToolInputRequest(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	insertRun(t, s, "r1", "p1", "running")
	insertMcpServer(t, s, "srv1")
	createTestToolInputRequest(t, s, "tir1", "r1", "srv1", "pending")

	resolvedAt := time.Now().UTC().Format(time.RFC3339Nano)
	rows, err := s.Queries().ExpireToolInputRequest(ctx, ExpireToolInputRequestParams{
		ResolvedAt: &resolvedAt,
		ID:         "tir1",
	})
	if err != nil {
		t.Fatalf("ExpireToolInputRequest: %v", err)
	}
	if rows != 1 {
		t.Fatalf("rows affected = %d, want 1", rows)
	}

	got, err := s.Queries().GetToolInputRequest(ctx, "tir1")
	if err != nil {
		t.Fatalf("GetToolInputRequest: %v", err)
	}
	if got.Status != "timed_out" {
		t.Errorf("status = %q, want timed_out", got.Status)
	}

	// An already-resolved request cannot also be expired.
	rows, err = s.Queries().ExpireToolInputRequest(ctx, ExpireToolInputRequestParams{
		ResolvedAt: &resolvedAt,
		ID:         "tir1",
	})
	if err != nil {
		t.Fatalf("second ExpireToolInputRequest: %v", err)
	}
	if rows != 0 {
		t.Errorf("rows affected on already-timed-out request = %d, want 0", rows)
	}
}

func TestListResumableToolInputRequests(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	insertRun(t, s, "r1", "p1", "running")
	insertMcpServer(t, s, "srv1")

	createTestToolInputRequest(t, s, "tir-pending-1", "r1", "srv1", "pending")
	createTestToolInputRequest(t, s, "tir-pending-2", "r1", "srv1", "pending")
	createTestToolInputRequest(t, s, "tir-resolved", "r1", "srv1", "resolved")
	createTestToolInputRequest(t, s, "tir-timed-out", "r1", "srv1", "timed_out")

	resumable, err := s.Queries().ListResumableToolInputRequests(ctx)
	if err != nil {
		t.Fatalf("ListResumableToolInputRequests: %v", err)
	}
	if len(resumable) != 2 {
		t.Fatalf("resumable count = %d, want 2", len(resumable))
	}
	ids := map[string]bool{}
	for _, r := range resumable {
		ids[r.ID] = true
		if r.Status != "pending" {
			t.Errorf("resumable row %s has status %q, want pending", r.ID, r.Status)
		}
	}
	if !ids["tir-pending-1"] || !ids["tir-pending-2"] {
		t.Errorf("resumable ids = %v, want tir-pending-1 and tir-pending-2", ids)
	}
}

func TestToolInputRequestCascadesOnRunDelete(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	insertRun(t, s, "r1", "p1", "running")
	insertMcpServer(t, s, "srv1")
	createTestToolInputRequest(t, s, "tir1", "r1", "srv1", "pending")

	if _, err := s.DB().ExecContext(ctx, `DELETE FROM runs WHERE id = ?`, "r1"); err != nil {
		t.Fatalf("delete run: %v", err)
	}

	_, err := s.Queries().GetToolInputRequest(ctx, "tir1")
	if err == nil {
		t.Fatal("expected error fetching a tool_input_requests row whose run was deleted (ON DELETE CASCADE)")
	}
}
